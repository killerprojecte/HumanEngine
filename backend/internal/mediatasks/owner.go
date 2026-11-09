// Package mediatasks owns the physical lifetime of admitted media work.
// Admission policy and scheduling belong to the producer, not this registry.
package mediatasks

import (
	"context"
	"errors"
	"strings"
	"sync"
)

var ErrClosed = errors.New("media_tasks_closed")
var ErrRunRequired = errors.New("media_task_run_required")

// ErrPanicked diagnoses a failed callback without exposing its potentially
// sensitive panic payload. Producers observe this through Task.Wait.
var ErrPanicked = errors.New("media_task_panicked")

// Owner must be shared by all media producers belonging to one engine. Its zero
// value is ready to use. Do not copy an Owner after first use.
type Owner struct {
	mu      sync.Mutex
	index   map[string]*Task
	jobs    map[*Task]struct{}
	closed  bool
	drained chan struct{}
}

// Task completes only after run and its deferred cleanup return. A producer must
// not detach work from run: scheduler waiting, processing, and cleanup all belong
// inside that callback. The callback must cooperate with context cancellation.
type Task struct {
	cancel context.CancelFunc
	done   chan struct{}
	err    error
}

func New() *Owner                     { return &Owner{} }
func (t *Task) Done() <-chan struct{} { return t.done }
func (t *Task) Wait(ctx context.Context) error {
	select {
	case <-t.done:
		return t.err
	default:
	}
	select {
	case <-t.done:
		return t.err
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Accept registers before launching run. It intentionally does not cancel an
// older same-key task: TypeScript Map.set overwrites only the cancellation index.
// No media parsing, personalization policy, fetching, or scheduling occurs here.
func (o *Owner) Accept(sceneID, messageID string, run func(context.Context) error) (*Task, error) {
	if run == nil {
		return nil, ErrRunRequired
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.closed {
		return nil, ErrClosed
	}
	if o.index == nil {
		o.index = make(map[string]*Task)
		o.jobs = make(map[*Task]struct{})
	}
	ctx, cancel := context.WithCancel(context.Background())
	task := &Task{cancel: cancel, done: make(chan struct{})}
	key := sceneID + "|" + messageID
	o.index[key] = task
	o.jobs[task] = struct{}{}
	go func() {
		defer func() {
			if recover() != nil {
				task.err = ErrPanicked
			}
			cancel()
			o.mu.Lock()
			// Deliberately unconditional: old source finally deletes a replacement too.
			delete(o.index, key)
			delete(o.jobs, task)
			close(task.done)
			if o.closed && len(o.jobs) == 0 {
				close(o.drained)
			}
			o.mu.Unlock()
		}()
		task.err = run(ctx)
	}()
	return task, nil
}

// CancelMessage matches source edit/recall: abort the indexed task, without
// deleting its index or cancelling overwritten tasks.
func (o *Owner) CancelMessage(sceneID, messageID string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if task := o.index[sceneID+"|"+messageID]; task != nil {
		task.cancel()
	}
}

// CancelScene uses the source string-prefix rule, including delimiter behavior.
// The caller invokes this on learning disable, not every scene change.
func (o *Owner) CancelScene(sceneID string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	for key, task := range o.index {
		if strings.HasPrefix(key, sceneID+"|") {
			task.cancel()
		}
	}
}

// Close synchronously seals admission and aborts only currently indexed tasks.
// It is idempotent and does not wait for physical work or cleanup to finish.
func (o *Owner) Close() {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.closed {
		return
	}
	o.closed = true
	o.drained = make(chan struct{})
	for _, task := range o.index {
		task.cancel()
	}
	if len(o.jobs) == 0 {
		close(o.drained)
	}
}

// CloseAndWait rejects further admission and cancels only indexed jobs, matching
// source dispose. Overwritten/de-indexed jobs are not additionally cancelled.
// Joining every physical job is a Go shutdown extension; source does not drain.
// A timeout does not release ownership; later calls resume waiting for cleanup.
// Callback errors belong to Task.Wait, not shutdown. No callback runs under mu.
func (o *Owner) CloseAndWait(ctx context.Context) error {
	o.Close()
	o.mu.Lock()
	drained := o.drained
	o.mu.Unlock()
	select {
	case <-drained:
		return nil
	default:
	}
	select {
	case <-drained:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
