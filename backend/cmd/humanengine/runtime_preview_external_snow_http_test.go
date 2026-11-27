//go:build linux && sqlite_fts5 && humanengine_icu

package main

import (
	"bytes"
	"context"
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

	"github.com/coder/websocket"
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
	var logins, imageLookups, unexpectedDownloads, messageLookups atomic.Int32
	var recalledPage atomic.Bool
	pixels := image.NewNRGBA(image.Rect(0, 0, 3, 2))
	pixels.SetNRGBA(1, 0, color.NRGBA{R: 13, G: 95, B: 200, A: 255})
	var original bytes.Buffer
	if err := png.Encode(&original, pixels); err != nil {
		t.Fatal(err)
	}
	const groupRemote = `{"message_type":"group","self_id":42,"group_id":900,"user_id":900,"message_id":21,"time":1700000001,"message":[{"type":"text","data":{"text":"Group history witness"}},{"type":"at","data":{"qq":777}},{"type":"at","data":{"qq":"all"}}]}`
	const friendRemote = `{"message_type":"private","self_id":42,"target_id":900,"user_id":42,"message_id":21,"time":1700000002,"message":[{"type":"text","data":{"text":"Private history witness"}},{"type":"image","data":{"file":"fixture-image-token"}}]}`
	const friendWrongScope = `{"message_type":"private","self_id":42,"target_id":900,"user_id":42,"message_id":22,"time":1700000003,"message":[{"type":"image","data":{"file":"fixture-image-token"}}]}`
	const recalledRemote = `{"message_type":"private","self_id":42,"user_id":900,"message_id":7,"time":1700000000,"message":[{"type":"text","data":{"text":"Do not show recalled history"}}]}`
	replies := make(chan struct{}, 2)
	lanes := newSnowLaneWitness()
	responses := new(snowResponsesWitness)
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
		case "/get_group_msg_history", "/get_friend_msg_history":
			var request struct {
				Group   string `json:"group_id"`
				User    string `json:"user_id"`
				Count   int
				Reverse bool `json:"reverse_order"`
			}
			if json.NewDecoder(r.Body).Decode(&request) != nil || request.Count != 30 || !request.Reverse {
				t.Error("invalid history window request")
				w.WriteHeader(400)
				return
			}
			var rows string
			switch {
			case r.URL.Path == "/get_group_msg_history" && request.Group == "900":
				rows = groupRemote + "," + groupRemote
			case r.URL.Path == "/get_friend_msg_history" && request.User == "900":
				rows = recalledRemote
				if !recalledPage.Load() {
					rows = friendWrongScope + "," + friendRemote + "," + rows
				}
			default:
				t.Error("history action was not scoped to the requested peer")
				w.WriteHeader(400)
				return
			}
			fmt.Fprintf(w, `{"status":"ok","retcode":0,"data":{"messages":[%s]}}`, rows)
		case "/get_msg":
			var request struct {
				Message string `json:"message_id"`
			}
			if json.NewDecoder(r.Body).Decode(&request) != nil || (request.Message != "21" && request.Message != "22") {
				t.Error("unexpected get_msg target")
				w.WriteHeader(400)
				return
			}
			messageLookups.Add(1)
			row := friendRemote
			if request.Message == "22" {
				// Global get_msg returns a group event with the requested raw ID.
				row = strings.Replace(groupRemote, `"message_id":21`, `"message_id":22`, 1)
			}
			fmt.Fprintf(w, `{"status":"ok","retcode":0,"data":%s}`, row)
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
				Message []snowLanePart
			}
			if err := json.NewDecoder(r.Body).Decode(&action); err != nil {
				t.Error(err)
				w.WriteHeader(400)
				return
			}
			if r.URL.Path == "/send_group_msg" && (action.Group == "911" || action.Group == "912") {
				lanes.action(w, action.Group, action.Message)
				return
			}
			if r.URL.Path == "/send_group_msg" && action.Group == responsesGroup {
				responses.action(w, action.Message)
				return
			}
			if (r.URL.Path == "/send_group_msg" && action.Group != "901") ||
				(r.URL.Path == "/send_private_msg" && action.User != "901") || len(action.Message) < 2 ||
				action.Message[0].Type != "reply" || action.Message[0].Data["id"] != "9" ||
				action.Message[1].Type != "text" || action.Message[1].Data["text"] != "Independent reply." {
				t.Errorf("invalid physical request: %s %+v", r.URL.Path, action)
				w.WriteHeader(400)
				return
			}
			if r.URL.Path == "/send_group_msg" {
				if len(action.Message) != 4 || action.Message[2].Type != "at" || action.Message[2].Data["qq"] != "901" ||
					action.Message[3].Type != "image" || action.Message[3].Data["file"] != "base64://"+base64.StdEncoding.EncodeToString(original.Bytes()) {
					t.Error("physical group reply did not preserve ordered @ and exact original image")
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
	t.Cleanup(func() {
		if t.Failed() && runtimeLogs != nil {
			logs := strings.NewReplacer(token, "[test credential]", eventToken, "[test credential]",
				"synthetic-launcher-token", "[test credential]").Replace(runtimeLogs.snapshot())
			lines := strings.Split(logs, "\n")
			t.Logf("sanitized launcher diagnostic tail:\n%s", strings.Join(lines[max(0, len(lines)-70):], "\n"))
		}
	})
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
		if !want || len(rows) != 1 || rows[0].ID != "snowluma" || rows[0].Version != "0.6.0-forward-preview" || rows[0].Builtin || rows[0].Enabled != enabled {
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
	const eventPath = "/onebot/custom-events"
	p := []byte(fmt.Sprintf(`{"transport":"http","url":%q,"accountId":"42","eventPort":%d,"eventPath":%q,"token":%q,"eventToken":%q}`, peer.URL, port, eventPath, token, eventToken))
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
		req, err := http.NewRequest("POST", fmt.Sprintf("http://127.0.0.1:%d%s", port, eventPath), strings.NewReader(raw))
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
	// A paired locator still renders as a same-origin attachment, but
	// must not dial private addresses or fall back to a token on a policy denial.
	publicPair := strings.Replace(strings.Replace(mediaRaw, `"message_id":11`, `"message_id":14`, 1),
		`"file":"fixture-image-token"`, fmt.Sprintf(`"url":%q,"file_id":"fixture-image-token"`, peer.URL+"/must-not-fetch"), 1)
	post(publicPair, eventToken, 204)
	mediaRows = commandRequest(t, addr, "GET", mediaPath+"/history?source=local", nil, 200)
	if err := json.Unmarshal(mediaRows, &mediaHistory); err != nil {
		t.Fatal(err)
	}
	pairURL := ""
	for _, row := range mediaHistory.Items {
		if row.ID == mediaScene+":m:"+encode(`"14"`) && len(row.Parts) == 2 {
			pairURL = row.Parts[1].MediaURL
			break
		}
	}
	actual, err := url.PathUnescape(pairURL)
	expected, _ := url.PathUnescape(mediaURL(`"14"`))
	if err != nil || actual != expected || bytes.Contains(mediaRows, []byte(peer.URL)) {
		t.Fatalf("the URL and file ID should project only a same-origin media route: %s", mediaRows)
	}
	commandRequest(t, addr, "GET", pairURL, nil, 503)
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
	lanes.run(t, addr, eventToken, post, encode)
	responses.run(t, addr, eventToken, post, encode)
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
		if kind == "group" {
			raw = snowMentionEvent(raw)
		}
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
	type remoteRow struct {
		ID, SceneID, Origin, SenderID string
		Parts                         []struct{ Type, Text, MediaURL, UserID string }
	}
	type remoteWindow struct {
		Items                []remoteRow
		Coverage, NextCursor string
	}
	remote := func(scene string) remoteWindow {
		t.Helper()
		body := commandRequest(t, addr, "GET", "/api/scenes/"+url.PathEscape(scene)+"/history?source=remote", nil, 200)
		var rows remoteWindow
		if err := json.Unmarshal(body, &rows); err != nil {
			t.Fatal(err)
		}
		return rows
	}
	g := remote(group)
	if len(g.Items) != 1 || g.Items[0].ID != group+":m:"+encode(`"21"`) ||
		g.Items[0].Origin != "history" || len(g.Items[0].Parts) != 3 || g.Items[0].Parts[0].Text != "Group history witness" ||
		g.Items[0].Parts[1].Type != "mention" || g.Items[0].Parts[1].UserID != "ob:u:"+encode(`["snowluma","42","777"]`) ||
		g.Items[0].Parts[2].Type != "mention" || g.Items[0].Parts[2].UserID != "all" || g.Coverage != "partial" {
		t.Fatalf("remote group deduplication and mentions: %+v", g)
	}
	d := remote(direct)
	if len(d.Items) != 2 || d.Items[0].ID != direct+":m:"+encode(`"21"`) || d.Items[1].ID != direct+":m:"+encode(`"22"`) ||
		d.Items[0].SenderID != "bot" || d.Items[0].SceneID != direct || d.Items[0].Parts[0].Text != "Private history witness" ||
		d.NextCursor != direct+":m:"+encode(`"7"`) || d.Items[0].Parts[1].MediaURL == "" || d.Items[1].Parts[0].MediaURL == "" {
		t.Fatalf("remote private ordering, sender scope or recalled cursor: %+v", d)
	}
	if got := commandRequest(t, addr, "GET", d.Items[0].Parts[1].MediaURL, nil, 200); !bytes.Equal(got, original.Bytes()) {
		t.Fatal("scoped get_msg did not recover the exact history image")
	}
	commandRequest(t, addr, "GET", d.Items[1].Parts[0].MediaURL, nil, 404)
	if messageLookups.Load() != 2 || imageLookups.Load() != 3 {
		t.Fatalf("unexpected remote image transport: messages=%d images=%d", messageLookups.Load(), imageLookups.Load())
	}
	recalledPage.Store(true)
	d = remote(direct)
	if len(d.Items) != 0 || d.Coverage != "partial" || d.NextCursor != direct+":m:"+encode(`"7"`) {
		t.Fatalf("recall-only remote window did not retain its cursor: %+v", d)
	}
	history(group, "")
	history(direct, "")
	recalledPage.Store(false)
	saved := portableSnowSocket(t, addr, peer.Config.Handler, token, eventPath, peer.URL, group, direct, original.Bytes(), func() string {
		stop()
		db, err := storage.OpenExisting(database, true)
		if err != nil {
			t.Fatal(err)
		}
		// Recreate the persisted TypeScript shape, keeping the original
		// action credential in the installation's separate secret store.
		_, err = db.Exec("UPDATE config SET value=json_remove(value, '$.transport') WHERE key='plugin:snowluma'")
		closeErr := db.Close()
		if err != nil || closeErr != nil {
			t.Fatalf("seed legacy WebSocket config: update=%v close=%v", err, closeErr)
		}
		addr, stop = boot(entry)
		catalog(addr, true, true)
		return addr
	})
	var returned map[string]json.RawMessage
	if err := json.Unmarshal(saved, &returned); err != nil {
		t.Fatal(err)
	}
	returned["transport"] = json.RawMessage(`"http"`)
	returned["url"], _ = json.Marshal(peer.URL)
	roundTrip, _ := json.Marshal(returned)
	commandRequest(t, addr, "PUT", "/api/plugins/snowluma/config", roundTrip, 200)

	post(snowSocketEvent("group", 907, 78), eventToken, 204)
	snowSocketHistory(t, addr, "group", 907, 78)

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
	for _, scope := range []string{group, direct} {
		var count int
		if err := db.QueryRow("SELECT count(*) FROM source_event_heads WHERE scene_id=? AND message_id IN (?,?)", scope, scope+":m:"+encode(`"21"`), scope+":m:"+encode(`"22"`)).Scan(&count); err != nil || count != 0 {
			t.Fatalf("remote history must not enroll live source rows: count=%d err=%v", count, err)
		}
	}
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
	for id, group := range laneGroup {
		scene := snowLaneScene(group, encode)
		var count int
		if err := db.QueryRow("SELECT count(*) FROM source_event_heads WHERE scene_id=? AND message_id=?", scene,
			snowLaneMessage(scene, id, encode)).Scan(&count); err != nil || count != 1 {
			t.Fatalf("event %s was not persisted in the right scene: count=%d err=%v", id, count, err)
		}
	}
	verifySnowResponsesAttempts(t, db)
	t.Log("standalone-snowluma-cross-group-ack-before-release-same-topic-fifo-three-turns")
	t.Log("installed-snowluma-http-and-websocket-mention-quoted-order-and-history")
}

func snowMentionEvent(event string) string {
	return strings.TrimSuffix(event, "]}") + `,{"type":"at","data":{"qq":777}}]}`
}

func snowSocketEvent(kind string, peer, id int) string {
	return fmt.Sprintf(`{"post_type":"message","message_type":%q,"sub_type":"normal","self_id":42,"group_id":%d,"user_id":%d,"message_id":%d,"time":%d,"message":[{"type":"text","data":{"text":"installed socket event"}}]}`, kind, peer, peer, id, time.Now().Unix())
}

func snowSocketHistory(t *testing.T, addr, kind string, peer, want int, forbid ...int) {
	t.Helper()
	encode := base64.RawURLEncoding.EncodeToString
	scene := "ob:s:" + encode([]byte(fmt.Sprintf(`["snowluma","42",%q,%q]`, kind, fmt.Sprint(peer))))
	key := func(id int) []byte { return []byte(scene + ":m:" + encode([]byte(fmt.Sprintf("%q", fmt.Sprint(id))))) }
	for deadline := time.Now().Add(5 * time.Second); ; {
		data := commandRequest(t, addr, "GET", "/api/scenes/"+url.PathEscape(scene)+"/history?source=local", nil, 200)
		for _, id := range forbid {
			if bytes.Contains(data, key(id)) {
				t.Fatalf("unqualified socket event or stale ACK reached scene: %s", data)
			}
		}
		if bytes.Contains(data, key(want)) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("missing socket receipt %d: %s", want, data)
		}
		time.Sleep(25 * time.Millisecond)
	}
}

func portableSnowSocket(t *testing.T, addr string, httpPeer http.Handler, token, eventPath, httpURL, historyGroup, historyDirect string, original []byte, reopenLegacy func() string) []byte {
	t.Helper()
	var connections, sent atomic.Int32
	sockets := make(chan *websocket.Conn, 3)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+token || r.URL.Path != "/socket" {
			t.Error("missing stored handshake token or wrong configured path")
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		socket, err := websocket.Accept(w, r, nil)
		if err != nil {
			t.Error(err)
			return
		}
		defer socket.CloseNow()
		attempt := int(connections.Add(1))
		for {
			_, raw, err := socket.Read(ctx)
			if err != nil {
				return
			}
			var request struct {
				Action, Echo string
				Params       json.RawMessage
			}
			if err := json.Unmarshal(raw, &request); err != nil || request.Echo == "" {
				t.Errorf("invalid socket envelope: %s", raw)
				return
			}
			var response []byte
			switch request.Action {
			case "get_login_info":
				// A valid independent event arriving before this socket's login
				// response must not reach the runtime.
				if err := socket.Write(ctx, websocket.MessageText, []byte(snowSocketEvent("group", 904, 80+attempt))); err != nil {
					return
				}
				response = []byte(`{"status":"ok","retcode":0,"data":{"user_id":42}}`)
			case "send_group_msg", "send_private_msg":
				var action struct {
					Group   string `json:"group_id"`
					User    string `json:"user_id"`
					Message []struct {
						Type string
						Data map[string]string
					}
				}
				if err := json.Unmarshal(request.Params, &action); err != nil {
					t.Error(err)
					return
				}
				count := sent.Add(1)
				if attempt == 1 {
					if count != 1 || action.User != "901" || len(action.Message) != 2 || action.Message[0].Data["id"] != "24" {
						t.Errorf("wrong request before disconnect: %+v", action)
					}
					// A physical send was accepted but no echo comes back.
					return
				}
				if count != 2 || action.Group != "901" || len(action.Message) != 4 ||
					action.Message[0].Type != "reply" || action.Message[0].Data["id"] != "25" || action.Message[1].Type != "text" ||
					action.Message[2].Type != "at" || action.Message[2].Data["qq"] != "901" ||
					action.Message[3].Type != "image" || action.Message[3].Data["file"] != "base64://"+base64.StdEncoding.EncodeToString(original) {
					t.Errorf("scope, replay or managed-original mismatch: %+v", action)
					return
				}
				response = []byte(`{"status":"ok","retcode":0,"data":{"message_id":29}}`)
			default:
				writer := httptest.NewRecorder()
				req := httptest.NewRequest(http.MethodPost, "/"+request.Action, bytes.NewReader(request.Params))
				req.Header.Set("Authorization", "Bearer "+token)
				httpPeer.ServeHTTP(writer, req)
				if writer.Code != 200 {
					t.Errorf("socket action %s failed: %d", request.Action, writer.Code)
					return
				}
				response = writer.Body.Bytes()
			}
			var data map[string]json.RawMessage
			if err := json.Unmarshal(response, &data); err != nil {
				t.Error(err)
				return
			}
			data["echo"], _ = json.Marshal(request.Echo)
			body, _ := json.Marshal(data)
			if err := socket.Write(ctx, websocket.MessageText, body); err != nil {
				return
			}
			if request.Action == "get_login_info" {
				sockets <- socket
			}
		}
	}))
	defer server.Close()
	const config = "/api/plugins/snowluma/config"
	invalid := commandRequest(t, addr, "PUT", config, []byte(`{"transport":"websocket","url":"127.0.0.1:9000","accountId":"42"}`), 400)
	if !bytes.Contains(invalid, []byte("websocket_scheme_required")) {
		t.Fatalf("missing useful error: %s", invalid)
	}
	previous := commandRequest(t, addr, "GET", config, nil, 200)
	if !bytes.Contains(previous, []byte(httpURL)) || !bytes.Contains(previous, []byte(eventPath)) {
		t.Fatalf("invalid websocket value changed the active config: %s", previous)
	}

	good := fmt.Sprintf(`{"url":%q,"accountId":"42"}`, "ws"+strings.TrimPrefix(server.URL, "http")+"/socket")
	saved := commandRequest(t, addr, "PUT", config, []byte(good), 200)
	if !bytes.Contains(saved, []byte(`"transport":"websocket"`)) || !bytes.Contains(saved, []byte(eventPath)) || bytes.Contains(saved, []byte(token)) {
		t.Fatalf("legacy config was not normalized, lost HTTP settings or disclosed a token: %s", saved)
	}
	next := func() *websocket.Conn {
		select {
		case socket := <-sockets:
			return socket
		case <-time.After(8 * time.Second):
			t.Fatal("no authenticated socket")
			return nil
		}
	}
	send := func(socket *websocket.Conn, event string) {
		t.Helper()
		if err := socket.Write(ctx, websocket.MessageText, []byte(event)); err != nil {
			t.Fatal(err)
		}
	}
	first := next()
	send(first, snowSocketEvent("private", 901, 24))
	second := next()
	// A successful history action proves the replacement peer is ready before
	// sending its post-login event. The action uses the same physical socket.
	remote := commandRequest(t, addr, "GET", "/api/scenes/"+url.PathEscape(historyGroup)+"/history?source=remote", nil, 200)
	if !bytes.Contains(remote, []byte("Group history witness")) {
		t.Fatalf("unexpected qualified history response: %s", remote)
	}
	private := "/api/scenes/" + url.PathEscape(historyDirect)
	remote = commandRequest(t, addr, "GET", private+"/history?source=remote", nil, 200)
	var history struct {
		Items []struct{ Parts []struct{ MediaURL string } }
	}
	if err := json.Unmarshal(remote, &history); err != nil || len(history.Items) != 2 || len(history.Items[0].Parts) != 2 || history.Items[0].Parts[1].MediaURL == "" {
		t.Fatalf("private image history unavailable over socket: %s", remote)
	}
	if image := commandRequest(t, addr, "GET", history.Items[0].Parts[1].MediaURL, nil, 200); !bytes.Equal(image, original) {
		t.Fatal("socket history image differed from the scoped original")
	}
	send(second, snowSocketEvent("group", 904, 83))
	snowSocketHistory(t, addr, "group", 904, 83, 81, 82)
	send(second, snowSocketEvent("private", 904, 83))
	snowSocketHistory(t, addr, "private", 904, 83, 81, 82)
	send(second, fmt.Sprintf(`{"post_type":"notice","notice_type":"friend_recall","self_id":42,"user_id":904,"message_id":83,"time":%d}`, time.Now().Unix()))
	send(second, snowMentionEvent(snowSocketEvent("group", 901, 25)))
	snowSocketHistory(t, addr, "group", 901, 29)
	private = "/api/scenes/" + url.PathEscape("ob:s:"+base64.RawURLEncoding.EncodeToString([]byte(`["snowluma","42","private","904"]`)))
	recalled := commandRequest(t, addr, "GET", private+"/history?source=local", nil, 200)
	if !bytes.Contains(recalled, []byte(`"status":"recalled"`)) {
		t.Fatalf("peer socket recall was not durably applied: %s", recalled)
	}
	snowSocketHistory(t, addr, "group", 904, 83, 81, 82)
	if sent.Load() != 2 || connections.Load() != 2 {
		t.Fatalf("unexpected playback: sends=%d sockets=%d", sent.Load(), connections.Load())
	}
	coldAddr := reopenLegacy()
	third := next()
	send(third, snowSocketEvent("group", 908, 86))
	snowSocketHistory(t, coldAddr, "group", 908, 86)
	if sent.Load() != 2 || connections.Load() != 3 {
		t.Fatalf("unexpected legacy reopen: sends=%d sockets=%d", sent.Load(), connections.Load())
	}
	return saved
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
	type message struct {
		ID    string
		Parts []struct{ Type, UserID string }
	}
	mentioned := func(row message, id string) bool {
		return row.ID == id && slices.ContainsFunc(row.Parts, func(p struct{ Type, UserID string }) bool {
			return p.Type == "mention" && p.UserID == "777"
		})
	}
	var in struct {
		SceneKind    string
		Messages     []message
		Evidence     []struct{ Value message }
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
			if !slices.ContainsFunc(in.Evidence, func(row struct{ Value message }) bool {
				return mentioned(row.Value, in.PlanContract.TargetMessageIDs[0])
			}) {
				http.Error(w, "expected incoming scoped mention in Planner evidence", 400)
				return
			}
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
			if !slices.ContainsFunc(in.Messages, func(row message) bool { return mentioned(row, in.Plan.TargetMessageIDs[0]) }) {
				http.Error(w, "expected scoped mention in Replyer messages", 400)
				return
			}
			parts = append(parts, map[string]any{"type": "mention", "userId": "901"}, map[string]any{"type": "media", "mediaType": "image", "assetId": in.Plan.Assets[0].AssetID})
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
