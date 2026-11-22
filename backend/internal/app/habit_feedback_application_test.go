package app

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"sync/atomic"
	"testing"
	"time"

	"github.com/killerprojecte/HumanEngine/backend/internal/botruntime"
	"github.com/killerprojecte/HumanEngine/backend/internal/habitservice"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
)

func TestInstalledHabitFeedbackAction(t *testing.T) {
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
	now := time.Now().UTC()
	req("PUT", plugin+"config", map[string]any{"timestamp": now.Format(time.RFC3339Nano)})
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
	wait("fixture source projected", func() bool {
		var seen bool
		err = app.Engine.Sources.WithRuntimeReads(ctx, func(reads botruntime.ReadAuthority) error {
			message, e := app.Engine.Runtime.PreviewMessage(sceneID, sourceID, reads)
			seen = message != nil
			return e
		})
		return err == nil && seen && app.Engine.Runtime.ActiveTasks() == 0
	})
	set := func(field string, value bool) {
		t.Helper()
		_ = scene.Set(field, value)
		req(http.MethodPut, scenePath, wire(scene))
	}
	set("learningEnabled", true)
	source := memoryToolObject("sceneId", sceneID, "messageId", sourceID, "senderId", "synthetic-user")
	evidence := memoryToolObject("sceneId", sceneID, "messageId", sourceID, "senderId", "synthetic-user", "origin", "live", "text", "I prefer concise replies.", "timestamp", float64(now.UnixMilli()), "available", true, "isBot", false)
	habit, err := app.Engine.Habits.Propose(ctx, habitservice.Context{SceneID: sceneID, SourceEvidence: []any{evidence}},
		memoryToolObject("kind", "sceneRule", "content", "Prefer concise replies", "sources", []any{source}))
	if err != nil || field(habit, "status") != "saved" {
		t.Fatalf("seed habit: %+v, %v", habit, err)
	}
	habitID := field(field(habit, "habit"), "id").(string)
	set("participationEnabled", true)
	var recalled atomic.Pointer[string]
	app.Engine.Runtime.AddExtension(&botruntime.Extension{
		Tools: []*jsjson.Object{memoryToolObject("name", "fixture.habitAnchor", "description", "Synthetic known reference to test the independent write gate.", "effect", "read", "inputSchema", memoryToolObject("type", "object", "properties", memoryToolObject(), "additionalProperties", false))},
		Execute: func(_ context.Context, _ string, _ any, _ *botruntime.TurnContext, done func(any, error)) {
			output := memoryToolObject("habitId", habitID)
			if id := recalled.Load(); id != nil {
				_ = output.Set("sourceMessageId", *id)
			}
			done(output, nil)
		},
	})
	var denied atomic.Int32
	app.Engine.Runtime.Observe(func(e botruntime.Event) {
		app.observeRuntime(e)
		if e.SceneID == sceneID && e.Stage == "planner" && e.Cause != nil && e.Cause.Error() == "personalization_disabled" {
			denied.Add(1)
		}
	})
	readSources := func() []any {
		t.Helper()
		habits, e := app.Engine.Habits.AdminList(ctx)
		if e != nil {
			t.Fatal(e)
		}
		for _, h := range habits {
			if field(h, "id") == habitID {
				return field(h, "sources").([]any)
			}
		}
		t.Fatal("durable habit missing")
		return nil
	}
	directionFor := func(messageID string) any {
		t.Helper()
		for _, row := range readSources() {
			if field(row, "messageId") == messageID {
				return field(row, "direction")
			}
		}
		return nil
	}
	run := func(direction float64, status, reason string) string {
		t.Helper()
		last := inspect()
		receipts := len(field(last, "habitReceipts").([]any))
		calls := field(last, "habitCalls").(float64)
		failed := denied.Load()
		sends := len(field(last, "sent").([]any))
		req("POST", plugin+"actions/habit-feedback", map[string]any{"direction": direction, "anchor": status == "" || reason == "invalid_source_evidence"})
		req("POST", plugin+"actions/new-source", map[string]any{})
		wait("Planner used habit feedback action", func() bool { return field(inspect(), "habitCalls").(float64) > calls })
		wait("feedback turn settled", func() bool { return app.Engine.Runtime.ActiveTasks() == 0 })
		current := inspect()
		if field(field(current, "habitTool"), "name") != habitFeedbackTool {
			t.Fatal("Planner did not advertise the habit action")
		}
		got := field(current, "habitReceipts").([]any)
		switch status {
		case "":
			if len(got) != receipts || denied.Load() != failed+1 {
				t.Fatalf("denied feedback reached Replyer or lacked its failure: %#v, %d", got, denied.Load())
			}
		case "saved":
			// An exact rules.list snapshot is stale after its own successful
			// mutation. Do not send a reply based on the old learned hint.
			if len(got) != receipts || len(field(current, "sent").([]any)) != sends {
				t.Fatal("the edited hint's stale turn reached Replyer or transport")
			}
		default:
			if len(got) != receipts+1 {
				t.Fatalf("missing action receipt: %#v", got)
			}
			r := got[receipts]
			if field(r, "status") != status || reason != "" && field(r, "reason") != reason {
				t.Fatalf("unexpected action receipt: %#v", r)
			}
		}
		return field(current, "source").(string)
	}
	positive := run(1, "saved", "")
	if directionFor(positive) != float64(1) {
		t.Fatal("positive source was not durably recorded")
	}
	negative := run(-1, "saved", "")
	if directionFor(negative) != float64(-1) {
		t.Fatal("negative source was not durably recorded")
	}
	if err := app.Engine.Runtime.Recall(ctx, sceneID, negative); err != nil {
		t.Fatal(err)
	}
	if directionFor(negative) != nil {
		t.Fatal("recalled feedback was still present")
	}
	recalled.Store(&negative)
	run(1, "rejected", "invalid_source_evidence")
	recalled.Store(nil)
	if directionFor(negative) != nil {
		t.Fatal("recalled source was accepted again")
	}
	set("learningEnabled", false)
	if directionFor(run(-1, "rejected", "learning_disabled")) != nil {
		t.Fatal("disabled learning committed evidence")
	}
	set("learningEnabled", true)
	set("personalizationEnabled", false)
	if directionFor(run(1, "", "")) != nil {
		t.Fatal("disabled personalization committed evidence")
	}
}
