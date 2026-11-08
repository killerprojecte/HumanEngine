package plugins

import (
	"context"
	"encoding/json"
	"errors"
)

var errTypedModelCompletionRequired = errors.New("typed_model_completion_required")
var errOpaqueContinuation = errors.New("opaque_model_continuation_not_serializable")

// ModelCompletionRequest contains only visible arguments plus an opaque reference.
// Validate must apply the model service's response, tool and usage rules. It runs
// before the worker reads/claims continuation and outside all lifecycle locks.
// Previous belongs to the last assistant message, never to a persisted JSON field.
type ModelCompletionRequest struct {
	ModelID  string
	Context  any
	Messages any
	Tools    any
	Previous *ContinuationRef
	Validate func(json.RawMessage) error
}

// ModelCompletionResult separates JSON-visible output from private isolate state.
// The assistant-message owner must retain Continuation() for as long as that
// assistant can be reused and call Release when it discards the assistant. Never
// release it merely because the original request context has finished.
type ModelCompletionResult struct {
	Visible      json.RawMessage
	continuation *ContinuationRef
}

func (r ModelCompletionResult) Continuation() *ContinuationRef { return r.continuation }
func (r ModelCompletionResult) MarshalJSON() ([]byte, error)   { return json.Marshal(r.Visible) }

// ContinuationRef is an in-process capability, not a wire marker. Only the owning
// worker stores its original JavaScript value. Copies share release state; a zero,
// released, other-host, model-mismatched or stale-generation ref yields undefined.
// There is intentionally no finalizer: assistant owners MUST Release references
// when removing messages. Disable/crash/Close also releases every worker value.
type ContinuationRef struct{ state *continuationState }
type continuationState struct {
	host                                 *V1Host
	lease                                *nodeLease
	owner, handle, modelID, invocationID string
	released                             bool // guarded by host.lifecycle
}

func (ContinuationRef) MarshalJSON() ([]byte, error) { return nil, errOpaqueContinuation }
func (*ContinuationRef) UnmarshalJSON([]byte) error  { return errOpaqueContinuation }
func (ContinuationRef) String() string               { return "opaque model continuation" }
func (ContinuationRef) GoString() string             { return "opaque model continuation" }

// Release is idempotent across copies and concurrent users. It does not consume
// another reference returned by a later call, even if both hold the same JS value.
// It acknowledges control admission; removal in the worker is ordered before
// later calls on the same host. No timeout or retry policy is imposed here.
func (r *ContinuationRef) Release() error {
	if r == nil || r.state == nil {
		return nil
	}
	state := r.state
	h := state.host
	h.lifecycle.Lock()
	if state.released {
		h.lifecycle.Unlock()
		return nil
	}
	state.released = true
	live := h.currentModelLease(state.owner, state.lease)
	var pending *pendingNodeCall
	var err error
	if live {
		pending, err = state.lease.process.begin(map[string]any{"op": "release_continuation", "plugin": state.lease.key, "invocationId": state.invocationID})
	}
	h.lifecycle.Unlock()
	if pending != nil {
		_, err = pending.wait(context.Background())
	}
	return err
}

// currentModelLease requires lifecycle to be held.
func (h *V1Host) currentModelLease(owner string, lease *nodeLease) bool {
	return !h.closed && h.enabled[owner] == lease && lease.ready && !lease.closing && protocolProcessLive(lease)
}

