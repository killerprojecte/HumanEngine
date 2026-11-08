package botruntime

import (
	"context"
	"errors"
	"github.com/killerprojecte/HumanEngine/backend/internal/dispatch"
	"github.com/killerprojecte/HumanEngine/backend/internal/replyplan"
	"sync/atomic"
	"testing"
)

func TestCloseAndWaitRetainsLateModelOwner(t *testing.T) {
	started := make(chan func(Completion, error), 1)
	r, _, _ := testRuntime(t, ModelFunc(func(_ context.Context, _ ModelRequest, done func(Completion, error)) { started <- done }))
	topic := registerFixture(t, r)
	turn, e := r.Run("scene", topic, false)
	if e != nil {
		t.Fatal(e)
	}
	settle := <-started
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if e = r.CloseAndWait(ctx); !errors.Is(e, context.Canceled) {
		t.Fatalf("premature drain: %v", e)
	}
	awaitTurn(t, turn) // Turn cleanup is not the late callback lifetime.
	if e = r.CloseAndWait(ctx); !errors.Is(e, context.Canceled) {
		t.Fatalf("turn done mistaken for physical done: %v", e)
	}
	settle(Completion{}, errors.New("synthetic late completion"))
	if e = r.CloseAndWait(context.Background()); e != nil {
		t.Fatal(e)
	}
	if _, e = r.Run("scene", topic, false); e == nil {
		t.Fatal("turn accepted after close")
	}
}
func TestTrackedStarterMustReturnAfterSynchronousSettlement(t *testing.T) {
	r := new(Runtime)
	parent, _ := r.work.Start()
	settled, release, returned := make(chan struct{}), make(chan struct{}), make(chan struct{})
	go func() {
		defer close(returned)
		trackedStart(r, func(done func(int, error)) { done(1, nil); close(settled); <-release }, func(int, error) {})
	}()
	<-settled
	parent()
	r.work.Seal()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if e := r.work.Wait(ctx); !errors.Is(e, context.Canceled) {
		t.Fatal("starter lost ownership")
	}
	close(release)
	<-returned
	if e := r.work.Wait(context.Background()); e != nil {
		t.Fatal(e)
	}
}

type heldReady struct {
	*testAuthority
	entered, release chan struct{}
}

func (a *heldReady) Ready() error { close(a.entered); <-a.release; return nil }
func TestCloseAndWaitRetainsTimerAuthorityCallback(t *testing.T) {
	r, a, _ := testRuntime(t, ModelFunc(func(context.Context, ModelRequest, func(Completion, error)) {}))
	registerFixture(t, r)
	cfg := r.config()
	cfg.TopicAdmissionEnabled = true
	if e := r.Configure(cfg); e != nil {
		t.Fatal(e)
	}
	entered, release, finished := make(chan struct{}), make(chan struct{}), make(chan struct{})
	r.services.Authority = &heldReady{testAuthority: a, entered: entered, release: release}
	state := r.state("scene")
	go func() { defer close(finished); r.wake(state, "missing", &timerTicket{}) }()
	<-entered
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if e := r.CloseAndWait(ctx); !errors.Is(e, context.Canceled) {
		t.Fatalf("timer authority owner lost: %v", e)
	}
	close(release)
	<-finished
	if e := r.CloseAndWait(context.Background()); e != nil {
		t.Fatal(e)
	}
}

