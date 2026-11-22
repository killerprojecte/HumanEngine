package app

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/killerprojecte/HumanEngine/backend/internal/botruntime"
	"github.com/killerprojecte/HumanEngine/backend/internal/dispatch"
	"github.com/killerprojecte/HumanEngine/backend/internal/experienceservice"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
)

func TestInstalledExperiencePolicyTools(t *testing.T) {
	for _, status := range []string{"candidate", "active"} {
		t.Run(status, func(t *testing.T) { testInstalledExperiencePolicyTools(t, status) })
	}
}

func testInstalledExperiencePolicyTools(t *testing.T, status string) {
	t.Helper()
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

	const scene = "experience-tool-fixture"
	scope := experienceservice.Context{SceneID: scene}
	beforeProposal := make(chan experienceservice.AdminPolicy, 1)
	type call struct{ name, code string }
	calls := make(chan call, 16)
	var offer atomic.Bool
	var proposalMode atomic.Int32
	var sends atomic.Int32
	var outcomeAnchor atomic.Pointer[jsjson.Object]
	app.Engine.Runtime.AddExtension(&botruntime.Extension{
		Tools: []*jsjson.Object{memoryToolObject("name", "fixture.experienceRef", "description", "A synthetic known reference, not an Experience outcome receipt.",
			"inputSchema", memoryToolObject("type", "object", "properties", memoryToolObject(), "additionalProperties", false), "effect", "read")},
		Execute: func(_ context.Context, _ string, _ any, _ *botruntime.TurnContext, done func(any, error)) {
			done([]any{outcomeAnchor.Load()}, nil)
		},
	})
	app.Engine.Runtime.AddAdapter("synthetic", &botruntime.Adapter{Transport: &dispatch.Adapter{
		Send: func(dispatch.SceneRef, dispatch.Segment, *dispatch.Signal, func() error) *dispatch.Future[dispatch.Receipt] {
			sends.Add(1)
			return dispatch.Rejected[dispatch.Receipt](fmt.Errorf("unexpected Experience transport"))
		},
	}})
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
		var contract struct {
			ActionTools  []struct{ Name, Effect string }
			PlanContract struct{ TargetMessageIDs, ContextMessageIDs []string }
		}
		var outcomes []struct {
			ID      string
			Version int64
			Source  struct{ SenderID string }
		}
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
			for _, m := range req.Messages {
				if m.Role == "user" {
					_ = json.Unmarshal([]byte(m.Content), &contract)
				}
				if m.Role != "tool" {
					continue
				}
				var receipt struct {
					ToolName string
					Value    json.RawMessage
				}
				if json.Unmarshal([]byte(m.Content), &receipt) == nil &&
					(receipt.ToolName == "experience.outcomes" || receipt.ToolName == "fixture.experienceRef") {
					_ = json.Unmarshal(receipt.Value, &outcomes)
				}
			}
			if !continuePlan {
				writeAdvertised := false
				for _, tool := range contract.ActionTools {
					writeAdvertised = writeAdvertised || tool.Name == "experience.propose" && tool.Effect == "learningCandidate"
				}
				calls <- call{"advertised", fmt.Sprintf("%t/%t", advertised, writeAdvertised)}
			}
			if advertised && offer.Load() && !continuePlan {
				name = "experience__outcomes"
				if proposalMode.Load() == 3 {
					name = "fixture__experienceRef"
				}
			} else if mode := proposalMode.Load(); mode != 0 && len(outcomes) == 1 {
				if mode == 1 {
					// The same-user active hint, when selected, was already
					// exposed before this model continuation and is not a proposal use.
					view, err := app.Experience.AdminList(r.Context(), scope, experienceservice.ListOptions{})
					if err != nil || len(view.Items) != 1 {
						http.Error(w, "expected the pre-proposal policy", 500)
						return
					}
					beforeProposal <- view.Items[0]
				}
				out := outcomes[0]
				arguments := map[string]any{
					"action":      "conclusion_before_detail",
					"condition":   map[string]any{"kind": "response_style", "personId": out.Source.SenderID},
					"outcomeRefs": []map[string]any{{"id": out.ID, "version": out.Version}},
				}
				if mode == 2 {
					arguments["extra"] = true
				}
				encoded, _ := json.Marshal(map[string]any{
					"action": "noReply", "targetMessageIds": contract.PlanContract.TargetMessageIDs, "contextMessageIds": contract.PlanContract.ContextMessageIDs,
					"strategy": "Record only the scoped synthetic policy candidate.", "constraints": []any{}, "facts": []any{}, "assets": []any{}, "emoji": []any{}, "expressionGoals": []any{},
					"actions": []map[string]any{{"toolName": "experience.propose", "arguments": arguments}},
				})
				args = string(encoded)
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
		_ = settings.Set("memoryExperienceAutoActivateEnabled", status == "active")
		_ = settings.Set("memoryExperienceHintsEnabled", status == "active")
		raw, err := jsjson.Marshal(settings)
		if err != nil {
			t.Fatal(err)
		}
		appRequest(t, app, "PUT", "/api/settings", json.RawMessage(raw))
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	setScene := func(personal, read bool) {
		appRequest(t, app, "PUT", "/api/scenes/"+scene, map[string]any{
			"id": scene, "adapterId": "synthetic", "accountId": "bot", "platformSceneId": scene,
			"kind": "group", "name": "Fixture", "participationEnabled": true, "learningEnabled": true,
			"proactiveEnabled": false, "personalizationEnabled": personal, "memoryReadEnabled": read,
		})
	}
	ingest := func(id, senderID, text, reply string) string {
		message := memoryToolObject("id", id, "sceneId", scene, "senderId", senderID, "origin", "live", "status", "available",
			"sentAt", now, "receivedAt", now, "parts", []any{memoryToolObject("type", "text", "text", text)})
		if reply != "" {
			_ = message.Set("replyToId", reply)
		}
		topic, err := app.Engine.Runtime.Ingest(root, scene, message, false)
		if err != nil {
			t.Fatal(err)
		}
		return topic
	}
	putSettings(true)
	setScene(true, true)
	ingest("intro", "bot", "Synthetic introduction.", "")
	ingest("instruction", "person", "请先说结论", "intro")
	outcomes, err := app.Experience.ListOutcomes(root, scope, 20)
	if err != nil || len(outcomes) != 1 || outcomes[0].Source.MessageID != "instruction" {
		t.Fatalf("actual received feedback did not produce its outcome: %+v, %v", outcomes, err)
	}
	outcome := outcomes[0]
	outcomeAnchor.Store(memoryToolObject("id", outcome.ID, "version", float64(outcome.Version),
		"source", memoryToolObject("senderId", outcome.Source.SenderID)))
	initial, err := app.Experience.AdminList(root, scope, experienceservice.ListOptions{})
	if err != nil || len(initial.Items) != 1 || initial.Items[0].Status != status ||
		initial.Items[0].Trial != nil || initial.Items[0].TrialEligible ||
		status == "active" && initial.Items[0].ActivationBasis != "explicit_preference" {
		t.Fatalf("feedback did not produce the expected policy without a trial: %+v, %v", initial.Items, err)
	}
	before := initial.Items[0]

	topic := ingest("current", "person", "Please explain the fixture.", "")
	run := func(wantError string, want ...call) botruntime.TurnResult {
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
		result := handle.Wait()
		if wantError != "" {
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
		return result
	}
	if status == "candidate" {
		putSettings(false)
		run("", call{"advertised", "false/false"})
		putSettings(true)
	}
	offer.Store(true)
	proposalMode.Store(1)
	result := run("", call{"advertised", "true/true"}, call{"result", "visible"})
	if result.Err != nil || result.Reason != "planner_no_reply" || len(result.Receipts) != 0 {
		t.Fatalf("exact delivered-outcome proposal did not finish through noReply: %+v", result)
	}
	var actionWitness experienceservice.AdminPolicy
	select {
	case actionWitness = <-beforeProposal:
	default:
		t.Fatal("missing pre-proposal policy witness")
	}
	expectedUses := maps.Clone(before.Uses)
	if status == "active" {
		expectedUses["exposed"]++
	}
	if actionWitness.ID != before.ID || actionWitness.Version != before.Version || actionWitness.Status != before.Status ||
		actionWitness.Trial != nil || actionWitness.TrialEligible || !maps.Equal(actionWitness.Uses, expectedUses) {
		t.Fatalf("before proposal: only actual Planner hint exposure may change uses; got %+v, want %v", actionWitness.Uses, expectedUses)
	}
	stored, err := app.Experience.AdminList(root, scope, experienceservice.ListOptions{})
	if err != nil || len(stored.Items) != 1 || stored.Items[0].ID != actionWitness.ID ||
		stored.Items[0].Version != actionWitness.Version || stored.Items[0].Status != actionWitness.Status ||
		stored.Items[0].Trial != nil || stored.Items[0].TrialEligible ||
		!maps.Equal(stored.Items[0].Uses, actionWitness.Uses) || sends.Load() != 0 {
		t.Fatalf("proposal must leave the same policy, usage and absent trial with zero sends: %+v (sends=%d), %v", stored.Items, sends.Load(), err)
	}
	proposalMode.Store(2)
	run("experience_proposal_invalid", call{"advertised", "true/true"}, call{"result", "visible"})
	proposalMode.Store(3)
	run("experience_outcome_not_exposed", call{"advertised", "true/true"}, call{"result", "empty"})

	setScene(false, true)
	run("personalization_disabled", call{"advertised", "true/true"}, call{"result", "empty"})
	setScene(true, false)
	run("memory_read_disabled", call{"advertised", "true/true"}, call{"result", "empty"})
	proposalMode.Store(0)
	setScene(true, true)
	if err := app.Engine.Runtime.Recall(root, scene, "instruction"); err != nil {
		t.Fatal(err)
	}
	run("", call{"advertised", "true/true"}, call{"result", "empty"})
}

func TestExperienceWriteDropsWhileOutcomesOwnerSurvivesFeatureToggle(t *testing.T) {
	settings := new(settingsSnapshot)
	a := new(Application)
	ext := a.experienceToolsExtension(settings)
	off := new(botruntime.TurnContext)
	if got := ext.ToolsForTurn(off); len(got) != 0 {
		t.Fatal("disabled feature advertised Experience outcomes")
	}
	settings.publish(map[string]any{"memoryExperienceEnabled": true})
	on := new(botruntime.TurnContext)
	if got := ext.ToolsForTurn(on); len(got) != 2 || field(got[0], "name") != "experience.outcomes" || field(got[1], "name") != "experience.propose" {
		t.Fatal("enabled feature did not advertise both tools")
	}
	settings.publish(map[string]any{"memoryExperienceEnabled": false})
	if got := ext.ToolsForTurn(on); len(got) != 1 || field(got[0], "name") != "experience.outcomes" {
		t.Fatal("toggle must withdraw the write but retain the read delivery owner")
	}
	if got := ext.ToolsForTurn(new(botruntime.TurnContext)); len(got) != 0 {
		t.Fatal("disabled feature still advertised to a new turn")
	}
}
