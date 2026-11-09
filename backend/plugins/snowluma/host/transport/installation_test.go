package snowlumatransport

import (
	"context"
	"crypto/sha256"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/plugins"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"
)

// The actual source setup oracle aborts before connect. This test covers guest
// credential/prepare decisions only, not native installation or physical drain.
func TestInstallationActualSourceGuest(t *testing.T) {
	raw, err := os.ReadFile("../../core/testdata/installation-source.json")
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

	get := func(o *jsjson.Object, key string) any {
		if o == nil {
			return nil
		}
		v, _ := o.Get(key)
		return v
	}
	for _, v := range rows.([]any) {
		row := v.(*jsjson.Object)
		t.Run(get(row, "name").(string), func(t *testing.T) {
			scenario := get(row, "scenario").(*jsjson.Object)
			prepare := get(scenario, "kind") == "prepare"
			validate := get(scenario, "kind") == "validate"
			input := wsObject("operation", "startup_begin", "config", get(row, "parsed"))
			if prepare {
				input = wsObject("operation", "configuration_begin", "config", get(scenario, "config"), "previous", get(scenario, "previous"))
			}
			if validate {
				input = wsObject("operation", "configuration_validate", "config", get(scenario, "config"))
			}
			trace := []any{}
			var result, code any
			done := false
			for step := 0; step < 12; step++ {
				wire, e := jsjson.Marshal(input)
				if e != nil {
					t.Fatal(e)
				}
				raw, e := call(context.Background(), wire)
				if e != nil {
					t.Fatal(e)
				}
				value, e := jsjson.ParseBytes(raw)
				if e != nil {
					t.Fatal(e)
				}
				decision := value.(*jsjson.Object)
				kind := get(decision, "kind")
				state := get(decision, "state")
				key := get(decision, "key")
				switch kind {
				case "secret_apply":
					entry := wsObject("kind", "apply", "key", key, "action", get(decision, "action"))
					if v, ok := decision.Get("value"); ok {
						_ = entry.Set("value", v)
					}
					trace = append(trace, entry)
					input = wsObject("operation", "configuration_applied", "state", state)
				case "secret_get":
					trace = append(trace, wsObject("kind", "get", "key", key))
					secrets, _ := get(scenario, "secrets").(*jsjson.Object)
					input = wsObject("operation", "startup_secret", "state", state, "present", false)
					if secrets != nil {
						if v, exists := secrets.Get(key.(string)); exists {
							_ = input.Set("present", true)
							_ = input.Set("value", v)
						}
					}
				case "environment_get":
					env, _ := get(scenario, "env").(*jsjson.Object)
					input = wsObject("operation", "startup_environment", "state", state)
					if env != nil {
						if v, exists := env.Get(get(decision, "name").(string)); exists {
							_ = input.Set("value", v)
						}
					}
				case "issues":
					expected, _ := get(row, "error").(*jsjson.Object)
					a, _ := jsjson.Stringify(get(decision, "issues"))
					b, _ := jsjson.Stringify(get(expected, "issues"))
					if a != b {
						t.Fatalf("issues %s != %s", a, b)
					}
					done = true
				case "configuration":
					result = get(decision, "config")
					done = true
				case "error":
					code = get(decision, "code")
					done = true
				case "install":
					// Source uses the validated config and then installs an abort listener.
					// The synthetic signal is already aborted, so it removes without add/connect.
					trace = append(trace, wsObject("kind", "onDispose"), wsObject("kind", "remove"), wsObject("kind", "stopped"))
					code = "plugin_disabled"
					done = true
					got, _ := jsjson.Stringify(get(decision, "config"))
					want, _ := jsjson.Stringify(get(row, "parsed"))
					if got != want {
						t.Fatalf("parsed config %s != %s", got, want)
					}
				default:
					t.Fatalf("unknown continuation %v", kind)
				}
				if done {
					break
				}
				if (kind == "secret_get" || kind == "secret_apply") && key == get(scenario, "fail") {
					_ = input.Set("failed", true)
					_ = input.Set("errorIsError", true)
					_ = input.Set("errorMessage", get(scenario, "error"))
				}
			}
			if !done {
				t.Fatal("continuation did not settle")
			}
			if !reflect.DeepEqual(trace, get(row, "trace")) {
				a, _ := jsjson.Stringify(trace)
				b, _ := jsjson.Stringify(get(row, "trace"))
				t.Fatalf("trace %s != %s", a, b)
			}
			if (prepare || validate) && get(row, "error") == nil {
				a, _ := jsjson.Stringify(result)
				b, _ := jsjson.Stringify(get(row, "result"))
				if a != b {
					t.Fatalf("config %s != %s", a, b)
				}
			}
			expected, _ := get(row, "error").(*jsjson.Object)
			if code != get(expected, "code") {
				t.Fatalf("error %v != %v", code, get(expected, "code"))
			}
		})
	}
}
