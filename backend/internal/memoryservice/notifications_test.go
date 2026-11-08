package memoryservice

import (
	"errors"
	"testing"
)

func TestNotificationPostStackPolicyAndExactRequalification(t *testing.T) {
	callbacks := []func(){}
	policy := "p1"
	enabled := true
	current := true
	failed := 0
	seen := []string{}
	reads := 0
	q, e := NewNotificationQueue(NotificationPorts{
		Enabled: func(string) bool { return enabled }, PolicyIdentity: func(string) string { return policy }, PostStack: func(f func()) { callbacks = append(callbacks, f) }, Failed: func() { failed++ },
		Current: func(_ string, r Reference) (*Object, error) {
			reads++
			if !current {
				return nil, nil
			}
			return parseObject(t, `{"record":{"scope":{"kind":"scene","sceneId":"s","personId":"p"}},"derived":false,"rootCoverage":"complete","dependencyKey":"dep"}`), nil
		},
		Episodes: func(string, Reference) ([]*Object, error) { return []*Object{}, nil },
	})
	if e != nil {
		t.Fatal(e)
	}
	_, e = q.Subscribe(func(o *Object) error {
		seen = append(seen, text(o, "reason"))
		_ = o.Set("reason", "listener modified")
		return nil
	})
	if e != nil {
		t.Fatal(e)
	}
	_, e = q.Subscribe(func(o *Object) error {
		if text(o, "reason") == "listener modified" {
			t.Fatal("listener alias")
		}
		return nil
	})
	if e != nil {
		t.Fatal(e)
	}
	r := Reference{"a", 1, 0}
	_ = q.Notify("s", r, "one")
	_ = q.Notify("s", r, "two")
	if len(callbacks) != 1 || reads != 0 {
		t.Fatal("premature delivery")
	}
	callbacks[0]()
	if len(seen) != 1 || seen[0] != "two" {
		t.Fatal(seen)
	}
	_ = q.Notify("s", r, "policy")
	policy = "p2"
	callbacks[1]()
	if reads != 1 {
		t.Fatal("stale policy read")
	}
	_ = q.Notify("s", r, "rollback")
	current = false
	callbacks[2]()
	if len(seen) != 1 {
		t.Fatal("rolled back notification")
	}
	_, e = q.Subscribe(func(*Object) error { return errors.New("test listener") })
	if e != nil {
		t.Fatal(e)
	}
	current = true
	_ = q.Notify("s", r, "failure")
	callbacks[3]()
	if failed != 1 {
		t.Fatal(failed)
	}
	_ = q.Notify("s", r, "disposed")
	q.Dispose()
	callbacks[4]()
	if failed != 1 {
		t.Fatal("disposed delivery")
	}
}
func TestNotificationRequiresRealRemainingOwners(t *testing.T) {
	if _, e := NewNotificationQueue(NotificationPorts{}); e == nil {
		t.Fatal("missing runtime owners accepted")
	}
}

func TestListenersReleaseClosureAndPreserveLiveMutation(t *testing.T) {
	q := &NotificationQueue{}
	seen := []string{}
	var stopA, stopB func()
	var a, b *listener
	var e error
	stopA, e = q.Subscribe(func(*Object) error {
		seen = append(seen, "a")
		stopA()
		stopB()
		if a.fn != nil || b.fn != nil {
			t.Fatal("unsubscribe retained closure during traversal")
		}
		_, err := q.Subscribe(func(*Object) error { seen = append(seen, "d"); return nil })
		return err
	})
	if e != nil {
		t.Fatal(e)
	}
	a = q.head
	stopB, e = q.Subscribe(func(*Object) error { seen = append(seen, "b"); return nil })
	if e != nil {
		t.Fatal(e)
	}
	b = q.tail
	_, e = q.Subscribe(func(*Object) error { seen = append(seen, "c"); return nil })
	if e != nil {
		t.Fatal(e)
	}
	if e = q.emit(new(Object)); e != nil {
		t.Fatal(e)
	}
	if len(seen) != 3 || seen[0] != "a" || seen[1] != "c" || seen[2] != "d" {
		t.Fatalf("live Set order %v", seen)
	}
	if q.live != 2 || listenerEntryCount(t, q) != 2 || q.traversals != 0 {
		t.Fatalf("uncompacted entries: live=%d entries=%d traversals=%d", q.live, listenerEntryCount(t, q), q.traversals)
	}
	// Unsubscribe handles remain idempotent after unlinking and cannot deactivate
	// other entries.
	stopA()
	stopB()
	seen = nil
	if e = q.emit(new(Object)); e != nil {
		t.Fatal(e)
	}
	if len(seen) != 2 || seen[0] != "c" || seen[1] != "d" {
		t.Fatal(seen)
	}
}

