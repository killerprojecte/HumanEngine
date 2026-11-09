package snowlumaruntime

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

type policyLifetimeHost struct {
	mu                             sync.Mutex
	loaded                         bool
	enables, disables, binding     int
	call                           func(context.Context, []byte) ([]byte, error)
	bindErr, enableErr, disableErr error
	drain                          <-chan struct{}
	draining                       chan struct{}
}

func (h *policyLifetimeHost) Enable(ctx context.Context, _ string, _ []byte) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.enableErr != nil {
		return h.enableErr
	}
	if h.loaded {
		return errors.New("already_loaded")
	}
	h.loaded = true
	h.enables++
	return nil
}
func (h *policyLifetimeHost) BindCall(_ string) (func(context.Context, []byte) ([]byte, error), error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.bindErr != nil {
		return nil, h.bindErr
	}
	h.binding++
	return h.call, nil
}
func (h *policyLifetimeHost) Disable(_ context.Context, _ string) error {
	if h.draining != nil {
		h.draining <- struct{}{}
	}
	if h.drain != nil {
		<-h.drain
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.loaded = false
	h.disables++
	return h.disableErr
}
func (h *policyLifetimeHost) state() (bool, int, int) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.loaded, h.enables, h.disables
}
func TestTransientPolicyLifetime(t *testing.T) {
	t.Run("fresh whole operation binding and idle unload", func(t *testing.T) {
		h := &policyLifetimeHost{call: func(context.Context, []byte) ([]byte, error) {
			return []byte(`{"kind":"configuration","config":{}}`), nil
		}}
		p := transientPolicy(h, "policy", []byte("immutable"), nil)
		for i := 1; i <= 2; i++ {
			if e := p.Validate(context.Background(), obj()); e != nil {
				t.Fatal(e)
			}
			loaded, enables, disables := h.state()
			if loaded || enables != i || disables != i {
				t.Fatal(loaded, enables, disables)
			}
		}
	})
	t.Run("prepare continuations retain one binding", func(t *testing.T) {
		calls := 0
		h := &policyLifetimeHost{call: func(context.Context, []byte) ([]byte, error) {
			calls++
			if calls == 1 {
				return []byte(`{"kind":"secret_apply","key":"token","action":"set","value":"synthetic","state":{}}`), nil
			}
			return []byte(`{"kind":"configuration","config":{}}`), nil
		}}
		p := transientPolicy(h, "policy", nil, nil)
		_, changes, e := p.Prepare(context.Background(), obj(), obj())
		if e != nil || len(changes) != 1 {
			t.Fatal(changes, e)
		}
		loaded, enables, disables := h.state()
		if loaded || enables != 1 || disables != 1 || h.binding != 1 || calls != 2 {
			t.Fatal(loaded, enables, disables, h.binding, calls)
		}
	})
	t.Run("cancel queued admission cannot unload active owner", func(t *testing.T) {
		entered := make(chan struct{})
		release := make(chan struct{})
		h := &policyLifetimeHost{call: func(context.Context, []byte) ([]byte, error) {
			close(entered)
			<-release
			return []byte(`{"kind":"configuration"}`), nil
		}}
		p := transientPolicy(h, "policy", nil, nil)
		done := make(chan error, 1)
		go func() { done <- p.Validate(context.Background(), obj()) }()
		<-entered
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if e := p.Validate(ctx, obj()); !errors.Is(e, context.Canceled) {
			t.Fatal(e)
		}
		loaded, enables, disables := h.state()
		if !loaded || enables != 1 || disables != 0 {
			t.Fatal(loaded, enables, disables)
		}
		close(release)
		if e := <-done; e != nil {
			t.Fatal(e)
		}
	})
	t.Run("cancel active temporary call unloads", func(t *testing.T) {
		entered := make(chan struct{})
		h := &policyLifetimeHost{call: func(ctx context.Context, _ []byte) ([]byte, error) {
			close(entered)
			<-ctx.Done()
			return nil, ctx.Err()
		}}
		p := transientPolicy(h, "policy", nil, nil)
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		go func() { done <- p.Validate(ctx, obj()) }()
		<-entered
		cancel()
		if e := <-done; !errors.Is(e, context.Canceled) {
			t.Fatal(e)
		}
		loaded, _, disables := h.state()
		if loaded || disables != 1 {
			t.Fatal(loaded, disables)
		}
	})
	t.Run("bind failure joins physical cleanup error", func(t *testing.T) {
		bind := errors.New("bind_failed")
		drain := errors.New("drain_failed")
		h := &policyLifetimeHost{bindErr: bind, disableErr: drain}
		p := transientPolicy(h, "policy", nil, nil)
		e := p.Validate(context.Background(), obj())
		if !errors.Is(e, bind) || !errors.Is(e, drain) {
			t.Fatal(e)
		}
		loaded, _, d := h.state()
		if loaded || d != 1 {
			t.Fatal(loaded, d)
		}
	})
	t.Run("failed acquisition never disables another owner", func(t *testing.T) {
		h := &policyLifetimeHost{loaded: true, enableErr: errors.New("already_loaded")}
		p := transientPolicy(h, "policy", nil, nil)
		if e := p.Validate(context.Background(), obj()); e == nil {
			t.Fatal("accepted")
		}
		loaded, _, d := h.state()
		if !loaded || d != 0 {
			t.Fatal(loaded, d)
		}
	})
	t.Run("return waits for physical drain", func(t *testing.T) {
		drain := make(chan struct{})
		draining := make(chan struct{}, 1)
		h := &policyLifetimeHost{drain: drain, draining: draining, call: func(context.Context, []byte) ([]byte, error) { return []byte(`{"kind":"configuration"}`), nil }}
		p := transientPolicy(h, "policy", nil, nil)
		done := make(chan error, 1)
		go func() { done <- p.Validate(context.Background(), obj()) }()
		<-draining
		select {
		case e := <-done:
			t.Fatal("returned before drain", e)
		default:
		}
		ctx, cancel := context.WithTimeout(context.Background(), time.Millisecond)
		defer cancel()
		if e := p.Validate(ctx, obj()); !errors.Is(e, context.DeadlineExceeded) {
			t.Fatal(e)
		}
		close(drain)
		if e := <-done; e != nil {
			t.Fatal(e)
		}
	})
}
