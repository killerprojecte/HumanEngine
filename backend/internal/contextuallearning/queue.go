// Package contextuallearning owns ephemeral, single-active contextual learning.
// It has no durable queue or startup replay, and never retries a failed job.
package contextuallearning

import (
	"container/list"
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"sort"
)

type Job struct {
	SceneID, Key string
	SourceIDs    []string
	Validate     func() bool
	Run          func(context.Context) (any, error)
	Committed    func(any) bool
}
type Event struct {
	JobID     string `json:"jobId"`
	Status    string `json:"status"`
	Committed bool   `json:"committed,omitempty"`
}
type EnqueueResult struct {
	Status string `json:"status"`
	JobID  string `json:"jobId,omitempty"`
	Reason string `json:"reason,omitempty"`
}
type Stats struct {
	Accepted  uint64 `json:"accepted"`
	Completed uint64 `json:"completed"`
	Failed    uint64 `json:"failed"`
	Discarded uint64 `json:"discarded"`
	Cancelled uint64 `json:"cancelled"`
	Queued    int    `json:"queued"`
	Running   int    `json:"running"`
	Disposed  bool   `json:"disposed"`
}
type QueueOptions struct {
	ForegroundBusy func() bool
	// Nil uses the exact source defaults. Explicit zero is invalid.
	MaxQueued, MaxRemembered, PollMS *int64
	OnEvent                          func(Event)
}
type entry struct {
	identity, id string
	job          Job
	ctx          context.Context
	cancel       context.CancelFunc
	cancelled    bool
}
type rememberedEntry struct{ identity, id string }
type Queue struct {
	eventIdle                 chan struct{}
	mu                        sync.Mutex
	options                   QueueOptions
	maxQueued, maxRemembered  int64
	pollMS                    int64
	pending                   []*entry
	active                    *entry
	identities                map[string]string
	remembered                map[string]*list.Element
	order                     list.List
	totals                    Stats
	sequence                  uint64
	wake                      chan struct{}
	idle, done, admissionIdle chan struct{}
	admissions                int
	started, emitting         bool
	events                    []Event
}

