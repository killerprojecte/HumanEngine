// Package learningruntime owns the ephemeral source-learning queue. It never
// reloads source payloads at startup and never replays a batch after write intent.
package learningruntime

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"math"
	"sync"
	"time"

	"github.com/killerprojecte/HumanEngine/backend/internal/botruntime"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/learningadmission"
	"github.com/killerprojecte/HumanEngine/backend/internal/learningbatch"
	"github.com/killerprojecte/HumanEngine/backend/internal/modelservice"
	"github.com/killerprojecte/HumanEngine/backend/internal/providertransport"
	"github.com/killerprojecte/HumanEngine/backend/internal/quiescence"
	"github.com/killerprojecte/HumanEngine/backend/internal/scheduler"
	"github.com/killerprojecte/HumanEngine/backend/internal/topiccollation"
)

type Settings struct {
	BatchMS, MaxWaitMS, MinIntervalMS       int64
	QueueMaxMessages, QueueMaxTotalMessages int64
	RetryAttempts                           int
	RetryBaseMS                             int64
}

func DefaultSettings() Settings { return Settings{30000, 120000, 60000, 2048, 8192, 3, 1000} }
func (s Settings) admission() learningadmission.Options {
	o := learningadmission.Defaults()
	o.QuietMS = s.BatchMS
	o.MaxWaitMS = max(s.BatchMS, s.MaxWaitMS)
	o.MinIntervalMS = s.MinIntervalMS
	o.MaxSourcesPerScene = s.QueueMaxMessages
	o.MaxSourcesTotal = s.QueueMaxTotalMessages
	return o
}
func (s Settings) validate() error {
	if err := s.admission().Validate(); err != nil {
		return err
	}
	if s.RetryAttempts < 1 || s.RetryBaseMS < 0 || s.RetryBaseMS > 9007199254740991 {
		return errors.New("invalid_learning_retry_option")
	}
	return nil
}

// Source reads must be fresh reads of the real source/scene owner. They are
// invoked outside the coordinator lock, so recall transactions cannot deadlock.
type Owner interface {
	Enabled(context.Context, string) (bool, error)
	Recalled(context.Context, string, string) (bool, error)
	CurrentSource(context.Context, *botruntime.Object) (bool, error)
	ForegroundBusy() bool
	Event(Event)
}
type BatchRunner interface {
	Run(context.Context, string, []*botruntime.Object, *learningbatch.Progress) (bool, error)
}
type Clock interface {
	Now() int64
	AfterFunc(int64, func()) func()
}
type realClock struct{}

func (realClock) Now() int64 { return time.Now().UnixMilli() }
func (realClock) AfterFunc(ms int64, f func()) func() {
	if ms < 1 || ms > 2147483647 {
		ms = 1
	}
	t := time.AfterFunc(time.Duration(ms)*time.Millisecond, f)
	return func() { t.Stop() }
}

type Options struct {
	Settings Settings
	Clock    Clock
	Collator *topiccollation.Collator
	NewID    func() string
}
type Event struct {
	Stage, SceneID, Status, Reason, NextStep, TraceID, BatchID, ParentTraceID string
	LinkedTraceIDs                                                            []string
	Attempt, InputCount, QueueDepth, RetryDelayMS, UnconfirmedCandidateCount  int
	Totals                                                                    learningbatch.Totals
}
type Failure struct {
	ID           string `json:"id"`
	SceneID      string `json:"sceneId"`
	Attempt      int    `json:"attempt"`
	MessageCount int    `json:"messageCount"`
	Partial      bool   `json:"partial"`
	CanRetry     bool   `json:"canRetry"`
	CreatedAt    string `json:"createdAt"`
	messages     []*botruntime.Object
}
type Status struct {
	Accepted  int       `json:"accepted"`
	Completed int       `json:"completed"`
	Failed    int       `json:"failed"`
	Overflow  int       `json:"overflow"`
	Retried   int       `json:"retried"`
	Paused    int       `json:"paused"`
	Failures  []Failure `json:"failures"`
	Queued    int       `json:"queued"`
	Retrying  int       `json:"retrying"`
	Active    int       `json:"active"`
}
type retryBatch struct {
	messages               []*botruntime.Object
	attempt                int
	due                    int64
	batchID, parentTraceID string
	linkedTraceIDs         []string
}
type activeBatch struct {
	messages               []*botruntime.Object
	cancel                 context.CancelCauseFunc
	ctx                    context.Context
	ticket                 *learningadmission.Batch
	attempt                int
	batchID, parentTraceID string
	linkedTraceIDs         []string
	progress               learningbatch.Progress
}
type timerEntry struct {
	cancel     func()
	due        int64
	generation uint64
}
type sourceKey struct{ scene, id string }

