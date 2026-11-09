package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
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
