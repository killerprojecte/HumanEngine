package httpapi

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/killerprojecte/HumanEngine/backend/internal/experienceservice"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/recovery"
	"github.com/killerprojecte/HumanEngine/backend/internal/sourceevents"
	"github.com/killerprojecte/HumanEngine/backend/internal/topiccollation"
)

func experienceHTTP(t *testing.T) (*Server, *sql.DB, *recovery.Authority) {
	t.Helper()
	_, db, _, gate := fixtureServer(t)
	a, e := recovery.OpenWriter(context.Background(), gate.Authority)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { a.Close() })
	collator, e := topiccollation.New("en-US")
	if errors.Is(e, topiccollation.ErrUnavailable) && os.Getenv("REQUIRE_ICU") == "" {
		t.Skip("pinned native ICU required")
	}
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { collator.Close() })
	_, e = db.Exec(`CREATE TABLE recall_tombstones(scene_id TEXT,message_id TEXT,recalled_at TEXT,PRIMARY KEY(scene_id,message_id));CREATE TABLE synthetic_preview(scene TEXT,id TEXT,value TEXT,PRIMARY KEY(scene,id));INSERT INTO scenes VALUES('s','{"id":"s","kind":"group","learningEnabled":true}');`)
	if e != nil {
		t.Fatal(e)
	}
	ports, e := experienceservice.NativePorts(func(ws *recovery.WriteSession, scene, id string) (*jsjson.Object, error) {
		rows, e := ws.Query("SELECT value FROM synthetic_preview WHERE scene=? AND id=?", scene, id)
		if e != nil {
			return nil, e
		}
		defer rows.Close()
		if !rows.Next() {
			return nil, rows.Err()
		}
		var raw string
		if e = rows.Scan(&raw); e != nil {
			return nil, e
		}
		v, e := jsjson.Parse(raw)
		if e != nil {
			return nil, e
		}
		o, ok := v.(*jsjson.Object)
		if !ok {
			return nil, errors.New("invalid synthetic preview")
		}
		return o, nil
	}, collator.Compare)
	if e != nil {
		t.Fatal(e)
	}
	ports.Now = func() int64 { return 1793232000000 }
	ports.ID = func() string { return "synthetic-reviewfix-receipt" }
	service, e := experienceservice.New(context.Background(), db, a, ports)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = sourceevents.New(db, a, sourceevents.Options{}); e != nil {
		t.Fatal(e)
	}
	s, e := NewWithExperience(db, a, service, fixtureToken, nil)
	if e != nil {
		t.Fatal(e)
	}
	return s, db, a
}

type experienceOracle struct {
	Initial map[string]json.RawMessage
	Cases   []struct {
		Name, Method, Path, Text, Token string
		Body                            *string
		JSONType                        bool
		Status                          int
	}
}

