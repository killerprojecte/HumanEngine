package pluginmanager

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/toolresults"
)

func (i *item) wasm() bool { return i.artifact != nil }

// RegisterExternalV2 loads only an explicitly configured, read-tool manifest.
// The artifact is resolved within the plugin root again when a generation starts.
func (m *Manager) RegisterExternalV2(entry string) error {
	if !filepath.IsLocal(entry) || !strings.HasSuffix(entry, ".v2.json") {
		return errors.New("invalid_local_plugin_path")
	}
	root, err := os.OpenRoot(m.root)
	if err != nil {
		return err
	}
	defer root.Close()
	raw, err := root.ReadFile(entry)
	if err != nil {
		return err
	}
	parsed, err := jsjson.ParseBytes(raw)
	if err != nil {
		return err
	}
	manifest, ok := parsed.(*jsjson.Object)
	capabilities, _ := prop(manifest, "capabilities").([]any)
	if !ok || text(manifest, "name") == "" || text(manifest, "version") == "" || len(capabilities) != 1 || capabilities[0] != "tool" {
		return errors.New("external_v2_manifest_invalid")
	}
	artifact := text(manifest, "artifact")
	if !filepath.IsLocal(artifact) || !strings.HasSuffix(artifact, ".wasm") {
		return errors.New("invalid_local_plugin_path")
	}
	path := filepath.Join(filepath.Dir(entry), artifact)
	manifest.Delete("artifact")
	if _, found := manifest.Get("configSchema"); found {
		return errors.New("external_v2_configuration_unsupported")
	}
	_ = manifest.Set("configSchema", objectJSON(map[string]any{"type": "object", "additionalProperties": false}))
	wire, err := jsjson.Marshal(manifest)
	if err != nil {
		return err
	}
	return m.registerWASM(BuiltinDescriptor{
		Manifest: wire, LoadArtifact: func() ([]byte, error) {
			root, err := os.OpenRoot(m.root)
			if err != nil {
				return nil, err
			}
			defer root.Close()
			return root.ReadFile(path)
		}, Start: m.startExternalV2,
	}, false)
}

// The tool bridge and WASM host already own all work for this guest.
type externalV2Lifetime struct{}

func (externalV2Lifetime) Dispose()                           {}
func (externalV2Lifetime) CloseAndWait(context.Context) error { return nil }

func (m *Manager) startExternalV2(ctx context.Context, in BuiltinRuntimeInput) (BuiltinLifetime, error) {
	definitions, err := in.CallGuest(ctx, []byte(`{"operation":"registrations"}`))
	if err != nil {
		return nil, err
	}
	call, err := m.wasm.BindCallWithAdmission(in.ID)
	if err != nil {
		return nil, err
	}
	_, err = m.InstallBuiltinTools(in, definitions, func(wait context.Context, name string, raw json.RawMessage, _ func(context.Context, toolresults.Source, toolresults.Options) (toolresults.Result, error)) (json.RawMessage, error) {
		request, err := json.Marshal(struct {
			Operation string          `json:"operation"`
			Tool      string          `json:"tool"`
			Input     json.RawMessage `json:"input"`
		}{"toolExecute", name, raw})
		if err != nil {
			return nil, err
		}
		// Planner cancellation must not close the shared instance. The bridge
		// waits for physical execution and rejects an abandoned result.
		return call(wait, in.Life, request)
	})
	if err != nil {
		return nil, err
	}
	return externalV2Lifetime{}, nil
}
