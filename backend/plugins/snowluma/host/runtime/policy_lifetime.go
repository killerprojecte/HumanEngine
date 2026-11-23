package snowlumaruntime

import (
	"context"
	"errors"
	"sync"

	"github.com/killerprojecte/HumanEngine/backend/internal/credentials"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/pluginmanager"
	"github.com/killerprojecte/HumanEngine/backend/internal/plugins"
)

type policyGuestHost interface {
	Enable(context.Context, string, []byte) error
	BindCall(string) (func(context.Context, []byte) ([]byte, error), error)
	Disable(context.Context, string) error
}

// TransientPolicy holds no guest while idle. Each whole policy operation borrows
// a fresh immutable binding, including every continuation, and synchronously
// unloads it before returning. The stable wrapper never captures a retired call.
// It cannot borrow or retire the enabled adapter's separate guest generation.
func TransientPolicy(host *plugins.WASMHost, id string, artifact []byte, hasSecret func(context.Context, string) (bool, error)) pluginmanager.BuiltinPolicy {
	return transientPolicy(host, id, artifact, hasSecret, func(ctx context.Context, artifact []byte) (compiledPolicy, error) {
		return host.Compile(ctx, artifact)
	})
}

type compiledPolicy interface {
	Enable(context.Context, string) error
	Close()
}

func transientPolicy(host policyGuestHost, id string, artifact []byte, hasSecret func(context.Context, string) (bool, error), compile func(context.Context, []byte) (compiledPolicy, error)) pluginmanager.BuiltinPolicy {
	artifact = append([]byte(nil), artifact...)
	gate := make(chan struct{}, 1)
	gate <- struct{}{}
	policy := newTransientPolicy(host, id, artifact, hasSecret, gate, host.Enable)
	if compile != nil {
		policy.Configure = func() (pluginmanager.BuiltinPolicy, func()) {
			var compiled compiledPolicy
			scoped := newTransientPolicy(host, id, artifact, hasSecret, gate, func(ctx context.Context, id string, artifact []byte) error {
				if compiled == nil {
					var err error
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
func newTransientPolicy(host policyGuestHost, id string, artifact []byte, hasSecret func(context.Context, string) (bool, error), gate chan struct{}, enable func(context.Context, string, []byte) error) pluginmanager.BuiltinPolicy {
	run := func(ctx context.Context, fn func(pluginmanager.BuiltinPolicy) error) (err error) {
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
		if err = enable(ctx, id, artifact); err != nil {
			return
		}
		defer func() { err = errors.Join(err, host.Disable(context.Background(), id)) }()
		call, err := host.BindCall(id)
		if err != nil {
			return err
		}
		return fn(Policy(call, ctx, hasSecret))
	}
	return pluginmanager.BuiltinPolicy{
		Validate: func(ctx context.Context, value *jsjson.Object) error {
			return run(ctx, func(policy pluginmanager.BuiltinPolicy) error { return policy.Validate(ctx, value) })
		},
		Prepare: func(ctx context.Context, value, previous *jsjson.Object) (result *jsjson.Object, changes []credentials.Change, err error) {
			err = run(ctx, func(policy pluginmanager.BuiltinPolicy) (e error) {
				result, changes, e = policy.Prepare(ctx, value, previous)
				return
			})
			return
		},
		Secrets: func(ctx context.Context) (result *jsjson.Object, err error) {
			err = run(ctx, func(policy pluginmanager.BuiltinPolicy) (e error) { result, e = policy.Secrets(ctx); return })
			return
		},
		CredentialFailure: func(ctx context.Context, cause error) error {
			return run(ctx, func(policy pluginmanager.BuiltinPolicy) error { return policy.CredentialFailure(ctx, cause) })
		},
	}
}
