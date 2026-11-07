package settingsstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"sync"
	"testing"

	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/recovery"
	_ "github.com/mattn/go-sqlite3"
)

const initialSecurity = "12345678-1234-4123-8123-123456789012"

var uuidPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

type configRow struct{ Key, Value string }
type fixture struct {
	Name                                    string
	Previous                                json.RawMessage
	RawInput                                string
	Revisions                               map[string]string
	FailSettingsWrite, AbsentSecuritySchema bool
	Error                                   string
	Output                                  []configRow
	Events                                  []string
	Security                                *string
	AckSequence                             int64
	MinimumReader                           int
}

func fixtureStore(t *testing.T, security bool) *Store {
	t.Helper()
	root := t.TempDir()
	path := filepath.Join(root, "authority")
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	ledger, err := sql.Open("sqlite3", filepath.Join(path, "authority.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	_, err = ledger.Exec(`CREATE TABLE identity(id TEXT PRIMARY KEY,format INTEGER NOT NULL); INSERT INTO identity VALUES('12345678-1234-4123-8123-123456789012',1); CREATE TABLE revocations(sequence INTEGER PRIMARY KEY,previous_hash TEXT NOT NULL,payload TEXT NOT NULL,hash TEXT NOT NULL)`)
	if err != nil {
		t.Fatal(err)
	}
	if err = ledger.Close(); err != nil {
		t.Fatal(err)
	}
	if err = os.Mkdir(filepath.Join(root, "data"), 0700); err != nil {
		t.Fatal(err)
	}
	main, err := sql.Open("sqlite3", filepath.Join(root, "data", "main.sqlite")+"?_busy_timeout=5000")
	if err != nil {
		t.Fatal(err)
	}
	_, err = main.Exec(`PRAGMA journal_mode=WAL; CREATE TABLE recovery_gate(id INTEGER PRIMARY KEY,value TEXT NOT NULL); CREATE TABLE config(key TEXT PRIMARY KEY,value TEXT NOT NULL)`)
	if err != nil {
		t.Fatal(err)
	}
	if security {
		_, err = main.Exec(`CREATE TABLE IF NOT EXISTS recovery_security_state (id INTEGER PRIMARY KEY CHECK(id=1), revision TEXT NOT NULL); INSERT INTO recovery_security_state VALUES(1,'12345678-1234-4123-8123-123456789012')`)
		if err != nil {
			t.Fatal(err)
		}
	}
	gate, _ := json.Marshal(recovery.Gate{Head: recovery.Head{InstanceID: initialSecurity, Hash: strings.Repeat("0", 64)}, Authority: path, MinimumReader: 1, State: "ready"})
	if _, err = main.Exec("INSERT INTO recovery_gate VALUES(1,?)", string(gate)); err != nil {
		t.Fatal(err)
	}
	authority, err := recovery.OpenWriter(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { authority.Close(); main.Close() })
	return &Store{main, authority}
}
func parsed(t *testing.T, raw string) any {
	t.Helper()
	v, err := jsjson.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	return v
}
func configRows(t *testing.T, s *Store) []configRow {
	t.Helper()
	rows, err := s.Main.Query("SELECT key,value FROM config ORDER BY key")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	out := []configRow{}
	for rows.Next() {
		var r configRow
		if err = rows.Scan(&r.Key, &r.Value); err != nil {
			t.Fatal(err)
		}
		out = append(out, r)
	}
	if err = rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}
func authorityEvents(t *testing.T, s *Store) ([]string, string) {
	t.Helper()
	gate, err := recovery.ReadGate(context.Background(), s.Main)
	if err != nil {
		t.Fatal(err)
	}
	ledger, err := sql.Open("sqlite3", filepath.Join(gate.Authority, "authority.sqlite")+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer ledger.Close()
	rows, err := ledger.Query("SELECT payload FROM revocations ORDER BY sequence")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	out := []string{}
	lastRevision := ""
	for rows.Next() {
		var raw string
		if err = rows.Scan(&raw); err != nil {
			t.Fatal(err)
		}
		value := parsed(t, raw).(*jsjson.Object)
		if property(value, "kind") == "security_state" {
			revision, ok := property(value, "revision").(string)
			if !ok || !uuidPattern.MatchString(revision) {
				t.Fatal("invalid UUID", raw)
			}
			lastRevision = revision
			_ = value.Set("revision", "<uuid-v4>")
			raw, err = jsjson.Stringify(value)
			if err != nil {
				t.Fatal(err)
			}
		}
		out = append(out, raw)
	}
	if err = rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out, lastRevision
}

func TestActualStoreSettingsWriteOracle(t *testing.T) {
	raw, err := os.ReadFile("testdata/write.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixtures struct{ Cases []fixture }
	if err = json.Unmarshal(raw, &fixtures); err != nil {
		t.Fatal(err)
	}
	for _, entry := range fixtures.Cases {
		t.Run(entry.Name, func(t *testing.T) {
			s := fixtureStore(t, !entry.AbsentSecuritySchema)
			if string(entry.Previous) != "null" {
				previous := parsed(t, string(entry.Previous))
				encoded, err := jsjson.Stringify(previous)
				if err != nil {
					t.Fatal(err)
				}
				if _, err = s.Main.Exec("INSERT INTO config(key,value) VALUES('settings',?)", encoded); err != nil {
					t.Fatal(err)
				}
			}
			for key, value := range entry.Revisions {
				if _, err = s.Main.Exec("INSERT INTO config(key,value) VALUES(?,?)", key, value); err != nil {
					t.Fatal(err)
				}
			}
			if entry.FailSettingsWrite {
				_, err = s.Main.Exec("CREATE TRIGGER fixture_fail_settings BEFORE INSERT ON config WHEN NEW.key='settings' BEGIN SELECT RAISE(ABORT,'fixture_settings_write_failed'); END")
				if err != nil {
					t.Fatal(err)
				}
			}
			input := parsed(t, entry.RawInput)
			before, _ := jsjson.Stringify(input)
			_, err = s.Set(context.Background(), input)
			after, _ := jsjson.Stringify(input)
			if before != after {
				t.Fatal("input mutated")
			}
			if entry.AbsentSecuritySchema {
				// The oracle establishes that TS creates this table. The candidate reports
				// its bootstrap gap explicitly, before authority or main changes.
				if !errors.Is(err, ErrSecuritySchemaUnsupported) {
					t.Fatal("expected explicit enrollment gap", err)
				}
				events, _ := authorityEvents(t, s)
				if len(events) != 0 {
					t.Fatal("unsupported enrollment appended intent")
				}
				if got := configRows(t, s); !reflect.DeepEqual(got, []configRow{{"settings", "{}"}}) {
					t.Fatal(got)
				}
				return
			}
			if entry.Error != "" {
				if err == nil || err.Error() != entry.Error {
					t.Fatalf("error got %v\nwant %s", err, entry.Error)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			if got := configRows(t, s); !reflect.DeepEqual(got, entry.Output) {
				t.Fatalf("main got %+v\nwant %+v", got, entry.Output)
			}
			events, lastRevision := authorityEvents(t, s)
			if !reflect.DeepEqual(events, entry.Events) {
				t.Fatalf("events got %v\nwant %v", events, entry.Events)
			}
			var security string
			if err = s.Main.QueryRow("SELECT revision FROM recovery_security_state WHERE id=1").Scan(&security); err != nil {
				t.Fatal(err)
			}
			if security != initialSecurity {
				if security != lastRevision {
					t.Fatal("main security row does not ACK exact authority UUID")
				}
				security = "<uuid-v4>"
			}
			if entry.Security == nil || security != *entry.Security {
				t.Fatalf("security got %s want %v", security, entry.Security)
			}
			gate, err := recovery.ReadGate(context.Background(), s.Main)
			if err != nil || gate.Head.Sequence != entry.AckSequence || gate.MinimumReader != entry.MinimumReader {
				t.Fatal(gate, err)
			}
			head, err := s.Authority.Head(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if head.Sequence != int64(len(events)) {
				t.Fatal(head, events)
			}
			if entry.Error == "" && head != gate.Head {
				t.Fatal("main ACK does not match authority", head, gate)
			}
		})
	}
}

func TestConcurrentSettingsRevisions(t *testing.T) {
	s := fixtureStore(t, true)
	const count = 12
	var wg sync.WaitGroup
	failures := make(chan error, count)
	for i := 0; i < count; i++ {
		input := parsed(t, fmt.Sprintf(`{"memoryKnowledgeEnabled":"change-%d","budgetDailyUsd":%d}`, i, i+2))
		wg.Add(1)
		go func() { defer wg.Done(); _, err := s.Set(context.Background(), input); failures <- err }()
	}
	wg.Wait()
	close(failures)
	for err := range failures {
		if err != nil {
			t.Fatal(err)
		}
	}
	var revision string
	if err := s.Main.QueryRow("SELECT value FROM config WHERE key='memory-knowledge-policy-revision'").Scan(&revision); err != nil {
		t.Fatal(err)
	}
	if revision != "12" {
		t.Fatal("lost revision", revision)
	}
	events, last := authorityEvents(t, s)
	if len(events) != count {
		t.Fatal(events)
	}
	var security string
	if err := s.Main.QueryRow("SELECT revision FROM recovery_security_state").Scan(&security); err != nil {
		t.Fatal(err)
	}
	if security != last {
		t.Fatal("lost security update")
	}
	gate, err := recovery.ReadGate(context.Background(), s.Main)
	if err != nil || gate.Head.Sequence != count {
		t.Fatal(gate, err)
	}
}

func TestDurableIntentFailureDoesNotRetryOrAcknowledge(t *testing.T) {
	s := fixtureStore(t, true)
	input := parsed(t, `{"memoryKnowledgeEnabled":true,"budgetDailyUsd":0}`)
	if _, err := s.Set(context.Background(), input); err == nil {
		t.Fatal("invalid budget accepted")
	}
	events, _ := authorityEvents(t, s)
	if len(events) != 1 || len(configRows(t, s)) != 0 {
		t.Fatal("intent/rollback mismatch", events)
	}
	// A caller's later write cannot silently acknowledge/replay the failed intent.
	if _, err := s.Set(context.Background(), parsed(t, `{}`)); err == nil || !strings.Contains(err.Error(), "recovery_authority_head_changed") {
		t.Fatal(err)
	}
	after, _ := authorityEvents(t, s)
	if !reflect.DeepEqual(events, after) || len(configRows(t, s)) != 0 {
		t.Fatal("write retried or acknowledged stale intent")
	}
	gate, err := recovery.ReadGate(context.Background(), s.Main)
	if err != nil || gate.Head.Sequence != 0 {
		t.Fatal(gate, err)
	}
}

func TestUnsupportedSecuritySchemaFailsBeforeAnyIntent(t *testing.T) {
	for _, schema := range []string{"", `CREATE TABLE recovery_security_state(id INTEGER PRIMARY KEY,revision TEXT)`} {
		t.Run(schema, func(t *testing.T) {
			s := fixtureStore(t, false)
			if schema != "" {
				if _, err := s.Main.Exec(schema); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := s.Set(context.Background(), parsed(t, `{"memoryKnowledgeEnabled":true,"budgetDailyUsd":2}`)); !errors.Is(err, ErrSecuritySchemaUnsupported) {
				t.Fatal(err)
			}
			events, _ := authorityEvents(t, s)
			if len(events) != 0 || len(configRows(t, s)) != 0 {
				t.Fatal("unsupported schema changed state", events)
			}
		})
	}
	// Non-security writes do not require a table that the source creates lazily.
	s := fixtureStore(t, false)
	if _, err := s.Set(context.Background(), parsed(t, `{"memoryKnowledgeEnabled":true}`)); err != nil {
		t.Fatal(err)
	}
}

func TestSameValuesDoNotRotateSecurityOrAppendIntents(t *testing.T) {
	s := fixtureStore(t, true)
	input := parsed(t, `{"memoryKnowledgeEnabled":true,"budgetDailyUsd":2,"text":"\ud800"}`)
	if _, err := s.Set(context.Background(), input); err != nil {
		t.Fatal(err)
	}
	events, revision := authorityEvents(t, s)
	before := configRows(t, s)
	for i := 0; i < 3; i++ {
		if _, err := s.Set(context.Background(), input); err != nil {
			t.Fatal(err)
		}
	}
	afterEvents, afterRevision := authorityEvents(t, s)
	if !reflect.DeepEqual(before, configRows(t, s)) || !reflect.DeepEqual(events, afterEvents) || revision != afterRevision {
		t.Fatal("same-value write changed intent or policy")
	}
}

func TestReturningToEarlierFlagsAppendsNewIntent(t *testing.T) {
	s := fixtureStore(t, true)
	for _, raw := range []string{`{"cacheEnabled":false}`, `{}`, `{"cacheEnabled":false}`} {
		if _, err := s.Set(context.Background(), parsed(t, raw)); err != nil {
			t.Fatal(err)
		}
	}
	events, _ := authorityEvents(t, s)
	if len(events) != 3 || events[0] != events[2] || events[0] == events[1] {
		t.Fatal("configuration transitions must not globally deduplicate", events)
	}
}
