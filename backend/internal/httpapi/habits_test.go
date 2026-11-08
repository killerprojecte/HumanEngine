package httpapi

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"github.com/killerprojecte/HumanEngine/backend/internal/habitservice"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/recovery"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

const habitRecord = `{"id":"h","kind":"sceneRule","sceneId":"s","content":"Synthetic","evidenceVersion":2,"authorWatermarks":{"s:p":123},"sources":[],"weight":0,"confidence":0,"status":"invalidated","locked":false,"learnedAt":"2026-11-01T00:00:00.000Z"}`

func habitHTTP(t *testing.T) (*Server, *sql.DB, *recovery.Authority, recovery.Gate) {
	t.Helper()
	_, main, _, gate := fixtureServer(t)
	a, e := recovery.OpenWriter(context.Background(), gate.Authority)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { a.Close() })
	h, e := habitservice.New(context.Background(), main, a, habitservice.Options{})
	if e != nil {
		t.Fatal(e)
	}
	s, e := NewWithHabits(main, a, h, fixtureToken, nil)
	if e != nil {
		t.Fatal(e)
	}
	return s, main, a, gate
}
func TestActualHabitHTTPSource(t *testing.T) {
	raw, e := os.ReadFile("testdata/habits-source.json")
	if e != nil {
		t.Fatal(e)
	}
	var f struct {
		Cases []struct {
			Name, Method, Path, Text, Token                            string
			Body                                                       *string
			JSONType                                                   bool
			Status                                                     int
			Initial                                                    json.RawMessage
			WithdrawDuringBody, DeleteRollback, WithdrawDuringNotFound bool
			HeadDelta, GateDelta                                       *int64
			DurablePayloads                                            []string
			Rows                                                       []struct{ ID, Value string }
		}
	}
	if e = json.Unmarshal(raw, &f); e != nil {
		t.Fatal(e)
	}
	for _, c := range f.Cases {
		t.Run(c.Name, func(t *testing.T) {
			s, db, authority, gate := habitHTTP(t)
			initial, e := jsjson.Parse(string(c.Initial))
			if e != nil {
				t.Fatal(e)
			}
			initialRaw, e := jsjson.Stringify(initial)
			if e != nil {
				t.Fatal(e)
			}
			idValue, _ := initial.(*jsjson.Object).Get("id")
			id := idValue.(string)
			if _, e := db.Exec("INSERT INTO habits VALUES(?,?,?)", id, strings.Repeat("a", 64), initialRaw); e != nil {
				t.Fatal(e)
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
			bodyCallbacks := 0
			if c.WithdrawDuringBody {
				r.Body = io.NopCloser(&habitCallbackReader{Reader: strings.NewReader(body), before: func() error {
					bodyCallbacks++
					_, e := authority.Append(context.Background(), recovery.Source{SceneID: "s", MessageID: "body-withdrawal"}, gate.Head, false)
					return e
				}})
			}
			if c.DeleteRollback {
				if _, e := db.Exec("CREATE TRIGGER synthetic_delete_failure BEFORE DELETE ON habits BEGIN SELECT RAISE(ABORT,'synthetic_delete_failure'); END"); e != nil {
					t.Fatal(e)
				}
			}
			w := httptest.NewRecorder()
			if c.WithdrawDuringNotFound {
				// Independent connection withdraws while the handler materializes
				// private headers, before any response can be accepted.
				other, err := recovery.OpenWriter(context.Background(), gate.Authority)
				if err != nil {
					t.Fatal(err)
				}
				defer other.Close()
				callbacks := 0
				wrapped := &habitMaterializationWriter{ResponseRecorder: w, before: func() {
					callbacks++
					if _, err := other.Append(context.Background(), recovery.Source{SceneID: "s", MessageID: "not-found-withdrawal"}, gate.Head, false); err != nil {
						t.Fatal(err)
					}
				}}
				s.ServeHTTP(wrapped, r)
				if callbacks != 1 {
					t.Fatalf("materialization callback count %d", callbacks)
				}
			} else {
				s.ServeHTTP(w, r)
			}
			if c.WithdrawDuringBody || c.DeleteRollback || c.WithdrawDuringNotFound {
				head, e := authority.Head(context.Background())
				if e != nil {
					t.Fatal(e)
				}
				after, e := recovery.ReadGate(context.Background(), db)
				if e != nil {
					t.Fatal(e)
				}
				if c.HeadDelta == nil || c.GateDelta == nil || head.Sequence-gate.Head.Sequence != *c.HeadDelta || after.Head.Sequence-gate.Head.Sequence != *c.GateDelta {
					t.Fatal("source head/gate differential", head, after)
				}
				ledger, e := sql.Open("sqlite3", filepath.Join(gate.Authority, "authority.sqlite"))
				if e != nil {
					t.Fatal(e)
				}
				rows, e := ledger.Query("SELECT payload FROM revocations WHERE sequence>? ORDER BY sequence", gate.Head.Sequence)
				if e != nil {
					ledger.Close()
					t.Fatal(e)
				}
				n := 0
				for rows.Next() {
					var payload string
					if e = rows.Scan(&payload); e != nil {
						t.Fatal(e)
					}
					if n >= len(c.DurablePayloads) || payload != c.DurablePayloads[n] {
						t.Fatal("durable payload differential", payload)
					}
					n++
				}
				rowErr := rows.Err()
				rows.Close()
				ledger.Close()
				if rowErr != nil || n != len(c.DurablePayloads) {
					t.Fatal("durable payload rows", n, rowErr)
				}
				if c.WithdrawDuringBody && bodyCallbacks != 1 {
					t.Fatal("body withdrawal did not occur")
				}
				if c.DeleteRollback {
					var n int
					if e = db.QueryRow("SELECT count(*) FROM habit_deleted").Scan(&n); e != nil || n != 0 {
						t.Fatal("delete tombstone escaped rollback", n, e)
					}
				}
			}

			text := w.Body.String()
			if c.Name == "bad-auth" {
				text = strings.TrimSuffix(text, "\n")
			}
			if w.Code != c.Status || text != c.Text {
				t.Fatalf("%d %s; want %d %s", w.Code, text, c.Status, c.Text)
			}
			rows, e := db.Query("SELECT id,value FROM habits ORDER BY rowid")
			if e != nil {
				t.Fatal(e)
			}
			defer rows.Close()
			i := 0
			for rows.Next() {
				var id, value string
				if e = rows.Scan(&id, &value); e != nil {
					t.Fatal(e)
				}
				if i >= len(c.Rows) || id != c.Rows[i].ID || value != c.Rows[i].Value {
					t.Fatal("persisted source differential", id, value)
				}
				i++
			}
			if i != len(c.Rows) {
				t.Fatal("row count")
			}
		})
	}
}
func TestHabitHTTPTransportFailureNeverRetriesWrite(t *testing.T) {
	s, db, _, _ := habitHTTP(t)
	_, e := db.Exec("INSERT INTO habits VALUES('h','aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa',?)", habitRecord)
	if e != nil {
		t.Fatal(e)
	}
	_, e = db.Exec("CREATE TABLE attempts(n); CREATE TRIGGER count_updates AFTER UPDATE ON habits BEGIN INSERT INTO attempts VALUES(1); END")
	if e != nil {
		t.Fatal(e)
	}
	r := httptest.NewRequest("PATCH", "/api/habits/h", strings.NewReader(`{"locked":true}`))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Authorization", "Bearer "+fixtureToken)
	w := &trackingResponse{header: make(map[string][]string), failure: errors.New("synthetic network failure")}
	s.ServeHTTP(w, r)
	var count int
	db.QueryRow("SELECT count(*) FROM attempts").Scan(&count)
	if count != 1 || w.statusCalls != 1 || w.status != 200 || w.writeCalls != 1 {
		t.Fatal(count, w)
	}
	var raw string
	db.QueryRow("SELECT value FROM habits").Scan(&raw)
	if !strings.Contains(raw, `"locked":true`) {
		t.Fatal("committed write lost")
	}
}
func TestHabitHTTPUnavailableAndCommitFailure(t *testing.T) {
	s, db, _, _ := habitHTTP(t)
	_, e := db.Exec("INSERT INTO habits VALUES('h','aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa',?)", habitRecord)
	if e != nil {
		t.Fatal(e)
	}
	_, e = db.Exec("CREATE TRIGGER reject_habit BEFORE UPDATE ON habits BEGIN SELECT RAISE(ROLLBACK,'synthetic rollback'); END")
	if e != nil {
		t.Fatal(e)
	}
	r := httptest.NewRequest("PATCH", "/api/habits/h", strings.NewReader(`{"locked":true}`))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Authorization", "Bearer "+fixtureToken)
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	if w.Code != 503 || strings.Contains(w.Body.String(), "Synthetic") {
		t.Fatal(w.Code, w.Body)
	}
	r = httptest.NewRequest("GET", "/api/habits", nil)
	r.Header.Set("Authorization", "Bearer "+fixtureToken)
	w = httptest.NewRecorder()
	s.ServeUnavailable(w, r)
	if w.Code != 503 {
		t.Fatal(w.Code)
	}
}

// The hook executes only when the handler consumes request bytes, after its
// initial admission. It is a deterministic slow-body concurrency boundary.
type habitCallbackReader struct {
	io.Reader
	before func() error
	once   sync.Once
	err    error
}

func (r *habitCallbackReader) Read(p []byte) (int, error) {
	r.once.Do(func() { r.err = r.before() })
	if r.err != nil {
		return 0, r.err
	}
	return r.Reader.Read(p)
}

// The initial Server header setup is complete before the handler asks for its
// owned header snapshot. Trigger once at that real boundary, not in production.
type habitMaterializationWriter struct {
	*httptest.ResponseRecorder
	before func()
}

func (w *habitMaterializationWriter) Header() http.Header {
	h := w.ResponseRecorder.Header()
	if w.before != nil && h.Get("X-Content-Type-Options") == "nosniff" {
		fn := w.before
		w.before = nil
		fn()
	}
	return h
}
