package plugins

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
)

// ModelProtocolMetadata exposes capabilities, never provider credentials or call input.
type ModelProtocolMetadata struct {
	ID         string   `json:"id"`
	Label      string   `json:"label"`
	Operations []string `json:"operations"`
	Generation uint64   `json:"-"`
	handles    map[string]string
}

// RegisteredModelProtocols lists only live V1 registrations in registration order.
// Builtin provider protocols belong to the model service, not this compatibility host.
func (h *V1Host) RegisteredModelProtocols() []ModelProtocolMetadata {
	h.lifecycle.Lock()
	defer h.lifecycle.Unlock()
	result := []ModelProtocolMetadata{}
	if h.closed {
		return result
	}
	leases := []*nodeLease{}
	for _, lease := range h.enabled {
		if lease.ready && !lease.closing && protocolProcessLive(lease) {
			leases = append(leases, lease)
		}
	}
	sort.Slice(leases, func(i, j int) bool { return leases[i].registrationOrder < leases[j].registrationOrder })
	for _, lease := range leases {
		for _, entry := range lease.protocols {
			metadata := entry
			metadata.handles = nil
			metadata.Operations = []string{}
			for _, operation := range []string{"complete", "embeddings", "listModels"} {
				if entry.handles[operation] != "" {
					metadata.Operations = append(metadata.Operations, operation)
				}
			}
			result = append(result, metadata)
		}
	}
	return result
}

// CallModelProtocol invokes embeddings/listModels at the exact registration
// generation. Completions must use CompleteModelProtocol. A disabled/reloaded
// plugin must never receive a request admitted for its predecessor. The model
// service remains responsible for provider configuration, budget, retries and
// result validation. Transport errors here never confer retry permission.
func (h *V1Host) CallModelProtocol(ctx context.Context, id string, generation uint64, operation string, args []any) (json.RawMessage, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	owner, selected, handle, err := h.modelProtocol(id, generation, operation)
	if err != nil {
		return nil, err
	}
	if operation == "complete" {
		return nil, errTypedModelCompletionRequired
	}
	result, err := h.callLease(ctx, owner, selected, handle, args)
	return result, protocolCallError(ctx, err)
}

func (h *V1Host) modelProtocol(id string, generation uint64, operation string) (string, *nodeLease, string, error) {
	h.lifecycle.Lock()
	var owner string
	var selected *nodeLease
	var handle string
	for plugin, lease := range h.enabled {
		if !lease.ready || lease.closing || !protocolProcessLive(lease) || lease.generation != generation {
			continue
		}
		for _, entry := range lease.protocols {
			if entry.ID == id {
				owner, selected, handle = plugin, lease, entry.handles[operation]
				break
			}
		}
	}
	h.lifecycle.Unlock()
	if selected == nil {
		return "", nil, "", ErrNotLoaded
	}
	if handle == "" {
		return "", nil, "", errors.New("protocol_operation_unsupported")
	}
	return owner, selected, handle, nil
}

func protocolCallError(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err == nil || errors.Is(err, ErrNotLoaded) {
		return err
	}
	return &ProtocolInvocationError{cause: err}
}

func protocolProcessLive(lease *nodeLease) bool {
	if lease.dead {
		return false
	}
	select {
	case <-lease.process.done:
		return false
	default:
		return true
	}
}

// ProtocolInvocationError hides arbitrary plugin error text from public model
// failures. It deliberately carries no retry eligibility. The model service must
// apply credential-aware diagnostics before rendering its internal cause.
type ProtocolInvocationError struct{ cause error }

func (e *ProtocolInvocationError) Error() string { return "model_protocol_failure" }
func (e *ProtocolInvocationError) Unwrap() error { return e.cause }
