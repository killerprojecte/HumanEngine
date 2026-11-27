package pluginmanager

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/toolresults"
)

func (i *item) wasm() bool { return i.artifact != nil }

type V2AdapterDescriptor struct {
	PluginID  string
	AdapterID string
	Wire      string
}

type V2AdapterBinding struct {
	Schema *jsjson.Object
	Start  BuiltinRuntimeFactory
	Policy BuiltinPolicy
}

type V2AdapterResolver func(V2AdapterDescriptor) (V2AdapterBinding, error)

// RegisterExternalV2 resolves an explicitly configured manifest within the
// plugin root again when a generation starts.
func (m *Manager) RegisterExternalV2(entry string, resolve V2AdapterResolver) error {
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
	sendDelay := len(capabilities) == 2 && slices.Contains(capabilities, "send-delay") && slices.Contains(capabilities, "schema-form")
	publicGET := len(capabilities) == 2 && slices.Contains(capabilities, "tool") &&
		slices.Contains(capabilities, "schema-form") && text(manifest, "toolWire") == "public-get-v1"
	if !ok || !validID(text(manifest, "id")) || text(manifest, "name") == "" || text(manifest, "version") == "" ||
		(!sendDelay && !publicGET && (len(capabilities) != 1 || (capabilities[0] != "tool" && capabilities[0] != "adapter"))) {
		return errors.New("external_v2_manifest_invalid")
	}
	artifact := text(manifest, "artifact")
	if !filepath.IsLocal(artifact) || !strings.HasSuffix(artifact, ".wasm") {
		return errors.New("invalid_local_plugin_path")
	}
	path := filepath.Join(filepath.Dir(entry), artifact)
	var expected []byte
	if value, found := manifest.Get("artifactSha256"); found {
		encoded, _ := value.(string)
		expected, err = hex.DecodeString(encoded)
		if err != nil || len(expected) != sha256.Size {
			return errors.New("external_v2_manifest_invalid")
		}
		manifest.Delete("artifactSha256")
	}
	manifest.Delete("artifact")
	binding := V2AdapterBinding{
		Schema: objectJSON(map[string]any{"type": "object", "additionalProperties": false}),
		Start:  m.startExternalV2,
	}
	rawSchema, hasSchema := manifest.Get("configSchema")
	switch {
	case sendDelay:
		schema, _ := rawSchema.(*jsjson.Object)
		if text(schema, "type") != "object" || schemaDeclaresSecret(schema) {
			return errors.New("external_v2_send_delay_schema_invalid")
		}
		binding.Schema, binding.Start = schema, m.StartSendDelay
	case publicGET:
		schema, _ := rawSchema.(*jsjson.Object)
		if text(schema, "type") != "object" || schemaDeclaresSecret(schema) {
			return errors.New("external_v2_public_get_schema_invalid")
		}
		binding.Schema, binding.Start = schema, m.startExternalPublicGET
		manifest.Delete("toolWire")
	case hasSchema:
		return errors.New("external_v2_configuration_unsupported")
	case capabilities[0] == "adapter":
		adapter, _ := prop(manifest, "adapter").(*jsjson.Object)
		descriptor := V2AdapterDescriptor{
			PluginID: text(manifest, "id"), AdapterID: text(adapter, "id"), Wire: text(adapter, "wire"),
		}
		if resolve == nil || descriptor.AdapterID == "" || descriptor.Wire == "" {
			return errors.New("external_v2_adapter_descriptor_invalid")
		}
		binding, err = resolve(descriptor)
		if err != nil {
			return err
		}
		manifest.Delete("adapter")
	}
	_ = manifest.Set("configSchema", binding.Schema)
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
			artifact, err := root.ReadFile(path)
			if err != nil {
				return nil, err
			}
			if expected != nil && sha256.Sum256(artifact) != [sha256.Size]byte(expected) {
				return nil, NewStartupError("plugin_artifact_digest_mismatch", nil)
			}
			return artifact, nil
		}, Start: binding.Start, Policy: binding.Policy,
	}, false)
}

// A schema-form send hook has no credential policy. Do not accept secret
// fields that would otherwise be treated as ordinary plugin configuration.
func schemaDeclaresSecret(schema *jsjson.Object) bool {
	if schema == nil {
		return false
	}
	if prop(schema, "secret") == true {
		return true
	}
	if child, _ := prop(schema, "items").(*jsjson.Object); schemaDeclaresSecret(child) {
		return true
	}
	if properties, _ := prop(schema, "properties").(*jsjson.Object); properties != nil {
		for _, property := range properties.Members() {
			if child, _ := property.Value.(*jsjson.Object); schemaDeclaresSecret(child) {
				return true
			}
		}
	}
	return false
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
