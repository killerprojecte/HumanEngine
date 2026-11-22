package app

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/killerprojecte/HumanEngine/backend/internal/botruntime"
	"github.com/killerprojecte/HumanEngine/backend/internal/dispatch"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/memeengine"
	"github.com/killerprojecte/HumanEngine/backend/internal/memeservice"
)

func TestManagedDisplayImageActualSource(t *testing.T) {
	raw, err := os.ReadFile("testdata/managed-display-image-source.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Base64, PreviewURL string
		HistoryParts       map[string]map[string]any
		Cases              map[string]struct {
			Status                                                    int
			MIME, Length, Disposition, ResourcePolicy, Cache, Nosniff string
			Hash, Error                                               string
		}
	}
	if err := json.Unmarshal(raw, &fixture); err != nil {
		t.Fatal(err)
	}
	png, err := base64.StdEncoding.DecodeString(fixture.Base64)
	if err != nil {
		t.Fatal(err)
	}
	options := applicationOptions(t)
	model, err := os.ReadFile("testdata/snowluma-model.mjs")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(options.PluginRoot, "snow-model.mjs"), model, 0600); err != nil {
		t.Fatal(err)
	}
	options.Entries = []string{"snow-model.mjs"}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	app, err := Open(ctx, options)
	if err != nil {
		t.Fatal(err)
	}
	defer app.Shutdown(ctx)
	root, release, err := app.Engine.RootContext(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	runtime := app.Engine.Runtime
	sent := make(chan string, 2)
	ids := []string{"bot/message", "deleted"}
	runtime.AddAdapter("fixture", &botruntime.Adapter{History: func(_ context.Context, scene botruntime.Scene, _ int, settle func(*botruntime.Object, error)) {
		duplicate := map[string]any{"id": "bot/message", "sceneId": scene.ID, "senderId": "remote-only", "origin": "history", "status": "available", "sentAt": "2099-01-01T00:00:00.000Z", "receivedAt": "2099-01-01T00:00:00.000Z", "parts": []any{map[string]any{"type": "media", "mediaType": "image/png", "assetId": strings.Repeat("f", 64), "mediaUrl": "https://untrusted.invalid/asset"}}}
		another := make(map[string]any, len(duplicate))
		for k, v := range duplicate {
			another[k] = v
		}
		another["id"] = "remote-only"
		raw, err := json.Marshal(map[string]any{"items": []any{duplicate, another}, "coverage": "partial"})
		if err != nil {
			settle(nil, err)
			return
		}
		page, err := jsjson.ParseBytes(raw)
		if err != nil {
			settle(nil, err)
			return
		}
		settle(page.(*jsjson.Object), nil)
	}, Transport: &dispatch.Adapter{
		Send: func(scene dispatch.SceneRef, segment dispatch.Segment, _ *dispatch.Signal, before func() error) *dispatch.Future[dispatch.Receipt] {
			result := dispatch.NewFuture[dispatch.Receipt]()
			if err := before(); err != nil {
				result.Reject(err)
				return result
			}
			if scene.PlatformSceneID != "group" || len(segment.Parts) != 2 || len(ids) == 0 {
				result.Reject(errors.New("unexpected_managed_fixture_dispatch"))
				return result
			}
			id := ids[0]
			ids = ids[1:]
			result.Resolve(dispatch.Receipt{Status: dispatch.Sent, MessageID: &id})
			sent <- id
			return result
		},
	}})
	settings := appRequest(t, app, "GET", "/api/settings", nil).(*jsjson.Object)
	_ = settings.Set("promptContextWindowEnabled", false)
	_ = settings.Set("debounceMs", float64(0))
	_ = settings.Set("groupTurnCooldownMs", float64(0))
	wire, err := jsjson.Marshal(settings)
	if err != nil {
		t.Fatal(err)
	}
	appRequest(t, app, "PUT", "/api/settings", json.RawMessage(wire))
	appRequest(t, app, "PUT", "/api/plugins/snow-model/config", map[string]any{"media": true})
	appRequest(t, app, "PUT", "/api/plugins/snow-model/enabled", map[string]any{"enabled": true})
	const cms = `{"providers":[{"id":"p","name":"Synthetic","protocol":"snow-model:deterministic","baseUrl":"https://synthetic.invalid","retryCount":0,"timeoutMs":10000}],"models":[{"id":"m","displayName":"Synthetic","providerId":"p","apiModelName":"synthetic","capabilities":["text","tools"]}],"routes":[{"role":"planner","orderedModelIds":["m"],"timeoutMs":10000},{"role":"replyer","orderedModelIds":["m"],"timeoutMs":10000}]}`
	appRequest(t, app, "PUT", "/api/models", json.RawMessage(cms))
	saveScene := func(id, platformID string) {
		appRequest(t, app, "PUT", "/api/scenes/"+url.PathEscape(id), map[string]any{
			"id": id, "adapterId": "fixture", "accountId": "bot", "platformSceneId": platformID,
			"kind": "group", "name": "Media Fixture", "participationEnabled": true,
			"learningEnabled": true, "proactiveEnabled": false,
		})
	}
	const scene, foreign = "fixture:scene", "fixture:other"
	saveScene(scene, "group")
	saveScene(foreign, "other")
	ingest := func(sceneID, messageID, senderID, mediaID string, quote ...string) string {
		t.Helper()
		part := map[string]any{"type": "text", "text": "Please reply to this synthetic request."}
		if mediaID != "" {
			part = map[string]any{"type": "media", "assetId": mediaID, "mediaType": "image/png"}
			if messageID == "unproven" {
				part["mediaUrl"] = "https://untrusted.invalid/preview-image"
			}
		}
		value := map[string]any{
			"id": messageID, "sceneId": sceneID, "senderId": senderID,
			"origin": "live", "status": "available", "sentAt": "2099-01-01T00:00:00.000Z",
			"receivedAt": "2099-01-01T00:00:00.000Z", "parts": []any{part},
		}
		if len(quote) != 0 {
			value["replyToId"] = quote[0]
		}
		raw, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		message, err := jsjson.ParseBytes(raw)
		if err != nil {
			t.Fatal(err)
		}
		topic, err := runtime.Ingest(ctx, sceneID, message.(*jsjson.Object), false)
		if err != nil {
			t.Fatal(err)
		}
		return topic
	}
	sendManaged := func(sourceID string) string {
		t.Helper()
		topic := ingest(scene, sourceID, "person", "")
		handle, err := runtime.Run(scene, topic, false)
		if err != nil {
			t.Fatal(err)
		}
		select {
		case <-handle.Done:
			result := handle.Wait()
			if result.Counts["sentSegments"] != 1 {
				t.Fatalf("managed outbound receipt missing: %+v", result)
			}
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
		select {
		case id := <-sent:
			return id
		default:
			t.Fatal("real dispatch did not call the synthetic transport")
			return ""
		}
	}
	ingest(scene, "recalled-prefix", "person", "")
	if err := runtime.Recall(ctx, scene, "recalled-prefix"); err != nil {
		t.Fatal(err)
	}
	ingest(scene, "source", "person", "")
	asset := appRequest(t, app, "POST", "/api/memes/import", map[string]any{
		"sceneId": scene, "messageId": "source", "base64": fixture.Base64,
	})
	id := field(asset, "id").(string)
	patch := new(jsjson.Object)
	if err := patch.Set("tags", field(asset, "tags")); err != nil {
		t.Fatal(err)
	}
	if _, err := app.Engine.Memes.Update(root, id, patch); err != nil {
		t.Fatal(err)
	}
	assetPath := "/api/memes/" + id
	included := func(value bool) {
		appRequest(t, app, "POST", assetPath+"/inclusion", map[string]bool{"included": value})
	}
	disabled := func(value bool) { appRequest(t, app, "PATCH", assetPath, map[string]bool{"disabled": value}) }
	login := httptest.NewRequest("POST", "/api/auth/session", strings.NewReader(`{"token":"synthetic-token"}`))
	login.Header.Set("X-HumanEngine-Session", "1")
	loginResult := httptest.NewRecorder()
	app.ServeHTTP(loginResult, login)
	cookies := loginResult.Result().Cookies()
	if loginResult.Code != 200 || len(cookies) != 1 {
		t.Fatalf("browser session login: %d %s", loginResult.Code, loginResult.Body)
	}
	browserAuth := func(request *http.Request) {
		request.Header.Set("X-HumanEngine-Session", "1")
		request.Header.Set("Sec-Fetch-Site", "same-origin")
		request.AddCookie(cookies[0])
	}
	checkURL := func(name, method, path string, authorized bool) {
		t.Helper()
		t.Run(name, func(t *testing.T) {
			expected, ok := fixture.Cases[name]
			if !ok && !strings.HasPrefix(name, "historyRoundTrip/") {
				t.Fatal("missing source case", name)
			}
			request := httptest.NewRequest(method, path, nil)
			if name == "previewRoundTrip" || strings.HasPrefix(name, "historyRoundTrip/") {
				if name != "previewRoundTrip" {
					expected = fixture.Cases["previewRoundTrip"]
				}
				browserAuth(request)
			} else {
				sourceFixtureAuth(request, authorized)
			}
			response := httptest.NewRecorder()
			app.ServeHTTP(response, request)
			if response.Code != expected.Status {
				t.Fatalf("status got %d want %d: %s", response.Code, expected.Status, response.Body)
			}
			if response.Code == 200 {
				if method != "HEAD" {
					hash := sha256.Sum256(response.Body.Bytes())
					if hex.EncodeToString(hash[:]) != expected.Hash || !bytes.Equal(response.Body.Bytes(), png) {
						t.Fatal("response did not preserve original bytes")
					}
				}
				headers := response.Result().Header
				for k, want := range map[string]string{"Content-Type": expected.MIME, "Content-Length": expected.Length, "Content-Disposition": expected.Disposition, "Cross-Origin-Resource-Policy": expected.ResourcePolicy, "Cache-Control": expected.Cache, "X-Content-Type-Options": expected.Nosniff} {
					if got := headers.Get(k); got != want {
						t.Errorf("%s got %q want %q", k, got, want)
					}
				}
			} else {
				var problem struct{ Error string }
				if err := json.Unmarshal(response.Body.Bytes(), &problem); err != nil || problem.Error != expected.Error {
					t.Fatalf("error: %s, want %q", response.Body, expected.Error)
				}
			}
			if method == "HEAD" && response.Body.Len() != 0 {
				t.Fatal("HEAD wrote image bytes")
			}
		})
	}
	check := func(name, method, sceneID, messageID, index string, authorized bool) {
		t.Helper()
		path := "/api/scenes/" + url.PathEscape(sceneID) + "/messages/" + url.PathEscape(messageID) + "/media/" + index
		checkURL(name, method, path, authorized)
	}
	previewLink := func(messageID string) string {
		t.Helper()
		path := "/api/scenes/" + url.PathEscape(scene) + "/messages"
		request := httptest.NewRequest("GET", path, nil)
		browserAuth(request)
		response := httptest.NewRecorder()
		app.ServeHTTP(response, request)
		if response.Code != 200 {
			t.Fatalf("browser preview: %d %s", response.Code, response.Body)
		}
		parsed, err := jsjson.ParseBytes(response.Body.Bytes())
		if err != nil {
			t.Fatal(err)
		}
		rows := parsed.([]any)
		if len(rows) == 0 || field(rows[0], "id") != "recalled-prefix" || field(rows[0], "status") != "recalled" {
			t.Fatal("preview fixture must begin with a recall tombstone")
		}
		for _, row := range rows {
			if field(row, "id") != messageID {
				continue
			}
			parts, _ := field(row, "parts").([]any)
			for _, part := range parts {
				if field(part, "assetId") != nil {
					t.Fatal("preview exposed a private asset ID")
				}
				if link, ok := field(part, "mediaUrl").(string); ok {
					return link
				}
			}
			return ""
		}
		t.Fatalf("preview does not contain expected message %q", messageID)
		return ""
	}
	included(true)
	outgoing := sendManaged("first")
	t.Run("current quote requires a visible physically acknowledged original", func(t *testing.T) {
		const label = "fixture-visible-acked-meme"
		imageTags := field(asset, "tags").(*jsjson.Object).Clone()
		_ = imageTags.Set("emotion", []any{label})
		tagsWire, err := jsjson.Marshal(imageTags)
		if err != nil {
			t.Fatal(err)
		}
		appRequest(t, app, "PATCH", assetPath, map[string]any{"tags": json.RawMessage(tagsWire)})
		lookupScene := func() *jsjson.Object {
			t.Helper()
			for _, row := range appRequest(t, app, "GET", "/api/scenes", nil).([]any) {
				if field(row, "id") == scene {
					return row.(*jsjson.Object)
				}
			}
			t.Fatal("missing fixture scene")
			return nil
		}
		saved := lookupScene()
		change := saved.Clone()
		_ = change.Set("memoryReadEnabled", true)
		_ = change.Set("personalizationEnabled", true)
		writeScene := func(value *jsjson.Object) {
			raw, err := jsjson.Marshal(value)
			if err != nil {
				t.Fatal(err)
			}
			appRequest(t, app, "PUT", "/api/scenes/"+url.PathEscape(scene), json.RawMessage(raw))
		}
		writeScene(change)
		defer writeScene(saved)
		requests := make(chan string, 12)
		provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var request struct {
				Model    string
				Messages []struct {
					Role    string
					Content json.RawMessage
				}
			}
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				http.Error(w, "bad fixture request", 400)
				return
			}
			if request.Model != "quote-planner" {
				requests <- request.Model
				http.Error(w, "quoted stored label must not send pixels", 400)
				return
			}
			for _, message := range request.Messages {
				if message.Role == "user" {
					requests <- string(message.Content)
				}
			}
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprintf(w, `{"choices":[{"finish_reason":"tool_calls","message":{"role":"assistant","tool_calls":[{"id":"decide","type":"function","function":{"name":"participation__decide","arguments":%q}}]}}]}`, `{"action":"silent","reasonCode":"fixture"}`)
		}))
		defer provider.Close()
		models := fmt.Sprintf(`{"providers":[{"id":"qp","name":"Fixture","protocol":"openai-chat","baseUrl":%q,"retryCount":0,"timeoutMs":10000}],"models":[{"id":"qp","displayName":"Planner","providerId":"qp","apiModelName":"quote-planner","capabilities":["text","tools"]},{"id":"qv","displayName":"Vision","providerId":"qp","apiModelName":"forbidden-vision","capabilities":["text","vision"]}],"routes":[{"role":"planner","orderedModelIds":["qp"],"timeoutMs":10000},{"role":"vision","orderedModelIds":["qv"],"timeoutMs":10000}]}`, provider.URL+"/v1")
		appRequest(t, app, "PUT", "/api/models", json.RawMessage(models))
		defer appRequest(t, app, "PUT", "/api/models", json.RawMessage(cms))
		checkQuote := func(message, target string, want bool) {
			t.Helper()
			topic := ingest(scene, message, "person", "", target)
			h, err := runtime.Run(scene, topic, false)
			if err != nil {
				t.Fatal(err)
			}
			select {
			case <-h.Done:
				if r := h.Wait(); r.Status != "skipped" || r.Reason != "decision_silent" {
					t.Fatalf("quoted-source turn did not settle: %+v", r)
				}
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			select {
			case prompt := <-requests:
				if prompt == "forbidden-vision" {
					t.Fatal("managed quote sent pixels to the VLM")
				}
				got := strings.Contains(prompt, label) && strings.Contains(prompt, "stored_library_observation")
				if got != want {
					t.Fatalf("managed quote %s described=%t, want %t", message, got, want)
				}
			default:
				t.Fatal("quoted-source turn did not reach Planner")
			}
		}
		ingest(scene, "unproven-quote-image", "bot", id)
		checkQuote("quote-forged", "unproven-quote-image", false)
		disabled(true)
		checkQuote("quote-disabled", outgoing, false)
		disabled(false)
		included(true) // disabling an asset drops inclusion, even after re-enable
		checkQuote("quote-acked", outgoing, true)
		noVision, err := jsjson.Parse(models)
		if err != nil {
			t.Fatal(err)
		}
		cfg := noVision.(*jsjson.Object)
		routes, _ := cfg.Get("routes")
		_ = cfg.Set("routes", routes.([]any)[:1])
		wire, err := jsjson.Marshal(cfg)
		if err != nil {
			t.Fatal(err)
		}
		appRequest(t, app, "PUT", "/api/models", json.RawMessage(wire))
		checkQuote("quote-without-vision-route", outgoing, true)
		select {
		case unexpected := <-requests:
			t.Fatalf("additional model wire: %s", unexpected)
		default:
		}
	})
	included(false)
	check("proposed", "GET", scene, outgoing, "1", true)
	included(true)
	check("visible", "GET", scene, outgoing, "1", true)
	link := previewLink(outgoing)
	if link == "" || link != fixture.PreviewURL {
		t.Fatalf("preview returned %q, want TypeScript same-origin path %q", link, fixture.PreviewURL)
	}
	checkURL("previewRoundTrip", "GET", link, true)
	historyRows := func(t *testing.T, query string) []map[string]any {
		t.Helper()
		historyPath := "/api/scenes/" + url.PathEscape(scene) + "/history" + query
		request := httptest.NewRequest("GET", historyPath, nil)
		browserAuth(request)
		response := httptest.NewRecorder()
		app.ServeHTTP(response, request)
		var result struct{ Items []map[string]any }
		if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &result) != nil {
			t.Fatalf("history: %d %s", response.Code, response.Body)
		}
		return result.Items
	}
	t.Run("managed history links", func(t *testing.T) {
		for _, tc := range []struct{ name, query string }{{"local", "?source=local"}, {"default", ""}} {
			t.Run(tc.name, func(t *testing.T) {
				count := 0
				remoteSeen := false
				for _, row := range historyRows(t, tc.query) {
					if _, exists := row["sourceEventRef"]; exists {
						t.Fatal("history exposed private source metadata")
					}
					if row["id"] == "remote-only" {
						remoteSeen = true
						remote := row["parts"].([]any)[0].(map[string]any)
						if remote["assetId"] != nil || remote["mediaUrl"] != nil || remote["mediaKind"] != "image" {
							t.Fatalf("remote-only row was linked: %+v", remote)
						}
					}
					if row["id"] == outgoing {
						count++
						parts := row["parts"].([]any)
						if len(parts) != 2 {
							t.Fatalf("managed history part count = %d", len(parts))
						}
						part := parts[1].(map[string]any)
						if row["origin"] != "history" || row["senderId"] != "bot" || !reflect.DeepEqual(part, fixture.HistoryParts[tc.name]) {
							t.Fatalf("wrong owned history row: %+v", row)
						}
						checkURL("historyRoundTrip/"+tc.name, "GET", part["mediaUrl"].(string), true)
					}
				}
				if count != 1 || remoteSeen != (tc.name == "default") {
					t.Fatalf("history rows: managed=%d, remote-only=%t", count, remoteSeen)
				}
			})
		}
	})
	check("head", "HEAD", scene, outgoing, "1", true)
	check("unauthorized", "GET", scene, outgoing, "1", false)
	check("invalidIndex", "GET", scene, outgoing, "00", true)
	check("outOfRange", "GET", scene, outgoing, "256", true)
	check("wrongScene", "GET", "wrong-scene", outgoing, "1", true)
	check("scenePointsAt", "GET", strings.Repeat("😀", 4096), outgoing, "1", true)
	check("scenePointsBeyond", "GET", strings.Repeat("😀", 4097), outgoing, "1", true)
	check("messagePointsAt", "GET", scene, strings.Repeat("😀", 8192), "1", true)
	check("messagePointsBeyond", "GET", scene, strings.Repeat("😀", 8193), "1", true)
	assertHistoryUnlinked := func(messageID string) {
		t.Helper()
		for _, query := range []string{"?source=local", ""} {
			found := false
			for _, row := range historyRows(t, query) {
				if row["id"] != messageID {
					continue
				}
				found = true
				for _, part := range row["parts"].([]any) {
					fields := part.(map[string]any)
					if fields["mediaUrl"] != nil || fields["assetId"] != nil {
						t.Fatalf("history linked an unproven source on %q: %+v", query, fields)
					}
				}
			}
			if !found {
				t.Fatalf("expected unlinked history row %q on %q", messageID, query)
			}
		}
	}
	capture := func() *memeengine.DisplayImage {
		t.Helper()
		image, err := app.Engine.ReadDisplayImage(root, scene, outgoing, 1)
		if err != nil {
			t.Fatal(err)
		}
		return image
	}
	reject := func(image *memeengine.DisplayImage) {
		t.Helper()
		published := false
		err := app.readPublisher.Publish(ctx, func(c *sql.Conn) ([]byte, error) {
			return image.Bytes, image.VerifyPublication(ctx, c)
		}, func([]byte) error { published = true; return nil })
		if published || !errors.Is(err, memeengine.ErrDisplayImageUnavailable) {
			t.Fatalf("late revocation published stale bytes: %v, %v", published, err)
		}
	}
	image := capture()
	disabled(true)
	reject(image)
	check("disabled", "GET", scene, outgoing, "1", true)
	disabled(false)
	included(true)
	check("visibleAgain", "GET", scene, outgoing, "1", true)
	registered, ok := runtime.RegisteredScene(scene)
	if !ok {
		t.Fatal("missing runtime scene")
	}
	unmatched := registered
	unmatched.PlatformSceneID = "uncommitted-other-scene"
	if err := runtime.RegisterScene(unmatched); err != nil {
		t.Fatal(err)
	}
	if _, err := app.Engine.ReadDisplayImage(root, scene, outgoing, 1); !errors.Is(err, memeengine.ErrDisplayImageUnavailable) {
		t.Fatalf("captured different persisted transport: %v", err)
	}
	if err := runtime.RegisterScene(registered); err != nil {
		t.Fatal(err)
	}
	saveScene(scene, "different-platform-peer")
	if _, err := app.Engine.Memes.Original(root, memeservice.Context{SceneID: scene}, id); err != nil {
		t.Fatalf("remap fixture unexpectedly denies the ordinary asset owner: %v", err)
	}
	snapshot, err := runtime.Snapshot(scene)
	if err != nil {
		t.Fatal(err)
	}
	oldRetained := false
	for _, row := range snapshot.Messages {
		oldRetained = oldRetained || field(row, "id") == outgoing
	}
	if !oldRetained {
		t.Fatal("remap test requires the prior outbound row to remain retained")
	}
	if link := previewLink(outgoing); link != "" {
		t.Fatalf("remapped preview contains prior owner link %q", link)
	}
	assertHistoryUnlinked(outgoing)
	checkURL("wrongScene", "GET", link, true)
	// The SQL remap may commit before the corresponding runtime effect.
	if err := runtime.RegisterScene(registered); err != nil {
		t.Fatal(err)
	}
	if link := previewLink(outgoing); link != "" {
		t.Fatalf("preview used runtime's old transport instead of the durable remap: %q", link)
	}
	assertHistoryUnlinked(outgoing)
	checkURL("wrongScene", "GET", link, true)
	saveScene(scene, "group")
	ingest(scene, "unproven", "bot", id)
	if link := previewLink("unproven"); link != "" {
		t.Fatalf("unproven media exposed link %q", link)
	}
	assertHistoryUnlinked("unproven")
	check("wrongScene", "GET", scene, "unproven", "0", true)
	image = capture()
	if err := runtime.Recall(ctx, scene, outgoing); err != nil {
		t.Fatal(err)
	}
	reject(image)
	if link := previewLink(outgoing); link != "" {
		t.Fatalf("recalled preview contains image link %q", link)
	}
	t.Run("recalled history", func(t *testing.T) {
		for _, query := range []string{"?source=local", ""} {
			found := false
			for _, row := range historyRows(t, query) {
				if row["id"] == outgoing {
					found = true
					if row["status"] != "recalled" || len(row["parts"].([]any)) != 0 {
						t.Fatalf("recalled row disclosed old image proof: %+v", row)
					}
				}
			}
			if found != (query != "") {
				t.Fatalf("unexpected recalled visibility for %q", query)
			}
		}
	})
	checkURL("recalled", "GET", link, true)
	ingest(scene, "missing", "bot", strings.Repeat("f", 64))
	check("missingAsset", "GET", scene, "missing", "0", true)
	ingest(foreign, "foreign", "bot", id)
	check("foreignPrivateAsset", "GET", foreign, "foreign", "0", true)
	deleted := sendManaged("second")
	check("beforeDelete", "GET", scene, deleted, "1", true)
	appRequest(t, app, "DELETE", assetPath, nil)
	check("deleted", "GET", scene, deleted, "1", true)
}