func TestRepeatedSubscriptionsDoNotAccumulateEntriesOrClosures(t *testing.T) {
	q := &NotificationQueue{}
	for i := 0; i < 10000; i++ {
		stop, e := q.Subscribe(func(*Object) error { return nil })
		if e != nil {
			t.Fatal(e)
		}
		entry := q.head
		stop()
		stop()
		if entry.fn != nil || entry.active || listenerEntryCount(t, q) != 0 || q.live != 0 {
			t.Fatalf("cycle %d retained subscriber", i)
		}
	}
	// Dispose releases callbacks as well, including for callers holding an old
	// unsubscribe handle. The queue retains no historical scan list.
	stop, e := q.Subscribe(func(*Object) error { return nil })
	if e != nil {
		t.Fatal(e)
	}
	entry := q.head
	q.Dispose()
	stop()
	if entry.fn != nil || entry.active || q.head != nil || q.tail != nil || q.dead != nil || q.live != 0 {
		t.Fatal("dispose retained subscriber")
	}
}

func TestListenerCleanupOnFailureAndDisposeDuringDelivery(t *testing.T) {
	q := &NotificationQueue{}
	sentinel := errors.New("listener failed")
	var stop func()
	stop, _ = q.Subscribe(func(*Object) error { stop(); return sentinel })
	entry := q.head
	if e := q.emit(new(Object)); e != sentinel {
		t.Fatal(e)
	}
	if entry.fn != nil || q.traversals != 0 || listenerEntryCount(t, q) != 0 {
		t.Fatal("failure retained tombstone")
	}
	seen := []string{}
	_, _ = q.Subscribe(func(*Object) error {
		seen = append(seen, "before dispose")
		q.Dispose()
		_, e := q.Subscribe(func(*Object) error { seen = append(seen, "new after clear"); return nil })
		return e
	})
	_, _ = q.Subscribe(func(*Object) error { t.Fatal("deleted listener delivered"); return nil })
	if e := q.emit(new(Object)); e != nil {
		t.Fatal(e)
	}
	if len(seen) != 2 || seen[1] != "new after clear" || listenerEntryCount(t, q) != 1 || q.live != 1 {
		t.Fatalf("clear and live add %v %+v", seen, q)
	}
}

// Check both directions and the absence of leaked deferred nodes after a walk.
func listenerEntryCount(t *testing.T, q *NotificationQueue) int {
	t.Helper()
	count := 0
	var prev *listener
	visited := map[*listener]bool{}
	for l := q.head; l != nil; l = l.next {
		if visited[l] || l.prev != prev {
			t.Fatal("broken listener links")
		}
		visited[l] = true
		prev = l
		count++
		if q.traversals == 0 && (!l.active || l.fn == nil) {
			t.Fatal("retained dead listener")
		}
	}
	if prev != q.tail || (q.traversals == 0 && q.dead != nil) {
		t.Fatal("broken listener tail or retained deferred deletions")
	}
	return count
}

func assertDetachedListener(t *testing.T, l *listener) {
	t.Helper()
	if l.active || l.fn != nil || l.prev != nil || l.next != nil || l.deadNext != nil {
		t.Fatal("old handle retains listener state or neighboring closures")
	}
}

func TestListenerBulkRemovalWithLiveNeighbors(t *testing.T) {
	q := &NotificationQueue{}
	const count = 10000
	stops := make([]func(), count)
	nodes := make([]*listener, count)
	for i := range stops {
		var err error
		stops[i], err = q.Subscribe(func(*Object) error { return nil })
		if err != nil {
			t.Fatal(err)
		}
		nodes[i] = q.tail
	}
	// Remove alternating interior nodes before the remaining nodes. Each handle
	// updates its neighbors directly; no scan or deferred list is needed.
	for parity := 0; parity < 2; parity++ {
		for i := parity; i < count; i += 2 {
			stops[i]()
			stops[i]()
			assertDetachedListener(t, nodes[i])
		}
		if got := listenerEntryCount(t, q); got != q.live {
			t.Fatalf("entries=%d live=%d", got, q.live)
		}
	}
	if q.live != 0 || q.head != nil || q.tail != nil {
		t.Fatal("bulk removal retained entries")
	}
}

