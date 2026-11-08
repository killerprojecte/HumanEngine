package modelservice

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"
)

// AttemptUsage deliberately contains only the numeric source telemetry fields.
// No response text, tool arguments, credentials or continuation can cross this
// observation port.
type AttemptUsage struct {
	Known                                                                  bool
	InputTokens, OutputTokens, CacheReadInputTokens, CacheWriteInputTokens *float64
}
type AttemptObservation struct {
	ModelID, ProviderID, APIModelName, Role string
	Number                                  int
	At                                      time.Time
	Duration                                time.Duration
	TransportMode                           string
	FirstTokenMS                            *float64
	FirstTokenKind                          *string
	Usage                                   *AttemptUsage
	Completed                               bool
	FailureReason                           *string
}
type AttemptObserver func(context.Context, AttemptObservation)
type attemptObserverKey struct{}
type attemptStateKey struct{}
type attemptState struct {
	mu        sync.Mutex
	closed    bool
	mode      string
	firstMS   *float64
	firstKind *string
	usage     *AttemptUsage
}

func WithAttemptObserver(ctx context.Context, observer AttemptObserver) context.Context {
	return context.WithValue(ctx, attemptObserverKey{}, observer)
}
func ObserveTransportMode(ctx context.Context, mode string) {
	state, _ := ctx.Value(attemptStateKey{}).(*attemptState)
	if state == nil {
		return
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	if !state.closed {
		state.mode = mode
	}
}
func ObserveFirstContent(ctx context.Context, kind string, ms float64) {
	state, _ := ctx.Value(attemptStateKey{}).(*attemptState)
	if state == nil {
		return
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	if !state.closed && state.firstMS == nil {
		state.firstMS = &ms
		state.firstKind = &kind
	}
}
func copyAttemptUsage(u *AttemptUsage) *AttemptUsage {
	if u == nil {
		return nil
	}
	return &AttemptUsage{u.Known, copyNumber(u.InputTokens), copyNumber(u.OutputTokens), copyNumber(u.CacheReadInputTokens), copyNumber(u.CacheWriteInputTokens)}
}
func AttemptObservationActive(ctx context.Context) bool {
	state, _ := ctx.Value(attemptStateKey{}).(*attemptState)
	if state == nil {
		return false
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	return !state.closed
}
func ObserveAttemptUsage(ctx context.Context, usage *AttemptUsage) {
	state, _ := ctx.Value(attemptStateKey{}).(*attemptState)
	if state == nil {
		return
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	if !state.closed {
		state.usage = copyAttemptUsage(usage)
	}
}
func (p *Policy) executeObserved(ctx context.Context, c Candidate, number int, policy RequestPolicy, invoke Invocation) (value any, err error) {
	observer, _ := ctx.Value(attemptObserverKey{}).(AttemptObserver)
	if observer == nil || c.Role == "models" {
		return p.execute(ctx, c, number, policy, invoke)
	}
	mode := "custom"
	if IsBuiltin(c.Provider.Protocol) {
		mode = "non_stream"
		if c.Role != "embedding" && (c.Model.Streaming == nil || *c.Model.Streaming) {
			mode = "stream"
		}
	}
	state := &attemptState{mode: mode}
	at := time.Now()
	// Source starts before scheduler checkout. Image reschedules remain within
	// execute and therefore keep this same observation and first deadline.
	defer func() {
		state.mu.Lock()
		state.closed = true
		event := AttemptObservation{ModelID: c.Model.ID, ProviderID: c.Provider.ID, APIModelName: c.Model.APIModelName, Role: c.Role, Number: number, At: at, Duration: time.Since(at), TransportMode: state.mode, FirstTokenMS: copyNumber(state.firstMS), Completed: err == nil}
		if state.firstKind != nil {
			kind := *state.firstKind
			event.FirstTokenKind = &kind
		}
		if err == nil {
			event.Usage = copyAttemptUsage(state.usage)
		} else {
			reason := attemptFailureReason(ctx, state.mode, state.firstMS, err)
			event.FailureReason = &reason
		}
		state.mu.Unlock()
		// No business result is passed to the optional callback. Callback execution
		// remains inside the accepted Policy runner's physical lifetime.
		func() { defer func() { _ = recover() }(); observer(ctx, event) }()
	}()
	return p.execute(context.WithValue(ctx, attemptStateKey{}, state), c, number, policy, invoke)
}
func attemptFailureReason(ctx context.Context, mode string, first *float64, err error) string {
	code := ""
	var model *ModelError
	if errors.As(err, &model) {
		code = model.Code
	}
	// Source wraps validation errors after actual first content before classifying.
	if mode == "stream" && first != nil && ctx.Err() == nil && !strings.HasPrefix(code, "provider_stream_") {
		code = "provider_stream_interrupted"
	}
	if code == "provider_stream_idle_timeout" {
		return "stream_idle_timeout"
	}
	if code == "provider_timeout" || code == "model_stage_timeout" || (context.Cause(ctx) != nil && context.Cause(ctx).Error() == "model_stage_timeout") {
		if mode == "stream" {
			return "first_content_timeout"
		}
		return "request_timeout"
	}
	if ctx.Err() != nil {
		return "cancelled"
	}
	if strings.HasPrefix(code, "provider_stream_") {
		return "stream_interrupted"
	}
	return "request_failed"
}
