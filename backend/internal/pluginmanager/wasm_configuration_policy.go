package pluginmanager

import (
	"context"
	"errors"
	"sync"

	"github.com/killerprojecte/HumanEngine/backend/internal/credentials"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/plugins"
)

type policyGuestHost interface {
	Enable(context.Context, string, []byte) error
	BindCall(string) (func(context.Context, []byte) ([]byte, error), error)
	Disable(context.Context, string) error
}

// WASMConfigurationPolicy holds no guest while idle. Each whole policy operation borrows
// a fresh immutable binding, including every continuation, and synchronously
// unloads it before returning. The stable wrapper never captures a retired call.
// It cannot borrow or retire the enabled adapter's separate guest generation.
// The loader supplies read-only bytes and is called once per compiled scope.
func WASMConfigurationPolicy(host *plugins.WASMHost, pluginID string, load func() ([]byte, error), hasSecret func(context.Context, string) (bool, error)) BuiltinPolicy {
	// Internal guest names cannot collide with a catalog plugin ID.
	return transientPolicy(host, "policy:"+pluginID, load, hasSecret, func(ctx context.Context, artifact []byte) (compiledPolicy, error) {
		return host.Compile(ctx, artifact)
	})
}

type compiledPolicy interface {
	Enable(context.Context, string) error
	Close()
}

func transientPolicy(host policyGuestHost, id string, load func() ([]byte, error), hasSecret func(context.Context, string) (bool, error), compile func(context.Context, []byte) (compiledPolicy, error)) BuiltinPolicy {
	gate := make(chan struct{}, 1)
	gate <- struct{}{}
	policy := newTransientPolicy(host, id, hasSecret, gate, func(ctx context.Context, id string) error {
		artifact, err := load()
		if err != nil {
			return err
		}
		return host.Enable(ctx, id, artifact)
	})
	if compile != nil {
		policy.Configure = func() (BuiltinPolicy, func()) {
			var compiled compiledPolicy
			scoped := newTransientPolicy(host, id, hasSecret, gate, func(ctx context.Context, id string) error {
				if compiled == nil {
					artifact, err := load()
					if err != nil {
						return err
					}
					compiled, err = compile(ctx, artifact)
					if err != nil {
						return err
					}
				}
				return compiled.Enable(ctx, id)
			})
			return scoped, sync.OnceFunc(func() {
				if compiled != nil {
					compiled.Close()
				}
			})
		}
	}
	return policy
}

// The operation lease substitutes only the code loader; both paths use the
// same gate, fresh module, call binding and physical disable.
func newTransientPolicy(host policyGuestHost, id string, hasSecret func(context.Context, string) (bool, error), gate chan struct{}, enable func(context.Context, string) error) BuiltinPolicy {
	run := func(ctx context.Context, fn func(BuiltinPolicy) error) (err error) {
		if err = ctx.Err(); err != nil {
			return
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-gate:
		}
		defer func() { gate <- struct{}{} }()
		if err = ctx.Err(); err != nil {
			return
		}
		// Enable physically cleans its own failed initialization. Do not disable an
		// unrelated existing module if acquiring this reserved name fails.
		if err = enable(ctx, id); err != nil {
			return
		}
		defer func() { err = errors.Join(err, host.Disable(context.Background(), id)) }()
		call, err := host.BindCall(id)
		if err != nil {
			return err
		}
		return fn(configurationPolicy(call, ctx, hasSecret))
	}
	return BuiltinPolicy{
		Validate: func(ctx context.Context, value *jsjson.Object) error {
			return run(ctx, func(policy BuiltinPolicy) error { return policy.Validate(ctx, value) })
		},
		Prepare: func(ctx context.Context, value, previous *jsjson.Object) (result *jsjson.Object, changes []credentials.Change, err error) {
			err = run(ctx, func(policy BuiltinPolicy) (e error) {
				result, changes, e = policy.Prepare(ctx, value, previous)
				return
			})
			return
		},
		Secrets: func(ctx context.Context) (result *jsjson.Object, err error) {
			err = run(ctx, func(policy BuiltinPolicy) (e error) { result, e = policy.Secrets(ctx); return })
			return
		},
		CredentialFailure: func(ctx context.Context, cause error) error {
			return run(ctx, func(policy BuiltinPolicy) error { return policy.CredentialFailure(ctx, cause) })
		},
	}
}
