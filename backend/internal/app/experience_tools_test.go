package app

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/killerprojecte/HumanEngine/backend/internal/botruntime"
	"github.com/killerprojecte/HumanEngine/backend/internal/experienceservice"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
)

func TestInstalledExperienceOutcomesTool(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	options := applicationOptions(t)
	options.Entries = nil
	app, err := Open(ctx, options)
	if err != nil {
		t.Fatal(err)
	}
	defer app.Shutdown(context.Background())
	root, release, err := app.Engine.RootContext(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer release()

	type call struct{ name, code string }
	calls := make(chan call, 16)
	var offer atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Messages []struct{ Role, Content string }
			Tools    []struct{ Function struct{ Name string } }
		}
		raw, err := io.ReadAll(r.Body)
		if err != nil || json.Unmarshal(raw, &req) != nil {
			http.Error(w, "invalid fixture request", 400)
			return
		}
		var name string
		args := "{}"
		advertised := false
		for _, f := range req.Tools {
			if f.Function.Name == "experience__propose" {
				calls <- call{"unexpected write tool", ""}
			}
			if f.Function.Name == "experience__outcomes" {
				advertised = true
			}
		}
		for _, f := range req.Tools {
			switch f.Function.Name {
			case "participation__decide":
				name = "participation__decide"
				args = `{"action":"reply","reasonCode":"fixture","targetMessageIds":["current"]}`
			case "reply__plan":
				if name == "" {
					name = "reply__plan"
				}
			}
		}
		if name == "reply__plan" {
			continuePlan := false
			for _, m := range req.Messages {
				if m.Role == "tool" {
					code := "empty"
					if bytes.Contains([]byte(m.Content), []byte("request_conclusion_first")) {
						code = "visible"
					}
					calls <- call{"result", code}
					continuePlan = true
				}
			}
			if !continuePlan {
				calls <- call{"advertised", fmt.Sprint(advertised)}
			}
			if advertised && offer.Load() && !continuePlan {
				name = "experience__outcomes"
			} else {
				// No final reply/send is needed to qualify the delivered read.
				w.Header().Set("Content-Type", "application/json")
				fmt.Fprint(w, `{"choices":[{"finish_reason":"stop","message":{"role":"assistant","content":"Fixture is done"}}]}`)
				return
			}
		}
		if name == "" {
			http.Error(w, "unexpected fixture stage", 400)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"finish_reason": "tool_calls", "message": map[string]any{"role": "assistant", "content": "", "tool_calls": []any{map[string]any{"id": name, "type": "function", "function": map[string]string{"name": name, "arguments": args}}}}}}})
	}))
	defer server.Close()
	models := fmt.Sprintf(`{"providers":[{"id":"p","name":"Loopback","protocol":"openai-chat","baseUrl":%q,"retryCount":0,"timeoutMs":4000}],"models":[{"id":"p","displayName":"Planner","providerId":"p","apiModelName":"planner","capabilities":["text","tools"]}],"routes":[{"role":"planner","orderedModelIds":["p"],"timeoutMs":5000}]}`, server.URL+"/v1")
	appRequest(t, app, "PUT", "/api/models", json.RawMessage(models))
	settings := appRequest(t, app, "GET", "/api/settings", nil).(*jsjson.Object)
	putSettings := func(enabled bool) {
		t.Helper()
		_ = settings.Set("memoryExperienceEnabled", enabled)
		raw, err := jsjson.Marshal(settings)
		if err != nil {
			t.Fatal(err)
		}
		appRequest(t, app, "PUT", "/api/settings", json.RawMessage(raw))
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	const scene = "experience-tool-fixture"
	setScene := func(personal bool) {
		appRequest(t, app, "PUT", "/api/scenes/"+scene, map[string]any{
			"id": scene, "adapterId": "synthetic", "accountId": "bot", "platformSceneId": scene,
			"kind": "group", "name": "Fixture", "participationEnabled": true, "learningEnabled": true,
			"proactiveEnabled": false, "personalizationEnabled": personal,
		})
	}
	ingest := func(id, senderID, text string) string {
		topic, err := app.Engine.Runtime.Ingest(root, scene, memoryToolObject(
			"id", id, "sceneId", scene, "senderId", senderID, "origin", "live", "status", "available",
			"sentAt", now, "receivedAt", now, "parts", []any{memoryToolObject("type", "text", "text", text)},
		), false)
		if err != nil {
			t.Fatal(err)
		}
		return topic
	}
	putSettings(true)
	setScene(true)
	ingest("intro", "bot", "Synthetic introduction.")
	ingest("instruction", "person", "Keep the conclusion first.")
	feedback, err := app.Experience.RecordFeedback(root, experienceservice.Context{SceneID: scene}, experienceservice.Feedback{
		Source: experienceservice.Source{MessageID: "instruction", SenderID: "person"},
		Target: experienceservice.Target{MessageID: "intro"},
		Code:   "request_conclusion_first", ObservedAt: now,
	})
	if err != nil || feedback == nil {
		t.Fatalf("persist actual explicit-feedback observation: %v %v", feedback, err)
	}
	topic := ingest("current", "person", "Please explain the fixture.")
	run := func(wantError string, want ...call) {
		t.Helper()
		handle, err := app.Engine.Runtime.Run(scene, topic, false)
		if err != nil {
			t.Fatal(err)
		}
		select {
		case <-handle.Done:
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
		if wantError != "" {
			result := handle.Wait()
			if result.Err == nil || !strings.Contains(result.Err.Error(), wantError) {
				t.Fatalf("wanted %s, got %+v", wantError, result)
			}
		}
		for _, expected := range want {
			select {
			case got := <-calls:
				if got != expected {
					t.Fatalf("got %+v, want %+v", got, expected)
				}
			default:
				t.Fatalf("missing %+v in turn %+v", expected, handle.Wait())
			}
		}
		select {
		case got := <-calls:
			t.Fatalf("unexpected provider call %+v", got)
		default:
		}
	}
	putSettings(false)
	run("", call{"advertised", "false"})
	putSettings(true)
	offer.Store(true)
	run("", call{"advertised", "true"}, call{"result", "visible"})
	setScene(false)
	run("personalization_disabled", call{"advertised", "true"})
	setScene(true)
	if err := app.Engine.Runtime.Recall(root, scene, "instruction"); err != nil {
		t.Fatal(err)
	}
	run("", call{"advertised", "true"}, call{"result", "empty"})
}

func TestExperienceOutcomesDispatchOwnerSurvivesFeatureToggle(t *testing.T) {
	settings := new(settingsSnapshot)
	a := new(Application)
	ext := a.experienceOutcomesExtension(settings)
	off := new(botruntime.TurnContext)
	if got := ext.ToolsForTurn(off); len(got) != 0 {
		t.Fatal("disabled feature advertised Experience outcomes")
	}
	settings.publish(map[string]any{"memoryExperienceEnabled": true})
	on := new(botruntime.TurnContext)
	if got := ext.ToolsForTurn(on); len(got) != 1 || field(got[0], "name") != "experience.outcomes" {
		t.Fatal("enabled feature did not advertise its read")
	}
	settings.publish(map[string]any{"memoryExperienceEnabled": false})
	if got := ext.ToolsForTurn(on); len(got) != 1 {
		t.Fatal("prepared results would bypass delivery after toggle")
	}
	if got := ext.ToolsForTurn(new(botruntime.TurnContext)); len(got) != 0 {
		t.Fatal("disabled feature still advertised to a new turn")
	}
}
