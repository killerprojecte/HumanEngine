package providertransport

import (
	"context"
	"errors"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/modeldiagnostics"
	"os/exec"
	"testing"
)

func TestDiagnosticV1FailureKeepsActualContinuation(t *testing.T) {
	if _, e := exec.LookPath("node"); e != nil {
		t.Fatal("Node required for native V1 diagnostic fixture", e)
	}
	for _, panicHook := range []bool{false, true} {
		m, _, _ := v1Models(t)
		calls := 0
		m.Diagnostics = diagnosticFunc(func(_ context.Context, entry modeldiagnostics.Entry, secrets []string) error {
			calls++
			if len(secrets) != 1 || secrets[0] != "synthetic-key" {
				t.Error("lost V1 actual credential")
			}
			output := entry.Output.(*jsjson.Object)
			if _, ok := output.Get("usage"); ok {
				t.Error("source custom diagnostics omit usage")
			}
			if panicHook {
				panic("synthetic optional diagnostic")
			}
			return errors.New("synthetic optional diagnostic")
		})
		first, e := m.Complete(context.Background(), diagnosticRequest())
		if e != nil || first.Text != "fresh" {
			t.Fatalf("first %v", e)
		}
		second, e := m.Complete(context.Background(), CompletionRequest{Role: "replyer", Messages: []*Message{first.Assistant}})
		if e != nil || second.Text != "continued" {
			t.Fatalf("opaque continuation lost %v", e)
		}
		if calls != 2 {
			t.Fatalf("diagnostic changed attempts: %d", calls)
		}
		if e = first.Release(); e != nil {
			t.Fatal(e)
		}
		if e = second.Release(); e != nil {
			t.Fatal(e)
		}
		if e = m.CloseAndWait(context.Background()); e != nil {
			t.Fatal(e)
		}
	}
}
