// Package plugins manages actual loaded modules, not enabled flags around dormant callbacks.
package plugins

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sync"

	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/api"
	"github.com/tetratelabs/wazero/imports/wasi_snapshot_preview1"
)

var ErrNotLoaded = errors.New("plugin_not_loaded")

type wasmInstance struct {
	calls      chan struct{}
	done       chan struct{}
	stopped    context.Context
	cancel     context.CancelFunc
	cancelLoad context.CancelFunc
	module     api.Module
	compiled   wazero.CompiledModule
	err        error
	unloadOnce sync.Once
	unloaded   chan struct{}
	unloadErr  error
}
type WASMHost struct {
	mu        sync.Mutex
	runtime   wazero.Runtime
	instances map[string]*wasmInstance
	closed    bool
	closeDone chan struct{}
	closeErr  error
}

func NewWASMHost(ctx context.Context) (*WASMHost, error) {
	runtime := wazero.NewRuntimeWithConfig(ctx, wazero.NewRuntimeConfig().WithCloseOnContextDone(true))
	if _, err := wasi_snapshot_preview1.Instantiate(ctx, runtime); err != nil {
		runtime.Close(ctx)
		return nil, err
	}
	return &WASMHost{runtime: runtime, instances: map[string]*wasmInstance{}, closeDone: make(chan struct{})}, nil
}
func (h *WASMHost) Enable(ctx context.Context, id string, artifact []byte) error {
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		h.mu.Lock()
		if h.closed {
			h.mu.Unlock()
			return errors.New("plugin_host_closed")
		}
		if previous := h.instances[id]; previous != nil {
			stopping := previous.stopped.Err() != nil
			select {
			case <-previous.done:
				stopping = stopping || previous.module != nil && previous.module.IsClosed()
			default:
			}
			h.mu.Unlock()
			if stopping {
				// Keep the name reserved until teardown completes. An explicit enable
				// may replace a cancelled guest, but must not race its old instance.
				go h.disableInstance(context.Background(), id, previous)
				select {
				case <-ctx.Done():
					return ctx.Err()
				case <-previous.unloaded:
					continue
				}
			}
			return errors.New("plugin_already_loaded")
		}
		stopped, cancel := context.WithCancel(context.Background())
		loading, cancelLoad := context.WithCancel(ctx)
		instance := &wasmInstance{calls: make(chan struct{}, 1), done: make(chan struct{}), stopped: stopped, cancel: cancel, cancelLoad: cancelLoad, unloaded: make(chan struct{})}
		instance.calls <- struct{}{}
		h.instances[id] = instance
		h.mu.Unlock()
		// Compilation and guest initialization never hold the registry lock: disable
		// can cancel either phase, including a guest stuck inside _initialize.
		instance.compiled, instance.err = h.runtime.CompileModule(loading, artifact)
		if instance.err == nil {
			instance.module, instance.err = h.runtime.InstantiateModule(loading, instance.compiled, wazero.NewModuleConfig().WithName(id).WithStartFunctions("_initialize"))
		}
		if instance.err == nil {
			signatures := map[string]struct{ params, results []api.ValueType }{
				"alloc":       {[]api.ValueType{api.ValueTypeI32}, []api.ValueType{api.ValueTypeI32}},
				"free":        {[]api.ValueType{api.ValueTypeI32}, nil},
				"plugin_call": {[]api.ValueType{api.ValueTypeI32, api.ValueTypeI32}, []api.ValueType{api.ValueTypeI64}},
			}
			if instance.module.Memory() == nil {
				instance.err = errors.New("plugin ABI memory missing")
			}
			for name, want := range signatures {
				fn := instance.module.ExportedFunction(name)
				if fn == nil {
					instance.err = fmt.Errorf("plugin ABI export missing: %s", name)
					break
				}
				got := fn.Definition()
				if !sameTypes(got.ParamTypes(), want.params) || !sameTypes(got.ResultTypes(), want.results) {
					instance.err = fmt.Errorf("plugin ABI signature invalid: %s", name)
					break
				}
			}
		}

		close(instance.done)
		cancelLoad()
		h.mu.Lock()
		current := h.instances[id] == instance && !h.closed && instance.stopped.Err() == nil
		h.mu.Unlock()
		if !current {
			return ErrNotLoaded
		}
		if instance.err != nil {
			_ = h.disableInstance(context.Background(), id, instance)
			return instance.err
		}
		return nil
	}
}
func (h *WASMHost) Loaded(id string) bool {
	h.mu.Lock()
	instance := h.instances[id]
	h.mu.Unlock()
	if instance == nil {
		return false
	}
	select {
	case <-instance.done:
		return instance.stopped.Err() == nil && instance.err == nil && instance.module != nil && !instance.module.IsClosed()
	default:
		return false
	}
}

