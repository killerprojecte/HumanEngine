package plugins

import (
	"context"
	"encoding/json"
	"errors"
	"sort"

	"github.com/killerprojecte/HumanEngine/backend/internal/dispatch"
)

// AdapterMetadata pins a single installation. A disabled/re-enabled adapter never
// receives a call admitted for the old generation. readImage is deliberately absent:
// the source V1 installAdapter wrapper does not expose the factory's optional method.
type AdapterMetadata struct {
	ID           string `json:"id"`
	Generation   uint64 `json:"-"`
	handles      map[string]string
	stopped      bool
	installing   bool
	stopNotified bool
	order        uint64
}
type AdapterMedia struct {
	File string `json:"file"`
	Type string `json:"type"`
}

// AdapterServices are synchronous native runtime/authority ports, not plugin code.
// Callbacks must complete native work without waiting for a JavaScript callback in
// this isolate. Source runtime.ingest schedules asynchronous plugin listeners rather
// than awaiting them. Pass the provided context to native work; lifecycle abort
// cancels it. This context rejects accidental same-isolate bridge reentry instead of
// deadlocking. Other isolates and ordinary concurrent calls remain independent.
// Edit returns the entire source SourceEventResult, including source authority refs.
// No synthetic source authority, permissive fallback, cache, retry or replay is added.
type AdapterServices struct {
	Installed  func(context.Context, AdapterMetadata) error
	Stopped    func(AdapterMetadata)
	Receive    func(context.Context, AdapterMetadata, dispatch.SceneRef, json.RawMessage) error
	Edit       func(context.Context, AdapterMetadata, dispatch.SceneRef, json.RawMessage, json.RawMessage) (json.RawMessage, error)
	Recall     func(context.Context, AdapterMetadata, string, string) error
	IsRecalled func(context.Context, AdapterMetadata, string, string) (bool, error)
	Media      func(context.Context, AdapterMetadata, string, string) (*AdapterMedia, error)
}
type adapterCall struct {
	ctx       context.Context
	before    func() error
	cancel    context.CancelFunc
	stopLease func() bool
}
type adapterCallbackKey struct{}

func adapterReentry(ctx context.Context, lease *nodeLease) error {
	if lease != nil && ctx.Value(adapterCallbackKey{}) == lease {
		return errors.New("adapter_native_callback_reentry")
	}
	return nil
}
func (h *V1Host) RegisteredAdapters() []AdapterMetadata {
	h.lifecycle.Lock()
	defer h.lifecycle.Unlock()
	result := []AdapterMetadata{}
	if h.closed {
		return result
	}
	leases := []*nodeLease{}
	for _, lease := range h.enabled {
		if !lease.closing && lease.adapterContext.Err() == nil && protocolProcessLive(lease) {
			leases = append(leases, lease)
		}
	}
	sort.Slice(leases, func(i, j int) bool { return leases[i].generation < leases[j].generation })
	for _, lease := range leases {
		for _, entry := range lease.adapters {
			if !entry.stopped && !entry.installing {
				entry.handles = nil
				result = append(result, entry)
			}
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].order < result[j].order })
	return result
}

// retireAdapterLocked hides an installation immediately, removes every handle,
// and emits its native stop once. An in-progress Installed callback owns its
// side effects until it returns; defer its stop notification until then so a late
// native install cannot resurrect a removed transport after cleanup already ran.
func retireAdapterLocked(lease *nodeLease, index int) (AdapterMetadata, bool) {
	entry := &lease.adapters[index]
	entry.stopped = true
	for _, handle := range entry.handles {
		delete(lease.handles, handle)
	}
	entry.handles = nil
	notify := !entry.installing && !entry.stopNotified
	if notify {
		entry.stopNotified = true
	}
	return *entry, notify
}
func (h *V1Host) stopAdapters(lease *nodeLease) {
	h.lifecycle.Lock()
	stopped := []AdapterMetadata{}
	for i := len(lease.adapters) - 1; i >= 0; i-- {
		if entry, notify := retireAdapterLocked(lease, i); notify {
			stopped = append(stopped, entry)
		}
	}
	h.lifecycle.Unlock()
	for _, entry := range stopped {
		if h.adapterServices.Stopped != nil {
			h.adapterServices.Stopped(entry)
		}
	}
}
func (h *V1Host) completeAdapterInstall(lease *nodeLease, id string, err error) error {
	h.lifecycle.Lock()
	var stopped AdapterMetadata
	notify := false
	for i := range lease.adapters {
		if lease.adapters[i].ID == id {
			entry := &lease.adapters[i]
			entry.installing = false
			if err == nil && (entry.stopped || h.closed || lease.closing || !protocolProcessLive(lease)) {
				err = ErrNotLoaded
			}
			if err != nil {
				stopped, notify = retireAdapterLocked(lease, i)
			}
			break
		}
	}
	h.lifecycle.Unlock()
	if notify && h.adapterServices.Stopped != nil {
		h.adapterServices.Stopped(stopped)
	}
	return err
}

