//go:build linux && sqlite_fts5 && humanengine_icu

package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
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
			Code       string `json:"code"`
			Address    string `json:"address"`
			GoMaxProcs int    `json:"gomaxprocs"`
		}
		if json.Unmarshal([]byte(flat), &event) == nil && event.Code == "server.listening" {
			if prefix == "" || event.Address == "" || event.GoMaxProcs < 1 {
				t.Fatal("source listening event lacks prefix, address, or effective Go parallelism", line)
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
