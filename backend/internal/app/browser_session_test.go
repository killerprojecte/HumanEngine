package app

import (
	"context"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestBrowserSessionLoopbackApplication(t *testing.T) {
	options := applicationOptions(t)
	options.Entries = []string{}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	app, err := Open(ctx, options)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := app.Shutdown(context.Background()); err != nil {
			t.Error(err)
		}
	})
	for _, transport := range []string{"http", "tls"} {
		t.Run(transport, func(t *testing.T) {
			start := httptest.NewServer
			if transport == "tls" {
				start = httptest.NewTLSServer
			}
			server := start(app)
			defer server.Close()
			browser := server.Client()
			browser.Jar, _ = cookiejar.New(nil)
			base, err := url.Parse(server.URL)
			if err != nil {
				t.Fatal(err)
			}
			do := func(client *http.Client, method, path, origin, bearer string, cookie *http.Cookie, body io.Reader) (int, []*http.Cookie) {
				t.Helper()
				req, err := http.NewRequestWithContext(ctx, method, server.URL+path, body)
				if err != nil {
					t.Fatal(err)
				}
				if origin != "" {
					req.Header.Set("Origin", origin)
					req.Header.Set("X-HumanEngine-Session", "1")
				}
				if bearer != "" {
					req.Header.Set("Authorization", bearer)
				}
				if cookie != nil {
					req.AddCookie(cookie)
				}
				resp, err := client.Do(req)
				if err != nil {
					t.Fatal(err)
				}
				defer resp.Body.Close()
				if _, err := io.Copy(io.Discard, resp.Body); err != nil {
					t.Fatal(err)
				}
				return resp.StatusCode, resp.Cookies()
			}
			tokenJSON := `{"token":"synthetic-token"}`
			const endpoint = "/api/auth/session"
			// A real chunked request with no declared Content-Length exercises
			// the stream byte bound, not just the request header check.
			chunked := struct{ io.Reader }{strings.NewReader(`{"pad":"` + strings.Repeat("x", 9000) + `"}`)}
			if status, cookies := do(browser, "POST", endpoint, server.URL, "", nil, chunked); status != 413 || len(cookies) != 0 {
				t.Fatalf("chunked login limit: %d %v", status, cookies)
			}
			if status, cookies := do(browser, "GET", "/api/status", "", "Bearer synthetic-token", nil, nil); status != 200 || len(cookies) != 0 {
				t.Fatalf("direct bearer regressed: %d %v", status, cookies)
			}
			if status, cookies := do(browser, "POST", endpoint, "https://other.invalid", "", nil, strings.NewReader(tokenJSON)); status != 403 || len(cookies) != 0 {
				t.Fatalf("cross-origin login: %d %v", status, cookies)
			}
			if status, cookies := do(browser, "POST", endpoint, server.URL, "", nil, strings.NewReader(tokenJSON)); status != 200 ||
				len(cookies) != 1 || cookies[0].Secure != (transport == "tls") || !cookies[0].HttpOnly ||
				cookies[0].SameSite != http.SameSiteStrictMode || cookies[0].Path != "/api" || cookies[0].MaxAge != 604800 {
				t.Fatalf("session cookie: %d %v", status, cookies)
			}
			apiURL := base.ResolveReference(&url.URL{Path: "/api/logs"})
			stored := browser.Jar.Cookies(apiURL)
			if len(stored) != 1 || stored[0].Name != "humanengine-session" || len(browser.Jar.Cookies(base)) != 0 {
				t.Fatal("wrong session cookie scope")
			}
			first := stored[0]
			for _, path := range []string{endpoint, "/api/status", "/api/logs", "/api/logs/status"} {
				if status, cookies := do(browser, "GET", path, server.URL, "", nil, nil); status != 200 || len(cookies) != 0 {
					t.Fatalf("authenticated %s: %d %v", path, status, cookies)
				}
			}
			if status, cookies := do(browser, "GET", "/api/status", "", "", nil, nil); status != 401 || len(cookies) != 0 {
				t.Fatalf("cookie without marker reached API: %d %v", status, cookies)
			}
			if status, cookies := do(browser, "GET", "/api/status", server.URL, "Bearer wrong", nil, nil); status != 401 || len(cookies) != 0 {
				t.Fatalf("bad explicit bearer was replaced: %d %v", status, cookies)
			}
			if status, _ := do(browser, "POST", endpoint, server.URL, "", nil, strings.NewReader(tokenJSON)); status != 200 {
				t.Fatalf("rotation failed: %d", status)
			}
			second := browser.Jar.Cookies(apiURL)[0]
			other := *browser
			other.Jar = nil
			if first.Value == second.Value {
				t.Fatal("login reused a session ID")
			}
			if status, _ := do(&other, "GET", "/api/status", server.URL, "", first, nil); status != 401 {
				t.Fatalf("rotation left old cookie usable: %d", status)
			}
			if status, _ := do(browser, "DELETE", endpoint, "https://other.invalid", "", nil, nil); status != 403 {
				t.Fatalf("cross-origin logout: %d", status)
			}
			if status, _ := do(&other, "GET", "/api/status", server.URL, "", second, nil); status != 200 {
				t.Fatalf("rejected logout revoked session: %d", status)
			}
			if status, _ := do(browser, "DELETE", endpoint, server.URL, "", nil, nil); status != 200 {
				t.Fatalf("logout failed: %d", status)
			}
			if status, _ := do(&other, "GET", "/api/status", server.URL, "", second, nil); status != 401 {
				t.Fatalf("logout left captured session usable: %d", status)
			}
		})
	}

	t.Run("engine-unavailable", func(t *testing.T) {
		if err := app.Shutdown(ctx); err != nil {
			t.Fatal(err)
		}
		// Keep the gateway's HTTP host up, as Nitro can be when its Engine
		// is unavailable. The installed graph is already physically closed.
		server := httptest.NewServer(app)
		defer server.Close()
		client := server.Client()
		client.Jar, _ = cookiejar.New(nil)
		for _, tc := range []struct {
			method, path, body string
			status             int
		}{
			{"POST", "/api/auth/session", `{"token":"synthetic-token"}`, 200},
			{"GET", "/api/auth/session", "", 200},
			{"GET", "/api/status", "", 503},
			{"GET", "/api/logs", "", 503},
			{"DELETE", "/api/auth/session", "", 200},
			{"GET", "/api/auth/session", "", 401},
		} {
			req, err := http.NewRequestWithContext(ctx, tc.method, server.URL+tc.path, strings.NewReader(tc.body))
			if err != nil {
				t.Fatal(err)
			}
			req.Header.Set("Origin", server.URL)
			req.Header.Set("X-HumanEngine-Session", "1")
			resp, err := client.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			body, err := io.ReadAll(resp.Body)
			resp.Body.Close()
			if err != nil || resp.StatusCode != tc.status {
				t.Fatalf("%s %s: status=%d, body=%s, err=%v", tc.method, tc.path, resp.StatusCode, body, err)
			}
			if tc.status == http.StatusServiceUnavailable &&
				(!strings.Contains(string(body), `"error":`) || len(resp.Cookies()) != 0) {
				t.Fatalf("unavailable API published state: %s", body)
			}
		}
	})
}