func NewQueue(o QueueOptions) (*Queue, error) {
	value := func(p *int64, d int64) (int64, error) {
		if p == nil {
			return d, nil
		}
		if *p < 1 || *p > 9007199254740991 {
			return 0, errors.New("invalid_contextual_learning_queue_option")
		}
		return *p, nil
	}
	q := &Queue{options: o, identities: map[string]string{}, remembered: map[string]*list.Element{}, wake: make(chan struct{}, 1), idle: make(chan struct{}), done: make(chan struct{}), admissionIdle: make(chan struct{})}
	var err error
	if q.maxQueued, err = value(o.MaxQueued, 128); err != nil {
		return nil, err
	}
	if q.maxRemembered, err = value(o.MaxRemembered, 512); err != nil {
		return nil, err
	}
	if q.pollMS, err = value(o.PollMS, 100); err != nil {
		return nil, err
	}
	close(q.idle)
	q.eventIdle = make(chan struct{})
	close(q.eventIdle)
	close(q.admissionIdle)
	return q, nil
}
func safelyValid(f func() bool) (ok bool) {
	defer func() {
		if recover() != nil {
			ok = false
		}
	}()
	return f != nil && f()
}
func canonicalSources(ids []string) ([]string, error) {
	set := map[string]bool{}
	out := []string{}
	units := map[string][]uint16{}
	for _, id := range ids {
		if id == "" {
			return nil, errors.New("invalid_source")
		}
		if !set[id] {
			u, e := jsjson.UTF16(id)
			if e != nil {
				return nil, e
			}
			units[id] = u
			set[id] = true
			out = append(out, id)
		}
	}
	// JavaScript Array.sort compares UTF-16 code units, not UTF-8 bytes.
	sort.Slice(out, func(i, j int) bool {
		a := units[out[i]]
		b := units[out[j]]
		for n := 0; n < len(a) && n < len(b); n++ {
			if a[n] != b[n] {
				return a[n] < b[n]
			}
		}
		return len(a) < len(b)
	})
	return out, nil
}
func (q *Queue) Enqueue(j Job) EnqueueResult {
	reject := func(s string) EnqueueResult { return EnqueueResult{Status: "rejected", Reason: s} }
	q.mu.Lock()
	if q.totals.Disposed {
		q.mu.Unlock()
		return reject("disposed")
	}
	if q.admissions == 0 {
		q.admissionIdle = make(chan struct{})
	}
	q.admissions++
	q.mu.Unlock()
	defer func() {
		q.mu.Lock()
		q.admissions--
		if q.admissions == 0 {
			close(q.admissionIdle)
		}
		q.mu.Unlock()
	}()
	if j.SceneID == "" || j.Key == "" || len(j.SourceIDs) == 0 || j.Run == nil || !safelyValid(j.Validate) {
		return reject("invalid")
	}
	ids, e := canonicalSources(j.SourceIDs)
	if e != nil {
		return reject("invalid")
	}
	j.SourceIDs = ids
	a := make([]any, len(ids))
	for i, id := range ids {
		a[i] = id
	}
	identity, e := jsjson.Stringify([]any{j.SceneID, j.Key, a})
	if e != nil {
		return reject("invalid")
	}
	q.mu.Lock()
	if q.totals.Disposed {
		q.mu.Unlock()
		return reject("disposed")
	}
	prior := q.identities[identity]
	if p := q.remembered[identity]; prior == "" && p != nil {
		prior = p.Value.(rememberedEntry).id
	}
	if prior != "" {
		q.mu.Unlock()
		return EnqueueResult{Status: "duplicate", JobID: prior}
	}
	count := len(q.pending)
	if q.active != nil {
		count++
	}
	if int64(count) >= q.maxQueued {
		q.mu.Unlock()
		return reject("capacity")
	}
	if count == 0 {
		q.idle = make(chan struct{})
	}
	q.sequence++
	ctx, cancel := context.WithCancel(context.Background())
	v := &entry{identity: identity, id: fmt.Sprintf("contextual-learning-%d", q.sequence), job: j, ctx: ctx, cancel: cancel}
	q.identities[identity] = v.id
	q.pending = append(q.pending, v)
	q.totals.Accepted++
	q.events = append(q.events, Event{JobID: v.id, Status: "queued"})
	start := !q.started
	if start {
		q.started = true
	}
	q.mu.Unlock()
	q.emit()
	if start {
		go q.loop()
	}
	q.Resume()
	return EnqueueResult{Status: "queued", JobID: v.id}
}
func (q *Queue) Resume() {
	select {
	case q.wake <- struct{}{}:
	default:
	}
}
func (q *Queue) Stats() Stats {
	q.mu.Lock()
	defer q.mu.Unlock()
	s := q.totals
	s.Queued = len(q.pending)
	if q.active != nil {
		s.Running = 1
	}
	return s
}
func (q *Queue) WhenIdle(ctx context.Context) error {
	q.mu.Lock()
	done := q.idle
	q.mu.Unlock()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
func (q *Queue) CloseAndWait(ctx context.Context) error {
	q.Dispose()
	q.mu.Lock()
	admissions := q.admissionIdle
	q.mu.Unlock()
	for _, done := range []<-chan struct{}{admissions, q.done} {
		select {
		case <-done:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	q.mu.Lock()
	events := q.eventIdle
	q.mu.Unlock()
	select {
	case <-events:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
func (q *Queue) CancelScene(scene string) {
	q.cancelWhere(func(e *entry) bool { return e.job.SceneID == scene }, false)
}
func (q *Queue) Recall(scene, source string) {
	q.cancelWhere(func(e *entry) bool {
		if e.job.SceneID != scene {
			return false
		}
		for _, id := range e.job.SourceIDs {
			if id == source {
				return true
			}
		}
		return false
	}, false)
}
func (q *Queue) Dispose() { q.cancelWhere(func(*entry) bool { return true }, true) }
func (q *Queue) notifyIdleLocked() {
	if q.active == nil && len(q.pending) == 0 {
		select {
		case <-q.idle:
		default:
			close(q.idle)
		}
	}
}
func (q *Queue) cancelWhere(match func(*entry) bool, dispose bool) {
	q.mu.Lock()
	if dispose {
		q.totals.Disposed = true
	}
	// Source terminal ordering is reverse pending order. Compact once: O(n).
	keep := make([]*entry, 0, len(q.pending))
	for i := len(q.pending) - 1; i >= 0; i-- {
		v := q.pending[i]
		if match(v) {
			v.cancelled = true
			v.cancel()
			q.terminalLocked(v, "cancelled", false)
		}
	}
	for _, v := range q.pending {
		if !v.cancelled {
			keep = append(keep, v)
		}
	}
	q.pending = keep
	if q.active != nil && match(q.active) {
		q.active.cancelled = true
		q.active.cancel()
	}
	q.notifyIdleLocked()
	if dispose && !q.started {
		q.started = true
		close(q.done)
	}
	q.mu.Unlock()
	q.emit()
	q.Resume()
}
func (q *Queue) terminalLocked(v *entry, status string, committed bool) {
	delete(q.identities, v.identity)
	if old := q.remembered[v.identity]; old != nil {
		old.Value = rememberedEntry{v.identity, v.id}
	} else {
		q.remembered[v.identity] = q.order.PushBack(rememberedEntry{v.identity, v.id})
	}
	for int64(q.order.Len()) > q.maxRemembered {
		head := q.order.Front()
		delete(q.remembered, head.Value.(rememberedEntry).identity)
		q.order.Remove(head)
	}
	switch status {
	case "completed":
		q.totals.Completed++
	case "failed":
		q.totals.Failed++
	case "discarded":
		q.totals.Discarded++
	case "cancelled":
		q.totals.Cancelled++
	}
	q.events = append(q.events, Event{JobID: v.id, Status: status, Committed: committed})
}
func (q *Queue) emit() {
	q.mu.Lock()
	if q.emitting {
		q.mu.Unlock()
		return
	}
	q.emitting = true
	q.eventIdle = make(chan struct{})
	for len(q.events) > 0 {
		event := q.events[0]
		q.events[0] = Event{}
		q.events = q.events[1:]
		q.mu.Unlock()
		func() {
			defer func() { _ = recover() }()
			if q.options.OnEvent != nil {
				q.options.OnEvent(event)
			}
		}()
		q.mu.Lock()
	}
	q.emitting = false
	close(q.eventIdle)
	q.mu.Unlock()
}
func (q *Queue) loop() {
	defer close(q.done)
	for {
		q.mu.Lock()
		disposed := q.totals.Disposed
		pending := len(q.pending) > 0
		q.mu.Unlock()
		if disposed {
			return
		}
		if !pending {
			<-q.wake
			continue
		}
		busy := false
		if q.options.ForegroundBusy != nil {
			busy = !safelyValid(func() bool { return !q.options.ForegroundBusy() })
		}
		if busy {
			// Node clamps an overflowing setTimeout delay to 1ms, not a new cap.
			delay := q.pollMS
			if delay > 2147483647 {
				delay = 1
			}
			timer := time.NewTimer(time.Duration(delay) * time.Millisecond)
			select {
			case <-q.wake:
				if !timer.Stop() {
					select {
					case <-timer.C:
					default:
					}
				}
			case <-timer.C:
			}
			continue
		}
		q.mu.Lock()
		if q.totals.Disposed {
			q.mu.Unlock()
			return
		}
		if len(q.pending) == 0 {
			q.mu.Unlock()
			continue
		}
		v := q.pending[0]
		q.pending[0] = nil
		q.pending = q.pending[1:]
		q.active = v
		q.mu.Unlock()
		q.execute(v)
	}
}
func (q *Queue) execute(v *entry) {
	status := "failed"
	committed := false
	func() {
		defer func() {
			if recover() != nil {
				status = "failed"
			}
		}()
		if !safelyValid(v.job.Validate) {
			status = "discarded"
			return
		}
		q.mu.Lock()
		q.events = append(q.events, Event{JobID: v.id, Status: "running"})
		q.mu.Unlock()
		q.emit()
		if v.ctx.Err() != nil {
			status = "cancelled"
			return
		}
		result, err := v.job.Run(v.ctx)
		if err != nil {
			return
		}
		if v.job.Committed != nil {
			committed = safelyValid(func() bool { return v.job.Committed(result) })
		}
		if committed || safelyValid(v.job.Validate) {
			status = "completed"
		} else {
			status = "discarded"
		}
	}()
	q.mu.Lock()
	if v.cancelled {
		status = "cancelled"
	}
	q.terminalLocked(v, status, committed)
	q.mu.Unlock()
	q.emit()
	v.cancel()
	q.mu.Lock()
	q.active = nil
	q.notifyIdleLocked()
	q.mu.Unlock()
}
