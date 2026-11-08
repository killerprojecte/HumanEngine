package semantic

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/learningadmission"
	"github.com/killerprojecte/HumanEngine/backend/internal/modelread"
	"github.com/killerprojecte/HumanEngine/backend/internal/modelservice"
	"github.com/killerprojecte/HumanEngine/backend/internal/providertransport"
	"github.com/killerprojecte/HumanEngine/backend/internal/recovery"
	"github.com/killerprojecte/HumanEngine/backend/internal/topiccollation"
	"sync"
	"sync/atomic"
	"time"
)

// Owner binds resolution/attempt receipts/commit to the real domain owner. Commit
// MUST execute guard inside the existing assertion/checkpoint writer transaction.
// No constructor supplies a permissive authority or fallback commit implementation.
type Owner interface {
	Enabled(string) bool
	PolicyRevision() uint64
	ForegroundBusy() bool
	Timezone() string
	ResolveDirty(context.Context, []Dirty, *modelread.Ticket) (*ResolvedInput, error)
	Valid(context.Context, *Input) bool
	ValidInSession(context.Context, *Input, *recovery.WriteSession) bool
	BeginAttempt(context.Context, *Input) (bool, error)
	FinishAttempt(context.Context, *Input, string) error
	Commit(context.Context, *Input, []*jsjson.Object, func(*recovery.WriteSession) error) (CommitResult, error)
	Event(Event)
}
type CommitResult struct{ Created, Rejected int }
type Event struct{ SceneID, Status, Reason string }
type Clock interface {
	Now() time.Time
	AfterFunc(time.Duration, func()) func()
}
type realClock struct{}

func (realClock) Now() time.Time { return time.Now() }
func (realClock) AfterFunc(d time.Duration, f func()) func() {
	t := time.AfterFunc(d, f)
	return func() { t.Stop() }
}

type CoordinatorOptions struct {
	Admission learningadmission.Options
	Poll      time.Duration
	Clock     Clock
	Collator  *topiccollation.Collator
}

func DefaultCoordinatorOptions() CoordinatorOptions {
	return CoordinatorOptions{Admission: learningadmission.SemanticDefaults(), Poll: time.Second, Clock: realClock{}}
}

type dirtyTicket struct {
	dirty    Dirty
	ticket   *modelread.Ticket
	revision uint64
}
type activeWork struct {
	scene, partition string
	cancel           context.CancelFunc
	ctx              context.Context
	entries          []dirtyTicket
	snapshot         *Input
}
type CoordinatorStats struct{ Accepted, Attempts, Finished, Abandoned, Skipped, Pending, Running int }

// Coordinator is notification-driven and ephemeral. Idle owners have no timer,
// startup scan, persisted queue, retry loop, or automatic reenrollment.
type Coordinator struct {
	mu                  sync.Mutex
	callbacks           sync.WaitGroup
	scheduledStop       func()
	owner               Owner
	read                *modelread.Policy
	router              CompletionPort
	admission           *learningadmission.Admission
	clock               Clock
	poll                time.Duration
	dirty               map[string]map[string]dirtyTicket
	active              *activeWork
	timer               func()
	timerGeneration     uint64
	disposed, scheduled bool
	counts              CoordinatorStats
	idle                chan struct{}
}

