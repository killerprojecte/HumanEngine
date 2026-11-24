//go:build linux && sqlite_fts5 && humanengine_icu

package app

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	"github.com/killerprojecte/HumanEngine/backend/internal/botruntime"
	"github.com/killerprojecte/HumanEngine/backend/internal/dispatch"
	"github.com/killerprojecte/HumanEngine/backend/internal/installation"
	"github.com/killerprojecte/HumanEngine/backend/internal/recovery"
)

func TestInstalledExternalV2ReadTool(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	root := t.TempDir()
	pluginRoot := filepath.Join(root, "plugins")
	if err := os.Mkdir(pluginRoot, 0700); err != nil {
		t.Fatal(err)
	}
	const id = "stationary-fixture"
	const tool = id + ".read"
	const entry = id + ".v2.json"
	const artifact = "stationary.wasm"
	const manifest = `{"id":"stationary-fixture","apiVersion":2,"name":"Independent fixture","version":"1.0.0","artifact":"stationary.wasm","capabilities":["tool"]}`
	if err := os.WriteFile(filepath.Join(pluginRoot, entry), []byte(manifest), 0600); err != nil {
		t.Fatal(err)
	}
	build := exec.CommandContext(ctx, "go", "build", "-trimpath", "-buildmode=c-shared", "-o", filepath.Join(pluginRoot, artifact), "./testdata/externalv2")
	build.Env = append(os.Environ(), "GOOS=wasip1", "GOARCH=wasm", "CGO_ENABLED=0")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build independent WASI guest: %v\n%s", err, output)
	}
	node := filepath.Join(root, "node-witness")
	if err := os.WriteFile(node, []byte("#!/bin/sh\nprintf started > \"$0.started\"\nexit 97\n"), 0700); err != nil {
		t.Fatal(err)
	}
	nodeMarker := node + ".started"
	source := os.Getenv("HE_SOURCE_ROOT")
	if source == "" {
		source = "../../.."
	}
	extension, err := filepath.Abs(filepath.Join(source, "node_modules/sqlite-vec-linux-x64/vec0.so"))
	if err != nil {
		t.Fatal(err)
	}
	database := filepath.Join(root, "live", "main.sqlite")
	authority := filepath.Join(root, "authority")
	if _, err := recovery.AuthorityPath(database, authority); err != nil {
		t.Fatal(err)
	}
	var logs applicationLogBuffer
	app, err := Open(ctx, Options{
		Installation: installation.Options{Database: database,
			AuthorityDirectory: authority, VectorExtension: extension, SourceTimezone: "UTC"},
		PluginRoot: pluginRoot, ImageLibrary: os.Getenv("HE_IMAGE_LIBRARY"), Node: node, AdminToken: "synthetic-token", Locale: "en-US",
		Entries: []string{entry}, Builtins: []string{}, Diagnostics: &logs,
	})
	if err != nil {
		t.Fatalf("open unbundled application: %v\n%s", err, logs.snapshot())
	}
	t.Cleanup(func() {
		if t.Failed() {
			t.Log(logs.snapshot())
		}
	})
	defer func() {
		if err := app.Shutdown(context.Background()); err != nil {
			t.Error(err)
		}
		if _, err := os.Stat(nodeMarker); !os.IsNotExist(err) {
			t.Errorf("unexpected V1 helper startup or marker error: %v", err)
		}
	}()

	const scene = "external-v2-scene"
	const nativeTool = "stationary-fixture__read"
	var sends atomic.Int32
	app.Engine.Runtime.AddAdapter("synthetic", &botruntime.Adapter{Transport: &dispatch.Adapter{
		Send: func(dispatch.SceneRef, dispatch.Segment, *dispatch.Signal, func() error) *dispatch.Future[dispatch.Receipt] {
			sends.Add(1)
			return dispatch.Rejected[dispatch.Receipt](fmt.Errorf("unexpected send"))
		},
	}})
	appRequest(t, app, "PUT", "/api/scenes/"+scene, map[string]any{
		"id": scene, "adapterId": "synthetic", "accountId": "bot", "platformSceneId": scene, "kind": "group",
		"name": "Fixture", "participationEnabled": true, "learningEnabled": false, "proactiveEnabled": false,
	})
	type witness struct {
		advertised bool
		reads      int
	}
	results := make(chan witness, 8)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Messages []struct{ Role, Content string }
			Tools    []struct{ Function struct{ Name string } }
		}
		if r.URL.Path != "/v1/chat/completions" || json.NewDecoder(r.Body).Decode(&req) != nil {
			http.Error(w, "invalid fixture request", 400)
			return
		}
		names := make([]string, 0, len(req.Tools))
		for _, tool := range req.Tools {
			names = append(names, tool.Function.Name)
		}
		var name, arguments string
		var contract struct {
			PlanContract struct{ TargetMessageIDs, ContextMessageIDs []string }
		}
		for _, message := range req.Messages {
			if message.Role == "user" {
				_ = json.Unmarshal([]byte(message.Content), &contract)
			}
		}
		noReply := func() {
			name = "reply__plan"
			encoded, _ := json.Marshal(map[string]any{
				"action": "noReply", "targetMessageIds": contract.PlanContract.TargetMessageIDs,
				"contextMessageIds": contract.PlanContract.ContextMessageIDs, "strategy": "Read only.",
				"constraints": []any{}, "facts": []any{}, "assets": []any{}, "emoji": []any{},
				"expressionGoals": []any{}, "actions": []any{},
			})
			arguments = string(encoded)
		}
		w.Header().Set("Content-Type", "application/json")
		if slices.Contains(names, "participation__decide") {
			name, arguments = "participation__decide", `{"action":"reply","reasonCode":"fixture","targetMessageIds":["current"]}`
		} else {
			advertised := slices.Contains(names, nativeTool)
			var receipt struct {
				ToolName string
				Value    struct {
					Marker string
					Reads  int
				}
			}
			for _, message := range req.Messages {
				if message.Role == "tool" && json.Unmarshal([]byte(message.Content), &receipt) == nil &&
					receipt.ToolName == tool && receipt.Value.Marker == "external read fixture" {
					results <- witness{advertised, receipt.Value.Reads}
					noReply()
					break
				}
			}
			if name == "" {
				if !advertised {
					results <- witness{}
					noReply()
				} else {
					name, arguments = nativeTool, `{"key":"sample"}`
				}
			}
		}
		fmt.Fprintf(w, `{"choices":[{"finish_reason":"tool_calls","message":{"role":"assistant","tool_calls":[{"id":%q,"type":"function","function":{"name":%q,"arguments":%q}}]}}]}`, name, name, arguments)
	}))
	defer server.Close()
	models := fmt.Sprintf(`{"providers":[{"id":"fixture","name":"Loopback","protocol":"openai-chat","baseUrl":%q,"retryCount":0,"timeoutMs":4000}],"models":[{"id":"fixture","displayName":"Planner","providerId":"fixture","apiModelName":"planner","capabilities":["text","tools"]}],"routes":[{"role":"planner","orderedModelIds":["fixture"],"timeoutMs":5000}]}`, server.URL+"/v1")
	appRequest(t, app, "PUT", "/api/models", json.RawMessage(models))

	now := time.Now().UTC().Format(time.RFC3339Nano)
	topic, err := app.Engine.Runtime.Ingest(ctx, scene, memoryToolObject("id", "current", "sceneId", scene,
		"senderId", "person", "origin", "live", "status", "available", "sentAt", now, "receivedAt", now,
		"parts", []any{memoryToolObject("type", "text", "text", "Read the independent fixture.")}), false)
	if err != nil {
		t.Fatal(err)
	}
	run := func(want witness) {
		t.Helper()
		handle, err := app.Engine.Runtime.Run(scene, topic, false)
		if err != nil {
			t.Fatal(err)
		}
		select {
		case <-handle.Done:
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
		if result := handle.Wait(); result.Err != nil || result.Reason != "planner_no_reply" || len(result.Receipts) != 0 {
			t.Fatalf("expected noReply without an outbound receipt, got %+v", result)
		}
		select {
		case got := <-results:
			if got != want {
				t.Fatalf("Planner saw %+v, want %+v (turn: %+v)", got, want, handle.Wait())
			}
		default:
			t.Fatalf("Planner did not observe a tool result: %+v", handle.Wait())
		}
	}
	toggle := func(enabled bool) {
		t.Helper()
		appRequest(t, app, "PUT", "/api/plugins/"+id+"/enabled", map[string]any{"enabled": enabled})
	}
	plugins := appRequest(t, app, "GET", "/api/plugins", nil).([]any)
	if len(plugins) != 1 || field(plugins[0], "id") != id || field(plugins[0], "builtin") != false || field(plugins[0], "apiVersion") != float64(2) {
		t.Fatalf("unexpected composition: %+v", plugins)
	}
	run(witness{})
	toggle(true)
	run(witness{true, 1})
	run(witness{true, 2})
	toggle(false)
	if app.WASM.Loaded(id) {
		t.Fatal("disabled guest remained loaded")
	}
	run(witness{})
	toggle(true)
	run(witness{true, 1})
	if sends.Load() != 0 {
		t.Fatalf("read-only fixture sent %d replies", sends.Load())
	}
}
