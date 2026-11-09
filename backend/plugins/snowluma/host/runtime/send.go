// Package snowlumaruntime binds one immutable guest generation to native IO.
// All send protocol decisions and receipt identities remain in the WASM guest.
package snowlumaruntime

import (
	"context"
	"errors"
	"sync"

	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/plugins"
	"github.com/killerprojecte/HumanEngine/backend/internal/quiescence"
	"github.com/killerprojecte/HumanEngine/backend/plugins/snowluma/host/outboundmedia"
	transport "github.com/killerprojecte/HumanEngine/backend/plugins/snowluma/host/transport"
)

type Source struct {
	Value    *jsjson.Object
	Validate func() error
	Retain   func() (func(), error)
}
type Ports struct {
	status        *installationStatus
	ReadOriginal  func(context.Context, string, string) (*jsjson.Object, error)
	Life          context.Context
	Current       func() bool
	ResolveSource func(context.Context, any, *jsjson.Object) (*Source, error)
	ResolveMedia  func(context.Context, any, *jsjson.Object) (*jsjson.Object, error)
}
type Sender struct {
	status           *installationStatus
	queries          *Queries
	adapter, account string
	ports            Ports
	guest            func(context.Context, []byte) ([]byte, error)
	encoder          *outboundmedia.Encoder
	http             *transport.HTTP
	ws               *transport.WS
	life             context.Context
	cancel           context.CancelFunc
	detach           func() bool
	work             quiescence.Group
	once             sync.Once
	closed           chan struct{}
	closeErr         error
}

func NewSender(host *plugins.WASMHost, guestID, adapter, account string, ports Ports, http *transport.HTTP, ws *transport.WS) (*Sender, error) {
	if ports.Life == nil || ports.Current == nil || (http == nil) == (ws == nil) {
		return nil, errors.New("invalid_sender_ports")
	}
	guest, err := host.BindCall(guestID)
	if err != nil {
		return nil, err
	}
	encoder, err := outboundmedia.NewEncoder()
	if err != nil {
		return nil, err
	}
	life, cancel := context.WithCancel(ports.Life)
	s := &Sender{status: ports.status, adapter: adapter, account: account, ports: ports, guest: guest, encoder: encoder, http: http, ws: ws, life: life, cancel: cancel, closed: make(chan struct{})}
	if s.ports.ResolveSource == nil && s.ports.ReadOriginal != nil {
		s.ports.ResolveSource = s.resolveOriginal
	}
	s.detach = context.AfterFunc(ports.Life, s.Dispose)
	if ports.Life.Err() != nil {
		s.Dispose()
	}
	return s, nil
}
func obj(pairs ...any) *jsjson.Object {
	o := new(jsjson.Object)
	for i := 0; i < len(pairs); i += 2 {
		_ = o.Set(pairs[i].(string), pairs[i+1])
	}
	return o
}
func get(o *jsjson.Object, key string) any    { v, _ := o.Get(key); return v }
func str(o *jsjson.Object, key string) string { v, _ := get(o, key).(string); return v }
func (s *Sender) current() error {
	if s.life.Err() != nil || !s.ports.Current() {
		return errors.New("plugin_disabled")
	}
	return nil
}
func (s *Sender) call(input *jsjson.Object) (*jsjson.Object, error) {
	if err := s.current(); err != nil {
		return nil, err
	}
	wire, err := jsjson.Marshal(input)
	if err != nil {
		return nil, err
	}
	// Caller cancellation must not close the shared WASM instance. Admission is
	// checked outside, and execution belongs to this captured generation's life.
	raw, err := s.guest(s.life, wire)
	if err != nil {
		return nil, err
	}
	value, err := jsjson.ParseBytes(raw)
	if err != nil {
		return nil, err
	}
	result, ok := value.(*jsjson.Object)
	if !ok {
		return nil, errors.New("invalid_guest_result")
	}
	return result, nil
}
func (s *Sender) result(scene *jsjson.Object, ctx context.Context, response any, actionErr error) (*jsjson.Object, error) {
	input := obj("operation", "send_result", "adapter", s.adapter, "account", s.account, "scene", scene, "aborted", ctx.Err() != nil)
	if actionErr != nil {
		code, transmitted := "send_failed", false
		var known *transport.Error
		if errors.As(actionErr, &known) {
			code, transmitted = known.Code, known.Transmitted
		}
		_ = input.Set("actionError", obj("code", code, "transmitted", transmitted))
	} else {
		_ = input.Set("response", response)
	}
	return s.call(input)
}

