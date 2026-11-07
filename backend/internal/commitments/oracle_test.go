package commitments

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsdate"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/recovery"
	_ "github.com/mattn/go-sqlite3"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type fixture struct {
	main       *sql.DB
	ledger     *recovery.Authority
	external   *sql.DB
	store      *Store
	service    *PrivacyService
	events     []any
	fault, now string
}

func must(t *testing.T, e error) {
	t.Helper()
	if e != nil {
		t.Fatal(e)
	}
}
func parse(t *testing.T, raw string) any { t.Helper(); v, e := jsjson.Parse(raw); must(t, e); return v }
func stringify(t *testing.T, v any) string {
	t.Helper()
	raw, e := jsjson.Stringify(v)
	must(t, e)
	return raw
}
func setup(t *testing.T) *fixture {
	t.Helper()
	ctx := context.Background()
	root := t.TempDir()
	path := filepath.Join(root, "authority")
	must(t, os.Mkdir(path, 0700))
	external, e := sql.Open("sqlite3", filepath.Join(path, "authority.sqlite"))
	must(t, e)
	_, e = external.Exec(`CREATE TABLE identity(id TEXT PRIMARY KEY,format INTEGER NOT NULL);CREATE TABLE revocations(sequence INTEGER PRIMARY KEY,previous_hash TEXT NOT NULL,payload TEXT NOT NULL,hash TEXT NOT NULL);INSERT INTO identity VALUES('12345678-1234-4123-8123-123456789012',1)`)
	must(t, e)
	must(t, os.Mkdir(filepath.Join(root, "live"), 0700))
	main, e := sql.Open("sqlite3", filepath.Join(root, "live", "main.sqlite"))
	must(t, e)
	main.SetMaxOpenConns(1)
	_, e = main.Exec(`PRAGMA journal_mode=WAL;CREATE TABLE recovery_gate(id INTEGER PRIMARY KEY CHECK(id=1),value TEXT NOT NULL)`)
	must(t, e)
	raw := stringify(t, obj("head", obj("instanceId", "12345678-1234-4123-8123-123456789012", "sequence", float64(0), "hash", strings.Repeat("0", 64)), "authority", path, "minimumReader", float64(1), "state", "ready"))
	_, e = main.Exec("INSERT INTO recovery_gate VALUES(1,?)", raw)
	must(t, e)
	ledger, e := recovery.OpenWriter(ctx, path)
	must(t, e)
	parser, e := jsdate.New("UTC")
	must(t, e)
	f := &fixture{main: main, external: external, ledger: ledger, events: []any{}, now: "2026-10-28T13:00:00.000Z"}
	f.store, e = New(main, ledger, Options{DateParser: parser, Compare: sourceCompare(t), Now: func() string { return f.now }})
	must(t, e)
	f.service, e = NewPrivacyService(f.store, Owners{AfterWrite: func(_ *Store, h *Object, next, prior []any) error {
		f.events = append(f.events, []any{"afterWrite", h, next, prior})
		if f.fault == "afterWrite" {
			return errors.New("synthetic_afterWrite")
		}
		return nil
	}})
	must(t, e)
	t.Cleanup(func() { ledger.Close(); external.Close(); main.Close() })
	return f
}
func loadOracle(t *testing.T) *Object {
	t.Helper()
	raw, e := os.ReadFile("testdata/source-oracle.json")
	must(t, e)
	return object(parse(t, string(raw)))
}

var tableNames = []string{"memory_commitment_commands", "memory_commitment_heads", "memory_commitment_revisions", "memory_commitment_temporal_windows", "memory_commitment_transitions"}

