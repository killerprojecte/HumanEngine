package httpapi

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"sync/atomic"
	"testing"
	"time"

	"github.com/killerprojecte/HumanEngine/backend/internal/modelservice"
	"github.com/killerprojecte/HumanEngine/backend/internal/plugins"
	"github.com/killerprojecte/HumanEngine/backend/internal/providertransport"
	"github.com/killerprojecte/HumanEngine/backend/internal/recovery"
)

type providerReadCredentials struct{}

func (providerReadCredentials) Resolve(context.Context, providertransport.Provider) (string, error) {
	return "", nil
}
func providerReadFixture(t *testing.T, base, protocol string) (*Server, *providertransport.NativeModels, *plugins.V1Host) {
	t.Helper()
	s, db, a, _, _ := configurationFixture(t)
	s.token = "synthetic-token"
	host := plugins.NewV1Host("node", io.Discard)
	t.Cleanup(func() { host.Close(context.Background()) })
	retry, timeout := 0.0, 1000.0
	policy, e := modelservice.New(modelservice.Config{Providers: []modelservice.Provider{{ID: "p", Protocol: protocol, RetryCount: &retry, TimeoutMS: &timeout}}}, modelservice.Options{})
	if e != nil {
		t.Fatal(e)
	}
	m := &providertransport.NativeModels{Policy: policy, Host: host, Client: &providertransport.Client{Credentials: providerReadCredentials{}, Budgets: providertransport.ExplicitOff{}}, Providers: map[string]providertransport.Provider{"p": {ID: "p", Protocol: protocol, BaseURL: base}}, ProviderSnapshots: map[string]map[string]any{"p": {"id": "p", "protocol": protocol, "baseUrl": base}}}
	publisher, e := recovery.NewReadPublisher(context.Background(), db, a)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { publisher.CloseAndWait(context.Background()) })
	if e = s.WithProviderReads(m, publisher); e != nil {
		t.Fatal(e)
	}
	return s, m, host
}
func providerReadRequest(s *Server, ctx context.Context, method, path, token string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, nil).WithContext(ctx)
	r.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	return w
}
func TestProviderReadsActualSourceHTTP(t *testing.T) {
	raw, e := os.ReadFile("testdata/provider-reads-source.json")
	if e != nil {
		t.Fatal(e)
	}
	var source struct {
		Cases []struct {
			Name, Path, Method, Text                  string
			Custom, Disabled, Empty, Failure, BadAuth bool
			Status, Calls                             int
			ResponseHeaders                           map[string]*string
		}
	}
	if e = json.Unmarshal(raw, &source); e != nil {
		t.Fatal(e)
	}
	for _, c := range source.Cases {
		t.Run(c.Name, func(t *testing.T) {
			var calls atomic.Int32
			peer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				w.Header().Set("Content-Type", "application/json")
				if c.Failure {
					w.WriteHeader(500)
					io.WriteString(w, `{"error":"synthetic-secret"}`)
				} else if c.Empty {
					io.WriteString(w, `{"data":[]}`)
				} else {
					io.WriteString(w, `{"data":[{"id":"z"},{"id":"a"},{"id":"z"}]}`)
				}
			}))
			defer peer.Close()
			protocol := "openai-chat"
			if c.Custom {
				protocol = "fixture:custom"
			}
			s, _, host := providerReadFixture(t, peer.URL, protocol)
			if c.Custom {
				path := filepath.Join(t.TempDir(), "fixture.mjs")
				code := `export default{manifest:{id:'fixture',configSchema:{type:'object'},capabilities:['model-protocol']},setup(c){c.registerModelProtocol('custom','Fixture',{complete(){return{text:'',toolCalls:[]}},listModels(){return['z','a','z']}})}}`
				if e = os.WriteFile(path, []byte(code), 0600); e != nil {
					t.Fatal(e)
				}
				if _, e = host.Enable(context.Background(), "fixture", path, map[string]any{}); e != nil {
					t.Fatal(e)
				}
				if c.Disabled {
					if e = host.Disable(context.Background(), "fixture"); e != nil {
						t.Fatal(e)
					}
				}
			}
			token := "synthetic-token"
			if c.BadAuth {
				token = "bad"
			}
			w := providerReadRequest(s, context.Background(), c.Method, c.Path, token)
			text := regexp.MustCompile(`"requestId":"[^"]+"`).ReplaceAllString(w.Body.String(), `"requestId":"<request-id>"`)
			if w.Code != c.Status || text != c.Text || int(calls.Load()) != c.Calls {
				t.Fatalf("got %d %s calls%d; source %d %s calls%d", w.Code, text, calls.Load(), c.Status, c.Text, c.Calls)
			}
			for key, want := range c.ResponseHeaders {
				got := w.Header().Get(key)
				if want == nil {
					if got != "" {
						t.Errorf("%s=%q", key, got)
					}
				} else if got != *want {
					t.Errorf("%s=%q want%q", key, got, *want)
				}
			}
		})
	}
}
func TestProviderReadsCancellationDoesNotHoldPublication(t *testing.T) {
	entered := make(chan struct{})
	cancelled := make(chan struct{})
	peer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { close(entered); <-r.Context().Done(); close(cancelled) }))
	defer peer.Close()
	s, _, _ := providerReadFixture(t, peer.URL, "openai-chat")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() { done <- providerReadRequest(s, ctx, "GET", "/api/providers/p/models", "synthetic-token") }()
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("provider not entered")
	}
	concurrent := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		concurrent <- providerReadRequest(s, context.Background(), "GET", "/api/model-protocols", "synthetic-token")
	}()
	select {
	case w := <-concurrent:
		if w.Code != 200 {
			t.Fatal(w.Code, w.Body.String())
		}
	case <-time.After(time.Second):
		t.Fatal("network IO held publisher")
	}
	cancel()
	select {
	case w := <-done:
		if w.Body.Len() != 0 {
			t.Fatal("cancelled response published", w.Body.String())
		}
	case <-time.After(3 * time.Second):
		t.Fatal("request did not cancel")
	}
	select {
	case <-cancelled:
	case <-time.After(time.Second):
		t.Fatal("transport did not cancel")
	}
}
func TestProviderReadsUncomposedAndUnavailable(t *testing.T) {
	s, _, _ := providerReadFixture(t, "http://127.0.0.1:1", "openai-chat")
	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/api/model-protocols", nil)
	r.Header.Set("Authorization", "Bearer synthetic-token")
	s.ServeUnavailable(w, r)
	if w.Code != 503 {
		t.Fatal(w.Code)
	}
	s.providerReads = nil
	w = providerReadRequest(s, context.Background(), "GET", "/api/model-protocols", "synthetic-token")
	if w.Code != 503 {
		t.Fatal(w.Code)
	}
	if s.WithProviderReads(nil, s.providerPublisher) == nil {
		t.Fatal("accepted missing owner")
	}
}
