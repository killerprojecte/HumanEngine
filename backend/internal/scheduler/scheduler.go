// Package scheduler implements the global work admission policy in src/scheduler.ts.
// Each running task retains its slot until its own completion, including after
// cancellation. It does not invoke providers or add admission limits.
package scheduler

import (
	"context"
	"errors"
	"math"
	"sort"
	"sync"
	"time"
)

var (
	ErrInvalidOptions     = errors.New("invalid_scheduler_options")
	ErrInvalidConcurrency = errors.New("invalid_concurrency")
	ErrInvalidCallRate    = errors.New("invalid_call_rate")
	ErrCancelled          = errors.New("request_cancelled")
	ErrQueueFull          = errors.New("work_queue_full")
	ErrWorkFailed         = errors.New("work_failed")
)

// Clock uses JavaScript millisecond timestamps. AfterFunc must enqueue, rather
// than invoke inline, its callback. Stop may race with an already fired callback.
type Clock interface {
	Now() float64
	AfterFunc(delayMS float64, callback func()) Timer
}
type Timer interface{ Stop() bool }
type wallClock struct{}

func (wallClock) Now() float64 { return float64(time.Now().UnixMilli()) }
func (wallClock) AfterFunc(ms float64, callback func()) Timer {
	// Preserve Node setTimeout coercion, including a large backward wall-clock jump.
	if math.IsNaN(ms) || ms < 1 || ms > 2147483647 {
		ms = 1
	}
	return time.AfterFunc(time.Duration(math.Trunc(ms))*time.Millisecond, callback)
}

type Options struct {
	ReserveForeground float64
	BackgroundAgingMS *float64
	Clock             Clock
}
type SchedulingOptions struct{ OverdueLearningAt *float64 }
type Status struct {
	Running     int     `json:"running"`
	Queued      int     `json:"queued"`
	Concurrency float64 `json:"concurrency"`
}

// Task starts asynchronous work and returns promptly, as a JavaScript async
// callback does after returning its promise. It must call complete only when
// that work really ends. Starters run in admission order, outside the scheduler
// lock, and can submit nested work. Use a goroutine inside a starter for blocking
// Go operations. Completing more than once is harmless, like resolving a promise.
// Panicking before completion rejects the task; error panics retain identity.
type Task func(ctx context.Context, complete func(value any, err error))

type Future struct {
	done   chan struct{}
	value  any
	err    error
	cancel func()
}

func (f *Future) Done() <-chan struct{} { return f.done }

// Wait waits for the actual task result. Cancellation of running work does not
// synthesize a result or release its slot.
func (f *Future) Wait() (any, error) { <-f.done; return f.value, f.err }

// Cancel cancels this request, not the submitting context or any sibling job.
// It removes queued work immediately. A running task observes ctx cancellation
// and continues owning its slot until it calls complete.
func (f *Future) Cancel() {
	if f.cancel != nil {
		f.cancel()
	}
}

type job struct {
	priority         float64
	overdue          *float64
	enqueuedAt       float64
	sequence         float64
	ctx              context.Context
	stopCancel       func() bool
	task             Task
	future           *Future
	completionQueued bool
	settled          bool
}

type Scheduler struct {
	mu                                 sync.Mutex
	queue                              []*job
	running, runningNonForeground      int
	sequence                           float64
	concurrency, maxQueued             float64
	callsPerMinute                     float64
	starts                             []float64
	foregroundStartsWhileLearningWaits int
	reserveForeground                  float64
	backgroundAgingMS                  *float64
	clock                              Clock
	rateTimer                          Timer
	timerGeneration                    uint64
	callbacks                          []func()
	dispatching                        bool
	launch                             func(func())
}

func safeNonnegative(n float64) bool {
	return !math.IsNaN(n) && !math.IsInf(n, 0) && n >= 0 && n <= 9007199254740991 && math.Trunc(n) == n
}
func numberCopy(n *float64) *float64 {
	if n == nil {
		return nil
	}
	v := *n
	return &v
}

// New mirrors the source constructor: only reservation and aging are validated.
// Configure, unlike the constructor, validates concurrency and call rate.
func New(concurrency, maxQueued float64, options Options) (*Scheduler, error) {
	if !safeNonnegative(options.ReserveForeground) || (options.BackgroundAgingMS != nil && !safeNonnegative(*options.BackgroundAgingMS)) {
		return nil, ErrInvalidOptions
	}
	clock := options.Clock
	if clock == nil {
		clock = wallClock{}
	}
	return &Scheduler{concurrency: concurrency, maxQueued: maxQueued, reserveForeground: options.ReserveForeground, backgroundAgingMS: numberCopy(options.BackgroundAgingMS), clock: clock, launch: func(f func()) { go f() }}, nil
}
func NewDefault() *Scheduler { s, _ := New(4, 256, Options{}); return s }
func NewModelScheduler() *Scheduler {
	aging := float64(5 * 60_000)
	s, _ := New(4, 256, Options{ReserveForeground: 1, BackgroundAgingMS: &aging})
	return s
}

