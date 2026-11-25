package botruntime

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
)

func TestAdapterClaimRetainsPhysicalIDUntilDrain(t *testing.T) {
	r := new(Runtime)
	legacy, external := r.NewAdapterOwner(), r.NewAdapterOwner()
	claim, err := external.Claim("shared-wire")
	if err != nil {
		t.Fatal(err)
	}
	if _, present := r.adapters.Load("shared-wire"); present {
		t.Fatal("reservation was dispatchable")
	}
	assertCollision := func() {
		t.Helper()
		if _, err := legacy.Claim("shared-wire"); !errors.Is(err, ErrAdapterIDInUse) {
			t.Fatalf("second origin claim: %v", err)
		}
		if _, err := legacy.Install("shared-wire", new(Adapter)); !errors.Is(err, ErrAdapterIDInUse) {
			t.Fatalf("legacy origin displaced reservation: %v", err)
		}
	}
	assertCollision()
	disposed := 0
	original := &Adapter{Dispose: func() { disposed++ }}
	if err = claim.Publish(original); err != nil {
		t.Fatal(err)
	}
	claim.Release() // A published route must detach first.
	assertCollision()
	claim.Detach()
	if disposed != 1 {
		t.Fatalf("dispose count: %d", disposed)
	}
	if _, present := r.adapters.Load("shared-wire"); present {
		t.Fatal("detached route remained dispatchable")
	}
	if err = claim.Publish(original); err == nil {
		t.Fatal("detached generation could publish again")
	}
	assertCollision()
	claim.Release()

	replacement := new(Adapter)
	newClaim, err := legacy.Install("shared-wire", replacement)
	if err != nil {
		t.Fatal(err)
	}
	claim.Detach()
	claim.Release()
	if got, present := r.adapters.Load("shared-wire"); !present || got != replacement {
		t.Fatal("old cleanup erased replacement")
	}
	newClaim.Detach()
	newClaim.Release()
}

func TestAdapterReplacePreservesOrderAndGeneration(t *testing.T) {
	r := new(Runtime)
	r.settings.Store(&Settings{})
	origin := r.NewAdapterOwner()
	firstDisposed := false
	old, err := origin.Install("first", &Adapter{Dispose: func() { firstDisposed = true }})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = origin.Install("second", new(Adapter)); err != nil {
		t.Fatal(err)
	}
	next, err := origin.Install("first", new(Adapter))
	if err != nil || !firstDisposed {
		t.Fatal(err, firstDisposed)
	}
	old.Detach()
	old.Release()
	got, err := r.AdapterStatuses(context.Background())
	if err != nil || len(got) != 2 || got[0].ID != "first" || got[1].ID != "second" {
		t.Fatal(got, err)
	}
	next.Detach()
	next.Release()
}

func TestAdapterIDHasOneConcurrentOwner(t *testing.T) {
	r := new(Runtime)
	var wg sync.WaitGroup
	var successes atomic.Int32
	start := make(chan struct{})
	for range 12 {
		wg.Go(func() {
			<-start
			if _, err := r.NewAdapterOwner().Claim("same"); err == nil {
				successes.Add(1)
			} else if !errors.Is(err, ErrAdapterIDInUse) {
				t.Error(err)
			}
		})
	}
	close(start)
	wg.Wait()
	if got := successes.Load(); got != 1 {
		t.Fatal(got)
	}
	r.Close()
	if _, err := r.NewAdapterOwner().Claim("other"); err == nil {
		t.Fatal("closed runtime admitted a new owner")
	}
}

func TestZeroRuntimeLegacyAdapterRegistration(t *testing.T) {
	r := new(Runtime)
	value := new(Adapter)
	if err := r.AddAdapter("zero", value); err != nil {
		t.Fatal(err)
	}
	if got, ok := r.adapters.Load("zero"); !ok || got != value {
		t.Fatal("legacy route missing")
	}
	r.RemoveAdapter("zero")
	if _, ok := r.adapters.Load("zero"); ok {
		t.Fatal("legacy route retained")
	}
}
