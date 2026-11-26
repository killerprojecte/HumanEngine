//go:build linux && sqlite_fts5 && humanengine_icu

package main

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha1"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/killerprojecte/HumanEngine/backend/internal/storage"
)

func TestRuntimePreviewExternalSnowHTTP(t *testing.T) {
	args := launcherArgs(t)
	var root, database string
	for i, arg := range args {
		switch arg {
		case "--plugin-root":
			root = args[i+1]
		case "--database":
			database = args[i+1]
		}
	}
	binary := filepath.Join(t.TempDir(), "humanengine")
	run := func(dir string, v ...string) {
		t.Helper()
		cmd := exec.CommandContext(t.Context(), v[0], v[1:]...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%v: %v\n%s", v, err, out)
		}
	}
	run(".", "go", "build", "-tags=sqlite_fts5,humanengine_icu", "-o", binary, ".")
	const token, eventToken = "synthetic-portable-action", "synthetic-portable-event"
	var logins atomic.Int32
	replies := make(chan struct{}, 2)
	peer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.Header.Get("Authorization") != "Bearer "+token {
			t.Error("unauthorized synthetic peer request")
			w.WriteHeader(401)
			return
		}
		switch r.URL.Path {
		case "/get_login_info":
			logins.Add(1)
			fmt.Fprint(w, `{"status":"ok","retcode":0,"data":{"user_id":42}}`)
		case "/send_group_msg", "/send_private_msg":
			var action struct {
				Group   string `json:"group_id"`
				User    string `json:"user_id"`
				Message []struct {
					Type string
					Data map[string]string
				}
			}
			if json.NewDecoder(r.Body).Decode(&action) != nil || (r.URL.Path == "/send_group_msg" && action.Group != "901") ||
				(r.URL.Path == "/send_private_msg" && action.User != "901") || len(action.Message) != 2 ||
				action.Message[0].Type != "reply" || action.Message[0].Data["id"] != "9" ||
				action.Message[1].Type != "text" || action.Message[1].Data["text"] != "Independent reply." {
				t.Errorf("invalid physical request: %s %+v", r.URL.Path, action)
				w.WriteHeader(400)
				return
			}
			fmt.Fprint(w, `{"status":"ok","retcode":0,"data":{"message_id":10}}`)
			replies <- struct{}{}
		default:
			t.Errorf("unexpected synthetic action: %s", r.URL.Path)
			w.WriteHeader(400)
		}
	}))
	defer peer.Close()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	_ = l.Close()
	node := filepath.Join(root, "node-must-not-run")
	if err := os.WriteFile(node, []byte("#!/bin/sh\nprintf started > \"$0.started\"\nexit 97\n"), 0700); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if _, err := os.Stat(node + ".started"); !os.IsNotExist(err) {
			t.Error("external package unexpectedly started Node")
		}
	}()
	args = append(args, "--node", node)
	boot := func(entry string) (string, func()) {
		t.Helper()
		proc, logs, done := runtimeCommandWithPlugin(t, args, entry, binary)
		return runtimeAddress(t, logs, done), func() {
			t.Helper()
			if err := proc.Process.Signal(syscall.SIGTERM); err != nil {
				t.Fatal(err)
			}
			waitCommandExit(t, logs, done)
			if strings.Contains(logs.snapshot(), token) || strings.Contains(logs.snapshot(), eventToken) {
				t.Fatal("plugin secret leaked")
			}
		}
	}
	catalog := func(address string, want, enabled bool) {
		t.Helper()
		body := commandRequest(t, address, "GET", "/api/plugins", nil, 200)
		var rows []struct {
			ID               string
			Version          string
			Builtin, Enabled bool
		}
		if err := json.Unmarshal(body, &rows); err != nil {
			t.Fatal(err)
		}
		if !want && len(rows) == 0 {
			return
		}
		if !want || len(rows) != 1 || rows[0].ID != "snowluma" || rows[0].Version != "0.2.0-http-preview" || rows[0].Builtin || rows[0].Enabled != enabled {
			t.Fatalf("unexpected catalog: %s", body)
		}
	}
	addr, stop := boot("")
	catalog(addr, false, false)
	stop()
	run("../..", "make", "wasm-snowluma-http-preview", "SNOW_HTTP_PACKAGE_DIR="+root)
	const entry = "snowluma-http.v2.json"
	addr, stop = boot(entry)
	catalog(addr, true, false)
	var settings map[string]json.RawMessage
	if err := json.Unmarshal(commandRequest(t, addr, "GET", "/api/settings", nil, 200), &settings); err != nil {
		t.Fatal(err)
	}
	settings["sourceEventRevisionsEnabled"] = json.RawMessage("true")
	settings["promptContextWindowEnabled"] = json.RawMessage("false")
	settings["debounceMs"] = json.RawMessage("0")
	settings["groupTurnCooldownMs"] = json.RawMessage("0")
	settingsRaw, err := json.Marshal(settings)
	if err != nil {
		t.Fatal(err)
	}
	commandRequest(t, addr, "PUT", "/api/settings", settingsRaw, 200)
	p := []byte(fmt.Sprintf(`{"transport":"http","url":%q,"accountId":"42","eventPort":%d,"token":%q,"eventToken":%q}`, peer.URL, port, token, eventToken))
	public := commandRequest(t, addr, "PUT", "/api/plugins/snowluma/config", p, 200)
	if bytes.Contains(public, []byte(token)) || bytes.Contains(public, []byte(eventToken)) {
		t.Fatal("stored credentials exposed")
	}
	commandRequest(t, addr, "PUT", "/api/plugins/snowluma/enabled", []byte(`{"enabled":true}`), 200)
	catalog(addr, true, true)
	if logins.Load() != 1 {
		t.Fatal("expected one real startup login")
	}
	post := func(raw string, key string, want int) {
		t.Helper()
		mac := hmac.New(sha1.New, []byte(key))
		_, _ = mac.Write([]byte(raw))
		req, err := http.NewRequest("POST", fmt.Sprintf("http://127.0.0.1:%d/onebot/events", port), strings.NewReader(raw))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Self-ID", "42")
		req.Header.Set("X-Signature", fmt.Sprintf("sha1=%x", mac.Sum(nil)))
		client := http.Client{Timeout: 5 * time.Second}
		res, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = res.Body.Close()
		if res.StatusCode != want {
			t.Fatalf("event status %d, want %d", res.StatusCode, want)
		}
	}
	encode := func(s string) string { return base64.RawURLEncoding.EncodeToString([]byte(s)) }
	group := "ob:s:" + encode(`["snowluma","42","group","900"]`)
	direct := "ob:s:" + encode(`["snowluma","42","private","900"]`)
	for _, kind := range []string{"group", "private"} {
		raw := fmt.Sprintf(`{"post_type":"message","message_type":%q,"sub_type":"normal","self_id":42,"group_id":900,"user_id":900,"message_id":7,"time":%d,"message":[{"type":"text","data":{"text":"portable receipt"}}]}`, kind, time.Now().Unix())
		post(raw, "incorrect", 401)
		post(raw, eventToken, 204)
	}
	history := func(scene, status string) {
		t.Helper()
		path := "/api/scenes/" + url.PathEscape(scene) + "/history?source=local"
		body := commandRequest(t, addr, "GET", path, nil, 200)
		var history struct{ Items []struct{ ID, Status string } }
		if err := json.Unmarshal(body, &history); err != nil {
			t.Fatal(err)
		}
		if status == "" && len(history.Items) == 0 {
			return
		}
		if len(history.Items) != 1 || history.Items[0].ID != scene+":m:"+encode(`"7"`) || history.Items[0].Status != status {
			t.Fatalf("unexpected %s history: %s", scene, body)
		}
	}
	history(group, "available")
	history(direct, "available")
	model := httptest.NewServer(http.HandlerFunc(portableSnowHTTPModel))
	defer model.Close()
	models := fmt.Sprintf(`{"providers":[{"id":"fixture","name":"Loopback","protocol":"openai-chat","baseUrl":%q,"retryCount":0,"timeoutMs":4000}],"models":[{"id":"fixture","displayName":"Deterministic","providerId":"fixture","apiModelName":"fixture","capabilities":["text","tools"]}],"routes":[{"role":"planner","orderedModelIds":["fixture"],"timeoutMs":5000},{"role":"replyer","orderedModelIds":["fixture"],"timeoutMs":5000}]}`, model.URL+"/v1")
	commandRequest(t, addr, "PUT", "/api/models", []byte(models), 200)
	for _, kind := range []string{"group", "private"} {
		scene := "ob:s:" + encode(fmt.Sprintf(`["snowluma","42",%q,"901"]`, kind))
		config := fmt.Sprintf(`{"id":%q,"adapterId":"snowluma","accountId":"42","platformSceneId":"901","kind":%q,"name":"Reply witness","participationEnabled":true,"learningEnabled":false,"proactiveEnabled":false}`, scene, kind)
		commandRequest(t, addr, "PUT", "/api/scenes/"+url.PathEscape(scene), []byte(config), 200)
		raw := fmt.Sprintf(`{"post_type":"message","message_type":%q,"sub_type":"normal","self_id":42,"group_id":901,"user_id":901,"message_id":9,"time":%d,"message":[{"type":"text","data":{"text":"reply using installed artifact"}}]}`, kind, time.Now().Unix())
		post(raw, eventToken, 204)
		select {
		case <-replies:
		case <-time.After(10 * time.Second):
			t.Fatalf("no physical %s reply", kind)
		}
		deadline := time.Now().Add(5 * time.Second)
		for {
			body := commandRequest(t, addr, "GET", "/api/scenes/"+url.PathEscape(scene)+"/history?source=local", nil, 200)
			if bytes.Contains(body, []byte(scene+":m:"+encode(`"10"`))) {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("ACK not retained for %s: %s", kind, body)
			}
			time.Sleep(25 * time.Millisecond)
		}
	}
	post(fmt.Sprintf(`{"post_type":"notice","notice_type":"friend_recall","self_id":42,"user_id":900,"message_id":7,"time":%d}`, time.Now().Unix()), eventToken, 204)
	history(group, "available")
	history(direct, "recalled")
	stop()
	addr, stop = boot(entry)
	catalog(addr, true, true)
	if logins.Load() != 2 {
		t.Fatal("existing credential did not reopen actual peer")
	}
	history(group, "")
	history(direct, "")
	stop()
	for _, name := range []string{entry, "snowluma-http.wasm"} {
		if err := os.Remove(filepath.Join(root, name)); err != nil {
			t.Fatal(err)
		}
	}
	addr, stop = boot("")
	catalog(addr, false, false)
	history(group, "")
	stop()
	db, err := storage.OpenExisting(database, true)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, check := range []struct {
		table, scene string
		want         int
	}{
		{"source_event_heads", group, 1}, {"source_event_heads", direct, 0},
		{"recall_tombstones", group, 0}, {"recall_tombstones", direct, 1},
		{"source_event_tombstones", direct, 1},
	} {
		var got int
		err := db.QueryRow("SELECT count(*) FROM "+check.table+" WHERE scene_id=? AND message_id=?", check.scene, check.scene+":m:"+encode(`"7"`)).Scan(&got)
		if err != nil || got != check.want {
			t.Fatalf("persisted %s: got %d want %d (%v)", check.table, got, check.want, err)
		}
	}
}

