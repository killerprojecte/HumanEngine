package semantic

import (
	"context"
	"errors"
	"github.com/killerprojecte/HumanEngine/backend/internal/assertions"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/modelread"
	"github.com/killerprojecte/HumanEngine/backend/internal/providertransport"
	"github.com/killerprojecte/HumanEngine/backend/internal/recovery"
	"github.com/killerprojecte/HumanEngine/backend/internal/topiccollation"
	"os"
	"sync"
	"testing"
	"time"
)

type clockTask struct {
	at     time.Time
	fn     func()
	cancel bool
}
type testClock struct {
	mu    sync.Mutex
	now   time.Time
	tasks []*clockTask
}

func (c *testClock) Now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.now }
func (c *testClock) AfterFunc(d time.Duration, fn func()) func() {
	c.mu.Lock()
	task := &clockTask{at: c.now.Add(d), fn: fn}
	c.tasks = append(c.tasks, task)
	c.mu.Unlock()
	return func() { c.mu.Lock(); task.cancel = true; c.mu.Unlock() }
}
func (c *testClock) advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	c.mu.Unlock()
	for {
		c.mu.Lock()
		var next *clockTask
		for i, t := range c.tasks {
			if !t.cancel && !t.at.After(c.now) {
				next = t
				c.tasks = append(c.tasks[:i], c.tasks[i+1:]...)
				break
			}
		}
		c.mu.Unlock()
		if next == nil {
			return
		}
		next.fn()
	}
}

type testOwner struct {
	mu                          sync.Mutex
	fixture                     *semanticFixture
	resolved                    *ResolvedInput
	revision                    uint64
	enabled, busy, valid, begin bool
	events                      []Event
	receipts                    []string
	commitCount                 int
	failFinish                  bool
	beforeCommit                func()
	started                     chan struct{}
}

func (o *testOwner) Enabled(string) bool    { o.mu.Lock(); defer o.mu.Unlock(); return o.enabled }
func (o *testOwner) PolicyRevision() uint64 { o.mu.Lock(); defer o.mu.Unlock(); return o.revision }
func (o *testOwner) ForegroundBusy() bool   { o.mu.Lock(); defer o.mu.Unlock(); return o.busy }
func (o *testOwner) Timezone() string       { return "UTC" }
func (o *testOwner) ResolveDirty(context.Context, []Dirty, *modelread.Ticket) (*ResolvedInput, error) {
	return o.resolved, nil
}
func (o *testOwner) Valid(context.Context, *Input) bool {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.valid
}
func (o *testOwner) ValidInSession(ctx context.Context, s *Input, session *recovery.WriteSession) bool {
	return o.Valid(ctx, s)
}
func (o *testOwner) BeginAttempt(ctx context.Context, s *Input) (bool, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if !o.begin {
		return false, nil
	}
	o.receipts = append(o.receipts, "started")
	return true, nil
}
func (o *testOwner) FinishAttempt(ctx context.Context, s *Input, status string) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.receipts = append(o.receipts, status)
	if o.failFinish {
		return errors.New("receipt_failed")
	}
	return nil
}
func (o *testOwner) Commit(ctx context.Context, s *Input, p []*jsjson.Object, guard func(*recovery.WriteSession) error) (CommitResult, error) {
	if o.beforeCommit != nil {
		o.beforeCommit()
	}
	err := o.fixture.authority.WithWriteSession(ctx, o.fixture.main, func(session *recovery.WriteSession) error {
		if e := guard(session); e != nil {
			return e
		}
		o.mu.Lock()
		o.commitCount++
		o.mu.Unlock()
		return nil
	})
	return CommitResult{Created: len(p)}, err
}
func (o *testOwner) Event(e Event) { o.mu.Lock(); o.events = append(o.events, e); o.mu.Unlock() }

type blockingRouter struct {
	entered, release chan struct{}
	calls            []any
	err              error
}

