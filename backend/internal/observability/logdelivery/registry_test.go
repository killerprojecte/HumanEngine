package logdelivery

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func lease(t *testing.T, r *Registry, directory string) *Lease {
	t.Helper()
	l, e := r.Acquire(directory)
	if e != nil {
		t.Fatal(e)
	}
	return l
}
func closeLease(t *testing.T, l *Lease) FlushResult {
	t.Helper()
	r, e := l.Close(context.Background())
	if e != nil {
		t.Fatal(e)
	}
	return r
}
func beginClose(t *testing.T, l *Lease, timeout time.Duration) <-chan flushAnswer {
	t.Helper()
	c := l.owner.channel
	select {
	case <-c.flushStarted:
	default:
	}
	done := make(chan flushAnswer, 1)
	go func() { r, e := l.CloseWithTimeout(context.Background(), timeout); done <- flushAnswer{r, e} }()
	select {
	case <-c.flushStarted:
	case a := <-done:
		done <- a
	case <-time.After(3 * time.Second):
		t.Fatal("close not queued")
	}
	return done
}
func memoryRegistry(t *testing.T, firstGate <-chan struct{}, firstSink *memorySink) (*Registry, *int) {
	t.Helper()
	r := NewRegistry(io.Discard)
	count := new(int)
	r.factory = func(o channelOptions, exit func()) (*Channel, error) {
		*count++
		var gate <-chan struct{}
		s := &memorySink{}
		if *count == 1 {
			gate = firstGate
			if firstSink != nil {
				s = firstSink
			}
		}
		c := startChannel(o, s, gate, exit)
		c.flushStarted = make(chan struct{}, 16)
		t.Cleanup(func() {
			c.mu.Lock()
			c.abandonLocked("worker_exited")
			c.mu.Unlock()
			select {
			case <-c.Done():
			case <-time.After(3 * time.Second):
				t.Error("retained live owner")
			}
		})
		return c, nil
	}
	return r, count
}
func TestSourceRegistryOracle(t *testing.T) {
	cases := sourceCases(t)
	t.Run("released-lease-is-local-and-retains-generation-view", func(t *testing.T) {
		n := "released-lease-is-local-and-retains-generation-view"
		r, count := memoryRegistry(t, nil, nil)
		a := lease(t, r, "/logs")
		b := lease(t, r, "/logs/.")
		a.Enqueue("one\n", false)
		exact(t, cases, n, "aClosed", closeLease(t, a))
		if a.Enqueue("after-release\n", false) {
			t.Fatal("released accepted")
		}
		b.Enqueue("two\n", false)
		exact(t, cases, n, "bClosed", closeLease(t, b))
		exact(t, cases, n, "aStatus", a.Status())
		if a.CloseSync(DefaultTimeout) != nil {
			t.Fatal("duplicate sync close")
		}
		exact(t, cases, n, "repeated", closeLease(t, a))
		exact(t, cases, n, "workers", *count)
	})
	t.Run("retiring-owner-blocks-replacement-until-close-ack", func(t *testing.T) {
		n := "retiring-owner-blocks-replacement-until-close-ack"
		gate := make(chan struct{})
		r, _ := memoryRegistry(t, gate, nil)
		a := lease(t, r, "/logs")
		a.Enqueue("one\n", false)
		exited := make(chan struct{})
		a.owner.channel.afterControl = func() { <-exited }
		defer close(exited)
		closing := beginClose(t, a, DefaultTimeout)
		b := lease(t, r, "/logs/.")
		exact(t, cases, n, "whileClosing", b.Enqueue("rejected\n", false))
		close(gate)
		exact(t, cases, n, "firstClose", answer(t, closing))
		exact(t, cases, n, "next", b.Enqueue("three\n", false))
		exact(t, cases, n, "result", closeLease(t, b))
		exact(t, cases, n, "retired", a.Status())
	})
	t.Run("unconfirmed-owner-stays-registered-until-exit", func(t *testing.T) {
		n := "unconfirmed-owner-stays-registered-until-exit"
		start := make(chan struct{})
		physical := make(chan struct{})
		r, count := memoryRegistry(t, start, &memorySink{closeGate: physical})
		a := lease(t, r, "/logs")
		a.Enqueue("one\n", false)
		stopped, e := a.CloseWithTimeout(context.Background(), 0)
		if e != nil {
			t.Fatal(e)
		}
		exact(t, cases, n, "stopped", stopped)
		b := lease(t, r, "/logs")
		if b.Enqueue("blocked\n", false) {
			t.Fatal("overlap before physical exit")
		}
		if *count != 1 {
			t.Fatal(*count)
		}
		close(physical)
		<-a.Done()
		if !b.Enqueue("new\n", false) {
			t.Fatal("no successor after physical exit")
		}
		exact(t, cases, n, "result", closeLease(t, b))
		exact(t, cases, n, "retired", a.Status())
		exact(t, cases, n, "workers", *count)
	})
	t.Run("zero-reference-confirmed-exit-creates-clean-owner", func(t *testing.T) {
		n := "zero-reference-confirmed-exit-creates-clean-owner"
		start := make(chan struct{})
		r, _ := memoryRegistry(t, start, nil)
		a := lease(t, r, "/logs")
		a.Enqueue("old\n", false)
		a.CloseWithTimeout(context.Background(), 0)
		<-a.Done()
		b := lease(t, r, "/logs")
		b.Enqueue("new\n", false)
		exact(t, cases, n, "result", closeLease(t, b))
		exact(t, cases, n, "retired", a.Status())
	})
	t.Run("canonical-existing-parent-shares-not-lexical-path", func(t *testing.T) {
		n := "canonical-existing-parent-shares-not-lexical-path"
		root := t.TempDir()
		real := filepath.Join(root, "real")
		if e := os.Mkdir(real, 0700); e != nil {
			t.Fatal(e)
		}
		alias := filepath.Join(root, "alias")
		if e := os.Symlink(real, alias); e != nil {
			t.Fatal(e)
		}
		r, count := memoryRegistry(t, nil, nil)
		a := lease(t, r, filepath.Join(alias, "new"))
		b := lease(t, r, filepath.Join(real, "new", "."))
		a.Enqueue("shared\n", false)
		closeLease(t, a)
		exact(t, cases, n, "result", closeLease(t, b))
		exact(t, cases, n, "workers", *count)
		if _, e := os.Stat(filepath.Join(real, "new")); !os.IsNotExist(e) {
			t.Fatal("canonicalization created directory", e)
		}
	})
}

