package modelservice

import (
	"context"
	"errors"
	"github.com/killerprojecte/HumanEngine/backend/internal/quiescence"
	"math"
	"strings"
	"sync"

	"github.com/killerprojecte/HumanEngine/backend/internal/scheduler"
)

// Invocation must start promptly and settle only when its native operation
// really ends. For custom calls this engine races cancellation as invokeProtocol
// does. Invocation owns request serialization, credentials, budget admission,
// accounting, result validation, and any opaque assistant continuation. There is
// deliberately no default invocation and no HTTP/V1 production adapter here.
// Only core-created *ModelError failures carry retry/fallback classifications.
type Invocation func(context.Context, Attempt, func(any, error))
type Attempt struct {
	Candidate   Candidate
	Number      int
	MaxAttempts int
	TimeoutMS   float64
	// FirstContent is only for meaningful native streamed text/reasoning/tools.
	// It is ignored for custom/non-streaming calls and after cancellation.
	FirstContent func()
}
type Event struct {
	Kind        string  `json:"kind"`
	ModelID     string  `json:"modelId,omitempty"`
	Index       int     `json:"index,omitempty"`
	Attempt     int     `json:"attempt,omitempty"`
	MaxAttempts int     `json:"maxAttempts,omitempty"`
	TimeoutMS   float64 `json:"timeoutMs,omitempty"`
	DelayMS     float64 `json:"delayMs,omitempty"`
	Code        string  `json:"code,omitempty"`
}
type Options struct {
	Clock     Clock
	Scheduler *scheduler.Scheduler
	Observe   func(Event)
	// Sleep is injectable to drive source-pinned retry traces. Nil uses Clock.
	Sleep func(context.Context, float64) error
}
type Policy struct {
	work      quiescence.Group
	config    Config
	clock     Clock
	scheduler *scheduler.Scheduler
	observe   func(Event)
	sleep     func(context.Context, float64) error
}

func New(config Config, options Options) (*Policy, error) {
	if n := config.NativeMaxOutputTokens; n != nil && (math.IsNaN(*n) || math.IsInf(*n, 0) || math.Trunc(*n) != *n || *n < 1 || *n > 1000000) {
		return nil, NewError("invalid_output_cap", nil, false)
	}
	copyConfig := Config{NativeMaxOutputTokens: copyNumber(config.NativeMaxOutputTokens)}
	for _, p := range config.Providers {
		copyConfig.Providers = append(copyConfig.Providers, cloneProvider(p))
	}
	for _, m := range config.Models {
		copyConfig.Models = append(copyConfig.Models, cloneModel(m))
	}
	roles := map[string]bool{}
	for _, r := range config.Routes {
		r.Role = CanonicalRole(r.Role)
		if roles[r.Role] {
			return nil, errors.New("duplicate_role_routes")
		}
		roles[r.Role] = true
		r.OrderedModelIDs = append([]string(nil), r.OrderedModelIDs...)
		r.TimeoutMS = copyNumber(r.TimeoutMS)
		copyConfig.Routes = append(copyConfig.Routes, r)
	}
	clock := options.Clock
	if clock == nil {
		clock = WallClock{}
	}
	sched := options.Scheduler
	if sched == nil {
		sched = scheduler.ModelScheduler
	}
	p := &Policy{config: copyConfig, clock: clock, scheduler: sched, observe: options.Observe, sleep: options.Sleep}
	if p.sleep == nil {
		p.sleep = func(ctx context.Context, ms float64) error { return waitRetry(ctx, ms, clock) }
	}
	return p, nil
}
func (p *Policy) route(role string) *Route {
	role = CanonicalRole(role)
	for i := range p.config.Routes {
		if p.config.Routes[i].Role == role {
			return &p.config.Routes[i]
		}
	}
	return nil
}

// Select preserves first-match model/provider lookup and original route order.
// It never reorders candidates by capability, cost, health, or previous failure.
func (p *Policy) Select(request Request) ([]Selection, error) {
	route := p.route(request.Role)
	if route == nil {
		return nil, NewError("route_not_configured", nil, false)
	}
	if err := ValidateToolNames(request.ToolNames); err != nil {
		return nil, err
	}
	result := make([]Selection, 0, len(route.OrderedModelIDs))
	for i, id := range route.OrderedModelIDs {
		row := Selection{ModelID: id, Index: i + 1}
		var model *Model
		for j := range p.config.Models {
			if p.config.Models[j].ID == id {
				model = &p.config.Models[j]
				break
			}
		}
		if model == nil {
			row.SkipReason = "model_not_configured"
		} else if !contains(model.Capabilities, "text") || (len(request.ToolNames) > 0 && !contains(model.Capabilities, "tools")) || (request.HasImages && !contains(model.Capabilities, "vision")) {
			row.SkipReason = "model_capability_mismatch"
		} else {
			var provider *Provider
			for j := range p.config.Providers {
				if p.config.Providers[j].ID == model.ProviderID {
					provider = &p.config.Providers[j]
					break
				}
			}
			if provider == nil {
				row.SkipReason = "provider_not_configured"
			} else {
				row.Candidate = &Candidate{CanonicalRole(request.Role), i + 1, len(route.OrderedModelIDs) - i - 1, cloneModel(*model), cloneProvider(*provider)}
			}
		}
		result = append(result, row)
	}
	return result, nil
}
func (p *Policy) emit(e Event) {
	if p.observe != nil {
		func() { defer func() { _ = recover() }(); p.observe(e) }()
	}
}

