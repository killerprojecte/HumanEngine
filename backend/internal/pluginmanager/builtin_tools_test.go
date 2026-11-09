package pluginmanager

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/killerprojecte/HumanEngine/backend/internal/botruntime"
	"github.com/killerprojecte/HumanEngine/backend/internal/dispatch"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsdate"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/plugins"
	"github.com/killerprojecte/HumanEngine/backend/internal/replyplan"
	"github.com/killerprojecte/HumanEngine/backend/internal/toolresults"
)

// Only the source-validation observation is synthetic in this focused bridge
// test. Manager configuration/queue/disable and cache files are genuine owners.
// Actual installed application authority is covered separately by app tests.
type builtinTestAuthority struct{ botruntime.Authority }

func (*builtinTestAuthority) ValidateSources(botruntime.Scene, []*jsjson.Object, []string) (bool, error) {
	return true, nil
}

func builtinToolManager(t *testing.T) (*Manager, *item, context.Context, *botruntime.TurnContext) {
	t.Helper()
	m, host, _ := queueManager(t)
	dates, err := jsdate.New("UTC")
	if err != nil {
		t.Fatal(err)
	}
	policy, err := replyplan.New(func(a, b string) (int, error) { return strings.Compare(a, b), nil }, dates)
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := botruntime.New(botruntime.DefaultSettings(), botruntime.Services{Authority: &builtinTestAuthority{}, Dates: dates, Policy: policy, Models: botruntime.ModelFunc(func(context.Context, botruntime.ModelRequest, func(botruntime.Completion, error)) {
		t.Error("unexpected model")
	})})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(runtime.Close)
	m.runtime = runtime
	wasm, err := plugins.NewWASMHost(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = wasm.Close(context.Background()) })
	if err = m.WithWASM(wasm); err != nil {
		t.Fatal(err)
	}
	life, cancel := context.WithCancel(context.Background())
	tail := make(chan struct{})
	close(tail)
	i := &item{builtin: true, ctx: life, cancel: cancel, revision: 1, desired: true, enabled: true, tail: tail}
	m.items["web-read"] = i
	m.order = append(m.order, "web-read")
	if host.PID() != 0 {
		t.Fatal("builtin bridge started Node")
	}
	turn := &botruntime.TurnContext{Mode: "live", Scene: botruntime.Scene{SceneRef: dispatch.SceneRef{ID: "scene", AdapterID: "adapter", AccountID: "account", PlatformSceneID: "room"}}}
	return m, i, life, turn
}

const builtinReadDefinition = `[{"name":"web-read.read","effect":"read","inputSchema":{"type":"object"}}]`

