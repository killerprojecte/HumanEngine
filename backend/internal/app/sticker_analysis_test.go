package app

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	snow "github.com/killerprojecte/HumanEngine/backend/plugins/snowluma/core"
)

// Run last: the explicit media-owner drain makes absence of a late write
// observable without sleeping or adding a product hook.
func assertAutomaticStickerAnalysisHeldRecall(t *testing.T, ctx context.Context, app *Application, emit func(any), scene string) (string, string, *atomic.Int32) {
	t.Helper()
	const sourceMarker = "exclusive-recalled-sticker-caption"
	const personaMarker = "exclusive-sticker-persona-marker"
	setLearning := func(enabled bool) {
		t.Helper()
		appRequest(t, app, "PUT", "/api/scenes/"+url.PathEscape(scene), map[string]any{
			"id": scene, "adapterId": "snowluma", "accountId": "42", "platformSceneId": "100",
			"kind": "group", "name": "Automatic Sticker Fixture", "participationEnabled": false,
			"learningEnabled": enabled, "proactiveEnabled": false, "memoryReadEnabled": true, "personalizationEnabled": true,
		})
	}
	setLearning(false)
	settings := appRequest(t, app, "GET", "/api/settings", nil).(*jsjson.Object)
	_ = settings.Set("persona", personaMarker)
	wire, err := jsjson.Marshal(settings)
	if err != nil {
		t.Fatal(err)
	}
	appRequest(t, app, "PUT", "/api/settings", json.RawMessage(wire))

	arrived, release := make(chan []byte, 4), make(chan struct{})
	transition := make(chan struct{})
	cancelled := make(chan struct{}, 1)
	var calls atomic.Int32
	var once, transitionOnce sync.Once
	finishTransition := func() { transitionOnce.Do(func() { close(transition) }) }
	finish := func() { once.Do(func() { close(release) }) }
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, e := io.ReadAll(r.Body)
		if e != nil {
			http.Error(w, "fixture read", 400)
			return
		}
		number := calls.Add(1)
		arrived <- raw
		tag := "verified-automatic-label"
		w.Header().Set("Content-Type", "application/json")
		if number == 2 || number == 5 {
			gate := (<-chan struct{})(release)
			if number == 2 {
				gate = transition
			}
			select {
			case <-gate:
			case <-r.Context().Done():
				cancelled <- struct{}{}
				return
			}
			tag = "revoked-proposal"
		}
		if number == 3 || number == 4 {
			tag = "verified-automatic-contextual-label"
		}
		content := fmt.Sprintf(`{"appearance":[],"style":[],"subject":[],"emotion":[],"intent":[%q],"tone":[],"intensity":[],"memeMeaning":[],"ocr":"","selection":{"usable":true,"shareable":%t,"confidence":0.99,"containsPersonalData":false,"sourceSpecific":false,"containsRealPerson":false}}`, tag, number != 3)
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"finish_reason": "stop", "message": map[string]any{"content": content}}}})
	}))
	t.Cleanup(server.Close)
	defer finish()
	defer finishTransition()
	appRequest(t, app, "PUT", "/api/models", json.RawMessage(fmt.Sprintf(`{"providers":[{"id":"auto-sticker-fixture","name":"Fixture","protocol":"openai-chat","baseUrl":%q,"retryCount":0,"timeoutMs":10000}],"models":[{"id":"auto","displayName":"Vision","providerId":"auto-sticker-fixture","apiModelName":"native-auto-sticker","capabilities":["text","vision"]}],"routes":[{"role":"vision","orderedModelIds":["auto"],"timeoutMs":10000}]}`, server.URL+"/v1")))
	wait, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	emitSticker := func(id int, red uint8) (string, string) {
		t.Helper()
		img := image.NewNRGBA(image.Rect(0, 0, 7, 6))
		img.SetNRGBA(2, 3, color.NRGBA{red, 134, 216, 255})
		var b bytes.Buffer
		if err := png.Encode(&b, img); err != nil {
			t.Fatal(err)
		}
		encoded := base64.StdEncoding.EncodeToString(b.Bytes())
		emit(map[string]any{"self_id": 42, "post_type": "message", "message_type": "group", "group_id": 100,
			"user_id": 7, "message_id": id, "time": time.Now().Unix(), "message": []any{
				map[string]any{"type": "text", "data": map[string]any{"text": sourceMarker + "-" + strconv.Itoa(id)}},
				map[string]any{"type": "mface", "data": map[string]any{"base64": encoded}},
			}})
		return snow.MessageID(scene, strconv.Itoa(id)), encoded
	}
	awaitAsset := func(sourceID string, analyzed bool) *jsjson.Object {
		t.Helper()
		tick := time.NewTicker(5 * time.Millisecond)
		defer tick.Stop()
		for {
			assets, err := app.Engine.Memes.AdminList(wait)
			if err != nil {
				t.Fatal(err)
			}
			for _, asset := range assets {
				if analyzed && field(asset, "analyzed") != true {
					continue
				}
				sources, _ := field(asset, "sources").([]any)
				for _, source := range sources {
					if field(source, "messageId") == sourceID {
						return asset
					}
				}
			}
			select {
			case <-tick.C:
			case <-wait.Done():
				t.Fatal("verified ingress did not store expected sticker disposition")
			}
		}
	}
	awaitPixels := func() {
		t.Helper()
		select {
		case raw := <-arrived:
			if !bytes.Contains(raw, []byte("data:image/png;base64,")) || bytes.Contains(raw, []byte(sourceMarker)) || bytes.Contains(raw, []byte(personaMarker)) {
				t.Fatal("automatic sticker request did not contain pixels only")
			}
		case <-wait.Done():
			t.Fatal("new exact sticker was admitted but did not invoke the configured automatic image-only analyzer")
		}
	}
	positive, positivePNG := emitSticker(978, 41)
	awaitAsset(positive, false)
	awaitPixels()
	labeled := awaitAsset(positive, true)
	tagsWire, err := jsjson.Marshal(field(labeled, "tags"))
	if err != nil || !bytes.Contains(tagsWire, []byte("verified-automatic-label")) ||
		field(labeled, "knownOnly") != true || field(labeled, "available") != false ||
		field(labeled, "shared") != false || field(labeled, "privacyReviewed") != false ||
		field(labeled, "selectionOrigin") != nil || field(labeled, "sharingOrigin") != nil {
		t.Fatal("verified image-only result did not publish labels without granting selection/sharing")
	}
	// Turning learning on must not let an in-flight off-lane answer become
	// the first canonical analysis of a now contextual asset.
	heldID, _ := emitSticker(980, 87)
	heldAsset := field(awaitAsset(heldID, false), "id").(string)
	awaitPixels()
	logs := app.options.Diagnostics.(*applicationLogBuffer)
	const withheld = "\"code\":\"media.intake_unavailable\""
	priorFailures := strings.Count(logs.snapshot(), withheld)
	setLearning(true)
	finishTransition()
	for strings.Count(logs.snapshot(), withheld) <= priorFailures {
		select {
		case <-time.After(5 * time.Millisecond):
		case <-wait.Done():
			t.Fatal("learning-on did not settle held tag-only work")
		}
	}
	currentHeld, err := app.Engine.Memes.AdminGet(wait, heldAsset)
	if err != nil || field(currentHeld, "analyzed") != false || calls.Load() != 2 {
		t.Fatalf("learning-on transition published tag-only work: calls=%d err=%v", calls.Load(), err)
	}

	// A distinct learning-on sticker performs its first contextual analysis
	// automatically, even while participation is disabled.
	awaitContextual := func(donor int) {
		t.Helper()
		select {
		case raw := <-arrived:
			if !bytes.Contains(raw, []byte("data:image/png;base64,")) ||
				!bytes.Contains(raw, []byte(personaMarker)) || !bytes.Contains(raw, []byte(sourceMarker+"-"+strconv.Itoa(donor))) {
				t.Fatal("automatic contextual analysis did not include exact pixels and isolated selection context")
			}
		case <-wait.Done():
			t.Fatal("learning-on background owner did not call the configured native model")
		}
	}
	onID, _ := emitSticker(981, 115)
	awaitContextual(981)
	learned := awaitAsset(onID, true)
	tagsWire, err = jsjson.Marshal(field(learned, "tags"))
	if err != nil || calls.Load() != 3 || !bytes.Contains(tagsWire, []byte("verified-automatic-contextual-label")) ||
		field(learned, "knownOnly") != false || field(learned, "available") != true ||
		field(learned, "shared") != false || field(learned, "privacyReviewed") != false ||
		field(learned, "selectionOrigin") != "automatic" || field(learned, "sharingOrigin") != "automatic" {
		t.Fatal("contextual automatic selection did not grant the fixture's local-only disposition")
	}
	warmID, _ := emitSticker(984, 115)
	if warm := awaitAsset(warmID, true); field(warm, "id") != field(learned, "id") {
		t.Fatal("same exact pixels did not reuse the analyzed contextual asset")
	}
	sharedID, _ := emitSticker(983, 164)
	awaitContextual(983)
	shared := awaitAsset(sharedID, true)
	if calls.Load() != 4 || field(shared, "available") != true || field(shared, "shared") != true || field(shared, "privacyReviewed") != true {
		t.Fatal("known-context generic sticker did not receive the explicit share disposition")
	}
	sourceID, _ := emitSticker(979, 23)
	assetID := field(awaitAsset(sourceID, false), "id").(string)
	awaitContextual(979)
	root, closeRoot, err := app.Engine.RootContext(ctx)
	if err != nil {
		t.Fatal(err)
	}
	err = app.Engine.Runtime.Recall(root, scene, sourceID)
	closeRoot()
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-cancelled:
	case <-wait.Done():
		t.Fatal("last-source recall did not cancel the in-flight provider request before teardown")
	}
	finish()
	if err := app.MediaTasks.CloseAndWait(ctx); err != nil {
		t.Fatal(err)
	}
	current, err := app.Engine.Memes.AdminGet(ctx, assetID)
	if err != nil {
		t.Fatal(err)
	}
	if current == nil {
		t.Fatal("retained known asset audit row disappeared")
	}
	sources, _ := field(current, "sources").([]any)
	tags := field(current, "tags")
	raw, err := jsjson.Marshal(tags)
	if err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 5 || len(sources) != 0 || field(current, "analyzed") == true || field(current, "available") == true ||
		field(current, "shared") == true || field(current, "privacyReviewed") == true || bytes.Contains(raw, []byte("revoked-proposal")) {
		t.Fatalf("revoked last source published an inference or triggered another provider request (calls=%d)", calls.Load())
	}
	t.Logf("native sticker provider calls after owner drain: %d (2 off, 3 contextual; warmed duplicate adds 0)", calls.Load())
	setLearning(false) // Restore the tag-only fixture state for the cold reopen.
	return field(labeled, "id").(string), positivePNG, &calls
}
