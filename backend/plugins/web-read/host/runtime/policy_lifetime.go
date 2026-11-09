//go:build web_read_candidate

package webreadruntime

import (
	"context"
	"errors"

	"github.com/killerprojecte/HumanEngine/backend/internal/credentials"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/pluginmanager"
	"github.com/killerprojecte/HumanEngine/backend/internal/plugins"
)

type transientPolicyHost interface {
	Enable(context.Context, string, []byte) error
	BindCallWithAdmission(string) (func(context.Context, context.Context, []byte) ([]byte, error), error)
	Disable(context.Context, string) error
}

// TransientPolicy never leaves a policy guest resident while disabled or idle.
// The source has no prepare/secret hooks and no state spanning its two validation
// calls, so each explicit validation owns one synchronous load/call/unload. The
// fixed private ID is protected by cancellable admission, never a global lock.
func TransientPolicy(host *plugins.WASMHost, artifact []byte) pluginmanager.BuiltinPolicy {
	if host == nil {
		return transientPolicy(nil, artifact)
	}
	return transientPolicy(host, artifact)
}

func transientPolicy(host transientPolicyHost, artifact []byte) pluginmanager.BuiltinPolicy {
	artifact = append([]byte(nil), artifact...)
	gate := make(chan struct{}, 1)
	gate <- struct{}{}
	return pluginmanager.BuiltinPolicy{
		Validate: func(ctx context.Context, input *jsjson.Object) (err error) {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-gate:
			}
			defer func() { gate <- struct{}{} }()
			if err = ctx.Err(); err != nil {
				return err
			}
			if host == nil {
				return errors.New("web_read_policy_host_missing")
			}
			const id = "web-read-policy"
			if err = host.Enable(ctx, id, artifact); err != nil {
				return err
			}
			// Use a fresh uncancelled join context: a cancelled caller cannot make
			// successful completion leave this temporary guest behind.
			defer func() { err = errors.Join(err, host.Disable(context.Background(), id)) }()
			call, err := host.BindCallWithAdmission(id)
			if err != nil {
				return err
			}
			return Policy(call, context.Background()).Validate(ctx, input)
		},
		Prepare: func(_ context.Context, input, _ *jsjson.Object) (*jsjson.Object, []credentials.Change, error) {
			return input, nil, nil
		},
		Secrets: func(context.Context) (*jsjson.Object, error) { return new(jsjson.Object), nil },
	}
}
