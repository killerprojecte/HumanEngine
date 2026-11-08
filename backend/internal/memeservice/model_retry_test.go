package memeservice

import (
	"context"
	"github.com/killerprojecte/HumanEngine/backend/internal/modelread"
	"github.com/killerprojecte/HumanEngine/backend/internal/modelservice"
	"github.com/killerprojecte/HumanEngine/backend/internal/providertransport"
	"net/http"
	"sync/atomic"
	"testing"
)

func TestNativeProviderRetryStopsAtRevokedModelReadTicket(t *testing.T) {
	f, s, codec, c, _, _, _ := nativeImageFixture(t)
	read, e := modelread.New(s.assertions)
	if e != nil {
		t.Fatal(e)
	}
	ticket, e := read.Require(context.Background(), c.SceneID)
	if e != nil {
		t.Fatal(e)
	}
	var calls atomic.Int32
	models := modelFixture(t, codec, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if _, e := f.main.Exec("UPDATE scenes SET value=json_set(value,'$.memoryReadRevision',1) WHERE id=?", c.SceneID); e != nil {
			t.Error(e)
		}
		w.WriteHeader(429)
	})
	retries, timeout := float64(1), float64(30000)
	stream := false
	var attempts atomic.Int32
	models.Policy, e = modelservice.New(modelservice.Config{Providers: []modelservice.Provider{{ID: "p", Protocol: "openai-chat", RetryCount: &retries, TimeoutMS: &timeout}}, Models: []modelservice.Model{{ID: "m", ProviderID: "p", APIModelName: "synthetic", Capabilities: []string{"text"}, Streaming: &stream}, {ID: "fallback", ProviderID: "p", APIModelName: "never", Capabilities: []string{"text"}, Streaming: &stream}}, Routes: []modelservice.Route{{Role: "vision", OrderedModelIDs: []string{"m", "fallback"}}}}, modelservice.Options{Sleep: func(context.Context, float64) error { return nil }, Observe: func(event modelservice.Event) {
		if event.Kind == "attempt" {
			attempts.Add(1)
		}
	}})
	if e != nil {
		t.Fatal(e)
	}
	guarded := providertransport.WithModelReadGuard(context.Background(), func(ctx context.Context) error { return read.Assert(ctx, ticket) })
	_, e = models.Complete(guarded, providertransport.CompletionRequest{Role: "vision", Messages: []*providertransport.Message{{Visible: map[string]any{"role": "user", "content": "synthetic"}}}})
	if e == nil || e.Error() != "memory_read_revoked" {
		t.Fatalf("lost read-admission classification: %v", e)
	}
	if calls.Load() != 1 {
		t.Fatal("revoked retry/fallback escaped to provider", calls.Load())
	}
	if !modelservice.ProviderAttemptsHandled(e) {
		t.Fatal("completed provider attempt was not marked handled")
	}
}
