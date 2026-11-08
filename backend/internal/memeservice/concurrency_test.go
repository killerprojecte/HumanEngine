package memeservice

import (
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
)

func TestAssetWriteRegistryReleasesFailuresAndWaiters(t *testing.T) {
	s := &Service{writes: map[string]*assetGate{}}
	var entered atomic.Int32
	var collisions atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			release := s.assetLock("same")
			defer release()
			if entered.Add(1) != 1 {
				collisions.Add(1)
			}
			entered.Add(-1)
		}()
	}
	wg.Wait()
	if collisions.Load() != 0 {
		t.Fatal("parallel writers")
	}
	if len(s.writes) != 0 {
		t.Fatal("registry retained waiters")
	}
	for i := 0; i < 100; i++ {
		func() { release := s.assetLock(fmt.Sprint(i)); defer release() /* failure path */ }()
	}
	if len(s.writes) != 0 {
		t.Fatal("registry retained failed writers")
	}
}