func (r *blockingRouter) Complete(ctx context.Context, q providertransport.CompletionRequest) (*providertransport.Completion, error) {
	close(r.entered)
	<-r.release
	if r.err != nil {
		return nil, r.err
	}
	return &providertransport.Completion{ToolCalls: r.calls}, nil
}
func coordinatorSetup(t *testing.T) (*Coordinator, *testOwner, *testClock, *blockingRouter) {
	t.Helper()
	f := semSetup(t)
	_, e := f.main.Exec(`CREATE TABLE scenes(id TEXT PRIMARY KEY,value TEXT NOT NULL);INSERT INTO scenes VALUES('scene','{"id":"scene"}')`)
	semMust(t, e)
	store, e := assertions.New(f.main, f.authority, assertions.Options{})
	semMust(t, e)
	read, e := modelread.New(store)
	semMust(t, e)
	oracle := runtimeOracle(t)
	r := resolvedObject(scObject(scGet(scArr(scGet(oracle, "captures"))[0], "resolved")))
	owner := &testOwner{fixture: f, resolved: &r, enabled: true, valid: true, begin: true}
	clock := &testClock{now: time.Unix(0, 0)}
	router := &blockingRouter{entered: make(chan struct{}), release: make(chan struct{})}
	options := DefaultCoordinatorOptions()
	options.Admission.QuietMS = 0
	options.Admission.MaxWaitMS = 0
	options.Admission.MinIntervalMS = 0
	options.Clock = clock
	options.Collator, e = topiccollation.New("en-US")
	if errors.Is(e, topiccollation.ErrUnavailable) && os.Getenv("REQUIRE_ICU") == "" {
		t.Skip("native ICU profile required for semantic admission collation")
	}
	semMust(t, e)
	t.Cleanup(func() { options.Collator.Close() })
	c, e := newCoordinator(owner, read, router, options)
	semMust(t, e)
	t.Cleanup(c.Dispose)
	return c, owner, clock, router
}
func notifyFixture(t *testing.T, c *Coordinator, o *testOwner) {
	t.Helper()
	person := "person"
	s, e := c.Notify(context.Background(), Dirty{SceneID: "scene", PersonID: &person, Trigger: o.resolved.Entries[0].Selection, Reason: "assertion_committed"})
	semMust(t, e)
	if s != "accepted" {
		t.Fatal(s)
	}
}
func waitIdle(t *testing.T, c *Coordinator) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	semMust(t, c.WhenIdle(ctx))
}
func waitEntered(t *testing.T, r *blockingRouter) {
	t.Helper()
	select {
	case <-r.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("model did not start")
	}
}
func TestCoordinatorSilentSuccessAndNoRestart(t *testing.T) {
	c, o, clock, r := coordinatorSetup(t)
	notifyFixture(t, c, o)
	clock.advance(0)
	waitEntered(t, r)
	close(r.release)
	waitIdle(t, c)
	s := c.Stats()
	if s.Accepted != 1 || s.Attempts != 1 || s.Finished != 1 || s.Abandoned != 0 || s.Pending != 0 || s.Running != 0 {
		t.Fatal(s)
	}
	clock.advance(time.Hour)
	if c.Stats() != s {
		t.Fatal("clock restarted work")
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if len(o.events) != 3 || o.events[0].Status != "queued" || o.events[1].Status != "started" || o.events[2].Status != "finished" {
		t.Fatal(o.events)
	}
}
func TestCoordinatorDisposeWaitsForRealCompletion(t *testing.T) {
	c, o, clock, r := coordinatorSetup(t)
	notifyFixture(t, c, o)
	clock.advance(0)
	waitEntered(t, r)
	c.Dispose()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if c.WhenIdle(ctx) == nil {
		t.Fatal("dispose prematurely idle")
	}
	if c.Stats().Running != 1 {
		t.Fatal("lost active root")
	}
	close(r.release)
	waitIdle(t, c)
	s := c.Stats()
	if s.Abandoned != 1 || s.Finished != 0 {
		t.Fatal(s)
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if len(o.receipts) != 2 || o.receipts[1] != "abandoned" {
		t.Fatal(o.receipts)
	}
}
func TestCoordinatorRevisionRevokesPendingWithoutReplay(t *testing.T) {
	c, o, clock, r := coordinatorSetup(t)
	_ = r
	o.mu.Lock()
	o.busy = true
	o.mu.Unlock()
	notifyFixture(t, c, o)
	clock.advance(0)
	if c.Stats().Pending != 1 {
		t.Fatal("pending lost")
	}
	o.mu.Lock()
	o.revision++
	o.mu.Unlock()
	semMust(t, c.Configure(context.Background(), nil))
	waitIdle(t, c)
	clock.advance(time.Hour)
	s := c.Stats()
	if s.Attempts != 0 || s.Pending != 0 || s.Skipped != 0 || s.Abandoned != 0 {
		t.Fatal(s)
	}
}
func TestCoordinatorFinalWriterFenceAndReadTicket(t *testing.T) {
	for _, revoke := range []bool{false, true} {
		t.Run(map[bool]string{false: "write", true: "revoked"}[revoke], func(t *testing.T) {
			c, o, clock, r := coordinatorSetup(t)
			r.calls = []any{inputObject("name", "memory.semantic_propose", "arguments", inputObject("kind", "summary_candidate", "selectedParentIds", []any{"a", "b"}, "text", "synthetic", "hypothesis", false))}
			if revoke {
				o.beforeCommit = func() {
					_, e := o.fixture.main.Exec(`UPDATE scenes SET value='{"id":"scene","memoryReadRevision":1}'`)
					semMust(t, e)
				}
			}
			notifyFixture(t, c, o)
			clock.advance(0)
			waitEntered(t, r)
			close(r.release)
			waitIdle(t, c)
			s := c.Stats()
			if revoke {
				if s.Abandoned != 1 || o.commitCount != 0 {
					t.Fatal(s, o.commitCount)
				}
			} else if s.Finished != 1 || o.commitCount != 1 {
				t.Fatal(s, o.commitCount)
			}
		})
	}
}
func TestCoordinatorFinishFailureAbandonsAndDoesNotReplay(t *testing.T) {
	c, o, clock, r := coordinatorSetup(t)
	o.failFinish = true
	notifyFixture(t, c, o)
	clock.advance(0)
	waitEntered(t, r)
	close(r.release)
	waitIdle(t, c)
	if s := c.Stats(); s.Abandoned != 1 || s.Finished != 0 || s.Attempts != 1 {
		t.Fatal(s)
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if len(o.receipts) != 3 || o.receipts[2] != "abandoned" {
		t.Fatal(o.receipts)
	}
}

func TestActualSourceCoordinatorOutcomes(t *testing.T) {
	oracle := runtimeOracle(t)
	for _, raw := range scArr(scGet(oracle, "coordinators")) {
		scenario := semString(scObject(raw), "scenario")
		t.Run(scenario, func(t *testing.T) {
			c, o, clock, r := coordinatorSetup(t)
			switch scenario {
			case "request-failed":
				r.err = errors.New("synthetic failure")
			case "unchanged":
				o.begin = false
			case "no-input":
				o.resolved = nil
			case "receipt-failed":
				o.failFinish = true
			}
			resolved := resolvedObject(scObject(scGet(scArr(scGet(oracle, "captures"))[0], "resolved")))
			person := "person"
			accepted, e := c.Notify(context.Background(), Dirty{SceneID: "scene", PersonID: &person, Trigger: resolved.Entries[0].Selection, Reason: "assertion_committed"})
			semMust(t, e)
			if accepted != scGet(raw, "accepted") {
				t.Fatal(accepted)
			}
			clock.advance(0)
			if scenario != "unchanged" && scenario != "no-input" {
				waitEntered(t, r)
				if scenario == "revision-revoked" {
					o.mu.Lock()
					o.revision++
					o.mu.Unlock()
					semMust(t, c.Configure(context.Background(), nil))
				}
				close(r.release)
			}
			waitIdle(t, c)
			s := c.Stats()
			stats := inputObject("accepted", float64(s.Accepted), "attempts", float64(s.Attempts), "finished", float64(s.Finished), "abandoned", float64(s.Abandoned), "skipped", float64(s.Skipped), "pending", float64(s.Pending), "running", float64(s.Running))
			if !privacyEqual(stats, scGet(raw, "stats")) {
				t.Fatalf("stats %#v", s)
			}
			o.mu.Lock()
			defer o.mu.Unlock()
			events := []any{}
			for _, event := range o.events {
				v := inputObject("sceneId", event.SceneID, "status", event.Status)
				if event.Reason != "" {
					_ = v.Set("reason", event.Reason)
				}
				events = append(events, v)
			}
			if !privacyEqual(events, scGet(raw, "events")) {
				t.Fatalf("events %#v", o.events)
			}
			receipts := []any{}
			for _, r := range o.receipts {
				receipts = append(receipts, r)
			}
			if !privacyEqual(receipts, scGet(raw, "receipts")) {
				t.Fatalf("receipts %#v", o.receipts)
			}
		})
	}
}
func TestCloseJoinsAndCancelsScheduledCallback(t *testing.T) {
	c, o, clock, _ := coordinatorSetup(t)
	notifyFixture(t, c, o)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	semMust(t, c.CloseAndWait(ctx))
	clock.advance(time.Hour)
	if c.Stats().Attempts != 0 {
		t.Fatal("scheduled callback survived close")
	}
}

func TestConcurrentNotificationsAreDeduplicated(t *testing.T) {
	c, o, clock, r := coordinatorSetup(t)
	person := "person"
	var wg sync.WaitGroup
	var mu sync.Mutex
	counts := map[string]int{}
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			status, e := c.Notify(context.Background(), Dirty{SceneID: "scene", PersonID: &person, Trigger: o.resolved.Entries[0].Selection, Reason: "assertion_committed"})
			mu.Lock()
			defer mu.Unlock()
			if e != nil {
				counts["error"]++
			} else {
				counts[status]++
			}
		}()
	}
	wg.Wait()
	if counts["accepted"] != 1 || counts["duplicate"] != 31 {
		t.Fatal(counts)
	}
	clock.advance(0)
	waitEntered(t, r)
	close(r.release)
	waitIdle(t, c)
	if c.Stats().Attempts != 1 {
		t.Fatal(c.Stats())
	}
}
func TestFinalTicketRejectsForeignAndExpiredWriter(t *testing.T) {
	c, o, _, _ := coordinatorSetup(t)
	ctx := context.Background()
	ticket, e := c.read.Require(ctx, "scene")
	semMust(t, e)
	var expired *recovery.WriteSession
	semMust(t, o.fixture.authority.WithWriteSession(ctx, o.fixture.main, func(session *recovery.WriteSession) error {
		expired = session
		if !c.read.ValidInSession(ctx, ticket, session) {
			t.Fatal("valid writer rejected")
		}
		copy := *ticket
		if c.read.ValidInSession(ctx, &copy, session) {
			t.Fatal("forged ticket accepted")
		}
		return nil
	}))
	if c.read.ValidInSession(ctx, ticket, expired) {
		t.Fatal("expired writer accepted")
	}
	other := semSetup(t)
	semMust(t, other.authority.WithWriteSession(ctx, other.main, func(session *recovery.WriteSession) error {
		if c.read.ValidInSession(ctx, ticket, session) {
			t.Fatal("foreign writer accepted")
		}
		return nil
	}))
}
