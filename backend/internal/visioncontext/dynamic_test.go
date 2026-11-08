package visioncontext

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
)

func TestActualSourceDynamicConfigurationAcrossAwaits(t *testing.T) {
	raw, e := os.ReadFile("testdata/dynamic.json")
	if e != nil {
		t.Fatal(e)
	}
	var fixture struct {
		PNG, FixedTime string
		Cases          []struct {
			Name                                                  string
			ModelReads, TimezoneReads, Reads, Recognitions, Calls int
			ModelValues, CacheKeys                                []string
			Prompts                                               []json.RawMessage
			Error                                                 string
			Result                                                json.RawMessage
		}
	}
	if e = json.Unmarshal(raw, &fixture); e != nil {
		t.Fatal(e)
	}
	bytes, e := base64.StdEncoding.DecodeString(fixture.PNG)
	if e != nil {
		t.Fatal(e)
	}
	now, e := time.Parse(time.RFC3339Nano, fixture.FixedTime)
	if e != nil {
		t.Fatal(e)
	}
	for _, f := range fixture.Cases {
		t.Run(f.Name, func(t *testing.T) {
			makeModels := func(configured, route bool) *jsjson.Object {
				routes := `[{"role":"vision","orderedModelIds":["m"]}]`
				if !route {
					routes = `[]`
				}
				v, e := jsjson.Parse(fmt.Sprintf(`{"providers":[{"id":"p","credentialConfigured":%t}],"routes":%s}`, configured, routes))
				if e != nil {
					t.Fatal(e)
				}
				return v.(*jsjson.Object)
			}
			models, zone := makeModels(false, true), "UTC"
			modelReads, timezoneReads, reads, recognitions, calls := 0, 0, 0, 0, 0
			modelValues := []string{}
			prompts := []json.RawMessage{}
			cfg := Config{SceneID: "s", Mode: "real", Now: func() time.Time { return now }, ReadModels: func() (*jsjson.Object, error) {
				modelReads++
				if f.Name == fmt.Sprintf("models-error-%d", modelReads) {
					return nil, errors.New("synthetic_models_read")
				}
				text, e := jsjson.Stringify(models)
				if e != nil {
					return nil, e
				}
				modelValues = append(modelValues, text)
				return models, nil
			}, ReadTimezone: func() (string, error) {
				timezoneReads++
				if f.Name == "timezone-error" {
					return "", errors.New("synthetic_timezone_read")
				}
				return zone, nil
			}}
			ports := Ports{Recalled: func(string, string) (bool, error) { return false, nil }, SourceAvailable: func(Candidate) (bool, error) { return true, nil }, ReadImage: func(context.Context, Candidate) ([]byte, error) {
				reads++
				if f.Name == "read-route-off" {
					models = makeModels(false, false)
				}
				if f.Name == "read-credential" {
					models = makeModels(true, true)
				}
				return bytes, nil
			}, ImageDataURL: func(context.Context, []byte) (string, error) {
				if f.Name == "decode-change" {
					models = makeModels(true, true)
					zone = "Europe/Paris"
				}
				return "data:image/png;base64," + fixture.PNG, nil
			}, Complete: func(_ context.Context, messages []ChatMessage) (string, error) {
				calls++
				prompts = append(prompts, json.RawMessage(messages[1].Content))
				if f.Name == "complete-change" {
					models = makeModels(true, true)
				}
				return "synthetic description", nil
			}, AddDependency: func(string) {}, AddSourceValidation: func(Candidate) {}}
			lib := &Library{Lookup: func(Candidate) (string, bool, error) { return "", false, nil }, CanRecognize: func(Candidate) (bool, error) { return true, nil }, Recognize: func(context.Context, []byte, Candidate) (string, bool, error) {
				recognitions++
				if f.Name == "recognize-route-off" {
					models = makeModels(false, false)
				}
				if f.Name == "recognize-timezone" {
					zone = "Asia/Tokyo"
				}
				return "", false, nil
			}}
			count := 1
			if f.Name == "complete-change" {
				count = 2
			}
			messages := []*Message{}
			for i := 0; i < count; i++ {
				messages = append(messages, &Message{ID: fmt.Sprintf("m%d", i), SceneID: "s", Status: "available", SentAt: "2026-01-01T00:00:00.000Z", Parts: []Part{{Type: "media", MediaType: "image", AssetID: fmt.Sprintf("a%d", i)}}})
			}
			cache := &Cache{Now: func() time.Time { return now }}
			result, e := Describe(context.Background(), &Turn{}, cfg, messages, cache, ports, lib)
			if f.Error != "" {
				if e == nil || e.Error() != f.Error {
					t.Fatalf("error %v source %s", e, f.Error)
				}
			} else {
				if e != nil {
					t.Fatal(e)
				}
				got, e := json.Marshal(result)
				if e != nil {
					t.Fatal(e)
				}
				var a, b any
				json.Unmarshal(got, &a)
				json.Unmarshal(f.Result, &b)
				if !reflect.DeepEqual(a, b) {
					t.Fatalf("result %s source %s", got, f.Result)
				}
			}
			if modelReads != f.ModelReads || timezoneReads != f.TimezoneReads || reads != f.Reads || recognitions != f.Recognitions || calls != f.Calls {
				t.Fatalf("getter/await counts %v source %v", []int{modelReads, timezoneReads, reads, recognitions, calls}, []int{f.ModelReads, f.TimezoneReads, f.Reads, f.Recognitions, f.Calls})
			}
			if !reflect.DeepEqual(modelValues, f.ModelValues) {
				t.Fatalf("read snapshots %v source %v", modelValues, f.ModelValues)
			}
			keys := []string{}
			for _, entry := range cache.entries {
				keys = append(keys, entry.key)
			}
			if !reflect.DeepEqual(keys, f.CacheKeys) {
				t.Fatalf("cache keys %v source %v", keys, f.CacheKeys)
			}
			if len(prompts) != len(f.Prompts) {
				t.Fatal(prompts, f.Prompts)
			}
			for i, prompt := range prompts {
				var a, b any
				json.Unmarshal(prompt, &a)
				json.Unmarshal(f.Prompts[i], &b)
				if !reflect.DeepEqual(a, b) {
					t.Fatalf("prompt %s source %s", prompt, f.Prompts[i])
				}
			}
		})
	}
}
