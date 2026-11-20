package app

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"net/url"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/memeengine"
	snow "github.com/killerprojecte/HumanEngine/backend/plugins/snowluma/core"
)

func assertSnowLumaInlineImageActualIngress(t *testing.T, ctx context.Context, app *Application, emit func(any), unexpected <-chan string, checkRemote bool) func() {
	t.Helper()
	raw, err := os.ReadFile("testdata/managed-display-image-source.json")
	if err != nil {
		t.Fatal(err)
	}
	var source struct{ Base64 string }
	if err := json.Unmarshal(raw, &source); err != nil {
		t.Fatal(err)
	}
	png, err := base64.StdEncoding.DecodeString(source.Base64)
	if err != nil {
		t.Fatal(err)
	}

	ingress := func(id int, kind string, data map[string]any, rest ...any) {
		t.Helper()
		emit(map[string]any{
			"self_id": 42, "post_type": "message", "message_type": "group", "group_id": 100,
			"user_id": 7, "message_id": id, "time": time.Now().Unix(),
			"message": append([]any{map[string]any{"type": kind, "data": data}}, rest...),
		})
	}
	const adapter, account, peerID = "snowluma", "42", "100"
	scene := snow.SceneID(adapter, account, "group", peerID)
	id := func(platform string) string { return snow.MessageID(scene, platform) }
	wait := func(messageID string, status string) *jsjson.Object {
		t.Helper()
		tick := time.NewTicker(5 * time.Millisecond)
		defer tick.Stop()
		waitContext, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		for {
			snapshot, err := app.Engine.Runtime.Snapshot(scene)
			if err == nil {
				for _, row := range snapshot.Messages {
					if field(row, "id") == messageID && field(row, "status") == status {
						return row
					}
				}
			}
			select {
			case <-tick.C:
			case <-waitContext.Done():
				t.Fatalf("native ingress did not publish %s:%s: %v", messageID, status, waitContext.Err())
			}
		}
	}
	checkURL := func(path string, wantStatus int, authorized bool) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest("GET", path, nil)
		sourceFixtureAuth(req, authorized)
		res := httptest.NewRecorder()
		app.ServeHTTP(res, req)
		if res.Code != wantStatus {
			t.Fatalf("media %q: status %d want %d: %s", path, res.Code, wantStatus, res.Body)
		}
		if wantStatus == 200 && (!bytes.Equal(res.Body.Bytes(), png) || res.Header().Get("Content-Type") != "image/png") {
			t.Fatalf("original bytes or verified content type differed: len=%d mime=%q", res.Body.Len(), res.Header().Get("Content-Type"))
		}
		return res
	}
	check := func(sceneID, messageID string, wantStatus int, authorized bool) *httptest.ResponseRecorder {
		t.Helper()
		return checkURL("/api/scenes/"+url.PathEscape(sceneID)+"/messages/"+url.PathEscape(messageID)+"/media/0", wantStatus, authorized)
	}
	ingress(1, "image", map[string]any{"base64": source.Base64})
	ingress(2, "mface", map[string]any{"file": "DATA:image/JPEG;base64," + source.Base64},
		map[string]any{"type": "image", "data": map[string]any{"base64": source.Base64}})
	ingress(3, "image", map[string]any{"file": "base64://YQ==\n"})
	ingress(4, "image", map[string]any{"url": "https://127.0.0.1/fixture", "file": "image-token"})
	ingress(5, "image", map[string]any{"base64": "YQ=="})
	row := wait(id("1"), "available")
	for _, message := range []string{"2", "3", "4", "5"} {
		wait(id(message), "available")
	}
	if field(row, "sourceEventRef") == nil {
		t.Fatal("fixture must exercise the exact source revision witness")
	}
	check(scene, id("1"), 200, true)
	check(scene, id("2"), 200, true)
	path := "/api/scenes/" + url.PathEscape(scene)
	assertLinks := func(expected map[string]map[int]int, mustSee ...string) {
		t.Helper()
		for _, surface := range []string{"/messages", "/history?source=local", "/history"} {
			body := appRequest(t, app, "GET", path+surface, nil)
			rows, isPreview := body.([]any)
			if !isPreview {
				rows, _ = field(body, "items").([]any)
			}
			seen := make(map[string]bool)
			for _, row := range rows {
				messageID := field(row, "id").(string)
				expectedParts := expected[messageID]
				if checkRemote && expected != nil && surface == "/history" && messageID == id("900") {
					expectedParts = map[int]int{0: 200}
				}
				parts, _ := field(row, "parts").([]any)
				found := 0
				for _, part := range parts {
					if field(part, "assetId") != nil {
						t.Fatalf("%s leaked private media reference", surface)
					}
					link, ok := field(part, "mediaUrl").(string)
					if !ok {
						continue
					}
					u, err := url.Parse(link)
					base := "/api/scenes/" + scene + "/messages/" + messageID + "/media/"
					if err != nil || u.IsAbs() || u.Host != "" || u.RawQuery != "" || len(u.Path) <= len(base) || u.Path[:len(base)] != base {
						t.Fatalf("%s minted invalid media link %q", surface, link)
					}
					index, err := strconv.Atoi(u.Path[len(base):])
					status := expectedParts[index]
					if err != nil || status == 0 {
						t.Fatalf("%s linked unexpected part %q[%d]", surface, messageID, index)
					}
					if messageID == id("2") && index == 0 && field(part, "mediaKind") != "sticker" {
						t.Fatalf("%s lost compacted sticker metadata", surface)
					}
					found++
					checkURL(link, status, true)
				}
				if found != len(expectedParts) {
					t.Fatalf("%s linked %d parts for %s; want %d", surface, found, messageID, len(expectedParts))
				}
				seen[messageID] = true
			}
			if checkRemote && expected != nil && surface == "/history" && !seen[id("900")] {
				t.Fatal("default history did not include eligible remote-only inline image")
			}
			for message := range expected {
				if !seen[message] {
					t.Fatalf("%s did not include expected row %q", surface, message)
				}
			}
			for _, message := range mustSee {
				if !seen[message] {
					t.Fatalf("%s did not retain the expected row %q", surface, message)
				}
			}
		}
	}
	links := map[string]map[int]int{id("1"): {0: 200}, id("2"): {0: 200, 2: 200}, id("3"): {0: 503}, id("4"): {0: 503}, id("5"): {0: 415}}
	assertLinks(links)

	check(scene, id("3"), 503, true)
	blocked := check(scene, id("4"), 503, true)
	var failure struct{ Error string }
	if err := json.Unmarshal(blocked.Body.Bytes(), &failure); err != nil || failure.Error != "image_fetch_unavailable" {
		t.Fatalf("blocked direct literal: %s, %v", blocked.Body, err)
	}
	select {
	case action := <-unexpected:
		t.Fatalf("blocked direct literal attempted adapter token fallback %s", action)
	default:
	}
	check(scene, id("5"), 415, true)
	check(scene, id("1"), 401, false)
	check(snow.SceneID(adapter, account, "group", "101"), id("1"), 404, true)
	_ = row.Set("id", id("666"))
	if _, err := app.Engine.Runtime.Ingest(ctx, scene, row, false); err != nil {
		t.Fatal(err)
	}
	wait(id("666"), "available")
	check(scene, id("666"), 404, true)
	assertLinks(links, id("4"), id("666"))

	root, release, err := app.Engine.RootContext(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	captured, err := app.Engine.ReadDisplayImage(root, scene, id("1"), 0)
	if err != nil {
		t.Fatal(err)
	}
	emit(map[string]any{"post_type": "notice", "notice_type": "group_recall", "self_id": 42, "group_id": 100, "message_id": 1, "user_id": 7, "operator_id": 7, "time": time.Now().Unix()})
	wait(id("1"), "recalled")
	assertRevoked := func(operation string, held *memeengine.DisplayImage) {
		t.Helper()
		published := false
		err := app.readPublisher.Publish(ctx, func(conn *sql.Conn) ([]byte, error) {
			return held.Bytes, held.VerifyPublication(ctx, conn)
		}, func([]byte) error { published = true; return nil })
		if published || !errors.Is(err, memeengine.ErrDisplayImageUnavailable) {
			t.Fatalf("late native %s published bytes: %v %v", operation, published, err)
		}
	}
	assertRevoked("recall", captured)
	check(scene, id("1"), 404, true)
	delete(links, id("1"))
	assertLinks(links, id("4"), id("666"))

	ingress(6, "image", map[string]any{"base64": source.Base64})
	edited := wait(id("6"), "available")
	captured, err = app.Engine.ReadDisplayImage(root, scene, id("6"), 0)
	if err != nil {
		t.Fatal(err)
	}
	parts := field(edited, "parts").([]any)
	_ = edited.Set("parts", append(parts, memoryToolObject("type", "text", "text", "Edited after reading.")))
	guard := memoryToolObject("editId", "inline-edit", "order", memoryToolObject("kind", "platform_sequence", "sequence", float64(1)))
	if _, err := app.Engine.Runtime.Edit(root, scene, edited, guard, false); err != nil {
		t.Fatal(err)
	}
	revised := wait(id("6"), "available")
	if len(field(revised, "parts").([]any)) != 2 {
		t.Fatal("native source fixture did not retain the edited body")
	}
	assertRevoked("edit", captured)
	check(scene, id("6"), 404, true)

	var saved *jsjson.Object
	for _, value := range appRequest(t, app, "GET", "/api/scenes", nil).([]any) {
		if field(value, "id") == scene {
			saved = value.(*jsjson.Object)
			break
		}
	}
	if saved == nil {
		t.Fatal("missing native scene")
	}
	remapped := saved.Clone()
	_ = remapped.Set("platformSceneId", "101")
	for _, next := range []*jsjson.Object{remapped, saved} {
		wire, err := jsjson.Marshal(next)
		if err != nil {
			t.Fatal(err)
		}
		appRequest(t, app, "PUT", "/api/scenes/"+url.PathEscape(scene), json.RawMessage(wire))
		if next == remapped {
			check(scene, id("2"), 404, true)
			assertLinks(nil)
		}
	}
	check(scene, id("2"), 200, true)
	assertLinks(links, id("6"))
	return func() {
		t.Helper()
		wait(id("2"), "available")
		check(scene, id("2"), 404, true)
		assertLinks(map[string]map[int]int{}, id("2"), id("666"))
		select {
		case action := <-unexpected:
			t.Fatalf("inline display attempted adapter fallback %s", action)
		default:
		}
	}
}
