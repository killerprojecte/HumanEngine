package main

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRuntimePreviewUIMux(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "_nuxt"), 0700); err != nil {
		t.Fatal(err)
	}
	for name, contents := range map[string]string{
		"index.html":    "<title>HumanEngine index</title>",
		"200.html":      "<title>HumanEngine shell</title>",
		"_nuxt/hash.js": "export const preview = true;",
	} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(contents), 0600); err != nil {
			t.Fatal(err)
		}
	}
	outside := filepath.Join(t.TempDir(), "not-public")
	if err := os.WriteFile(outside, []byte("outside"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "escape.js")); err != nil {
		t.Fatal(err)
	}
	ui, err := openPreviewUI(root)
	if err != nil {
		t.Fatal(err)
	}
	defer ui.root.Close()
	var wantAPI *http.Request
	mux := ui.handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r != wantAPI || r.Header.Get("X-HumanEngine-Session") != "1" {
			t.Fatal("API request was rewritten")
		}
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = io.WriteString(w, "API owner response")
	}))
	for _, c := range []struct {
		name, method, url, want, cache string
		status                         int
		api                            bool
	}{
		{"index", "GET", "/", "HumanEngine index", "no-cache", 200, false},
		{"direct-client-route", "GET", "/chat?scene=example", "HumanEngine shell", "no-cache", 200, false},
		{"route-head", "HEAD", "/settings", "", "no-cache", 200, false},
		{"immutable-asset", "GET", "/_nuxt/hash.js", "export const", "public, max-age=31536000, immutable", 200, false},
		{"missing-asset", "GET", "/_nuxt/missing", "404", "", 404, false},
		{"missing-file", "GET", "/missing.png", "404", "", 404, false},
		{"plain-assets-no-file-escape", "GET", "/escape.js", "404", "", 404, false},
		{"non-canonical-path", "GET", "/x/../foo", "404", "", 404, false},
		{"static-write", "POST", "/", "", "", 405, false},
		{"api-prefix", "PUT", "/api/settings?opaque=1", "API owner response", "", 503, true},
		{"bare-api", "GET", "/api", "API owner response", "", 503, true},
		{"encoded-api-prefix", "GET", "/api%2Funknown", "API owner response", "", 503, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			req := httptest.NewRequest(c.method, c.url, strings.NewReader("request-body"))
			req.Header.Set("Accept", "text/html")
			if c.api {
				wantAPI = req
				req.Header.Set("X-HumanEngine-Session", "1")
			}
			got := httptest.NewRecorder()
			mux.ServeHTTP(got, req)
			if got.Code != c.status || got.Header().Get("Cache-Control") != c.cache ||
				!strings.Contains(got.Body.String(), c.want) || (c.want == "" && got.Body.Len() != 0) {
				t.Fatalf("status %d cache %q body %q", got.Code, got.Header().Get("Cache-Control"), got.Body.String())
			}
		})
	}
}

func TestRuntimePreviewUIUnavailableBeforeEnrollment(t *testing.T) {
	root := t.TempDir()
	database := filepath.Join(root, "live", "humanengine.sqlite")
	err := runRuntimePreview(context.Background(), database, "127.0.0.1:0", runtimeConfig{uiRoot: root}, io.Discard, io.Discard)
	if err == nil || err.Error() != "runtime_preview_ui_shell_missing" {
		t.Fatalf("expected frontend preflight failure: %v", err)
	}
	if _, err := os.Stat(filepath.Dir(database)); !os.IsNotExist(err) {
		t.Fatalf("writable install touched despite missing frontend: %v", err)
	}
}
