package candidateservice

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestExplicitHabitConstructorAndOwnedShutdown(t *testing.T) {
	old, db, path := fixture(t)
	if e := old.Close(); e != nil {
		t.Fatal(e)
	}
	s, e := OpenWithHabits(context.Background(), path, "test", nil)
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	tcp := httptest.NewServer(s)
	defer tcp.Close()
	if code, body := get(t, tcp, "GET", "/api/habits"); code != 200 || body != "[]" {
		t.Fatal(code, body)
	}
	if _, e = db.Exec(`INSERT INTO habits VALUES('h','aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa','{"id":"h","kind":"sceneRule","sceneId":"s","content":"synthetic","sources":[],"weight":0,"confidence":0,"status":"invalidated","locked":false,"learnedAt":"2026-11-01T00:00:00.000Z"}')`); e != nil {
		t.Fatal(e)
	}
	if code, body := get(t, tcp, "DELETE", "/api/habits/h"); code != 200 || body != `{"ok":true}` {
		t.Fatal(code, body)
	}
	if e = s.Close(); e != nil {
		t.Fatal(e)
	}
	if code, body := get(t, tcp, "GET", "/api/habits"); code != 503 || strings.Contains(body, "synthetic") {
		t.Fatal(code, body)
	}
}
func TestHabitConstructionFailureReleasesResources(t *testing.T) {
	old, db, path := fixture(t)
	if e := old.Close(); e != nil {
		t.Fatal(e)
	}
	if _, e := db.Exec("CREATE TABLE ddl_conflict(id TEXT); CREATE INDEX habits ON ddl_conflict(id)"); e != nil {
		t.Fatal(e)
	}
	for i := 0; i < 2; i++ {
		s, e := OpenWithHabits(context.Background(), path, "test", nil)
		if e == nil {
			if s != nil {
				s.Close()
			}
			t.Fatal("real DDL name conflict accepted")
		}
		if !strings.Contains(e.Error(), "index") || !strings.Contains(e.Error(), "habits") {
			t.Fatal("unexpected constructor failure", e)
		}
	}
	if _, e := db.Exec("DROP INDEX habits; DROP TABLE ddl_conflict"); e != nil {
		t.Fatal(e)
	}
	s, e := OpenWithHabits(context.Background(), path, "test", nil)
	if e != nil {
		t.Fatal("failed constructor retained resources", e)
	}
	if e = s.Close(); e != nil {
		t.Fatal(e)
	}
}