// CompleteModelProtocol is the only public completion bridge. Visible response
// validation is mandatory, and a successful claim is fenced by registration and
// cancellation checks both before and after the worker acknowledges it.
func (h *V1Host) CompleteModelProtocol(ctx context.Context, id string, generation uint64, request ModelCompletionRequest) (*ModelCompletionResult, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	owner, lease, handle, err := h.modelProtocol(id, generation, "complete")
	if err != nil {
		return nil, err
	}
	if err := adapterReentry(ctx, lease); err != nil {
		return nil, err
	}
	if request.Validate == nil {
		return nil, errors.New("model_response_validator_required")
	}
	// Previous never enters this encoder, including for mismatched or forged refs.
	encoded, err := json.Marshal([]any{request.Context, request.Messages, request.Tools})
	if err != nil {
		return nil, err
	}
	h.lifecycle.Lock()
	if err := ctx.Err(); err != nil {
		h.lifecycle.Unlock()
		return nil, err
	}
	if !h.currentModelLease(owner, lease) {
		h.lifecycle.Unlock()
		return nil, ErrNotLoaded
	}
	previous := ""
	if request.Previous != nil && request.Previous.state != nil {
		state := request.Previous.state
		// Check host identity before reading fields protected by this host's mutex.
		if state.host == h && state.lease == lease && state.handle == handle && state.modelID == request.ModelID && !state.released {
			previous = state.invocationID
		}
	}
	pending, err := lease.process.begin(map[string]any{"op": "complete_model", "plugin": lease.key, "handle": handle, "args": json.RawMessage(encoded), "modelId": request.ModelID, "previous": previous, "retainTurnSignal": ctx.Done() != nil})
	if err != nil {
		h.lifecycle.Unlock()
		return nil, protocolCallError(ctx, err)
	}
	lease.adapterCalls[pending.id] = newAdapterCall(ctx, lease, nil)
	if ctx.Done() != nil {
		lease.turnStops[pending.id] = context.AfterFunc(ctx, func() {
			h.lifecycle.Lock()
			delete(lease.turnStops, pending.id)
			h.lifecycle.Unlock()
			_, _ = lease.process.call(context.Background(), map[string]any{"op": "cancel_invoke", "plugin": lease.key, "invocationId": pending.id})
		})
	}
	h.lifecycle.Unlock()
	defer h.endAdapterCall(lease, pending.id)
	owned := false
	defer func() {
		if owned {
			return
		}
		h.lifecycle.Lock()
		if stop := lease.turnStops[pending.id]; stop != nil {
			stop()
			delete(lease.turnStops, pending.id)
		}
		h.lifecycle.Unlock()
		// Also abort a callback that ignored caller cancellation. A late result must
		// not create new pending state after the release control message was handled.
		_, _ = lease.process.call(context.Background(), map[string]any{"op": "cancel_invoke", "plugin": lease.key, "invocationId": pending.id})
		_, _ = lease.process.call(context.Background(), map[string]any{"op": "release_continuation", "plugin": lease.key, "invocationId": pending.id})
	}()
	raw, err := pending.wait(ctx)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		h.lifecycle.Lock()
		current := h.currentModelLease(owner, lease)
		h.lifecycle.Unlock()
		if !current {
			return nil, ErrNotLoaded
		}
		return nil, protocolCallError(ctx, err)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	h.lifecycle.Lock()
	current := h.currentModelLease(owner, lease)
	h.lifecycle.Unlock()
	if !current {
		return nil, ErrNotLoaded
	}
	var envelope struct {
		Visible string `json:"visible"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil || !json.Valid([]byte(envelope.Visible)) {
		return nil, errors.New("invalid_provider_response")
	}
	visible := json.RawMessage(envelope.Visible)
	if err := request.Validate(append(json.RawMessage(nil), visible...)); err != nil {
		return nil, err
	}
	h.lifecycle.Lock()
	if err := ctx.Err(); err != nil {
		h.lifecycle.Unlock()
		return nil, err
	}
	if !h.currentModelLease(owner, lease) {
		h.lifecycle.Unlock()
		return nil, ErrNotLoaded
	}
	claim, err := lease.process.begin(map[string]any{"op": "claim_continuation", "plugin": lease.key, "invocationId": pending.id, "handle": handle, "modelId": request.ModelID})
	h.lifecycle.Unlock()
	if err != nil {
		return nil, protocolCallError(ctx, err)
	}
	claimed, err := claim.wait(ctx)
	if err != nil {
		if ctx.Err() != nil {
			// Claim has its own transport request. Retire that listener as well as the
			// original invocation if an opaque getter ignores cancellation.
			_, _ = lease.process.call(context.Background(), map[string]any{"op": "cancel_invoke", "plugin": lease.key, "invocationId": claim.id})
		}
		return nil, protocolCallError(ctx, err)
	}
	if string(claimed) != "true" {
		return nil, errors.New("model_continuation_not_pending")
	}
	h.lifecycle.Lock()
	defer h.lifecycle.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !h.currentModelLease(owner, lease) {
		return nil, ErrNotLoaded
	}
	ref := &ContinuationRef{state: &continuationState{host: h, lease: lease, owner: owner, handle: handle, modelID: request.ModelID, invocationID: pending.id}}
	owned = true
	return &ModelCompletionResult{Visible: visible, continuation: ref}, nil
}
