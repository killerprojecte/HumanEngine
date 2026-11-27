//go:build bundled_wasm

package app

import (
	"encoding/json"

	"github.com/killerprojecte/HumanEngine/backend/internal/pluginmanager"
	typing "github.com/killerprojecte/HumanEngine/backend/plugins/typing-delay"
)

func (a *Application) bindTypingDelay() error {
	return a.Plugins.RegisterBuiltin(pluginmanager.BuiltinDescriptor{
		Manifest: typing.Manifest().(json.RawMessage), LoadArtifact: typing.Artifact,
		Start: a.Plugins.StartSendDelay,
	})
}
