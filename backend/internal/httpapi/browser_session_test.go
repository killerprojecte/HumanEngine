package httpapi

import (
	"crypto/tls"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"
)

func TestBrowserSessionActualSource(t *testing.T) {
	raw, err := os.ReadFile("testdata/browser-session-source.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Origin, Token string
		Cases         []struct {
			Name, Method, Path, Cookie, Save, Raw string
			Browser, Secure, TLS, Restart         bool
			Configured                            *string
			Headers                               map[string]string
			Input                                 map[string]any
			Advance                               int64
			Want                                  struct {
				Status                              int
				Body                                any
				Cookie, Cache, Nosniff, ContentType string
			}
		}
	}
	if err := json.Unmarshal(raw, &fixture); err != nil {
		t.Fatal(err)
	}
	now := time.UnixMilli(1000)
	newOwner := func() *browserSessions {
		s := newBrowserSessions(false)
		s.now = func() time.Time { return now }
		return s
	}
	owner := newOwner()
	cookies := map[string]string{}
	opaque := regexp.MustCompile(`^humanengine-session=[A-Za-z0-9_-]{43}$`)
	for _, tc := range fixture.Cases {
		t.Run(tc.Name, func(t *testing.T) {
			if tc.Restart {
				owner = newOwner()
			}
			now = now.Add(time.Duration(tc.Advance) * time.Millisecond)
			owner.secure = tc.Secure
			method, path, origin := tc.Method, tc.Path, fixture.Origin
			if method == "" {
				method = http.MethodGet
			}
			if path == "" {
				path = sessionEndpoint
			}
			if tc.TLS {
				origin = strings.Replace(origin, "http:", "https:", 1)
			}
			body := tc.Raw
			if tc.Input != nil {
				if tc.Input["token"] == "{astral-too-long}" {
					tc.Input["token"] = strings.Repeat("😀", 1500) + strings.Repeat("x", 1097)
				}
				if tc.Input["pad"] == "{oversize}" {
					tc.Input["pad"] = strings.Repeat("x", 9000)
				}
				encoded, err := json.Marshal(tc.Input)
				if err != nil {
					t.Fatal(err)
				}
				body = string(encoded)
			}
			req := httptest.NewRequest(method, origin+path, strings.NewReader(body))
			if tc.TLS {
				req.TLS = new(tls.ConnectionState)
			}
			if tc.Browser {
				req.Header.Set(sessionMarker, "1")
				req.Header.Set("Origin", fixture.Origin)
			}
			if tc.Cookie != "" {
				req.Header.Set("Cookie", cookies[tc.Cookie])
			}
			for k, v := range tc.Headers {
				req.Header.Set(k, v)
				if k == "Content-Length" {
					var length int64
					if err := json.Unmarshal([]byte(v), &length); err != nil {
						t.Fatal(err)
					}
					req.ContentLength = length
				}
			}
			token := fixture.Token
			if tc.Configured != nil {
				token = *tc.Configured
			}
			w := httptest.NewRecorder()
			if forwarded := owner.admit(w, req, token); forwarded != nil {
				w.Header().Set("Content-Type", "text/plain;charset=UTF-8")
				var bearer any
				if v, present := forwarded.Header["Authorization"]; present {
					bearer = strings.Join(v, ", ")
				}
				raw, _ := json.Marshal(map[string]any{"bearer": bearer})
				_, _ = w.Write(raw)
			}
			gotCookie := w.Header().Get("Set-Cookie")
			if tc.Save != "" {
				first, _, _ := strings.Cut(gotCookie, ";")
				if !opaque.MatchString(first) || strings.Contains(first, fixture.Token) {
					t.Fatal("gateway did not issue an opaque cookie")
				}
				cookies[tc.Save] = first
				gotCookie = strings.Replace(gotCookie, first, "humanengine-session={cookie}", 1)
			}
			var got any
			if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
				t.Fatal(err)
			}
			want := tc.Want
			if w.Code != want.Status || !reflect.DeepEqual(got, want.Body) ||
				gotCookie != want.Cookie || w.Header().Get("Cache-Control") != want.Cache ||
				w.Header().Get("X-Content-Type-Options") != want.Nosniff || w.Header().Get("Content-Type") != want.ContentType {
				t.Fatalf("HTTP=%d body=%s headers=%v; expected %d %+v %s %s %s %s",
					w.Code, w.Body, w.Header(), want.Status, want.Body, want.Cookie, want.Cache, want.Nosniff, want.ContentType)
			}
		})
	}
}

func TestBrowserSessionRetainsExistingFIFO(t *testing.T) {
	owner := newBrowserSessions(false)
	first, _ := owner.create("", "fixture")
	for range sessionCapacity {
		owner.create("", "fixture")
	}
	if len(owner.byID) != sessionCapacity {
		t.Fatal("existing session size limit changed")
	}
	if _, ok := owner.find(first, "fixture"); ok {
		t.Fatal("oldest session was not evicted")
	}
}

func TestBrowserSessionTokenDecoderUsesNodeInvalidUTF8Semantics(t *testing.T) {
	for _, tc := range []struct {
		bad  []byte
		want string
	}{
		{[]byte{0xff, 0xff}, "��"},
		{[]byte{0xe0, 0xa0}, "�"},
	} {
		raw := append([]byte(`{"token":"`), tc.bad...)
		raw = append(raw, []byte(`"}`)...)
		if got, ok := browserLoginToken(raw); !ok || got != tc.want {
			t.Errorf("node UTF-8 %x: got %q want %q", tc.bad, got, tc.want)
		}
	}
}
