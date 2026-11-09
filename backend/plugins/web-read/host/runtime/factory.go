//go:build web_read_candidate

// Package webreadruntime assembles the isolated runtime candidate. It does not
// grant production admission or change the pending HTTP 101 protocol branch.
package webreadruntime

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"sync"

	"github.com/killerprojecte/HumanEngine/backend/internal/pluginmanager"
	"github.com/killerprojecte/HumanEngine/backend/internal/quiescence"
	"github.com/killerprojecte/HumanEngine/backend/internal/toolresults"
	host "github.com/killerprojecte/HumanEngine/backend/plugins/web-read/host"
)

type installation struct {
	cancel    context.CancelFunc
	work      quiescence.Group
	resolver  *host.OSResolver
	remove    func()
	once      sync.Once
	closeOnce sync.Once
	closed    chan struct{}
}

func (o *installation) Dispose() {
	o.once.Do(func() {
		o.cancel()
		o.work.Seal()
		if o.resolver != nil {
			o.resolver.Close()
		}
		if o.remove != nil {
			o.remove()
		}
	})
}
func (o *installation) CloseAndWait(ctx context.Context) error {
	o.Dispose()
	o.closeOnce.Do(func() {
		go func() {
			// All Service, admitted guest, stream Finish and DNS relay work belongs to
			// this generation. A stalled libc lookup is not reported as a finished drain.
			_ = o.work.Wait(context.Background())
			if o.resolver != nil {
				o.resolver.CloseAndWait()
			}
			close(o.closed)
		}()
	})
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-o.closed:
		return nil
	}
}

// Factory is available only in explicitly tagged validation builds. Production
// app wiring calls the untagged rejection stub, and distributed bytes are unchanged.
func Factory(manager *pluginmanager.Manager) pluginmanager.BuiltinRuntimeFactory {
	return func(ctx context.Context, in pluginmanager.BuiltinRuntimeInput) (pluginmanager.BuiltinLifetime, error) {
		if manager == nil || in.Host == nil || in.Life == nil || in.Current == nil || in.ID != "web-read" {
			return nil, errors.New("web_read_installation_ports_missing")
		}
		life, cancel := context.WithCancel(in.Life)
		owner := &installation{cancel: cancel, closed: make(chan struct{})}
		resolver, err := host.NewOSResolver()
		if err != nil {
			return owner, err
		}
		owner.resolver = resolver
		call, err := in.Host.BindCallWithAdmission(in.ID)
		if err != nil {
			return owner, err
		}
		current := func() bool { return life.Err() == nil && in.Current() }
		network := host.Network{Life: life, BeginWork: owner.work.Start, CallGuestWithAdmission: call, Lookup: resolver.Lookup, DialContext: (&net.Dialer{}).DialContext}
		service, err := host.NewCandidate(host.Ports{Life: life, Config: append(json.RawMessage(nil), in.Config...), BeginWork: owner.work.Start, Current: current, Lifetime: combine, CallGuestWithAdmission: call, Fetch: network.Fetch})
		if err != nil {
			return owner, err
		}
		// An old/default reactor returns an object, never a registrations array. The
		// manager rejects it before any extension is advertised or enabled persisted.
		registrations, err := service.Registrations()
		if err != nil {
			return owner, err
		}
		if err = ctx.Err(); err != nil {
			return owner, err
		}
		owner.remove, err = manager.InstallBuiltinTools(in, registrations, func(ctx context.Context, name string, raw json.RawMessage, cache func(context.Context, toolresults.Source, toolresults.Options) (toolresults.Result, error)) (json.RawMessage, error) {
			done, ok := owner.work.Start()
			if !ok {
				return nil, context.Canceled
			}
			defer done()
			wire, err := json.Marshal(map[string]any{"operation": "toolValidate", "tool": name, "input": raw})
			if err != nil {
				return nil, err
			}
			validation, err := call(ctx, life, wire)
			if err != nil {
				return nil, err
			}
			var result struct {
				Valid bool   `json:"valid"`
				Error string `json:"error"`
			}
			if err = json.Unmarshal(validation, &result); err != nil {
				return nil, err
			}
			if result.Error != "" {
				return nil, errors.New(result.Error)
			}
			if !result.Valid {
				return nil, errors.New("web_read_tool_validation_artifact_mismatch")
			}
			return service.Execute(ctx, name, raw, host.CacheResult(cache))
		})
		return owner, err
	}
}
func combine(ctx, life context.Context) (context.Context, func()) {
	combined, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(life, cancel)
	if life.Err() != nil {
		cancel()
	}
	return combined, func() { stop(); cancel() }
}
