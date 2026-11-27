//go:build linux && sqlite_fts5 && humanengine_icu

package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"sync/atomic"
	"testing"
	"time"
)

const (
	responsesGroup  = "913"
	responsesInput  = "Synthetic Responses stream witness."
	responsesReply  = "Responses stream reply."
	responsesReadID = "call_responses_read"
)

type snowResponsesWitness struct {
	requests atomic.Int32
	sends    atomic.Int32
}

func (p *snowResponsesWitness) action(w http.ResponseWriter, parts []snowLanePart) {
	if len(parts) != 2 || parts[0].Type != "reply" || parts[0].Data["id"] != "401" ||
		parts[1].Type != "text" || parts[1].Data["text"] != responsesReply || p.sends.Add(1) != 1 {
		http.Error(w, "wrong or repeated streamed reply", 400)
		return
	}
	fmt.Fprint(w, `{"status":"ok","retcode":0,"data":{"message_id":8401}}`)
}

func (p *snowResponsesWitness) model(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Model  string
		Stream bool
		Input  []struct {
			Role, Type, Output string
			CallID             string `json:"call_id"`
			Content            json.RawMessage
		}
		Tools []struct{ Name string }
	}
	if r.URL.Path != "/v1/responses" || json.NewDecoder(r.Body).Decode(&body) != nil || body.Model != "responses-fixture" || !body.Stream {
		http.Error(w, "expected an actual default-on Responses stream", 400)
		return
	}
	var user struct {
		Messages     []struct{ ID string }
		PlanContract struct{ TargetMessageIDs, ContextMessageIDs []string }
		Plan         struct{ TargetMessageIDs []string }
	}
	var callOutputs []string
	var toolReceipt struct {
		ToolName string
		Value    json.RawMessage
	}
	for _, item := range body.Input {
		if item.Role == "user" {
			var text string
			if json.Unmarshal(item.Content, &text) == nil {
				_ = json.Unmarshal([]byte(text), &user)
			}
		}
		if item.Type == "function_call_output" {
			callOutputs = append(callOutputs, item.CallID)
			_ = json.Unmarshal([]byte(item.Output), &toolReceipt)
		}
	}
	step := int(p.requests.Add(1))
	var name, callID string
	var args any
	switch step {
	case 1:
		name, callID = "participation__decide", "call_responses_decide"
		if len(callOutputs) != 0 || len(user.Messages) != 1 || user.Messages[0].ID != "401" {
			http.Error(w, "wrong decision context", 400)
			return
		}
		args = map[string]any{"action": "reply", "reasonCode": "fixture", "targetMessageIds": []string{"401"}}
	case 2:
		name, callID = "memes__search", responsesReadID
		if len(callOutputs) != 0 || !slices.Equal(user.PlanContract.TargetMessageIDs, []string{"401"}) {
			http.Error(w, "wrong initial plan context", 400)
			return
		}
		args = map[string]any{"query": "synthetic empty sticker lookup"}
	case 3:
		name, callID = "reply__plan", "call_responses_plan"
		if !slices.Equal(callOutputs, []string{responsesReadID}) || toolReceipt.ToolName != "memes.search" || len(toolReceipt.Value) == 0 ||
			!slices.Equal(user.PlanContract.TargetMessageIDs, []string{"401"}) {
			http.Error(w, "Responses continuation lost the exact call ID or tool result", 400)
			return
		}
		args = map[string]any{"action": "reply", "deliveryTool": "messages.send", "targetMessageIds": user.PlanContract.TargetMessageIDs,
			"contextMessageIds": user.PlanContract.ContextMessageIDs, "strategy": "Respond to the synthetic request.",
			"constraints": []any{}, "facts": []any{}, "assets": []any{}, "emoji": []any{}, "actions": []any{}}
	case 4:
		name, callID = "messages__send", "call_responses_send"
		if len(callOutputs) != 0 || !slices.Equal(user.Plan.TargetMessageIDs, []string{"401"}) {
			http.Error(w, "wrong Replyer context", 400)
			return
		}
		args = map[string]any{"operationId": "responses-stream-send", "segments": []any{map[string]any{
			"replyToId": "401", "parts": []any{map[string]any{"type": "text", "text": responsesReply}}}}}
	default:
		http.Error(w, "duplicate Responses request", 400)
		return
	}
	if !slices.ContainsFunc(body.Tools, func(tool struct{ Name string }) bool { return tool.Name == name }) {
		http.Error(w, "expected tool is not advertised", 400)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	flush := http.NewResponseController(w)
	frame := func(kind string, fields map[string]any) {
		fields["type"] = kind
		data, _ := json.Marshal(fields)
		fmt.Fprintf(w, "event: %s\ndata: %s\n\n", kind, data)
		_ = flush.Flush()
	}
	responseID, itemID := fmt.Sprint("resp_fixture_", step), fmt.Sprint("fc_fixture_", step)
	frame("response.created", map[string]any{"response": map[string]any{"id": responseID, "status": "in_progress"}})
	frame("response.output_item.added", map[string]any{"output_index": 0, "item": map[string]any{
		"id": itemID, "type": "function_call", "name": name, "call_id": callID, "arguments": ""}})
	time.Sleep(70 * time.Millisecond) // Metadata is not first usable content.
	argumentJSON, _ := json.Marshal(args)
	arg := string(argumentJSON)
	for _, part := range []string{arg[:5], arg[5:]} {
		frame("response.function_call_arguments.delta", map[string]any{"output_index": 0, "item_id": itemID, "delta": part})
	}
	time.Sleep(110 * time.Millisecond) // Usable content arrives well before the terminal counters.
	item := map[string]any{"id": itemID, "type": "function_call", "name": name, "call_id": callID, "arguments": arg, "status": "completed"}
	frame("response.output_item.done", map[string]any{"output_index": 0, "item": item})
	frame("response.completed", map[string]any{"response": map[string]any{"id": responseID, "status": "completed", "output": []any{item},
		"usage": map[string]any{"input_tokens": 100 + step, "output_tokens": 20 + step, "total_tokens": 120 + 2*step,
			"input_tokens_details": map[string]any{"cached_tokens": 10 + step}}}})
}

