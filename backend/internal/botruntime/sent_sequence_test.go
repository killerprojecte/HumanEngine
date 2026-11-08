package botruntime

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

func TestSentSequencerPublicationBarrier(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var calls atomic.Int32
	s := NewSentSequencer(ctx, func(Scene, string, string) { calls.Add(1) })
	reservation := s.reserve(Scene{}, "turn", "target")
	waiting := make(chan error, 1)
	go func() { waiting <- s.Barrier() }()
	select {
	case <-waiting:
		t.Fatal("unreleased publication escaped")
	case <-time.After(10 * time.Millisecond):
	}
	if calls.Load() != 0 {
		t.Fatal("callback ran under unpublished reservation")
	}
	reservation.Release()
	if err := <-waiting; err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 1 {
		t.Fatal("association not executed")
	}
	failed := s.reserve(Scene{}, "failed", "failed")
	failed.Cancel()
	if err := s.Barrier(); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 1 {
		t.Fatal("failed publication ran association")
	}
	pending := s.reserve(Scene{}, "close", "close")
	cancel()
	if err := s.Barrier(); err == nil {
		t.Fatal("close did not cancel wait")
	}
	pending.Release()
	if got := s.reserve(Scene{}, "after-close", "after-close"); got != nil {
		t.Fatal("closed sequence accepted reservation")
	}
}
func TestSentSequencerPanicDoesNotStrandIngress(t *testing.T) {
	s := NewSentSequencer(context.Background(), func(Scene, string, string) { panic("synthetic") })
	s.reserve(Scene{}, "turn", "target").Release()
	done := make(chan error, 1)
	go func() { done <- s.Barrier() }()
	select {
	case e := <-done:
		if e != nil {
			t.Fatal(e)
		}
	case <-time.After(time.Second):
		t.Fatal("panic stranded ingress")
	}
}
