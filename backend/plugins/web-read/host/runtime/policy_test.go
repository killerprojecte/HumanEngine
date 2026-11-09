//go:build web_read_candidate

package webreadruntime

import (
	"context"
	"encoding/json"
	"os"
	"reflect"
	"testing"

	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/plugins"
)

func TestInstalledPolicyUsesActualGuestAndSourceFixtures(t *testing.T) {
	wasmPath, fixturePath := os.Getenv("WEB_READ_WASM"), os.Getenv("WEB_READ_INSTALLATION_FIXTURE")
	if wasmPath == "" || fixturePath == "" {
		t.Fatal("fresh WEB_READ_WASM and WEB_READ_INSTALLATION_FIXTURE required")
	}
	artifact, err := os.ReadFile(wasmPath)
	if err != nil {
		t.Fatal(err)
	}
	fixture, err := os.ReadFile(fixturePath)
	if err != nil {
		t.Fatal(err)
	}
	var inputs struct {
		Cases       []json.RawMessage `json:"cases"`
		ToolCases   []json.RawMessage `json:"toolCases"`
		Definitions any               `json:"definitions"`
	}
	if err = json.Unmarshal(fixture, &inputs); err != nil {
		t.Fatal(err)
	}
	wasm, err := plugins.NewWASMHost(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := wasm.Close(context.Background()); err != nil {
			t.Error(err)
		}
	}()
	if err = wasm.Enable(context.Background(), "web-read-policy", artifact); err != nil {
		t.Fatal(err)
	}
	call, err := wasm.BindCallWithAdmission("web-read-policy")
	if err != nil {
		t.Fatal(err)
	}
	policy := Policy(call, context.Background())
	for _, raw := range inputs.Cases {
		var input struct {
			Config   json.RawMessage `json:"config"`
			Expected any             `json:"expected"`
		}
		_ = json.Unmarshal(raw, &input)
		output, err := call(context.Background(), context.Background(), raw)
		if err != nil {
			t.Fatal(err)
		}
		var actual any
		if err = json.Unmarshal(output, &actual); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(input.Expected, actual) {
			t.Fatalf("actual guest source mismatch %s: %s", raw, output)
		}
		parsed, err := jsjson.ParseBytes(input.Config)
		if err != nil {
			t.Fatal(err)
		}
		if object, ok := parsed.(*jsjson.Object); ok {
			valid := input.Expected.(map[string]any)["valid"].(bool)
			if err = policy.Validate(context.Background(), object); (err == nil) != valid {
				t.Fatal("policy adapter changed guest validation", string(raw), err)
			}
		}
	}
	for _, raw := range inputs.ToolCases {
		var testcase struct {
			Expected any `json:"expected"`
		}
		_ = json.Unmarshal(raw, &testcase)
		result, err := call(context.Background(), context.Background(), raw)
		if err != nil {
			t.Fatal(err)
		}
		var actual any
		_ = json.Unmarshal(result, &actual)
		if !reflect.DeepEqual(testcase.Expected, actual) {
			t.Fatalf("actual manager policy mismatch %s: %s", raw, result)
		}
	}
	raw, err := call(context.Background(), context.Background(), []byte(`{"operation":"registrations"}`))
	if err != nil {
		t.Fatal(err)
	}
	var definitions any
	_ = json.Unmarshal(raw, &definitions)
	if !reflect.DeepEqual(inputs.Definitions, definitions) {
		t.Fatal("actual guest registrations differ from source")
	}
	if err = wasm.Disable(context.Background(), "web-read-policy"); err != nil {
		t.Fatal(err)
	}
	if err = policy.Validate(context.Background(), new(jsjson.Object)); err == nil {
		t.Fatal("retained policy entered unloaded instance")
	}
}
func TestInstalledPolicyRejectsDefaultGuest(t *testing.T) {
	path := os.Getenv("WEB_READ_DEFAULT_WASM")
	if path == "" {
		t.Fatal("WEB_READ_DEFAULT_WASM must contain separately built default guest")
	}
	artifact, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	wasm, err := plugins.NewWASMHost(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := wasm.Close(context.Background()); err != nil {
			t.Error(err)
		}
	}()
	if err = wasm.Enable(context.Background(), "default-web-read-policy", artifact); err != nil {
		t.Fatal(err)
	}
	call, err := wasm.BindCallWithAdmission("default-web-read-policy")
	if err != nil {
		t.Fatal(err)
	}
	if err = Policy(call, context.Background()).Validate(context.Background(), new(jsjson.Object)); err == nil || err.Error() != "web_read_policy_artifact_mismatch" {
		t.Fatal("old artifact admitted", err)
	}
}

func TestTransientPolicyUnloadsSuccessFailureAndCancellation(t *testing.T) {
	path := os.Getenv("WEB_READ_WASM")
	if path == "" {
		t.Fatal("WEB_READ_WASM required")
	}
	artifact, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	wasm, err := plugins.NewWASMHost(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := wasm.Close(context.Background()); err != nil {
			t.Error(err)
		}
	}()
	policy := TransientPolicy(wasm, artifact)
	unloaded := func() {
		t.Helper()
		if _, err := wasm.BindCall("web-read-policy"); err == nil {
			t.Fatal("policy guest remained resident")
		}
	}
	if err = policy.Validate(context.Background(), new(jsjson.Object)); err != nil {
		t.Fatal(err)
	}
	unloaded()
	invalid, _ := jsjson.Parse(`{"maxChars":0}`)
	if err = policy.Validate(context.Background(), invalid.(*jsjson.Object)); err == nil {
		t.Fatal("invalid config accepted")
	}
	unloaded()
	cancelled, stop := context.WithCancel(context.Background())
	stop()
	if err = policy.Validate(cancelled, new(jsjson.Object)); err == nil {
		t.Fatal("cancelled validation admitted")
	}
	unloaded()
	// Subsequent valid inspection must not borrow or be cancelled by a stale guest.
	if err = policy.Validate(context.Background(), new(jsjson.Object)); err != nil {
		t.Fatal(err)
	}
	unloaded()
	if _, err = policy.Secrets(context.Background()); err != nil {
		t.Fatal(err)
	}
	unloaded()
	if _, _, err = policy.Prepare(context.Background(), new(jsjson.Object), new(jsjson.Object)); err != nil {
		t.Fatal(err)
	}
	unloaded()
}
