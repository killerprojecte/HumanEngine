//go:build linux && sqlite_fts5 && humanengine_icu

package main

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/killerprojecte/HumanEngine/backend/internal/recovery"
)

func TestRuntimePreviewExternalV2ColdReplacement(t *testing.T) {
	args := launcherArgs(t)
	// launcherArgs owns the independent installation directories and plugin root.
	argument := func(key string) string {
		t.Helper()
		for i := range args {
			if args[i] == key {
				return args[i+1]
			}
		}
		t.Fatalf("missing launcher argument %s", key)
		return ""
	}
	if _, err := recovery.AuthorityPath(argument("--database"), argument("--authority")); err != nil {
		t.Fatal(err)
	}
	root := argument("--plugin-root")
	node := filepath.Join(t.TempDir(), "node-witness")
	write := func(path string, data []byte, mode os.FileMode) {
		t.Helper()
		if err := os.WriteFile(path, data, mode); err != nil {
			t.Fatal(err)
		}
	}
	write(node, []byte("#!/bin/sh\nprintf started > \"$0.started\"\nexit 97\n"), 0700)
	t.Cleanup(func() {
		if _, err := os.Stat(node + ".started"); !os.IsNotExist(err) {
			t.Errorf("unexpected Node startup or marker error: %v", err)
		}
	})
	args = append(args, "--node", node)
	binary := filepath.Join(t.TempDir(), "humanengine")
	cmd := exec.CommandContext(t.Context(), "go", "build", "-tags=sqlite_fts5,humanengine_icu", "-o", binary, ".")
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build unbundled host: %v\n%s", err, output)
	}
	const id = "stationary-fixture"
	const first = "1.0.0"
	const second = "2.0.0"
	type version struct {
		entry, artifact string
		bytes           []byte
	}
	buildGuest := func(v string) version {
		t.Helper()
		entry := id + "-" + v + ".v2.json"
		artifact := id + "-" + v + ".wasm"
		path := filepath.Join(root, artifact)
		build := exec.CommandContext(t.Context(), "go", "build", "-trimpath", "-buildmode=c-shared",
			"-ldflags=-X main.marker="+v, "-o", path, "./internal/app/testdata/externalv2")
		build.Dir = "../.."
		build.Env = append(os.Environ(), "GOOS=wasip1", "GOARCH=wasm", "CGO_ENABLED=0")
		if output, err := build.CombinedOutput(); err != nil {
			t.Fatalf("build external guest %s: %v\n%s", v, err, output)
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		write(filepath.Join(root, entry), []byte(fmt.Sprintf(`{"id":%q,"apiVersion":2,"name":"Independent tool","version":%q,"artifact":%q,"artifactSha256":%q,"capabilities":["tool"]}`, id, v, artifact, fmt.Sprintf("%x", sha256.Sum256(raw)))), 0600)
		return version{entry, path, raw}
	}
	v1, v2 := buildGuest(first), buildGuest(second)
	if sha256.Sum256(v1.bytes) == sha256.Sum256(v2.bytes) {
		t.Fatal("fixture versions must have independently built bytes")
	}

	results := make(chan externalV2PlannerRead, 1)
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		externalV2Model(w, r, results)
	}))
	defer provider.Close()

	var address string
	var logs *commandLog
	boot := func(entry string) func() {
		t.Helper()
		process, processLogs, done := runtimeCommandWithPlugin(t, args, entry, binary)
		logs = processLogs
		address = runtimeAddress(t, logs, done)
		return func() {
			t.Helper()
			if err := process.Process.Signal(syscall.SIGTERM); err != nil {
				t.Fatal(err)
			}
			waitCommandExit(t, logs, done)
		}
	}
	catalog := func(version string, enabled bool, failure string) {
		t.Helper()
		body := commandRequest(t, address, "GET", "/api/plugins", nil, 200)
		var rows []struct {
			ID, Version, Error string
			APIVersion         int
			Builtin, Enabled   bool
		}
		if err := json.Unmarshal(body, &rows); err != nil {
			t.Fatal(err)
		}
		if version == "" {
			if len(rows) != 0 {
				t.Fatalf("expected empty catalog: %s", body)
			}
		} else if len(rows) != 1 || rows[0].ID != id || rows[0].Version != version || rows[0].APIVersion != 2 ||
			rows[0].Builtin || rows[0].Enabled != enabled || rows[0].Error != failure {
			t.Fatalf("catalog must match configured version %q (enabled=%t, failure=%q): %s\n%s", version, enabled, failure, body, logs.snapshot())
		}
	}
	verifyPlanner := func(want externalV2PlannerRead) {
		t.Helper()
		raw := commandRequest(t, address, "POST", "/api/simulations", nil, 200)
		var simulation struct{ ID string }
		if err := json.Unmarshal(raw, &simulation); err != nil || simulation.ID == "" {
			t.Fatalf("simulation: %s, %v", raw, err)
		}
		raw = commandRequest(t, address, "POST", "/api/simulations/"+simulation.ID+"/messages", []byte(`{"text":"Read from the external fixture."}`), 200)
		var rows []struct{ SenderID string }
		if err := json.Unmarshal(raw, &rows); err != nil || len(rows) != 1 || rows[0].SenderID != "simulation-user" {
			t.Fatalf("noReply should leave only the synthetic input: %s, %v", raw, err)
		}
		select {
		case got := <-results:
			if got != want {
				t.Fatalf("Planner read witness %+v, want %+v", got, want)
			}
		default:
			t.Fatalf("actual Planner did not observe expected read state: %+v\n%s", want, logs.snapshot())
		}
		if !strings.Contains(logs.snapshot(), fmt.Sprintf(`"code":"runtime.turn.skipped","sceneId":%q`, simulation.ID)) {
			t.Fatalf("missing completed skipped turn for %s\n%s", simulation.ID, logs.snapshot())
		}
	}
	stop := boot("")
	catalog("", false, "")
	models := fmt.Sprintf(`{"providers":[{"id":"fixture","name":"Loopback","protocol":"openai-chat","baseUrl":%q,"retryCount":0,"timeoutMs":4000}],"models":[{"id":"fixture","displayName":"Planner","providerId":"fixture","apiModelName":"planner","capabilities":["text","tools"]}],"routes":[{"role":"planner","orderedModelIds":["fixture"],"timeoutMs":5000}]}`, provider.URL+"/v1")
	commandRequest(t, address, "PUT", "/api/models", []byte(models), 200)
	verifyPlanner(externalV2PlannerRead{})
	stop()

	stop = boot(v1.entry)
	catalog(first, false, "")
	path := "/api/plugins/" + id + "/enabled"
	commandRequest(t, address, "PUT", path, []byte(`{"enabled":true}`), 200)
	catalog(first, true, "")
	verifyPlanner(externalV2PlannerRead{first, 1})
	stop()

	for range 2 {
		stop = boot(v2.entry)
		catalog(second, true, "")
		verifyPlanner(externalV2PlannerRead{second, 1})
		stop()
	}

	// Valid but wrong WASM at the selected artifact path must not run against
	// the new manifest. Leave the previous version's files in place so this
	// also detects an unintended automatic fallback.
	write(v2.artifact, v1.bytes, 0600)
	stop = boot(v2.entry)
	catalog(second, false, "plugin_artifact_digest_mismatch")
	verifyPlanner(externalV2PlannerRead{})
	stop()

	write(v2.artifact, v2.bytes, 0600)
	stop = boot(v2.entry)
	catalog(second, false, "")
	commandRequest(t, address, "PUT", path, []byte(`{"enabled":true}`), 200)
	verifyPlanner(externalV2PlannerRead{second, 1})
	stop()

	stop = boot("")
	catalog("", false, "")
	verifyPlanner(externalV2PlannerRead{})
	stop()
}

