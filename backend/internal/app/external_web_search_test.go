//go:build linux && sqlite_fts5 && humanengine_icu

package app

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/killerprojecte/HumanEngine/backend/internal/installation"
	"github.com/killerprojecte/HumanEngine/backend/internal/toolresults"
)

func TestInstalledStructuredSearchAndByteRange(t *testing.T) {
	root := t.TempDir()
	pkg, mode := os.Getenv("HE_SEARCH_PACKAGE"), os.Getenv("HE_SEARCH_MODE")
	if pkg == "" {
		t.Skip("explicit independent web search package required")
	}
	switch mode {
	case "private":
	case "fixture":
		addrs, err := (&net.Resolver{PreferGo: true}).LookupHost(t.Context(), "api.tavily.com")
		if err != nil || !slices.Equal(addrs, []string{"192.0.1.2"}) {
			t.Fatal("test requires its isolated TLS fixture host", err)
		}
	default:
		t.Fatal("missing explicit search fixture mode")
	}
	const id = "web-search"
	const secret = "synthetic-tavily-key"
	const tail = "搜索缓存里的原始字节"
	const prefix = `{"results":[{"title":"Fixture result","url":"https://example.com/doc","content":"Fixture snippet"}],"payload":"`
	offset := len(prefix) + 9000
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Minute)
	defer cancel()
	a, err := Open(ctx, Options{
		Installation: installation.Options{
			Database: filepath.Join(root, "live/main.sqlite"), AuthorityDirectory: filepath.Join(root, "authority"),
			VectorExtension: filepath.Join(os.Getenv("HE_SOURCE_ROOT"), "node_modules/sqlite-vec-linux-x64/vec0.so"), SourceTimezone: "UTC",
		},
		PluginRoot: pkg, ImageLibrary: os.Getenv("HE_IMAGE_LIBRARY"), Node: "node", AdminToken: "synthetic-token", Locale: "en-US",
		Entries: []string{"web-search.v2.json"}, Builtins: []string{},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := a.Shutdown(context.Background()); err != nil {
			t.Error(err)
		}
	}()
	catalog := appRequest(t, a, http.MethodGet, "/api/plugins", nil).([]any)
	if len(catalog) != 1 || field(catalog[0], "id") != id || field(catalog[0], "builtin") != false || field(catalog[0], "enabled") != false {
		t.Fatal("not an independent default-disabled package", catalog)
	}
	if field(field(catalog[0], "configuredSecrets"), "tavilyKey") != false {
		t.Fatal("fixture starts without a key")
	}
	appRequest(t, a, http.MethodPut, "/api/plugins/web-search/config", map[string]any{"provider": "tavily", "tavilyKey": secret})
	config := appRequest(t, a, http.MethodGet, "/api/plugins/web-search/config", nil)
	catalog = appRequest(t, a, http.MethodGet, "/api/plugins", nil).([]any)
	if field(config, "tavilyKey") != nil || field(field(catalog[0], "configuredSecrets"), "tavilyKey") != true {
		t.Fatal("key did not remain write-only")
	}
	db, err := sql.Open("sqlite3", a.options.Installation.Database)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var ciphertext, stored string
	if err := db.QueryRowContext(ctx, `SELECT ciphertext FROM secrets WHERE namespace='plugin:web-search' AND name='tavilyKey'`).Scan(&ciphertext); err != nil ||
		ciphertext == "" || strings.Contains(ciphertext, secret) {
		t.Fatal("key not encrypted in plugin namespace", err)
	}
	if err := db.QueryRowContext(ctx, `SELECT value FROM config WHERE key='plugin:web-search'`).Scan(&stored); err != nil || strings.Contains(stored, secret) {
		t.Fatal("ordinary configuration stored a key", err)
	}
	appRequest(t, a, http.MethodPut, "/api/plugins/web-search/config", map[string]any{"tavilyKey": "", "maxResults": 2})
	appRequest(t, a, http.MethodPut, "/api/plugins/web-search/enabled", map[string]any{"enabled": true})
	if !a.WASM.Loaded(id) || a.Bridge.Host.PID() != 0 {
		t.Fatal("expected actual WASM without Node")
	}

	const scene = "external-search-fixture"
	appRequest(t, a, http.MethodPut, "/api/scenes/"+scene, map[string]any{
		"id": scene, "kind": "group", "adapterId": "synthetic", "accountId": "bot", "platformSceneId": scene,
		"name": "Fixture", "participationEnabled": true, "learningEnabled": false, "proactiveEnabled": false,
	})
	protected := mode == "private"
	observed := make(chan string, 4)
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Messages []struct{ Role, Content string }
			Tools    []struct{ Function struct{ Name string } }
		}
		if r.URL.Path != "/v1/chat/completions" || json.NewDecoder(r.Body).Decode(&req) != nil {
			http.Error(w, "invalid model fixture", 400)
			return
		}
		var tools []string
		for _, item := range req.Tools {
			tools = append(tools, item.Function.Name)
		}
		var contract struct {
			PlanContract struct{ TargetMessageIDs, ContextMessageIDs []string }
		}
		var search, cached struct {
			ToolName string
			Value    json.RawMessage
		}
		for _, msg := range req.Messages {
			if msg.Role == "user" {
				_ = json.Unmarshal([]byte(msg.Content), &contract)
			}
			if msg.Role != "tool" {
				continue
			}
			var receipt struct {
				ToolName string
				Value    json.RawMessage
			}
			if err := json.Unmarshal([]byte(msg.Content), &receipt); err == nil {
				switch receipt.ToolName {
				case "web-search.search":
					search = receipt
				case "tool-results.read":
					cached = receipt
				}
			}
		}
		name, args := "", ""
		switch {
		case slices.Contains(tools, "participation__decide"):
			name, args = "participation__decide", `{"action":"reply","reasonCode":"fixture","targetMessageIds":["current"]}`
		case !slices.Contains(tools, "web-search__search") || slices.Contains(tools, "web-read__read"):
			http.Error(w, "wrong external tool menu", 400)
			return
		case len(search.Value) == 0:
			name, args = "web-search__search", `{"query":"  Go 搜索  ","limit":3}`
		default:
			var result struct {
				Status, Trust, BodyProvider string
				AttemptedProviders          []string
				Results                     []struct{ Provider, Title, URL, Snippet string }
				Body                        toolresults.Result
			}
			if json.Unmarshal(search.Value, &result) != nil || strings.Contains(string(search.Value), secret) ||
				!slices.Equal(result.AttemptedProviders, []string{"tavily"}) || result.Trust != "untrusted_web_content" {
				http.Error(w, "invalid search result", 400)
				return
			}
			if protected {
				if result.Status != "unavailable" || result.Body.ResultID != "" || len(result.Results) != 0 {
					http.Error(w, "private origin not refused", 400)
					return
				}
				observed <- "private-search-origin-not-fetched"
			} else {
				if result.Status != "ok" || result.BodyProvider != "tavily" || len(result.Results) != 1 ||
					result.Results[0].Provider != "tavily" || result.Results[0].Title != "Fixture result" ||
					result.Results[0].URL != "https://example.com/doc" || result.Results[0].Snippet != "Fixture snippet" ||
					result.Body.ResultID == "" || result.Body.TotalBytes <= int64(offset) || strings.Contains(result.Body.Content, tail) {
					http.Error(w, "wrong search result or body reference", 400)
					return
				}
				if len(cached.Value) == 0 {
					name, args = "tool-results__read", fmt.Sprintf(`{"resultId":%q,"offset":%d,"length":%d,"encoding":"utf8"}`, result.Body.ResultID, offset, len(tail))
					break
				}
				var retrieved toolresults.Result
				if json.Unmarshal(cached.Value, &retrieved) != nil || retrieved.Content != tail ||
					retrieved.ResultID != result.Body.ResultID || retrieved.ReturnedRange.Offset != int64(offset) ||
					retrieved.ReturnedRange.Length != int64(len(tail)) {
					http.Error(w, "wrong cached UTF-8 range", 400)
					return
				}
				observed <- "structured-search-and-range"
			}
			payload, _ := json.Marshal(map[string]any{
				"action": "noReply", "targetMessageIds": contract.PlanContract.TargetMessageIDs, "contextMessageIds": contract.PlanContract.ContextMessageIDs,
				"strategy": "Use the cited search result.", "constraints": []any{}, "facts": []any{}, "assets": []any{}, "emoji": []any{}, "expressionGoals": []any{}, "actions": []any{},
			})
			name, args = "reply__plan", string(payload)
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"choices":[{"finish_reason":"tool_calls","message":{"role":"assistant","tool_calls":[{"id":%q,"type":"function","function":{"name":%q,"arguments":%q}}]}}]}`, name, name, args)
	}))
	defer api.Close()
	models := fmt.Sprintf(`{"providers":[{"id":"fixture","name":"Loopback","protocol":"openai-chat","baseUrl":%q,"retryCount":0,"timeoutMs":5000}],"models":[{"id":"fixture","displayName":"Planner","providerId":"fixture","apiModelName":"planner","capabilities":["text","tools"]}],"routes":[{"role":"planner","orderedModelIds":["fixture"],"timeoutMs":8000}]}`, api.URL+"/v1")
	appRequest(t, a, http.MethodPut, "/api/models", json.RawMessage(models))
	now := time.Now().UTC().Format(time.RFC3339Nano)
	topic, err := a.Engine.Runtime.Ingest(ctx, scene, memoryToolObject("id", "current", "sceneId", scene,
		"senderId", "person", "origin", "live", "status", "available", "sentAt", now, "receivedAt", now,
		"parts", []any{memoryToolObject("type", "text", "text", "Search for the public manual.")}), false)
	if err != nil {
		t.Fatal(err)
	}
	handle, err := a.Engine.Runtime.Run(scene, topic, false)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-handle.Done:
	case <-ctx.Done():
		t.Fatal("planner did not finish", ctx.Err())
	}
	out := handle.Wait()
	if out.Err != nil || out.Reason != "planner_no_reply" || len(out.Receipts) != 0 {
		t.Fatalf("unexpected turn %+v", out)
	}
	want := "structured-search-and-range"
	if protected {
		want = "private-search-origin-not-fetched"
	}
	select {
	case got := <-observed:
		if got != want {
			t.Fatal(got, want)
		}
		t.Log(got)
	default:
		t.Fatal("Planner did not receive the tool evidence")
	}
	appRequest(t, a, http.MethodPut, "/api/plugins/web-search/enabled", map[string]any{"enabled": false})
	if a.WASM.Loaded(id) {
		t.Fatal("retired guest remains loaded")
	}
	appRequest(t, a, http.MethodPut, "/api/plugins/web-search/config", map[string]any{"clearTavilyKey": true})
	catalog = appRequest(t, a, http.MethodGet, "/api/plugins", nil).([]any)
	if field(field(catalog[0], "configuredSecrets"), "tavilyKey") != false {
		t.Fatal("disabled plugin could not clear its credential")
	}
}

func TestStructuredSearchPreservesTypeScriptRollback(t *testing.T) {
	pkg := os.Getenv("HE_SEARCH_PACKAGE")
	if pkg == "" {
		t.Skip("explicit independent search package required")
	}
	for _, tc := range []struct {
		scenario, provider, fallback string
		consent, active              bool
	}{
		{"tavily", "tavily", "none", false, true},
		{"bing", "bing", "tavily", true, false},
		{"implicit", "", "", true, false},
		{"fallback", "tavily", "bing", true, false},
	} {
		t.Run(tc.scenario, func(t *testing.T) {
			root := t.TempDir()
			ctx, cancel := context.WithTimeout(t.Context(), 3*time.Minute)
			defer cancel()
			source := func(phase string) {
				t.Helper()
				cmd := exec.CommandContext(ctx, "node", "--import", "tsx", "testdata/web-search-rollback.mjs", phase, root, tc.scenario)
				out, err := cmd.CombinedOutput()
				marker := "search-rollback-source-" + phase + "-" + tc.scenario
				if err != nil || !strings.Contains(string(out), marker) {
					t.Fatalf("TypeScript %s failed: %v\n%s", phase, err, out)
				}
			}
			source("seed")
			a, err := Open(ctx, Options{
				Installation: installation.Options{
					Database: filepath.Join(root, "live/main.sqlite"), AuthorityDirectory: filepath.Join(root, "authority"),
					VectorExtension: filepath.Join(os.Getenv("HE_SOURCE_ROOT"), "node_modules/sqlite-vec-linux-x64/vec0.so"), SourceTimezone: "UTC",
				},
				PluginRoot: pkg, ImageLibrary: os.Getenv("HE_IMAGE_LIBRARY"), Node: "node", AdminToken: "synthetic-token", Locale: "en-US",
				Entries: []string{"web-search.v2.json"}, Builtins: []string{},
			})
			if err != nil {
				t.Fatal(err)
			}
			defer a.Shutdown(context.Background())
			catalog := appRequest(t, a, http.MethodGet, "/api/plugins", nil).([]any)
			config := appRequest(t, a, http.MethodGet, "/api/plugins/web-search/config", nil)
			provider, _ := field(config, "provider").(string)
			fallback, _ := field(config, "fallbackProvider").(string)
			item := catalog[0]
			if provider != tc.provider || fallback != tc.fallback || field(config, "allowExperimentalHtml") != tc.consent ||
				field(config, "maxResults") != float64(3) || field(config, "timeoutMs") != float64(8500) ||
				field(config, "maxResponseBytes") != float64(16384) || field(config, "maxConcurrency") != float64(1) ||
				field(config, "tavilyKey") != nil || field(field(item, "configuredSecrets"), "tavilyKey") != true ||
				field(item, "enabled") != tc.active || a.WASM.Loaded("web-search") != tc.active || a.Bridge.Host.PID() != 0 {
				t.Fatal("saved settings, credential presence or active route changed")
			}
			if !tc.active && field(item, "error") != "plugin_profile_unsupported" {
				t.Fatal("missing explicit unsupported route result", field(item, "error"))
			}
			if err := a.Shutdown(context.Background()); err != nil {
				t.Fatal(err)
			}
			source("rollback")
			t.Log("ts-go-ts-search-settings-preserved:", tc.scenario)
		})
	}
}
