package providertransport

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/killerprojecte/HumanEngine/backend/internal/modelcalls"
	"github.com/killerprojecte/HumanEngine/backend/internal/modelservice"
	"github.com/killerprojecte/HumanEngine/backend/internal/scheduler"
)

func TestModelCallsInitialQueueCancellationRecordsWithoutTransmission(t *testing.T) {
	sched := scheduler.NewModelScheduler()
	if err := sched.Configure(1, 0); err != nil {
		t.Fatal(err)
	}
	held := make(chan func(any, error), 1)
	sched.SubmitModel(context.Background(), 0, func(_ context.Context, done func(any, error)) { held <- done })
	release := <-held
	defer release(nil, nil)
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Error("queued cancelled model transmitted") }))
	defer server.Close()
	client, provider := nativeTestClient(server)
	provider.ID = "p"
	retry, timeout := float64(0), float64(1000)
	attemptEntered := make(chan struct{})
	policy, err := modelservice.New(modelservice.Config{Providers: []modelservice.Provider{{ID: "p", Protocol: provider.Protocol, RetryCount: &retry, TimeoutMS: &timeout}}, Models: []modelservice.Model{{ID: "m", ProviderID: "p", APIModelName: "synthetic", Capabilities: []string{"text"}}}, Routes: []modelservice.Route{{Role: "replyer", OrderedModelIDs: []string{"m"}}}}, modelservice.Options{Scheduler: sched, Observe: func(e modelservice.Event) {
		if e.Kind == "attempt" {
			close(attemptEntered)
		}
	}})
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var rows []modelcalls.Observation
	m := &NativeModels{Policy: policy, Client: client, Providers: map[string]Provider{"p": provider}, Calls: callSinkFunc(func(_ context.Context, e modelcalls.Observation) error {
		mu.Lock()
		rows = append(rows, e)
		mu.Unlock()
		return nil
	})}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	started := time.Now()
	go func() { _, err := m.Complete(ctx, diagnosticRequest()); done <- err }()
	<-attemptEntered
	// Observe(“attempt”) precedes the source's final cancellation check, so wait
	// for the actual scheduler queue, not only that diagnostic notification.
	for sched.Status().Queued == 0 {
		select {
		case err := <-done:
			t.Fatalf("ended before queue admission: %v", err)
		default:
		}
		time.Sleep(time.Millisecond)
	}
	cancel()
	if err = <-done; err == nil {
		t.Fatal("cancelled queued request succeeded")
	}
	if err = m.CloseAndWait(context.Background()); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(rows) != 1 || rows[0].Event != "model.failed" || rows[0].FailureReason == nil || *rows[0].FailureReason != "cancelled" || rows[0].FirstTokenMS != nil {
		t.Fatalf("missing queued attempt: %+v", rows)
	}
	at, err := time.Parse(time.RFC3339Nano, rows[0].At)
	if err != nil || at.Before(started.Add(-time.Millisecond)) {
		t.Fatal("bad attempt wall time", at, err)
	}
}