type externalV2PlannerRead struct {
	Marker string
	Reads  int
}

func externalV2Model(w http.ResponseWriter, r *http.Request, results chan<- externalV2PlannerRead) {
	var request struct {
		Messages []struct{ Role, Content string }
		Tools    []struct{ Function struct{ Name string } }
	}
	if r.URL.Path != "/v1/chat/completions" || json.NewDecoder(r.Body).Decode(&request) != nil {
		http.Error(w, "invalid model request", 400)
		return
	}
	var data struct {
		Messages     []struct{ ID string }
		PlanContract struct{ TargetMessageIDs, ContextMessageIDs []string }
	}
	var observed externalV2PlannerRead
	for _, message := range request.Messages {
		switch message.Role {
		case "user":
			_ = json.Unmarshal([]byte(message.Content), &data)
		case "tool":
			var result struct {
				ToolName string
				Value    externalV2PlannerRead
			}
			if json.Unmarshal([]byte(message.Content), &result) == nil && result.ToolName == "stationary-fixture.read" {
				observed = result.Value
			}
		}
	}
	name := "reply__plan"
	var arguments any = map[string]any{
		"action": "noReply", "targetMessageIds": data.PlanContract.TargetMessageIDs,
		"contextMessageIds": data.PlanContract.ContextMessageIDs, "strategy": "Read only.",
		"constraints": []any{}, "facts": []any{}, "assets": []any{}, "emoji": []any{},
		"expressionGoals": []any{}, "actions": []any{},
	}
	for _, tool := range request.Tools {
		switch tool.Function.Name {
		case "participation__decide":
			if len(data.Messages) != 1 || data.Messages[0].ID == "" {
				http.Error(w, "expected single simulation source", 400)
				return
			}
			name, arguments = tool.Function.Name, map[string]any{"action": "reply", "reasonCode": "fixture", "targetMessageIds": []string{data.Messages[0].ID}}
		case "stationary-fixture__read":
			if observed.Marker == "" {
				name, arguments = tool.Function.Name, map[string]string{"key": "sample"}
			}
		}
	}
	if name == "reply__plan" {
		select {
		case results <- observed:
		default:
			http.Error(w, "unexpected extra Planner continuation", 400)
			return
		}
	}
	raw, _ := json.Marshal(arguments)
	w.Header().Set("Content-Type", "application/json")
	fmt.Fprintf(w, `{"choices":[{"finish_reason":"tool_calls","message":{"role":"assistant","tool_calls":[{"id":%q,"type":"function","function":{"name":%q,"arguments":%q}}]}}]}`, name, name, raw)
}