func (p *snowResponsesWitness) run(t *testing.T, addr, token string, post func(string, string, int), encode func(string) string) {
	t.Helper()
	model := httptest.NewServer(http.HandlerFunc(p.model))
	defer model.Close()
	models := fmt.Sprintf(`{"providers":[{"id":"responses","name":"Synthetic Responses","protocol":"openai-responses","baseUrl":%q,"retryCount":0,"timeoutMs":5000}],"models":[{"id":"responses","displayName":"Fixture Responses","providerId":"responses","apiModelName":"responses-fixture","capabilities":["text","tools"]}],"routes":[{"role":"planner","orderedModelIds":["responses"],"timeoutMs":7000},{"role":"replyer","orderedModelIds":["responses"],"timeoutMs":7000}]}`, model.URL+"/v1")
	commandRequest(t, addr, "PUT", "/api/models", []byte(models), 200)
	scene := snowLaneScene(responsesGroup, encode)
	path := "/api/scenes/" + url.PathEscape(scene)
	config := fmt.Sprintf(`{"id":%q,"adapterId":"snowluma","accountId":"42","platformSceneId":%q,"kind":"group","name":"Responses stream","participationEnabled":true,"learningEnabled":false,"proactiveEnabled":false}`, scene, responsesGroup)
	commandRequest(t, addr, "PUT", path, []byte(config), 200)
	raw := fmt.Sprintf(`{"post_type":"message","message_type":"group","self_id":42,"group_id":%s,"user_id":7,"message_id":401,"time":%d,"message":[{"type":"text","data":{"text":%q}}]}`, responsesGroup, time.Now().Unix(), responsesInput)
	post(raw, token, 204)
	for until := time.Now().Add(8 * time.Second); ; time.Sleep(20 * time.Millisecond) {
		var body struct {
			Items []struct{ ID, ReplyToID, SenderID string }
		}
		if err := json.Unmarshal(commandRequest(t, addr, "GET", path+"/history?source=local", nil, 200), &body); err != nil {
			t.Fatal(err)
		}
		if slices.ContainsFunc(body.Items, func(row struct{ ID, ReplyToID, SenderID string }) bool {
			return row.ID == snowLaneMessage(scene, "8401", encode) && row.ReplyToID == snowLaneMessage(scene, "401", encode) && row.SenderID == "bot"
		}) {
			break
		}
		if time.Now().After(until) {
			t.Fatalf("Responses stream did not retain the quoted ACK (requests=%d sends=%d): %+v", p.requests.Load(), p.sends.Load(), body)
		}
	}
	if p.requests.Load() != 4 || p.sends.Load() != 1 {
		t.Fatalf("wrong Responses call or ACK count: %d/%d", p.requests.Load(), p.sends.Load())
	}
}

func verifySnowResponsesAttempts(t *testing.T, db *sql.DB) {
	t.Helper()
	rows, err := db.Query(`SELECT role,status,transport_mode,first_token_kind,first_token_ms,duration_ms,
		input_tokens,output_tokens,cache_read_tokens,failure_reason FROM model_calls WHERE provider_id='responses' ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for i, want := range []string{"planner", "planner", "planner", "replyer"} {
		if !rows.Next() {
			t.Fatalf("missing Responses model attempt %d: %v", i, rows.Err())
		}
		var role, status, mode, kind string
		var first, duration float64
		var input, output, cached int
		var failure sql.NullString
		if err := rows.Scan(&role, &status, &mode, &kind, &first, &duration, &input, &output, &cached, &failure); err != nil {
			t.Fatal(err)
		}
		if role != want || status != "completed" || mode != "stream" || kind != "tool" || first < 50 || duration-first < 80 ||
			input != 101+i || output != 21+i || cached != 11+i || failure.Valid {
			t.Fatalf("wrong Responses attempt: %s %s %s %s first=%v duration=%v usage=%d/%d/%d failure=%v", role, status, mode, kind, first, duration, input, output, cached, failure)
		}
	}
	if rows.Next() || rows.Err() != nil {
		t.Fatalf("unexpected extra Responses attempt: %v", rows.Err())
	}
	t.Log("standalone-snowluma-responses-sse-exact-continuation-quoted-ack-durable-stream-usage")
}
