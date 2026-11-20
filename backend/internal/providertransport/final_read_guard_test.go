package providertransport

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"sync/atomic"
	"testing"
	"time"
)

func TestNativeFinalReadGuardAfterSerialization(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"choices":[{"finish_reason":"stop","message":{"content":"ok"}}]}`)
	}))
	defer server.Close()
	client, provider := nativeTestClient(server)
	released := false
	request := Request{
		Provider: provider, Path: "chat/completions",
		Body:        map[string]any{"model": "synthetic", "messages": []any{map[string]any{"role": "user", "content": "synthetic"}}},
		Descriptor:  &Descriptor{ProviderID: provider.ID, ModelID: "synthetic", Operation: "complete", Role: "vision"},
		Measurement: func(Measurement) { released = true },
	}
	guard := func(context.Context) error {
		if released {
			return errors.New("memory_read_revoked")
		}
		return nil
	}
	ctx := WithFinalModelReadGuard(context.Background(), guard)
	if err := checkModelRead(ctx); err != nil {
		t.Fatal(err)
	}
	result, err := client.Request(ctx, request)
	if !released || result != nil || err == nil || err.Error() != "memory_read_revoked" || calls.Load() != 0 {
		t.Fatalf("late revocation reached provider: measured=%t result=%v err=%v calls=%d", released, result, err, calls.Load())
	}
	// An ordinary early callback is not replayed in the native transport.
	if _, err := client.Request(WithModelReadGuard(context.Background(), guard), request); err != nil || calls.Load() != 1 {
		t.Fatalf("ordinary caller unexpectedly ran late guard: %v calls=%d", err, calls.Load())
	}
}

type blockedFinalStart struct {
	*SQLLedger
	entered chan string
	release chan struct{}
}

func (l *blockedFinalStart) Start(ctx context.Context, id string) (bool, error) {
	l.entered <- id
	<-l.release
	return l.SQLLedger.Start(ctx, id)
}

func TestNativeFinalReadGuardCancellationDuringSQLStartRefundsUnsent(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		calls.Add(1)
	}))
	defer server.Close()
	client, provider := nativeTestClient(server)
	db, sqlLedger := ledgerDatabase(t)
	ledger := &blockedFinalStart{SQLLedger: sqlLedger, entered: make(chan string, 1), release: make(chan struct{})}
	released := false
	defer func() {
		if !released {
			close(ledger.release)
		}
	}()
	client.Budgets = &testBudgets{&NativeGate{
		Ledger: ledger,
		Policy: func(context.Context) (BudgetPolicy, error) { return BudgetPolicy{DailyNanoUSD: 100_000_000}, nil },
		Mode:   "best_effort", MaxOutputTokens: 10,
		Contract: func(Descriptor) *ProviderContract {
			return &ProviderContract{Version: "fixture", InputCategories: []string{"inputUsdPerMillion"}}
		},
	}}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var late atomic.Int32
	guarded := WithFinalModelReadGuard(ctx, func(context.Context) error {
		late.Add(1)
		// A real cancellation can occur during SQL Start; a callback returning
		// nil despite that cancellation cannot authorize the request.
		return nil
	})
	done := make(chan error, 1)
	go func() {
		_, err := client.Request(guarded, Request{
			Provider: provider, Path: "chat/completions", Body: map[string]any{},
			Descriptor: &Descriptor{ProviderID: provider.ID, Protocol: provider.Protocol, ModelID: "synthetic", Operation: "complete", Role: "vision",
				Prices: map[string]float64{"inputUsdPerMillion": 1, "outputUsdPerMillion": 1}},
		})
		done <- err
	}()
	var id string
	select {
	case id = <-ledger.entered:
	case err := <-done:
		t.Fatalf("request did not reach native SQL Start: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("request did not reach native SQL Start")
	}
	var state string
	var known, held int64
	if err := db.QueryRow("SELECT state,known_nano,held_nano FROM budget_attempts WHERE id=?", id).Scan(&state, &known, &held); err != nil || state != "reserved" || known != 0 || held <= 0 {
		t.Fatalf("expected held, unstarted reservation at barrier: %s %d %d %v", state, known, held, err)
	}
	if late.Load() != 0 {
		t.Fatal("final source read ran before blocking SQL Start")
	}
	cancel()
	close(ledger.release)
	released = true
	select {
	case err := <-done:
		if err == nil || err.Error() != "request_cancelled" || calls.Load() != 0 || late.Load() != 1 {
			t.Fatalf("late cancel did not stop upstream: %v; calls=%d; final reads=%d", err, calls.Load(), late.Load())
		}
	case <-time.After(5 * time.Second):
		t.Fatal("guarded attempt did not drain")
	}
	var start, settled string
	if err := db.QueryRow("SELECT state,known_nano,held_nano,started_at,settled_at FROM budget_attempts WHERE id=?", id).Scan(&state, &known, &held, &start, &settled); err != nil || state != "released" || known != 0 || held != 0 || start == "" || settled == "" {
		t.Fatalf("started, provably unsent attempt was not refunded: %s %d %d %q %q %v", state, known, held, start, settled, err)
	}
}

func TestNativeFinalReadGuardForeignBudgetStaysUnstarted(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { calls.Add(1) }))
	defer server.Close()
	client, provider := nativeTestClient(server)
	ledger := &testLedger{}
	client.Budgets = &testBudgets{&NativeGate{Ledger: ledger, Policy: func(context.Context) (BudgetPolicy, error) { return BudgetPolicy{}, nil }, Mode: "best_effort", MaxOutputTokens: 10}}
	guarded := WithFinalModelReadGuard(context.Background(), func(context.Context) error { return nil })
	_, err := client.Request(guarded, Request{Provider: provider, Path: "chat/completions", Body: map[string]any{},
		Descriptor: &Descriptor{ProviderID: provider.ID, Protocol: provider.Protocol, ModelID: "synthetic", Operation: "complete", Role: "vision"}})
	if err == nil || err.Error() != "budget_protocol_unsupported" || calls.Load() != 0 || !slices.Equal(ledger.calls, []string{"reserve", "release"}) {
		t.Fatalf("foreign ledger allowed a started guarded attempt: %v; calls=%d; accounting=%v", err, calls.Load(), ledger.calls)
	}
}

func TestOrdinaryNativeStartStillPrecedesMeasurement(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"usage":{"prompt_tokens":2,"completion_tokens":1,"total_tokens":3}}`)
	}))
	defer server.Close()
	client, provider := nativeTestClient(server)
	ledger := &testLedger{}
	client.Budgets = &testBudgets{&NativeGate{Ledger: ledger, Policy: func(context.Context) (BudgetPolicy, error) { return BudgetPolicy{}, nil }, Mode: "best_effort", MaxOutputTokens: 10}}
	_, err := client.Request(context.Background(), Request{Provider: provider, Path: "chat/completions", Body: map[string]any{},
		Descriptor: &Descriptor{ProviderID: provider.ID, Protocol: provider.Protocol, ModelID: "ordinary", Operation: "complete", Role: "planner"},
		Measurement: func(Measurement) {
			if !slices.Equal(ledger.calls, []string{"reserve", "start"}) {
				t.Errorf("ordinary measurement moved ahead of native Start: %v", ledger.calls)
			}
		},
	})
	if err != nil || !slices.Equal(ledger.calls, []string{"reserve", "start", "settle"}) {
		t.Fatalf("ordinary accounting changed: %v %v", err, ledger.calls)
	}
}
