//go:build bundled_wasm

package app

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"net/url"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/killerprojecte/HumanEngine/backend/internal/botruntime"
	"github.com/killerprojecte/HumanEngine/backend/internal/dispatch"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	snow "github.com/killerprojecte/HumanEngine/backend/plugins/snowluma/core"
)

// This arm exercises the native application, retained ingress proof and browser
// byte publication; transport/decoding is separately exercised by a real
// bundled Sender. There is no test endpoint or shipping injection.
func TestTokenImageNativeApplicationProof(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	options := applicationOptions(t)
	options.Entries = []string{}
	app, err := Open(ctx, options)
	if err != nil {
		t.Fatal(err)
	}
	defer app.Shutdown(context.Background())
	root, releaseRoot, err := app.Engine.RootContext(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer releaseRoot()
	settings := appRequest(t, app, "GET", "/api/settings", nil).(*jsjson.Object)
	_ = settings.Set("sourceEventRevisionsEnabled", true)
	wire, err := jsjson.Marshal(settings)
	if err != nil {
		t.Fatal(err)
	}
	appRequest(t, app, "PUT", "/api/settings", json.RawMessage(wire))

	fixture := newSnowTokenFixture(t)
	original, err := base64.StdEncoding.DecodeString(fixture.encoded)
	if err != nil {
		t.Fatal(err)
	}
	const peer = "104"
	scene := botruntime.Scene{SceneRef: dispatch.SceneRef{
		ID: snow.SceneID("snowluma", "42", "group", peer), AdapterID: "snowluma", AccountID: "42", Kind: "group", PlatformSceneID: peer,
	}}
	message := func(id int, token string) (*jsjson.Object, string) {
		t.Helper()
		raw, err := jsjson.Parse(`{"self_id":42,"post_type":"message","message_type":"group","group_id":104,"user_id":7}`)
		if err != nil {
			t.Fatal(err)
		}
		event := raw.(*jsjson.Object)
		_ = event.Set("message_id", float64(id))
		_ = event.Set("time", float64(time.Now().Unix()))
		_ = event.Set("message", []any{memoryToolObject("type", "image", "data", memoryToolObject("file", token))})
		row, _, err := snow.Normalize("snowluma", "42", event, "live", time.Now().UTC().Format(time.RFC3339Nano), false, nil)
		if err != nil {
			t.Fatal(err)
		}
		parts := field(row, "parts").([]any)
		return row, field(parts[0], "assetId").(string)
	}
	available, availableAsset := message(1, "native-opaque-a")
	held, heldAsset := message(2, "native-opaque-held")
	unproven, _ := message(3, "native-opaque-a")
	entered, release := make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	releaseHeld := func() { releaseOnce.Do(func() { close(release) }) }
	defer releaseHeld()
	var actions atomic.Int32
	newAdapter := func(read func(context.Context, botruntime.Scene, string, func(context.Context) error) ([]byte, error)) *botruntime.Adapter {
		return &botruntime.Adapter{
			InlineImage: &botruntime.InlineImageAdapter{
				Current: func() bool { return true },
				Links:   func(botruntime.Scene, string) bool { return false },
				Read: func(context.Context, botruntime.Scene, string) ([]byte, error) {
					return nil, errors.New("opaque source is not direct inline")
				},
			},
			BrowserImage: read,
		}
	}
	owner := newAdapter(func(call context.Context, bound botruntime.Scene, locator string, guard func(context.Context) error) ([]byte, error) {
		if bound.SceneRef.ID != scene.ID || bound.PlatformSceneID != peer || (locator != availableAsset && locator != heldAsset) {
			return nil, errors.New("browser used the wrong retained owner or locator")
		}
		if err := guard(call); err != nil {
			return nil, err
		}
		actions.Add(1)
		if locator == heldAsset {
			close(entered)
			select {
			case <-release:
			case <-call.Done():
				return nil, call.Err()
			}
		}
		return bytes.Clone(original), nil
	})
	runtime := app.Engine.Runtime
	runtime.AddAdapter(scene.AdapterID, owner)
	for _, row := range []*jsjson.Object{available, held} {
		if _, err := runtime.IngestNativeMedia(root, owner, scene, row, false); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := runtime.IngestScene(root, scene, unproven, false); err != nil {
		t.Fatal(err)
	}
	scope := "/api/scenes/" + url.PathEscape(scene.ID)
	get := func(n int, authorized bool) *httptest.ResponseRecorder {
		id := snow.MessageID(scene.ID, strconv.Itoa(n))
		req := httptest.NewRequestWithContext(ctx, "GET", scope+"/messages/"+url.PathEscape(id)+"/media/0", nil)
		sourceFixtureAuth(req, authorized)
		out := httptest.NewRecorder()
		app.ServeHTTP(out, req)
		return out
	}
	check := func(n, status int, authorized bool) {
		t.Helper()
		res := get(n, authorized)
		if res.Code != status || status == 200 && (!bytes.Equal(res.Body.Bytes(), original) || res.Header().Get("Content-Type") != "image/png") {
			t.Fatalf("token row %d: code=%d wanted=%d bytes=%d MIME=%q", n, res.Code, status, res.Body.Len(), res.Header().Get("Content-Type"))
		}
	}
	check(1, 401, false)
	check(3, 404, true)
	check(1, 200, true)
	if actions.Load() != 1 {
		t.Fatal("unproven or unauthorized input reached token IO")
	}

	var saved *jsjson.Object
	for _, v := range appRequest(t, app, "GET", "/api/scenes", nil).([]any) {
		if field(v, "id") == scene.ID {
			saved = v.(*jsjson.Object)
			break
		}
	}
	if saved == nil {
		t.Fatal("native source did not persist its scene")
	}
	remapped := saved.Clone()
	_ = remapped.Set("platformSceneId", "105")
	update := func(v *jsjson.Object) {
		t.Helper()
		wire, err := jsjson.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		appRequest(t, app, "PUT", scope, json.RawMessage(wire))
	}
	update(remapped)
	check(1, 404, true)
	update(saved)
	check(1, 200, true)
	result := make(chan *httptest.ResponseRecorder, 1)
	go func() { result <- get(2, true) }()
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("token request did not cross its exact guarded transport boundary")
	}
	if err := runtime.Recall(root, scene.ID, snow.MessageID(scene.ID, "2")); err != nil {
		t.Fatal(err)
	}
	releaseHeld()
	select {
	case res := <-result:
		if res.Code != 404 || bytes.Equal(res.Body.Bytes(), original) {
			t.Fatalf("late recall exposed token result: status=%d bytes=%d", res.Code, res.Body.Len())
		}
	case <-ctx.Done():
		t.Fatal("token response did not settle after recall")
	}

	runtime.AddAdapter(scene.AdapterID, newAdapter(func(context.Context, botruntime.Scene, string, func(context.Context) error) ([]byte, error) {
		actions.Add(1)
		return bytes.Clone(original), nil
	}))
	check(1, 404, true)
	if actions.Load() != 3 {
		t.Fatalf("unexpected guarded token calls: %d", actions.Load())
	}
}
