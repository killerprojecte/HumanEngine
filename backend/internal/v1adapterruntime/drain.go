package v1adapterruntime

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/killerprojecte/HumanEngine/backend/internal/dispatch"
	"github.com/killerprojecte/HumanEngine/backend/internal/plugins"
)

var errBridgeClosed = errors.New("adapter_runtime_closed")

// callback admission and closing share the bridge registry mutex. A native RPC
// which reaches the wrapper only after shutdown cannot touch Engine owners.
func (b *Bridge) beginCallback() (func(), bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closing {
		return nil, false
	}
	return b.callbacks.Start()
}
func (b *Bridge) adapterServices() plugins.AdapterServices {
	return plugins.AdapterServices{
		Installed: func(ctx context.Context, m plugins.AdapterMetadata) error {
			done, ok := b.beginCallback()
			if !ok {
				return errBridgeClosed
			}
			defer done()
			return b.install(ctx, m)
		},
		Stopped: func(m plugins.AdapterMetadata) {
			done, ok := b.beginCallback()
			if !ok {
				return
			}
			defer done()
			b.stop(m)
		},
		Receive: func(ctx context.Context, m plugins.AdapterMetadata, s dispatch.SceneRef, v json.RawMessage) error {
			done, ok := b.beginCallback()
			if !ok {
				return errBridgeClosed
			}
			defer done()
			return b.receive(ctx, m, s, v)
		},
		Edit: func(ctx context.Context, m plugins.AdapterMetadata, s dispatch.SceneRef, v, c json.RawMessage) (json.RawMessage, error) {
			done, ok := b.beginCallback()
			if !ok {
				return nil, errBridgeClosed
			}
			defer done()
			return b.edit(ctx, m, s, v, c)
		},
		Recall: func(ctx context.Context, m plugins.AdapterMetadata, s, id string) error {
			done, ok := b.beginCallback()
			if !ok {
				return errBridgeClosed
			}
			defer done()
			return b.recall(ctx, m, s, id)
		},
		IsRecalled: func(ctx context.Context, m plugins.AdapterMetadata, s, id string) (bool, error) {
			done, ok := b.beginCallback()
			if !ok {
				return false, errBridgeClosed
			}
			defer done()
			return b.isRecalled(ctx, m, s, id)
		},
		Media: func(ctx context.Context, m plugins.AdapterMetadata, id, s string) (*plugins.AdapterMedia, error) {
			done, ok := b.beginCallback()
			if !ok {
				return nil, errBridgeClosed
			}
			defer done()
			return b.media(ctx, m, id, s)
		},
	}
}

// CloseAndWait unloads the host, removes any remaining generation-bound runtime
// registrations, and waits for accepted native callbacks through actual cleanup.
// Cancellation of the wait never claims those callbacks have finished.
func (b *Bridge) CloseAndWait(ctx context.Context) error {
	b.mu.Lock()
	b.closing = true
	b.callbacks.Seal()
	b.mu.Unlock()
	hostErr := b.Host.Close(ctx)
	b.mu.Lock()
	// Stopped may be delivered after an in-progress Installed callback returns.
	// Late callbacks are sealed; this owned cleanup supplies the same removal.
	for id, entry := range b.adapters {
		delete(b.adapters, id)
		entry.cancel()
		b.engine.Runtime.RemoveAdapter(id)
	}
	b.mu.Unlock()
	return errors.Join(hostErr, b.callbacks.Wait(ctx))
}
