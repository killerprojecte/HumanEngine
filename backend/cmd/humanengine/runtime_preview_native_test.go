//go:build linux && sqlite_fts5 && humanengine_icu && bundled_wasm

package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// Native process acceptance is mandatory in native-runtime-preview-test.
// Missing assets fail; these tests never substitute a graph or skip a dependency.
func waitRuntimeLog(t *testing.T, logs *commandLog, contains string, done <-chan error) string {
	t.Helper()
	timer := time.NewTimer(30 * time.Second)
	defer timer.Stop()
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
	for {
		s := logs.snapshot()
		if strings.Contains(s, contains) {
			return s
		}
		select {
		case err := <-done:
			t.Fatalf("command exited before %s: %v\n%s", contains, err, s)
		case <-timer.C:
			t.Fatalf("command never reached %s\n%s", contains, s)
		case <-ticker.C:
		}
	}
}
func runtimeAddress(t *testing.T, logs *commandLog, done <-chan error) string {
	t.Helper()
	s := waitRuntimeLog(t, logs, `"code":"server.listening"`, done)
	for _, line := range strings.Split(s, "\n") {
		prefix, flat, ok := strings.Cut(line, " | ")
		if !ok {
			continue
		}
		var event struct {
			Code    string `json:"code"`
			Address string `json:"address"`
		}
		if json.Unmarshal([]byte(flat), &event) == nil && event.Code == "server.listening" {
			if prefix == "" || event.Address == "" {
				t.Fatal("source listening event lacks prefix/flat address", line)
			}
			return event.Address
		}
	}
	t.Fatal("listening event lacks address")
	return ""
}
func commandRequest(t *testing.T, address, method, path string, body []byte, want int) []byte {
	t.Helper()
	request, err := http.NewRequest(method, "http://"+address+path, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer synthetic-launcher-token")
	request.Header.Set("Content-Type", "application/json")
	client := &http.Client{Timeout: 10 * time.Second}
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	data, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != want {
		t.Fatalf("%s %s: %d %s", method, path, response.StatusCode, data)
	}
	return data
}
func launcherArgs(t *testing.T) []string {
	t.Helper()
	root := t.TempDir()
	plugins := filepath.Join(root, "plugins")
	if err := os.Mkdir(plugins, 0700); err != nil {
		t.Fatal(err)
	}
	source := os.Getenv("HE_SOURCE_ROOT")
	if source == "" {
		t.Fatal("HE_SOURCE_ROOT required for real native launcher tests")
	}
	images := os.Getenv("HE_IMAGE_LIBRARY")
	if images == "" {
		t.Fatal("HE_IMAGE_LIBRARY required for real native launcher tests")
	}
	return []string{"--runtime-preview", "--database", filepath.Join(root, "live/main.sqlite"), "--authority", filepath.Join(root, "authority"), "--listen", "127.0.0.1:0", "--plugin-root", plugins, "--image-library", images, "--vector-extension", filepath.Join(source, "node_modules/sqlite-vec-linux-x64/vec0.so")}
}
func waitCommandExit(t *testing.T, logs *commandLog, done <-chan error) {
	t.Helper()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("command exit: %v\n%s", err, logs.snapshot())
		}
	case <-time.After(30 * time.Second):
		t.Fatalf("physical drain did not finish\n%s", logs.snapshot())
	}
	if !strings.Contains(logs.snapshot(), `"code":"app.drained"`) {
		t.Fatal("exit lacks real owner drain receipt")
	}
	if strings.Contains(logs.snapshot(), "synthetic-launcher-token") {
		t.Fatal("credential printed")
	}
}
func TestRuntimePreviewTCPBootWriteSignalDrainRestart(t *testing.T) {
	args := launcherArgs(t)
	cmd, logs, done := runtimeCommand(t, args)
	address := runtimeAddress(t, logs, done)
	settings := commandRequest(t, address, "GET", "/api/settings", nil, 200)
	// A still-unmigrated route must remain unavailable, never synthetic healthy.
	commandRequest(t, address, "GET", "/api/not-migrated", nil, 503)
	var configuration map[string]any
	if err := json.Unmarshal(settings, &configuration); err != nil {
		t.Fatal(err)
	}
	configuration["debugModelIO"] = true
	settings, err := json.Marshal(configuration)
	if err != nil {
		t.Fatal(err)
	}
	// HTTP 100 Continue proves a real handler reached the body reader, and thus
	// entered the application's admission group before SIGTERM seals it.
	connection, err := net.Dial("tcp", address)
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	if err = connection.SetDeadline(time.Now().Add(30 * time.Second)); err != nil {
		t.Fatal(err)
	}
	_, err = fmt.Fprintf(connection, "PUT /api/settings HTTP/1.1\r\nHost: %s\r\nAuthorization: Bearer synthetic-launcher-token\r\nContent-Type: application/json\r\nContent-Length: %d\r\nExpect: 100-continue\r\nConnection: close\r\n\r\n", address, len(settings))
	if err != nil {
		t.Fatal(err)
	}
	reader := bufio.NewReader(connection)
	response, err := http.ReadResponse(reader, &http.Request{Method: "PUT"})
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != 100 {
		t.Fatalf("expected body-reader admission, got %d", response.StatusCode)
	}
	if err = cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	waitRuntimeLog(t, logs, `"stage":"http"`, done)
	select {
	case err := <-done:
		t.Fatalf("exited with accepted body still blocked: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	if strings.Contains(logs.snapshot(), `"code":"app.drained"`) {
		t.Fatal("database drained before physical HTTP completion")
	}
	if _, err = connection.Write(settings); err != nil {
		t.Fatal(err)
	}
	response, err = http.ReadResponse(reader, &http.Request{Method: "PUT"})
	if err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(response.Body)
	response.Body.Close()
	if err != nil || response.StatusCode != 200 {
		t.Fatalf("accepted request did not commit during independent drain: %d %v %s", response.StatusCode, err, data)
	}
	waitCommandExit(t, logs, done)
	// The same installation/authority reopens, retaining the committed write.
	cmd, logs, done = runtimeCommand(t, args)
	address = runtimeAddress(t, logs, done)
	settings = commandRequest(t, address, "GET", "/api/settings", nil, 200)
	if err = json.Unmarshal(settings, &configuration); err != nil {
		t.Fatal(err)
	}
	if configuration["debugModelIO"] != true {
		t.Fatal("restart lost accepted write")
	}
	if err = cmd.Process.Signal(os.Interrupt); err != nil {
		t.Fatal(err)
	}
	waitCommandExit(t, logs, done)
}
func TestRuntimePreviewNativePreflightFailureDoesNotEnroll(t *testing.T) {
	source := os.Getenv("HE_SOURCE_ROOT")
	images := os.Getenv("HE_IMAGE_LIBRARY")
	if source == "" || images == "" {
		t.Fatal("real native dependency paths required")
	}
	node, err := exec.LookPath("node")
	if err != nil {
		t.Fatal(err)
	}
	for _, dependency := range []string{"node", "vector"} {
		t.Run(dependency, func(t *testing.T) {
			root := t.TempDir()
			pluginRoot := filepath.Join(root, "plugins")
			if err := os.Mkdir(pluginRoot, 0700); err != nil {
				t.Fatal(err)
			}
			config := runtimeConfig{authority: filepath.Join(root, "authority"), vector: filepath.Join(source, "node_modules/sqlite-vec-linux-x64/vec0.so"), images: images, node: node, pluginRoot: pluginRoot, timezone: "UTC", locale: "en-US"}
			want := "runtime_preview_node_required"
			if dependency == "node" {
				config.node = filepath.Join(root, "missing-node")
			} else {
				config.vector = filepath.Join(root, "missing-vector.so")
				want = "runtime_preview_sqlite_vec_invalid"
			}
			err := runRuntimePreview(context.Background(), filepath.Join(root, "live/main.sqlite"), "127.0.0.1:0", config, io.Discard, io.Discard)
			if err == nil || err.Error() != want {
				t.Fatalf("native preflight: %v, want %s", err, want)
			}
			entries, err := os.ReadDir(root)
			if err != nil || len(entries) != 1 || entries[0].Name() != "plugins" {
				t.Fatalf("dependency failure enrolled installation: %v %v", entries, err)
			}
		})
	}
}

func TestRuntimePreviewUnauthorizedHTTP(t *testing.T) {
	cmd, logs, done := runtimeCommand(t, launcherArgs(t))
	address := runtimeAddress(t, logs, done)
	client := &http.Client{Timeout: 10 * time.Second}
	response, err := client.Get("http://" + address + "/api/settings")
	if err != nil {
		t.Fatal(err)
	}
	_, err = io.Copy(io.Discard, response.Body)
	response.Body.Close()
	if err != nil || response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthorized request: %d %v", response.StatusCode, err)
	}
	commandRequest(t, address, "GET", "/api/settings", nil, 200)
	if err = cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	waitCommandExit(t, logs, done)
}

