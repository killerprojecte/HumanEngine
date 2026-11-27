//go:build linux && sqlite_fts5 && humanengine_icu

package app

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/killerprojecte/HumanEngine/backend/internal/installation"
	"github.com/killerprojecte/HumanEngine/backend/internal/toolresults"
)

func TestInstalledPublicPagePreviewAndByteRange(t *testing.T) {
	root := t.TempDir()
	pkg, origin := os.Getenv("HE_PAGE_PACKAGE"), os.Getenv("HE_PAGE_ORIGIN")
	if pkg == "" {
		t.Skip("explicit independent webpage package required")
	}
	const id = "web-read"
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Minute)
	defer cancel()
	a, err := Open(ctx, Options{
		Installation: installation.Options{
			Database: filepath.Join(root, "live/main.sqlite"), AuthorityDirectory: filepath.Join(root, "authority"),
			VectorExtension: filepath.Join(os.Getenv("HE_SOURCE_ROOT"), "node_modules/sqlite-vec-linux-x64/vec0.so"), SourceTimezone: "UTC",
		},
		PluginRoot: pkg, ImageLibrary: os.Getenv("HE_IMAGE_LIBRARY"), Node: "node", AdminToken: "synthetic-token", Locale: "en-US",
		Entries: []string{"web-read.v2.json"}, Builtins: []string{},
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
	appRequest(t, a, http.MethodPut, "/api/plugins/web-read/config", map[string]any{"maxChars": 256})
	appRequest(t, a, http.MethodPut, "/api/plugins/web-read/enabled", map[string]any{"enabled": true})
	if !a.WASM.Loaded(id) || a.Bridge.Host.PID() != 0 {
		t.Fatal("expected actual WASM without Node")
	}

	runPublicPagePlanner(t, ctx, a, "external-page-fixture", origin)
	appRequest(t, a, http.MethodPut, "/api/plugins/web-read/enabled", map[string]any{"enabled": false})
	if a.WASM.Loaded(id) {
		t.Fatal("retired guest remains loaded")
	}
}

func runPublicPagePlanner(t *testing.T, ctx context.Context, a *Application, scene, origin string) {
	t.Helper()
	const heading = "Public fixture heading"
	const tail = "缓存中的准确字节"
	const prefix = "<html><head><title>Fixture title</title></head><body><h1>" + heading + "</h1><p>"
	offset := len(prefix) + 9000
	appRequest(t, a, http.MethodPut, "/api/scenes/"+scene, map[string]any{
		"id": scene, "kind": "group", "adapterId": "synthetic", "accountId": "bot", "platformSceneId": scene,
		"name": "Fixture", "participationEnabled": true, "learningEnabled": false, "proactiveEnabled": false,
	})
	if origin == "" {
		origin = "http://127.0.0.1/"
	}
	protected := origin == "http://127.0.0.1/"
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
		var page, cached struct {
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
				case "web-read.read":
					page = receipt
				case "tool-results.read":
					cached = receipt
				}
			}
		}
		name, args := "", ""
		switch {
		case slices.Contains(tools, "participation__decide"):
			name, args = "participation__decide", fmt.Sprintf(`{"action":"reply","reasonCode":"fixture","targetMessageIds":[%q]}`, scene+"-current")
		case !slices.Contains(tools, "web-read__read") || slices.Contains(tools, "web-read__request") || slices.Contains(tools, "web-read__send"):
			http.Error(w, "wrong external tool menu", 400)
			return
		case len(page.Value) == 0:
			name, args = "web-read__read", fmt.Sprintf(`{"url":%q}`, origin)
		default:
			var result struct {
				Status, Text, Title string
				Truncated           bool
				Body                struct {
					ResultID   string
					Content    *string
					TotalBytes int
					NextOffset *int
				}
			}
			if json.Unmarshal(page.Value, &result) != nil {
				http.Error(w, "invalid page", 400)
				return
			}
			if protected {
				if result.Status != "address_not_public" || result.Body.ResultID != "" {
					http.Error(w, "private page not refused", 400)
					return
				}
				observed <- "private-not-fetched"
			} else {
				if result.Status != "ok" || result.Title != "Fixture title" || !strings.Contains(result.Text, heading) ||
					strings.Contains(result.Text, tail) || len([]rune(result.Text)) != 256 || !result.Truncated ||
					result.Body.ResultID == "" || result.Body.Content != nil || result.Body.TotalBytes <= offset || result.Body.NextOffset == nil {
					http.Error(w, "wrong readable preview or body reference", 400)
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
				observed <- "preview-and-range"
			}
			payload, _ := json.Marshal(map[string]any{
				"action": "noReply", "targetMessageIds": contract.PlanContract.TargetMessageIDs, "contextMessageIds": contract.PlanContract.ContextMessageIDs,
				"strategy": "Read only.", "constraints": []any{}, "facts": []any{}, "assets": []any{}, "emoji": []any{}, "expressionGoals": []any{}, "actions": []any{},
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
	topic, err := a.Engine.Runtime.Ingest(ctx, scene, memoryToolObject("id", scene+"-current", "sceneId", scene,
		"senderId", "person", "origin", "live", "status", "available", "sentAt", now, "receivedAt", now,
		"parts", []any{memoryToolObject("type", "text", "text", "Read the public manual.")}), false)
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
	want := "preview-and-range"
	if protected {
		want = "private-not-fetched"
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
}
