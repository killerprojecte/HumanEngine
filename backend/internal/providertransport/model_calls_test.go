package providertransport

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/killerprojecte/HumanEngine/backend/internal/modelcalls"
)

type callSinkFunc func(context.Context, modelcalls.Observation) error

func (f callSinkFunc) Record(ctx context.Context, e modelcalls.Observation) error { return f(ctx, e) }
func TestModelCallsActualPartialStreamFailureHasFirstContent(t *testing.T) {
	var requests atomic.Int32
	m := diagnosticModels(t, func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"partial\"}}]}\n\n")
	})
	var rows []modelcalls.Observation
	var mu sync.Mutex
	m.Calls = callSinkFunc(func(_ context.Context, e modelcalls.Observation) error {
		mu.Lock()
		defer mu.Unlock()
		rows = append(rows, e)
		return nil
	})
	if _, err := m.Complete(context.Background(), diagnosticRequest()); err == nil {
		t.Fatal("incomplete stream accepted")
	}
	if err := m.CloseAndWait(context.Background()); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if requests.Load() != 1 || len(rows) != 1 {
		t.Fatal("replayed paid stream", requests.Load(), len(rows))
	}
	e := rows[0]
	if e.Event != "model.failed" || e.FailureReason == nil || *e.FailureReason != "stream_interrupted" || e.FirstTokenMS == nil || e.FirstTokenKind == nil || *e.FirstTokenKind != "text" || e.Usage != nil {
		t.Fatalf("bad real stream metadata: %+v", e)
	}
}
func TestModelCallsActualRetryAndObserverFailure(t *testing.T) {
	for _, panicObserver := range []bool{false, true} {
		t.Run(fmt.Sprint(panicObserver), func(t *testing.T) {
			var requests atomic.Int32
			var mu sync.Mutex
			var rows []modelcalls.Observation
			m := diagnosticModels(t, func(w http.ResponseWriter, r *http.Request) {
				if requests.Add(1) == 1 {
					w.WriteHeader(500)
					return
				}
				diagnosticResponse(w)
			})
			m.Calls = callSinkFunc(func(_ context.Context, e modelcalls.Observation) error {
				mu.Lock()
				rows = append(rows, e)
				mu.Unlock()
				if panicObserver {
					panic("synthetic metadata sink")
				}
				return errors.New("synthetic metadata sink")
			})
			c, err := m.Complete(context.Background(), diagnosticRequest())
			if err != nil {
				t.Fatal(err)
			}
			defer c.Release()
			if err = m.CloseAndWait(context.Background()); err != nil {
				t.Fatal(err)
			}
			mu.Lock()
			defer mu.Unlock()
			if requests.Load() != 2 || len(rows) != 2 || rows[0].Event != "model.failed" || rows[1].Event != "model.completed" || rows[0].Attempt != 1 || rows[1].Attempt != 2 {
				t.Fatalf("retry metadata: requests=%d rows=%+v", requests.Load(), rows)
			}
		})
	}
}

func TestModelCallsActualLateCanceledHTTPRecordsFailureAndDrains(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	t.Cleanup(func() {
		select {
		case <-release:
		default:
			close(release)
		}
	})
	m := diagnosticModels(t, func(http.ResponseWriter, *http.Request) { t.Error("unexpected server") })
	m.Client.HTTP = &http.Client{Transport: diagnosticTransport(func(*http.Request) (*http.Response, error) {
		close(entered)
		<-release
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"choices":[{"finish_reason":"stop","message":{"content":"late"}}]}`))}, nil
	})}
	var mu sync.Mutex
	var rows []modelcalls.Observation
	m.Calls = callSinkFunc(func(_ context.Context, e modelcalls.Observation) error {
		mu.Lock()
		rows = append(rows, e)
		mu.Unlock()
		return nil
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	returned := make(chan error, 1)
	go func() { _, err := m.Complete(ctx, diagnosticRequest()); returned <- err }()
	<-entered
	cancel()
	closedCtx, stop := context.WithCancel(context.Background())
	stop()
	if err := m.CloseAndWait(closedCtx); !errors.Is(err, context.Canceled) {
		t.Fatal("physical HTTP escaped drain", err)
	}
	close(release)
	if err := <-returned; err == nil {
		t.Fatal("late success survived cancellation")
	}
	if err := m.CloseAndWait(context.Background()); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(rows) != 1 || rows[0].Event != "model.failed" || rows[0].FailureReason == nil || *rows[0].FailureReason != "cancelled" {
		t.Fatalf("lost late failed attempt: %+v", rows)
	}
}

func TestModelCallsObserverCannotMutateBusinessUsage(t *testing.T) {
	m := diagnosticModels(t, func(w http.ResponseWriter, r *http.Request) { diagnosticResponse(w) })
	m.Calls = callSinkFunc(func(_ context.Context, e modelcalls.Observation) error {
		if e.Usage == nil || e.Usage.Input == nil {
			t.Error("actual usage missing")
			return nil
		}
		*e.Usage.Input = 999
		return nil
	})
	value, err := m.Complete(context.Background(), diagnosticRequest())
	if err != nil {
		t.Fatal(err)
	}
	defer value.Release()
	if value.Usage["inputTokens"] != float64(1) {
		t.Fatal("observer changed business/billing result", value.Usage)
	}
	if err = m.CloseAndWait(context.Background()); err != nil {
		t.Fatal(err)
	}
}
