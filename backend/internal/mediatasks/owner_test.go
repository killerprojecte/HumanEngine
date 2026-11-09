package mediatasks

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

func timeout(t *testing.T) (context.Context, context.CancelFunc) {
	t.Helper()
	return context.WithTimeout(context.Background(), time.Second)
}
func blocked(t *testing.T, o *Owner, scene, id string) (*Task, context.Context, chan struct{}) {
	t.Helper()
	started := make(chan context.Context, 1)
	release := make(chan struct{})
	task, e := o.Accept(scene, id, func(ctx context.Context) error { started <- ctx; <-release; return ctx.Err() })
	if e != nil {
		t.Fatal(e)
	}
	ctx, cancel := timeout(t)
	defer cancel()
	select {
	case runctx := <-started:
		return task, runctx, release
	case <-ctx.Done():
		t.Fatal("start timed out")
	}
	return nil, nil, nil
}
func wait(t *testing.T, task *Task, want error) {
	t.Helper()
	ctx, cancel := timeout(t)
	defer cancel()
	if e := task.Wait(ctx); !errors.Is(e, want) {
		t.Fatalf("wait: %v want %v", e, want)
	}
}
func TestOverwriteAndUnconditionalDelete(t *testing.T) {
	o := New()
	a, ac, ar := blocked(t, o, "s", "m")
	b, bc, br := blocked(t, o, "s", "m")
	o.CancelMessage("s", "m")
	if ac.Err() != nil || bc.Err() != context.Canceled {
		t.Fatal("overwrite cancellation differs")
	}
	close(ar)
	wait(t, a, nil)
	c, cc, cr := blocked(t, o, "s", "m")
	close(br)
	wait(t, b, context.Canceled)
	o.CancelMessage("s", "m")
	if cc.Err() != nil {
		t.Fatal("old finally must erase replacement index")
	}
	close(cr)
	wait(t, c, nil)
	if e := o.CloseAndWait(context.Background()); e != nil {
		t.Fatal(e)
	}
}
func TestScenePrefixAndMessageIsolation(t *testing.T) {
	o := New()
	a, ac, ar := blocked(t, o, "s", "m")
	b, bc, br := blocked(t, o, "s2", "m")
	c, cc, cr := blocked(t, o, "s|nested", "m")
	o.CancelMessage("s", "other")
	if ac.Err() != nil {
		t.Fatal("wrong message cancelled")
	}
	o.CancelScene("s")
	if ac.Err() != context.Canceled || cc.Err() != context.Canceled || bc.Err() != nil {
		t.Fatal("source prefix mismatch")
	}
	close(ar)
	close(br)
	close(cr)
	wait(t, a, context.Canceled)
	wait(t, b, nil)
	wait(t, c, context.Canceled)
}
func TestCloseRetainsPhysicalJobsAndRetries(t *testing.T) {
	o := New()
	a, ac, ar := blocked(t, o, "s", "m")
	b, bc, br := blocked(t, o, "s", "m")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if e := o.CloseAndWait(ctx); e != context.Canceled {
		t.Fatalf("close returned %v", e)
	}
	if ac.Err() != nil || bc.Err() != context.Canceled {
		t.Fatal("close must only abort current index")
	}
	if _, e := o.Accept("s", "n", func(context.Context) error { return nil }); e != ErrClosed {
		t.Fatal(e)
	}
	close(br)
	wait(t, b, context.Canceled)
	if e := o.CloseAndWait(ctx); e != context.Canceled {
		t.Fatal("lost overwritten physical job")
	}
	close(ar)
	wait(t, a, nil)
	if e := o.CloseAndWait(ctx); e != nil {
		t.Fatal("drained retry", e)
	}
}
func TestDeferredCleanupAndCallbackReentry(t *testing.T) {
	o := New()
	entered := make(chan struct{})
	release := make(chan struct{})
	sentinel := errors.New("synthetic")
	task, e := o.Accept("s", "m", func(ctx context.Context) error {
		defer func() { close(entered); <-release }()
		o.CancelMessage("s", "m") // callbacks never execute under owner lock
		<-ctx.Done()
		return sentinel
	})
	if e != nil {
		t.Fatal(e)
	}
	<-entered
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if e := o.CloseAndWait(ctx); e != context.Canceled {
		t.Fatal("cleanup not retained", e)
	}
	select {
	case <-task.Done():
		t.Fatal("done before cleanup")
	default:
	}
	close(release)
	wait(t, task, sentinel)
	if e := o.CloseAndWait(context.Background()); e != nil {
		t.Fatal(e)
	}
}
func TestConcurrentAcceptCancelClose(t *testing.T) {
	o := New()
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			task, e := o.Accept("s", "m", func(context.Context) error { return nil })
			if e == nil {
				<-task.Done()
			} else if e != ErrClosed {
				t.Error(e)
			}
			o.CancelMessage("s", "m")
			o.CancelScene("s")
		}()
	}
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ctx, c := timeout(t)
			defer c()
			if e := o.CloseAndWait(ctx); e != nil {
				t.Error(e)
			}
		}()
	}
	wg.Wait()
}
func TestZeroOwnerAndNilRun(t *testing.T) {
	var o Owner
	if _, e := o.Accept("", "", nil); e != ErrRunRequired {
		t.Fatal(e)
	}
	if e := o.CloseAndWait(context.Background()); e != nil {
		t.Fatal(e)
	}
}

func TestPanicIsDiagnosedAfterDeferredCleanup(t *testing.T) {
	o := New()
	entered := make(chan struct{})
	release := make(chan struct{})
	task, e := o.Accept("s", "m", func(context.Context) error {
		defer func() { close(entered); <-release }()
		panic("synthetic secret must not be exposed")
	})
	if e != nil {
		t.Fatal(e)
	}
	<-entered
	o.Close()
	o.Close()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if e := o.CloseAndWait(ctx); e != context.Canceled {
		t.Fatal("panic cleanup not retained", e)
	}
	select {
	case <-task.Done():
		t.Fatal("panic done before cleanup")
	default:
	}
	close(release)
	wait(t, task, ErrPanicked)
	if e := task.Wait(context.Background()); e.Error() != "media_task_panicked" {
		t.Fatal("unsafe panic diagnostics", e)
	}
	if e := o.CloseAndWait(context.Background()); e != nil {
		t.Fatal(e)
	}
}
func TestCloseSealsWithoutWaitingOrCancellingOrphans(t *testing.T) {
	o := New()
	a, ac, ar := blocked(t, o, "s", "m")
	b, bc, br := blocked(t, o, "s", "m")
	o.Close()
	o.Close()
	if ac.Err() != nil || bc.Err() != context.Canceled {
		t.Fatal("close cancellation differs from source")
	}
	if _, e := o.Accept("s", "n", func(context.Context) error { return nil }); e != ErrClosed {
		t.Fatal(e)
	}
	select {
	case <-b.Done():
		t.Fatal("close pretended to finish physical work")
	default:
	}
	close(ar)
	close(br)
	wait(t, a, nil)
	wait(t, b, context.Canceled)
	if e := o.CloseAndWait(context.Background()); e != nil {
		t.Fatal(e)
	}
}
