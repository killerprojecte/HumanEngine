package plugins

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestFailedOldActivationCannotAbortReplacement(t *testing.T) {
	h := NewV1Host("node", io.Discard)
	defer h.Close(context.Background())
	path := filepath.Join(t.TempDir(), "fixture.mjs")
	if err := os.WriteFile(path, []byte(`export default{manifest:{id:'stale',capabilities:[],configSchema:{type:'object'}},setup(c){c.registerAdminAction('ping',()=>42)}}`), 0600); err != nil {
		t.Fatal(err)
	}
	entered, release := make(chan struct{}), make(chan struct{})
	released := false
	defer func() {
		if !released {
			close(release)
		}
	}()
	old := make(chan error, 1)
	go func() {
		_, e := h.EnableManagedJSONWithAdmission(context.Background(), "stale", path, json.RawMessage(`{}`), "old", func(json.RawMessage) error { close(entered); <-release; return errors.New("old_install_failed") }, nil, func() bool { return true })
		old <- e
	}()
	select {
	case <-entered:
	case e := <-old:
		t.Fatal("old install returned early", e)
	case <-time.After(5 * time.Second):
		t.Fatal("old install not entered")
	}
	if err := h.Disable(context.Background(), "stale"); err != nil {
		t.Fatal(err)
	}
	raw, err := h.Enable(context.Background(), "stale", path, map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	generation, ok := h.RuntimeGeneration("stale")
	pid := h.PID()
	if !ok || pid == 0 {
		t.Fatal("replacement missing")
	}
	close(release)
	released = true
	select {
	case e := <-old:
		if e == nil {
			t.Fatal("old failed install succeeded")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("old failure self-waited")
	}
	current, ok := h.RuntimeGeneration("stale")
	if !ok || current != generation || h.PID() != pid {
		t.Fatal("old activation revoked replacement")
	}
	result, err := h.Call(context.Background(), "stale", actionHandle(t, raw, "ping"), nil)
	if err != nil || string(result) != "42" {
		t.Fatal(string(result), err)
	}
	if err = h.Disable(context.Background(), "stale"); err != nil {
		t.Fatal(err)
	}
}
