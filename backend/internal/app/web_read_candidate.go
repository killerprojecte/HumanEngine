//go:build web_read_candidate

package app

import (
	"context"
	"encoding/json"

	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/pluginmanager"
	"github.com/killerprojecte/HumanEngine/backend/internal/plugins"
	resource "github.com/killerprojecte/HumanEngine/backend/plugins/web-read"
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
	return a.Plugins.RegisterBuiltin(pluginmanager.BuiltinDescriptor{
		Manifest: resource.Manifest().(json.RawMessage), LoadArtifact: func() ([]byte, error) { return artifact, nil },
		Start: webread.Factory(a.Plugins), Policy: policy,
	})
}
