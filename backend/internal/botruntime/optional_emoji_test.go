package botruntime

import (
	"context"
	"encoding/json"
	"os"
	"sync/atomic"
	"testing"

	"github.com/killerprojecte/HumanEngine/backend/internal/dispatch"
	"github.com/killerprojecte/HumanEngine/backend/internal/replyplan"
)

func TestTextOnlyPlanWithoutEmojiReachesReplyerAndDispatch(t *testing.T) {
	var plans, replies, sends atomic.Int64
	r, _, _ := testRuntime(t, ModelFunc(func(_ context.Context, req ModelRequest, done func(Completion, error)) {
		switch req.Stage {
		case "decision":
			done(fixtureDecision("reply"), nil)
		case "plan":
			plans.Add(1)
			result := fixturePlan(req, "reply", []any{})
			args := result.ToolCalls[0].Arguments.(*Object)
			for _, field := range []string{"emoji", "deliveryTool", "memorySelections"} {
				args.Delete(field)
			}
			done(completion(result.ToolCalls, ""), nil)
		case "replyer":
			replies.Add(1)
			done(completion([]replyplan.ToolCall{fixtureSend("A concise warm response.", false)}, ""), nil)
		default:
			t.Errorf("unexpected stage %s", req.Stage)
			done(Completion{}, code("unexpected_stage"))
		}
	}))
	if err := r.RegisterScene(fixtureScene(false)); err != nil {
		t.Fatal(err)
	}
	topic, err := r.Ingest(context.Background(), "scene", fixtureMessage("scene", "source", "Please offer a warm response."), false)
	if err != nil {
		t.Fatal(err)
	}
	r.AddAdapter("fixture", &Adapter{Transport: &dispatch.Adapter{Send: func(_ dispatch.SceneRef, segment dispatch.Segment, _ *dispatch.Signal, before func() error) *dispatch.Future[dispatch.Receipt] {
		if err := before(); err != nil {
			return dispatch.Resolved(dispatch.Receipt{Status: dispatch.Cancelled})
		}
		sends.Add(1)
		return dispatch.Resolved(dispatch.Receipt{Status: dispatch.Sent, MessageID: ptr("sent")})
	}}})
	handle, err := r.Run("scene", topic, false)
	if err != nil {
		t.Fatal(err)
	}
	result := awaitTurn(t, handle)
	if plans.Load() != 1 || replies.Load() != 1 || sends.Load() != 1 {
		t.Fatalf("plan/replyer/send=%d/%d/%d result=%+v", plans.Load(), replies.Load(), sends.Load(), result)
	}
}

// Only the two source-verified input-contract fields differ from frozen traces.
// Keep comparing every remaining request field, including full tool schemas.
func optionalEmojiOracleRequests(t *testing.T, requests []any) []any {
	t.Helper()
	data, err := os.ReadFile("../replyplan/schema/plan-input-defaults.json")
	if err != nil {
		t.Fatal(err)
	}
	var delta map[string]any
	if err := json.Unmarshal(data, &delta); err != nil {
		t.Fatal(err)
	}
	for _, request := range requests {
		row := request.(map[string]any)
		tools, _ := row["tools"].([]any)
		for _, tool := range tools {
			definition := tool.(map[string]any)
			if definition["name"] != "reply.plan" {
				continue
			}
			schema := definition["inputSchema"].(map[string]any)
			schema["properties"].(map[string]any)["emoji"] = delta["emoji"]
			schema["required"] = delta["required"]
		}
	}
	return requests
}
