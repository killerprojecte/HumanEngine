package modelservice

import (
	"context"
	"encoding/json"
	"os"
	"reflect"
	"testing"

	"github.com/killerprojecte/HumanEngine/backend/internal/scheduler"
)

func TestUsageCandidateRetryScopeActualSource(t *testing.T) {
	raw, err := os.ReadFile("testdata/usage-retry-source.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Node  string
		Views []struct {
			Name                string
			Wire                []string
			Billing, Diagnostic [][2]string
		}
	}
	if err := json.Unmarshal(raw, &fixture); err != nil || fixture.Node != "v24.22.1" {
		t.Fatalf("invalid source %s: %v", fixture.Node, err)
	}
	for _, v := range fixture.Views {
		t.Run(v.Name, func(t *testing.T) {
			cfg := baseConfig("openai-chat", 0)
			cfg.Providers[0].RetryCount = n(1)
			cfg.Providers[0].TimeoutMS = n(10000)
			cfg.Models = []Model{
				{ID: "primary", ProviderID: "p", Capabilities: []string{"text"}},
				{ID: "fallback", ProviderID: "p", Capabilities: []string{"text"}},
			}
			cfg.Routes = []Route{{Role: "planner", OrderedModelIDs: []string{"primary", "fallback"}}}
			p, err := New(cfg, Options{
				Scheduler: scheduler.NewModelScheduler(),
				Sleep:     func(context.Context, float64) error { return nil },
			})
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			billing, diagnostic := [][2]string{}, [][2]string{}
			label := func(e AttemptObservation) [2]string {
				if e.Completed {
					return [2]string{e.ModelID, "model.completed"}
				}
				return [2]string{e.ModelID, "model.failed"}
			}
			ctx = WithAttemptObserver(ctx, func(_ context.Context, e AttemptObservation) {
				diagnostic = append(diagnostic, label(e))
			})
			ctx = WithUsageObserver(ctx, func(_ context.Context, e AttemptObservation) {
				billing = append(billing, label(e))
			})
			index := 0
			_, err = p.Run(ctx, Request{Role: "planner"}, func(_ context.Context, _ Attempt, done func(any, error)) {
				switch v.Wire[index] {
				case "503":
					done(nil, HTTPFailure(503, nil))
				case "cancel":
					cancel()
					done(nil, context.Canceled)
				default:
					done("synthetic", nil)
				}
				index++
			})
			if drainErr := p.Drain(t.Context()); drainErr != nil {
				t.Fatal(drainErr)
			}
			if (err != nil) != (v.Name == "parent-abort") ||
				index != len(v.Wire) || !reflect.DeepEqual(billing, v.Billing) || !reflect.DeepEqual(diagnostic, v.Diagnostic) {
				t.Fatalf("calls=%d error=%v billing=%v diagnostic=%v; want %v and %v", index, err, billing, diagnostic, v.Billing, v.Diagnostic)
			}
		})
	}
}