// Required safety acceptance against the installation owner's retained writer
// lease. Independent ports must not admit a second graph for this installation.
// Keep this assertion strict: a second live graph is a publication blocker.
func TestRuntimePreviewSecondWriterRejectedFirstRemainsUsable(t *testing.T) {
	args := launcherArgs(t)
	first, logs, done := runtimeCommand(t, args)
	address := runtimeAddress(t, logs, done)
	commandRequest(t, address, "GET", "/api/settings", nil, 200)
	// Both commands bind independent ephemeral ports. Only the exact installation
	// and authority paths are shared, so socket contention cannot pass this test.
	second, secondLogs, secondDone := runtimeCommand(t, args)
	deadline := time.NewTimer(30 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
	rejected := false
	for !rejected {
		if strings.Contains(secondLogs.snapshot(), `"code":"server.listening"`) {
			_ = second.Process.Signal(syscall.SIGTERM)
			waitCommandExit(t, secondLogs, secondDone)
			commandRequest(t, address, "GET", "/api/settings", nil, 200)
			if err := first.Process.Signal(syscall.SIGTERM); err != nil {
				t.Fatal(err)
			}
			waitCommandExit(t, logs, done)
			t.Fatal("second writable graph admitted the same installation on a different port; owner-level exclusivity missing")
		}
		select {
		case err := <-secondDone:
			if err == nil {
				t.Fatal("second writer exited successfully instead of refusing ownership")
			}
			if !strings.Contains(secondLogs.snapshot(), "runtime_preview_open_failed") {
				t.Fatalf("second writer failed outside installation ownership: %s", secondLogs.snapshot())
			}
			rejected = true
		case <-deadline.C:
			t.Fatal("second writer neither refused nor completed startup")
		case <-ticker.C:
		}
	}
	commandRequest(t, address, "GET", "/api/settings", nil, 200)
	if err := first.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	waitCommandExit(t, logs, done)
}
