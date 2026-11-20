package httpapi

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http/httptest"
	"os"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"github.com/killerprojecte/HumanEngine/backend/internal/recovery"
)

func TestRulesHTTPActualSource(t *testing.T) {
	raw, err := os.ReadFile("testdata/rules-http-source.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Cases []struct {
			Name, Path, Key, Method, Text, Body string
			Seeded, Saved                       *string
			Status                              int
			Headers, ResponseHeaders            map[string]string
		}
	}
	if err := json.Unmarshal(raw, &fixture); err != nil {
		t.Fatal(err)
	}
	requestUUID := regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)
	for _, c := range fixture.Cases {
		t.Run(c.Name, func(t *testing.T) {
			s, db, authority, _, runtime := configurationFixture(t)
			ctx := context.Background()
			if c.Key == "" {
				c.Key = "rules"
			}
			if c.Seeded != nil {
				if _, err := db.Exec("INSERT INTO config(key,value) VALUES(?,?)", c.Key, *c.Seeded); err != nil {
					t.Fatal(err)
				}
			}
			head, err := authority.Head(ctx)
			if err != nil {
				t.Fatal(err)
			}
			gate, err := recovery.ReadGate(ctx, db)
			if err != nil {
				t.Fatal(err)
			}
			var revision string
			if err := db.QueryRow("SELECT revision FROM recovery_security_state WHERE id=1").Scan(&revision); err != nil {
				t.Fatal(err)
			}
			req := httptest.NewRequest(c.Method, c.Path, strings.NewReader(c.Body))
			for key, value := range c.Headers {
				req.Header.Set(key, value)
			}
			w := httptest.NewRecorder()
			s.ServeHTTP(w, req)
			id := w.Header().Get("X-Request-ID")
			if id == c.Headers["X-Request-ID"] || !requestUUID.MatchString(id) {
				t.Fatalf("expected a fresh request UUID, got %q", id)
			}
			if got := strings.ReplaceAll(w.Body.String(), id, "<request-id>"); w.Code != c.Status || got != c.Text {
				t.Fatalf("response %d %q want %d %q", w.Code, got, c.Status, c.Text)
			}
			for key, want := range c.ResponseHeaders {
				if got := w.Header().Get(key); got != want {
					t.Fatalf("header %s=%q want %q", key, got, want)
				}
			}
			var saved *string
			if err := db.QueryRow("SELECT value FROM config WHERE key=?", c.Key).Scan(&saved); err != nil && err != sql.ErrNoRows {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(saved, c.Saved) {
				t.Fatalf("stored rules %v want %v", saved, c.Saved)
			}
			afterHead, err := authority.Head(ctx)
			if err != nil || afterHead != head {
				t.Fatalf("authority head changed: %+v %v", afterHead, err)
			}
			afterGate, err := recovery.ReadGate(ctx, db)
			if err != nil || !reflect.DeepEqual(afterGate, gate) {
				t.Fatalf("recovery gate changed: %+v %v", afterGate, err)
			}
			var current string
			if err := db.QueryRow("SELECT revision FROM recovery_security_state WHERE id=1").Scan(&current); err != nil || current != revision || runtime.calls != 0 {
				t.Fatalf("runtime/security state changed: %q %d %v", current, runtime.calls, err)
			}
		})
	}
}