func builtinExtension(t *testing.T, m *Manager, i *item, life context.Context, raw string, run BuiltinToolExecutor) *botruntime.Extension {
	t.Helper()
	ext, _, err := m.installBuiltinTools(BuiltinRuntimeInput{ID: "web-read", Revision: i.revision, Life: life}, json.RawMessage(raw), run)
	if err != nil {
		t.Fatal(err)
	}
	return ext
}
func invokeBuiltin(t *testing.T, ext *botruntime.Extension, ctx context.Context, name string, turn *botruntime.TurnContext) (any, error) {
	t.Helper()
	type answer struct {
		value any
		err   error
	}
	out := make(chan answer, 1)
	ext.Execute(ctx, name, new(jsjson.Object), turn, func(v any, e error) { out <- answer{v, e} })
	select {
	case a := <-out:
		return a.value, a.err
	case <-time.After(5 * time.Second):
		t.Fatal("builtin execution did not settle")
		return nil, nil
	}
}
func TestBuiltinToolsSimulationAndRetainedGeneration(t *testing.T) {
	m, i, life, turn := builtinToolManager(t)
	var calls atomic.Int32
	run := func(context.Context, string, json.RawMessage, func(context.Context, toolresults.Source, toolresults.Options) (toolresults.Result, error)) (json.RawMessage, error) {
		calls.Add(1)
		return json.RawMessage(`{"ok":true}`), nil
	}
	ext := builtinExtension(t, m, i, life, `[{"name":"web-read.send","effect":"learningCandidate","inputSchema":{"type":"object"}}]`, run)
	turn.Mode = "simulation"
	value, err := invokeBuiltin(t, ext, context.Background(), "web-read.send", turn)
	if err != nil || text(value.(*jsjson.Object), "error") != "Simulation does not execute plugin side effects" || calls.Load() != 0 {
		t.Fatal(value, err, calls.Load())
	}
	turn.Mode = "live"
	if _, err = invokeBuiltin(t, ext, context.Background(), "web-read.send", turn); err != nil || calls.Load() != 1 {
		t.Fatal(err, calls.Load())
	}
	m.mu.Lock()
	i.cancel()
	i.revision++
	i.ctx, i.cancel = context.WithCancel(context.Background())
	m.mu.Unlock()
	if _, err = invokeBuiltin(t, ext, context.Background(), "web-read.send", turn); err == nil || calls.Load() != 1 {
		t.Fatal("retained tool entered replacement", err, calls.Load())
	}
}
func TestBuiltinToolsRegistrationFailureIsClosed(t *testing.T) {
	m, i, life, _ := builtinToolManager(t)
	for _, raw := range []string{`{"error":"unknown_plugin_operation"}`, `[]`, `[null]`, `[{"name":"foreign.read","effect":"read","inputSchema":{}}]`, `[{"name":"web-read.read","effect":"send","inputSchema":{}}]`, `[{"name":"web-read.read","effect":"read","inputSchema":{}},{"name":"web-read.read","effect":"read","inputSchema":{}}]`} {
		if _, err := m.builtinToolExtension("web-read", i, 1, life, json.RawMessage(raw), nil, new(builtinToolWork)); err == nil {
			t.Fatal(raw)
		}
	}
}
func TestBuiltinToolsOwnerCacheFinalizationAndDisable(t *testing.T) {
	m, i, life, turn := builtinToolManager(t)
	raw, _ := json.Marshal(map[string]any{"data": strings.Repeat("x", 60*1024)})
	ext := builtinExtension(t, m, i, life, builtinReadDefinition, func(context.Context, string, json.RawMessage, func(context.Context, toolresults.Source, toolresults.Options) (toolresults.Result, error)) (json.RawMessage, error) {
		return raw, nil
	})
	value, err := invokeBuiltin(t, ext, context.Background(), "web-read.read", turn)
	if err != nil {
		t.Fatal(err)
	}
	id := text(value.(*jsjson.Object), "resultId")
	if id == "" {
		t.Fatal("read was not finalized into existing cache", value)
	}
	if _, err = m.cache.Read(context.Background(), toolresults.ReadInput{ResultID: id}, "different-scene", nil); !errors.Is(err, toolresults.ErrNotFound) {
		t.Fatal("foreign cache scope", err)
	}
	var body strings.Builder
	offset := float64(0)
	for {
		view, e := m.cache.Read(context.Background(), toolresults.ReadInput{ResultID: id, Offset: offset, Full: true}, scope(turn), nil)
		if e != nil {
			t.Fatal(e)
		}
		body.WriteString(view.Content)
		if view.NextOffset == nil {
			break
		}
		offset = float64(*view.NextOffset)
	}
	if body.String() != string(raw) {
		t.Fatal("finalized result did not retain complete bytes")
	}
	if err = m.Disable(context.Background(), "web-read", "disabled"); err != nil {
		t.Fatal(err)
	}
	if _, err = m.cache.Read(context.Background(), toolresults.ReadInput{ResultID: id}, scope(turn), nil); !errors.Is(err, toolresults.ErrNotFound) {
		t.Fatal("plugin cache survived disable", err)
	}
	if _, err = invokeBuiltin(t, ext, context.Background(), "web-read.read", turn); err == nil {
		t.Fatal("disabled closure succeeded")
	}
}
func TestBuiltinToolsCacheUsesOriginalOwnerAndLiveGuard(t *testing.T) {
	m, i, life, turn := builtinToolManager(t)
	var valid atomic.Bool
	valid.Store(true)
	m.runtime.AddExtension(&botruntime.Extension{CaptureToolResultGuards: func(*botruntime.TurnContext) []*botruntime.ToolResultGuard {
		return []*botruntime.ToolResultGuard{{Check: func(botruntime.ReadAuthority) (bool, error) { return valid.Load(), nil }}}
	}})
	var id string
	ext := builtinExtension(t, m, i, life, builtinReadDefinition, func(ctx context.Context, _ string, _ json.RawMessage, cache func(context.Context, toolresults.Source, toolresults.Options) (toolresults.Result, error)) (json.RawMessage, error) {
		result, err := cache(ctx, toolresults.Text("complete raw body"), toolresults.Options{Encoding: toolresults.UTF8})
		if err != nil {
			return nil, err
		}
		id = result.ResultID
		return json.Marshal(result)
	})
	if _, err := invokeBuiltin(t, ext, context.Background(), "web-read.read", turn); err != nil {
		t.Fatal(err)
	}
	view, err := m.cache.Read(context.Background(), toolresults.ReadInput{ResultID: id}, scope(turn), nil)
	if err != nil || view.Content != "complete raw body" {
		t.Fatal(view, err)
	}
	valid.Store(false)
	if _, err = m.cache.Read(context.Background(), toolresults.ReadInput{ResultID: id}, scope(turn), nil); !errors.Is(err, toolresults.ErrInvalidated) {
		t.Fatal("lost original live guard", err)
	}
}
func TestBuiltinToolsInFlightCacheAndLateResultCannotSurviveDisable(t *testing.T) {
	m, i, life, turn := builtinToolManager(t)
	entered, release := make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	releaseAll := func() { releaseOnce.Do(func() { close(release) }) }
	defer releaseAll()
	source := toolresults.NextFunc(func(context.Context) ([]byte, error) {
		close(entered)
		<-release
		return nil, errors.New("synthetic_end")
	})
	ext := builtinExtension(t, m, i, life, builtinReadDefinition, func(_ context.Context, _ string, _ json.RawMessage, cache func(context.Context, toolresults.Source, toolresults.Options) (toolresults.Result, error)) (json.RawMessage, error) {
		// A detached callback is deliberately adversarial. The manager must still
		// attach its write to the original generation and reject the late result.
		_, _ = cache(context.Background(), source, toolresults.Options{})
		return json.RawMessage(`{"ok":true}`), nil
	})
	out := make(chan error, 1)
	ext.Execute(context.Background(), "web-read.read", new(jsjson.Object), turn, func(_ any, e error) { out <- e })
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("cache never entered source")
	}
	if err := m.Disable(context.Background(), "web-read", "stop"); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-out:
		if err == nil {
			t.Fatal("late result was accepted")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("cancelled cache did not settle")
	}
	releaseAll()
}