// Send synchronously advances guest-requested resolver pauses in source order.
// The returned envelope includes guest receipt and private failure metadata.
func (s *Sender) Send(ctx context.Context, scene, segment *jsjson.Object, before func() error) (*jsjson.Object, error) {
	done, ok := s.work.Start()
	if !ok {
		return nil, errors.New("plugin_disabled")
	}
	defer done()
	result, err := s.call(obj("operation", "send_begin", "adapter", s.adapter, "account", s.account, "scene", scene, "segment", segment, "aborted", ctx.Err() != nil))
	if err != nil {
		return nil, err
	}
	var resolved []*Source
	for {
		switch str(result, "kind") {
		case "receipt":
			return result, nil
		case "resolve_source":
			var source *Source
			if s.ports.ResolveSource != nil {
				source, err = s.ports.ResolveSource(ctx, get(result, "assetId"), get(result, "scene").(*jsjson.Object))
			}
			if err != nil {
				return s.result(scene, ctx, nil, err)
			}
			input := obj("operation", "send_source", "state", get(result, "state"), "aborted", ctx.Err() != nil)
			if source != nil {
				resolved = append(resolved, source)
				_ = input.Set("source", source.Value)
			}
			result, err = s.call(input)
		case "resolve_media":
			var media *jsjson.Object
			if s.ports.ResolveMedia != nil {
				media, err = s.ports.ResolveMedia(ctx, get(result, "assetId"), get(result, "scene").(*jsjson.Object))
			}
			if err != nil {
				return s.result(scene, ctx, nil, err)
			}
			input := obj("operation", "send_media", "state", get(result, "state"), "aborted", ctx.Err() != nil)
			if media != nil {
				_ = input.Set("media", media)
			}
			result, err = s.call(input)
		case "action":
			actionCtx, cancel := context.WithCancel(ctx)
			detach := context.AfterFunc(s.life, cancel)
			if s.life.Err() != nil {
				cancel()
			}
			defer func() { detach(); cancel() }()
			entries, _ := get(result, "sources").([]any)
			if len(entries) != len(resolved) {
				return nil, errors.New("invalid_guest_sources")
			}
			sources := make([]outboundmedia.Source, 0, len(entries))
			for i, entry := range entries {
				source, ok := entry.(*jsjson.Object)
				if !ok {
					return nil, errors.New("invalid_guest_source")
				}
				index, _ := get(source, "index").(float64)
				size, _ := get(source, "bytes").(float64)
				sources = append(sources, outboundmedia.Source{Index: int(index), Path: str(source, "path"), Bytes: int64(size), Hash: str(source, "hash"), Retain: resolved[i].Retain})
			}
			guard := func() error {
				if err := s.current(); err != nil {
					return err
				}
				checked, e := s.call(obj("operation", "send_validate", "adapter", s.adapter, "account", s.account, "scene", scene))
				if e != nil {
					return e
				}
				if str(checked, "kind") != "valid" {
					return &transport.Error{Code: str(checked, "code")}
				}
				if before != nil {
					if e := before(); e != nil {
						return e
					}
				}
				for _, source := range resolved {
					// Source calls every validator here without an optional check.
					// A missing callback fails after beforeTransmit, not during
					// resolution, so later part errors keep source precedence.
					if source.Validate == nil {
						return errors.New("missing_source_validator")
					}
					if e := source.Validate(); e != nil {
						return e
					}
				}
				return s.current()
			}
			preparation := transport.Preparation{Encoder: s.encoder, Sources: sources}
			params, ok := get(result, "params").(*jsjson.Object)
			if !ok {
				return nil, errors.New("invalid_guest_action")
			}
			var response any
			if s.http != nil {
				response, err = s.http.ActionPrepared(actionCtx, str(result, "action"), params, preparation, func(context.Context) error { return guard() })
			} else {
				response, err = s.ws.ActionPrepared(actionCtx, str(result, "action"), params, preparation, guard)
			}
			return s.result(scene, ctx, response, err)
		default:
			return nil, errors.New("invalid_guest_send_stage")
		}
		if err != nil {
			return nil, err
		}
	}
}
func (s *Sender) Dispose() {
	s.once.Do(func() {
		s.cancel()
		s.work.Seal()
		s.encoder.Dispose()
		if s.status != nil {
			s.status.dispose()
		}
		go func() {
			var err error
			if s.http != nil {
				err = s.http.CloseAndWait(context.Background())
			} else {
				err = s.ws.CloseAndWait(context.Background())
			}
			s.closeErr = errors.Join(err, s.work.Wait(context.Background()), s.encoder.CloseAndWait(context.Background()))
			close(s.closed)
		}()
	})
}
func (s *Sender) CloseAndWait(ctx context.Context) error {
	s.Dispose()
	if s.detach != nil {
		s.detach()
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-s.closed:
		return s.closeErr
	}
}

// optionalState encodes an absent continuation as JSON null, not a typed nil
// Object (which jsjson intentionally rejects). Guest owns all state defaults.
func optionalState(value *jsjson.Object) any {
	if value == nil {
		return nil
	}
	return value
}
