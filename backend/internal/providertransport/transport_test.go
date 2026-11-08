package providertransport

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/killerprojecte/HumanEngine/backend/internal/modelservice"
)

type testCredentials string

func (c testCredentials) Resolve(context.Context, Provider) (string, error) { return string(c), nil }

type testBudgets struct{ gate BudgetGate }

func (b *testBudgets) Current(context.Context) (BudgetGate, error) { return b.gate, nil }

type testLedger struct {
	mu      sync.Mutex
	states  map[string]string
	calls   []string
	failure string
}

func (l *testLedger) Reserve(_ context.Context, r Reservation, _ BudgetPolicy) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.states == nil {
		l.states = map[string]string{}
	}
	if _, ok := l.states[r.ID]; ok {
		return errors.New("budget_idempotency_conflict")
	}
	l.states[r.ID] = "reserved"
	l.calls = append(l.calls, "reserve")
	return nil
}
func (l *testLedger) Start(_ context.Context, id string) (bool, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.states[id] != "reserved" {
		return false, nil
	}
	l.states[id] = "started"
	l.calls = append(l.calls, "start")
	return true, nil
}
func (l *testLedger) Settle(_ context.Context, id string, _ map[string]any) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.failure == "settle" {
		return errors.New("synthetic secret database error")
	}
	if l.states[id] != "started" {
		return errors.New("budget_invalid_transition")
	}
	l.states[id] = "complete"
	l.calls = append(l.calls, "settle")
	return nil
}
func (l *testLedger) Unknown(_ context.Context, id string) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.failure == "unknown" {
		return errors.New("synthetic secret")
	}
	if l.states[id] != "started" {
		return errors.New("budget_invalid_transition")
	}
	l.states[id] = "unknown"
	l.calls = append(l.calls, "unknown")
	return nil
}
func (l *testLedger) Release(_ context.Context, id string) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.states[id] != "reserved" {
		return errors.New("budget_release_started")
	}
	l.states[id] = "released"
	l.calls = append(l.calls, "release")
	return nil
}
func nativeTestClient(server *httptest.Server) (*Client, Provider) {
	return &Client{HTTP: server.Client(), Credentials: testCredentials("synthetic-key"), Budgets: ExplicitOff{}}, Provider{ID: "fixture", Protocol: "openai-chat", BaseURL: server.URL}
}
func TestActualHTTPFormationAndJSONFallback(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" || r.Method != "POST" || r.Header.Get("Authorization") != "Bearer synthetic-key" || r.Header.Get("Accept") != "text/event-stream, application/json" {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"choices":[{"finish_reason":"stop","message":{"content":"ok"}}]}`)
	}))
	defer server.Close()
	c, p := nativeTestClient(server)
	out, e := c.Request(context.Background(), Request{Provider: p, Path: "chat/completions", Body: map[string]any{"stream": true}})
	if e != nil || out["choices"] == nil {
		t.Fatalf("%v %v", out, e)
	}
}
func TestPartialStreamNeverRetries(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"partial\"}}]}\n\n")
	}))
	defer server.Close()
	c, p := nativeTestClient(server)
	progress := 0
	_, e := c.Request(context.Background(), Request{Provider: p, Path: "chat/completions", Body: map[string]any{"stream": true}, FirstContent: func() { progress++ }})
	me, ok := e.(*modelservice.ModelError)
	if !ok || me.Code != "provider_stream_interrupted" || me.Retryable || !modelservice.StopsFallback(me) || requests != 1 || progress != 1 {
		t.Fatalf("requests=%d progress=%d err=%v", requests, progress, e)
	}
}
func TestHTTPBudgetSettlementAndUnknown(t *testing.T) {
	for _, tc := range []struct{ name, body, failure, want string }{{"success", `{"usage":{"prompt_tokens":2,"completion_tokens":1,"total_tokens":3}}`, "", "settle"}, {"invalid", "not-json", "", "unknown"}, {"settlement-failure", `{"usage":{"prompt_tokens":2,"completion_tokens":1,"total_tokens":3}}`, "settle", "unknown"}} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				fmt.Fprint(w, tc.body)
			}))
			defer server.Close()
			c, p := nativeTestClient(server)
			ledger := &testLedger{failure: tc.failure}
			gate := &NativeGate{Ledger: ledger, Policy: func(context.Context) (BudgetPolicy, error) { return BudgetPolicy{}, nil }, Mode: "best_effort", MaxOutputTokens: 10}
			c.Budgets = &testBudgets{gate}
			_, e := c.Request(context.Background(), Request{Provider: p, Path: "chat/completions", Body: map[string]any{}, Descriptor: &Descriptor{ProviderID: p.ID, Protocol: p.Protocol, ModelID: "m", Operation: "complete"}})
			if len(ledger.calls) != 3 || ledger.calls[2] != tc.want {
				t.Fatalf("calls %v error %v", ledger.calls, e)
			}
			if tc.failure != "" && (e == nil || strings.Contains(e.Error(), "secret")) {
				t.Fatalf("unsafe accounting error %v", e)
			}
		})
	}
}
func TestCancellationClosesRealHTTP(t *testing.T) {
	entered := make(chan struct{})
	cancelled := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { close(entered); <-r.Context().Done(); close(cancelled) }))
	defer server.Close()
	c, p := nativeTestClient(server)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { _, e := c.Request(ctx, Request{Provider: p, Path: "models"}); done <- e }()
	<-entered
	cancel()
	select {
	case e := <-done:
		if e == nil || e.Error() != "request_cancelled" {
			t.Fatal(e)
		}
	case <-time.After(time.Second):
		t.Fatal("HTTP cancellation did not settle")
	}
	select {
	case <-cancelled:
	case <-time.After(time.Second):
		t.Fatal("request body not cancelled")
	}
}
func TestRedirectDoesNotTransmitCredential(t *testing.T) {
	secondCalls := 0
	second := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { secondCalls++ }))
	defer second.Close()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, second.URL, 302) }))
	defer server.Close()
	c, p := nativeTestClient(server)
	_, e := c.Request(context.Background(), Request{Provider: p, Path: "models"})
	if e == nil || secondCalls != 0 {
		t.Fatalf("redirect sent=%d err=%v", secondCalls, e)
	}
}
