package host

import (
	"context"

	"github.com/killerprojecte/HumanEngine/backend/internal/dispatch"
	"github.com/killerprojecte/HumanEngine/backend/internal/pluginmanager"
)

type removeDelay func()

func (remove removeDelay) Dispose() { remove() }

// The plugin has no separate work owner: its waits borrow the manager's work
// admission, and cancelling the generation revokes them as before.
func (removeDelay) CloseAndWait(context.Context) error { return nil }

// Factory installs the plugin's delay in the host dispatch capability.
// Work remains tracked by the manager, without an extra plugin worker owner.
func Factory(install func(context.Context, dispatch.TypingDelay) (func(), error)) pluginmanager.BuiltinRuntimeFactory {
	return func(_ context.Context, in pluginmanager.BuiltinRuntimeInput) (pluginmanager.BuiltinLifetime, error) {
		remove, err := install(in.Life, Delay(Ports{
			Life: in.Life, Config: in.Config, BeginWork: in.BeginWork,
			Current: in.Current, Lifetime: in.JoinLife, CallGuest: in.CallGuest,
		}))
		if err != nil {
			return nil, err
		}
		return removeDelay(remove), nil
	}
}
