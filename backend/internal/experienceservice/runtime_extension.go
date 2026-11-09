package experienceservice

import (
	"context"
	"errors"
	"fmt"
	"github.com/killerprojecte/HumanEngine/backend/internal/botruntime"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/recovery"
	"github.com/killerprojecte/HumanEngine/backend/internal/sourceevents"
	"sync"
)

// RuntimeGates are required Engine policy boundaries. Planner must implement
// personalization capture/assert/begin around run. Tool must apply model-tool
// admission. Proposal must preserve semantic, identity and knowledge guards.
// Passing nil is rejected rather than inventing a permissive native dispatcher.
type RuntimeGates struct {
	Planner  func(*botruntime.TurnContext, func() (any, error)) (any, error)
	Tool     func(string, *botruntime.TurnContext) error
	Proposal func(*botruntime.TurnContext) (string, error)
	Report   func(string, error)
	Tools    []*jsjson.Object
}

// NativeRuntimePorts resolves exact retained bodies with the SAME writer used
// for the Experience write. Neither constructor nor callback acquires a scene.
func NativeRuntimePorts(runtime *botruntime.Runtime, sources *sourceevents.Store, compare func(string, string) (int, error)) (Ports, error) {
	if runtime == nil || sources == nil {
		return Ports{}, errors.New("experience_runtime_dependencies_required")
	}
	return NativePorts(func(ws *recovery.WriteSession, scene, id string) (out *jsjson.Object, err error) {
		err = sources.WithSession(ws).WithRuntimeReads(context.Background(), func(reads botruntime.ReadAuthority) error {
			var e error
			out, e = runtime.PreviewMessage(scene, id, reads)
			return e
		})
		return
	}, compare)
}

// RuntimeBinding owns FIFO microtask-style sent association. The queue has no
// admission cap. Flush is for shutdown/tests, never for model/transport calls.
type sentAssociation struct {
	run  func()
	done chan struct{}
}
type RuntimeBinding struct {
	sequence *botruntime.SentSequencer
	ctx      context.Context
	cancel   context.CancelFunc
	report   func(string, error)
	tail     chan struct{}
	mu       sync.Mutex
	queue    []sentAssociation
	idle     chan struct{}
	running  bool
}

func (b *RuntimeBinding) enqueue(fn func()) {
	b.mu.Lock()
	if b.ctx.Err() != nil {
		b.mu.Unlock()
		return
	}
	job := sentAssociation{run: fn, done: make(chan struct{})}
	b.tail = job.done
	b.queue = append(b.queue, job)
	if b.running {
		b.mu.Unlock()
		return
	}
	b.running = true
	b.idle = make(chan struct{})
	b.mu.Unlock()
	go func() {
		for {
			b.mu.Lock()
			if len(b.queue) == 0 {
				b.running = false
				close(b.idle)
				b.mu.Unlock()
				return
			}
			fn := b.queue[0]
			b.queue[0] = sentAssociation{}
			b.queue = b.queue[1:]
			b.mu.Unlock()
			b.runAssociation(fn)
		}
	}()
}

// Barrier captures only work queued before this call. Later sends do not
// extend an ingress wait, and no Experience writer is held while waiting.
func (b *RuntimeBinding) Barrier() error {
	if b.sequence != nil {
		if e := b.sequence.Barrier(); e != nil {
			return e
		}
	}
	b.mu.Lock()
	tail := b.tail
	b.mu.Unlock()
	if tail == nil {
		return b.ctx.Err()
	}
	select {
	case <-b.ctx.Done():
		return b.ctx.Err()
	case <-tail:
		return b.ctx.Err()
	}
}
func (b *RuntimeBinding) Close() { b.cancel() }

