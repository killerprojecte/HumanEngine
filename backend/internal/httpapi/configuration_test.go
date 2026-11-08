package httpapi

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/killerprojecte/HumanEngine/backend/internal/credentials"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/recovery"
	"github.com/killerprojecte/HumanEngine/backend/internal/settingsstore"
)

type configurationRecorder struct {
	main  *sql.DB
	calls int
}

func (r *configurationRecorder) SettingsChanged(ctx context.Context, c SettingsChange) error {
	var raw string
	if e := r.main.QueryRowContext(ctx, "SELECT value FROM config WHERE key='settings'").Scan(&raw); e != nil {
		return e
	}
	encoded, e := jsjson.Stringify(c.Current)
	if e != nil {
		return e
	}
	if raw != encoded {
		panic("runtime callback before commit")
	}
	r.calls++
	return nil
}
func configurationFixture(t *testing.T) (*Server, *sql.DB, *recovery.Authority, *credentials.Store, *configurationRecorder) {
	t.Helper()
	_, db, _, gate := fixtureServer(t)
	if _, e := db.Exec(`DROP TABLE secrets; CREATE TABLE recovery_security_state(id INTEGER PRIMARY KEY CHECK(id=1),revision TEXT NOT NULL);INSERT INTO recovery_security_state VALUES(1,'12345678-1234-4123-8123-123456789012')`); e != nil {
		t.Fatal(e)
	}
	a, e := recovery.OpenWriter(context.Background(), gate.Authority)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { a.Close() })
	secrets, e := credentials.NewStore(context.Background(), db, a, "")
	if e != nil {
		t.Fatal(e)
	}
	s, e := New(db, a, "synthetic-config-token", nil)
	if e != nil {
		t.Fatal(e)
	}
	runtime := &configurationRecorder{main: db}
	if e = s.WithConfiguration(secrets, &settingsstore.Store{Main: db, Authority: a}, runtime); e != nil {
		t.Fatal(e)
	}
	return s, db, a, secrets, runtime
}
func TestActualConfigurationHTTPSource(t *testing.T) {
	raw, e := os.ReadFile("testdata/configuration-source.json")
	if e != nil {
		t.Fatal(e)
	}
	var fixture struct {
		Cases []struct {
			Name, Path, Method, Body, Text   string
			Headers                          map[string]string
			ResponseHeaders                  map[string]*string
			Initial                          json.RawMessage
			Secret, Rollback, SecretRollback bool
			Status                           int
			DurablePayloads                  []string
			HeadDelta, GateDelta             int64
			Rows                             []struct{ Key, Value string }
			Secrets                          []struct {
				Namespace, Name, Value string
				Encrypted              bool
			}
		}
	}
	if e = json.Unmarshal(raw, &fixture); e != nil {
		t.Fatal(e)
	}
	for _, c := range fixture.Cases {
		t.Run(c.Name, func(t *testing.T) {
			s, db, authority, secrets, runtime := configurationFixture(t)
			ctx := context.Background()
			if string(c.Initial) != "null" {
				v, e := jsjson.ParseBytes(c.Initial)
				if e != nil {
					t.Fatal(e)
				}
				m := new(jsjson.Object)
				if c.Secret {
					mutation, _ := jsjson.NewObject(jsjson.Member{Key: "action", Value: "set"}, jsjson.Member{Key: "value", Value: "synthetic-original"})
					_ = m.Set("p", mutation)
				}
				if _, e = secrets.SaveModels(ctx, v.(*jsjson.Object), m); e != nil {
					t.Fatal(e)
				}
			}
			oldCipher := ""
			_ = db.QueryRow("SELECT ciphertext FROM secrets WHERE namespace='model' AND name='p'").Scan(&oldCipher)
			if c.SecretRollback {
				if _, e := db.Exec("CREATE TRIGGER synthetic_secret_failure BEFORE INSERT ON secrets BEGIN SELECT RAISE(ABORT,'synthetic_secret_failure'); END"); e != nil {
					t.Fatal(e)
				}
			}
			if c.Rollback && !c.SecretRollback {
				key := "models"
				if c.Path == "/api/settings" {
					key = "settings"
				}
				if _, e := db.Exec("CREATE TRIGGER synthetic_fail_insert BEFORE INSERT ON config WHEN NEW.key='" + key + "' BEGIN SELECT RAISE(ABORT,'synthetic_config_failure'); END; CREATE TRIGGER synthetic_fail_update BEFORE UPDATE ON config WHEN NEW.key='" + key + "' BEGIN SELECT RAISE(ABORT,'synthetic_config_failure'); END"); e != nil {
					t.Fatal(e)
				}
			}
			beforeHead, e := authority.Head(ctx)
			if e != nil {
				t.Fatal(e)
			}
			req := httptest.NewRequest(c.Method, c.Path, strings.NewReader(c.Body))
			for k, v := range c.Headers {
				req.Header.Set(k, v)
			}
			w := httptest.NewRecorder()
			s.ServeHTTP(w, req)
			afterHead, e := authority.Head(ctx)
			if e != nil {
				t.Fatal(e)
			}
			gate, e := recovery.ReadGate(ctx, db)
			if e != nil {
				t.Fatal(e)
			}
			if afterHead.Sequence-beforeHead.Sequence != c.HeadDelta || gate.Head.Sequence-beforeHead.Sequence != c.GateDelta {
				t.Fatalf("head/gate deltas %d/%d want %d/%d", afterHead.Sequence-beforeHead.Sequence, gate.Head.Sequence-beforeHead.Sequence, c.HeadDelta, c.GateDelta)
			}
			ledger, e := sql.Open("sqlite3", filepath.Join(gate.Authority, "authority.sqlite"))
			if e != nil {
				t.Fatal(e)
			}
			payloadRows, e := ledger.Query("SELECT payload FROM revocations WHERE sequence>? ORDER BY sequence", beforeHead.Sequence)
			if e != nil {
				t.Fatal(e)
			}
			payloads := []string{}
			for payloadRows.Next() {
				var raw string
				if e = payloadRows.Scan(&raw); e != nil {
					t.Fatal(e)
				}
				v, e := jsjson.Parse(raw)
				if e != nil {
					t.Fatal(e)
				}
				o := v.(*jsjson.Object)
				kind, _ := o.Get("kind")
				if kind == "security_state" {
					_ = o.Set("revision", "<uuid>")
				}
				raw, e = jsjson.Stringify(o)
				if e != nil {
					t.Fatal(e)
				}
				payloads = append(payloads, raw)
			}
			payloadRows.Close()
			ledger.Close()
			if !reflect.DeepEqual(payloads, c.DurablePayloads) {
				t.Fatalf("durable payload prefix %v want %v", payloads, c.DurablePayloads)
			}
			if w.Code != c.Status {
				t.Fatalf("status %d want %d: %s", w.Code, c.Status, w.Body.String())
			}
			for key, value := range c.ResponseHeaders {
				want := ""
				if value != nil {
					want = *value
				}
				if got := w.Header().Get(key); got != want {
					t.Fatalf("header %s=%q want %q", key, got, want)
				}
			}
			got, want := strings.ReplaceAll(w.Body.String(), c.Headers["X-Request-ID"], "<request-id>"), c.Text
			gv, ge := jsjson.Parse(got)
			wv, we := jsjson.Parse(want)
			if ge == nil && we == nil {
				if !reflect.DeepEqual(unordered(gv), unordered(wv)) {
					t.Fatalf("response %s want %s", got, want)
				}
			} else if got != want {
				t.Fatalf("response %q want %q", got, want)
			}
			rows, e := db.Query("SELECT key,value FROM config WHERE key IN ('settings','models','personaVersions','memory-experience-feedback-revision') ORDER BY key")
			if e != nil {
				t.Fatal(e)
			}
			actual := []struct{ Key, Value string }{}
			for rows.Next() {
				var r struct{ Key, Value string }
				if e = rows.Scan(&r.Key, &r.Value); e != nil {
					t.Fatal(e)
				}
				if r.Key == "personaVersions" {
					v, e := jsjson.Parse(r.Value)
					if e != nil {
						t.Fatal(e)
					}
					for _, x := range v.([]any) {
						o := x.(*jsjson.Object)
						_ = o.Set("id", "<uuid>")
						_ = o.Set("createdAt", "<time>")
					}
					r.Value, e = jsjson.Stringify(v)
					if e != nil {
						t.Fatal(e)
					}
				}
				actual = append(actual, r)
			}
			if e = rows.Close(); e != nil {
				t.Fatal(e)
			}
			if !reflect.DeepEqual(actual, c.Rows) {
				t.Fatalf("SQL rows %#v want %#v", actual, c.Rows)
			}
			for _, secret := range c.Secrets {
				var envelope string
				if e = db.QueryRow("SELECT ciphertext FROM secrets WHERE namespace=? AND name=?", secret.Namespace, secret.Name).Scan(&envelope); e != nil {
					t.Fatal(e)
				}
				if strings.Contains(envelope, "synthetic-") {
					t.Fatal("plaintext persisted")
				}
				if c.Rollback {
					if envelope != oldCipher {
						t.Fatal("credential escaped model rollback")
					}
				} else {
					v, found, e := secrets.Get(ctx, secret.Namespace, secret.Name)
					if e != nil || !found || v != secret.Value {
						t.Fatalf("credential mismatch: found=%v err=%v", found, e)
					}
				}
			}
			var count int
			if e = db.QueryRow("SELECT COUNT(*) FROM secrets").Scan(&count); e != nil || count != len(c.Secrets) {
				t.Fatalf("secrets count=%d err=%v", count, e)
			}
			expectedCalls := 0
			if c.Path == "/api/settings" && c.Status == 200 && c.Method == "PUT" {
				expectedCalls = 1
			}
			if runtime.calls != expectedCalls {
				t.Fatalf("runtime calls=%d want=%d", runtime.calls, expectedCalls)
			}
		})
	}
}
