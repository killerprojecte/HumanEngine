package experienceservice

import (
	"context"
	"errors"
	"sync"
	"testing"
)

type observedDrainContext struct {
	context.Context
	entered chan struct{}
	once    sync.Once
}

func (c *observedDrainContext) Done() <-chan struct{} {
	c.once.Do(func() { close(c.entered) })
	return c.Context.Done()
}

func TestRuntimeBindingCloseWaitJoinsHeldSQLite(t *testing.T) {
	f := newLiveFixture(t, false)
	f.main.SetMaxOpenConns(1)
	held, e := f.main.Conn(context.Background())
	if e != nil {
		t.Fatal(e)
	}
	var release sync.Once
	unblock := func() { release.Do(func() { _ = held.Close() }) }
	defer unblock()
	owner, cancel := context.WithCancel(context.Background())
	binding := &RuntimeBinding{ctx: owner, cancel: cancel, report: func(string, error) {}}
	entered, finished := make(chan struct{}), make(chan error, 1)
	binding.enqueue(func() {
		close(entered)
		var n int
		finished <- f.main.QueryRowContext(context.Background(), "SELECT count(*) FROM scenes").Scan(&n)
	})
	<-entered
	stopped, stop := context.WithCancel(context.Background())
	stop()
	if e = binding.CloseAndWait(stopped); !errors.Is(e, context.Canceled) {
		t.Fatal("physical SQL callback reported drained", e)
	}
	if e = held.PingContext(context.Background()); e != nil {
		t.Fatal("database unavailable before callback release", e)
	}
	select {
	case e := <-finished:
		t.Fatal("held SQL unexpectedly finished", e)
	default:
	}
	waiting := &observedDrainContext{Context: context.Background(), entered: make(chan struct{})}
	joined := make(chan error, 1)
	go func() { joined <- binding.CloseAndWait(waiting) }()
	<-waiting.entered // The actual tail wait has reached its select, not a preceding stage.
	select {
	case e := <-joined:
		t.Fatal("uncanceled wait returned before physical SQL release", e)
	default:
	}
	late := false
	binding.enqueue(func() { late = true })
	unblock()
	if e = <-joined; e != nil {
		t.Fatal(e)
	}
	if e = binding.CloseAndWait(context.Background()); e != nil {
		t.Fatal(e)
	}
	if e = <-finished; e != nil {
		t.Fatal(e)
	}
	if late {
		t.Fatal("job entered after close")
	}
	if e = f.main.Ping(); e != nil {
		t.Fatal("binding closed borrowed database", e)
	}
}
