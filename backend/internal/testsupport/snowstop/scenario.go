// Package snowstop drives synthetic installed SnowLuma acknowledgement fixtures.
package snowstop

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	snow "github.com/killerprojecte/HumanEngine/backend/plugins/snowluma/core"
)

type Process struct {
	Address func() string
	Stop    func() string // Stop and verify the real owner drain, returning logs.
}

type snowSegment struct {
	Type string `json:"type"`
	Data struct {
		ID   json.RawMessage `json:"id"`
		Text string          `json:"text"`
	} `json:"data"`
}
type snowStopAction struct {
	Action string          `json:"action"`
	Echo   json.RawMessage `json:"echo"`
	Params struct {
		GroupID json.RawMessage `json:"group_id"`
		Message []snowSegment   `json:"message"`
	} `json:"params"`
}
type snowStopSend struct {
	Connection, Number int
	Action             snowStopAction
}

func Run(t *testing.T, bind, token string, v1 bool, start func() Process) {
	ctx, cancel := context.WithTimeout(t.Context(), 12*time.Minute)
	defer cancel()
	var mu sync.Mutex
	var acked []any
	var connectionID, sendCount int
	connected := make(chan *websocket.Conn, 8)
	closed := make(chan int, 8)
	transmissions := make(chan snowStopSend, 8)
	mux := http.NewServeMux()
	mux.HandleFunc("/snow", func(w http.ResponseWriter, req *http.Request) {
		if req.Header.Get("Authorization") != "Bearer synthetic-snow-stop" {
			http.Error(w, "missing synthetic credential", http.StatusUnauthorized)
			return
		}
		socket, err := websocket.Accept(w, req, nil)
		if err != nil {
			return
		}
		defer socket.CloseNow()
		mu.Lock()
		connectionID++
		connection := connectionID
		mu.Unlock()
		defer func() { closed <- connection }()
		connected <- socket
		for {
			_, raw, err := socket.Read(ctx)
			if err != nil {
				return
			}
			var action snowStopAction
			if err = json.Unmarshal(raw, &action); err != nil {
				return
			}
			var data any = map[string]any{}
			var remembered any
			switch action.Action {
			case "get_login_info":
				data = map[string]any{"user_id": 42}
			case "get_group_msg_history":
				mu.Lock()
				history := append([]any{}, acked...)
				mu.Unlock()
				data = map[string]any{"messages": history}
			case "send_group_msg":
				mu.Lock()
				sendCount++
				number := sendCount
				mu.Unlock()
				transmissions <- snowStopSend{connection, number, action}
				if number == 2 {
					// Deliberately keep reading. The process, not the peer,
					// must close this socket after the second action is on wire.
					continue
				}
				id := 100 + number
				data = map[string]any{"message_id": id}
				remembered = map[string]any{"message_id": id, "message_type": "group",
					"group_id": 99, "user_id": 42, "time": time.Now().Unix(), "message": action.Params.Message}
			}
			ack, _ := json.Marshal(map[string]any{"echo": action.Echo, "status": "ok", "retcode": 0, "data": data})
			if err = socket.Write(ctx, websocket.MessageText, ack); err != nil {
				return
			}
			if remembered != nil {
				mu.Lock()
				acked = append(acked, remembered)
				mu.Unlock()
			}
		}
	})
	target, partial := "", false
	var controls sync.Mutex
	mux.HandleFunc("/v1/chat/completions", func(w http.ResponseWriter, r *http.Request) {
		controls.Lock()
		text, split := target, partial
		controls.Unlock()
		serveSnowStopModel(t, w, r, text, split)
	})
	listener, err := net.Listen("tcp4", net.JoinHostPort(bind, "0"))
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: mux}
	defer server.Close()
	go func() {
		if err := server.Serve(listener); err != http.ErrServerClosed {
			t.Errorf("fixture server: %v", err)
		}
	}()
	peer := listener.Addr().String()
	protocol, modelURL := "openai-chat", "http://"+peer+"/v1"
	if v1 {
		protocol, modelURL = "snow-model:deterministic", "https://synthetic.invalid"
	}

	process := start()
	address := process.Address()
	client := &http.Client{}
	request := func(method, path string, value any) []byte {
		t.Helper()
		var body io.Reader
		if value != nil {
			wire, err := json.Marshal(value)
			if err != nil {
				t.Fatal(err)
			}
			body = bytes.NewReader(wire)
		}
		timeout := 10 * time.Second
		// Fresh race builds can take longer on the first bundled SnowLuma load.
		if method == http.MethodPut && (path == "/api/plugins/snowluma/config" || path == "/api/plugins/snowluma/enabled") {
			timeout = 4 * time.Minute
		}
		requestCtx, cancel := context.WithTimeout(ctx, timeout)
		defer cancel()
		req, err := http.NewRequestWithContext(requestCtx, method, "http://"+address+path, body)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer "+token)
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("%s %s returned %s", method, path, resp.Status)
		}
		data, err := io.ReadAll(resp.Body)
		if err != nil {
			t.Fatal(err)
		}
		return data
	}
	admin := func(path string, value any) { t.Helper(); request(http.MethodPut, path, value) }
	var settings map[string]any
	if err = json.Unmarshal(request(http.MethodGet, "/api/settings", nil), &settings); err != nil {
		t.Fatal(err)
	}
	settings["sourceEventRevisionsEnabled"] = true
	settings["promptContextWindowEnabled"] = false
	settings["debounceMs"] = 0
	settings["groupTurnCooldownMs"] = 0
	admin("/api/settings", settings)
	if v1 {
		admin("/api/plugins/snow-model/enabled", map[string]any{"enabled": true})
	}
	admin("/api/models", json.RawMessage(fmt.Sprintf(`{"providers":[{"id":"p","name":"Synthetic","protocol":%q,"baseUrl":%q,"retryCount":0,"timeoutMs":10000}],"models":[{"id":"m","displayName":"Synthetic","providerId":"p","apiModelName":"synthetic","capabilities":["text","tools"]}],"routes":[{"role":"planner","orderedModelIds":["m"],"timeoutMs":10000},{"role":"replyer","orderedModelIds":["m"],"timeoutMs":10000}]}`, protocol, modelURL)))
	admin("/api/plugins/snowluma/config", map[string]any{"url": "ws://" + peer + "/snow", "accountId": "42", "token": "synthetic-snow-stop"})
	admin("/api/plugins/snowluma/enabled", map[string]any{"enabled": true})
	waitConnection := func(timeout time.Duration) *websocket.Conn {
		t.Helper()
		select {
		case socket := <-connected:
			return socket
		case <-time.After(timeout):
			t.Fatal("Snow peer did not connect")
			return nil
		}
	}
	socket := waitConnection(30 * time.Second)
	scene := snow.SceneID("snowluma", "42", "group", "99")
	scenePath := "/api/scenes/" + url.PathEscape(scene)
	admin(scenePath, map[string]any{"id": scene, "kind": "group", "adapterId": "snowluma", "accountId": "42", "platformSceneId": "99", "name": "Synthetic Stop Fixture", "participationEnabled": true, "learningEnabled": false, "proactiveEnabled": false})
	ingress := func(id int, split bool) {
		t.Helper()
		text := fmt.Sprintf("Synthetic stop fixture event %d.", id)
		if v1 {
			admin("/api/plugins/snow-model/config", map[string]any{"media": false, "targetText": text, "partial": split})
		} else {
			controls.Lock()
			target, partial = text, split
			controls.Unlock()
		}
		body, _ := json.Marshal(map[string]any{"self_id": 42, "post_type": "message", "message_type": "group", "group_id": 99, "user_id": 7, "message_id": id, "time": time.Now().Unix(), "message": []any{map[string]any{"type": "text", "data": map[string]any{"text": text}}}})
		if err := socket.Write(ctx, websocket.MessageText, body); err != nil {
			t.Fatal(err)
		}
	}
	expectSend := func(connection, number, reply int, text string) {
		t.Helper()
		select {
		case got := <-transmissions:
			p := got.Action.Params
			if got.Connection != connection || got.Number != number || strings.Trim(string(p.GroupID), `"`) != "99" ||
				len(p.Message) != 2 || p.Message[0].Type != "reply" || strings.Trim(string(p.Message[0].Data.ID), `"`) != fmt.Sprint(reply) ||
				p.Message[1].Type != "text" || p.Message[1].Data.Text != text {
				t.Fatalf("unexpected physical action: %+v", got)
			}
		case <-time.After(20 * time.Second):
			t.Fatal("missing Snow action")
		}
	}
	history := func(origin string) map[string]string {
		t.Helper()
		type message struct {
			ID, SenderID string
			Parts        []struct{ Type, Text string }
		}
		var body struct{ Items []message }
		wire := request(http.MethodGet, scenePath+"/history"+origin, nil)
		if err := json.Unmarshal(wire, &body); err != nil {
			t.Fatal(err)
		}
		result := map[string]string{}
		for _, m := range body.Items {
			if m.SenderID != "bot" {
				continue
			}
			if _, seen := result[m.ID]; seen {
				t.Fatalf("duplicate bot history %s", m.ID)
			}
			for _, p := range m.Parts {
				if p.Type == "text" {
					result[m.ID] += p.Text
				}
			}
		}
		return result
	}
	firstID := snow.MessageID(scene, "101")
	firstText := "Synthetic Snow partial segment 1."
	wantFirst := map[string]string{firstID: firstText}
	ingress(1, true)
	expectSend(1, 1, 1, firstText)
	expectSend(1, 2, 1, "Synthetic Snow partial segment 2.")
	if got := history("?source=local"); !reflect.DeepEqual(got, wantFirst) {
		t.Fatalf("live retained receipts before SIGTERM: %v", got)
	}
	var held struct{ ActiveTasks int }
	if err := json.Unmarshal(request(http.MethodGet, scenePath+"/state", nil), &held); err != nil || held.ActiveTasks != 1 {
		t.Fatalf("second physical action was not held in an active turn: %+v, %v", held, err)
	}
	select {
	case c := <-closed:
		t.Fatalf("peer %d closed before SIGTERM", c)
	default:
	}
	logs := process.Stop()
	waitPeerClose := func(want int) {
		t.Helper()
		select {
		case c := <-closed:
			if c != want {
				t.Fatalf("unexpected closed peer %d, want %d", c, want)
			}
		case <-time.After(10 * time.Second):
			t.Fatal("SIGTERM left physical peer open")
		}
	}
	waitPeerClose(1)
	// A separate trace check asserts the old process's classification; remote
	// history below remains authoritative only for ACKed platform messages.
	var terminals int
	wantCounts := map[string]int{"sentSegments": 1, "unknownSegments": 1, "failedSegments": 0, "cancelledSegments": 0}
	for _, line := range strings.Split(logs, "\n") {
		_, flat, _ := strings.Cut(line, " | ")
		var event struct {
			Code, SceneID, Message, FailureCode string
			Counts                              map[string]int
		}
		if json.Unmarshal([]byte(flat), &event) != nil || event.SceneID != scene || !strings.HasPrefix(event.Code, "runtime.turn.") {
			continue
		}
		terminals++
		t.Logf("old-process terminal trace: %s message=%q failureCode=%q counts=%v", event.Code, event.Message, event.FailureCode, event.Counts)
		if event.Code != "runtime.turn.partial" || !reflect.DeepEqual(event.Counts, wantCounts) {
			t.Fatalf("old-process turn did not classify physical send as partial: %+v", event)
		}
	}
	if terminals != 1 {
		t.Fatalf("expected exactly one terminal turn receipt, got %d", terminals)
	}

	process = start()
	// Restored adapter login can compile the bundled guest before HTTP listens.
	socket = waitConnection(4 * time.Minute)
	address = process.Address()
	if got := history("?source=local"); len(got) != 0 {
		t.Fatalf("cold reopen resurrected process-local rows: %v", got)
	}
	if got := history(""); !reflect.DeepEqual(got, wantFirst) {
		t.Fatalf("cold authoritative ACK history: %v", got)
	}
	ingress(2, false)
	const fresh = "Synthetic Snow reply."
	expectSend(2, 3, 2, fresh)
	freshID := snow.MessageID(scene, "103")
	wantBoth := map[string]string{firstID: firstText, freshID: fresh}
	freshDeadline := time.NewTimer(15 * time.Second)
	defer freshDeadline.Stop()
	for {
		local := history("?source=local")
		if reflect.DeepEqual(local, map[string]string{freshID: fresh}) {
			break
		}
		select {
		case <-freshDeadline.C:
			t.Fatalf("fresh ACK never entered local retention: %v", local)
		case <-time.After(20 * time.Millisecond):
		}
	}
	if got := history(""); !reflect.DeepEqual(got, wantBoth) {
		t.Fatalf("cold authoritative history after fresh ACK: %v", got)
	}
	process.Stop()
	waitPeerClose(2)
	mu.Lock()
	defer mu.Unlock()
	if sendCount != 3 || connectionID != 2 || len(transmissions) != 0 || len(acked) != 2 {
		t.Fatalf("replay/third action: sends=%d peers=%d pending=%d ACKs=%d", sendCount, connectionID, len(transmissions), len(acked))
	}
}

