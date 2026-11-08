package learningowner

import (
	"context"
	"encoding/json"
	"github.com/killerprojecte/HumanEngine/backend/internal/botruntime"
	"github.com/killerprojecte/HumanEngine/backend/internal/dispatch"
	"github.com/killerprojecte/HumanEngine/backend/internal/habitservice"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsdate"
	"github.com/killerprojecte/HumanEngine/backend/internal/modelread"
	"github.com/killerprojecte/HumanEngine/backend/internal/modelservice"
	"github.com/killerprojecte/HumanEngine/backend/internal/observability"
	"github.com/killerprojecte/HumanEngine/backend/internal/providertransport"
	"github.com/killerprojecte/HumanEngine/backend/internal/replyplan"
	"github.com/killerprojecte/HumanEngine/backend/internal/sourceevents"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type syntheticCredentials struct{}

func (syntheticCredentials) Resolve(context.Context, providertransport.Provider) (string, error) {
	return "synthetic-test-only", nil
}

// ownerFixture composes actual source, domain, read, budget, routing and HTTP
// owners. Only the remote provider is a synthetic protocol server.
func ownerFixture(t *testing.T, handler http.HandlerFunc) (*Owner, scopedFixture) {
	t.Helper()
	memory, f := serviceSetup(t)
	ctx := context.Background()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	zero, timeout := float64(0), float64(5000)
	policy, e := modelservice.New(modelservice.Config{Providers: []modelservice.Provider{{ID: "p", Protocol: "openai-chat", RetryCount: &zero, TimeoutMS: &timeout}}, Models: []modelservice.Model{{ID: "m", ProviderID: "p", APIModelName: "synthetic", Capabilities: []string{"text", "tools", "embedding"}}}, Routes: []modelservice.Route{{Role: "learning", OrderedModelIDs: []string{"m"}}, {Role: "embedding", OrderedModelIDs: []string{"m"}}}}, modelservice.Options{})
	if e != nil {
		t.Fatal(e)
	}
	models := &providertransport.NativeModels{Policy: policy, Client: &providertransport.Client{HTTP: server.Client(), Credentials: syntheticCredentials{}, Budgets: &providertransport.BudgetService{DB: f.main}}, Providers: map[string]providertransport.Provider{"p": {ID: "p", Protocol: "openai-chat", BaseURL: server.URL}}, ProviderSnapshots: map[string]map[string]any{"p": {"id": "p", "protocol": "openai-chat", "baseUrl": server.URL}}, ModelSnapshots: map[string]map[string]any{"m": {"id": "m", "providerId": "p", "apiModelName": "synthetic", "capabilities": []any{"text", "tools", "embedding"}}}}
	t.Cleanup(func() { _ = models.CloseAndWait(context.Background()) })
	config := map[string]any{"providers": []any{map[string]any{"id": "p", "protocol": "openai-chat", "baseUrl": server.URL}}, "models": []any{map[string]any{"id": "m", "providerId": "p", "apiModelName": "synthetic", "capabilities": []any{"text", "tools", "embedding"}}}, "routes": []any{map[string]any{"role": "learning", "orderedModelIds": []any{"m"}}, map[string]any{"role": "embedding", "orderedModelIds": []any{"m"}}}}
	raw, _ := json.Marshal(config)
	if _, e = f.main.Exec("INSERT INTO config(key,value) VALUES('models',?)", string(raw)); e != nil {
		t.Fatal(e)
	}

	dates, e := jsdate.New("UTC")
	if e != nil {
		t.Fatal(e)
	}
	compare := func(a, b string) (int, error) { return strings.Compare(a, b), nil }
	rp, e := replyplan.New(compare, dates)
	if e != nil {
		t.Fatal(e)
	}
	authority, e := sourceevents.NewRuntimeAuthority(f.events)
	if e != nil {
		t.Fatal(e)
	}
	foreground, e := botruntime.New(botruntime.DefaultSettings(), botruntime.Services{Models: botruntime.ModelFunc(func(_ context.Context, _ botruntime.ModelRequest, done func(botruntime.Completion, error)) {
		t.Error("unexpected foreground model")
		done(botruntime.Completion{}, nil)
	}), Policy: rp, Dates: dates, Compare: compare, Authority: authority})
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { _ = foreground.CloseAndWait(context.Background()) })
	if e = foreground.RegisterScene(botruntime.Scene{SceneRef: dispatch.SceneRef{ID: "s", Kind: "group", AdapterID: "adapter", AccountID: "account", PlatformSceneID: "s"}, LearningEnabled: true}); e != nil {
		t.Fatal(e)
	}
	reads, e := modelread.New(f.store)
	if e != nil {
		t.Fatal(e)
	}
	habits, e := habitservice.New(ctx, f.main, f.recovery, habitservice.Options{})
	if e != nil {
		t.Fatal(e)
	}
	logs := observability.New(io.Discard, io.Discard)
	t.Cleanup(func() { _ = logs.Close(context.Background()) })
	o := &Owner{d: Dependencies{Main: f.main, Authority: f.recovery, Assertions: f.store, Sources: f.events, Memory: memory, Habits: habits, Models: models, Read: reads, Foreground: foreground, Logs: logs, Now: time.Now}}
	if e = o.initializeIndex(ctx); e != nil {
		t.Fatal(e)
	}
	return o, f
}