func NewCoordinator(owner Owner, read *modelread.Policy, models *providertransport.NativeModels, o CoordinatorOptions) (*Coordinator, error) {
	if owner == nil || read == nil || models == nil {
		return nil, errors.New("semantic_runtime_composition_required")
	}
	return newCoordinator(owner, read, models, o)
}
func newCoordinator(owner Owner, read *modelread.Policy, router CompletionPort, o CoordinatorOptions) (*Coordinator, error) {
	if o.Clock == nil {
		o.Clock = realClock{}
	}
	if o.Poll < 10*time.Millisecond || o.Poll%time.Millisecond != 0 {
		return nil, errors.New("semantic_poll_invalid")
	}
	a, e := learningadmission.New(o.Admission, func() int64 { return o.Clock.Now().UnixMilli() }, o.Collator)
	if e != nil {
		return nil, e
	}
	idle := make(chan struct{})
	close(idle)
	return &Coordinator{owner: owner, read: read, router: router, admission: a, clock: o.Clock, poll: o.Poll, dirty: map[string]map[string]dirtyTicket{}, idle: idle}, nil
}
func cloneDirty(d Dirty) Dirty {
	if d.PersonID != nil {
		v := *d.PersonID
		d.PersonID = &v
	}
	if d.EpisodeAnchor != nil {
		d.EpisodeAnchor = d.EpisodeAnchor.Clone()
	}
	if d.Trigger != nil {
		d.Trigger = d.Trigger.Clone()
	}
	return d
}
func (c *Coordinator) Notify(ctx context.Context, d Dirty) (string, error) {
	c.mu.Lock()
	disposed := c.disposed
	c.mu.Unlock()
	if disposed || !c.owner.Enabled(d.SceneID) {
		return "disabled", nil
	}
	ticket, e := c.read.Require(ctx, d.SceneID)
	if e != nil {
		return "disabled", nil
	}
	subject := []any{"person", nullableString(d.PersonID)}
	if d.EpisodeAnchor != nil {
		subject = []any{"episode", d.EpisodeAnchor}
	}
	partition, e := jsjson.Stringify([]any{d.SceneID, subject})
	if e != nil {
		return "", e
	}
	key, e := privacyHash([]any{nullableObject(d.Trigger), d.Reason})
	if e != nil {
		return "", e
	}
	revision := c.owner.PolicyRevision()
	c.mu.Lock()
	if c.disposed {
		c.mu.Unlock()
		return "disabled", nil
	}
	accepted, e := c.admission.Admit(partition, key, "background")
	if e != nil || accepted != "accepted" {
		c.mu.Unlock()
		return accepted, e
	}
	if len(c.dirty) == 0 && c.active == nil {
		c.idle = make(chan struct{})
	}
	pending := c.dirty[partition]
	if pending == nil {
		pending = map[string]dirtyTicket{}
		c.dirty[partition] = pending
	}
	pending[key] = dirtyTicket{cloneDirty(d), ticket, revision}
	c.counts.Accepted++
	c.mu.Unlock()
	c.emit(Event{d.SceneID, "queued", ""})
	c.Resume()
	return accepted, nil
}
func (c *Coordinator) ticketValid(ctx context.Context, e dirtyTicket) bool {
	return c.owner.Enabled(e.dirty.SceneID) && c.owner.PolicyRevision() == e.revision && c.read.Valid(ctx, e.ticket)
}
func (c *Coordinator) Configure(ctx context.Context, o *learningadmission.Options) error {
	if o != nil {
		if e := c.admission.Configure(*o); e != nil {
			return e
		}
	}
	c.mu.Lock()
	entries := []dirtyTicket{}
	for _, p := range c.dirty {
		for _, e := range p {
			entries = append(entries, e)
		}
	}
	active := c.active
	var snapshot *Input
	if active != nil && active.snapshot != nil {
		snapshot = active.snapshot.Clone()
	}
	c.mu.Unlock()
	for _, e := range entries {
		if !c.ticketValid(ctx, e) {
			c.CancelScene(e.dirty.SceneID)
		}
	}
	if active != nil {
		invalid := !c.owner.Enabled(active.scene)
		for _, e := range active.entries {
			invalid = invalid || !c.ticketValid(ctx, e)
		}
		if snapshot != nil {
			invalid = invalid || !c.owner.Valid(ctx, snapshot)
		}
		if invalid {
			active.cancel()
		}
	}
	c.Resume()
	return nil
}
func (c *Coordinator) CancelScene(scene string) {
	c.mu.Lock()
	for partition, p := range c.dirty {
		for _, entry := range p {
			if entry.dirty.SceneID == scene {
				delete(c.dirty, partition)
				c.admission.CancelScene(partition)
			}
			break
		}
	}
	if c.active != nil && c.active.scene == scene {
		c.admission.CancelScene(c.active.partition)
		c.active.cancel()
	}
	c.settleIdleLocked()
	c.mu.Unlock()
}
func (c *Coordinator) Dispose() {
	c.mu.Lock()
	c.disposed = true
	c.stopTimerLocked()
	if c.scheduledStop != nil {
		c.scheduledStop()
		c.scheduledStop = nil
		c.scheduled = false
	}
	c.admission.SetEnabled(false)
	c.dirty = map[string]map[string]dirtyTicket{}
	if c.active != nil {
		c.active.cancel()
	}
	c.settleIdleLocked()
	c.mu.Unlock()
}