func experienceFixture(t *testing.T) experienceOracle {
	t.Helper()
	raw, e := os.ReadFile("testdata/experience-source.json")
	if e != nil {
		t.Fatal(e)
	}
	var out experienceOracle
	if e = json.Unmarshal(raw, &out); e != nil {
		t.Fatal(e)
	}
	return out
}
func seedExperienceHTTP(t *testing.T, db *sql.DB, initial map[string]json.RawMessage) {
	t.Helper()
	exec := func(q string, args ...any) {
		if _, e := db.Exec(q, args...); e != nil {
			t.Fatal(e)
		}
	}
	// Preserve the real Settings schema rather than inventing a partial config.
	settings := `{"name":"HumanEngine","timezone":"UTC","persona":"Synthetic","cacheEnabled":true,"cacheHours":24,"debounceMs":1200,"maxWaitMs":6000,"memoryExperienceEnabled":true}`
	exec("INSERT OR REPLACE INTO config VALUES('settings',?)", settings)
	exec("INSERT INTO config VALUES('identity:s:alice',?)", string(initial["identity"]))
	exec("INSERT INTO memory_experience_policies VALUES('p','s',1,?)", string(initial["policy"]))
	exec("INSERT INTO memory_experience_outcomes VALUES('outcome','root','s','instruction',?)", string(initial["outcome"]))
	exec("INSERT INTO memory_experience_validations VALUES('receipt','p',?)", string(initial["receipt"]))
	exec("CREATE TABLE IF NOT EXISTS memory_experience_trials(policy TEXT PRIMARY KEY,scene TEXT NOT NULL,version INTEGER NOT NULL,value TEXT)")
	exec("INSERT INTO memory_experience_trials VALUES('p','s',1,?)", string(initial["trial"]))
}
func TestActualExperienceHTTPSource(t *testing.T) {
	fixture := experienceFixture(t)
	for _, c := range fixture.Cases {
		t.Run(c.Name, func(t *testing.T) {
			s, db, _ := experienceHTTP(t)
			if strings.HasPrefix(c.Name, "rich-") || strings.HasPrefix(c.Name, "clean-") {
				seedExperienceHTTP(t, db, fixture.Initial)
			}
			if strings.HasPrefix(c.Name, "clean-") {
				policy := fixture.Initial["cleanPolicy"]
				version := 1
				if c.Name == "clean-review-active" {
					policy = fixture.Initial["validatedPolicy"]
					version = 2
				}
				for _, stmt := range []struct {
					q    string
					args []any
				}{{"UPDATE memory_experience_policies SET version=?,value=? WHERE id='p'", []any{version, string(policy)}}, {"UPDATE memory_experience_validations SET value=? WHERE id='receipt'", []any{string(fixture.Initial["cleanReceipt"])}}, {"DELETE FROM memory_experience_trials", nil}} {
					if _, e := db.Exec(stmt.q, stmt.args...); e != nil {
						t.Fatal(e)
					}
				}
			}
			body := ""
			if c.Body != nil {
				body = *c.Body
			}
			r := httptest.NewRequest(c.Method, c.Path, strings.NewReader(body))
			r.Header.Set("Authorization", "Bearer "+c.Token)
			r.Header.Set("X-Request-ID", "<request-id>")
			if c.JSONType {
				r.Header.Set("Content-Type", "application/json")
			}
			w := httptest.NewRecorder()
			s.ServeHTTP(w, r)
			if w.Code != c.Status {
				t.Fatalf("status %d want %d: %s", w.Code, c.Status, w.Body.String())
			}
			got := w.Body.String()
			var x, y any
			if json.Unmarshal([]byte(got), &x) == nil && json.Unmarshal([]byte(c.Text), &y) == nil {
				a, _ := json.Marshal(x)
				b, _ := json.Marshal(y)
				if string(a) != string(b) {
					t.Fatalf("got %s want %s", a, b)
				}
			} else if got != c.Text {
				t.Fatalf("got %q want %q", got, c.Text)
			}
			for _, secret := range []string{"private-policy", "private-positive", "private-source", "private-receipt", "private-condition", "private-root"} {
				if strings.Contains(got, secret) {
					t.Fatalf("evidence leak: %s", secret)
				}
			}
		})
	}
}
func TestExperienceBodyReadDoesNotReserveAuthority(t *testing.T) {
	for _, body := range []string{`{}`, `{"sceneId":"absent","expectedVersion":1}`, `{"sceneId":"s","expectedVersion":1}`} {
		t.Run(body, func(t *testing.T) {
			s, _, a := experienceHTTP(t)
			r := httptest.NewRequest("POST", experiencePrefix+"/p/validate", nil)
			r.Header.Set("Authorization", "Bearer "+fixtureToken)
			r.Header.Set("Content-Type", "application/json")
			calls := 0
			r.Body = io.NopCloser(&habitCallbackReader{Reader: strings.NewReader(body), before: func() error {
				calls++
				head, e := a.Head(context.Background())
				if e != nil {
					return e
				}
				_, e = a.Append(context.Background(), recovery.Source{SceneID: "s", MessageID: "during-body"}, head, false)
				return e
			}})
			w := httptest.NewRecorder()
			s.ServeHTTP(w, r)
			if calls != 1 || w.Code != 503 {
				t.Fatalf("calls %d status %d: %s", calls, w.Code, w.Body.String())
			}
		})
	}
}
func TestExperienceConstructorIsolation(t *testing.T) {
	s, db, a := experienceHTTP(t)
	if _, e := NewWithExperience(db, a, nil, fixtureToken, nil); e == nil {
		t.Fatal("nil accepted")
	}
	if _, e := NewWithExperience(nil, a, s.experience, fixtureToken, nil); e == nil {
		t.Fatal("foreign pool accepted")
	}
	preview, e := New(db, a, fixtureToken, nil)
	if e != nil {
		t.Fatal(e)
	}
	r := httptest.NewRequest("GET", experiencePrefix+"?sceneId=s", nil)
	r.Header.Set("Authorization", "Bearer "+fixtureToken)
	w := httptest.NewRecorder()
	preview.ServeHTTP(w, r)
	if w.Code != 503 {
		t.Fatal("preview enabled writes")
	}
}
