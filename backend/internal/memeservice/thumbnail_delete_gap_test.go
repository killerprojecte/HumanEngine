package memeservice

import (
	"context"
	"sync"
	"testing"
)

// A queued generator is not evidence that every caller passed the initial
// readiness read. Hold the real delete's file gate to expose that distinction
// deterministically, rather than relying on scheduler timing.
func TestThumbnailLateCallerDuringDeleteDurableGap(t *testing.T) {
	f, s, codec, _, _, _, a := nativeImageFixture(t)
	id := str(get(a, "id"))
	slots := []func(){s.thumbnailSlot(), s.thumbnailSlot(), s.thumbnailSlot()}
	var slotsOnce sync.Once
	releaseSlots := func() {
		slotsOnce.Do(func() {
			for _, release := range slots {
				release()
			}
		})
	}
	defer releaseSlots()
	first := make(chan error, 1)
	go func() { _, e := s.Thumbnail(context.Background(), id, codec); first <- e }()
	waitUntil(t, func() bool {
		s.mu.Lock()
		defer s.mu.Unlock()
		return len(s.thumbnailTasks) == 1 && len(s.thumbnailWaiters) == 1
	})
	operation, release, e := f.authority.OperationContext(context.Background(), f.main)
	if e != nil {
		t.Fatal(e)
	}
	defer release()
	unlock := s.assetLock(id)
	var fileOnce sync.Once
	releaseFile := func() { fileOnce.Do(unlock) }
	defer releaseFile()
	deleted := make(chan error, 1)
	go func() {
		ok, e := s.AdminDelete(operation, id)
		if e == nil && !ok {
			t.Error("delete did not find queued thumbnail asset")
		}
		deleted <- e
	}()
	waitUntil(t, func() bool {
		s.mu.Lock()
		defer s.mu.Unlock()
		gate := s.writes[id]
		return gate != nil && gate.users == 2
	})
	// BeginDurable has completed but CommitDurable cannot run until file IO is
	// released. The late caller must fail readiness, not join the earlier task.
	if _, e := s.Thumbnail(context.Background(), id, codec); e == nil || e.Error() != "recovery_authority_head_changed" {
		t.Fatalf("late caller during durable gap: %v", e)
	}
	releaseFile()
	if e := <-deleted; e != nil {
		t.Fatal(e)
	}
	releaseSlots()
	if e := <-first; e == nil || e.Error() != "meme_not_found" {
		t.Fatalf("attached caller after delete: %v", e)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.thumbnails) != 0 || len(s.thumbnailTasks) != 0 || s.thumbnailActive != 0 || len(s.writes) != 0 {
		t.Fatal("async state leaked after delete")
	}
}
