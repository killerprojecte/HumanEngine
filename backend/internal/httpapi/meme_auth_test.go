package httpapi

import (
	"context"
	"github.com/killerprojecte/HumanEngine/backend/internal/memeengine"
	"github.com/mattn/go-sqlite3"
	"io"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

type authBody struct{ reads int }

func (b *authBody) Read([]byte) (int, error) { b.reads++; return 0, io.EOF }
func (*authBody) Close() error               { return nil }
func TestMemeEveryRouteRejectsAuthBeforeDependencies(t *testing.T) {
	routes := [][2]string{{"GET", "/api/memes"}, {"HEAD", "/api/memes"}, {"GET", "/api/memes/a"}, {"GET", "/api/memes/a/file"}, {"GET", "/api/memes/a/thumbnail"}, {"PATCH", "/api/memes/a"}, {"POST", "/api/memes/a/inclusion"}, {"DELETE", "/api/memes/a"}, {"POST", "/api/memes/a/analyze"}, {"POST", "/api/memes/import"}}
	// Deliberately inaccessible native dependencies prove authentication cannot
	// reach even RootContext, hence cannot enter DB/FS/provider/codec operations.
	poisoned := &Server{token: fixtureToken, memes: &memeengine.Service{}}
	for _, route := range routes {
		body := &authBody{}
		r := httptest.NewRequest(route[0], route[1], body)
		r.Header.Set("Authorization", "Bearer wrong")
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		poisoned.ServeHTTP(w, r)
		if w.Code != 401 || body.reads != 0 {
			t.Fatalf("%v: status %d body reads %d", route, w.Code, body.reads)
		}
	}
	// Also exercise an actually composed native owner, with a driver authorizer
	// counting every SQL operation. No recovery readiness or writer is entered.
	server, db, _ := memeHTTP(t)
	db.SetMaxOpenConns(1)
	conn, e := db.Conn(context.Background())
	if e != nil {
		t.Fatal(e)
	}
	var sqlCalls atomic.Int64
	if e = conn.Raw(func(raw any) error {
		raw.(*sqlite3.SQLiteConn).RegisterAuthorizer(func(int, string, string, string) int { sqlCalls.Add(1); return sqlite3.SQLITE_DENY })
		return nil
	}); e != nil {
		t.Fatal(e)
	}
	conn.Close()
	for _, route := range routes {
		r := httptest.NewRequest(route[0], route[1], strings.NewReader(`{}`))
		r.Header.Set("Authorization", "Bearer wrong")
		w := httptest.NewRecorder()
		server.ServeHTTP(w, r)
		if w.Code != 401 {
			t.Fatalf("%v: %d", route, w.Code)
		}
	}
	if n := sqlCalls.Load(); n != 0 {
		t.Fatalf("unauthorized route touched SQL %d times", n)
	}
	conn, e = db.Conn(context.Background())
	if e != nil {
		t.Fatal(e)
	}
	if e = conn.Raw(func(raw any) error { raw.(*sqlite3.SQLiteConn).RegisterAuthorizer(nil); return nil }); e != nil {
		t.Fatal(e)
	}
	conn.Close()
}
