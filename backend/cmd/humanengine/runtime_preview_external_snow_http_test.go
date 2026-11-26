//go:build linux && sqlite_fts5 && humanengine_icu

package main

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha1"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/png"
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
	var logins, imageLookups, unexpectedDownloads atomic.Int32
	pixels := image.NewNRGBA(image.Rect(0, 0, 3, 2))
	pixels.SetNRGBA(1, 0, color.NRGBA{R: 13, G: 95, B: 200, A: 255})
	var original bytes.Buffer
	if err := png.Encode(&original, pixels); err != nil {
		t.Fatal(err)
	}
	replies := make(chan struct{}, 2)
	peer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/must-not-fetch" {
			unexpectedDownloads.Add(1)
			w.WriteHeader(400)
			return
		}
		if r.Method != http.MethodPost || r.Header.Get("Authorization") != "Bearer "+token {
			t.Error("unauthorized synthetic peer request")
			w.WriteHeader(401)
			return
		}
		switch r.URL.Path {
		case "/get_login_info":
			logins.Add(1)
			fmt.Fprint(w, `{"status":"ok","retcode":0,"data":{"user_id":42}}`)
		case "/get_image":
			var request struct{ File string }
			if json.NewDecoder(r.Body).Decode(&request) != nil || (request.File != "fixture-image-token" && request.File != "fixture-url-token") {
				t.Error("unexpected physical image token")
				w.WriteHeader(400)
				return
			}
			imageLookups.Add(1)
			if request.File == "fixture-url-token" {
				fmt.Fprintf(w, `{"status":"ok","retcode":0,"data":{"url":%q}}`, "http://"+r.Host+"/must-not-fetch")
				return
			}
			fmt.Fprintf(w, `{"status":"ok","retcode":0,"data":{"file":%q}}`, "base64://"+base64.StdEncoding.EncodeToString(original.Bytes()))
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
				(r.URL.Path == "/send_private_msg" && action.User != "901") || len(action.Message) < 2 ||
				action.Message[0].Type != "reply" || action.Message[0].Data["id"] != "9" ||
				action.Message[1].Type != "text" || action.Message[1].Data["text"] != "Independent reply." {
				t.Errorf("invalid physical request: %s %+v", r.URL.Path, action)
				w.WriteHeader(400)
				return
			}
			if r.URL.Path == "/send_group_msg" {
				if len(action.Message) != 3 || action.Message[2].Type != "image" ||
					action.Message[2].Data["file"] != "base64://"+base64.StdEncoding.EncodeToString(original.Bytes()) {
					t.Error("physical group outbound image is not the exact imported original")
					w.WriteHeader(400)
					return
				}
			} else if len(action.Message) != 2 {
				t.Error("private reply should contain only quote and text")
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
	var runtimeLogs *commandLog
	boot := func(entry string) (string, func()) {
		t.Helper()
		proc, logs, done := runtimeCommandWithPlugin(t, args, entry, binary)
		runtimeLogs = logs
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
		if !want || len(rows) != 1 || rows[0].ID != "snowluma" || rows[0].Version != "0.4.0-http-preview" || rows[0].Builtin || rows[0].Enabled != enabled {
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
	mediaScene := "ob:s:" + encode(`["snowluma","42","group","902"]`)
	mediaID := mediaScene + ":m:" + encode(`"11"`)
	mediaRaw := fmt.Sprintf(`{"post_type":"message","message_type":"group","self_id":42,"group_id":902,"user_id":900,"message_id":11,"time":%d,"message":[{"type":"text","data":{"text":"Independent caption"}},{"type":"image","data":{"file":"fixture-image-token"}}]}`, time.Now().Unix())
	post(mediaRaw, eventToken, 204)
	post(mediaRaw, eventToken, 204)
	mediaPath := "/api/scenes/" + url.PathEscape(mediaScene)
	var mediaHistory struct {
		Items []struct {
			ID    string
			Parts []struct {
				Type, Text, MediaURL string
			}
		}
	}
	mediaRows := commandRequest(t, addr, "GET", mediaPath+"/history?source=local", nil, 200)
	if err := json.Unmarshal(mediaRows, &mediaHistory); err != nil {
		t.Fatal(err)
	}
	items := mediaHistory.Items
	if len(items) != 1 || items[0].ID != mediaID || len(items[0].Parts) != 2 ||
		items[0].Parts[0].Type != "text" || items[0].Parts[0].Text != "Independent caption" ||
		items[0].Parts[1].Type != "media" || items[0].Parts[1].MediaURL == "" ||
		bytes.Contains(mediaRows, []byte(`"assetId"`)) {
		t.Fatalf("caption and opaque image must form one ordered row: %s", mediaRows)
	}
	if got := commandRequest(t, addr, "GET", items[0].Parts[1].MediaURL, nil, 200); !bytes.Equal(got, original.Bytes()) {
		t.Fatal("opaque image did not return original bytes")
	}
	if got := imageLookups.Load(); got != 1 {
		t.Fatalf("opaque image lookups = %d, want 1", got)
	}
	mediaURL := func(rawID string) string {
		return mediaPath + "/messages/" + url.PathEscape(mediaScene+":m:"+encode(rawID)) + "/media/1"
	}
	post(strings.ReplaceAll(strings.Replace(mediaRaw, `"message_id":11`, `"message_id":12`, 1),
		"fixture-image-token", "base64://"+base64.StdEncoding.EncodeToString(original.Bytes())), eventToken, 204)
	if got := commandRequest(t, addr, "GET", mediaURL(`"12"`), nil, 200); !bytes.Equal(got, original.Bytes()) {
		t.Fatal("inline image did not return original bytes")
	}
	post(strings.ReplaceAll(strings.Replace(mediaRaw, `"message_id":11`, `"message_id":13`, 1),
		"fixture-image-token", "fixture-url-token"), eventToken, 204)
	commandRequest(t, addr, "GET", mediaURL(`"13"`), nil, 404)
	post(fmt.Sprintf(`{"post_type":"notice","notice_type":"group_recall","self_id":42,"group_id":902,"message_id":11,"time":%d}`, time.Now().Unix()), eventToken, 204)
	commandRequest(t, addr, "GET", mediaURL(`"11"`), nil, 404)
	if got := imageLookups.Load(); got != 2 || unexpectedDownloads.Load() != 0 {
		t.Fatalf("image transport escaped token custody: lookups=%d downloads=%d", got, unexpectedDownloads.Load())
	}

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
	outboundGroup := "ob:s:" + encode(`["snowluma","42","group","901"]`)
	sourceSetup := fmt.Sprintf(`{"id":%q,"adapterId":"snowluma","accountId":"42","platformSceneId":"901","kind":"group","name":"Outbound media source","participationEnabled":false,"learningEnabled":true,"proactiveEnabled":false}`, outboundGroup)
	commandRequest(t, addr, "PUT", "/api/scenes/"+url.PathEscape(outboundGroup), []byte(sourceSetup), 200)
	sourceID := outboundGroup + ":m:" + encode(`"8"`)
	post(fmt.Sprintf(`{"post_type":"message","message_type":"group","self_id":42,"group_id":901,"user_id":901,"message_id":8,"time":%d,"message":[{"type":"text","data":{"text":"outbound asset source"}}]}`, time.Now().Unix()-60), eventToken, 204)
	imported := commandRequest(t, addr, "POST", "/api/memes/import", []byte(fmt.Sprintf(`{"sceneId":%q,"messageId":%q,"base64":%q}`, outboundGroup, sourceID, base64.StdEncoding.EncodeToString(original.Bytes()))), 200)
	var asset struct {
		ID   string
		Tags map[string]any
	}
	if err := json.Unmarshal(imported, &asset); err != nil || asset.ID == "" {
		t.Fatalf("managed asset import failed: %s", imported)
	}
	asset.Tags["intent"] = []string{"celebrate"}
	patch, err := json.Marshal(map[string]any{"available": true, "tags": asset.Tags})
	if err != nil {
		t.Fatal(err)
	}
	commandRequest(t, addr, "PATCH", "/api/memes/"+asset.ID, patch, 200)
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
			t.Fatalf("no physical %s reply: %s", kind, strings.NewReplacer(token, "[test credential]", eventToken, "[test credential]").Replace(runtimeLogs.snapshot()))
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
		SceneKind    string
		Messages     []struct{ ID string }
		PlanContract struct{ TargetMessageIDs []string }
		Plan         struct {
			TargetMessageIDs []string
			Assets           []struct{ AssetID string }
		}
	}
	var receipt struct {
		ID    string
		Value struct{ Items []struct{ ID string } }
	}
	for _, row := range req.Messages {
		if row.Role == "tool" {
			_ = json.Unmarshal([]byte(row.Content), &receipt)
		}
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
	case slices.Contains(names, "participation__decide") && len(in.Messages) != 0:
		name = "participation__decide"
		input = map[string]any{"action": "reply", "reasonCode": "fixture", "targetMessageIds": []string{in.Messages[len(in.Messages)-1].ID}}
	case slices.Contains(names, "reply__plan") && len(in.PlanContract.TargetMessageIDs) == 1:
		var chosen = []any{}
		if in.SceneKind == "group" {
			if receipt.ID == "" {
				name = "memes__search"
				input = map[string]any{"query": "celebrate"}
				break
			}
			if len(receipt.Value.Items) != 1 || receipt.Value.Items[0].ID == "" {
				http.Error(w, "expected synthetic sticker evidence", 400)
				return
			}
			chosen = append(chosen, map[string]string{"assetId": receipt.Value.Items[0].ID, "evidenceId": receipt.ID})
		}
		name = "reply__plan"
		input = map[string]any{"action": "reply", "deliveryTool": "messages.send", "strategy": "Return the fixture reply.", "targetMessageIds": in.PlanContract.TargetMessageIDs, "contextMessageIds": in.PlanContract.TargetMessageIDs, "constraints": []any{}, "facts": []any{}, "assets": chosen, "emoji": []any{}, "actions": []any{}}
	case slices.Contains(names, "messages__send") && len(in.Plan.TargetMessageIDs) == 1:
		name = "messages__send"
		parts := []any{map[string]any{"type": "text", "text": "Independent reply."}}
		if len(in.Plan.Assets) == 1 {
			parts = append(parts, map[string]any{"type": "media", "mediaType": "image", "assetId": in.Plan.Assets[0].AssetID})
		}
		input = map[string]any{"operationId": "portable-reply", "segments": []any{map[string]any{"replyToId": in.Plan.TargetMessageIDs[0], "parts": parts}}}
	default:
		w.WriteHeader(400)
		return
	}
	raw, _ := json.Marshal(input)
	w.Header().Set("Content-Type", "application/json")
	fmt.Fprintf(w, `{"choices":[{"finish_reason":"tool_calls","message":{"role":"assistant","tool_calls":[{"id":%q,"type":"function","function":{"name":%q,"arguments":%q}}]}}]}`, name, name, raw)
}
