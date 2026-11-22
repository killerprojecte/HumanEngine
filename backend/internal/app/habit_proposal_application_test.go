package app

import (
	"context"
	"encoding/json"
	"net/url"
	"sync/atomic"
	"testing"
	"time"

	"github.com/killerprojecte/HumanEngine/backend/internal/botruntime"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
)

func TestInstalledHabitProposalAction(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 35*time.Second)
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
	wire := func(o *jsjson.Object) json.RawMessage {
		t.Helper()
		b, err := jsjson.Marshal(o)
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	settings := req("GET", "/api/settings", nil).(*jsjson.Object)
	for k, v := range map[string]any{"sourceEventRevisionsEnabled": true, "promptContextWindowEnabled": false, "debounceMs": float64(0), "groupTurnCooldownMs": float64(0)} {
		_ = settings.Set(k, v)
	}
	req("PUT", "/api/settings", wire(settings))
	req("PUT", "/api/models", json.RawMessage(`{"providers":[{"id":"p","name":"Synthetic","protocol":"runtime-fixture:deterministic","baseUrl":"https://synthetic.invalid","retryCount":0,"timeoutMs":10000}],"models":[{"id":"m","displayName":"Synthetic","providerId":"p","apiModelName":"synthetic","capabilities":["text","tools"]}],"routes":[{"role":"planner","orderedModelIds":["m"],"timeoutMs":10000},{"role":"replyer","orderedModelIds":["m"],"timeoutMs":10000}]}`))
	plugin := "/api/plugins/runtime-fixture/"
	req("PUT", plugin+"config", map[string]any{"timestamp": time.Now().UTC().Format(time.RFC3339Nano)})
	req("PUT", plugin+"enabled", map[string]bool{"enabled": true})
	inspect := func() any { return req("POST", plugin+"actions/inspect", map[string]any{}) }
	sceneID := field(field(inspect(), "scene"), "id").(string)
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
	set := func(name string, value bool) {
		t.Helper()
		_ = scene.Set(name, value)
	}
	set("learningEnabled", true)
	set("participationEnabled", true)
	// Message-only proposals need neither a model-read nor a personalization grant.
	set("memoryReadEnabled", false)
	set("personalizationEnabled", false)
	req("PUT", scenePath, wire(scene))
	var noReply atomic.Int32
	app.Engine.Runtime.Observe(func(e botruntime.Event) {
		app.observeRuntime(e)
		if e.SceneID == sceneID && e.Stage == "planner" && e.Cause != nil && e.Cause.Error() == "planner_invalid_no_reply" {
			noReply.Add(1)
		}
	})
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
	run := func(arguments map[string]any, text, status, reason string) (string, any) {
		t.Helper()
		last := inspect()
		before := len(field(last, "proposalReceipts").([]any))
		calls := field(last, "proposalCalls").(float64)
		denied := noReply.Load()
		req("POST", plugin+"actions/habit-proposal", map[string]any{"candidate": arguments, "text": text, "noReply": status == ""})
		req("POST", plugin+"actions/new-source", map[string]any{})
		wait("Planner used habit proposal action", func() bool { return field(inspect(), "proposalCalls").(float64) > calls })
		wait("proposal turn settled", func() bool { return app.Engine.Runtime.ActiveTasks() == 0 })
		current := inspect()
		if field(field(current, "proposalTool"), "name") != habitProposalTool {
			t.Fatal("Planner did not advertise the habit proposal action")
		}
		source := field(current, "source").(string)
		receipts := field(current, "proposalReceipts").([]any)
		if status == "" {
			if len(receipts) != before || noReply.Load() != denied+1 || len(field(current, "sent").([]any)) != len(field(last, "sent").([]any)) {
				t.Fatal("forbidden noReply proposal escaped plan validation")
			}
			return source, nil
		}
		if len(receipts) != before+1 {
			t.Fatalf("missing proposal action receipt: receipts=%d, want %d", len(receipts), before+1)
		}
		r := receipts[before]
		if field(r, "status") != status || reason != "" && field(r, "reason") != reason {
			t.Fatalf("unexpected proposal receipt: %+v", r)
		}
		return source, field(r, "habitId")
	}
	read := func() []*jsjson.Object {
		t.Helper()
		habits, err := app.Engine.Habits.AdminList(ctx)
		if err != nil {
			t.Fatal(err)
		}
		return habits
	}
	assertStored := func(kind, source string, id any) {
		t.Helper()
		if alias, ok := id.(string); !ok || alias == "" {
			t.Fatal("saved action had no projected habit receipt")
		}
		for _, habit := range read() {
			if field(habit, "kind") != kind || field(habit, "sceneId") != sceneID {
				continue
			}
			for _, evidence := range field(habit, "sources").([]any) {
				if field(evidence, "messageId") == source {
					return
				}
			}
		}
		t.Fatalf("no durable %s proposal with its cited source", kind)
	}
	rule, ruleID := run(map[string]any{"kind": "sceneRule", "content": "Use short replies in this group"}, "Use short replies in this group.", "saved", "")
	assertStored("sceneRule", rule, ruleID)
	expression, expressionID := run(map[string]any{"kind": "botExpression", "preference": "concise", "direction": 1},
		"In this group, the assistant should use short replies.", "saved", "")
	assertStored("botExpression", expression, expressionID)
	if err := app.Engine.Runtime.Recall(ctx, sceneID, rule); err != nil {
		t.Fatal(err)
	}
	for _, habit := range read() {
		for _, evidence := range field(habit, "sources").([]any) {
			if field(evidence, "messageId") == rule {
				t.Fatal("recalled proposal source remained in a habit")
			}
		}
	}
	before := len(read())
	never := map[string]any{"kind": "sceneRule", "content": "Never persist this synthetic policy"}
	run(never, "This is an explicit synthetic policy.", "", "")
	set("learningEnabled", false)
	req("PUT", scenePath, wire(scene))
	run(never, "This is an explicit synthetic policy.", "rejected", "learning_disabled")
	if len(read()) != before {
		t.Fatal("denied proposals changed the persistent habit count")
	}
}
