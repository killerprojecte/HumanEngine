package providertransport

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/killerprojecte/HumanEngine/backend/internal/modelcalls"
)

func TestModelCallsActualV1ObserverFailureKeepsContinuation(t *testing.T) {
	for _, panicHook := range []bool{false, true} {
		m, _, _ := v1Models(t)
		var mu sync.Mutex
		var rows []modelcalls.Observation
		m.Calls = callSinkFunc(func(_ context.Context, e modelcalls.Observation) error {
			mu.Lock()
			rows = append(rows, e)
			mu.Unlock()
			if panicHook {
				panic("synthetic metadata sink")
			}
			return errors.New("synthetic metadata sink")
		})
		first, err := m.Complete(context.Background(), diagnosticRequest())
		if err != nil || first.Text != "fresh" {
			t.Fatal("first completion", err)
		}
		second, err := m.Complete(context.Background(), CompletionRequest{Role: "replyer", Messages: []*Message{first.Assistant}})
		if err != nil || second.Text != "continued" {
			t.Fatal("lost continuation", err)
		}
		if err = first.Release(); err != nil {
			t.Fatal(err)
		}
		if err = second.Release(); err != nil {
			t.Fatal(err)
		}
		if err = m.CloseAndWait(context.Background()); err != nil {
			t.Fatal(err)
		}
		mu.Lock()
		if len(rows) != 2 {
			t.Fatal("changed actual attempt count", len(rows))
		}
		for _, e := range rows {
			if e.TransportMode != "custom" || e.FirstTokenMS != nil || e.FirstTokenKind != nil || e.Event != "model.completed" {
				t.Fatalf("incorrect custom observation: %+v", e)
			}
		}
		mu.Unlock()
	}
}
