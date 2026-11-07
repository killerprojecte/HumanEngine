package httpapi

import (
	"net/http/httptest"
	"strings"
	"testing"
)

func TestUnavailableNeverTouchesDependencies(t *testing.T) {
	// Deliberately no DB, authority, or schemas: shutdown must not access them.
	s := &Server{token: "test"}
	for _, test := range []struct {
		method, path, token string
		code                int
		body                string
	}{
		{"GET", "/api/settings", "test", 503, "Request unavailable"},
		{"GET", "/api/scenes", "wrong", 401, "Authentication required"},
		{"POST", "/api/settings", "test", 503, "has not yet migrated"},
	} {
		r := httptest.NewRequest(test.method, test.path, nil)
		r.Header.Set("Authorization", "Bearer "+test.token)
		w := httptest.NewRecorder()
		s.ServeUnavailable(w, r)
		if w.Code != test.code || !strings.Contains(w.Body.String(), test.body) {
			t.Fatalf("%d %s", w.Code, w.Body.String())
		}
		if w.Header().Get("Cache-Control") != "no-store" || w.Header().Get("X-Request-ID") == "" || w.Header().Get("Content-Type") != "application/json" {
			t.Fatal("shutdown headers changed")
		}
	}
}
