//go:build web_read_candidate

package app

import (
	"context"

	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/plugins"
	webread "github.com/killerprojecte/HumanEngine/backend/plugins/web-read/host/runtime"
)

// bindWebRead is an explicitly selected validation-only installation. The
// default builtin list and distributed artifact do not enable this candidate.
func (a *Application) bindWebRead(ctx context.Context) error {
	artifact, err := plugins.BuiltinArtifact("web-read")
	if err != nil {
		return err
	}
	policy := webread.TransientPolicy(a.WASM, artifact)
	// A default/old artifact cannot pass admission just because its manifest exists.
	if err = policy.Validate(ctx, new(jsjson.Object)); err != nil {
		return err
	}
	if err = a.Plugins.WithBuiltinPolicy("web-read", policy); err != nil {
		return err
	}
	return a.Plugins.WithBuiltinRuntime("web-read", webread.Factory(a.Plugins))
}