// CloseAndWait does not equate cancellation with completion. It joins the model,
// final writer callback and receipt cleanup, even if a provider delays cancellation.
func (c *Coordinator) CloseAndWait(ctx context.Context) error {
	c.Dispose()
	if e := c.WhenIdle(ctx); e != nil {
		return e
	}
	done := make(chan struct{})
	go func() { c.callbacks.Wait(); close(done) }()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
func (c *Coordinator) WhenIdle(ctx context.Context) error {
	c.mu.Lock()
	idle := c.idle
	c.mu.Unlock()
	select {
	case <-idle:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
func (c *Coordinator) Stats() CoordinatorStats {
	c.mu.Lock()
	defer c.mu.Unlock()
	s := c.counts
	for _, p := range c.dirty {
		s.Pending += len(p)
	}
	if c.active != nil {
		s.Running = 1
	}
	return s
}
func (c *Coordinator) stopTimerLocked() {
	c.timerGeneration++
	if c.timer != nil {
		c.timer()
		c.timer = nil
	}
}
func (c *Coordinator) Resume() {
	c.mu.Lock()
	if c.disposed || c.active != nil || c.scheduled {
		c.mu.Unlock()
		return
	}
	c.stopTimerLocked()
	c.scheduled = true
	c.scheduledStop = c.callbackLocked(0, func() { c.mu.Lock(); c.scheduled = false; c.scheduledStop = nil; c.mu.Unlock(); c.pump() })
	c.mu.Unlock()
}
func (c *Coordinator) scheduleLocked() {
	if c.timer != nil || c.disposed || len(c.dirty) == 0 {
		return
	}
	c.timerGeneration++
	generation := c.timerGeneration
	c.timer = c.callbackLocked(c.poll, func() {
		c.mu.Lock()
		if generation != c.timerGeneration {
			c.mu.Unlock()
			return
		}
		c.timer = nil
		c.mu.Unlock()
		c.Resume()
	})
}
func (c *Coordinator) pump() {
	c.mu.Lock()
	if c.disposed || c.active != nil || len(c.dirty) == 0 {
		c.settleIdleLocked()
		c.mu.Unlock()
		return
	}
	c.mu.Unlock()
	busy := c.owner.ForegroundBusy()
	c.mu.Lock()
	if c.disposed || c.active != nil || len(c.dirty) == 0 {
		c.settleIdleLocked()
		c.mu.Unlock()
		return
	}
	if busy {
		c.scheduleLocked()
		c.mu.Unlock()
		return
	}
	ready, readyErr := c.admission.ReadyScenes(learningadmission.Capacity{})
	if readyErr != nil {
		c.mu.Unlock()
		c.Dispose()
		return
	}
	for _, partition := range ready {
		batch, _ := c.admission.Take(partition, 128, learningadmission.Capacity{})
		if batch == nil {
			continue
		}
		pending := c.dirty[partition]
		entries := []dirtyTicket{}
		for _, key := range batch.SourceKeys {
			if entry, ok := pending[key]; ok {
				entries = append(entries, entry)
			}
			delete(pending, key)
		}
		if len(pending) == 0 {
			delete(c.dirty, partition)
		}
		if len(entries) == 0 {
			c.admission.Finish(partition, batch.Token, nil)
			continue
		}
		ctx, cancel := context.WithCancel(context.Background())
		active := &activeWork{scene: entries[0].dirty.SceneID, partition: partition, cancel: cancel, ctx: ctx, entries: entries}
		c.active = active
		c.mu.Unlock()
		go func() {
			defer func() {
				cancel()
				c.mu.Lock()
				c.admission.Finish(partition, batch.Token, nil)
				c.active = nil
				c.settleIdleLocked()
				c.mu.Unlock()
				c.Resume()
			}()
			c.run(active)
		}()
		return
	}
	c.scheduleLocked()
	c.mu.Unlock()
}
func (c *Coordinator) guard(a *activeWork) error {
	c.mu.Lock()
	invalid := c.disposed || a.ctx.Err() != nil || len(a.entries) == 0
	var snapshot *Input
	if a.snapshot != nil {
		snapshot = a.snapshot.Clone()
	}
	c.mu.Unlock()
	if invalid {
		return errors.New("semantic_authority_changed")
	}
	for _, e := range a.entries {
		if !c.ticketValid(a.ctx, e) {
			return errors.New("semantic_authority_changed")
		}
	}
	if snapshot != nil && !c.owner.Valid(a.ctx, snapshot) {
		return errors.New("semantic_authority_changed")
	}
	return nil
}
func (c *Coordinator) run(a *activeWork) {
	started := false
	var snapshot *Input
	var runErr error
	defer func() {
		if recover() != nil {
			runErr = errors.New("semantic_owner_panicked")
		}
		if runErr == nil {
			return
		}
		if started && snapshot != nil {
			func() {
				defer func() { _ = recover() }()
				_ = c.owner.FinishAttempt(context.WithoutCancel(a.ctx), snapshot.Clone(), "abandoned")
			}()
		}
		c.mu.Lock()
		c.counts.Abandoned++
		c.mu.Unlock()
		c.emit(Event{a.scene, "abandoned", "authority_or_request_failed"})
	}()
	guard := func() error { return c.guard(a) }
	if runErr = guard(); runErr != nil {
		return
	}
	dirty := []Dirty{}
	for _, e := range a.entries {
		dirty = append(dirty, cloneDirty(e.dirty))
	}
	var resolved *ResolvedInput
	resolved, runErr = c.owner.ResolveDirty(a.ctx, dirty, a.entries[0].ticket)
	if runErr != nil {
		return
	}
	if runErr = guard(); runErr != nil {
		return
	}
	if resolved == nil {
		c.skipped(a.scene, "no_complete_input")
		return
	}
	snapshot = CaptureInput(a.scene, *resolved)
	if snapshot == nil {
		c.skipped(a.scene, "no_complete_input")
		return
	}
	if !a.entries[0].ticket.PersonalizationEnabled() {
		for _, a := range snapshot.Assertions() {
			scope := scObject(scGet(scGet(a, "record"), "scope"))
			if _, has := scope.Get("personId"); has {
				c.skipped(snapshot.SceneID(), "no_complete_input")
				return
			}
		}
	}
	trigger := a.entries[0].dirty
	anchor := scObject(scGet(snapshot.Value, "episodeAnchor"))
	mismatch := false
	if trigger.EpisodeAnchor != nil {
		mismatch = anchor == nil || scGet(anchor, "id") != scGet(trigger.EpisodeAnchor, "id") || scGet(anchor, "version") != scGet(trigger.EpisodeAnchor, "version")
	} else {
		mismatch = scGet(snapshot.Value, "personId") != nullableString(trigger.PersonID) || scGet(snapshot.Value, "episodeOnly") == true
	}
	if mismatch {
		c.skipped(a.scene, "partition_mismatch")
		return
	}
	c.mu.Lock()
	a.snapshot = snapshot.Clone()
	c.mu.Unlock()
	if runErr = guard(); runErr != nil {
		return
	}
	var begin bool
	begin, runErr = c.owner.BeginAttempt(a.ctx, snapshot.Clone())
	if runErr != nil {
		return
	}
	if !begin {
		c.skipped(a.scene, "unchanged_or_capacity")
		return
	}
	started = true
	c.mu.Lock()
	c.counts.Attempts++
	c.mu.Unlock()
	c.emit(Event{a.scene, "started", ""})
	id := [16]byte{}
	if _, runErr = rand.Read(id[:]); runErr != nil {
		return
	}
	ctx := modelservice.WithProviderOperationPriority(a.ctx, 2)
	id[6] = (id[6] & 0x0f) | 0x40
	id[8] = (id[8] & 0x3f) | 0x80
	encoded := hex.EncodeToString(id[:])
	turnID := encoded[:8] + "-" + encoded[8:12] + "-" + encoded[12:16] + "-" + encoded[16:20] + "-" + encoded[20:]
	ctx = providertransport.WithBudgetContext(ctx, turnID, "maintenance")
	var proposals []*jsjson.Object
	proposals, runErr = RequestProposals(ctx, snapshot, c.router, guard, c.owner.Timezone(), c.clock.Now())
	if runErr != nil {
		return
	}
	if runErr = guard(); runErr != nil {
		return
	}
	if len(proposals) > 0 {
		_, runErr = c.owner.Commit(a.ctx, snapshot.Clone(), proposals, func(session *recovery.WriteSession) error { return c.commitGuard(a, session) })
		if runErr != nil {
			return
		}
	}
	if runErr = guard(); runErr != nil {
		return
	}
	runErr = c.owner.FinishAttempt(a.ctx, snapshot.Clone(), "finished")
	if runErr != nil {
		return
	}
	c.mu.Lock()
	c.counts.Finished++
	c.mu.Unlock()
	c.emit(Event{a.scene, "finished", ""})
}
func (c *Coordinator) skipped(scene, reason string) {
	c.mu.Lock()
	c.counts.Skipped++
	c.mu.Unlock()
	c.emit(Event{scene, "skipped", reason})
}
func (c *Coordinator) emit(e Event) { defer func() { _ = recover() }(); c.owner.Event(e) }
func (c *Coordinator) settleIdleLocked() {
	if c.active != nil || len(c.dirty) != 0 {
		return
	}
	c.stopTimerLocked()
	select {
	case <-c.idle:
	default:
		close(c.idle)
	}
}

func (c *Coordinator) commitGuard(a *activeWork, session *recovery.WriteSession) error {
	c.mu.Lock()
	invalid := c.disposed || a.ctx.Err() != nil || len(a.entries) == 0 || session == nil
	var snapshot *Input
	if a.snapshot != nil {
		snapshot = a.snapshot.Clone()
	}
	c.mu.Unlock()
	if invalid {
		return errors.New("semantic_authority_changed")
	}
	for _, e := range a.entries {
		if !c.owner.Enabled(e.dirty.SceneID) || c.owner.PolicyRevision() != e.revision || !c.read.ValidInSession(a.ctx, e.ticket, session) {
			return errors.New("semantic_authority_changed")
		}
	}
	if snapshot == nil || !c.owner.ValidInSession(a.ctx, snapshot, session) {
		return errors.New("semantic_authority_changed")
	}
	return nil
}

// Called under mu; Clock callbacks must never execute inline. Cancellation joins
// scheduled callbacks as well as active model work during CloseAndWait.
func (c *Coordinator) callbackLocked(delay time.Duration, fn func()) func() {
	c.callbacks.Add(1)
	var state atomic.Int32
	stop := c.clock.AfterFunc(delay, func() {
		if !state.CompareAndSwap(0, 1) {
			return
		}
		defer c.callbacks.Done()
		fn()
	})
	return func() {
		stop()
		if state.CompareAndSwap(0, 2) {
			c.callbacks.Done()
		}
	}
}

// ConfigurePatch preserves omitted admission options exactly as the source API.
func (c *Coordinator) ConfigurePatch(ctx context.Context, p *learningadmission.Patch) error {
	if p != nil {
		if e := c.admission.ConfigurePatch(*p); e != nil {
			return e
		}
	}
	return c.Configure(ctx, nil)
}