// ModelScheduler is the source's one process-wide model scheduler. Constructing
// it starts no goroutine or timer. Composition may instead share an injected
// NewModelScheduler instance, but must not make one instance per model or scene.
var ModelScheduler = NewModelScheduler()

func (s *Scheduler) Configure(concurrency, callsPerMinute float64) error {
	if math.IsNaN(concurrency) || math.IsInf(concurrency, 0) || math.Trunc(concurrency) != concurrency || concurrency < 1 || concurrency > 32 {
		return ErrInvalidConcurrency
	}
	if !safeNonnegative(callsPerMinute) || callsPerMinute > 6000 {
		return ErrInvalidCallRate
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.concurrency = concurrency
	s.callsPerMinute = callsPerMinute
	if callsPerMinute == 0 {
		s.starts = nil
	}
	s.drainLocked()
	return nil
}
func (s *Scheduler) Status() Status {
	s.mu.Lock()
	defer s.mu.Unlock()
	return Status{s.running, len(s.queue), s.concurrency}
}
func rejected(err error) *Future {
	f := &Future{done: make(chan struct{}), err: err}
	close(f.done)
	return f
}

// Submit captures the submitting context, including all caller values, model
// priority and learning deadline. Explicit priority/options follow WorkScheduler
// directly; SubmitModel additionally applies the model-scheduling context helpers.
func (s *Scheduler) Submit(ctx context.Context, priority float64, task Task, options SchedulingOptions) *Future {
	if ctx == nil {
		ctx = context.Background()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if ctx.Err() != nil {
		return rejected(ErrCancelled)
	}
	if float64(len(s.queue)) >= s.maxQueued {
		return rejected(ErrQueueFull)
	}
	child, cancel := context.WithCancel(ctx)
	j := &job{priority: priority, enqueuedAt: s.clock.Now(), sequence: s.sequence, ctx: child, task: task, future: &Future{done: make(chan struct{})}}
	if priority > 0 {
		j.overdue = numberCopy(options.OverdueLearningAt)
	}
	s.sequence++
	j.future.cancel = func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		cancel()
		s.cancelQueuedLocked(j)
	}
	j.stopCancel = context.AfterFunc(child, func() { s.cancelQueued(j) })
	s.queue = append(s.queue, j)
	s.drainLocked()
	return j.future
}
func (s *Scheduler) SubmitModel(ctx context.Context, basePriority float64, task Task) *Future {
	var options SchedulingOptions
	if deadline, ok := GetOverdueLearningAt(ctx); ok {
		options.OverdueLearningAt = &deadline
	}
	return s.Submit(ctx, EffectiveModelPriority(ctx, basePriority), task, options)
}
func (s *Scheduler) cancelQueued(j *job) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cancelQueuedLocked(j)
}
func (s *Scheduler) cancelQueuedLocked(j *job) {
	for i, candidate := range s.queue {
		if candidate == j {
			s.removeQueuedLocked(i)
			s.settleLocked(j, nil, ErrCancelled)
			s.drainLocked()
			return
		}
	}
}
func (s *Scheduler) removeQueuedLocked(index int) {
	copy(s.queue[index:], s.queue[index+1:])
	s.queue[len(s.queue)-1] = nil
	s.queue = s.queue[:len(s.queue)-1]
}
func (s *Scheduler) settleLocked(j *job, value any, err error) {
	if j.settled {
		return
	}
	j.settled = true
	if j.stopCancel != nil {
		j.stopCancel()
	}
	j.future.value = value
	j.future.err = err
	close(j.future.done)
	// Do not cancel the captured context on completion: nested work may still
	// depend on it. Its lifetime belongs to the submitting caller, as AbortSignal
	// does in the source. Removing the cancellation callback releases job ownership.
}
func (s *Scheduler) complete(j *job, value any, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if j.settled {
		return
	}
	s.settleLocked(j, value, err)
	s.running--
	if j.priority > 0 {
		s.runningNonForeground--
	}
	s.drainLocked()
}
func (s *Scheduler) enqueueLocked(callback func()) {
	s.callbacks = append(s.callbacks, callback)
	if !s.dispatching {
		s.dispatching = true
		s.launch(s.dispatch)
	}
}
func (s *Scheduler) dispatch() {
	for {
		s.mu.Lock()
		if len(s.callbacks) == 0 {
			s.dispatching = false
			s.mu.Unlock()
			return
		}
		f := s.callbacks[0]
		s.callbacks[0] = nil
		s.callbacks = s.callbacks[1:]
		s.mu.Unlock()
		f()
	}
}
func (s *Scheduler) start(j *job) {
	// The source removes its abort listener at admission. Do not recheck here:
	// cancellation between admission and invocation must not skip the callback.
	defer func() {
		if failure := recover(); failure != nil {
			err, ok := failure.(error)
			if !ok {
				err = ErrWorkFailed
			}
			s.queueCompletion(j, nil, err)
		}
	}()
	j.task(j.ctx, func(value any, err error) {
		// Promise continuations do not run inline in the starter. Deferring result
		// settlement also keeps already admitted starters ahead of nested completion.
		s.queueCompletion(j, value, err)
	})
}
func (s *Scheduler) queueCompletion(j *job, value any, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if j.settled || j.completionQueued {
		return
	}
	j.completionQueued = true
	s.enqueueLocked(func() { s.complete(j, value, err) })
}
func (s *Scheduler) effectivePriority(j *job) float64 {
	if j.priority == 2 && s.backgroundAgingMS != nil && s.clock.Now()-j.enqueuedAt >= *s.backgroundAgingMS {
		return 1
	}
	return j.priority
}
func (s *Scheduler) drainLocked() {
	if s.rateTimer != nil {
		s.rateTimer.Stop()
		s.rateTimer = nil
	}
	s.timerGeneration++
	now := s.clock.Now()
	kept := s.starts[:0]
	for _, started := range s.starts {
		if started > now-60_000 {
			kept = append(kept, started)
		}
	}
	s.starts = kept
	overdueWaiting := false
	for _, j := range s.queue {
		if j.overdue != nil && now >= *j.overdue {
			overdueWaiting = true
			break
		}
	}
	if !overdueWaiting {
		s.foregroundStartsWhileLearningWaits = 0
	}
	for float64(s.running) < s.concurrency && len(s.queue) > 0 {
		sort.SliceStable(s.queue, func(i, k int) bool {
			a, b := s.queue[i], s.queue[k]
			delta := s.effectivePriority(a) - s.effectivePriority(b)
			// JavaScript's comparator uses sequence when subtraction is zero or NaN.
			if delta == 0 || math.IsNaN(delta) {
				return a.sequence < b.sequence
			}
			return delta < 0
		})
		nonForegroundLimit := float64(1)
		if s.concurrency != 1 {
			nonForegroundLimit = math.Max(1, s.concurrency-s.reserveForeground)
		}
		overdueIndex := -1
		for i, j := range s.queue {
			if j.overdue != nil && s.clock.Now() >= *j.overdue && (overdueIndex < 0 || j.sequence < s.queue[overdueIndex].sequence) {
				overdueIndex = i
			}
		}
		index := -1
		if overdueIndex >= 0 && s.foregroundStartsWhileLearningWaits >= 4 && float64(s.runningNonForeground) < nonForegroundLimit {
			index = overdueIndex
		} else {
			for i, j := range s.queue {
				if j.priority <= 0 || float64(s.runningNonForeground) < nonForegroundLimit {
					index = i
					break
				}
			}
		}
		if index < 0 {
			return
		}
		if s.callsPerMinute != 0 && float64(len(s.starts)) >= s.callsPerMinute {
			generation := s.timerGeneration
			s.rateTimer = s.clock.AfterFunc(math.Max(1, s.starts[0]+60_000-s.clock.Now()), func() {
				s.mu.Lock()
				defer s.mu.Unlock()
				if generation == s.timerGeneration {
					s.drainLocked()
				}
			})
			return
		}
		j := s.queue[index]
		s.removeQueuedLocked(index)
		if j.stopCancel != nil {
			j.stopCancel()
		}
		if j.ctx.Err() != nil {
			s.settleLocked(j, nil, ErrCancelled)
			continue
		}
		nonForeground := j.priority > 0
		if (j.overdue != nil && s.clock.Now() >= *j.overdue) || overdueIndex < 0 {
			s.foregroundStartsWhileLearningWaits = 0
		} else if !nonForeground && s.foregroundStartsWhileLearningWaits < 4 {
			s.foregroundStartsWhileLearningWaits++
		}
		s.running++
		if s.callsPerMinute != 0 {
			s.starts = append(s.starts, s.clock.Now())
		}
		if nonForeground {
			s.runningNonForeground++
		}
		s.enqueueLocked(func() { s.start(j) })
	}
}
