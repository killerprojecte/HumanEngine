package snowlumaruntime

import (
	"context"
	"encoding/json"
	"errors"
	"sync"

	"github.com/killerprojecte/HumanEngine/backend/internal/botruntime"
	"github.com/killerprojecte/HumanEngine/backend/internal/dispatch"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
)

// Registry is one application's SnowLuma installation owner. It never resolves a
// mutable guest ID during dispatch, and stale cleanup cannot remove a replacement.
type Registry struct {
	mu      sync.Mutex
	runtime *botruntime.Runtime
	current *Registration
}
type Registration struct {
	owner   *Registry
	sender  *Sender
	adapter *botruntime.Adapter
	detach  func() bool
}

// NewRegistry must be called once by the application owner, not once per plugin
// generation. That owner must route every SnowLuma install/removal through this
// registry; direct competing Runtime.AddAdapter/RemoveAdapter calls violate the
// ownership contract. The application assembly is responsible for this binding.
func NewRegistry(runtime *botruntime.Runtime) *Registry { return &Registry{runtime: runtime} }
func jsonObject(value any) (*jsjson.Object, error) {
	wire, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	parsed, err := jsjson.ParseBytes(wire)
	if err != nil {
		return nil, err
	}
	o, ok := parsed.(*jsjson.Object)
	if !ok {
		return nil, errors.New("invalid_adapter_input")
	}
	return o, nil
}
func (r *Registry) RegisterAdapter(s *Sender) (*Registration, error) {
	if r.runtime == nil {
		return nil, errors.New("runtime_required")
	}
	identity, err := s.call(obj("operation", "identifiers", "adapter", s.adapter, "account", s.account, "platform", s.account))
	if err != nil {
		return nil, err
	}
	entry := &Registration{owner: r, sender: s}
	adapter := &botruntime.Adapter{BotUserID: str(identity, "userId"), Dispose: s.Dispose}
	adapter.Transport = dispatch.AbortContainedAdapter(&dispatch.Adapter{Send: func(scene dispatch.SceneRef, segment dispatch.Segment, signal *dispatch.Signal, before func() error) *dispatch.Future[dispatch.Receipt] {
		result := dispatch.NewFuture[dispatch.Receipt]()
		go func() {
			sc, err := jsonObject(scene)
			if err != nil {
				result.Reject(err)
				return
			}
			sg, err := jsonObject(segment)
			if err != nil {
				result.Reject(err)
				return
			}
			var ctx context.Context = context.Background()
			if signal != nil {
				ctx = signal
			}
			envelope, err := s.Send(ctx, sc, sg, before)
			if err != nil {
				result.Reject(err)
				return
			}
			raw, err := jsjson.Marshal(get(envelope, "receipt"))
			if err != nil {
				result.Reject(err)
				return
			}
			var receipt dispatch.Receipt
			if err = json.Unmarshal(raw, &receipt); err != nil {
				result.Reject(err)
				return
			}
			if failure, ok := get(envelope, "failure").(*jsjson.Object); ok {
				// Preserve only the guest's fixed code in diagnostics, never peer payloads.
				receipt = dispatch.PreserveOutcomeError(receipt, errors.New(str(failure, "code")), dispatch.ErrorContext{Operation: "adapter.send", Stage: "send_segment"})
			}
			result.Resolve(receipt)
		}()
		return result
	}})
	entry.adapter = adapter
	r.mu.Lock()
	defer r.mu.Unlock()
	if err = s.current(); err != nil {
		return nil, err
	}
	if r.current != nil && r.current.sender.adapter != s.adapter {
		return nil, errors.New("registry_adapter_mismatch")
	}
	r.runtime.AddAdapter(s.adapter, adapter)
	r.current = entry
	entry.detach = context.AfterFunc(s.life, entry.Remove)
	return entry, nil
}
func (r *Registration) Remove() {
	r.owner.mu.Lock()
	if r.detach != nil {
		r.detach()
	}
	if r.owner.current == r {
		r.owner.current = nil
		r.owner.runtime.RemoveAdapter(r.sender.adapter)
	}
	r.owner.mu.Unlock()
	r.sender.Dispose()
}
func (r *Registration) CloseAndWait(ctx context.Context) error {
	r.Remove()
	return r.sender.CloseAndWait(ctx)
}
