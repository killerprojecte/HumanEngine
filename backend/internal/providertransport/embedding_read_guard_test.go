package providertransport

import (
	"context"
	"errors"
	"github.com/killerprojecte/HumanEngine/backend/internal/modelservice"
	"net/http"
	"sync/atomic"
	"testing"
)

func TestEmbeddingChecksReadGuardBeforePhysicalRequest(t *testing.T) {
	for _, denyAt := range []int32{1, 2} {
		t.Run(string(rune('0'+denyAt)), func(t *testing.T) {
			var calls, checks atomic.Int32
			m := diagnosticModels(t, func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				t.Error("revoked embedding reached provider")
			})
			zero, timeout := float64(0), float64(3000)
			policy, e := modelservice.New(modelservice.Config{Providers: []modelservice.Provider{{ID: "p", Protocol: "openai-chat", RetryCount: &zero, TimeoutMS: &timeout}}, Models: []modelservice.Model{{ID: "m", ProviderID: "p", APIModelName: "synthetic", Capabilities: []string{"embedding"}}}}, modelservice.Options{})
			if e != nil {
				t.Fatal(e)
			}
			m.Policy = policy
			ctx := WithModelReadGuard(context.Background(), func(context.Context) error {
				if checks.Add(1) >= denyAt {
					return errors.New("memory_read_revoked")
				}
				return nil
			})
			_, e = m.Embeddings(ctx, EmbeddingModel{ID: "m", ProviderID: "p", APIModelName: "synthetic"}, []string{"Synthetic query"})
			if e == nil || e.Error() != "memory_read_revoked" || calls.Load() != 0 {
				t.Fatalf("result %v requests=%d checks=%d", e, calls.Load(), checks.Load())
			}
			if e = m.CloseAndWait(context.Background()); e != nil {
				t.Fatal(e)
			}
		})
	}
}
