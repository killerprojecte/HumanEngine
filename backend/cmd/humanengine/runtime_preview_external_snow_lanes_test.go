//go:build linux && sqlite_fts5 && humanengine_icu

package main

import (
	"encoding/json"
	"fmt"
	"maps"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

var laneGroup = map[string]string{"201": "911", "202": "911", "301": "912"}

type snowLaneWitness struct {
	mu      sync.Mutex
	order   []string
	calls   map[string][3]int
	held    chan struct{}
	release chan struct{}
}

func newSnowLaneWitness() *snowLaneWitness {
	return &snowLaneWitness{calls: make(map[string][3]int), held: make(chan struct{}), release: make(chan struct{})}
}

type snowLanePart struct {
	Type string
	Data map[string]string
}

func (p *snowLaneWitness) action(w http.ResponseWriter, group string, parts []snowLanePart) {
	if len(parts) != 2 || parts[0].Type != "reply" || parts[1].Type != "text" {
		http.Error(w, "unexpected lane action", 400)
		return
	}
	id := parts[0].Data["id"]
	if laneGroup[id] != group || parts[1].Data["text"] != "Synthetic reply "+id {
		http.Error(w, "incorrect lane delivery", 400)
		return
	}
	p.mu.Lock()
	p.order = append(p.order, id)
	p.mu.Unlock()
	fmt.Fprintf(w, `{"status":"ok","retcode":0,"data":{"message_id":%q}}`, "8"+id)
}

func (p *snowLaneWitness) model(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Messages []struct{ Role, Content string }
		Tools    []struct{ Function struct{ Name string } }
	}
	if r.URL.Path != "/v1/chat/completions" || json.NewDecoder(r.Body).Decode(&req) != nil || len(req.Tools) == 0 {
		http.Error(w, "invalid model fixture request", 400)
		return
	}
	var user struct {
		Messages     []struct{ ID string }
		PlanContract struct{ TargetMessageIDs, ContextMessageIDs []string }
		Plan         struct{ TargetMessageIDs []string }
	}
	var wire string
	for _, m := range req.Messages {
		if m.Role == "user" {
			wire = m.Content
		}
	}
	if json.Unmarshal([]byte(wire), &user) != nil {
		http.Error(w, "invalid user context", 400)
		return
	}
	var target string
	var stage int
	var args any
	offered := func(name string) bool {
		return slices.ContainsFunc(req.Tools, func(tool struct{ Function struct{ Name string } }) bool { return tool.Function.Name == name })
	}
	switch {
	case offered("participation__decide"):
		for _, m := range user.Messages {
			if laneGroup[m.ID] != "" && m.ID > target {
				target = m.ID
			}
		}
		args = map[string]any{"action": "reply", "reasonCode": "fixture", "targetMessageIds": []string{target}}
	case offered("reply__plan"):
		stage = 1
		if len(user.PlanContract.TargetMessageIDs) == 1 {
			target = user.PlanContract.TargetMessageIDs[0]
		}
		args = map[string]any{"action": "reply", "deliveryTool": "messages.send", "targetMessageIds": user.PlanContract.TargetMessageIDs,
			"contextMessageIds": user.PlanContract.ContextMessageIDs, "strategy": "Reply to the selected message.",
			"constraints": []any{}, "facts": []any{}, "assets": []any{}, "emoji": []any{}, "actions": []any{}}
	case offered("messages__send"):
		stage = 2
		if len(user.Plan.TargetMessageIDs) == 1 {
			target = user.Plan.TargetMessageIDs[0]
		}
		args = map[string]any{"operationId": "reply-" + target, "segments": []any{map[string]any{"replyToId": target,
			"parts": []any{map[string]any{"type": "text", "text": "Synthetic reply " + target}}}}}
	}
	group := laneGroup[target]
	if group == "" || !strings.Contains(wire, "Lane "+group+" source "+target) ||
		(group == "911" && strings.Contains(wire, "Lane 912")) ||
		(group == "912" && strings.Contains(wire, "Lane 911")) {
		http.Error(w, "wrong scene context or unknown stage", 400)
		return
	}
	p.mu.Lock()
	counts := p.calls[target]
	counts[stage]++
	p.calls[target] = counts
	p.mu.Unlock()
	if target == "201" && stage == 1 && counts[stage] == 1 {
		close(p.held)
		select {
		case <-p.release:
		case <-r.Context().Done():
			return
		}
	}
	name := [...]string{"participation__decide", "reply__plan", "messages__send"}[stage]
	raw, _ := json.Marshal(args)
	w.Header().Set("Content-Type", "application/json")
	fmt.Fprintf(w, `{"choices":[{"finish_reason":"tool_calls","message":{"role":"assistant","tool_calls":[{"id":%q,"type":"function","function":{"name":%q,"arguments":%q}}]}}]}`, name, name, raw)
}

func snowLaneScene(group string, encode func(string) string) string {
	return "ob:s:" + encode(fmt.Sprintf(`["snowluma","42","group",%q]`, group))
}

func snowLaneMessage(scene, id string, encode func(string) string) string {
	return scene + ":m:" + encode(fmt.Sprintf("%q", id))
}

