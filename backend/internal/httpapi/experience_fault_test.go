package httpapi

import (
	"context"
	"errors"
	"github.com/killerprojecte/HumanEngine/backend/internal/recovery"
	"github.com/mattn/go-sqlite3"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestExperienceTransportFailureDoesNotReplayDelete(t *testing.T) {
	s, db, _ := experienceHTTP(t)
	seedExperienceHTTP(t, db, experienceFixture(t).Initial)
	if _, e := db.Exec("CREATE TABLE attempts(n);CREATE TRIGGER count_delete AFTER DELETE ON memory_experience_policies BEGIN INSERT INTO attempts VALUES(1);END"); e != nil {
		t.Fatal(e)
	}
	r := httptest.NewRequest("DELETE", experiencePrefix+"/p", strings.NewReader(`{"sceneId":"s","expectedVersion":1}`))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Authorization", "Bearer "+fixtureToken)
	w := &trackingResponse{header: make(map[string][]string), failure: errors.New("synthetic lost response")}
	s.ServeHTTP(w, r)
	var count int
	if e := db.QueryRow("SELECT count(*) FROM attempts").Scan(&count); e != nil {
		t.Fatal(e)
	}
	if count != 1 || w.status != 200 || w.statusCalls != 1 || w.writeCalls != 1 {
		t.Fatal(count, w)
	}
}
func TestExperienceDeleteRollbackLeavesIntentUnacknowledged(t *testing.T) {
	s, db, a := experienceHTTP(t)
	seedExperienceHTTP(t, db, experienceFixture(t).Initial)
	if _, e := db.Exec("CREATE TRIGGER fail_delete BEFORE DELETE ON memory_experience_policies BEGIN SELECT RAISE(ROLLBACK,'synthetic');END"); e != nil {
		t.Fatal(e)
	}
	before, e := a.Head(context.Background())
	if e != nil {
		t.Fatal(e)
	}
	r := httptest.NewRequest("DELETE", experiencePrefix+"/p", strings.NewReader(`{"sceneId":"s","expectedVersion":1}`))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Authorization", "Bearer "+fixtureToken)
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	after, e := a.Head(context.Background())
	if e != nil {
		t.Fatal(e)
	}
	if after.Sequence <= before.Sequence || w.Code != 503 {
		t.Fatal(before, after, w.Code)
	}
	var count int
	if e = db.QueryRow("SELECT count(*) FROM memory_experience_policies WHERE id='p'").Scan(&count); e != nil || count != 1 {
		t.Fatal(count, e)
	}
}
func TestExperienceCommitFailureCannotAccept(t *testing.T) {
	s, db, _ := experienceHTTP(t)
	seedExperienceHTTP(t, db, experienceFixture(t).Initial)
	db.SetMaxOpenConns(1)
	conn, e := db.Conn(context.Background())
	if e != nil {
		t.Fatal(e)
	}
	calls := 0
	e = conn.Raw(func(v any) error {
		v.(*sqlite3.SQLiteConn).RegisterCommitHook(func() int { calls++; return 1 })
		return nil
	})
	conn.Close()
	if e != nil {
		t.Fatal(e)
	}
	r := httptest.NewRequest("DELETE", experiencePrefix+"/p", strings.NewReader(`{"sceneId":"s","expectedVersion":1}`))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Authorization", "Bearer "+fixtureToken)
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	conn, e = db.Conn(context.Background())
	if e != nil {
		t.Fatal(e)
	}
	e = conn.Raw(func(v any) error { v.(*sqlite3.SQLiteConn).RegisterCommitHook(nil); return nil })
	conn.Close()
	if e != nil {
		t.Fatal(e)
	}
	if calls != 1 || w.Code != 503 {
		t.Fatal(calls, w.Code, w.Body.String())
	}
	var count int
	if e = db.QueryRow("SELECT count(*) FROM memory_experience_policies WHERE id='p'").Scan(&count); e != nil || count != 1 {
		t.Fatal(count, e)
	}
}

func TestExperienceFinalGateRejectsSceneAndConflictResponses(t *testing.T) {
	for _, body := range []string{`{"sceneId":"absent","expectedVersion":1}`, `{"sceneId":"s","expectedVersion":2}`} {
		t.Run(body, func(t *testing.T) {
			s, db, a := experienceHTTP(t)
			seedExperienceHTTP(t, db, experienceFixture(t).Initial)
			db.SetMaxOpenConns(1)
			conn, e := db.Conn(context.Background())
			if e != nil {
				t.Fatal(e)
			}
			calls := 0
			var hookError error
			e = conn.Raw(func(v any) error {
				v.(*sqlite3.SQLiteConn).RegisterCommitHook(func() int {
					calls++
					head, err := a.Head(context.Background())
					if err == nil {
						_, err = a.Append(context.Background(), recovery.Source{SceneID: "s", MessageID: "before-final-publication"}, head, false)
					}
					hookError = err
					return 0
				})
				return nil
			})
			conn.Close()
			if e != nil {
				t.Fatal(e)
			}
			r := httptest.NewRequest("POST", experiencePrefix+"/p/validate", strings.NewReader(body))
			r.Header.Set("Content-Type", "application/json")
			r.Header.Set("Authorization", "Bearer "+fixtureToken)
			w := httptest.NewRecorder()
			s.ServeHTTP(w, r)
			conn, e = db.Conn(context.Background())
			if e != nil {
				t.Fatal(e)
			}
			e = conn.Raw(func(v any) error { v.(*sqlite3.SQLiteConn).RegisterCommitHook(nil); return nil })
			conn.Close()
			if e != nil {
				t.Fatal(e)
			}
			if hookError != nil || calls != 1 || w.Code != 503 || strings.Contains(w.Body.String(), "Scene unavailable") || strings.Contains(w.Body.String(), "eligible") {
				t.Fatal(calls, hookError, w.Code, w.Body.String())
			}
		})
	}
}
