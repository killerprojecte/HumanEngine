package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// The helper runs the actual command and signal handler in a separate process,
// with the test executable's native build profile. No alternate graph or health
// endpoint is substituted for app.Open.
func TestRuntimePreviewProcess(t *testing.T) {
	if os.Getenv("HE_RUNTIME_COMMAND_HELPER") != "1" {
		return
	}
	var args []string
	if json.Unmarshal([]byte(os.Getenv("HE_RUNTIME_COMMAND_ARGS")), &args) != nil {
		os.Exit(2)
	}
	flag.CommandLine = flag.NewFlagSet("humanengine", flag.ContinueOnError)
	os.Args = append([]string{"humanengine"}, args...)
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	os.Exit(0)
}

type commandLog struct {
	sync.Mutex
	buffer bytes.Buffer
}

func (b *commandLog) Write(p []byte) (int, error) {
	b.Lock()
	defer b.Unlock()
	return b.buffer.Write(p)
}
func (b *commandLog) snapshot() string { b.Lock(); defer b.Unlock(); return b.buffer.String() }
func runtimeCommand(t *testing.T, args []string) (*exec.Cmd, *commandLog, <-chan error) {
	t.Helper()
	raw, err := json.Marshal(args)
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestRuntimePreviewProcess$")
	cmd.Env = append(os.Environ(), "HE_RUNTIME_COMMAND_HELPER=1", "HE_RUNTIME_COMMAND_ARGS="+string(raw), "HUMANENGINE_ADMIN_TOKEN=synthetic-launcher-token", "HUMANENGINE_PLUGINS=")
	logs := new(commandLog)
	cmd.Stdout = logs
	cmd.Stderr = logs
	if err = cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	t.Cleanup(func() { _ = cmd.Process.Kill() })
	return cmd, logs, done
}
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
		var event struct {
			Code   string `json:"code"`
			Fields struct {
				Address string `json:"address"`
			} `json:"fields"`
		}
		if json.Unmarshal([]byte(line), &event) == nil && event.Code == "server.listening" {
			return event.Fields.Address
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
func TestRuntimePreviewBindFailureDoesNotEnroll(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	root := t.TempDir()
	database := filepath.Join(root, "live/main.sqlite")
	err = runRuntimePreview(context.Background(), database, listener.Addr().String(), runtimeConfig{}, io.Discard, io.Discard)
	if err == nil || err.Error() != "runtime_preview_listen_failed" {
		t.Fatalf("bind result %v", err)
	}
	entries, err := os.ReadDir(root)
	if err != nil || len(entries) != 0 {
		t.Fatalf("failed bind mutated installation: %v %v", entries, err)
	}
}
func TestRuntimePreviewModeConflictsAndDefaultRefusal(t *testing.T) {
	for _, test := range []struct {
		args []string
		want string
	}{
		{nil, "Go migration is incomplete"},
		{[]string{"--runtime-preview", "--read-only-preview"}, "runtime_preview_mode_conflict"},
		{[]string{"--runtime-preview", "--inspect-builtins"}, "runtime_preview_mode_conflict"},
		{[]string{"--runtime-preview", "unexpected"}, "runtime_preview_unexpected_arguments"},
	} {
		_, logs, done := runtimeCommand(t, test.args)
		select {
		case err := <-done:
			if err == nil {
				t.Fatal("unexpected success")
			}
		case <-time.After(10 * time.Second):
			t.Fatal("command did not refuse")
		}
		if !strings.Contains(logs.snapshot(), test.want) {
			t.Fatalf("missing refusal %s: %s", test.want, logs.snapshot())
		}
	}
}
func TestRuntimePreviewPreflightFailureDoesNotEnroll(t *testing.T) {
	root := t.TempDir()
	database := filepath.Join(root, "live/main.sqlite")
	err := runRuntimePreview(context.Background(), database, "127.0.0.1:0", runtimeConfig{authority: filepath.Join(root, "live/authority")}, io.Discard, io.Discard)
	if err == nil || err.Error() != "runtime_preview_authority_invalid" {
		t.Fatalf("authority result %v", err)
	}
	entries, err := os.ReadDir(root)
	if err != nil || len(entries) != 0 {
		t.Fatalf("failed preflight mutated installation: %v %v", entries, err)
	}
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
