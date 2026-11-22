package app

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync/atomic"
	"testing"
	"time"

	"github.com/killerprojecte/HumanEngine/backend/internal/botruntime"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/memoryservice"
	"github.com/killerprojecte/HumanEngine/backend/internal/sourceadmission"
)

func TestInstalledMemorySourceTool(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	app, err := Open(ctx, applicationOptions(t))
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := app.Shutdown(context.Background()); err != nil {
			t.Error(err)
		}
	}()
	req := func(method, path string, value any) any {
		t.Helper()
		return appRequest(t, app, method, path, value)
	}
	settings := req("GET", "/api/settings", nil).(*jsjson.Object)
	for k, v := range map[string]any{"sourceEventRevisionsEnabled": true, "promptContextWindowEnabled": false, "debounceMs": float64(0), "groupTurnCooldownMs": float64(0)} {
		_ = settings.Set(k, v)
	}
	wire := func(o *jsjson.Object) json.RawMessage {
		t.Helper()
		b, err := jsjson.Marshal(o)
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	req("PUT", "/api/settings", wire(settings))
	req("PUT", "/api/models", json.RawMessage(`{"providers":[{"id":"p","name":"Synthetic","protocol":"runtime-fixture:deterministic","baseUrl":"https://synthetic.invalid","retryCount":0,"timeoutMs":10000}],"models":[{"id":"m","displayName":"Synthetic","providerId":"p","apiModelName":"synthetic","capabilities":["text","tools"]}],"routes":[{"role":"planner","orderedModelIds":["m"],"timeoutMs":10000},{"role":"replyer","orderedModelIds":["m"],"timeoutMs":10000}]}`))
	plugin := "/api/plugins/runtime-fixture/"
	req("PUT", plugin+"config", map[string]any{"timestamp": time.Now().UTC().Format(time.RFC3339Nano)})
	req("PUT", plugin+"enabled", map[string]bool{"enabled": true})
	inspect := func() any { return req("POST", plugin+"actions/inspect", map[string]any{}) }
	initial := inspect()
	sceneID := field(field(initial, "scene"), "id").(string)
	sourceID := field(initial, "source").(string)
	scenePath := "/api/scenes/" + url.PathEscape(sceneID)
	var scene *jsjson.Object
	for _, row := range req("GET", "/api/scenes", nil).([]any) {
		if field(row, "id") == sceneID {
			scene = row.(*jsjson.Object)
		}
	}
	if scene == nil {
		t.Fatal("fixture scene was not registered")
	}
	wait := func(label string, check func() bool) {
		t.Helper()
		for !check() {
			select {
			case <-ctx.Done():
				t.Fatal(label, ctx.Err())
			case <-time.After(5 * time.Millisecond):
			}
		}
	}
	var evidence []any
	wait("fixture source projected", func() bool {
		err = app.Engine.Sources.WithRuntimeReads(ctx, func(reads botruntime.ReadAuthority) error {
			msg, err := app.Engine.Runtime.PreviewMessage(sceneID, sourceID, reads)
			if err != nil || msg == nil {
				return err
			}
			evidence, err = sourceadmission.TrustedEvidence([]*jsjson.Object{msg}, func(scene, id string) (bool, error) {
				return reads.IsRecalled(botruntime.Scene{ID: scene}, id)
			})
			return err
		})
		return err == nil && len(evidence) == 1
	})
	source := memoryToolObject("sceneId", sceneID, "messageId", sourceID, "senderId", "synthetic-user")
	proposal := memoryToolObject("sceneId", sceneID, "simulation", false, "provenance", "source-only", "sourceEvidence", evidence)
	candidate := memoryToolObject("content", "A private fixture preference.", "kind", "fact", "epistemicKind", "explicit",
		"sources", []any{source}, "subject", memoryToolObject("kind", "self", "sourceMessageId", sourceID))
	_ = scene.Set("learningEnabled", true)
	req("PUT", scenePath, wire(scene))
	saved, err := app.Semantic.Memory.Propose(ctx, proposal, candidate, memoryservice.ProposalOptions{})
	if err != nil || saved.Status != "saved" {
		t.Fatalf("fixture source not durably saved: %+v %v", saved, err)
	}
	id := field(saved.Memory, "id").(string)
	foreign := req("POST", "/api/memories", map[string]any{"scope": map[string]any{"kind": "scene", "sceneId": "unrelated-scene"},
		"kind": "fact", "content": "Not visible."})
	_ = scene.Set("learningEnabled", false)
	_ = scene.Set("participationEnabled", true)
	req("PUT", scenePath, wire(scene))
	var target atomic.Pointer[string]
	var positive atomic.Bool
	var checkedPositive atomic.Bool
	app.Engine.Runtime.AddExtension(&botruntime.Extension{
		Tools: []*jsjson.Object{memoryToolObject("name", "fixture.memoryAnchor", "description", "Provide the synthetic fixture reference.", "effect", "read",
			"inputSchema", memoryToolObject("type", "object", "properties", memoryToolObject(), "additionalProperties", false))},
		Execute: func(_ context.Context, _ string, _ any, _ *botruntime.TurnContext, done func(any, error)) {
			done(memoryToolObject("memoryId", *target.Load()), nil)
		},
		ContextSelected: func(c *botruntime.TurnContext, _ *jsjson.Object) error {
			want := 0
			if positive.Load() {
				want = 1 // Only the preceding memory.person receipt books this assertion.
				checkedPositive.Store(true)
			}
			if len(app.MemoryExposure.ReadExposed(c)) != want {
				t.Error("identifier-only receipt unexpectedly changed booked memory")
			}
			return nil
		},
	})
	var hook atomic.Pointer[func()]
	var unknown, sourceFailed atomic.Int32
	app.Engine.Runtime.Observe(func(e botruntime.Event) {
		app.observeRuntime(e)
		if e.SceneID == sceneID && e.Stage == "planner" && e.Cause != nil && e.Cause.Error() == "context_reference_unknown" {
			unknown.Add(1)
		}
		if e.SceneID == sceneID && e.Stage == "tool" && e.ToolName == "memory.source" && e.Status == "failed" {
			sourceFailed.Add(1)
		}
		if e.SceneID == sceneID && e.Stage == "tool" && e.ToolName == "memory.source" && e.Status == "completed" {
			if f := hook.Swap(nil); f != nil {
				(*f)()
			}
		}
	})
	run := func(mode, id string) any {
		t.Helper()
		last := inspect()
		wantCalls := field(last, "memorySourceCalls").(float64) + 1
		wantReceipts := len(field(last, "sourceReceipts").([]any)) + 1
		target.Store(&id)
		req("POST", plugin+"actions/source-memory", map[string]string{"id": mode})
		req("POST", plugin+"actions/new-source", map[string]any{})
		wait("model tool call dispatched", func() bool { return field(inspect(), "memorySourceCalls") == wantCalls })
		wait("turn settled", func() bool { return app.Engine.Runtime.ActiveTasks() == 0 })
		receipts := field(inspect(), "sourceReceipts").([]any)
		if len(receipts) != wantReceipts {
			t.Fatalf("unexpected delivered source count %d, want %d", len(receipts), wantReceipts)
		}
		return receipts[len(receipts)-1]
	}
	positive.Store(true)
	visible := run("person", id)
	positive.Store(false)
	if !checkedPositive.Load() || len(field(inspect(), "sent").([]any)) != 1 {
		t.Fatal("positive source receipt did not complete the native turn")
	}
	sources, ok := field(visible, "sources").([]any)
	if !ok || len(sources) != 1 || field(sources[0], "messageId") == nil || field(sources[0], "senderId") == nil ||
		field(visible, "note") != "Identifiers only; use messages.get for available originals" || field(visible, "content") != nil {
		t.Fatalf("unexpected model source receipt: %+v", visible)
	}
	for _, unknownID := range []string{id, "prior"} {
		last := inspect()
		wantUnknown := unknown.Load() + sourceFailed.Load() + 1
		req("POST", plugin+"actions/source-memory", map[string]string{"id": unknownID})
		req("POST", plugin+"actions/new-source", map[string]any{})
		wait("unknown assertion reference rejected", func() bool { return unknown.Load()+sourceFailed.Load() == wantUnknown })
		wait("unknown turn settled", func() bool { return app.Engine.Runtime.ActiveTasks() == 0 })
		current := inspect()
		before := len(field(last, "sourceReceipts").([]any))
		for _, extra := range field(current, "sourceReceipts").([]any)[before:] {
			if field(extra, "sources") != nil {
				t.Fatal("uncaptured assertion reference disclosed sources")
			}
		}
		if len(field(current, "sent").([]any)) != len(field(last, "sent").([]any)) {
			t.Fatal("unknown assertion reference reached model delivery or send")
		}
	}
	if denied := run("anchor", field(foreign, "id").(string)); field(denied, "error") != "Memory unavailable" {
		t.Fatal("foreign memory source exposed")
	}
	_ = scene.Set("personalizationEnabled", false)
	req("PUT", scenePath, wire(scene))
	if denied := run("anchor", id); field(denied, "error") != "Memory unavailable" {
		t.Fatal("personal memory source exposed without personalization")
	}
	public := req("POST", "/api/memories", map[string]any{"scope": map[string]any{"kind": "sharedPublicKnowledge"},
		"kind": "fact", "content": "A general fixture fact."})
	if general := run("anchor", field(public, "id").(string)); field(general, "error") != nil ||
		field(general, "note") != "Identifiers only; use messages.get for available originals" {
		t.Fatal("general fact source lost without personalization")
	}
	_ = scene.Set("personalizationEnabled", true)
	req("PUT", scenePath, wire(scene))
	for _, scenario := range []string{"disable-model-read", "delete-assertion"} {
		statuses := make(chan int, 1)
		var request *http.Request
		if scenario == "disable-model-read" {
			_ = scene.Set("memoryReadEnabled", false)
			request = httptest.NewRequest("PUT", scenePath, bytes.NewReader(wire(scene))).WithContext(ctx)
			request.Header.Set("Content-Type", "application/json")
		} else {
			request = httptest.NewRequest("DELETE", "/api/memories/"+id, nil).WithContext(ctx)
		}
		sourceFixtureAuth(request, true)
		change := func() {
			response := httptest.NewRecorder()
			app.ServeHTTP(response, request)
			statuses <- response.Code
		}
		hook.Store(&change)
		last := inspect()
		target.Store(&id)
		req("POST", plugin+"actions/source-memory", map[string]string{"id": "anchor"})
		req("POST", plugin+"actions/new-source", map[string]any{})
		select {
		case status := <-statuses:
			if status != http.StatusOK {
				t.Fatalf("%s did not commit at delivery boundary: %d", scenario, status)
			}
		case <-ctx.Done():
			t.Fatal("missing source result before revocation", ctx.Err())
		}
		wait("revoked turn settled", func() bool { return app.Engine.Runtime.ActiveTasks() == 0 })
		if current := inspect(); len(field(current, "sourceReceipts").([]any)) != len(field(last, "sourceReceipts").([]any)) ||
			len(field(current, "sent").([]any)) != len(field(last, "sent").([]any)) {
			t.Fatalf("%s: revoked source reached model delivery or send", scenario)
		}
		if scenario == "disable-model-read" {
			_ = scene.Set("memoryReadEnabled", true)
			req("PUT", scenePath, wire(scene))
		}
	}
}
