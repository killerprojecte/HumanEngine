//go:build linux && sqlite_fts5 && humanengine_icu && bundled_wasm

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/coder/websocket"
	snow "github.com/killerprojecte/HumanEngine/backend/plugins/snowluma/core"
)

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

func TestRuntimePreviewSnowLumaPartialACKSignalDrainRestart(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 12*time.Minute)
	defer cancel()
	var mu sync.Mutex
	var acked []any
	var connectionID, sendCount int
	connected := make(chan *websocket.Conn, 8)
	closed := make(chan int, 8)
	transmissions := make(chan snowStopSend, 8)
	peer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
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
	}))
	defer peer.Close()

	args := launcherArgs(t)
	var pluginRoot string
	for i, arg := range args {
		if arg == "--plugin-root" {
			pluginRoot = args[i+1]
			break
		}
	}
	fixture, err := os.ReadFile("../../internal/app/testdata/snowluma-model.mjs")
	if err != nil {
		t.Fatal(err)
	}
	const plugin = "snow-model.mjs"
	if err = os.WriteFile(filepath.Join(pluginRoot, plugin), fixture, 0600); err != nil {
		t.Fatal(err)
	}
	binary := os.Getenv("HE_RUNTIME_PREVIEW_BINARY")
	cmd, logs, done := runtimeCommandWithPlugin(t, args, plugin, binary)
	address := runtimeAddress(t, logs, done)
	admin := func(path string, value any) {
		t.Helper()
		wire, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPut, "http://"+address+path, strings.NewReader(string(wire)))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer synthetic-launcher-token")
		req.Header.Set("Content-Type", "application/json")
		resp, err := (&http.Client{Timeout: 4 * time.Minute}).Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("PUT %s returned %s", path, resp.Status)
		}
	}
	var settings map[string]any
	if err = json.Unmarshal(commandRequest(t, address, http.MethodGet, "/api/settings", nil, 200), &settings); err != nil {
		t.Fatal(err)
	}
	settings["sourceEventRevisionsEnabled"] = true
	settings["promptContextWindowEnabled"] = false
	settings["debounceMs"] = 0
	settings["groupTurnCooldownMs"] = 0
	admin("/api/settings", settings)
	admin("/api/plugins/snow-model/enabled", map[string]any{"enabled": true})
	admin("/api/models", json.RawMessage(`{"providers":[{"id":"p","name":"Synthetic","protocol":"snow-model:deterministic","baseUrl":"https://synthetic.invalid","retryCount":0,"timeoutMs":10000}],"models":[{"id":"m","displayName":"Synthetic","providerId":"p","apiModelName":"synthetic","capabilities":["text","tools"]}],"routes":[{"role":"planner","orderedModelIds":["m"],"timeoutMs":10000},{"role":"replyer","orderedModelIds":["m"],"timeoutMs":10000}]}`))
	admin("/api/plugins/snowluma/config", map[string]any{"url": "ws" + strings.TrimPrefix(peer.URL, "http"), "accountId": "42", "token": "synthetic-snow-stop"})
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
	ingress := func(id int, partial bool) {
		t.Helper()
		text := fmt.Sprintf("Synthetic stop fixture event %d.", id)
		admin("/api/plugins/snow-model/config", map[string]any{"media": false, "targetText": text, "partial": partial})
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
		wire := commandRequest(t, address, http.MethodGet, scenePath+"/history"+origin, nil, 200)
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
	if err := json.Unmarshal(commandRequest(t, address, http.MethodGet, scenePath+"/state", nil, 200), &held); err != nil || held.ActiveTasks != 1 {
		t.Fatalf("second physical action was not held in an active turn: %+v, %v", held, err)
	}
	select {
	case c := <-closed:
		t.Fatalf("peer %d closed before SIGTERM", c)
	default:
	}
	if err = cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	waitCommandExit(t, logs, done)
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
	for _, line := range strings.Split(logs.snapshot(), "\n") {
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

	cmd, logs, done = runtimeCommandWithPlugin(t, args, plugin, binary)
	socket = waitConnection(4 * time.Minute) // enabled adapter login is restored before CLI ready.
	address = runtimeAddress(t, logs, done)
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
	if err = cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	waitCommandExit(t, logs, done)
	waitPeerClose(2)
	mu.Lock()
	defer mu.Unlock()
	if sendCount != 3 || connectionID != 2 || len(transmissions) != 0 || len(acked) != 2 {
		t.Fatalf("replay/third action: sends=%d peers=%d pending=%d ACKs=%d", sendCount, connectionID, len(transmissions), len(acked))
	}
}
