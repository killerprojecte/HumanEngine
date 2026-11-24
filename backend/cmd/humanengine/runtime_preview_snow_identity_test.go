//go:build linux && sqlite_fts5 && humanengine_icu && bundled_wasm

package main

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha1"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/killerprojecte/HumanEngine/backend/internal/recovery"
	"github.com/killerprojecte/HumanEngine/backend/internal/storage"
)

func TestRuntimePreviewSnowLumaTransportIdentityAcrossRestart(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 6*time.Minute)
	defer cancel()
	const actionToken = "synthetic-identity-action"
	const eventToken = "synthetic-identity-event"
	const group = 900
	const otherGroup = 901
	const original = 101
	const quoted = 102
	now := time.Now().Unix()
	message := func(scene, id int) map[string]any {
		parts := []any{}
		if id == quoted {
			parts = append(parts, map[string]any{"type": "reply", "data": map[string]string{"id": "101"}})
		}
		parts = append(parts, map[string]any{"type": "text", "data": map[string]string{"text": fmt.Sprintf("Synthetic %d/%d", scene, id)}})
		return map[string]any{"post_type": "message", "message_type": "group", "sub_type": "normal", "self_id": 42,
			"group_id": scene, "user_id": 7, "message_id": id, "time": now, "message": parts}
	}
	type peerRequest struct {
		Action string
		Echo   json.RawMessage
		Params struct {
			GroupID json.Number `json:"group_id"`
		}
	}
	answer := func(req peerRequest) (map[string]any, bool) {
		switch req.Action {
		case "get_login_info":
			return map[string]any{"user_id": 42}, true
		case "get_group_info":
			return map[string]any{"group_id": req.Params.GroupID, "group_name": "Synthetic group"}, true
		case "get_group_msg_history":
			switch req.Params.GroupID.String() {
			case "900":
				return map[string]any{"messages": []any{message(group, original), message(group, quoted)}}, true
			case "901":
				return map[string]any{"messages": []any{message(otherGroup, original)}}, true
			}
		}
		return nil, false
	}
	connect := make(chan *websocket.Conn, 1)
	peer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+actionToken {
			http.Error(w, "missing fixture token", http.StatusUnauthorized)
			return
		}
		if r.URL.Path != "/snow" {
			req := peerRequest{Action: strings.TrimPrefix(r.URL.Path, "/")}
			if r.Method != http.MethodPost || json.NewDecoder(r.Body).Decode(&req.Params) != nil {
				http.Error(w, "invalid HTTP action", http.StatusBadRequest)
				return
			}
			data, ok := answer(req)
			if !ok {
				http.Error(w, "unexpected HTTP action", http.StatusBadRequest)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"status": "ok", "retcode": 0, "data": data})
			return
		}
		socket, err := websocket.Accept(w, r, nil)
		if err != nil {
			t.Error(err)
			return
		}
		defer socket.CloseNow()
		select {
		case connect <- socket:
		case <-ctx.Done():
			return
		}
		for {
			_, raw, err := socket.Read(ctx)
			if err != nil {
				return
			}
			var req peerRequest
			if err := json.Unmarshal(raw, &req); err != nil {
				t.Error(err)
				return
			}
			data, ok := answer(req)
			ack := map[string]any{"echo": req.Echo, "status": "ok", "retcode": 0, "data": data}
			if !ok {
				ack["status"], ack["retcode"] = "failed", 100
			}
			wire, _ := json.Marshal(ack)
			if err := socket.Write(ctx, websocket.MessageText, wire); err != nil {
				return
			}
		}
	}))
	defer func() { cancel(); peer.Close() }()
	reserved, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	eventAddress := reserved.Addr().String()
	eventPort := reserved.Addr().(*net.TCPAddr).Port
	if err := reserved.Close(); err != nil {
		t.Fatal(err)
	}

	args := launcherArgs(t)
	arg := func(key string) string {
		t.Helper()
		for i := range args {
			if args[i] == key {
				return args[i+1]
			}
		}
		t.Fatalf("missing launcher argument %s", key)
		return ""
	}
	database, authority := arg("--database"), arg("--authority")
	if _, err := recovery.AuthorityPath(database, authority); err != nil {
		t.Fatal(err)
	}
	var address string
	boot := func() func() {
		t.Helper()
		cmd, logs, done := runtimeCommandWithPlugin(t, args, "", os.Getenv("HE_RUNTIME_PREVIEW_BINARY"))
		// A persisted enabled adapter compiles before listening, also under -race.
		address = runtimeAddressFor(t, logs, done, 2*time.Minute)
		return func() {
			t.Helper()
			if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
				t.Fatal(err)
			}
			waitCommandExit(t, logs, done)
		}
	}
	request := func(method, path string, body []byte) []byte {
		t.Helper()
		// Cold configuration/enable compiles the pinned guest, also under -race.
		call, stop := context.WithTimeout(ctx, time.Minute)
		defer stop()
		return commandRequestContext(t, call, address, method, path, body, 200)
	}
	put := func(path string, value any) {
		t.Helper()
		raw, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		request(http.MethodPut, path, raw)
	}
	const configPath = "/api/plugins/snowluma/config"
	const enabledPath = "/api/plugins/snowluma/enabled"
	enable := func(enabled bool) { t.Helper(); put(enabledPath, map[string]any{"enabled": enabled}) }
	waitSocket := func() *websocket.Conn {
		t.Helper()
		select {
		case socket := <-connect:
			return socket
		case <-ctx.Done():
			t.Fatal("SnowLuma did not connect via WebSocket")
			return nil
		}
	}
	httpEvent := func(value any) {
		t.Helper()
		raw, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://"+eventAddress+"/onebot/events", bytes.NewReader(raw))
		if err != nil {
			t.Fatal(err)
		}
		mac := hmac.New(sha1.New, []byte(eventToken))
		_, _ = mac.Write(raw)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Self-ID", "42")
		req.Header.Set("X-Signature", fmt.Sprintf("sha1=%x", mac.Sum(nil)))
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusNoContent {
			t.Fatalf("event status %d, want 204", resp.StatusCode)
		}
	}
	wsEvent := func(socket *websocket.Conn, value any) {
		t.Helper()
		raw, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		if err := socket.Write(ctx, websocket.MessageText, raw); err != nil {
			t.Fatal(err)
		}
	}
	encoded := base64.RawURLEncoding.EncodeToString
	canonical := func(g int) string { return "ob:s:" + encoded([]byte(fmt.Sprintf(`["snowluma","42","group","%d"]`, g))) }
	scene, other := canonical(group), canonical(otherGroup)
	id := func(s string, m int) string { return s + ":m:" + encoded([]byte(fmt.Sprintf(`"%d"`, m))) }
	originalID, quoteID, otherID := id(scene, original), id(scene, quoted), id(other, original)
	senderID := "ob:u:" + encoded([]byte(`["snowluma","42","7"]`))
	type row struct{ ID, SceneID, SenderID, ReplyToID, Status string }
	sceneOriginal := row{originalID, scene, senderID, "", "available"}
	sceneQuote := row{quoteID, scene, senderID, originalID, "available"}
	independentOriginal := row{otherID, other, senderID, "", "available"}
	read := func(sceneID, source string) []row {
		t.Helper()
		body := request(http.MethodGet, "/api/scenes/"+sceneID+"/history?source="+source, nil)
		var history struct{ Items []row }
		if err := json.Unmarshal(body, &history); err != nil {
			t.Fatalf("history decode: %v", err)
		}
		return history.Items
	}
	waitHistory := func(sceneID, source string, match func([]row) bool) {
		t.Helper()
		deadline := time.NewTimer(10 * time.Second)
		defer deadline.Stop()
		ticker := time.NewTicker(20 * time.Millisecond)
		defer ticker.Stop()
		for {
			rows := read(sceneID, source)
			if match(rows) {
				return
			}
			select {
			case <-deadline.C:
				t.Fatalf("%s history %s did not reach expected state: %+v", source, sceneID, rows)
			case <-ticker.C:
			}
		}
	}
	exact := func(sceneID string, want ...row) {
		t.Helper()
		got := read(sceneID, "remote")
		if len(got) != len(want) {
			t.Fatalf("remote history %s got %+v, want %+v", sceneID, got, want)
		}
		for _, item := range want {
			if !slices.Contains(got, item) {
				t.Fatalf("remote history %s missing %+v: %+v", sceneID, item, got)
			}
		}
	}
	stop := boot()
	var settings map[string]any
	if err := json.Unmarshal(request(http.MethodGet, "/api/settings", nil), &settings); err != nil {
		t.Fatal(err)
	}
	settings["sourceEventRevisionsEnabled"] = true
	put("/api/settings", settings)
	put(configPath, map[string]any{"transport": "http", "url": peer.URL, "accountId": "42", "token": actionToken,
		"eventToken": eventToken, "eventHost": "127.0.0.1", "eventPort": eventPort, "eventPath": "/onebot/events"})
	enable(true)
	httpEvent(message(group, original))
	httpEvent(message(otherGroup, original))
	waitHistory(scene, "local", func(rows []row) bool { return slices.Contains(rows, sceneOriginal) })
	waitHistory(other, "local", func(rows []row) bool { return slices.Contains(rows, independentOriginal) })

	enable(false)
	put(configPath, map[string]any{"transport": "websocket", "url": "ws" + strings.TrimPrefix(peer.URL, "http") + "/snow", "accountId": "42", "token": actionToken})
	stop()
	stop = boot()
	enable(true)
	socket := waitSocket()
	exact(scene, sceneOriginal, sceneQuote)
	wsEvent(socket, message(group, quoted))
	waitHistory(scene, "local", func(rows []row) bool { return slices.Contains(rows, sceneQuote) })
	wsEvent(socket, map[string]any{"post_type": "notice", "notice_type": "group_recall", "self_id": 42, "group_id": group,
		"user_id": 7, "operator_id": 7, "message_id": original, "time": now})
	// The HTTP original was never replayed into local memory on restart.
	// Wait for the real remote fetch to suppress it despite the peer returning it.
	waitHistory(scene, "remote", func(rows []row) bool {
		return len(rows) == 1 && rows[0] == sceneQuote
	})
	exact(other, independentOriginal)
	stop()

	stop = boot()
	_ = waitSocket()
	// The cold local transcript is empty. Remote rows retain their canonical
	// identities and respect the durable, scene-scoped recall.
	for _, sceneID := range []string{scene, other} {
		if rows := read(sceneID, "local"); len(rows) != 0 {
			t.Fatalf("cold local history replayed rows for %s: %+v", sceneID, rows)
		}
	}
	exact(scene, sceneQuote)
	exact(other, independentOriginal)
	stop()

	db, err := storage.OpenExisting(database, false)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, check := range []struct {
		table, sceneID, messageID string
		want                      int
	}{
		{"recall_tombstones", scene, originalID, 1},
		{"source_event_tombstones", scene, originalID, 1},
		{"source_event_heads", scene, originalID, 0},
		{"source_event_heads", scene, quoteID, 1},
		{"recall_tombstones", other, otherID, 0},
		{"source_event_tombstones", other, otherID, 0},
		{"source_event_heads", other, otherID, 1},
	} {
		var got int
		query := "SELECT count(*) FROM " + check.table + " WHERE scene_id=? AND message_id=?"
		if err := db.QueryRowContext(ctx, query, check.sceneID, check.messageID).Scan(&got); err != nil || got != check.want {
			t.Fatalf("persisted %s for %s/%s: %d, want %d: %v", check.table, check.sceneID, check.messageID, got, check.want, err)
		}
	}
	for _, want := range []row{sceneQuote, independentOriginal} {
		var got row
		query := `SELECT json_extract(value, '$.metadata.identity.messageId'),
		                 json_extract(value, '$.metadata.identity.sceneId'),
		                 json_extract(value, '$.metadata.identity.senderId')
		          FROM source_event_heads WHERE scene_id=? AND message_id=?`
		if err := db.QueryRowContext(ctx, query, want.SceneID, want.ID).Scan(&got.ID, &got.SceneID, &got.SenderID); err != nil {
			t.Fatal(err)
		}
		if got.ID != want.ID || got.SceneID != want.SceneID || got.SenderID != want.SenderID {
			t.Fatalf("persisted source identity %+v does not match %s/%s/%s", got, want.SceneID, want.ID, want.SenderID)
		}
	}
}
