package proactiveowner

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

type manualClock struct {
	now, next, interval time.Duration
	callback            func()
	stopped             bool
}

func (c *manualClock) Every(d time.Duration, f func()) Stopper {
	c.interval = d
	c.next = c.now + d
	c.callback = f
	return c
}
func (c *manualClock) Stop() { c.stopped = true }
func (c *manualClock) advance(d time.Duration) {
	c.now += d
	for !c.stopped && c.next <= c.now {
		c.next += c.interval
		c.callback()
	}
}
func canceled() context.Context {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	return ctx
}
func owner(t *testing.T, c Config) *Owner {
	t.Helper()
	o, e := New(c)
	if e != nil {
		t.Fatal(e)
	}
	if e = o.Start(); e != nil {
		t.Fatal(e)
	}
	return o
}
func TestSourceIntervalFilteringDynamicScenesAndOverlappingTasks(t *testing.T) {
	c := &manualClock{}
	scenes := []Scene{{"a", "group", true}, {"direct", "direct", true}, {"off", "group", false}}
	var ids []string
	var pending []chan struct{}
	o := owner(t, Config{Clock: c, Scenes: func() ([]Scene, error) { return scenes, nil }, Proactive: func(id string, scheduled bool) (<-chan struct{}, error) {
		if !scheduled {
			t.Fatal("manual call")
		}
		ids = append(ids, id)
		done := make(chan struct{})
		pending = append(pending, done)
		return done, nil
	}, OnError: func(e error) { t.Error(e) }})
	if c.interval != 60*time.Second || len(ids) != 0 {
		t.Fatal("interval or eager tick")
	}
	c.advance(59999 * time.Millisecond)
	if len(ids) != 0 {
		t.Fatal("early tick")
	}
	c.advance(time.Millisecond)
	scenes = []Scene{{"b", "group", true}}
	c.advance(time.Minute)
	if len(ids) != 2 || ids[0] != "a" || ids[1] != "b" {
		t.Fatal(ids)
	}
	if e := o.CloseAndWait(canceled()); !errors.Is(e, context.Canceled) {
		t.Fatal("stopped timer mistaken for settled work", e)
	}
	c.advance(time.Minute)
	c.callback() // already queued callback cannot touch scene/runtime
	if len(ids) != 2 {
		t.Fatal("admission after close")
	}
	for _, done := range pending {
		close(done)
	}
	if e := o.CloseAndWait(context.Background()); e != nil {
		t.Fatal(e)
	}
	if e := o.Start(); e == nil {
		t.Fatal("restart after close")
	}
}
func TestPhysicalSceneCallbackRetainedAfterStopAndCanceledWait(t *testing.T) {
	c := &manualClock{}
	entered := make(chan struct{})
	release := make(chan struct{})
	returned := make(chan struct{})
	o := owner(t, Config{Clock: c, Scenes: func() ([]Scene, error) { close(entered); <-release; return nil, nil }, Proactive: func(string, bool) (<-chan struct{}, error) { return nil, nil }, OnError: func(error) {}})
	go func() { c.callback(); close(returned) }()
	<-entered
	if e := o.CloseAndWait(canceled()); !errors.Is(e, context.Canceled) {
		t.Fatal(e)
	}
	close(release)
	<-returned
	if e := o.CloseAndWait(context.Background()); e != nil {
		t.Fatal(e)
	}
}
func TestPhysicalProactiveAndErrorCallbacksAreJoined(t *testing.T) {
	c := &manualClock{}
	entered := make(chan struct{})
	release := make(chan struct{})
	errorEntered := make(chan struct{})
	errorRelease := make(chan struct{})
	returned := make(chan struct{})
	settled := make(chan struct{})
	failure := errors.New("synthetic")
	o := owner(t, Config{Clock: c, Scenes: func() ([]Scene, error) { return []Scene{{"a", "group", true}}, nil }, Proactive: func(string, bool) (<-chan struct{}, error) { close(entered); <-release; return settled, failure }, OnError: func(e error) {
		if e != failure {
			t.Error(e)
		}
		close(errorEntered)
		<-errorRelease
	}})
	go func() { c.callback(); close(returned) }()
	<-entered
	if e := o.CloseAndWait(canceled()); !errors.Is(e, context.Canceled) {
		t.Fatal(e)
	}
	close(release)
	<-errorEntered
	close(settled)
	if e := o.CloseAndWait(canceled()); !errors.Is(e, context.Canceled) {
		t.Fatal("error callback still using owners", e)
	}
	close(errorRelease)
	<-returned
	if e := o.CloseAndWait(context.Background()); e != nil {
		t.Fatal(e)
	}
}
func TestCloseDoesNotCancelAdmittedOperation(t *testing.T) {
	c := &manualClock{}
	done := make(chan struct{})
	var once sync.Once
	o := owner(t, Config{Clock: c, Scenes: func() ([]Scene, error) { return []Scene{{"a", "group", true}}, nil }, Proactive: func(string, bool) (<-chan struct{}, error) { return done, nil }, OnError: func(error) {}})
	c.advance(time.Minute)
	o.Close()
	select {
	case <-done:
		t.Fatal("invented cancellation")
	default:
	}
	if e := o.CloseAndWait(canceled()); !errors.Is(e, context.Canceled) {
		t.Fatal(e)
	}
	// Runtime's source cancellation/physical settlement belongs to application.
	once.Do(func() { close(done) })
	if e := o.CloseAndWait(context.Background()); e != nil {
		t.Fatal(e)
	}
}
func TestDependencyValidationAndCloseBeforeStart(t *testing.T) {
	if _, e := New(Config{}); e == nil {
		t.Fatal("missing dependencies")
	}
	c := &manualClock{}
	o, e := New(Config{Clock: c, Scenes: func() ([]Scene, error) { return nil, nil }, Proactive: func(string, bool) (<-chan struct{}, error) { return nil, nil }, OnError: func(error) {}})
	if e != nil {
		t.Fatal(e)
	}
	o.Close()
	if e = o.Start(); e == nil || c.callback != nil {
		t.Fatal("closed startup")
	}
	if e = o.CloseAndWait(context.Background()); e != nil {
		t.Fatal(e)
	}
}

func TestSceneReadErrorIsVisibleAndNextTickCanRetry(t *testing.T) {
	c := &manualClock{}
	failure := errors.New("synthetic_scene_read")
	reads, calls, reports := 0, 0, 0
	o := owner(t, Config{Clock: c, Scenes: func() ([]Scene, error) {
		reads++
		if reads == 1 {
			return nil, failure
		}
		return []Scene{{"a", "group", true}}, nil
	}, Proactive: func(string, bool) (<-chan struct{}, error) { calls++; return nil, nil }, OnError: func(e error) {
		if e != failure {
			t.Error(e)
		}
		reports++
	}})
	c.advance(time.Minute)
	if calls != 0 || reports != 1 {
		t.Fatal(calls, reports)
	}
	c.advance(time.Minute)
	if calls != 1 || reports != 1 {
		t.Fatal(calls, reports)
	}
	if e := o.CloseAndWait(context.Background()); e != nil {
		t.Fatal(e)
	}
}
