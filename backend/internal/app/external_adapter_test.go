//go:build linux && sqlite_fts5 && humanengine_icu

package app

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/killerprojecte/HumanEngine/backend/internal/installation"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/recovery"
)

func TestInstalledExternalV2SignedAdapter(t *testing.T) {
	const (
		id      = "letterbox-independent"
		adapter = "letterbox-wire"
		account = "tenant-42"
		group   = "shared-box"
		key     = "synthetic-adapter-key"
	)
	root := t.TempDir()
	packageDir := filepath.Join(root, "plugins")
	if err := os.Mkdir(packageDir, 0700); err != nil {
		t.Fatal(err)
	}
	sig := func(purpose string, raw []byte) string {
		h := hmac.New(sha256.New, []byte(key))
		_, _ = io.WriteString(h, purpose+"\x00")
		_, _ = h.Write(raw)
		return hex.EncodeToString(h.Sum(nil))
	}
	pack := func(values ...string) string {
		data, _ := json.Marshal(values)
		return base64.RawURLEncoding.EncodeToString(data)
	}
	sceneID := "v2:s:" + pack(adapter, account, "group", group)
	sender := "v2:u:" + pack(adapter, account, "letter-writer")
	sourceID := sceneID + ":m:" + pack("source-letter")
	sentID := sceneID + ":m:" + pack("returned-ack")
	node := filepath.Join(root, "node-no")
	if err := os.WriteFile(node, []byte("#!/bin/sh\ntouch \"$0.started\"\nexit 97\n"), 0700); err != nil {
		t.Fatal(err)
	}
	source := os.Getenv("HE_SOURCE_ROOT")
	if source == "" {
		source = "../../.."
	}
	vec, err := filepath.Abs(filepath.Join(source, "node_modules/sqlite-vec-linux-x64/vec0.so"))
	if err != nil {
		t.Fatal(err)
	}
	dbPath := filepath.Join(root, "live", "main.sqlite")
	authority := filepath.Join(root, "authority")
	if _, err := recovery.AuthorityPath(dbPath, authority); err != nil {
		t.Fatal(err)
	}
	var logs applicationLogBuffer
	t.Cleanup(func() {
		if t.Failed() {
			t.Log(logs.snapshot())
		}
	})
	opt := Options{
		Installation: installation.Options{Database: dbPath, AuthorityDirectory: authority, VectorExtension: vec, SourceTimezone: "UTC"},
		PluginRoot:   packageDir, ImageLibrary: os.Getenv("HE_IMAGE_LIBRARY"), Node: node, AdminToken: "synthetic-token", Locale: "en-US",
		Entries: []string{}, Builtins: []string{}, Diagnostics: &logs,
	}
	ctx, stop := context.WithTimeout(t.Context(), 4*time.Minute)
	defer stop()
	open := func(opts Options) *Application {
		t.Helper()
		app, err := Open(ctx, opts)
		if err != nil {
			t.Fatal(err)
		}
		return app
	}
	app := open(opt)
	defer func() {
		if err := app.Shutdown(context.Background()); err != nil {
			t.Error(err)
		}
	}()
	if got := appRequest(t, app, "GET", "/api/plugins", nil).([]any); len(got) != 0 {
		t.Fatal("blank host loaded a plugin", got)
	}
	if err := app.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}

	wasmPath := filepath.Join(packageDir, "letterbox.wasm")
	build := exec.CommandContext(ctx, "go", "build", "-buildvcs=false", "-buildmode=c-shared", "-o", wasmPath, "../externaladapter/testdata/letterbox")
	build.Env = append(os.Environ(), "GOOS=wasip1", "GOARCH=wasm", "CGO_ENABLED=0")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build separate WASI package: %v %s", err, out)
	}
	artifact, err := os.ReadFile(wasmPath)
	if err != nil {
		t.Fatal(err)
	}
	manifest := fmt.Sprintf(`{"id":%q,"apiVersion":2,"name":"Independent Letterbox","version":"1","artifact":"letterbox.wasm","artifactSha256":"%x","capabilities":["adapter"],"adapter":{"id":%q,"wire":"signed-json-http-1"}}`, id, sha256.Sum256(artifact), adapter)
	entry := id + ".v2.json"
	if err := os.WriteFile(filepath.Join(packageDir, entry), []byte(manifest), 0600); err != nil {
		t.Fatal(err)
	}
	opt.Entries = []string{entry}
	app = open(opt)
	registered := appRequest(t, app, "GET", "/api/plugins", nil).([]any)
	if len(registered) != 1 || field(registered[0], "id") != id || field(registered[0], "builtin") != false || field(registered[0], "enabled") != false {
		t.Fatal("wrong package registration:", registered)
	}

	window, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	listen := window.Addr().String()
	if err := window.Close(); err != nil {
		t.Fatal(err)
	}

	var physical, modelCalls atomic.Int32
	accepted := make(chan struct{}, 1)
	action := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, err := io.ReadAll(r.Body)
		if err != nil || r.URL.Path != "/receive-letter" || r.Method != http.MethodPost ||
			r.Header.Get("X-HumanEngine-Signature") != sig("action", raw) {
			t.Error("incorrect physical action signature or target")
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		var wire struct {
			Box, InReplyTo string
			Contents       []struct{ Type, Text string }
		}
		if json.Unmarshal(raw, &wire) != nil || wire.Box != group || wire.InReplyTo != "source-letter" ||
			len(wire.Contents) != 1 || wire.Contents[0].Type != "text" || wire.Contents[0].Text != "Reply from native Go." {
			t.Errorf("unexpected translated send: %s", raw)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if physical.Add(1) != 1 {
			t.Error("repeated physical action")
		}
		ack := []byte(`{"ticket":"returned-ack"}`)
		w.Header().Set("X-HumanEngine-Signature", sig("receipt", ack))
		_, _ = w.Write(ack)
		select {
		case accepted <- struct{}{}:
		default:
		}
	}))
	defer action.Close()

	type prompt struct {
		Messages     []struct{ ID string }
		PlanContract struct{ TargetMessageIDs, ContextMessageIDs []string }
		Plan         struct{ TargetMessageIDs []string }
	}
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Messages []struct{ Role, Content string }
			Tools    []struct{ Function struct{ Name string } }
		}
		if r.URL.Path != "/v1/chat/completions" || json.NewDecoder(r.Body).Decode(&req) != nil {
			w.WriteHeader(400)
			return
		}
		names := make([]string, 0, len(req.Tools))
		var input prompt
		for _, m := range req.Messages {
			if m.Role == "user" {
				_ = json.Unmarshal([]byte(m.Content), &input)
			}
		}
		for _, tool := range req.Tools {
			names = append(names, tool.Function.Name)
		}
		var name string
		var args map[string]any
		switch {
		case slices.Contains(names, "participation__decide") && len(input.Messages) == 1:
			name = "participation__decide"
			args = map[string]any{"action": "reply", "reasonCode": "fixture", "targetMessageIds": []string{input.Messages[0].ID}}
		case slices.Contains(names, "reply__plan") && len(input.PlanContract.TargetMessageIDs) == 1:
			name = "reply__plan"
			args = map[string]any{"action": "reply", "deliveryTool": "messages.send", "strategy": "Answer the letter.",
				"targetMessageIds": input.PlanContract.TargetMessageIDs, "contextMessageIds": input.PlanContract.ContextMessageIDs,
				"constraints": []any{}, "facts": []any{}, "assets": []any{}, "emoji": []any{}, "actions": []any{}}
		case slices.Contains(names, "messages__send") && len(input.Plan.TargetMessageIDs) == 1:
			name = "messages__send"
			args = map[string]any{"operationId": "letter-reply", "segments": []any{map[string]any{"replyToId": input.Plan.TargetMessageIDs[0],
				"parts": []any{map[string]any{"type": "text", "text": "Reply from native Go."}}}}}
		default:
			t.Errorf("unexpected fixture model role: %v %+v", names, input)
			w.WriteHeader(400)
			return
		}
		modelCalls.Add(1)
		b, _ := json.Marshal(args)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"choices":[{"finish_reason":"tool_calls","message":{"role":"assistant","tool_calls":[{"id":%q,"type":"function","function":{"name":%q,"arguments":%q}}]}}]}`, name, name, b)
	}))
	defer model.Close()
	settings := appRequest(t, app, "GET", "/api/settings", nil).(*jsjson.Object)
	_ = settings.Set("sourceEventRevisionsEnabled", true)
	_ = settings.Set("promptContextWindowEnabled", false)
	_ = settings.Set("debounceMs", float64(0))
	_ = settings.Set("groupTurnCooldownMs", float64(0))
	wire, _ := jsjson.Marshal(settings)
	appRequest(t, app, "PUT", "/api/settings", json.RawMessage(wire))
	models := fmt.Sprintf(`{"providers":[{"id":"fixture","name":"Loopback","protocol":"openai-chat","baseUrl":%q,"retryCount":0,"timeoutMs":4000}],"models":[{"id":"fixture","displayName":"Deterministic","providerId":"fixture","apiModelName":"fixture","capabilities":["text","tools"]}],"routes":[{"role":"planner","orderedModelIds":["fixture"],"timeoutMs":5000},{"role":"replyer","orderedModelIds":["fixture"],"timeoutMs":5000}]}`, model.URL+"/v1")
	appRequest(t, app, "PUT", "/api/models", json.RawMessage(models))
	appRequest(t, app, "PUT", "/api/plugins/"+id+"/config", map[string]any{"listen": listen, "actionUrl": action.URL + "/receive-letter", "accountId": account, "botId": "letterbot", "key": key})
	adminConfig := appRequest(t, app, "GET", "/api/plugins/"+id+"/config", nil).(*jsjson.Object)
	redacted, _ := jsjson.Stringify(adminConfig)
	if strings.Contains(redacted, key) {
		t.Fatal("transport secret returned in configuration")
	}
	appRequest(t, app, "PUT", "/api/plugins/"+id+"/enabled", map[string]any{"enabled": true})
	appRequest(t, app, "PUT", "/api/scenes/"+sceneID, map[string]any{
		"id": sceneID, "adapterId": adapter, "accountId": account, "platformSceneId": group, "kind": "group", "name": "Letterbox",
		"participationEnabled": true, "learningEnabled": false, "proactiveEnabled": false,
	})
	stamp := time.Now().UTC().Format(time.RFC3339Nano)
	post := func(purpose, box string, recall bool) (int, error) {
		data, _ := json.Marshal(map[string]any{"letter": "source-letter", "box": box, "writer": "letter-writer", "payload": "A message arrived.", "stamp": stamp, "recall": recall})
		request, err := http.NewRequestWithContext(ctx, "POST", "http://"+listen+"/event", bytes.NewReader(data))
		if err != nil {
			return 0, err
		}
		request.Header.Set("X-HumanEngine-Signature", sig(purpose, data))
		response, err := http.DefaultClient.Do(request)
		if err != nil {
			return 0, err
		}
		defer response.Body.Close()
		return response.StatusCode, nil
	}
	if code, err := post("action", group, false); err != nil || code != 401 || modelCalls.Load() != 0 {
		t.Fatal("unexpected unauthorized ingress", code, err)
	}
	if code, err := post("event", group, false); err != nil || code != 204 {
		t.Fatal("signed ingress failed", code, err)
	}
	select {
	case <-accepted:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}

	wait := time.NewTimer(10 * time.Second)
	defer wait.Stop()
	poll := time.NewTicker(25 * time.Millisecond)
	defer poll.Stop()
	for {
		rows := appRequest(t, app, "GET", "/api/scenes/"+sceneID+"/history?source=local", nil)
		items, _ := field(rows, "items").([]any)
		if len(items) == 2 && field(items[0], "id") == sourceID && field(items[0], "senderId") == sender && field(items[1], "id") == sentID {
			break
		}
		select {
		case <-wait.C:
			t.Fatalf("canonical local history missing: %v", rows)
		case <-poll.C:
		}
	}
	if err := app.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite3", dbPath+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var sourceCount int
	if err = db.QueryRow(`SELECT count(*) FROM source_event_heads WHERE scene_id=? AND message_id=?`, sceneID, sourceID).Scan(&sourceCount); err != nil || sourceCount != 1 {
		t.Fatal("durable source head not retained", sourceCount, err)
	}
	app = open(opt)
	if !app.WASM.Loaded(id) {
		t.Fatal("cold install did not reclaim the route using the stored key")
	}
	if code, err := post("event", group, false); err != nil || code != 204 {
		t.Fatal("cold replay did not meet the normal duplicate boundary", code, err)
	}
	recalled := func(scene, id string) bool {
		t.Helper()
		var count int
		if err := db.QueryRow(`SELECT count(*) FROM recall_tombstones WHERE scene_id=? AND message_id=?`, scene, id).Scan(&count); err != nil {
			t.Fatal(err)
		}
		return count == 1
	}
	const other = "different-box"
	otherScene := "v2:s:" + pack(adapter, account, "group", other)
	if code, err := post("event", other, true); err != nil || code != 204 {
		t.Fatal("cold other group recall failed", code, err)
	}
	if recalled(sceneID, sourceID) || !recalled(otherScene, otherScene+":m:"+pack("source-letter")) {
		t.Fatal("recall crossed an enrolled scene boundary")
	}
	if code, err := post("event", group, true); err != nil || code != 204 {
		t.Fatal("cold original group recall failed", code, err)
	}
	if !recalled(sceneID, sourceID) {
		t.Fatal("cold original recall did not reach durable custody")
	}
	if err := app.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	opt.Entries = []string{}
	app = open(opt)
	if got := appRequest(t, app, "GET", "/api/plugins", nil).([]any); len(got) != 0 || app.WASM.Loaded(id) {
		t.Fatal("host fell back after removal:", got)
	}
	if physical.Load() != 1 || modelCalls.Load() != 3 {
		t.Fatal("unexpected replay after cold reopen; expected one physical ACK and three calls", physical.Load(), modelCalls.Load())
	}
	if _, err := os.Stat(node + ".started"); !os.IsNotExist(err) {
		t.Fatalf("Node started or witness check failed: %v", err)
	}
}