type outcome struct {
	value any
	err   error
}

// Run executes completion policy over an injected invocation. It does not expand
// context messages: the caller supplies HasImages from the expanded prompt and
// ToolNames from the exact tools passed to the response validator.
func (p *Policy) Run(ctx context.Context, request Request, invoke Invocation) (any, error) {
	doneWork, ok := p.work.Start()
	if !ok {
		return nil, errors.New("model_policy_closed")
	}
	defer doneWork()
	if ctx == nil {
		ctx = context.Background()
	}
	if invoke == nil {
		return nil, errors.New("model_invocation_required")
	}
	request.Role = CanonicalRole(request.Role)
	request.ToolNames = append([]string(nil), request.ToolNames...)
	route := p.route(request.Role)
	if route == nil || route.TimeoutMS == nil {
		return p.run(ctx, request, invoke)
	}
	timeout := *route.TimeoutMS
	if math.IsNaN(timeout) || math.IsInf(timeout, 0) || timeout < 1 {
		return nil, errors.New("invalid_role_budget")
	}
	done := make(chan outcome, 1)
	var finished sync.Once
	settle := func(value any, err error) {
		published := false
		finished.Do(func() { published = true; done <- outcome{value, err} })
		if !published {
			releaseUnpublished(value)
		}
	}
	deadline := newDeadlineWithTimeout(ctx, timeout, "model_stage_timeout", p.clock, func(cause error) {
		settle(nil, cause)
	})
	defer deadline.Close()
	finishRunner, _ := p.work.Child()
	go func() {
		defer finishRunner()
		value, err := p.run(deadline.Context, request, invoke)
		deadline.mu.Lock()
		defer deadline.mu.Unlock()
		deadline.disposeLocked()
		settle(value, err)
	}()
	// Parent cancellation alone does not manufacture settlement. A native
	// operation may retain its scheduler slot until it actually completes, but
	// a role timeout that already settled this race remains terminal afterward.
	result := <-done
	return result.value, result.err
}
func (p *Policy) run(ctx context.Context, request Request, invoke Invocation) (any, error) {
	if ctx.Err() != nil {
		return nil, cancelled(context.Cause(ctx))
	}
	rows, err := p.Select(request)
	if err != nil {
		return nil, err
	}
	attempted, retryable := false, true
	var lastStatus *int
	failures := []error{}
	for _, row := range rows {
		if ctx.Err() != nil {
			return nil, cancelled(context.Cause(ctx))
		}
		if row.Candidate == nil {
			p.emit(Event{Kind: "skip", ModelID: row.ModelID, Index: row.Index, Code: row.SkipReason})
			continue
		}
		attempted = true
		// The source reads providerPolicy again while constructing failure context.
		// Invalid configuration therefore escapes, rather than falling back.
		if _, err := ProviderPolicy(row.Candidate.Provider); err != nil {
			return nil, err
		}
		value, err := p.attempts(ctx, *row.Candidate, invoke)
		if err == nil {
			return value, nil
		}
		if ctx.Err() != nil {
			return nil, cancelled(err)
		}
		if len(failures) == 4 {
			failures = failures[1:]
		}
		failures = append(failures, err)
		code := "provider_request_failed"
		lastStatus = nil
		if e, ok := err.(*ModelError); ok {
			retryable = retryable && e.Retryable
			lastStatus = e.Status
			code = e.Code
		} else {
			retryable = false
		}
		p.emit(Event{Kind: "failed", ModelID: row.ModelID, Index: row.Index, Code: code})
		if StopsFallback(err) {
			return nil, err
		}
	}
	code := "no_compatible_model"
	if attempted {
		code = "all_models_failed"
	}
	if !attempted || !retryable {
		lastStatus = nil
	}
	result := NewError(code, lastStatus, attempted && retryable)
	result.failures = failures
	if len(failures) > 0 {
		withCause(result, failures[len(failures)-1])
		for _, e := range failures {
			if ProviderAttemptsHandled(e) {
				result.handled.Store(true)
			}
		}
	}
	return nil, result
}
func (p *Policy) attempts(ctx context.Context, c Candidate, invoke Invocation) (any, error) {
	policy, err := ProviderPolicy(c.Provider)
	if err != nil {
		return nil, err
	}
	for attempt := 1; attempt <= policy.MaxAttempts; attempt++ {
		if ctx.Err() != nil {
			return nil, cancelled(context.Cause(ctx))
		}
		p.emit(Event{Kind: "attempt", ModelID: c.Model.ID, Index: c.Index, Attempt: attempt, MaxAttempts: policy.MaxAttempts, TimeoutMS: policy.TimeoutMS})
		if ctx.Err() != nil {
			return nil, cancelled(context.Cause(ctx))
		}
		value, err := p.execute(ctx, c, attempt, policy, invoke)
		if ctx.Err() != nil {
			releaseUnpublished(value)
			return nil, cancelled(err)
		}
		if err == nil {
			return value, nil
		}
		if attempt == policy.MaxAttempts {
			return nil, markHandled(err)
		}
		e, ok := err.(*ModelError)
		if !ok || !e.Retryable {
			return nil, markHandled(err)
		}
		delay := RetryDelay(float64(attempt), e.RetryAfterMS)
		p.emit(Event{Kind: "retry", ModelID: c.Model.ID, Index: c.Index, Attempt: attempt, MaxAttempts: policy.MaxAttempts, TimeoutMS: policy.TimeoutMS, DelayMS: delay})
		if ctx.Err() != nil {
			return nil, cancelled(context.Cause(ctx))
		}
		if err := p.sleep(ctx, delay); err != nil {
			return nil, err
		}
		if ctx.Err() != nil {
			return nil, cancelled(context.Cause(ctx))
		}
	}
	return nil, errors.New("invalid_provider_request_policy")
}
func (p *Policy) execute(parent context.Context, c Candidate, number int, policy RequestPolicy, invoke Invocation) (any, error) {
	if IsBuiltin(c.Provider.Protocol) {
		return p.executeNative(parent, c, number, policy, invoke)
	}
	builtin := IsBuiltin(c.Provider.Protocol)
	var custom *progressDeadline
	queueContext := parent
	if !builtin {
		custom = newDeadline(parent, policy.TimeoutMS, "provider_timeout", p.clock)
		defer custom.Close()
		queueContext = custom.Context
	}
	future := p.scheduler.SubmitModel(queueContext, operationPriority(queueContext, c.Role), func(ctx context.Context, complete func(any, error)) {
		if ctx.Err() != nil {
			complete(nil, attemptCancellation(parent, ctx))
			return
		}
		deadline := custom
		if builtin {
			deadline = newDeadline(ctx, policy.TimeoutMS, "provider_timeout", p.clock)
		}
		attemptCtx := deadline.Context
		var mu sync.Mutex
		settled, content := false, false
		var stopCancel func() bool
		settle := func(value any, err error) {
			mu.Lock()
			if settled {
				mu.Unlock()
				releaseUnpublished(value)
				return
			}
			settled = true
			var unpublished any
			if attemptCtx.Err() != nil {
				unpublished = value
				value = nil
				err = attemptCancellation(parent, attemptCtx)
			} else if err != nil && builtin && content {
				if e, ok := err.(*ModelError); !ok || !strings.HasPrefix(e.Code, "provider_stream_") {
					err = withCause(NewError("provider_stream_interrupted", nil, false), err)
				}
			}
			stop := stopCancel
			mu.Unlock()
			releaseUnpublished(unpublished)
			if stop != nil {
				stop()
			}
			if builtin {
				deadline.Close()
			}
			complete(value, err)
		}
		if !builtin {
			stop := context.AfterFunc(attemptCtx, func() { settle(nil, attemptCancellation(parent, attemptCtx)) })
			mu.Lock()
			stopCancel = stop
			already := settled
			mu.Unlock()
			if already {
				stop()
			}
		}
		if attemptCtx.Err() != nil {
			settle(nil, attemptCancellation(parent, attemptCtx))
			return
		}
		progress := func() {
			mu.Lock()
			defer mu.Unlock()
			if settled || attemptCtx.Err() != nil || !builtin || (c.Model.Streaming != nil && !*c.Model.Streaming) {
				return
			}
			content = true
			deadline.Progress()
		}
		attempt := Attempt{cloneCandidate(c), number, policy.MaxAttempts, policy.TimeoutMS, progress}
		func() {
			defer func() {
				if failure := recover(); failure != nil {
					if err, ok := failure.(error); ok {
						settle(nil, err)
					} else {
						settle(nil, errors.New("provider_request_failed"))
					}
				}
			}()
			invoke(attemptCtx, attempt, settle)
		}()
	})
	value, err := future.Wait()
	if custom != nil && custom.Err() != nil {
		releaseUnpublished(value)
		return nil, attemptCancellation(parent, custom.Context)
	}
	return value, err
}
func attemptCancellation(parent, attempt context.Context) error {
	if parent.Err() != nil {
		return cancelled(context.Cause(parent))
	}
	if e, ok := context.Cause(attempt).(*ModelError); ok && e.Code == "provider_timeout" {
		return withCause(NewError("provider_timeout", nil, false), e)
	}
	return cancelled(context.Cause(attempt))
}

// A completion discarded by cancellation/duplicate settlement must release its
// isolate-owned continuation. This never serializes or inspects opaque state.
func releaseUnpublished(value any) {
	if owned, ok := value.(interface{ Release() error }); ok {
		_ = owned.Release()
	}
}
