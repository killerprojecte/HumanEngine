package snowlumatransport

import (
	"context"
	"crypto/sha256"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/plugins"
)

// This is the send business oracle. The source action is intercepted to expose
// its plan and callback order; real network ownership is tested separately.
func TestSendActualSourceGuest(t *testing.T) {
	raw, err := os.ReadFile("../../core/testdata/send-source.json")
	if err != nil {
		t.Fatal(err)
	}
	rows, err := jsjson.ParseBytes(raw)
	if err != nil {
		t.Fatal(err)
	}
	artifactPath := filepath.Join(t.TempDir(), "snowluma.wasm")
	build := exec.Command("go", "build", "-buildvcs=false", "-buildmode=c-shared", "-o", artifactPath, "../../wasm")
	build.Env = append(os.Environ(), "GOOS=wasip1", "GOARCH=wasm")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("guest build: %v %s", err, out)
	}
	artifact, err := os.ReadFile(artifactPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("actual guest SHA256 %x", sha256.Sum256(artifact))
	host, err := plugins.NewWASMHost(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer host.Close(context.Background())
	if err = host.Enable(context.Background(), "snowluma", artifact); err != nil {
		t.Fatal(err)
	}
	call, err := host.BindCall("snowluma")
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range rows.([]any) {
		row := v.(*jsjson.Object)
		name, _ := row.Get("name")
		t.Run(name.(string), func(t *testing.T) {
			get := func(o *jsjson.Object, key string) any { v, _ := o.Get(key); return v }
			input := get(row, "input").(*jsjson.Object)
			initial := input
			scenario := get(row, "scenario").(*jsjson.Object)
			trace, requests := []any{}, []any{}
			var receipt any
			for step := 0; step < 100; step++ {
				wire, err := jsjson.Marshal(input)
				if err != nil {
					t.Fatal(err)
				}
				raw, err := call(context.Background(), wire)
				if err != nil {
					t.Fatal(err)
				}
				value, err := jsjson.ParseBytes(raw)
				if err != nil {
					t.Fatal(err)
				}
				result := value.(*jsjson.Object)
				if get(result, "kind") == "receipt" {
					receipt = get(result, "receipt")
					break
				}
				state, _ := get(result, "state").(*jsjson.Object)
				scene := get(initial, "scene").(*jsjson.Object)
				switch get(result, "kind") {
				case "resolve_source":
					id := get(result, "assetId")
					trace = append(trace, wsObject("kind", "resolve_source", "assetId", id, "sceneId", get(scene, "id")))
					input = wsObject("operation", "send_source", "state", state)
					if id == "managed" || id == "source-array" {
						var kind any = "image"
						if id == "source-array" {
							kind = []any{"image"}
						}
						_ = input.Set("source", wsObject("type", kind, "path", "/synthetic/fixture", "bytes", float64(3), "hash", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"))
					}
				case "resolve_media":
					id := get(result, "assetId")
					trace = append(trace, wsObject("kind", "resolve_media", "assetId", id, "sceneId", get(scene, "id")))
					input = wsObject("operation", "send_media", "state", state)
					if id == "fallback" || id == "fallback-array" {
						var kind any = "image"
						if id == "fallback-array" {
							kind = []any{"image"}
						}
						_ = input.Set("media", wsObject("type", kind, "file", "https://example.invalid/image"))
					}
				case "action":
					sources := get(result, "sources").([]any)
					requests = append(requests, wsObject("name", get(result, "action"), "params", get(result, "params"), "sources", sources))
					trace = append(trace, wsObject("kind", "before_transmit"))
					failed := get(scenario, "failBefore") == true
					if !failed {
						for range sources {
							trace = append(trace, wsObject("kind", "validate"))
							if get(scenario, "failValidation") == true {
								failed = true
								break
							}
						}
					}
					input = wsObject("operation", "send_result", "adapter", "snowluma", "account", "42", "scene", scene)
					switch {
					case failed || get(scenario, "ordinaryError") == true:
						_ = input.Set("actionError", wsObject("code", "send_failed", "transmitted", false))
					case get(scenario, "actionError") != nil:
						_ = input.Set("actionError", get(scenario, "actionError"))
					default:
						if response, exists := scenario.Get("response"); exists {
							_ = input.Set("response", response)
						} else {
							_ = input.Set("response", wsObject("message_id", float64(11)))
						}
					}
				default:
					t.Fatalf("unexpected guest stage %s", raw)
				}
			}
			if receipt == nil {
				t.Fatal("send continuation did not finish")
			}
			for key, actual := range map[string]any{"receipt": receipt, "trace": trace, "requests": requests} {
				expected := get(row, key)
				if !reflect.DeepEqual(normalizationValue(actual), normalizationValue(expected)) {
					got, _ := jsjson.Stringify(actual)
					want, _ := jsjson.Stringify(expected)
					t.Fatalf("%s: actual %s source %s", key, got, want)
				}
			}
		})
	}
}
