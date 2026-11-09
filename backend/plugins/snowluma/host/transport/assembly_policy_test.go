package snowlumatransport

import (
	"context"
	"crypto/sha256"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/plugins"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func sourceValue(o *jsjson.Object, key string) any {
	if o == nil {
		return nil
	}
	v, _ := o.Get(key)
	return v
}
func sourceSame(a, b any) bool {
	if x, ok := a.(*jsjson.Object); ok {
		y, ok := b.(*jsjson.Object)
		if !ok || x.Len() != y.Len() {
			return false
		}
		for _, k := range x.Keys() {
			v, exists := y.Get(k)
			if !exists || !sourceSame(sourceValue(x, k), v) {
				return false
			}
		}
		return true
	}
	if x, ok := a.([]any); ok {
		y, ok := b.([]any)
		if !ok || len(x) != len(y) {
			return false
		}
		for i := range x {
			if !sourceSame(x[i], y[i]) {
				return false
			}
		}
		return true
	}
	return a == b
}
func sourceAssert(t *testing.T, label string, a, b any) {
	t.Helper()
	if !sourceSame(a, b) {
		aa, _ := jsjson.Stringify(a)
		bb, _ := jsjson.Stringify(b)
		t.Fatalf("%s: %s != %s", label, aa, bb)
	}
}
func TestAssemblyGuestSourcePolicies(t *testing.T) {
	var err error
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

	invoke := func(t *testing.T, input *jsjson.Object) *jsjson.Object {
		t.Helper()
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
		return value.(*jsjson.Object)
	}
	load := func(t *testing.T, name string) any {
		raw, e := os.ReadFile("../../core/testdata/" + name)
		if e != nil {
			t.Fatal(e)
		}
		value, e := jsjson.ParseBytes(raw)
		if e != nil {
			t.Fatal(e)
		}
		return value
	}
	t.Run("configured-secrets", func(t *testing.T) {
		for _, v := range load(t, "configured-secrets-source.json").([]any) {
			row := v.(*jsjson.Object)
			input := wsObject("operation", "configuration_secrets")
			trace := []any{}
			done := false
			for i := 0; i < 3; i++ {
				result := invoke(t, input)
				if sourceValue(result, "kind") == "configured_secrets" {
					sourceAssert(t, "secret flags", sourceValue(result, "secrets"), sourceValue(row, "result"))
					done = true
					break
				}
				key := sourceValue(result, "key").(string)
				trace = append(trace, wsObject("scope", "plugin:snowluma", "key", key))
				secrets := sourceValue(row, "secrets").(*jsjson.Object)
				input = wsObject("operation", "configuration_secret_present", "state", sourceValue(result, "state"), "present", sourceValue(secrets, key) == true)
			}
			if !done {
				t.Fatal("secrets did not settle")
			}
			sourceAssert(t, "secret order", trace, sourceValue(row, "trace"))
		}
	})
	t.Run("profiles", func(t *testing.T) {
		t.Run("initial-null-state", func(t *testing.T) {
			profiles := load(t, "profiles-source.json").([]any)
			first := sourceValue(profiles[0].(*jsjson.Object), "steps").([]any)[0].(*jsjson.Object)
			observed := sourceValue(first, "input").(*jsjson.Object)
			input := wsObject("operation", "profiles_observe", "now", float64(0), "message", sourceValue(observed, "message"), "scene", sourceValue(observed, "scene"))
			absent := invoke(t, input)
			_ = input.Set("state", nil)
			explicit := invoke(t, input)
			sourceAssert(t, "profile absent/null initial state", absent, explicit)
			event := wsObject("self_id", float64(42), "post_type", "message", "message_type", "group", "group_id", float64(99), "user_id", float64(7), "message_id", float64(1), "time", float64(1700000000), "message", []any{})
			input = wsObject("operation", "ingress_begin", "adapter", "snowluma", "account", "42", "event", event, "receivedAt", "2026-01-01T00:00:00.000Z")
			absent = invoke(t, input)
			_ = input.Set("state", nil)
			explicit = invoke(t, input)
			// The continuation deliberately retains its original request envelope.
			// Compare the decision/state, then execute both native-style resumptions.
			sourceAssert(t, "ingress owner lookup", sourceValue(absent, "kind"), "is_recalled")
			for _, key := range []string{"kind", "messageId", "sceneId", "state"} {
				sourceAssert(t, "ingress absent/null "+key, sourceValue(absent, key), sourceValue(explicit, key))
			}
			absentRequest := sourceValue(absent, "request").(*jsjson.Object)
			explicitRequest := sourceValue(explicit, "request").(*jsjson.Object)
			if _, exists := absentRequest.Get("state"); exists {
				t.Fatal("absent state was not preserved in request envelope")
			}
			if value, exists := explicitRequest.Get("state"); !exists || value != nil {
				t.Fatal("explicit null state was not preserved in request envelope")
			}
			resume := func(decision *jsjson.Object, recalled bool) *jsjson.Object {
				request := sourceValue(decision, "request").(*jsjson.Object).Clone()
				_ = request.Set("operation", "ingress_recalled")
				_ = request.Set("recalled", recalled)
				_ = request.Set("state", sourceValue(decision, "state"))
				return invoke(t, request)
			}
			for _, recalled := range []bool{false, true} {
				sourceAssert(t, "ingress absent/null resumed", resume(absent, recalled), resume(explicit, recalled))
			}
			input = wsObject("operation", "recalled_lookup", "sceneId", "synthetic-scene", "messageId", "synthetic-message")
			absent = invoke(t, input)
			_ = input.Set("state", nil)
			explicit = invoke(t, input)
			sourceAssert(t, "recall absent/null initial state", absent, explicit)
		})

		for _, v := range load(t, "profiles-source.json").([]any) {
			row := v.(*jsjson.Object)
			t.Run(sourceValue(row, "name").(string), func(t *testing.T) {
				var state any
				trace := []any{}
				jobs := []*jsjson.Object{}
				for _, v := range sourceValue(row, "steps").([]any) {
					entry := v.(*jsjson.Object)
					step := sourceValue(entry, "step").(*jsjson.Object)
					operation := sourceValue(step, "op")
					input := wsObject("state", state, "now", sourceValue(entry, "now"))
					var observed *jsjson.Object
					switch operation {
					case "clock":
						continue
					case "observe":
						observed = sourceValue(entry, "input").(*jsjson.Object)
						_ = input.Set("operation", "profiles_observe")
						_ = input.Set("message", sourceValue(observed, "message"))
						_ = input.Set("scene", sourceValue(observed, "scene"))
					case "complete":
						index := int(sourceValue(step, "index").(float64))
						_ = input.Set("operation", "profiles_complete")
						_ = input.Set("job", jobs[index])
						_ = input.Set("failed", sourceValue(step, "failed") == true)
						if value, exists := step.Get("value"); exists {
							_ = input.Set("value", value)
						}
					case "dispose":
						_ = input.Set("operation", "profiles_dispose")
					}
					result := invoke(t, input)
					state = sourceValue(result, "state").(*jsjson.Object)
					if operation == "observe" {
						expected := sourceValue(entry, "result").(*jsjson.Object)
						sourceAssert(t, "observed message", sourceValue(result, "message"), sourceValue(expected, "message"))
						sourceAssert(t, "observed scene", sourceValue(result, "scene"), sourceValue(expected, "scene"))
					}
					if queue, ok := sourceValue(result, "jobs").([]any); ok {
						for _, j := range queue {
							job := j.(*jsjson.Object)
							trace = append(trace, wsObject("kind", "action", "index", float64(len(jobs)), "action", sourceValue(job, "action"), "params", sourceValue(job, "params")))
							jobs = append(jobs, job)
						}
					}
					if sourceValue(result, "failure") == true {
						trace = append(trace, wsObject("kind", "failure"))
					}
					if profile, exists := result.Get("profile"); exists {
						trace = append(trace, wsObject("kind", "profile", "profile", profile))
					}
					sourceAssert(t, "profile source trace", trace, sourceValue(entry, "trace"))
				}
			})
		}
	})
	t.Run("history", func(t *testing.T) {
		fixture := load(t, "history-source.json").(*jsjson.Object)
		for _, v := range sourceValue(fixture, "cases").([]any) {
			row := v.(*jsjson.Object)
			t.Run(sourceValue(row, "name").(string), func(t *testing.T) {
				trace, requests := []any{}, []any{}
				scenario := sourceValue(row, "scenario").(*jsjson.Object)
				input := wsObject("operation", "history_begin", "adapter", "snowluma", "account", "42", "scene", sourceValue(row, "scene"), "input", sourceValue(row, "input"))
				if sourceValue(row, "kind") == "message" {
					_ = input.Set("operation", "message_begin")
					_ = input.Set("messageId", sourceValue(row, "messageId"))
				}
				recalled := func(scene, id any) bool {
					trace = append(trace, wsObject("kind", "recalled", "sceneId", scene, "id", id))
					for _, v := range sourceValue(row, "recalledMessageIds").([]any) {
						if v == id {
							return true
						}
					}
					return false
				}
				done := false
				for step := 0; step < 1000; step++ {
					result := invoke(t, input)
					state := sourceValue(result, "state")
					switch sourceValue(result, "kind") {
					case "history":
						sourceAssert(t, "history result", sourceValue(result, "result"), sourceValue(row, "result"))
						done = true
					case "is_recalled":
						value := recalled(sourceValue(result, "sceneId"), sourceValue(result, "messageId"))
						input = wsObject("operation", "message_recalled", "state", state, "recalled", value)
					case "action":
						requests = append(requests, wsObject("action", sourceValue(result, "action"), "params", sourceValue(result, "params")))
						input = wsObject("operation", "history_response", "state", state, "value", sourceValue(scenario, "data"))
						if sourceValue(scenario, "failed") == true {
							_ = input.Set("failed", true)
							_ = input.Set("oneBot", true)
							_ = input.Set("code", "unsupported")
						}
					case "normalize":
						normalize := wsObject("operation", "identify_message", "adapter", "snowluma", "account", "42", "origin", "history", "receivedAt", "2026-01-01T00:00:00.000Z", "event", sourceValue(result, "event"), "expected", sourceValue(result, "scene"))
						id := invoke(t, normalize)
						input = wsObject("operation", "history_normalized", "state", state)
						if code, exists := id.Get("error"); exists {
							_ = input.Set("failed", true)
							_ = input.Set("oneBot", sourceValue(id, "oneBot"))
							_ = input.Set("code", code)
							break
						}
						sc := sourceValue(id, "scene").(*jsjson.Object)
						recall := recalled(sourceValue(sc, "id"), sourceValue(id, "messageId"))
						_ = normalize.Set("operation", "normalize")
						_ = normalize.Set("recalled", recall)
						normalized := invoke(t, normalize)
						if code, exists := normalized.Get("error"); exists {
							_ = input.Set("failed", true)
							_ = input.Set("oneBot", sourceValue(normalized, "oneBot"))
							_ = input.Set("code", code)
						} else {
							_ = input.Set("message", sourceValue(normalized, "message"))
						}
					case "compare":
						var order any
						found := false
						for _, v := range sourceValue(fixture, "comparisons").([]any) {
							pair := v.([]any)
							if pair[0] == sourceValue(result, "left") && pair[1] == sourceValue(result, "right") {
								order = pair[2]
								found = true
								break
							}
						}
						if !found {
							t.Fatal("comparison not from actual source")
						}
						input = wsObject("operation", "history_sorted", "state", state, "comparison", order)
					default:
						t.Fatalf("unknown history continuation %v", sourceValue(result, "kind"))
					}
					if done {
						break
					}
				}
				if !done {
					t.Fatal("history did not settle")
				}
				sourceAssert(t, "history recall order", trace, sourceValue(row, "trace"))
				sourceAssert(t, "history wire requests", requests, sourceValue(row, "requests"))
			})
		}
	})
}