func serveSnowStopModel(t *testing.T, w http.ResponseWriter, r *http.Request, target string, split bool) {
	var req struct {
		Model    string
		Messages []struct{ Role, Content string }
		Tools    []struct{ Function struct{ Name string } }
	}
	if r.URL.Path != "/v1/chat/completions" || json.NewDecoder(r.Body).Decode(&req) != nil || req.Model != "synthetic" {
		t.Error("unexpected native fixture request")
		http.Error(w, "unexpected request", 400)
		return
	}
	data := map[string]json.RawMessage{}
	hasTool := false
	for _, m := range req.Messages {
		if m.Role == "tool" {
			hasTool = true
		}
		if m.Role == "user" {
			_ = json.Unmarshal([]byte(m.Content), &data)
		}
	}
	names := make([]string, 0, len(req.Tools))
	for _, tool := range req.Tools {
		names = append(names, tool.Function.Name)
	}
	name := ""
	var args any
	switch {
	case slices.Contains(names, "participation__decide"):
		var rows []struct {
			ID    string
			Parts []struct{ Text string }
		}
		_ = json.Unmarshal(data["messages"], &rows)
		for _, row := range rows {
			if slices.ContainsFunc(row.Parts, func(part struct{ Text string }) bool { return part.Text == target }) {
				name = "participation__decide"
				args = map[string]any{"action": "reply", "reasonCode": "fixture", "targetMessageIds": []string{row.ID}}
				break
			}
		}
	case slices.Contains(names, "reply__plan"):
		var contract map[string]json.RawMessage
		_ = json.Unmarshal(data["planContract"], &contract)
		name = "reply__plan"
		args = map[string]any{"action": "reply", "deliveryTool": "messages.send", "targetMessageIds": contract["targetMessageIds"], "contextMessageIds": contract["contextMessageIds"], "strategy": "Answer the selected synthetic request.", "constraints": []any{}, "facts": []any{}, "assets": []any{}, "emoji": []any{}, "stateEvidenceIds": []any{}, "memorySelections": []any{}, "expressionGoals": []any{}, "actions": []any{}}
	case slices.Contains(names, "messages__send") && !hasTool:
		var plan struct{ TargetMessageIDs []string }
		_ = json.Unmarshal(data["plan"], &plan)
		if len(plan.TargetMessageIDs) != 1 {
			break
		}
		name = "messages__send"
		texts := []string{"Synthetic Snow reply."}
		if split {
			texts = []string{"Synthetic Snow partial segment 1.", "Synthetic Snow partial segment 2.", "Synthetic Snow partial segment 3."}
		}
		segments := make([]any, 0, len(texts))
		for _, text := range texts {
			segments = append(segments, map[string]any{"replyToId": plan.TargetMessageIDs[0], "parts": []any{map[string]any{"type": "text", "text": text}}})
		}
		args = map[string]any{"operationId": "snow-send", "segments": segments}
	case slices.Contains(names, "messages__send") && hasTool:
	default:
		t.Errorf("unexpected native fixture model stage %v", names)
		http.Error(w, "unexpected stage", 400)
		return
	}
	message := map[string]any{"role": "assistant", "content": ""}
	reason := "stop"
	if name != "" {
		raw, err := json.Marshal(args)
		if err != nil {
			t.Error(err)
			http.Error(w, "invalid fixture response", 500)
			return
		}
		reason = "tool_calls"
		message["tool_calls"] = []any{map[string]any{"id": "snow-" + name, "type": "function", "function": map[string]string{"name": name, "arguments": string(raw)}}}
	} else if !hasTool {
		t.Errorf("missing native fixture target in %v", names)
		http.Error(w, "missing fixture target", 400)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": message, "finish_reason": reason}}, "usage": map[string]int{"prompt_tokens": 0, "completion_tokens": 0, "total_tokens": 0}})
}