// Called under lifecycle admission. These contexts own native RPC work only;
// the model/hook turn signal and continuation ownership remain independent.
func newAdapterCall(ctx context.Context, lease *nodeLease, before func() error) adapterCall {
	linked, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(lease.adapterContext, cancel)
	return adapterCall{ctx: linked, before: before, cancel: cancel, stopLease: stop}
}
func (h *V1Host) endAdapterCall(lease *nodeLease, id string) {
	h.lifecycle.Lock()
	call, ok := lease.adapterCalls[id]
	delete(lease.adapterCalls, id)
	h.lifecycle.Unlock()
	if ok {
		call.stopLease()
		call.cancel()
	}
}
func (p *nodeProcess) notify(message map[string]any) error {
	bytes, err := json.Marshal(message)
	if err != nil {
		return err
	}
	p.write.Lock()
	err = p.writeErr
	if err == nil {
		p.writeQueue = append(p.writeQueue, append(bytes, '\n'))
	}
	p.write.Unlock()
	if err == nil {
		select {
		case p.writeWake <- struct{}{}:
		default:
		}
	}
	return err
}
func (h *V1Host) adapterRPC(process *nodeProcess, request nodeReply) {
	response := map[string]any{"op": "adapter_response", "plugin": request.Plugin, "requestId": request.RequestID}
	result, err := h.adapterService(process, request)
	if err != nil {
		response["failed"] = true
		response["error"] = err.Error()
		if errors.Is(err, ErrNotLoaded) || errors.Is(err, context.Canceled) {
			response["error"] = "plugin_disabled"
		}
	} else if result != nil {
		response["result"] = result
	}
	// The response belongs to the exact process and lease that requested it. An
	// expired response cannot be routed to a new worker with the same plugin id.
	if err := process.notify(response); err != nil {
		// A malformed native JSON response must release the synchronous waiter too.
		// This retries only control serialization, never the native operation.
		_ = process.notify(map[string]any{"op": "adapter_response", "plugin": request.Plugin, "requestId": request.RequestID, "failed": true, "error": "adapter_service_invalid_response"})
	}
}
func (h *V1Host) adapterService(process *nodeProcess, r nodeReply) (any, error) {
	h.lifecycle.Lock()
	var lease *nodeLease
	for _, entry := range h.enabled {
		if entry.process == process && entry.key == r.Plugin && entry.generation == r.Activation {
			lease = entry
			break
		}
	}
	if lease == nil {
		h.lifecycle.Unlock()
		return nil, ErrNotLoaded
	}
	// Removal after sealing is acknowledged without touching native owners;
	// stopAdapters performs the exact-generation cleanup synchronously.
	if lease.closing && r.Operation == "remove" {
		h.lifecycle.Unlock()
		return nil, nil
	}
	finish, admitted := lease.adapterWork.Start()
	if !admitted {
		h.lifecycle.Unlock()
		return nil, ErrNotLoaded
	}
	defer finish()
	var meta AdapterMetadata
	found := false
	for _, entry := range lease.adapters {
		if entry.ID == r.AdapterID {
			meta = entry
			found = true
			break
		}
	}
	if r.Operation == "remove" {
		notify := false
		for i := range lease.adapters {
			if lease.adapters[i].ID == r.AdapterID {
				meta, notify = retireAdapterLocked(lease, i)
				break
			}
		}
		h.lifecycle.Unlock()
		if notify && h.adapterServices.Stopped != nil {
			h.adapterServices.Stopped(meta)
		}
		return nil, nil
	}
	if h.closed || lease.closing || !protocolProcessLive(lease) {
		h.lifecycle.Unlock()
		return nil, ErrNotLoaded
	}
	if r.Operation == "install" {
		if found {
			h.lifecycle.Unlock()
			return nil, errors.New("invalid_adapter_registration")
		}
		var handles map[string]string
		if len(r.Args) != 1 || json.Unmarshal(r.Args[0], &handles) != nil {
			h.lifecycle.Unlock()
			return nil, errors.New("invalid_adapter_registration")
		}
		h.registrySequence++
		meta = AdapterMetadata{ID: r.AdapterID, Generation: lease.generation, handles: handles, order: h.registrySequence, installing: true}
		lease.adapters = append(lease.adapters, meta)
		for _, handle := range handles {
			lease.handles[handle] = "adapterMethod"
		}
		found = true
	}
	if !found || meta.stopped {
		h.lifecycle.Unlock()
		return nil, ErrNotLoaded
	}
	ctx := lease.adapterContext
	call, hasCall := lease.adapterCalls[r.InvocationID]
	if r.InvocationID != "" {
		if !hasCall {
			h.lifecycle.Unlock()
			return nil, errors.New("plugin_call_cancelled")
		}
		ctx = call.ctx
	}
	ctx = context.WithValue(ctx, adapterCallbackKey{}, lease)
	h.lifecycle.Unlock()
	if err := ctx.Err(); err != nil {
		if r.Operation == "install" {
			return nil, h.completeAdapterInstall(lease, r.AdapterID, err)
		}
		return nil, err
	}
	meta.handles = nil
	services := h.adapterServices
	stringArg := func(index int) (string, error) {
		var v string
		if index >= len(r.Args) {
			return "", errors.New("invalid_adapter_bridge_request")
		}
		e := json.Unmarshal(r.Args[index], &v)
		return v, e
	}
	sceneArg := func() (dispatch.SceneRef, error) {
		var s dispatch.SceneRef
		if len(r.Args) == 0 {
			return s, errors.New("invalid_adapter_bridge_request")
		}
		e := json.Unmarshal(r.Args[0], &s)
		return s, e
	}
	switch r.Operation {
	case "install":
		var err error
		if services.Installed != nil {
			err = services.Installed(ctx, meta)
		}
		return nil, h.completeAdapterInstall(lease, r.AdapterID, err)
	case "beforeTransmit":
		if call.before != nil {
			return nil, call.before()
		}
		return nil, nil
	case "receive", "edit":
		scene, err := sceneArg()
		if err != nil {
			return nil, err
		}
		if r.Operation == "receive" && len(r.Args) == 2 && services.Receive != nil {
			return nil, services.Receive(ctx, meta, scene, r.Args[1])
		}
		if r.Operation == "edit" && len(r.Args) == 3 && services.Edit != nil {
			return services.Edit(ctx, meta, scene, r.Args[1], r.Args[2])
		}
	case "recall", "isRecalled", "media":
		first, err := stringArg(0)
		if err != nil {
			return nil, err
		}
		second, err := stringArg(1)
		if err != nil {
			return nil, err
		}
		if r.Operation == "recall" && services.Recall != nil {
			return nil, services.Recall(ctx, meta, first, second)
		}
		if r.Operation == "isRecalled" && services.IsRecalled != nil {
			return services.IsRecalled(ctx, meta, first, second)
		}
		if r.Operation == "media" {
			if services.Media == nil {
				return nil, nil
			}
			media, err := services.Media(ctx, meta, first, second)
			if media == nil {
				return nil, err
			}
			return media, err
		}
	}
	return nil, errors.New("adapter_runtime_service_unavailable")
}