func TestCancellationRetainsPhysicalOwnerAndIndependentRegistryProgress(t *testing.T) {
	root := t.TempDir()
	held := make(chan struct{})
	entered := make(chan struct{}, 1)
	s := &memorySink{writeGate: held, entered: entered}
	r, count := memoryRegistry(t, nil, s)
	a := lease(t, r, filepath.Join(root, "held"))
	a.Enqueue("old\n", false)
	<-entered
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	result, e := a.CloseWithTimeout(ctx, time.Second)
	if e != context.Canceled || result.Complete {
		t.Fatal(result, e)
	}
	select {
	case <-a.Done():
		t.Fatal("caller cancellation released physical owner")
	default:
	}
	b := lease(t, r, filepath.Join(root, "held"))
	if b.owner != a.owner || b.Enqueue("blocked\n", false) {
		t.Fatal("replaced held owner")
	}
	// This acquire, real write, and close must finish while the old sink is held.
	other := lease(t, r, filepath.Join(root, "independent"))
	other.Enqueue("other\n", false)
	if !closeLease(t, other).Complete {
		t.Fatal("independent owner blocked")
	}
	if *count != 2 {
		t.Fatal("overlapping owner", *count)
	}
	close(held)
	retry := closeLease(t, a)
	if retry.Status.WrittenRecords != 1 {
		t.Fatal(retry)
	}
	<-a.Done()
	if !b.Enqueue("new\n", false) {
		t.Fatal("no replacement after physical close")
	}
	final := closeLease(t, b)
	if final.Status.AcceptedRecords != 2 || final.Status.WrittenRecords != 2 || final.Complete {
		t.Fatal(final)
	}
	// Historical result cannot be mutated by changing a returned nullable token.
	historical := a.Status()
	*historical.LastFailure = "caller mutation"
	if *a.Status().LastFailure != "sink_closed" {
		t.Fatal("mutable historical status")
	}
}
func TestConcurrentLeaseIdentityAndPhysicalDrain(t *testing.T) {
	r := NewRegistry(io.Discard)
	directory := filepath.Join(t.TempDir(), "logs")
	anchor := lease(t, r, directory)
	const holders = 64
	var group sync.WaitGroup
	errs := make(chan string, holders)
	for i := 0; i < holders; i++ {
		group.Add(1)
		go func() {
			defer group.Done()
			l, e := r.Acquire(directory)
			if e != nil {
				errs <- e.Error()
				return
			}
			if l.owner != anchor.owner {
				errs <- "owner identity mismatch"
			}
			if !l.Enqueue("one\n", false) {
				errs <- "unexpected rejection"
			}
			v, e := l.Close(context.Background())
			if e != nil || !v.Complete {
				errs <- "nonlast close incomplete"
			}
		}()
	}
	group.Wait()
	close(errs)
	for e := range errs {
		t.Error(e)
	}
	if s := anchor.Status(); s.AcceptedRecords != holders || s.WrittenRecords != holders {
		t.Fatal(s)
	}
	result := closeLease(t, anchor)
	if !result.Durable {
		t.Fatal(result)
	}
	<-anchor.Done()
	fresh := lease(t, r, directory)
	if fresh.owner == anchor.owner || fresh.Status().AcceptedRecords != 0 {
		t.Fatal("old zero-ref generation reused")
	}
	closeLease(t, fresh)
}