func portableSnowHTTPModel(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Messages []struct{ Role, Content string }
		Tools    []struct{ Function struct{ Name string } }
	}
	if r.URL.Path != "/v1/chat/completions" || json.NewDecoder(r.Body).Decode(&req) != nil {
		w.WriteHeader(400)
		return
	}
	var in struct {
		Messages     []struct{ ID string }
		PlanContract struct{ TargetMessageIDs []string }
		Plan         struct{ TargetMessageIDs []string }
	}
	for _, row := range req.Messages {
		if row.Role == "user" {
			_ = json.Unmarshal([]byte(row.Content), &in)
		}
	}
	names := make([]string, 0, len(req.Tools))
	for _, tool := range req.Tools {
		names = append(names, tool.Function.Name)
	}
	var name string
	var input map[string]any
	switch {
	case slices.Contains(names, "participation__decide") && len(in.Messages) == 1:
		name = "participation__decide"
		input = map[string]any{"action": "reply", "reasonCode": "fixture", "targetMessageIds": []string{in.Messages[0].ID}}
	case slices.Contains(names, "reply__plan") && len(in.PlanContract.TargetMessageIDs) == 1:
		name = "reply__plan"
		input = map[string]any{"action": "reply", "deliveryTool": "messages.send", "strategy": "Return the fixture reply.", "targetMessageIds": in.PlanContract.TargetMessageIDs, "contextMessageIds": in.PlanContract.TargetMessageIDs, "constraints": []any{}, "facts": []any{}, "assets": []any{}, "emoji": []any{}, "actions": []any{}}
	case slices.Contains(names, "messages__send") && len(in.Plan.TargetMessageIDs) == 1:
		name = "messages__send"
		input = map[string]any{"operationId": "portable-reply", "segments": []any{map[string]any{"replyToId": in.Plan.TargetMessageIDs[0], "parts": []any{map[string]any{"type": "text", "text": "Independent reply."}}}}}
	default:
		w.WriteHeader(400)
		return
	}
	raw, _ := json.Marshal(input)
	w.Header().Set("Content-Type", "application/json")
	fmt.Fprintf(w, `{"choices":[{"finish_reason":"tool_calls","message":{"role":"assistant","tool_calls":[{"id":%q,"type":"function","function":{"name":%q,"arguments":%q}}]}}]}`, name, name, raw)
}