// invokeAdapter admits one operation, returns whether it could have reached the
// isolate, and never retries it. Send uses admission to distinguish pre-send
// cancellation from unknown delivery. Read operations reject retired results.
func (h *V1Host) invokeAdapter(ctx context.Context, id string, generation uint64, operation string, args []any, before func() error) (json.RawMessage, bool, error) {
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}
	encoded, err := json.Marshal(args)
	if err != nil {
		return nil, false, err
	}
	h.lifecycle.Lock()
	var lease *nodeLease
	var handle string
	for _, entry := range h.enabled {
		if entry.generation == generation && !entry.closing && protocolProcessLive(entry) {
			for _, adapter := range entry.adapters {
				if adapter.ID == id && !adapter.stopped && !adapter.installing {
					lease = entry
					handle = adapter.handles[operation]
					break
				}
			}
		}
	}
	if h.closed || lease == nil {
		h.lifecycle.Unlock()
		return nil, false, ErrNotLoaded
	}
	if err := adapterReentry(ctx, lease); err != nil {
		h.lifecycle.Unlock()
		return nil, false, err
	}
	if handle == "" {
		h.lifecycle.Unlock()
		return nil, false, errors.New("adapter_operation_unsupported")
	}
	if err := ctx.Err(); err != nil {
		h.lifecycle.Unlock()
		return nil, false, err
	}
	pending, err := lease.process.begin(map[string]any{"op": "invoke_adapter", "plugin": lease.key, "handle": handle, "args": json.RawMessage(encoded), "beforeTransmit": before != nil})
	if err != nil {
		h.lifecycle.Unlock()
		return nil, false, err
	}
	lease.adapterCalls[pending.id] = newAdapterCall(ctx, lease, before)
	h.lifecycle.Unlock()
	defer h.endAdapterCall(lease, pending.id)
	result, err := pending.wait(ctx)
	if ctx.Err() != nil {
		_, cancelErr := lease.process.call(context.Background(), map[string]any{"op": "cancel_invoke", "plugin": lease.key, "invocationId": pending.id})
		err = errors.Join(ctx.Err(), cancelErr)
		result = nil
	}
	h.lifecycle.Lock()
	live := !h.closed && !lease.closing && protocolProcessLive(lease)
	h.lifecycle.Unlock()
	if !live && (operation != "send" || err != nil) {
		return nil, true, ErrNotLoaded
	}
	return result, true, err
}
func adapterReceipt(status dispatch.Status, detail string) dispatch.Receipt {
	return dispatch.Receipt{Status: status, Detail: &detail}
}
func (h *V1Host) AdapterSend(ctx context.Context, id string, generation uint64, scene dispatch.SceneRef, segment dispatch.Segment, before func() error) (dispatch.Receipt, error) {
	raw, admitted, err := h.invokeAdapter(ctx, id, generation, "send", []any{scene, segment}, before)
	if err != nil {
		if !admitted {
			return adapterReceipt(dispatch.Cancelled, "cancelled_before_send"), err
		}
		detail := "delivery_unknown"
		if errors.Is(err, context.DeadlineExceeded) {
			detail = "request_timeout"
		} else if errors.Is(err, context.Canceled) || errors.Is(err, ErrNotLoaded) {
			detail = "request_cancelled"
		}
		receipt := dispatch.PreserveOutcomeError(adapterReceipt(dispatch.Unknown, detail), err, dispatch.ErrorContext{Operation: "adapter.send", Stage: "adapter_send"})
		return receipt, nil
	}
	var response struct {
		Receipt  dispatch.Receipt `json:"receipt"`
		Rejected *string          `json:"rejected"`
		Failure  *struct {
			Message string `json:"message"`
		} `json:"failure"`
	}
	if err = json.Unmarshal(raw, &response); err != nil {
		return adapterReceipt(dispatch.Unknown, "invalid_send_receipt"), nil
	}
	if response.Rejected != nil {
		return dispatch.Receipt{}, errors.New(*response.Rejected)
	}
	if response.Failure != nil {
		response.Receipt = dispatch.PreserveOutcomeError(response.Receipt, errors.New(response.Failure.Message), dispatch.ErrorContext{Operation: "adapter.send", Stage: "adapter_send"})
	}
	return response.Receipt, nil
}
func (h *V1Host) AdapterHistory(ctx context.Context, id string, generation uint64, scene dispatch.SceneRef, input any) (json.RawMessage, error) {
	r, _, e := h.invokeAdapter(ctx, id, generation, "history", []any{scene, input}, nil)
	return r, e
}
func (h *V1Host) AdapterGetMessage(ctx context.Context, id string, generation uint64, scene dispatch.SceneRef, messageID string) (json.RawMessage, error) {
	r, _, e := h.invokeAdapter(ctx, id, generation, "getMessage", []any{scene, messageID}, nil)
	return r, e
}
func (h *V1Host) AdapterStatus(ctx context.Context, id string, generation uint64) (string, error) {
	r, _, e := h.invokeAdapter(ctx, id, generation, "status", nil, nil)
	if e != nil {
		return "", e
	}
	var s string
	e = json.Unmarshal(r, &s)
	return s, e
}