func (p *snowLaneWitness) run(t *testing.T, addr, token string, post func(string, string, int), encode func(string) string) {
	t.Helper()
	model := httptest.NewServer(http.HandlerFunc(p.model))
	defer model.Close()
	var release sync.Once
	defer release.Do(func() { close(p.release) })
	request := func(method, path string, value any) []byte {
		t.Helper()
		var raw []byte
		if value != nil {
			var err error
			raw, err = json.Marshal(value)
			if err != nil {
				t.Fatal(err)
			}
		}
		return commandRequest(t, addr, method, path, raw, 200)
	}
	var settings map[string]any
	if err := json.Unmarshal(request("GET", "/api/settings", nil), &settings); err != nil {
		t.Fatal(err)
	}
	original := maps.Clone(settings)
	settings["debounceMs"], settings["maxWaitMs"] = 75, 1000
	settings["modelConcurrency"], settings["sceneTurnConcurrency"] = 2, 1
	settings["topicAdmissionEnabled"], settings["cacheEnabled"] = false, true
	request("PUT", "/api/settings", settings)
	defer request("PUT", "/api/settings", original)
	request("PUT", "/api/models", map[string]any{
		"providers": []any{map[string]any{"id": "lane", "name": "Synthetic lanes", "protocol": "openai-chat", "baseUrl": model.URL + "/v1", "retryCount": 0, "timeoutMs": 20000}},
		"models":    []any{map[string]any{"id": "lane", "displayName": "Synthetic lanes", "providerId": "lane", "apiModelName": "lane", "capabilities": []string{"text", "tools"}}},
		"routes":    []any{map[string]any{"role": "planner", "orderedModelIds": []string{"lane"}, "timeoutMs": 20000}, map[string]any{"role": "replyer", "orderedModelIds": []string{"lane"}, "timeoutMs": 20000}},
	})
	scene := func(group string) string { return snowLaneScene(group, encode) }
	path := func(group string) string { return "/api/scenes/" + url.PathEscape(scene(group)) }
	messageID := func(group, id string) string { return snowLaneMessage(scene(group), id, encode) }
	for _, group := range []string{"911", "912"} {
		request("PUT", path(group), map[string]any{"id": scene(group), "adapterId": "snowluma", "accountId": "42", "platformSceneId": group,
			"kind": "group", "name": "Synthetic lane " + group, "participationEnabled": true, "learningEnabled": false, "proactiveEnabled": false})
	}
	event := func(id string) {
		group := laneGroup[id]
		post(fmt.Sprintf(`{"post_type":"message","message_type":"group","self_id":42,"group_id":%s,"user_id":7,"message_id":%s,"time":%d,"message":[{"type":"text","data":{"text":"Lane %s source %s"}}]}`,
			group, id, time.Now().Unix(), group, id), token, 204)
	}
	type historyRow struct {
		ID, SceneID, SenderID, Origin, Status, ReplyToID string
		Parts                                            []struct{ Type, Text string }
	}
	history := func(group string) map[string]historyRow {
		var body struct{ Items []historyRow }
		if err := json.Unmarshal(request("GET", path(group)+"/history?source=local", nil), &body); err != nil {
			t.Fatal(err)
		}
		rows := make(map[string]historyRow, len(body.Items))
		for _, row := range body.Items {
			if row.SceneID != scene(group) || rows[row.ID].ID != "" {
				t.Fatal("foreign or duplicate source", row)
			}
			rows[row.ID] = row
		}
		return rows
	}
	wait := func(group string, count int) map[string]historyRow {
		t.Helper()
		for until := time.Now().Add(6 * time.Second); ; time.Sleep(20 * time.Millisecond) {
			if rows := history(group); len(rows) == count {
				return rows
			} else if time.Now().After(until) {
				t.Fatalf("group %s did not retain %d rows: %+v", group, count, rows)
			}
		}
	}
	event("201")
	select {
	case <-p.held:
	case <-time.After(6 * time.Second):
		t.Fatal("first group did not reach held Planner response")
	}
	event("202")
	event("301")
	wait("912", 2) // Retained ACK, not merely an attempted peer write.
	wait("911", 2) // First and second inputs, no admitted send yet.
	p.mu.Lock()
	orderAtHold, secondCalls := slices.Clone(p.order), p.calls["202"]
	p.mu.Unlock()
	if !slices.Equal(orderAtHold, []string{"301"}) || secondCalls != [3]int{} {
		t.Fatalf("independent group did not finish first or same-topic work overtook: %v / %v", orderAtHold, secondCalls)
	}
	release.Do(func() { close(p.release) })
	a, b := wait("911", 4), history("912")
	for id, group := range laneGroup {
		rows := a
		if group == "912" {
			rows = b
		}
		source, reply := rows[messageID(group, id)], rows[messageID(group, "8"+id)]
		if source.Origin != "history" || source.Status != "available" ||
			source.SenderID != "ob:u:"+encode(`["snowluma","42","7"]`) || len(source.Parts) != 1 ||
			source.Parts[0].Text != "Lane "+group+" source "+id ||
			reply.SenderID != "bot" || reply.ReplyToID != source.ID || reply.Status != "available" || len(reply.Parts) != 1 ||
			reply.Parts[0].Text != "Synthetic reply "+id {
			t.Fatalf("wrong durable lane source and ACK: %s / %+v / %+v", id, source, reply)
		}
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if !slices.Equal(p.order, []string{"301", "201", "202"}) || len(p.calls) != 3 {
		t.Fatalf("unexpected send order or coalescing: %v / %v", p.order, p.calls)
	}
	for id, counts := range p.calls {
		if counts != [3]int{1, 1, 1} {
			t.Fatalf("turn %s did not run once at every stage: %v", id, counts)
		}
	}
}
