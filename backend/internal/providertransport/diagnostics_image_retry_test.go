package providertransport

import (
	"context"
	"fmt"
	"github.com/killerprojecte/HumanEngine/backend/internal/modeldiagnostics"
	"github.com/killerprojecte/HumanEngine/backend/internal/modelservice"
	"github.com/killerprojecte/HumanEngine/backend/internal/scheduler"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

type rotatingDiagnosticCredential struct{ calls atomic.Int32 }

func (c *rotatingDiagnosticCredential) Resolve(context.Context, Provider) (string, error) {
	if c.calls.Add(1) == 1 {
		return "before-rotation", nil
	}
	return "after-rotation", nil
}

// Uses the existing explicitly synthetic codec fixture, actual HTTP413 native
// rescheduling, scheduler slots and credential resolution. It proves request
// secret retention across rescheduling, not image encoder byte-limit parity.
func TestDiagnosticImageRetryRetainsAllActualCredentials(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempt := requests.Add(1)
		expected := "Bearer before-rotation"
		if attempt > 1 {
			expected = "Bearer after-rotation"
		}
		if r.Header.Get("Authorization") != expected {
			t.Error("wrong actual credential", attempt)
		}
		if attempt == 1 {
			w.WriteHeader(413)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"choices":[{"finish_reason":"stop","message":{"content":"before-rotation and after-rotation"}}]}`)
	}))
	defer server.Close()
	client, p := nativeTestClient(server)
	p.ID = "p"
	credentials := new(rotatingDiagnosticCredential)
	client.Credentials = credentials
	client.Images = &adaptiveImageFixture{}
	retry, timeout := float64(0), float64(1000)
	policy, e := modelservice.New(modelservice.Config{Providers: []modelservice.Provider{{ID: "p", Protocol: p.Protocol, RetryCount: &retry, TimeoutMS: &timeout}}, Models: []modelservice.Model{{ID: "m", ProviderID: "p", APIModelName: "synthetic", Capabilities: []string{"text", "vision"}}}, Routes: []modelservice.Route{{Role: "replyer", OrderedModelIDs: []string{"m"}}}}, modelservice.Options{Scheduler: scheduler.NewModelScheduler()})
	if e != nil {
		t.Fatal(e)
	}
	diagnostics, e := modeldiagnostics.New(modeldiagnostics.Ports{Enabled: func() bool { return true }, Secrets: func(context.Context) ([]string, error) { return []string{"after-rotation"}, nil }})
	if e != nil {
		t.Fatal(e)
	}
	defer diagnostics.CloseAndWait(context.Background())
	models := &NativeModels{Policy: policy, Client: client, Providers: map[string]Provider{"p": p}, Diagnostics: diagnostics}
	completion, e := models.Complete(context.Background(), CompletionRequest{Role: "replyer", Messages: []*Message{{Visible: map[string]any{"role": "user", "content": "synthetic", "images": []any{"data:image/png;base64,c3ludGhldGlj"}}}}})
	if e != nil {
		t.Fatal(e)
	}
	if completion.Text != "before-rotation and after-rotation" || requests.Load() != 2 || credentials.calls.Load() != 2 {
		t.Fatal("rescheduling did not exercise two actual credentials")
	}
	rows := diagnostics.Rows()
	if len(rows) != 1 || strings.Contains(rows[0].Output, "before-rotation") || strings.Contains(rows[0].Output, "after-rotation") || !strings.Contains(rows[0].Output, "[redacted] and [redacted]") {
		t.Fatal("lost old physical request secret", rows)
	}
	if e = models.CloseAndWait(context.Background()); e != nil {
		t.Fatal(e)
	}
}