// BindCall captures an instance capability, not a movable plugin ID.
// A retained callback cannot invoke or cancel a replacement guest.
func (h *WASMHost) BindCall(id string) (func(context.Context, []byte) ([]byte, error), error) {
	h.mu.Lock()
	instance := h.instances[id]
	valid := !h.closed && instance != nil && instance.stopped.Err() == nil
	h.mu.Unlock()
	if !valid {
		return nil, ErrNotLoaded
	}
	return func(ctx context.Context, input []byte) ([]byte, error) {
		return h.callInstance(ctx, id, instance, input)
	}, nil
}
func (h *WASMHost) Call(ctx context.Context, id string, input []byte) ([]byte, error) {
	return h.callInstance(ctx, id, nil, input)
}
func (h *WASMHost) callInstance(ctx context.Context, id string, expected *wasmInstance, input []byte) (output []byte, resultErr error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	h.mu.Lock()
	instance := h.instances[id]
	valid := !h.closed && instance != nil && (expected == nil || expected == instance)
	h.mu.Unlock()
	if !valid {
		return nil, ErrNotLoaded
	}
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-instance.stopped.Done():
		return nil, ErrNotLoaded
	case <-instance.done:
	}
	if instance.err != nil {
		return nil, instance.err
	}
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-instance.stopped.Done():
		return nil, ErrNotLoaded
	case <-instance.calls:
	}
	defer func() {
		// A cancelled call can close its own module. Release physical call
		// ownership before joining retirement, which also waits on this gate.
		instance.calls <- struct{}{}
		if instance.module.IsClosed() {
			resultErr = errors.Join(resultErr, h.disableInstance(context.Background(), id, instance))
		}
		if resultErr != nil {
			output = nil
		}
	}()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if instance.stopped.Err() != nil || instance.module.IsClosed() {
		return nil, ErrNotLoaded
	}
	// This is the wasm32 address width, not a configurable resource/admission limit.
	if uint64(len(input)) > math.MaxUint32 {
		return nil, errors.New("plugin_input_exceeds_wasm32_address_space")
	}
	callContext, cancel := context.WithCancel(ctx)
	detach := context.AfterFunc(instance.stopped, cancel)
	defer func() { detach(); cancel() }()
	var allocations []uint32
	defer func() {
		// free is guest code too. It must share caller/lifecycle cancellation,
		// and a failing cleanup must not turn a cancelled call into success.
		for i := len(allocations) - 1; i >= 0; i-- {
			_, err := instance.module.ExportedFunction("free").Call(callContext, uint64(allocations[i]))
			resultErr = errors.Join(resultErr, err)
		}
		resultErr = errors.Join(resultErr, callContext.Err())
		if instance.stopped.Err() != nil {
			resultErr = errors.Join(resultErr, ErrNotLoaded)
		}
		if resultErr != nil {
			output = nil
		}
	}()
	allocated, err := instance.module.ExportedFunction("alloc").Call(callContext, uint64(len(input)))
	if err != nil {
		return nil, err
	}
	ptr := uint32(allocated[0])
	allocations = append(allocations, ptr)
	if !instance.module.Memory().Write(ptr, input) {
		return nil, errors.New("plugin_input_memory_invalid")
	}
	response, err := instance.module.ExportedFunction("plugin_call").Call(callContext, uint64(ptr), uint64(len(input)))
	if err != nil {
		return nil, err
	}
	outPtr, outLen := uint32(response[0]>>32), uint32(response[0])
	allocations = append(allocations, outPtr)
	memory, ok := instance.module.Memory().Read(outPtr, outLen)
	if !ok {
		return nil, errors.New("plugin_output_memory_invalid")
	}
	return append([]byte(nil), memory...), nil
}
func (h *WASMHost) Disable(ctx context.Context, id string) error {
	h.mu.Lock()
	instance := h.instances[id]
	h.mu.Unlock()
	if instance == nil {
		return nil
	}
	return h.disableInstance(ctx, id, instance)
}
func (h *WASMHost) disableInstance(ctx context.Context, id string, instance *wasmInstance) error {
	instance.unloadOnce.Do(func() {
		instance.cancel()
		instance.cancelLoad()
		<-instance.done
		// Module.Close alone does not join an imported host function. Keep the
		// ID and compiled code owned until the active call and all guest cleanup
		// have physically returned. Stopped queued callers cannot enter again.
		<-instance.calls
		if instance.module != nil {
			instance.unloadErr = instance.module.Close(ctx)
		}
		if instance.compiled != nil {
			instance.unloadErr = errors.Join(instance.unloadErr, instance.compiled.Close(ctx))
		}
		h.mu.Lock()
		if h.instances[id] == instance {
			delete(h.instances, id)
		}
		h.mu.Unlock()
		close(instance.unloaded)
	})
	return instance.unloadErr
}
func (h *WASMHost) Close(ctx context.Context) error {
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		<-h.closeDone
		return h.closeErr
	}
	h.closed = true
	instances := make(map[string]*wasmInstance, len(h.instances))
	for id, instance := range h.instances {
		instances[id] = instance
		instance.cancel()
		instance.cancelLoad()
	}
	h.mu.Unlock()
	var err error
	for id, instance := range instances {
		err = errors.Join(err, h.disableInstance(ctx, id, instance))
	}
	h.closeErr = errors.Join(err, h.runtime.Close(ctx))
	close(h.closeDone)
	return h.closeErr
}

func sameTypes(a, b []api.ValueType) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
