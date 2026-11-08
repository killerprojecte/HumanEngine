package plugins

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/killerprojecte/HumanEngine/backend/internal/toolresults"
	"sync"
)

// ToolServices binds a single admitted invocation to its actual native send
// owner. The worker cannot select another turn or manufacture a service lease.
// Send must return only after its physical callback has finished; cancellation
// alone does not release plugin shutdown ownership.
type ToolServices struct {
	CacheResult func(context.Context, toolresults.Source, toolresults.Options) (toolresults.Result, error)
	Send        func(context.Context, json.RawMessage) (json.RawMessage, error)
}
type toolInvocation struct {
	mu        sync.Mutex
	streams   map[string]*toolStream
	ctx       context.Context
	cancel    context.CancelFunc
	stopLease func() bool
	services  ToolServices
}

// CallTool is the native send-capability entry point for a registered V1 tool.
// It retains the existing host and worker, and never starts another Node process.
func (h *V1Host) CallTool(ctx context.Context, id, handle string, args []any, services ToolServices) (json.RawMessage, error) {
	return h.callToolGeneration(ctx, id, 0, handle, args, services)
}

// CallToolGeneration fences admission against the activation that supplied the handle.
func (h *V1Host) CallToolGeneration(ctx context.Context, id string, generation uint64, handle string, args []any, services ToolServices) (json.RawMessage, error) {
	if generation == 0 {
		return nil, ErrNotLoaded
	}
	return h.callToolGeneration(ctx, id, generation, handle, args, services)
}
func (h *V1Host) callToolGeneration(ctx context.Context, id string, generation uint64, handle string, args []any, services ToolServices) (json.RawMessage, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	encoded, err := json.Marshal(args)
	if err != nil {
		return nil, err
	}
	h.lifecycle.Lock()
	if err := ctx.Err(); err != nil {
		h.lifecycle.Unlock()
		return nil, err
	}
	lease := h.enabled[id]
	if h.closed || lease == nil || lease.closing || !lease.ready || !protocolProcessLive(lease) || generation != 0 && lease.generation != generation {
		h.lifecycle.Unlock()
		return nil, ErrNotLoaded
	}
	if lease.handles[handle] != "tool" {
		h.lifecycle.Unlock()
		return nil, errors.New("plugin_tool_handle_required")
	}
	if lease.toolEffects[handle] != "send" {
		services.Send = nil
	}
	callCtx, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(lease.adapterContext, cancel)
	if lease.adapterContext.Err() != nil {
		cancel()
	}
	pending, err := lease.process.begin(map[string]any{"op": "invoke", "plugin": lease.key, "handle": handle, "args": json.RawMessage(encoded), "toolServices": true})
	if err != nil {
		h.lifecycle.Unlock()
		stop()
		cancel()
		return nil, err
	}
	if lease.toolInvocations == nil {
		lease.toolInvocations = map[string]*toolInvocation{}
	}
	invocation := &toolInvocation{ctx: callCtx, cancel: cancel, stopLease: stop, services: services}
	lease.toolInvocations[pending.id] = invocation
	h.lifecycle.Unlock()
	defer func() {
		h.lifecycle.Lock()
		delete(lease.toolInvocations, pending.id)
		h.lifecycle.Unlock()
		stop()
		cancel()
	}()
	result, err := pending.wait(callCtx)
	if callCtx.Err() != nil {
		_, cancelErr := lease.process.call(context.Background(), map[string]any{"op": "cancel_invoke", "plugin": lease.key, "invocationId": pending.id})
		return nil, errors.Join(callCtx.Err(), cancelErr)
	}
	h.lifecycle.Lock()
	current := !h.closed && h.enabled[id] == lease && !lease.closing && lease.ready && protocolProcessLive(lease)
	h.lifecycle.Unlock()
	if !current {
		return nil, ErrNotLoaded
	}
	return result, err
}

// Admission happens synchronously in the control reader before another reply
// can settle the invocation or unload the generation. Native work runs outside
// all host locks and remains counted even if the JavaScript worker exits first.
func (h *V1Host) toolRPC(process *nodeProcess, message nodeReply) {
	h.lifecycle.Lock()
	var lease *nodeLease
	for _, entry := range h.enabled {
		if entry.process == process && entry.key == message.Plugin && entry.generation == message.Activation {
			lease = entry
			break
		}
	}
	var invocation *toolInvocation
	if lease != nil && !h.closed && !lease.closing && lease.ready && protocolProcessLive(lease) {
		invocation = lease.toolInvocations[message.InvocationID]
	}
	var done func()
	var admitted bool
	if invocation != nil && invocation.ctx.Err() == nil {
		done, admitted = lease.toolWork.Start()
	}
	h.lifecycle.Unlock()
	respond := func(result json.RawMessage, err error) {
		payload := map[string]any{"op": "tool_response", "plugin": message.Plugin, "requestId": message.RequestID, "result": result}
		if err != nil {
			payload["failed"] = true
			payload["error"] = "plugin_native_callback_failed"
			if errors.Is(err, context.Canceled) || errors.Is(err, ErrNotLoaded) {
				payload["error"] = "plugin_disabled"
			}
		}
		pending, e := process.begin(payload)
		if e == nil {
			pending.remove()
		}
	}
	if !admitted {
		respond(nil, ErrNotLoaded)
		return
	}
	go func() {
		defer done()
		defer func() {
			if recover() != nil {
				respond(nil, errors.New("plugin_native_callback_failed"))
			}
		}()
		var result json.RawMessage
		var err error
		if message.Operation == "cache_start" || message.Operation == "cache_chunk" || message.Operation == "cache_finish" || message.Operation == "cache_abort" {
			result, err = h.toolCacheRPC(process, lease, invocation, message)
		} else if message.Operation != "send" || invocation.services.Send == nil || len(message.Args) != 1 {
			err = errors.New("plugin_send_not_allowed")
		} else {
			result, err = invocation.services.Send(invocation.ctx, message.Args[0])
		}
		if invocation.ctx.Err() != nil {
			err = invocation.ctx.Err()
		}
		respond(result, err)
	}()
}
