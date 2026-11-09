package snowlumaruntime

import (
	"context"
	"errors"
	"sync"
	"testing"
)

func TestInstallationStatusOwnership(t *testing.T) {
	old, replacement := newInstallationStatus(), newInstallationStatus()
	oldRead, newRead := old.read, replacement.read
	check := func(read func(context.Context) (string, error), want string) {
		t.Helper()
		got, err := read(context.Background())
		if err != nil || got != want {
			t.Fatal(got, err, want)
		}
	}
	check(oldRead, "disconnected")
	for _, value := range []string{"connecting", "connected", "disconnected", "raw-unrecognized"} {
		old.observe(value)
		check(oldRead, value)
	}
	replacement.observe("connected")
	old.dispose()
	check(oldRead, "disposed")
	check(newRead, "connected")
	old.observe("connected") // Late IO completion cannot resurrect a disposed owner.
	check(oldRead, "disposed")
	check(newRead, "connected")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := oldRead(ctx); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}
func TestInstallationStatusConcurrentDispose(t *testing.T) {
	state := newInstallationStatus()
	var work sync.WaitGroup
	for i := 0; i < 4; i++ {
		work.Add(1)
		go func() {
			defer work.Done()
			for j := 0; j < 100; j++ {
				state.observe("connected")
				_, _ = state.read(context.Background())
			}
		}()
	}
	state.dispose()
	work.Wait()
	got, err := state.read(context.Background())
	if err != nil || got != "disposed" {
		t.Fatal(got, err)
	}
}
