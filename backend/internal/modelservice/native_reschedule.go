package modelservice

import (
	"context"
	"strings"
	"sync"
)

// nativeReschedule is a core-only scheduler continuation, not retry eligibility.
// It releases the current native slot and reacquires one under the same deadline.
type nativeReschedule struct{ invoke Invocation }

func (*nativeReschedule) Error() string             { return "native_image_reschedule" }
func RescheduleNativeImage(invoke Invocation) error { return &nativeReschedule{invoke} }
func (p *Policy) executeNative(parent context.Context, c Candidate, number int, policy RequestPolicy, invoke Invocation) (any, error) {
	var deadline *progressDeadline
	defer func() {
		if deadline != nil {
			deadline.Close()
		}
	}()
	queueContext := parent
	for {
		future := p.scheduler.SubmitModel(queueContext, operationPriority(queueContext, c.Role), func(ctx context.Context, complete func(any, error)) {
			if ctx.Err() != nil {
				complete(nil, attemptCancellation(parent, ctx))
				return
			}
			if deadline == nil {
				deadline = newDeadline(ctx, policy.TimeoutMS, "provider_timeout", p.clock)
			}
			attemptContext := deadline.Context
			var mu sync.Mutex
			settled, content := false, false
			settle := func(value any, err error) {
				mu.Lock()
				if settled {
					mu.Unlock()
					return
				}
				settled = true
				if attemptContext.Err() != nil {
					value = nil
					err = attemptCancellation(parent, attemptContext)
				} else if err != nil && content {
					if e, ok := err.(*ModelError); !ok || (!strings.HasPrefix(e.Code, "provider_stream_") && !IsBudgetFailureCode(e.Code)) {
						err = withCause(NewError("provider_stream_interrupted", nil, false), err)
					}
				}
				mu.Unlock()
				complete(value, err)
			}
			progress := func() {
				mu.Lock()
				defer mu.Unlock()
				if settled || attemptContext.Err() != nil || (c.Model.Streaming != nil && !*c.Model.Streaming) {
					return
				}
				content = true
				deadline.Progress()
			}
			if attemptContext.Err() != nil {
				settle(nil, attemptCancellation(parent, attemptContext))
				return
			}
			attempt := Attempt{cloneCandidate(c), number, policy.MaxAttempts, policy.TimeoutMS, progress}
			func() {
				defer func() {
					if failure := recover(); failure != nil {
						if e, ok := failure.(error); ok {
							settle(nil, e)
						} else {
							settle(nil, NewError("provider_request_failed", nil, false))
						}
					}
				}()
				invoke(attemptContext, attempt, settle)
			}()
		})
		value, err := future.Wait()
		if next, ok := err.(*nativeReschedule); ok && next.invoke != nil {
			invoke = next.invoke
			queueContext = deadline.Context
			continue
		}
		if deadline != nil && deadline.Err() != nil {
			return nil, attemptCancellation(parent, deadline.Context)
		}
		return value, err
	}
}
