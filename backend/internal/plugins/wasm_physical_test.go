package plugins

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// Module.Close cancels WASM execution, but an imported Go callback may still be
// running. Retirement must join the entire call, including guest free, before
// closing compiled code, releasing the plugin ID, or reporting host shutdown.
func TestWASMRetirementJoinsPhysicalCall(t *testing.T) {
	for _, admission := range []string{"caller", "admitted"} {
		for _, operation := range []string{"disable", "close"} {
			t.Run(admission+"/"+operation, func(t *testing.T) {
				host, err := NewWASMHost(context.Background())
				if err != nil {
					t.Fatal(err)
				}
				defer host.Close(context.Background())
				entered, release := make(chan struct{}), make(chan struct{})
				var releaseOnce sync.Once
				defer releaseOnce.Do(func() { close(release) })
				var active atomic.Bool
				if _, err = host.runtime.NewHostModuleBuilder("probe").NewFunctionBuilder().WithFunc(func() {
					active.Store(true)
					close(entered)
					<-release
					active.Store(false)
				}).Export("entered").Instantiate(context.Background()); err != nil {
					t.Fatal(err)
				}
				if err = host.Enable(context.Background(), "physical", lifecycleWASM([]byte{0x10, 0, 0x0b}, nil)); err != nil {
					t.Fatal(err)
				}
				bound, err := host.BindCall("physical")
				if admission == "admitted" {
					var admitted func(context.Context, context.Context, []byte) ([]byte, error)
					admitted, err = host.BindCallWithAdmission("physical")
					bound = func(ctx context.Context, input []byte) ([]byte, error) {
						return admitted(context.Background(), ctx, input)
					}
				}
				if err != nil {
					t.Fatal(err)
				}
				called := make(chan error, 1)
				go func() {
					out, err := bound(context.Background(), []byte(`{}`))
					if len(out) != 0 {
						t.Errorf("retired call published output: %q", out)
					}
					called <- err
				}()
				select {
				case <-entered:
				case <-time.After(5 * time.Second):
					t.Fatal("guest cleanup did not enter its physical host callback")
				}
				host.mu.Lock()
				instance := host.instances["physical"]
				host.mu.Unlock()
				retired := make(chan error, 2)
				retire := func() {
					if operation == "close" {
						retired <- host.Close(context.Background())
					} else {
						retired <- host.Disable(context.Background(), "physical")
					}
				}
				go retire()
				select {
				case <-instance.stopped.Done():
				case <-time.After(5 * time.Second):
					t.Fatal("retirement did not revoke admission")
				}
				go retire()
				if host.Loaded("physical") {
					t.Error("logically retired plugin remains loaded")
				}
				if _, err = bound(context.Background(), []byte(`{}`)); !errors.Is(err, ErrNotLoaded) {
					t.Error("retired generation accepted a new call", err)
				}
				completed := 0
				select {
				case err := <-retired:
					completed++
					t.Errorf("%s returned while the physical callback is still running: %v", operation, err)
				case <-time.After(30 * time.Millisecond):
				}
				host.mu.Lock()
				reserved := host.instances["physical"] == instance
				host.mu.Unlock()
				if !reserved || !active.Load() {
					t.Error("plugin ID was released before physical callback exit")
				}
				releaseOnce.Do(func() { close(release) })
				if err = waitWASMResult(t, called); !errors.Is(err, ErrNotLoaded) {
					t.Error("retired call did not report revocation", err)
				}
				for ; completed < 2; completed++ {
					if err = waitWASMResult(t, retired); err != nil {
						t.Fatal(err)
					}
				}
				if active.Load() {
					t.Fatal("physical callback remains after retirement")
				}
				host.mu.Lock()
				retained := host.instances["physical"] != nil
				host.mu.Unlock()
				if retained {
					t.Fatal("retired instance still retained")
				}
				if operation == "disable" {
					if err = host.Enable(context.Background(), "physical", lifecycleWASM(nil, nil)); err != nil {
						t.Fatal("replacement failed after physical retirement", err)
					}
					if _, err = bound(context.Background(), []byte(`{}`)); !errors.Is(err, ErrNotLoaded) {
						t.Fatal("old callback invoked replacement", err)
					}
					if out, err := host.Call(context.Background(), "physical", []byte(`{}`)); err != nil || string(out) != `{}` {
						t.Fatalf("replacement call: %s %v", out, err)
					}
				}
			})
		}
	}
}

func TestWASMAdmittedLifetimeCancellationRetiresGuest(t *testing.T) {
	host, err := NewWASMHost(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer host.Close(context.Background())
	entered := make(chan struct{}, 1)
	if _, err = host.runtime.NewHostModuleBuilder("probe").NewFunctionBuilder().WithFunc(func() {
		entered <- struct{}{}
	}).Export("entered").Instantiate(context.Background()); err != nil {
		t.Fatal(err)
	}
	// Signal entry into free, then loop until the execution lifetime ends.
	artifact := lifecycleWASM([]byte{0x10, 0, 0x03, 0x40, 0x0c, 0, 0x0b, 0x0b}, nil)
	if err = host.Enable(context.Background(), "lifetime", artifact); err != nil {
		t.Fatal(err)
	}
	bound, err := host.BindCallWithAdmission("lifetime")
	if err != nil {
		t.Fatal(err)
	}
	life, cancel := context.WithCancel(context.Background())
	defer cancel()
	called := make(chan error, 1)
	go func() {
		out, err := bound(context.Background(), life, []byte(`{}`))
		if len(out) != 0 {
			t.Errorf("cancelled call published output: %q", out)
		}
		called <- err
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("guest cleanup did not begin")
	}
	cancel()
	if err = waitWASMResult(t, called); !errors.Is(err, context.Canceled) {
		t.Fatal("cancelled admission call did not finish", err)
	}
	host.mu.Lock()
	retained := host.instances["lifetime"] != nil
	host.mu.Unlock()
	if retained {
		t.Fatal("cancelled admission call retained its closed guest")
	}
	if err = host.Enable(context.Background(), "lifetime", lifecycleWASM(nil, nil)); err != nil {
		t.Fatal("replacement failed after cancellation", err)
	}
	if _, err = bound(context.Background(), context.Background(), []byte(`{}`)); !errors.Is(err, ErrNotLoaded) {
		t.Fatal("old admitted callback invoked replacement", err)
	}
}
