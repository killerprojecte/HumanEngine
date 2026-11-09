package contextuallearning

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func defaultJob(key string) Job {
	return Job{SceneID: "s", Key: key, SourceIDs: []string{"a"}, Validate: func() bool { return true }, Run: func(context.Context) (any, error) { return nil, nil }}
}
func waitIdle(t *testing.T, q *Queue) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if e := q.WhenIdle(ctx); e != nil {
		t.Fatal(e)
	}
}
func TestContextualQueuePhysicalDrainAndCancellationReceipt(t *testing.T) {
	var events []Event
	var mu sync.Mutex
	q, _ := NewQueue(QueueOptions{OnEvent: func(e Event) { mu.Lock(); events = append(events, e); mu.Unlock() }})
	started, release := make(chan context.Context, 1), make(chan struct{})
	j := defaultJob("active")
	j.Run = func(ctx context.Context) (any, error) { started <- ctx; <-release; return "saved", nil }
	j.Committed = func(v any) bool { return v == "saved" }
	q.Enqueue(j)
	run := <-started
	q.Enqueue(defaultJob("pending"))
	q.Dispose()
	if run.Err() == nil {
		t.Fatal("not aborted")
	}
	deadline, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if e := q.CloseAndWait(deadline); !errors.Is(e, context.DeadlineExceeded) {
		t.Fatal("drain returned before physical run finished", e)
	}
	if s := q.Stats(); s.Running != 1 || s.Queued != 0 || s.Cancelled != 1 {
		t.Fatal(s)
	}
	close(release)
	if e := q.CloseAndWait(context.Background()); e != nil {
		t.Fatal(e)
	}
	if s := q.Stats(); s.Cancelled != 2 || s.Running != 0 || s.Completed != 0 {
		t.Fatal(s)
	}
	mu.Lock()
	defer mu.Unlock()
	last := events[len(events)-1]
	if last.Status != "cancelled" || !last.Committed {
		t.Fatal(events)
	}
	if r := q.Enqueue(j); r.Reason != "disposed" {
		t.Fatal(r)
	}
}
func TestContextualQueueConcurrentAdmissionAndSingleActive(t *testing.T) {
	var busy atomic.Bool
	busy.Store(true)
	capacity := int64(256)
	q, _ := NewQueue(QueueOptions{MaxQueued: &capacity, ForegroundBusy: busy.Load})
	defer q.CloseAndWait(context.Background())
	var active, maxActive atomic.Int64
	var wg sync.WaitGroup
	for n := 0; n < 200; n++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			j := defaultJob(fmt.Sprint(n % 100))
			j.Run = func(context.Context) (any, error) {
				current := active.Add(1)
				if current > maxActive.Load() {
					maxActive.Store(current)
				}
				active.Add(-1)
				return nil, nil
			}
			r := q.Enqueue(j)
			if r.Status != "queued" && r.Status != "duplicate" {
				t.Errorf("%+v", r)
			}
		}(n)
	}
	wg.Wait()
	if s := q.Stats(); s.Accepted != 100 {
		t.Fatal(s)
	}
	busy.Store(false)
	q.Resume()
	waitIdle(t, q)
	if maxActive.Load() != 1 || q.Stats().Completed != 100 {
		t.Fatal(maxActive.Load(), q.Stats())
	}
}
func TestContextualQueueSourceIsolationRememberedFIFOAndReentrancy(t *testing.T) {
	busy := true
	remembered := int64(2)
	q, _ := NewQueue(QueueOptions{MaxRemembered: &remembered, ForegroundBusy: func() bool { return busy }})
	// Busy is changed only after closing this queue; this branch tests synchronous cancellation.
	a := defaultJob("a")
	a.SourceIDs = []string{"\ue000", "😀", "a", "a"}
	q.Enqueue(a)
	b := a
	b.SourceIDs = []string{"a", "😀", "\ue000"}
	if q.Enqueue(b).Status != "duplicate" {
		t.Fatal("canonical provenance")
	}
	other := defaultJob("other")
	other.SceneID = "other"
	q.Enqueue(other)
	q.Recall("s", "a")
	if q.Stats().Cancelled != 1 || q.Stats().Queued != 1 {
		t.Fatal(q.Stats())
	}
	q.CancelScene("other")
	waitIdle(t, q)
	q.Enqueue(defaultJob("b"))
	q.CancelScene("s")
	if q.Enqueue(a).Status != "queued" {
		t.Fatal("FIFO did not forget oldest")
	}
	q.Dispose()
	_ = q.CloseAndWait(context.Background())
	var r *Queue
	r, _ = NewQueue(QueueOptions{OnEvent: func(e Event) {
		_ = r.Stats()
		if e.Status == "queued" {
			r.CancelScene("s")
		}
		panic("PRIVATE_CANARY")
	}})
	r.Enqueue(defaultJob("reentrant"))
	_ = r.CloseAndWait(context.Background())
	if r.Stats().Cancelled != 1 {
		t.Fatal(r.Stats())
	}
}
func TestContextualQueuePreRunAuthorityPollingAndFailurePrivacy(t *testing.T) {
	var busy, valid atomic.Bool
	busy.Store(true)
	valid.Store(true)
	poll := int64(1)
	q, _ := NewQueue(QueueOptions{PollMS: &poll, ForegroundBusy: busy.Load})
	defer q.CloseAndWait(context.Background())
	j := defaultJob("stale")
	j.Validate = valid.Load
	j.Run = func(context.Context) (any, error) { t.Error("stale job ran"); return nil, nil }
	q.Enqueue(j)
	valid.Store(false)
	busy.Store(false)
	waitIdle(t, q)
	if q.Stats().Discarded != 1 {
		t.Fatal(q.Stats())
	}
	fail := defaultJob("failed")
	fail.Run = func(context.Context) (any, error) { panic("PRIVATE_CANARY") }
	q.Enqueue(fail)
	waitIdle(t, q)
	if q.Stats().Failed != 1 || q.Enqueue(fail).Status != "duplicate" {
		t.Fatal(q.Stats())
	}
	for _, v := range []int64{0, -1, 9007199254740992} {
		if _, e := NewQueue(QueueOptions{PollMS: &v}); e == nil {
			t.Fatal(v)
		}
	}
}
func TestContextualQueueCloseJoinsInFlightAdmission(t *testing.T) {
	q, _ := NewQueue(QueueOptions{})
	started, release, finished := make(chan struct{}), make(chan struct{}), make(chan struct{})
	j := defaultJob("admitting")
	j.Validate = func() bool { close(started); <-release; return true }
	go func() {
		defer close(finished)
		if r := q.Enqueue(j); r.Reason != "disposed" {
			t.Errorf("%+v", r)
		}
	}()
	<-started
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if e := q.CloseAndWait(ctx); !errors.Is(e, context.DeadlineExceeded) {
		t.Fatal("unjoined admission", e)
	}
	close(release)
	<-finished
	if e := q.CloseAndWait(context.Background()); e != nil {
		t.Fatal(e)
	}
}
func TestContextualQueueCloseJoinsCancellationObserver(t *testing.T) {
	entered, release, cancelled := make(chan struct{}), make(chan struct{}), make(chan struct{})
	q, _ := NewQueue(QueueOptions{ForegroundBusy: func() bool { return true }, OnEvent: func(e Event) {
		if e.Status == "cancelled" {
			close(entered)
			<-release
		}
	}})
	q.Enqueue(defaultJob("observer"))
	go func() { q.CancelScene("s"); close(cancelled) }()
	<-entered
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if e := q.CloseAndWait(ctx); !errors.Is(e, context.DeadlineExceeded) {
		t.Fatal("observer outlived physical drain", e)
	}
	close(release)
	<-cancelled
	if e := q.CloseAndWait(context.Background()); e != nil {
		t.Fatal(e)
	}
}
func TestContextualQueueDefaultsAndBusyUncertainty(t *testing.T) {
	q, e := NewQueue(QueueOptions{ForegroundBusy: func() bool { panic("scheduler uncertainty") }})
	if e != nil {
		t.Fatal(e)
	}
	defer q.CloseAndWait(context.Background())
	if q.maxQueued != 128 || q.maxRemembered != 512 || q.pollMS != 100 {
		t.Fatal("source defaults changed")
	}
	for n := 0; n < 128; n++ {
		if r := q.Enqueue(defaultJob(fmt.Sprint(n))); r.Status != "queued" {
			t.Fatal(n, r)
		}
	}
	if r := q.Enqueue(defaultJob("overflow")); r.Reason != "capacity" {
		t.Fatal(r)
	}
	if s := q.Stats(); s.Running != 0 || s.Queued != 128 {
		t.Fatal("fail-open scheduler", s)
	}
}