// Pending tokens fence only in-flight admissions, without retaining source IDs.
type pendingAdmission struct{ scene, id, reason string }

var errRecalled = errors.New("source_recalled")
var errDisabled = errors.New("learning_disabled")
var errDisposed = errors.New("runtime_stopped")

type Coordinator struct {
	mu           sync.Mutex
	owner        Owner
	runner       BatchRunner
	clock        Clock
	newID        func() string
	settings     Settings
	admission    *learningadmission.Admission
	disposed     bool
	generation   uint64
	overdueScene string
	queued       map[string][]*botruntime.Object
	retries      map[string]*retryBatch
	active       map[string]*activeBatch
	survivors    map[string][]*botruntime.Object
	traces       map[sourceKey]string
	failures     map[string]*Failure
	failureOrder []string
	timers       map[string]timerEntry
	counts       Status
	work         quiescence.Group
	pending      map[*pendingAdmission]struct{}
}

func New(owner Owner, runner BatchRunner, o Options) (*Coordinator, error) {
	if owner == nil || runner == nil {
		return nil, errors.New("learning_runtime_owners_required")
	}
	if e := o.Settings.validate(); e != nil {
		return nil, e
	}
	if o.Clock == nil {
		o.Clock = realClock{}
	}
	if o.NewID == nil {
		o.NewID = randomID
	}
	a, e := learningadmission.New(o.Settings.admission(), o.Clock.Now, o.Collator)
	if e != nil {
		return nil, e
	}
	return &Coordinator{owner: owner, runner: runner, clock: o.Clock, newID: o.NewID, settings: o.Settings, admission: a, queued: map[string][]*botruntime.Object{}, retries: map[string]*retryBatch{}, active: map[string]*activeBatch{}, survivors: map[string][]*botruntime.Object{}, traces: map[sourceKey]string{}, failures: map[string]*Failure{}, timers: map[string]timerEntry{}, pending: map[*pendingAdmission]struct{}{}}, nil
}
func randomID() string {
	var b [16]byte
	if _, e := rand.Read(b[:]); e != nil {
		panic(e)
	}
	b[6] = (b[6] & 15) | 64
	b[8] = (b[8] & 63) | 128
	return hex.EncodeToString(b[:])
}
func str(o *botruntime.Object, k string) string {
	if o == nil {
		return ""
	}
	v, _ := o.Get(k)
	s, _ := v.(string)
	return s
}
func clone(o *botruntime.Object) *botruntime.Object {
	if o == nil {
		return nil
	}
	n := new(jsjson.Object)
	for _, m := range o.Members() {
		_ = n.Set(m.Key, cloneValue(m.Value))
	}
	return n
}
func cloneValue(v any) any {
	switch v := v.(type) {
	case *jsjson.Object:
		return clone(v)
	case []any:
		r := make([]any, len(v))
		for i, x := range v {
			r[i] = cloneValue(x)
		}
		return r
	default:
		return v
	}
}
func remove(ms []*botruntime.Object, id string) []*botruntime.Object {
	r := make([]*botruntime.Object, 0, len(ms))
	for _, m := range ms {
		if str(m, "id") != id {
			r = append(r, m)
		}
	}
	return r
}
func (c *Coordinator) emit(e Event) { defer func() { _ = recover() }(); c.owner.Event(e) }
func (c *Coordinator) statusLocked() Status {
	s := c.counts
	s.Failures = []Failure{}
	for _, id := range c.failureOrder {
		if f := c.failures[id]; f != nil {
			s.Paused += len(f.messages)
			s.Failures = append(s.Failures, *f)
			s.Failures[len(s.Failures)-1].messages = nil
		}
	}
	for _, q := range c.queued {
		s.Queued += len(q)
	}
	for _, r := range c.retries {
		s.Retrying += len(r.messages)
	}
	for _, a := range c.active {
		s.Active += len(a.messages)
	}
	return s
}
func (c *Coordinator) Status() Status { c.mu.Lock(); defer c.mu.Unlock(); return c.statusLocked() }
func (c *Coordinator) SettingsChanged(settings Settings) error {
	if e := settings.validate(); e != nil {
		return e
	}
	c.mu.Lock()
	c.settings = settings
	e := c.admission.Configure(settings.admission())
	scenes := make([]string, 0, len(c.queued))
	for s := range c.queued {
		scenes = append(scenes, s)
	}
	c.mu.Unlock()
	if e != nil {
		return e
	}
	for _, s := range scenes {
		c.schedule(s, true)
	}
	return nil
}
func (c *Coordinator) Enqueue(ctx context.Context, m *botruntime.Object) (string, error) {
	scene, id := str(m, "sceneId"), str(m, "id")
	trace := c.newID()
	event := Event{Stage: "learning.admission", SceneID: scene, TraceID: trace, InputCount: 1, Status: "skipped", NextStep: "none"}
	reject := func(reason string) (string, error) { event.Reason = reason; c.emit(event); return reason, nil }
	if str(m, "senderId") == "bot" {
		return reject("bot_source")
	}
	c.mu.Lock()
	if c.disposed {
		c.mu.Unlock()
		return reject("runtime_stopped")
	}
	pending := &pendingAdmission{scene: scene, id: id}
	c.pending[pending] = struct{}{}
	done, _ := c.work.Start()
	c.mu.Unlock()
	defer done()
	defer func() { c.mu.Lock(); delete(c.pending, pending); c.mu.Unlock() }()
	enabled, e := c.owner.Enabled(ctx, scene)
	if e != nil {
		return "", e
	}
	if !enabled {
		return reject("learning_disabled")
	}
	if str(m, "origin") != "live" {
		return reject("not_live_source")
	}
	if str(m, "status") != "available" {
		return reject("source_unavailable")
	}
	recalled, e := c.owner.Recalled(ctx, scene, id)
	if e != nil {
		return "", e
	}
	if recalled {
		return reject("source_unavailable")
	}
	current, e := c.owner.CurrentSource(ctx, m)
	if e != nil {
		return "", e
	}
	if !current {
		return reject("source_unavailable")
	}
	copy := clone(m)
	c.mu.Lock()
	if c.disposed {
		c.mu.Unlock()
		return reject("runtime_stopped")
	}
	if pending.reason != "" {
		reason := pending.reason
		c.mu.Unlock()
		return reject(reason)
	}
	existing := append([]*botruntime.Object{}, c.queued[scene]...)
	if r := c.retries[scene]; r != nil {
		existing = append(existing, r.messages...)
	}
	existing = append(existing, c.survivors[scene]...)
	for _, f := range c.failures {
		if f.SceneID == scene {
			existing = append(existing, f.messages...)
		}
	}
	for _, x := range existing {
		if str(x, "id") == id {
			c.mu.Unlock()
			event.NextStep = "existing_batch"
			return reject("duplicate_source")
		}
	}
	depth := len(existing) - len(c.survivors[scene])
	if a := c.active[scene]; a != nil {
		depth += len(a.messages)
	}
	status := c.statusLocked()
	if int64(depth) >= c.settings.QueueMaxMessages || int64(status.Queued+status.Retrying+status.Active+status.Paused) >= c.settings.QueueMaxTotalMessages {
		c.counts.Overflow++
		c.mu.Unlock()
		event.QueueDepth = depth
		event.NextStep = "inspect_learning_queue"
		return reject("learning_queue_full")
	}
	admitted, e := c.admission.Admit(scene, id, "realtime")
	if e != nil {
		c.mu.Unlock()
		return "", e
	}
	if admitted != "accepted" {
		c.mu.Unlock()
		if admitted == "duplicate" {
			return reject("duplicate_source")
		}
		return reject("learning_queue_full")
	}
	c.queued[scene] = append(c.queued[scene], copy)
	c.traces[sourceKey{scene, id}] = trace
	c.counts.Accepted++
	event.QueueDepth = len(c.queued[scene])
	c.mu.Unlock()
	event.Status = "completed"
	event.NextStep = "learning_batch"
	c.emit(event)
	// Logging can synchronously recall the just-admitted source. Schedule from the
	// current queue, never a retained payload list or a re-admission of this input.
	c.schedule(scene, false)
	return "accepted", nil
}
func (c *Coordinator) Recall(scene, id string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for p := range c.pending {
		if p.scene == scene && p.id == id {
			p.reason = "source_unavailable"
		}
	}
	c.admission.Recall(scene, id, false)
	delete(c.traces, sourceKey{scene, id})
	c.queued[scene] = remove(c.queued[scene], id)
	for _, f := range c.failures {
		if f.SceneID == scene {
			f.messages = remove(f.messages, id)
		}
	}
	if r := c.retries[scene]; r != nil {
		r.messages = remove(r.messages, id)
	}
	if active, ok := c.survivors[scene]; ok {
		c.survivors[scene] = remove(active, id)
	}
	if a := c.active[scene]; a != nil {
		a.cancel(errRecalled)
	}
}
func (c *Coordinator) removeFailureLocked(id string) {
	delete(c.failures, id)
	for i, key := range c.failureOrder {
		if key == id {
			c.failureOrder = append(c.failureOrder[:i], c.failureOrder[i+1:]...)
			break
		}
	}
}
func (c *Coordinator) cancelLocked(scene string) {
	for p := range c.pending {
		if p.scene == scene {
			p.reason = "learning_disabled"
		}
	}
	c.admission.CancelScene(scene)
	delete(c.survivors, scene)
	delete(c.queued, scene)
	delete(c.retries, scene)
	for k := range c.traces {
		if k.scene == scene {
			delete(c.traces, k)
		}
	}
	for _, id := range append([]string{}, c.failureOrder...) {
		if c.failures[id].SceneID == scene {
			c.removeFailureLocked(id)
		}
	}
	if t, ok := c.timers[scene]; ok {
		t.cancel()
		delete(c.timers, scene)
	}
	if a := c.active[scene]; a != nil {
		a.cancel(errDisabled)
	}
}
func (c *Coordinator) CancelScene(scene string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.cancelLocked(scene)
}
func (c *Coordinator) Discard(id string) bool {
	c.mu.Lock()
	f := c.failures[id]
	if f != nil {
		c.removeFailureLocked(id)
	}
	c.mu.Unlock()
	if f == nil {
		return false
	}
	c.emit(Event{SceneID: f.SceneID, Reason: "learning_failure_discarded", Status: "completed"})
	return true
}
func (c *Coordinator) Retry(ctx context.Context, id string) (bool, error) {
	c.mu.Lock()
	f := c.failures[id]
	if f == nil || !f.CanRetry || c.disposed {
		c.mu.Unlock()
		return false, nil
	}
	done, _ := c.work.Start()
	defer done()
	snapshot := *f
	snapshot.messages = append([]*botruntime.Object{}, f.messages...)
	c.mu.Unlock()
	enabled, e := c.owner.Enabled(ctx, snapshot.SceneID)
	if e != nil || !enabled {
		return false, e
	}
	eligible, e := c.eligible(ctx, snapshot.SceneID, snapshot.messages)
	if e != nil {
		return false, e
	}
	c.mu.Lock()
	if c.disposed || c.failures[id] != f {
		c.mu.Unlock()
		return false, nil
	}
	// A recall racing eligibility must not restore a removed revision.
	remaining := map[*botruntime.Object]bool{}
	for _, m := range f.messages {
		remaining[m] = true
	}
	filtered := eligible[:0]
	for _, m := range eligible {
		if remaining[m] {
			filtered = append(filtered, m)
		}
	}
	eligible = filtered
	c.removeFailureLocked(id)
	for _, m := range eligible {
		_, _ = c.admission.Admit(f.SceneID, str(m, "id"), "realtime")
	}
	if len(eligible) > 0 {
		c.queued[f.SceneID] = append(eligible, c.queued[f.SceneID]...)
	}
	c.mu.Unlock()
	if len(eligible) > 0 {
		c.schedule(f.SceneID, true)
	}
	return true, nil
}
func (c *Coordinator) eligible(ctx context.Context, scene string, messages []*botruntime.Object) ([]*botruntime.Object, error) {
	r := make([]*botruntime.Object, 0, len(messages))
	for _, m := range messages {
		if str(m, "status") != "available" {
			continue
		}
		recalled, e := c.owner.Recalled(ctx, scene, str(m, "id"))
		if e != nil {
			return nil, e
		}
		if !recalled {
			r = append(r, m)
		}
	}
	return r, nil
}
func (c *Coordinator) schedule(scene string, repair bool) {
	foreground := c.owner.ForegroundBusy()
	var eligible []*botruntime.Object
	if repair {
		c.mu.Lock()
		snapshot := append([]*botruntime.Object{}, c.queued[scene]...)
		c.mu.Unlock()
		var e error
		eligible, e = c.eligible(context.Background(), scene, snapshot)
		if e != nil {
			c.emit(Event{Stage: "learning.batch", SceneID: scene, Status: "failed", Reason: "learning_source_read_failed"})
			return
		}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.disposed || (len(c.queued[scene]) == 0 && c.retries[scene] == nil) || c.active[scene] != nil {
		return
	}
	if repair {
		current := map[*botruntime.Object]bool{}
		for _, m := range c.queued[scene] {
			current[m] = true
		}
		for _, m := range eligible {
			if current[m] {
				_, _ = c.admission.Admit(scene, str(m, "id"), "realtime")
			}
		}
	}
	c.scheduleLocked(scene, foreground)
}
func (c *Coordinator) scheduleLocked(scene string, foreground bool) {
	if c.disposed || (len(c.queued[scene]) == 0 && c.retries[scene] == nil) || c.active[scene] != nil {
		return
	}
	retry := c.retries[scene]
	s := c.admission.Inspect(scene, learningadmission.Capacity{})
	if retry == nil && (s.State == "idle" || s.State == "disabled" || s.State == "active") {
		return
	}
	now := c.clock.Now()
	delay := int64(1000)
	if retry != nil {
		delay = max(0, retry.due-now)
	} else if s.State == "waiting" || s.State == "ready" {
		delay = max(0, s.DueAt-now)
	}
	if foreground {
		delay = max(1000, delay)
	}
	due := now + delay
	if t, ok := c.timers[scene]; ok {
		if t.due <= due {
			return
		}
		t.cancel()
	}
	c.generation++
	g := c.generation
	cancel := c.clock.AfterFunc(delay, func() { c.fire(scene, g) })
	c.timers[scene] = timerEntry{cancel, due, g}
}
func (c *Coordinator) oldestLocked(maxWait int64) string {
	best := ""
	at := int64(math.MaxInt64)
	seen := map[string]bool{}
	for s := range c.queued {
		seen[s] = true
	}
	for s := range c.retries {
		seen[s] = true
	}
	for s := range seen {
		if c.active[s] != nil {
			continue
		}
		due := int64(math.MaxInt64)
		if r := c.retries[s]; r != nil {
			due = r.due + maxWait
		} else if a := c.admission.Inspect(s, learningadmission.Capacity{}); a.State == "ready" {
			due = a.FirstAdmittedAt + maxWait
		}
		if due <= c.clock.Now() && (best == "" || due < at || (due == at && utf16Less(s, best))) {
			best = s
			at = due
		}
	}
	return best
}
func utf16Less(a, b string) bool {
	aa, _ := jsjson.UTF16(a)
	bb, _ := jsjson.UTF16(b)
	for i := 0; i < len(aa) && i < len(bb); i++ {
		if aa[i] != bb[i] {
			return aa[i] < bb[i]
		}
	}
	return len(aa) < len(bb)
}
func (c *Coordinator) fire(scene string, g uint64) {
	c.mu.Lock()
	first, live := c.timers[scene]
	if !live || first.generation != g || c.disposed {
		c.mu.Unlock()
		return
	}
	callbackDone, accepted := c.work.Start()
	c.mu.Unlock()
	if !accepted {
		return
	}
	defer callbackDone()
	foreground := c.owner.ForegroundBusy()
	enabled, ownerErr := c.owner.Enabled(context.Background(), scene)
	c.mu.Lock()
	t, ok := c.timers[scene]
	if !ok || t.generation != g || c.disposed {
		c.mu.Unlock()
		return
	}
	delete(c.timers, scene)
	if ownerErr != nil || !enabled {
		c.cancelLocked(scene)
		c.mu.Unlock()
		return
	}
	if c.active[scene] != nil {
		c.mu.Unlock()
		return
	}
	now := c.clock.Now()
	maxWait := max(c.settings.BatchMS, c.settings.MaxWaitMS)
	r := c.retries[scene]
	s := c.admission.Inspect(scene, learningadmission.Capacity{})
	overdueAt := int64(math.MaxInt64)
	if r != nil {
		overdueAt = r.due + maxWait
	} else if s.State == "waiting" || s.State == "ready" {
		overdueAt = s.FirstAdmittedAt + maxWait
	}
	overdue := foreground && now >= overdueAt
	if foreground && (!overdue || c.overdueScene != "" || c.oldestLocked(maxWait) != scene) {
		c.scheduleLocked(scene, foreground)
		c.mu.Unlock()
		return
	}
	if r != nil && r.due > now {
		c.scheduleLocked(scene, foreground)
		c.mu.Unlock()
		return
	}
	var ticket *learningadmission.Batch
	if r == nil {
		ticket, _ = c.admission.Take(scene, 60, learningadmission.Capacity{})
		if ticket == nil {
			c.scheduleLocked(scene, foreground)
			c.mu.Unlock()
			return
		}
	}
	delete(c.retries, scene)
	selected := []*botruntime.Object{}
	if r != nil {
		selected = r.messages
	} else {
		ids := map[string]bool{}
		for _, id := range ticket.SourceKeys {
			ids[id] = true
		}
		rest := []*botruntime.Object{}
		for _, m := range c.queued[scene] {
			if ids[str(m, "id")] {
				selected = append(selected, m)
			} else {
				rest = append(rest, m)
			}
		}
		c.queued[scene] = rest
	}
	if len(c.queued[scene]) == 0 {
		delete(c.queued, scene)
	}
	// JS Map preserves the first position and keeps the last duplicate value.
	positions := map[string]int{}
	messages := []*botruntime.Object{}
	for _, m := range selected {
		id := str(m, "id")
		if i, ok := positions[id]; ok {
			messages[i] = m
		} else {
			positions[id] = len(messages)
			messages = append(messages, m)
		}
	}
	child, cancel := context.WithCancelCause(context.Background())
	a := &activeBatch{messages: messages, ctx: child, cancel: cancel, ticket: ticket, attempt: 1, batchID: c.newID()}
	if r != nil {
		a.attempt = r.attempt
		a.batchID = r.batchID
		a.parentTraceID = r.parentTraceID
		a.linkedTraceIDs = append([]string{}, r.linkedTraceIDs...)
	} else {
		seen := map[string]bool{}
		for _, m := range messages {
			key := sourceKey{scene, str(m, "id")}
			trace := c.traces[key]
			if trace != "" {
				if a.parentTraceID == "" {
					a.parentTraceID = trace
				}
				if !seen[trace] {
					a.linkedTraceIDs = append(a.linkedTraceIDs, trace)
					seen[trace] = true
				}
			}
			delete(c.traces, key)
		}
	}
	c.active[scene] = a
	c.survivors[scene] = append([]*botruntime.Object{}, messages...)
	if overdue {
		c.overdueScene = scene
	}
	done, accepted := c.work.Start()
	if !accepted {
		panic("learning_work_registration_after_disposal")
	}
	c.mu.Unlock()
	go func() { defer done(); c.run(scene, a, overdueAt) }()
}
func (c *Coordinator) run(scene string, a *activeBatch, fairAfter int64) {
	ctx, _ := scheduler.WithModelPriority(a.ctx, 1)
	ctx, _ = scheduler.WithOverdueLearning(ctx, float64(fairAfter))
	ctx = providertransport.WithBudgetContext(ctx, a.batchID, "realtime")
	ctx = learningbatch.WithFlow(ctx, a.batchID, a.batchID, a.parentTraceID, a.linkedTraceIDs)
	// Track physical callbacks independently from logical model timeout/cancel.
	var physical quiescence.Group
	root, _ := physical.Start()
	ctx = providertransport.WithPhysicalWork(ctx, &physical)
	var completed bool
	var err error
	defer func() {
		if recover() != nil {
			err = errors.New("learning_batch_failed")
		}
		root()
		_ = physical.Wait(context.Background())
		c.finish(scene, a, completed, err)
	}()
	messages, err := c.eligible(ctx, scene, a.messages)
	c.mu.Lock()
	if len(messages) != len(a.messages) {
		a.messages = messages
		c.survivors[scene] = intersect(c.survivors[scene], messages)
	}
	c.mu.Unlock()
	if err == nil && len(messages) > 0 && ctx.Err() == nil {
		completed, err = c.runner.Run(ctx, scene, messages, &a.progress)
	}
}
func intersect(a, b []*botruntime.Object) []*botruntime.Object {
	set := map[*botruntime.Object]bool{}
	for _, m := range b {
		set[m] = true
	}
	r := []*botruntime.Object{}
	for _, m := range a {
		if set[m] {
			r = append(r, m)
		}
	}
	return r
}
func (c *Coordinator) finish(scene string, a *activeBatch, completed bool, err error) {
	enabled, _ := c.owner.Enabled(context.Background(), scene)
	c.mu.Lock()
	snapshot := append([]*botruntime.Object{}, c.survivors[scene]...)
	c.mu.Unlock()
	eligible, eligibilityErr := c.eligible(context.Background(), scene, snapshot)
	event := Event{Stage: "learning.batch", SceneID: scene, TraceID: a.batchID, BatchID: a.batchID, ParentTraceID: a.parentTraceID, LinkedTraceIDs: a.linkedTraceIDs, Attempt: a.attempt, InputCount: len(a.messages), Totals: a.progress.Totals}
	c.mu.Lock()
	cancelled := a.ctx.Err() != nil
	if len(a.messages) == 0 && err == nil {
		event.Status = "skipped"
		event.Reason = "source_unavailable"
		event.NextStep = "stop"
	} else if completed && err == nil && !cancelled {
		c.counts.Completed += len(a.messages)
		event.Status = "completed"
		event.Reason = "batch_processed"
		if a.progress.Totals.CandidateCount == 0 {
			event.Reason = "no_learning_candidates"
		}
		event.NextStep = "next_learning_batch"
	} else if err != nil {
		modelError, isModel := err.(*modelservice.ModelError)
		if !cancelled && !a.progress.Applying && isModel && modelError.Retryable && !modelservice.ProviderAttemptsHandled(err) && a.attempt < c.settings.RetryAttempts {
			delay := int64(math.Min(30000, float64(c.settings.RetryBaseMS)*math.Pow(2, float64(a.attempt-1))))
			c.retries[scene] = &retryBatch{a.messages, a.attempt + 1, c.clock.Now() + delay, a.batchID, a.parentTraceID, append([]string{}, a.linkedTraceIDs...)}
			c.counts.Retried++
			event.Status = "failed"
			event.Reason = modelReason(err)
			event.NextStep = "retry_scheduled"
			event.RetryDelayMS = int(delay)
		} else {
			if !cancelled {
				c.counts.Failed += len(a.messages)
				id := c.newID()
				f := &Failure{ID: id, SceneID: scene, Attempt: a.attempt, MessageCount: len(a.messages), Partial: a.progress.Applying, CanRetry: !a.progress.Applying, CreatedAt: time.UnixMilli(c.clock.Now()).UTC().Format("2006-01-02T15:04:05.000Z")}
				if !a.progress.Applying {
					f.messages = a.messages
				}
				c.failures[id] = f
				c.failureOrder = append(c.failureOrder, id)
				for int64(len(c.failureOrder)) > c.settings.QueueMaxTotalMessages {
					c.removeFailureLocked(c.failureOrder[0])
				}
			}
			partial := a.progress.Totals.SavedCount > 0 || a.progress.WritePending || (!cancelled && a.progress.Applying)
			terminalEvent(&event, partial, cancelled, err)
		}
	} else {
		terminalEvent(&event, a.progress.Totals.SavedCount > 0 || a.progress.WritePending, true, nil)
	}
	if context.Cause(a.ctx) == errRecalled && !a.progress.Applying && !c.disposed && enabled && eligibilityErr == nil {
		survivors := intersect(c.survivors[scene], eligible)
		for _, m := range survivors {
			_, _ = c.admission.Admit(scene, str(m, "id"), "realtime")
		}
		if len(survivors) > 0 {
			c.queued[scene] = append(survivors, c.queued[scene]...)
		}
	}
	if a.ticket != nil {
		_, _ = c.admission.Finish(scene, a.ticket.Token, nil)
	}
	delete(c.active, scene)
	delete(c.survivors, scene)
	if c.overdueScene == scene {
		c.overdueScene = ""
	}
	if a.progress.WritePending {
		event.UnconfirmedCandidateCount = 1
	}
	c.mu.Unlock()
	c.emit(event)
	c.schedule(scene, true)
}
func terminalEvent(e *Event, partial, cancelled bool, err error) {
	if partial {
		e.Status = "partial"
		e.Reason = "partial_commit_no_replay"
		e.NextStep = "inspect_partial_learning"
	} else if cancelled {
		e.Status = "cancelled"
		e.Reason = "request_cancelled"
		e.NextStep = "stop"
	} else {
		e.Status = "failed"
		e.Reason = modelReason(err)
		e.NextStep = "manual_retry"
	}
}

// Dispose is nonblocking. Active payloads retain resource occupancy until actual
// work returns; CloseAndWait joins those physical callbacks before owners close.
func (c *Coordinator) Dispose() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.disposed {
		return
	}
	c.disposed = true
	c.admission.SetEnabled(false)
	c.queued = map[string][]*botruntime.Object{}
	c.retries = map[string]*retryBatch{}
	c.survivors = map[string][]*botruntime.Object{}
	c.traces = map[sourceKey]string{}
	c.failures = map[string]*Failure{}
	c.failureOrder = nil
	for _, t := range c.timers {
		t.cancel()
	}
	c.timers = map[string]timerEntry{}
	for _, a := range c.active {
		a.cancel(errDisposed)
	}
	c.work.Seal()
}
func (c *Coordinator) CloseAndWait(ctx context.Context) error { c.Dispose(); return c.work.Wait(ctx) }
