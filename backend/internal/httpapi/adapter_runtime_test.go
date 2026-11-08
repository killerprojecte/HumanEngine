package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/killerprojecte/HumanEngine/backend/internal/contract"
	"github.com/killerprojecte/HumanEngine/backend/internal/dispatch"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/modelservice"
	"github.com/killerprojecte/HumanEngine/backend/internal/plugins"
	"github.com/killerprojecte/HumanEngine/backend/internal/providertransport"
	"github.com/killerprojecte/HumanEngine/backend/internal/v1adapterruntime"
)

// This is intentionally an integration test, not an injected AdapterServices
// fixture: every synchronous plugin callback reaches actual SQLite/source/runtime
// owners, and the send is produced by the native Runtime turn pipeline.
func TestV1AdapterNativeRuntimeLifecycle(t *testing.T) {
	_, db, engine := memeHTTP(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	settings, e := jsjson.Parse(string(contract.DefaultSettingsInput()))
	if e != nil {
		t.Fatal(e)
	}
	settings.(*jsjson.Object).Set("sourceEventRevisionsEnabled", true)
	encoded, e := jsjson.Stringify(settings)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = db.Exec("INSERT OR REPLACE INTO config(key,value) VALUES('settings',?)", encoded); e != nil {
		t.Fatal(e)
	}
	var diagnostic adapterDiagnosticBuffer
	node, e := exec.LookPath("node")
	if e != nil {
		t.Fatal("Node required for actual V1 runtime fixture", e)
	}
	bridge, e := v1adapterruntime.New(engine, node, &diagnostic, plugins.V1HostOptions{})
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() {
		if e := bridge.Close(context.Background()); e != nil {
			t.Error(e)
		}
	})
	zero, timeout := float64(0), float64(10000)
	const protocol = "runtime-fixture:deterministic"
	engine.Models.Policy, e = modelservice.New(modelservice.Config{Providers: []modelservice.Provider{{ID: "p", Protocol: protocol, RetryCount: &zero, TimeoutMS: &timeout}}, Models: []modelservice.Model{{ID: "m", ProviderID: "p", APIModelName: "synthetic", Capabilities: []string{"text", "tools"}}}, Routes: []modelservice.Route{{Role: "planner", OrderedModelIDs: []string{"m"}}, {Role: "replyer", OrderedModelIDs: []string{"m"}}}}, modelservice.Options{})
	if e != nil {
		t.Fatal(e)
	}
	engine.Models.Client = &providertransport.Client{Credentials: memeHTTPKeys{}, Budgets: providertransport.ExplicitOff{}}
	engine.Models.Providers = map[string]providertransport.Provider{"p": {ID: "p", Protocol: protocol, BaseURL: "https://synthetic.invalid"}}
	engine.Models.ProviderSnapshots = map[string]map[string]any{"p": {"id": "p", "protocol": protocol, "baseUrl": "https://synthetic.invalid"}}
	engine.Models.ModelSnapshots = map[string]map[string]any{"m": {"id": "m", "providerId": "p", "apiModelName": "synthetic"}}
	path, e := filepath.Abs("testdata/adapter-runtime.mjs")
	if e != nil {
		t.Fatal(e)
	}
	raw, e := bridge.Host.Enable(ctx, "runtime-fixture", path, map[string]any{"timestamp": time.Now().UTC().Format(time.RFC3339Nano)})
	if e != nil {
		t.Fatalf("enable: %v %s", e, diagnostic.String())
	}
	var loaded struct {
		Registrations []struct{ Kind, Name, Handle string }
	}
	if e = json.Unmarshal(raw, &loaded); e != nil {
		t.Fatal(e)
	}
	actions := map[string]string{}
	for _, r := range loaded.Registrations {
		if r.Kind == "action" {
			actions[r.Name] = r.Handle
		}
	}
	call := func(name string, args ...any) json.RawMessage {
		t.Helper()
		v, e := bridge.Host.Call(ctx, "runtime-fixture", actions[name], args)
		if e != nil {
			t.Fatalf("%s: %v %s", name, e, diagnostic.String())
		}
		return v
	}
	var inspection struct {
		Scene      dispatch.SceneRef
		Source     string
		Sent       []json.RawMessage
		ModelCalls int
	}
	inspect := func() {
		t.Helper()
		if e := json.Unmarshal(call("inspect"), &inspection); e != nil {
			t.Fatal(e)
		}
	}
	inspect()
	before, e := engine.Runtime.Snapshot(inspection.Scene.ID)
	if e != nil {
		t.Fatal(e)
	}
	if len(before.Messages) != 1 {
		t.Fatalf("plugin receive did not reach runtime: %+v", before)
	}
	original := memeGet(before.Messages[0], "sourceEventRef")
	if original == nil {
		t.Fatal("missing actual source event reference")
	}
	var edit struct {
		Status           string
		Ref, PreviousRef json.RawMessage
	}
	if e = json.Unmarshal(call("edit"), &edit); e != nil {
		t.Fatal(e)
	}
	originalJSON, e := jsjson.Marshal(original)
	if e != nil {
		t.Fatal(e)
	}
	equalJSON := func(a, b []byte) bool {
		var x, y any
		if json.Unmarshal(a, &x) != nil || json.Unmarshal(b, &y) != nil {
			return false
		}
		xa, _ := json.Marshal(x)
		ya, _ := json.Marshal(y)
		return bytes.Equal(xa, ya)
	}
	if edit.Status != "edited" || !equalJSON(edit.PreviousRef, originalJSON) || len(edit.Ref) == 0 {
		t.Fatalf("full EditResult lost: %+v original=%s", edit, originalJSON)
	}
	edited, e := engine.Runtime.Snapshot(inspection.Scene.ID)
	if e != nil {
		t.Fatal(e)
	}
	currentJSON, _ := jsjson.Marshal(memeGet(edited.Messages[0], "sourceEventRef"))
	if !equalJSON(currentJSON, edit.Ref) {
		t.Fatalf("returned edit ref differs from accepted runtime: %s != %s", currentJSON, edit.Ref)
	}
	// Enable participation through the actual persisted scene configuration before
	// requesting a manual turn, rather than bypassing the runtime's send gates.
	var sceneValue map[string]any
	var sceneRaw string
	if e = db.QueryRow("SELECT value FROM scenes WHERE id=?", inspection.Scene.ID).Scan(&sceneRaw); e != nil {
		t.Fatal(e)
	}
	if e = json.Unmarshal([]byte(sceneRaw), &sceneValue); e != nil {
		t.Fatal(e)
	}
	sceneValue["participationEnabled"] = true
	raw, e = json.Marshal(sceneValue)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = db.Exec("UPDATE scenes SET value=? WHERE id=?", string(raw), inspection.Scene.ID); e != nil {
		t.Fatal(e)
	}
	scene := edited.Scene
	scene.ParticipationEnabled = true
	if e = engine.Runtime.RegisterScene(scene); e != nil {
		t.Fatal(e)
	}
	edited, e = engine.Runtime.Snapshot(scene.ID)
	if e != nil || len(edited.Topics) != 1 {
		t.Fatalf("topic: %+v %v", edited, e)
	}
	turn, e := engine.Runtime.Run(scene.ID, edited.Topics[0].ID, false)
	if e != nil {
		t.Fatal(e)
	}
	select {
	case <-turn.Done:
	case <-ctx.Done():
		t.Fatalf("turn blocked: %v %s", ctx.Err(), diagnostic.String())
	}
	result := turn.Wait()
	if result.Err != nil || result.CleanupErr != nil {
		t.Fatalf("turn: %+v %s", result, diagnostic.String())
	}
	inspect()
	if len(inspection.Sent) != 1 || inspection.ModelCalls < 3 {
		t.Fatalf("native model/runtime/dispatch not exercised: %+v turn=%+v", inspection, result)
	}
	if string(call("recall")) != "true" {
		t.Fatal("plugin recall did not reach durable source owner")
	}
	if e = bridge.Host.Disable(ctx, "runtime-fixture"); e != nil {
		t.Fatal(e)
	}
	if len(bridge.Host.RegisteredAdapters()) != 0 || bridge.Host.PID() != 0 {
		t.Fatal("disable retained adapter or Node process")
	}
	// Re-enable creates a distinct host generation and real runtime registration.
	if _, e = bridge.Host.Enable(ctx, "runtime-fixture", path, map[string]any{"timestamp": time.Now().UTC().Format(time.RFC3339Nano)}); e != nil {
		t.Fatal(e)
	}
	if len(bridge.Host.RegisteredAdapters()) != 1 {
		t.Fatal("new generation missing")
	}
	if e = bridge.Host.Disable(ctx, "runtime-fixture"); e != nil {
		t.Fatal(e)
	}
}

type adapterDiagnosticBuffer struct {
	mu   sync.Mutex
	data bytes.Buffer
}

func (b *adapterDiagnosticBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.data.Write(p)
}
func (b *adapterDiagnosticBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.data.String()
}
