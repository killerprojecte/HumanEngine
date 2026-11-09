//go:build web_read_candidate

package webreadruntime

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

func TestInstallationCloseRetainsPhysicalWorkAndCanBeJoinedAgain(t *testing.T) {
	life, cancel := context.WithCancel(context.Background())
	var removals atomic.Int32
	owner := &installation{cancel: cancel, closed: make(chan struct{}), remove: func() { removals.Add(1) }}
	release, ok := owner.work.Start()
	if !ok {
		t.Fatal("admission rejected")
	}
	defer release()
	owner.Dispose()
	owner.Dispose()
	if life.Err() == nil || removals.Load() != 1 {
		t.Fatal("logical disposal did not revoke once", life.Err(), removals.Load())
	}
	if _, ok = owner.work.Start(); ok {
		t.Fatal("physical teardown admitted new work")
	}
	deadline, stop := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer stop()
	if err := owner.CloseAndWait(deadline); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("claimed physical completion before lease released", err)
	}
	release()
	deadline2, stop2 := context.WithTimeout(context.Background(), time.Second)
	defer stop2()
	if err := owner.CloseAndWait(deadline2); err != nil {
		t.Fatal("could not join actual completion", err)
	}
	if removals.Load() != 1 {
		t.Fatal("removed multiple times")
	}
}