func TestStarterPanicDoesNotReleaseRunningSettlement(t *testing.T) {
	r := new(Runtime)
	parent, _ := r.work.Start()
	entered, release, returned := make(chan struct{}), make(chan struct{}), make(chan struct{})
	func() {
		defer func() {
			if recover() == nil {
				t.Error("starter panic did not propagate")
			}
		}()
		trackedStart(r, func(done func(int, error)) {
			go func() { defer close(returned); done(1, nil) }()
			<-entered
			panic("synthetic starter failure after launching callback")
		}, func(int, error) { close(entered); <-release })
	}()
	parent()
	r.work.Seal()
	if e := r.work.Wait(context.Background()); e == nil || e.Error() != "runtime_callback_start_failed_pending_settlement" {
		t.Fatalf("starter panic released active settlement: %v", e)
	}
	close(release)
	<-returned
	if e := r.work.Wait(context.Background()); e != nil {
		t.Fatal(e)
	}
}
func TestStarterPanicWithoutSettlementCannotProveDrain(t *testing.T) {
	r := new(Runtime)
	parent, _ := r.work.Start()
	func() {
		defer func() { _ = recover() }()
		trackedStart(r, func(func(int, error)) { panic("unknown asynchronous outcome") }, func(int, error) {})
	}()
	parent()
	r.work.Seal()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if e := r.work.Wait(ctx); e == nil || e.Error() != "runtime_callback_start_failed_pending_settlement" {
		t.Fatal("panic was treated as physical callback completion")
	}
}
func TestCloseAndWaitRetainsRegisterAndPreviewAuthorityRoots(t *testing.T) {
	for _, name := range []string{"register", "preview"} {
		t.Run(name, func(t *testing.T) {
			r, a, _ := testRuntime(t, ModelFunc(func(context.Context, ModelRequest, func(Completion, error)) {}))
			entered, release, finished := make(chan struct{}), make(chan struct{}), make(chan struct{})
			authority := &heldReady{testAuthority: a, entered: entered, release: release}
			r.services.Authority = authority
			go func() {
				defer close(finished)
				if name == "register" {
					_ = r.RegisterScene(fixtureScene(false))
				} else {
					_, _ = r.PreviewMessage("missing", "message", authority)
				}
			}()
			<-entered
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			if e := r.CloseAndWait(ctx); !errors.Is(e, context.Canceled) {
				t.Fatalf("untracked %s owner: %v", name, e)
			}
			close(release)
			<-finished
			if e := r.CloseAndWait(context.Background()); e != nil {
				t.Fatal(e)
			}
		})
	}
}

type heldNextReady struct {
	*testAuthority
	hold             atomic.Bool
	entered, release chan struct{}
}

func (a *heldNextReady) Ready() error {
	if a.hold.CompareAndSwap(true, false) {
		close(a.entered)
		<-a.release
	}
	return a.testAuthority.Ready()
}
func TestCloseAndWaitRetainsActualBeforeTransmitAuthority(t *testing.T) {
	r, a, _ := testRuntime(t, ModelFunc(func(_ context.Context, req ModelRequest, done func(Completion, error)) {
		switch req.Stage {
		case "decision":
			done(fixtureDecision("reply"), nil)
		case "plan":
			done(fixturePlan(req, "reply", nil), nil)
		case "replyer":
			done(completion([]replyplan.ToolCall{fixtureSend("Synthetic response.", false)}, ""), nil)
		default:
			done(Completion{}, code("unexpected_stage"))
		}
	}))
	topic := registerFixture(t, r)
	gate := &heldNextReady{testAuthority: a, entered: make(chan struct{}), release: make(chan struct{})}
	r.services.Authority = gate
	beforeReady := make(chan func() error, 1)
	pending := dispatch.NewFuture[dispatch.Receipt]()
	r.AddAdapter("fixture", &Adapter{Transport: &dispatch.Adapter{Send: func(_ dispatch.SceneRef, _ dispatch.Segment, _ *dispatch.Signal, before func() error) *dispatch.Future[dispatch.Receipt] {
		beforeReady <- before
		return pending
	}}})
	turn, e := r.Run("scene", topic, false)
	if e != nil {
		t.Fatal(e)
	}
	before := <-beforeReady
	gate.hold.Store(true)
	callbackDone := make(chan error, 1)
	go func() { callbackDone <- before() }()
	<-gate.entered
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if e = r.CloseAndWait(ctx); !errors.Is(e, context.Canceled) {
		t.Fatalf("beforeTransmit lost authority ownership: %v", e)
	}
	awaitTurn(t, turn)
	if e = r.CloseAndWait(ctx); !errors.Is(e, context.Canceled) {
		t.Fatal("logical send cancellation released active callback")
	}
	close(gate.release)
	if e = <-callbackDone; e == nil {
		t.Fatal("closed runtime permitted transmission")
	}
	pending.Resolve(dispatch.Receipt{Status: dispatch.Cancelled})
	if e = r.CloseAndWait(context.Background()); e != nil {
		t.Fatal(e)
	}
	if e = before(); e == nil {
		t.Fatal("late callback accepted after drain")
	}
}
