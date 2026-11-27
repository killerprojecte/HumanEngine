package pluginmanager

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/killerprojecte/HumanEngine/backend/internal/toolresults"
	websearch "github.com/killerprojecte/HumanEngine/backend/plugins/web-search/core"
)

func TestStructuredFallbackOutcomeAndProvenance(t *testing.T) {
	const empty = `{"results":{"web":[]}}`
	const hit = `{"results":[{"title":"Fallback","url":"https://example.com/doc","content":"Synthetic"}]}`
	for _, scenario := range []struct {
		name, primary, secondary, status, origin       string
		calls, receipts                                int
		cacheFailure, unavailable, deadline, cancelled bool
	}{
		{name: "status failure", secondary: hit, status: "ok", origin: "tavily", calls: 2, receipts: 1},
		{name: "malformed then succeeds", primary: "{", secondary: hit, status: "ok", origin: "tavily", calls: 2, receipts: 2},
		{name: "malformed then HTTP failure", primary: "{", status: "unavailable", origin: "you", calls: 2, receipts: 1},
		{name: "malformed then cache failure", primary: "{", secondary: hit, status: "unavailable", origin: "you", calls: 2, receipts: 1, cacheFailure: true},
		{name: "valid empty", primary: empty, secondary: hit, status: "no_results", origin: "you", calls: 1, receipts: 1},
		{name: "parser budget", primary: strings.Repeat("x", 4097), secondary: hit, status: "partial", origin: "you", calls: 1, receipts: 1},
		{name: "local cache failure", primary: empty, secondary: hit, status: "unavailable", calls: 1, cacheFailure: true},
		{name: "missing primary credential", secondary: hit, status: "ok", origin: "tavily", calls: 1, receipts: 1, unavailable: true},
		{name: "shared timeout", secondary: hit, status: "timeout", calls: 1, deadline: true},
		{name: "cancelled", secondary: hit, status: "cancelled", calls: 1, cancelled: true},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			parent, cancel := context.WithCancel(t.Context())
			defer cancel()
			timeout := time.Second
			if scenario.deadline {
				timeout = 100 * time.Millisecond
			}
			ctx, stop := context.WithTimeout(parent, timeout)
			defer stop()
			var order []string
			origin := ""
			provider := func(name, entity string) structuredAttempt {
				return structuredAttempt{provider: name, contentType: "application/json", request: func(got context.Context, input []byte) (*http.Response, error) {
					if got != ctx || !strings.Contains(string(input), `"query":"fixture"`) {
						t.Fatal("fallback lost its original query or deadline")
					}
					order = append(order, name)
					origin = name
					if scenario.cancelled {
						cancel()
						return nil, got.Err()
					}
					if scenario.deadline {
						<-got.Done()
						return nil, got.Err()
					}
					status := 200
					if entity == "" {
						status = 503
					}
					return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(entity))}, nil
				}}
			}
			a, b := provider("you", scenario.primary), provider("tavily", scenario.secondary)
			if scenario.unavailable {
				a.request = nil
			}
			var cached []string
			cache := func(ctx context.Context, source toolresults.Source, _ toolresults.Options) (toolresults.Result, error) {
				if scenario.cacheFailure && (scenario.primary != "{" || origin == "tavily") {
					return toolresults.Result{}, errors.New("tool_result_storage_failed")
				}
				size := 0
				for {
					chunk, err := source.Next(ctx)
					size += len(chunk)
					if errors.Is(err, io.EOF) {
						break
					}
					if err != nil {
						return toolresults.Result{}, err
					}
				}
				cached = append(cached, origin)
				return toolresults.Result{ResultID: origin + "-cache", TotalBytes: int64(size)}, nil
			}
			out, err := runStructuredSearch(ctx, []structuredAttempt{a, b}, 5, 4096, []byte(`{"query":"  fixture  "}`), cache,
				func(raw []byte) ([]byte, error) { return json.Marshal(websearch.Invoke(raw)) })
			var reply struct {
				Status, BodyProvider string
				AttemptedProviders   []string
				Body                 toolresults.Result
			}
			if err != nil || json.Unmarshal(out, &reply) != nil || reply.Status != scenario.status ||
				reply.BodyProvider != scenario.origin || len(order) != scenario.calls || len(cached) != scenario.receipts {
				t.Fatalf("wrong provider outcome %s, order=%v cache=%v err=%v", out, order, cached, err)
			}
			if scenario.origin != "" && reply.Body.ResultID != scenario.origin+"-cache" {
				t.Fatalf("cache key no longer matches provider %s", out)
			}
			if scenario.deadline || scenario.cancelled {
				return
			}
			expected := []string{"you"}
			if scenario.calls == 2 || scenario.unavailable {
				expected = append(expected, "tavily")
			}
			if !slices.Equal(expected, reply.AttemptedProviders) {
				t.Fatalf("attempts=%v, want %v", reply.AttemptedProviders, expected)
			}
		})
	}
}
