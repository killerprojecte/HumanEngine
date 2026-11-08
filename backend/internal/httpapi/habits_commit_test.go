package httpapi

import (
	"context"
	"github.com/mattn/go-sqlite3"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestHabitHTTPCommitFailureCannotAccept(t *testing.T) {
	s, db, _, _ := habitHTTP(t)
	_, e := db.Exec("INSERT INTO habits VALUES('h','aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa',?)", habitRecord)
	if e != nil {
		t.Fatal(e)
	}
	db.SetMaxOpenConns(1)
	conn, e := db.Conn(context.Background())
	if e != nil {
		t.Fatal(e)
	}
	var commits atomic.Int32
	e = conn.Raw(func(v any) error {
		v.(*sqlite3.SQLiteConn).RegisterCommitHook(func() int { commits.Add(1); return 1 })
		return nil
	})
	conn.Close()
	if e != nil {
		t.Fatal(e)
	}
	r := httptest.NewRequest("PATCH", "/api/habits/h", strings.NewReader(`{"locked":true}`))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Authorization", "Bearer "+fixtureToken)
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	// The pool is quiescent again, so restore the fixture-only commit hook.
	conn, e = db.Conn(context.Background())
	if e != nil {
		t.Fatal(e)
	}
	e = conn.Raw(func(v any) error { v.(*sqlite3.SQLiteConn).RegisterCommitHook(nil); return nil })
	conn.Close()
	if e != nil {
		t.Fatal(e)
	}
	if commits.Load() != 1 {
		t.Fatalf("COMMIT hook was not reached exactly once: %d", commits.Load())
	}
	if w.Code != 503 || strings.Contains(w.Body.String(), "Synthetic") {
		t.Fatal(w.Code, w.Body.String())
	}
	var raw string
	if e = db.QueryRow("SELECT value FROM habits WHERE id='h'").Scan(&raw); e != nil {
		t.Fatal(e)
	}
	if raw != habitRecord {
		t.Fatal("commit failure persisted", raw)
	}
}
