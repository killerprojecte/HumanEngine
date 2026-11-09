package providertransport

import (
	"context"
	"github.com/killerprojecte/HumanEngine/backend/internal/modelservice"
)

// CloseAndWait seals new model operations, then drains policy runners and actual
// physical invocations. Neither a role timeout nor cancellation is physical
// completion. Failed waits may be retried and never authorize closing owners.
func (m *NativeModels) CloseAndWait(ctx context.Context) error {
	if err := m.requests.Wait(ctx); err != nil {
		return err
	}
	if err := m.drainRouters(ctx); err != nil {
		return err
	}
	if m.Policy != nil {
		if err := m.Policy.Drain(ctx); err != nil {
			return err
		}
	}
	return m.attempts.Wait(ctx)
}
func (m *NativeModels) asyncInvocation(invoke modelservice.Invocation) modelservice.Invocation {
	return func(ctx context.Context, a modelservice.Attempt, settle func(any, error)) {
		done, ok := m.attempts.Start()
		if !ok {
			settle(nil, safe("native_models_closed"))
			return
		}
		physicalDone, physicalAccepted := physicalChild(ctx)
		if !physicalAccepted {
			done()
			settle(nil, safe("provider_scope_closed"))
			return
		}
		go func() {
			defer done()
			defer physicalDone()
			defer func() {
				if recover() != nil {
					settle(nil, safe("provider_request_failed"))
				}
			}()
			invoke(ctx, a, settle)
		}()
	}
}
