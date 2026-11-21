package app

import (
	"context"
	"net/url"
	"strconv"

	"github.com/killerprojecte/HumanEngine/backend/internal/botruntime"
	"github.com/killerprojecte/HumanEngine/backend/internal/memeengine"
	"testing"
	"time"

	snow "github.com/killerprojecte/HumanEngine/backend/plugins/snowluma/core"
)

func assertNativeStickerIntake(t *testing.T, ctx context.Context, app *Application, scene string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	tick := time.NewTicker(5 * time.Millisecond)
	defer tick.Stop()
	for {
		assets, err := app.Engine.Memes.AdminList(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if len(assets) != 0 {
			if len(assets) != 1 {
				t.Fatalf("expected one known asset, got %d", len(assets))
			}
			asset := assets[0]
			sources, _ := field(asset, "sources").([]any)
			if field(asset, "knownOnly") != true || field(asset, "available") != false ||
				field(asset, "analyzed") != false || len(sources) != 1 ||
				field(sources[0], "sceneId") != scene || field(sources[0], "messageId") != snow.MessageID(scene, "2") {
				t.Fatalf("expected only the explicit actual sticker as a known-only source: %#v (%#v)", asset, sources)
			}
			return
		}
		select {
		case <-ctx.Done():
			t.Fatal("actual SnowLuma sticker did not become visible to the known library")
		case <-tick.C:
		}
	}
}

func assertNativeStickerPersonalization(t *testing.T, ctx context.Context, app *Application, emit func(any), scene, png string) {
	t.Helper()
	observed := make(chan *botruntime.DisplayImageSource, 1)
	stop := app.Engine.Runtime.AddExtension(&botruntime.Extension{NativeInlineReceived: func(o botruntime.NativeInlineObservation) {
		_, id := o.Identity()
		if id != snow.MessageID(scene, "951") && id != snow.MessageID(scene, "952") {
			return
		}
		observed <- o.Select(memeengine.IsStickerMetadata)[0]
	}})
	defer stop()
	for _, c := range []struct {
		id             int
		read, personal bool
	}{
		{951, true, false},
		{952, false, true},
	} {
		put := map[string]any{
			"id": scene, "adapterId": "snowluma", "accountId": "42", "platformSceneId": "100",
			"kind": "group", "name": "Sticker Intake Fixture", "participationEnabled": false,
			"learningEnabled": false, "proactiveEnabled": false,
			"memoryReadEnabled": c.read, "personalizationEnabled": c.personal,
		}
		appRequest(t, app, "PUT", "/api/scenes/"+url.PathEscape(scene), put)
		emit(map[string]any{
			"self_id": 42, "post_type": "message", "message_type": "group", "group_id": 100,
			"user_id": 7, "message_id": c.id, "time": time.Now().Unix(),
			"message": []any{map[string]any{"type": "mface", "data": map[string]any{"base64": png}}},
		})
		select {
		case proof := <-observed:
			// Join a direct run of the same owner with the setting still disabled;
			// no assertion depends on scheduling of the automatic task.
			if err := app.Engine.IntakeNativeSticker(ctx, proof); err != nil {
				t.Fatal(err)
			}
			assertNativeStickerIntake(t, ctx, app, scene)
			// Re-enable of either flag cannot leave the previously admitted
			// task eligible after its exact accepted source is recalled.
			root, release, err := app.Engine.RootContext(ctx)
			if err != nil {
				t.Fatal(err)
			}
			err = app.Engine.Runtime.Recall(root, scene, snow.MessageID(scene, strconv.Itoa(c.id)))
			release()
			if err != nil {
				t.Fatal(err)
			}
		case <-ctx.Done():
			t.Fatal("did not capture accepted fixture source")
		}
	}
}