func TestListenerNestedDeliveryDefersUnlinkUntilOuterExit(t *testing.T) {
	q := &NotificationQueue{}
	seen := []string{}
	nested := false
	var stopA, stopB func()
	var a, b *listener
	stopA, _ = q.Subscribe(func(*Object) error {
		if nested {
			seen = append(seen, "inner a")
			stopA()
			stopB()
			_, err := q.Subscribe(func(*Object) error { seen = append(seen, "new"); return nil })
			return err
		}
		seen = append(seen, "outer a")
		nested = true
		if err := q.emit(new(Object)); err != nil {
			return err
		}
		if q.traversals != 1 || q.head != a || a.next != b || a.fn != nil || b.fn != nil {
			t.Fatal("nested exit unlinked the outer cursor or retained closures")
		}
		return nil
	})
	a = q.head
	stopB, _ = q.Subscribe(func(*Object) error { t.Fatal("removed listener invoked"); return nil })
	b = q.tail
	if err := q.emit(new(Object)); err != nil {
		t.Fatal(err)
	}
	want := []string{"outer a", "inner a", "new", "new"}
	if len(seen) != len(want) {
		t.Fatal(seen)
	}
	for i := range want {
		if seen[i] != want[i] {
			t.Fatal(seen)
		}
	}
	assertDetachedListener(t, a)
	assertDetachedListener(t, b)
	if listenerEntryCount(t, q) != 1 || q.live != 1 || q.traversals != 0 {
		t.Fatal("nested cleanup failed")
	}
}

func TestListenerPanicUnwindsDeferredDeletion(t *testing.T) {
	q := &NotificationQueue{}
	var stop func()
	stop, _ = q.Subscribe(func(*Object) error { stop(); panic("listener panic") })
	entry := q.head
	func() {
		defer func() {
			if recover() != "listener panic" {
				t.Fatal("panic was swallowed or changed")
			}
		}()
		_ = q.emit(new(Object))
		t.Fatal("expected panic")
	}()
	assertDetachedListener(t, entry)
	if listenerEntryCount(t, q) != 0 || q.live != 0 || q.traversals != 0 {
		t.Fatal("panic retained entries")
	}
}

func TestListenerRemovedTailStillVisitsNewAppend(t *testing.T) {
	q := &NotificationQueue{}
	seen := []string{}
	var stop func()
	stop, _ = q.Subscribe(func(*Object) error {
		seen = append(seen, "old tail")
		stop()
		_, err := q.Subscribe(func(*Object) error { seen = append(seen, "new tail"); return nil })
		return err
	})
	old := q.tail
	if err := q.emit(new(Object)); err != nil {
		t.Fatal(err)
	}
	if len(seen) != 2 || seen[0] != "old tail" || seen[1] != "new tail" {
		t.Fatal(seen)
	}
	assertDetachedListener(t, old)
	if listenerEntryCount(t, q) != 1 || q.live != 1 {
		t.Fatal("tail append lost")
	}
}

func TestListenerErrorStopsTraversalAndKeepsSurvivor(t *testing.T) {
	q := &NotificationQueue{}
	sentinel := errors.New("stop delivery")
	var stop func()
	stop, _ = q.Subscribe(func(*Object) error { stop(); return sentinel })
	removed := q.head
	calls := 0
	_, _ = q.Subscribe(func(*Object) error { calls++; return nil })
	if err := q.emit(new(Object)); err != sentinel {
		t.Fatal(err)
	}
	if calls != 0 {
		t.Fatal("error did not stop delivery")
	}
	assertDetachedListener(t, removed)
	if listenerEntryCount(t, q) != 1 || q.live != 1 {
		t.Fatal("error lost survivor")
	}
	if err := q.emit(new(Object)); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatal("survivor unavailable after failure")
	}
}
