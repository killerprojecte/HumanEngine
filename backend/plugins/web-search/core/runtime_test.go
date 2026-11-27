package websearch_test

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/killerprojecte/HumanEngine/backend/internal/plugins"
)

func TestStructuredSearchGuestMatchesTypeScript(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "web-search.wasm")
	build := exec.CommandContext(ctx, "go", "build", "-buildvcs=false", "-buildmode=c-shared", "-o", path, "../wasm")
	build.Env = append(os.Environ(), "GOOS=wasip1", "GOARCH=wasm")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build guest: %v %s", err, out)
	}
	artifact, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	host, err := plugins.NewWASMHost(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer host.Close(ctx)
	if err = host.Enable(ctx, "web-search", artifact); err != nil {
		t.Fatal(err)
	}
	call := func(t *testing.T, payload any) any {
		t.Helper()
		raw, err := json.Marshal(payload)
		if err != nil {
			t.Fatal(err)
		}
		result, err := host.Call(ctx, "web-search", raw)
		if err != nil {
			t.Fatal(err)
		}
		return decode(t, result)
	}
	var fixture struct {
		Cases []struct {
			Name, Query, RawBody            string
			Limit                           *int
			QueryRepeat, MaxResults, Repeat int
			HTTPStatus                      int
		}
		Expected []struct {
			Name, Error    string
			Plan, Response json.RawMessage
		}
	}
	data, err := os.ReadFile("testdata/node-runtime.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &fixture); err != nil {
		t.Fatal(err)
	}
	for i, input := range fixture.Cases {
		want := fixture.Expected[i]
		t.Run(input.Name, func(t *testing.T) {
			if want.Name != input.Name {
				t.Fatalf("fixture misordered: %s", want.Name)
			}
			query := strings.Repeat(input.Query, max(1, input.QueryRepeat))
			plan := call(t, map[string]any{"operation": "search_plan", "provider": "tavily", "maxResults": input.MaxResults,
				"input": map[string]any{"query": query, "limit": input.Limit}})
			if want.Error != "" {
				if !reflect.DeepEqual(plan, map[string]any{"error": want.Error}) {
					t.Fatalf("plan rejection: %v, want %s", plan, want.Error)
				}
				return
			}
			if expected := decode(t, want.Plan); !reflect.DeepEqual(plan, expected) {
				t.Fatalf("plan = %v, want %v", plan, expected)
			}
			observation := map[string]any{"provider": "tavily", "limit": plan.(map[string]any)["limit"], "attemptedProviders": []string{"tavily"}}
			if input.HTTPStatus == 200 {
				observation["body"] = map[string]string{"resultId": "fixture-result"}
				observation["parserBudgetBytes"] = 4096
				observation["httpStatus"] = input.HTTPStatus
				if input.Repeat > 4096 {
					observation["parseLimited"] = true
				} else {
					observation["text"] = input.RawBody
				}
			}
			response := call(t, map[string]any{"operation": "search_response", "observation": observation})
			if expected := decode(t, want.Response); !reflect.DeepEqual(response, expected) {
				t.Fatalf("response = %v, want %v", response, expected)
			}
		})
	}
	if err := host.Disable(ctx, "web-search"); err != nil || host.Loaded("web-search") {
		t.Fatalf("unload: %v", err)
	}
}

func decode(t *testing.T, raw []byte) any {
	t.Helper()
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		t.Fatal(err)
	}
	return value
}
