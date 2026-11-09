package pluginmanager

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"

	"github.com/killerprojecte/HumanEngine/backend/internal/botruntime"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/quiescence"
	"github.com/killerprojecte/HumanEngine/backend/internal/toolresults"
)

// BuiltinToolExecutor runs a plugin-owned continuation. The manager supplies its
// existing turn-scoped cache capability, never a standalone plugin cache.
// The factory's BuiltinLifetime must join physical work after Execute settles.
type BuiltinToolExecutor func(context.Context, string, json.RawMessage, func(context.Context, toolresults.Source, toolresults.Options) (toolresults.Result, error)) (json.RawMessage, error)

// InstallBuiltinTools installs guest-produced definitions for this exact manager
// generation. It shares V1 ownership, simulation and finalization rules without
// creating a V1 worker or moving plugin policy into the manager.
func (m *Manager) InstallBuiltinTools(in BuiltinRuntimeInput, raw json.RawMessage, execute BuiltinToolExecutor) (func(), error) {
	_, remove, err := m.installBuiltinTools(in, raw, execute)
	return remove, err
}

// The bridge owns the full manager continuation, including owner capture and
// automatic result finalization after the plugin's Execute lease has finished.
// Each cache callback also has a lease because physical service work can outlive
// its logical result. The plugin factory still owns actual stream/DNS completion.
type builtinToolWork struct {
	work   quiescence.Group
	remove func()
	once   sync.Once
}

func (b *builtinToolWork) Dispose() {
	b.once.Do(func() {
		b.work.Seal()
		if b.remove != nil {
			b.remove()
		}
	})
}
func (b *builtinToolWork) CloseAndWait(ctx context.Context) error {
	b.Dispose()
	return b.work.Wait(ctx)
}

func (m *Manager) installBuiltinTools(in BuiltinRuntimeInput, raw json.RawMessage, execute BuiltinToolExecutor) (*botruntime.Extension, func(), error) {
	m.mu.Lock()
	i := m.items[in.ID]
	valid := i != nil && i.builtin && i.ctx == in.Life && i.revision == in.Revision && i.desired && !m.closed
	m.mu.Unlock()
	if !valid || in.Life == nil || in.Life.Err() != nil || execute == nil {
		return nil, nil, NewStartupError("plugin_disabled", nil)
	}
	bridge := new(builtinToolWork)
	extension, err := m.builtinToolExtension(in.ID, i, in.Revision, in.Life, raw, execute, bridge)
	if err != nil {
		return nil, nil, err
	}
	bridge.remove = m.runtime.AddExtension(extension)
	m.mu.Lock()
	valid = i.ctx == in.Life && i.revision == in.Revision && i.desired && !m.closed && in.Life.Err() == nil
	if valid {
		i.cleanups = append(i.cleanups, bridge.Dispose)
		i.physicalCleanups = append(i.physicalCleanups, bridge.CloseAndWait)
	}
	m.mu.Unlock()
	if !valid {
		return nil, nil, errors.Join(NewStartupError("plugin_disabled", nil), bridge.CloseAndWait(context.Background()))
	}
	return extension, bridge.Dispose, nil
}

func (m *Manager) builtinToolExtension(id string, i *item, revision uint64, life context.Context, raw json.RawMessage, execute BuiltinToolExecutor, bridge *builtinToolWork) (*botruntime.Extension, error) {
	value, err := jsjson.ParseBytes(raw)
	if err != nil {
		return nil, err
	}
	rows, ok := value.([]any)
	if !ok || len(rows) == 0 {
		return nil, errors.New("invalid_builtin_tool_registrations")
	}
	tools := map[string]*jsjson.Object{}
	definitions := make([]*jsjson.Object, 0, len(rows))
	for _, row := range rows {
		tool, ok := row.(*jsjson.Object)
		if !ok {
			return nil, errors.New("invalid_builtin_tool_registration")
		}
		name, effect := text(tool, "name"), text(tool, "effect")
		// This bridge exposes only the existing read/planned-action continuation.
		// Sending chat messages requires a different, explicitly bound capability.
		if !strings.HasPrefix(name, id+".") || name == id+"." || tools[name] != nil || effect != "read" && effect != "learningCandidate" {
			return nil, errors.New("invalid_builtin_tool_registration")
		}
		if _, ok := prop(tool, "inputSchema").(*jsjson.Object); !ok {
			return nil, errors.New("invalid_builtin_tool_registration")
		}
		tools[name] = clone(tool)
		definitions = append(definitions, clone(tool))
	}
	return &botruntime.Extension{Tools: definitions, Execute: func(ctx context.Context, name string, input any, turn *botruntime.TurnContext, settle func(any, error)) {
		physicalDone, ok := bridge.work.Start()
		if !ok {
			settle(nil, NewStartupError("plugin_disabled", nil))
			return
		}
		done, ok := m.work.Start()
		if !ok {
			physicalDone()
			settle(nil, NewStartupError("plugin_disabled", nil))
			return
		}
		go func() {
			defer done()
			defer physicalDone()
			value, err := m.executeBuiltinTool(ctx, id, i, revision, life, tools[name], input, turn, execute, bridge)
			settle(value, err)
		}()
	}}, nil
}

func (m *Manager) executeBuiltinTool(ctx context.Context, id string, i *item, revision uint64, life context.Context, tool *jsjson.Object, input any, turn *botruntime.TurnContext, execute BuiltinToolExecutor, bridge *builtinToolWork) (any, error) {
	if life.Err() != nil || !current(m, i, revision) {
		return nil, NewStartupError("plugin_disabled", nil)
	}
	if tool == nil {
		return nil, errors.New("tool_missing")
	}
	if turn == nil {
		return nil, errors.New("tool_turn_missing")
	}
	if turn.Mode == "simulation" && text(tool, "effect") == "learningCandidate" {
		return jsjson.NewObject(jsjson.Member{Key: "error", Value: "Simulation does not execute plugin side effects"})
	}
	combined, cancel := lifetime(ctx, life)
	defer cancel()
	raw, err := wire(input)
	if err != nil {
		return nil, err
	}
	generation := toolresults.NewGeneration(life)
	cache := func(ctx context.Context, source toolresults.Source, options toolresults.Options) (toolresults.Result, error) {
		done, ok := bridge.work.Start()
		if !ok {
			return toolresults.Result{}, toolresults.ErrCancelled
		}
		defer done()
		if combined.Err() != nil || !current(m, i, revision) {
			return toolresults.Result{}, toolresults.ErrCancelled
		}
		// Even a callback accidentally using Background cannot detach a cache write
		// from this call, plugin disable, or hot-configuration generation revocation.
		cacheLife, stop := lifetime(ctx, combined)
		defer stop()
		owner := m.owner(turn, id)
		owner.Generation = generation
		return m.cache.Store(cacheLife, source, owner, options)
	}
	result, err := execute(combined, text(tool, "name"), raw, cache)
	if err != nil {
		return nil, err
	}
	if combined.Err() != nil || !current(m, i, revision) {
		return nil, NewStartupError("plugin_disabled", nil)
	}
	parsed, err := jsjson.ParseBytes(result)
	if err != nil {
		return nil, err
	}
	resultValue, err := m.finalizeToolResultForGeneration(combined, id, tool, parsed, turn, generation)
	if err != nil {
		return nil, err
	}
	if combined.Err() != nil || !current(m, i, revision) {
		return nil, NewStartupError("plugin_disabled", nil)
	}
	return resultValue, nil
}