func seedRows(t *testing.T, f *fixture, initial *Object) {
	t.Helper()
	must(t, f.store.Initialize(context.Background()))
	if has(initial, "memory_commitment_transitions") {
		must(t, f.store.Transaction(context.Background(), func(s *Store) error { return s.session.EnsureCommitmentTemporalSchema() }))
	}
	for _, name := range tableNames {
		for _, v := range arr(get(initial, name)) {
			row := object(v)
			columns := row.Keys()
			args := []any{}
			marks := []string{}
			for _, key := range columns {
				args = append(args, get(row, key))
				marks = append(marks, "?")
			}
			_, e := f.main.Exec("INSERT INTO "+name+" ("+strings.Join(columns, ",")+") VALUES("+strings.Join(marks, ",")+")", args...)
			must(t, e)
		}
	}
}
func rowsValue(t *testing.T, db *sql.DB, q string, args ...any) []any {
	t.Helper()
	rows, e := db.Query(q, args...)
	must(t, e)
	defer rows.Close()
	columns, e := rows.Columns()
	must(t, e)
	out := []any{}
	for rows.Next() {
		values := make([]any, len(columns))
		dest := make([]any, len(columns))
		for i := range values {
			dest[i] = &values[i]
		}
		must(t, rows.Scan(dest...))
		o := new(Object)
		for i, k := range columns {
			value := values[i]
			if n, ok := value.(int64); ok {
				value = float64(n)
			}
			_ = o.Set(k, value)
		}
		out = append(out, o)
	}
	must(t, rows.Err())
	return out
}
func tableValue(t *testing.T, f *fixture) *Object {
	t.Helper()
	out := new(Object)
	for _, name := range tableNames {
		var exists bool
		must(t, f.main.QueryRow("SELECT EXISTS(SELECT 1 FROM sqlite_master WHERE type='table' AND name=?)", name).Scan(&exists))
		if exists {
			_ = out.Set(name, rowsValue(t, f.main, "SELECT * FROM "+name+" ORDER BY 1,2"))
		}
	}
	return out
}
func includes(values any, value any) bool {
	for _, v := range arr(values) {
		if deepEqual(v, value) {
			return true
		}
	}
	return false
}
func (f *fixture) action(op *Object, s *PrivacyService) error {
	scene := str(get(op, "scene"))
	if !has(op, "scene") {
		scene = "scene"
	}
	ctx := Context{SceneID: scene, Simulation: get(op, "simulation") == true}
	switch get(op, "kind") {
	case "purgeSource":
		return s.PurgeSource(scene, str(get(op, "message")))
	case "purgeAssertion":
		return s.PurgeAssertion(str(get(op, "id")))
	case "prune":
		calls := 0
		return s.PruneUnavailableProof(ctx, func(e, p *Object) (bool, error) {
			f.events = append(f.events, []any{"allowed", get(get(e, "backing"), "id"), get(p, "proofId")})
			calls++
			if num(get(op, "failAt")) == float64(calls) {
				return false, errors.New("synthetic_allowed")
			}
			for _, source := range arr(get(p, "sources")) {
				if includes(get(op, "denyMessages"), get(source, "messageId")) {
					return false, nil
				}
			}
			for _, parent := range arr(get(p, "parents")) {
				if includes(get(op, "denyParents"), parent) {
					return false, nil
				}
			}
			return true, nil
		})
	case "requalify":
		transitions := []Transition{}
		for _, v := range arr(get(op, "transitions")) {
			transitions = append(transitions, Transition{reference(get(v, "from")), reference(get(v, "to"))})
		}
		if get(op, "noSurviving") == true {
			return s.Requalify(ctx, transitions, nil)
		}
		return s.Requalify(ctx, transitions, func(_ Context, e *Object) (*Candidate, error) {
			id := str(get(get(e, "backing"), "id"))
			f.events = append(f.events, []any{"surviving", id, bound(e)})
			candidate := get(get(op, "candidates"), id)
			if candidate == nil {
				return nil, nil
			}
			view := get(candidate, "view")
			return &Candidate{reference(get(candidate, "ref")), &Assertion{Record: object(get(view, "record")), Proofs: arr(get(view, "proofs"))}}, nil
		})
	case "erase":
		return s.EraseRetained(ctx, reference(get(op, "ref")))
	}
	return errors.New("unknown_oracle_action")
}
func TestActualSourceDualSQLitePrivacy(t *testing.T) {
	oracle := loadOracle(t)
	for _, raw := range arr(get(oracle, "traces")) {
		trace := object(raw)
		t.Run(str(get(trace, "name")), func(t *testing.T) {
			f := setup(t)
			seedRows(t, f, object(parse(t, str(get(trace, "initial")))))
			for i, raw := range arr(get(trace, "steps")) {
				step := object(raw)
				op := object(parse(t, str(get(step, "op"))))
				f.events = []any{}
				f.fault = str(get(op, "fault"))
				f.now = "2026-10-28T13:00:00.000Z"
				if has(op, "now") {
					f.now = str(get(op, "now"))
				}
				if has(op, "sql") {
					_, e := f.main.Exec(str(get(op, "sql")))
					must(t, e)
				}
				e := f.store.Transaction(context.Background(), func(store *Store) error {
					service, _ := NewPrivacyService(store, f.service.owners)
					return f.action(op, service)
				})
				wantError := get(step, "error")
				if wantError == nil && e != nil {
					t.Fatalf("step %d: %v", i, e)
				}
				if wantError != nil && (e == nil || !strings.Contains(e.Error(), str(wantError))) {
					t.Fatalf("step %d error %v, want %s", i, e, wantError)
				}
				compare := func(label string, got any, want string) {
					t.Helper()
					actual := stringify(t, got)
					if actual != want {
						t.Fatalf("step %d %s mismatch\ngot %s\nwant %s", i, label, actual, want)
					}
				}
				compare("events", f.events, str(get(step, "events")))
				compare("rows", tableValue(t, f), str(get(step, "rows")))
				compare("authority", rowsValue(t, f.external, "SELECT sequence,previous_hash,payload,hash FROM revocations ORDER BY sequence"), str(get(step, "entries")))
				var rawGate string
				must(t, f.main.QueryRow("SELECT value FROM recovery_gate").Scan(&rawGate))
				gate := parse(t, rawGate)
				if get(get(gate, "head"), "sequence") != get(step, "gateSequence") || get(get(gate, "head"), "hash") != get(step, "gateHash") || get(gate, "minimumReader") != get(step, "minimumReader") {
					t.Fatalf("step %d gate mismatch %s", i, rawGate)
				}
				if wantError == nil {
					snapshots := []any{}
					must(t, f.store.Read(context.Background(), func(reader *Reader) error {
						heads, e := reader.AllHeads()
						if e != nil {
							return e
						}
						for _, head := range heads {
							snapshot, e := reader.Snapshot(str(get(head, "id")))
							if e != nil {
								return e
							}
							snapshots = append(snapshots, snapshot)
						}
						return nil
					}))
					compare("snapshots", snapshots, str(get(step, "snapshots")))
				} else if get(step, "gateSequence") != float64(len(arr(parse(t, str(get(step, "entries")))))) {
					if err := f.store.Read(context.Background(), func(r *Reader) error { _, e := r.AllHeads(); return e }); err == nil {
						t.Fatal("authority gap permitted publication")
					}
				}
			}
		})
	}
}
func TestActualSourceSchemasAndDeepEquality(t *testing.T) {
	oracle := loadOracle(t)
	decoders := map[string]decoder{"head": func(v any) (any, error) { return NormalizeHead(v) }, "revision": func(v any) (any, error) { return NormalizeRevision(v) }, "evidence": evidenceValue, "transition": func(v any) (any, error) { return NormalizeTransition(v) }, "window": func(v any) (any, error) { return NormalizeWindow(v) }, "state": stateValue, "marker": func(v any) (any, error) { return NormalizeMarker(v) }, "terms": termsValue, "ref": refValue}
	for i, v := range arr(get(oracle, "schemas")) {
		v := object(v)
		t.Run(fmt.Sprintf("%d-%s-%s", i, str(get(v, "kind")), str(get(v, "name"))), func(t *testing.T) {
			out, e := decoders[str(get(v, "kind"))](parse(t, str(get(v, "input"))))
			accepted := get(v, "accepted") == true
			if (e == nil) != accepted {
				t.Fatalf("acceptance mismatch: %v", e)
			}
			if accepted && stringify(t, out) != get(v, "normalized") {
				t.Fatalf("normalization mismatch: %s != %s", stringify(t, out), get(v, "normalized"))
			}
		})
	}
	for _, v := range arr(get(oracle, "deepCases")) {
		if deepEqual(parse(t, str(get(v, "left"))), parse(t, str(get(v, "right")))) != (get(v, "equal") == true) {
			t.Fatalf("deep equality mismatch %v", v)
		}
	}
	a := reference(parse(t, `{"id":"i","version":1,"deletionEpoch":-0}`))
	b := reference(parse(t, `{"id":"i","version":1,"deletionEpoch":0}`))
	if a != b {
		t.Fatal("explicit source ref === must equate signed zero")
	}
}
func TestSourceDigests(t *testing.T) {
	oracle := loadOracle(t)
	for _, source := range object(get(oracle, "sources")).Members() {
		raw, e := os.ReadFile(filepath.Join("../../..", source.Key))
		must(t, e)
		hash := fmt.Sprintf("%x", sha256.Sum256(raw))
		if hash != source.Value {
			t.Fatalf("source changed: %s; regenerate actual-source oracle", source.Key)
		}
	}
}
func TestActualSourceRecordedReads(t *testing.T) {
	oracle := loadOracle(t)
	sample := get(oracle, "readSample")
	first := arr(get(sample, "revisions"))[0]
	evidence := arr(get(first, "evidence"))[0]
	record := obj("id", "root", "scope", obj("kind", "scene", "sceneId", "scene", "personId", "a"), "content", "Synthetic declaration root", "kind", "fact", "epistemicKind", "explicit", "status", "active", "lineageStatus", "verified")
	for _, v := range arr(get(oracle, "reads")) {
		anchor := get(v, "anchor")
		input := get(v, "input")
		q := TimelineQuery{}
		if has(input, "limit") {
			n := num(get(input, "limit"))
			q.Limit = &n
		}
		if has(input, "asOfRecorded") {
			s := str(get(input, "asOfRecorded"))
			q.AsOfRecorded = &s
		}
		historical := func(_ Context, _ Reference) (*Assertion, error) {
			return &Assertion{Record: record, Proofs: arr(get(evidence, "proofs"))}, nil
		}
		if get(v, "noHistorical") == true {
			historical = nil
		}
		got, e := ReadTimeline(Context{SceneID: "scene"}, Exposure{reference(anchor), num(get(anchor, "mutationVersion"))}, object(get(sample, "timeline")), q, TimelinePorts{Revision: func(version float64) (*Object, error) {
			for _, r := range arr(get(sample, "revisions")) {
				if get(get(r, "ref"), "version") == version {
					return object(r), nil
				}
			}
			return nil, nil
		}, Historical: historical})
		must(t, e)
		if stringify(t, got) != get(v, "value") {
			t.Fatalf("timeline read mismatch\n%s\n%s", stringify(t, got), get(v, "value"))
		}
	}
}

