package providertransport

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/killerprojecte/HumanEngine/backend/internal/modelcalls"
	"github.com/killerprojecte/HumanEngine/backend/internal/modelservice"
	"github.com/killerprojecte/HumanEngine/backend/internal/scheduler"
)

// Synthetic codec, actual HTTP413 and scheduler ownership. This tests metadata
// attempt identity, not image encoder limit parity.
func TestModelCallsImageRescheduleSingleAttemptAndQueuedCancellation(t *testing.T) {
	for _, cancelQueue := range []bool{false, true} {
		name := "success"
		if cancelQueue {
			name = "cancel-queued"
		}
		t.Run(name, func(t *testing.T) {
			sched := scheduler.NewModelScheduler()
			if err := sched.Configure(1, 0); err != nil {
				t.Fatal(err)
			}
			blockerEntered := make(chan struct{})
			var blockerMu sync.Mutex
			var blockerRelease func(any, error)
			blockerReleased := false
			releaseBlocker := func() {
				blockerMu.Lock()
				blockerReleased = true
				done := blockerRelease
				blockerRelease = nil
				blockerMu.Unlock()
				if done != nil {
					done(nil, nil)
				}
			}
			t.Cleanup(releaseBlocker)
			var requests atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if requests.Add(1) == 1 {
					if cancelQueue {
						sched.SubmitModel(context.Background(), 0, func(_ context.Context, done func(any, error)) {
							blockerMu.Lock()
							released := blockerReleased
							if !released {
								blockerRelease = done
							}
							blockerMu.Unlock()
							if released {
								done(nil, nil)
							}
							close(blockerEntered)
						})
					}
					w.WriteHeader(413)
					return
				}
				diagnosticResponse(w)
			}))
			defer server.Close()
			client, provider := nativeTestClient(server)
			provider.ID = "p"
			client.Images = &adaptiveImageFixture{}
			retries, timeout := float64(0), float64(1000)
			policy, err := modelservice.New(modelservice.Config{Providers: []modelservice.Provider{{ID: "p", Protocol: provider.Protocol, RetryCount: &retries, TimeoutMS: &timeout}}, Models: []modelservice.Model{{ID: "m", ProviderID: "p", APIModelName: "synthetic", Capabilities: []string{"text", "vision"}}}, Routes: []modelservice.Route{{Role: "replyer", OrderedModelIDs: []string{"m"}}}}, modelservice.Options{Scheduler: sched})
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
			returned := make(chan error, 1)
			go func() {
				c, err := m.Complete(ctx, CompletionRequest{Role: "replyer", Messages: []*Message{{Visible: map[string]any{"role": "user", "content": "synthetic", "images": []any{"data:image/png;base64,c3ludGhldGlj"}}}}})
				if c != nil {
					c.Release()
				}
				returned <- err
			}()
			if cancelQueue {
				select {
				case <-blockerEntered:
				case err := <-returned:
					t.Fatalf("model ended before queued blocker: %v", err)
				}
				cancel()
			}
			err = <-returned
			if cancelQueue {
				releaseBlocker()
				if err == nil {
					t.Fatal("queued cancellation succeeded")
				}
			} else if err != nil {
				t.Fatal(err)
			}
			if err = m.CloseAndWait(context.Background()); err != nil {
				t.Fatal(err)
			}
			mu.Lock()
			defer mu.Unlock()
			if len(rows) != 1 || rows[0].Attempt != 1 {
				t.Fatalf("413 changed provider attempt identity: %+v", rows)
			}
			if cancelQueue {
				if requests.Load() != 1 || rows[0].Event != "model.failed" || rows[0].FailureReason == nil || *rows[0].FailureReason != "cancelled" {
					t.Fatalf("queued cancellation metadata %+v requests%d", rows, requests.Load())
				}
			} else if requests.Load() != 2 || rows[0].Event != "model.completed" {
				t.Fatalf("image reschedule metadata %+v requests%d", rows, requests.Load())
			}
		})
	}
}
