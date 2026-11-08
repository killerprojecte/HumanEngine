package plugins

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func toolFixture(t *testing.T, h *V1Host, effect string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "tool.mjs")
	source := `export default {manifest:{id:'tool-fixture',name:'Tool fixture',version:'1',configSchema:{type:'object'},capabilities:[]},setup(ctx){ctx.registerTool({name:'tool-fixture.send',effect:'` + effect + `',inputSchema:{type:'object',additionalProperties:true},async execute(input,turn){return await turn.send(input)}})}}`
	if err := os.WriteFile(path, []byte(source), 0600); err != nil {
		t.Fatal(err)
	}
	raw, err := h.Enable(context.Background(), "tool-fixture", path, map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	var ready struct{ Registrations []struct{ Handle string } }
	if err = json.Unmarshal(raw, &ready); err != nil {
		t.Fatal(err)
	}
	return ready.Registrations[0].Handle
}
func TestToolNativeSendRoundTripAndEffect(t *testing.T) {
	for _, effect := range []string{"send", "read"} {
		t.Run(effect, func(t *testing.T) {
			h := NewV1Host("node", io.Discard)
			defer h.Close(context.Background())
			handle := toolFixture(t, h, effect)
			called := false
			result, err := h.CallTool(context.Background(), "tool-fixture", handle, []any{map[string]any{"value": "hello"}, map[string]any{}}, ToolServices{Send: func(_ context.Context, input json.RawMessage) (json.RawMessage, error) {
				called = true
				if string(input) != `{"value":"hello"}` {
					t.Errorf("input %s", input)
				}
				return json.RawMessage(`[{"status":"sent"}]`), nil
			}})
			if effect == "send" {
				if err != nil || !called || string(result) != `[{"status":"sent"}]` {
					t.Fatal(string(result), err, called)
				}
			} else if err == nil || called {
				t.Fatal("read effect acquired send", err, called)
			}
		})
	}
}
func TestToolUnloadWaitsPhysicalNativeCallbackAfterNodeExit(t *testing.T) {
	h := NewV1Host("node", io.Discard)
	handle := toolFixture(t, h, "send")
	pid := h.PID()
	entered := make(chan struct{})
	release := make(chan struct{})
	released := false
	defer func() {
		if !released {
			close(release)
		}
		h.Close(context.Background())
	}()
	returned := make(chan error, 1)
	go func() {
		_, err := h.CallTool(context.Background(), "tool-fixture", handle, []any{map[string]any{}, map[string]any{}}, ToolServices{Send: func(context.Context, json.RawMessage) (json.RawMessage, error) {
			close(entered)
			<-release
			return json.RawMessage(`[]`), nil
		}})
		returned <- err
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("callback not admitted")
	}
	stopped := make(chan error, 1)
	go func() { stopped <- h.Disable(context.Background(), "tool-fixture") }()
	deadline := time.After(5 * time.Second)
	for syscall.Kill(pid, 0) == nil {
		select {
		case <-deadline:
			t.Fatal("real Node did not exit")
		case <-time.After(time.Millisecond):
		}
	}
	select {
	case err := <-stopped:
		t.Fatal("unload released native owner before physical callback", err)
	default:
	}
	close(release)
	released = true
	select {
	case err := <-stopped:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("unload did not drain")
	}
	select {
	case err := <-returned:
		if err == nil {
			t.Fatal("stale result published")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("tool call retained")
	}
}