// The default-build domain oracle supplies actual Node localeCompare results
// for every strict schema key. Unknown keys fail instead of using lexical order.
func sourceCompare(t *testing.T) func(string, string) (int, error) {
	t.Helper()
	pairs := get(loadOracle(t), "comparisons")
	return func(a, b string) (int, error) {
		row := get(pairs, a)
		if !has(row, b) {
			return 0, fmt.Errorf("source comparison key unavailable: %q/%q", a, b)
		}
		return int(num(get(row, b))), nil
	}
}
func TestActualSourceMalformedStorageTypes(t *testing.T) {
	for _, v := range arr(get(loadOracle(t), "storageCases")) {
		t.Run(str(get(v, "sql")), func(t *testing.T) {
			f := seeded(t)
			_, e := f.main.Exec(str(get(v, "sql")))
			must(t, e)
			var value any
			e = f.store.Read(context.Background(), func(r *Reader) error {
				var e error
				switch get(v, "method") {
				case "timeline":
					value, e = r.Timeline("commitment-1")
				case "head":
					value, e = r.Head("commitment-1")
				case "ids":
					var ids []string
					ids, e = r.AllTemporalIDs()
					if e == nil {
						a := []any{}
						for _, id := range ids {
							a = append(a, id)
						}
						value = a
					}
				}
				return e
			})
			if get(v, "error") != nil {
				if e == nil {
					t.Fatal("source rejected malformed SQLite type")
				}
			} else {
				must(t, e)
				if stringify(t, value) != get(v, "value") {
					t.Fatal("source accepted value differs")
				}
			}
		})
	}
}