// CloseAndWait joins physical sent callbacks even when Close has cancelled their
// owner context. Failed waits retain the service/database for an explicit retry.
func (b *RuntimeBinding) CloseAndWait(ctx context.Context) error {
	b.Close()
	if b.sequence != nil {
		if err := b.sequence.CloseAndWait(ctx); err != nil {
			return err
		}
	}
	b.mu.Lock()
	tail := b.tail
	b.mu.Unlock()
	if tail == nil {
		return nil
	}
	select {
	case <-tail:
		return nil
	default:
	}
	select {
	case <-tail:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (b *RuntimeBinding) Flush() {
	if b.sequence != nil {
		_ = b.sequence.Barrier()
	}
	b.mu.Lock()
	idle, running := b.idle, b.running
	b.mu.Unlock()
	if running {
		select {
		case <-idle:
		case <-b.ctx.Done():
		}
	}
}
func (b *RuntimeBinding) runAssociation(job sentAssociation) {
	defer close(job.done)
	b.runSafely(job.run)
}
func (b *RuntimeBinding) runSafely(run func()) {
	defer func() {
		if p := recover(); p != nil {
			func() {
				defer func() { _ = recover() }()
				b.report("experience_use_unavailable", fmt.Errorf("experience_sent_panic: %v", p))
			}()
		}
	}()
	if b.ctx.Err() == nil {
		run()
	}
}

// ComposeExtension preserves every unrelated callback on base and routes only
// the two fixed Experience tools. Install the returned extension in place of
// base (not additionally), so existing gates/exposure owners run exactly once.
func (r *RuntimeHints) ComposeExtension(base botruntime.Extension, g RuntimeGates) (*botruntime.Extension, *RuntimeBinding, error) {
	if g.Planner == nil || g.Tool == nil || g.Proposal == nil || g.Report == nil {
		return nil, nil, errors.New("experience_runtime_gates_required")
	}
	seen := map[string]bool{}
	for _, tool := range g.Tools {
		name := stringValue(prop(tool, "name"))
		if (name != "experience.outcomes" && name != "experience.propose") || seen[name] {
			return nil, nil, errors.New("experience_runtime_tools_invalid")
		}
		seen[name] = true
	}
	if len(seen) != 2 {
		return nil, nil, errors.New("experience_runtime_tools_required")
	}
	ext := base
	ext.Tools = []*jsjson.Object{}
	supplied := map[string]*jsjson.Object{}
	for _, tool := range g.Tools {
		supplied[stringValue(prop(tool, "name"))] = tool
	}
	for _, tool := range base.Tools {
		name := stringValue(prop(tool, "name"))
		if replacement, ok := supplied[name]; ok {
			ext.Tools = append(ext.Tools, replacement)
			delete(supplied, name)
		} else if name != "experience.outcomes" && name != "experience.propose" {
			ext.Tools = append(ext.Tools, tool)
		}
	}
	for _, tool := range g.Tools {
		if supplied[stringValue(prop(tool, "name"))] != nil {
			ext.Tools = append(ext.Tools, tool)
		}
	}
	bindingContext, cancel := context.WithCancel(context.Background())
	binding := &RuntimeBinding{ctx: bindingContext, cancel: cancel, report: g.Report}
	ext.PlannerContextData = func(c *botruntime.TurnContext, rows []*jsjson.Object) (any, error) {
		// Keep existing domains' data visible rather than overwriting it.
		var prior any
		var e error
		if base.PlannerContextData != nil {
			prior, e = base.PlannerContextData(c, rows)
			if e != nil {
				return nil, e
			}
		}
		value, e := g.Planner(c, func() (any, error) { return r.PlannerContext(c, rows) })
		if e != nil {
			return nil, e
		}
		if prior == nil {
			return value, nil
		}
		if value == nil {
			return prior, nil
		}
		return object("existing", prior, "experience", value), nil
	}
	ext.Execute = func(ctx context.Context, name string, args any, c *botruntime.TurnContext, done func(any, error)) {
		if name != "experience.outcomes" && name != "experience.propose" {
			if base.Execute == nil {
				done(nil, errors.New("runtime_unknown_tool"))
				return
			}
			base.Execute(ctx, name, args, c, done)
			return
		}
		if e := g.Tool(name, c); e != nil {
			done(nil, e)
			return
		}
		if name == "experience.propose" {
			reason, e := g.Proposal(c)
			if e != nil {
				done(nil, e)
				return
			}
			if reason != "" {
				done(object("status", "rejected", "reason", reason), nil)
				return
			}
			v, e := r.Propose(c, args)
			done(v, e)
			return
		}
		v, e := r.Outcomes(c)
		done(v, e)
	}
	ext.Delivered = func(name string, value any, c *botruntime.TurnContext, args any) (any, error) {
		if name == "experience.outcomes" || name == "experience.propose" {
			if e := g.Tool(name, c); e != nil {
				return nil, e
			}
			if name == "experience.outcomes" {
				if rows, ok := value.([]any); ok {
					return r.DeliverOutcomes(c, rows)
				}
			}
			return value, nil
		}
		delivered := value
		var e error
		if base.Delivered != nil {
			delivered, e = base.Delivered(name, value, c, args)
			if e != nil {
				return nil, e
			}
		}
		// Only source's post-exposure tool branches can establish observed use.
		if name == "memory.search" || name == "memory.person" || name == "memory.relations" {
			if e = r.DeliveredTool(c, name, args, delivered); e != nil {
				return nil, e
			}
		}
		return delivered, nil
	}
	return r.composeLifecycle(base, ext, binding), binding, nil
}

// ComposeLifecycle attaches real feedback and sent-association owners without
// granting model-tool authority. ComposeExtension uses this same implementation
// after installing its required policy-gated tools.
func (r *RuntimeHints) ComposeLifecycle(base botruntime.Extension, report func(string, error)) (*botruntime.Extension, *RuntimeBinding, error) {
	if report == nil {
		return nil, nil, errors.New("experience_runtime_report_required")
	}
	ctx, cancel := context.WithCancel(context.Background())
	binding := &RuntimeBinding{ctx: ctx, cancel: cancel, report: report}
	return r.composeLifecycle(base, base, binding), binding, nil
}

func (r *RuntimeHints) composeLifecycle(base, ext botruntime.Extension, binding *RuntimeBinding) *botruntime.Extension {
	sequence := botruntime.NewSentSequencer(binding.ctx, func(scene botruntime.Scene, turn, target string) {
		binding.runSafely(func() {
			if e := r.sentContext(binding.ctx, scene.ID, turn, target); e != nil {
				binding.report("experience_use_unavailable", e)
			}
		})
	})
	binding.sequence = sequence
	ext.SentSequencers = append(append([]*botruntime.SentSequencer(nil), base.SentSequencers...), sequence)
	ext.Received = func(m *jsjson.Object, scene botruntime.Scene) error {
		if e := binding.Barrier(); e != nil {
			return e
		}
		if prop(m, "sourceEventRegistrationLimited") != true {
			if e := r.Received(m); e != nil {
				binding.report("experience_feedback_unavailable", e)
			}
		}
		if base.Received != nil {
			return base.Received(m, scene)
		}
		return nil
	}
	ext.Sent = base.Sent
	ext.Closed = func() {
		binding.Close()
		if base.Closed != nil {
			base.Closed()
		}
	}
	ext.SceneChanged = func(scene botruntime.Scene) {
		if !scene.LearningEnabled {
			r.LearningPaused(scene.ID)
		}
		if base.SceneChanged != nil {
			base.SceneChanged(scene)
		}
	}
	return &ext
}
