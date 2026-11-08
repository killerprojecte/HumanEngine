package providertransport

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/killerprojecte/HumanEngine/backend/internal/modelservice"
	"github.com/killerprojecte/HumanEngine/backend/internal/providerwire"
	"github.com/killerprojecte/HumanEngine/backend/internal/scheduler"
)

// The synthetic codec exercises real HTTP/slot ownership without claiming codec parity.
type adaptiveImageFixture struct {
	mu     sync.Mutex
	limits []int
}

func (s *adaptiveImageFixture) Serialize(ctx context.Context, body map[string]any, configured *ImageUploadLimits, limit *int) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if limit != nil {
		s.limits = append(s.limits, *limit)
	}
	return providerwire.EncodeJSON(body)
}
func TestRejectedInlineImageReacquiresNativeSlot(t *testing.T) {
	sched, err := scheduler.New(1, 1000, scheduler.Options{})
	if err != nil {
		t.Fatal(err)
	}
	var intervened atomic.Bool
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if requests == 1 {
			sched.SubmitModel(context.Background(), 0, func(_ context.Context, done func(any, error)) { intervened.Store(true); done(nil, nil) })
			w.WriteHeader(413)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"choices":[{"finish_reason":"stop","message":{"content":"ok"}}]}`)
	}))
	defer server.Close()
	client, p := nativeTestClient(server)
	p.ID = "p"
	codec := &adaptiveImageFixture{}
	client.Images = codec
	retry, timeout := float64(0), float64(1000)
	policy, e := modelservice.New(modelservice.Config{Providers: []modelservice.Provider{{ID: p.ID, Protocol: p.Protocol, RetryCount: &retry, TimeoutMS: &timeout}}, Models: []modelservice.Model{{ID: "m", ProviderID: p.ID, APIModelName: "synthetic", Capabilities: []string{"text", "vision"}}}, Routes: []modelservice.Route{{Role: "replyer", OrderedModelIDs: []string{"m"}}}}, modelservice.Options{Scheduler: sched})
	if e != nil {
		t.Fatal(e)
	}
	models := &NativeModels{Policy: policy, Client: client, Providers: map[string]Provider{p.ID: p}}
	result, e := models.Complete(context.Background(), CompletionRequest{Role: "replyer", Messages: []*Message{{Visible: map[string]any{"role": "user", "content": "synthetic", "images": []any{"data:image/png;base64,c3ludGhldGlj"}}}}})
	if e != nil || result.Text != "ok" || requests != 2 || len(codec.limits) != 1 || !intervened.Load() {
		t.Fatalf("result %#v error %v requests %d limits %v", result, e, requests, codec.limits)
	}
}
