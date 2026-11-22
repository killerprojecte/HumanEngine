package app

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/killerprojecte/HumanEngine/backend/internal/botruntime"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	snow "github.com/killerprojecte/HumanEngine/backend/plugins/snowluma/core"
)

func assertNativeStickerTagReuse(t *testing.T, ctx context.Context, app *Application, emit func(any), scene, png string) {
	t.Helper()
	assets, err := app.Engine.Memes.AdminList(ctx)
	if err != nil || len(assets) != 1 {
		t.Fatalf("missing durable known sticker: %v %v", assets, err)
	}
	asset := assets[0]
	tags := field(asset, "tags").(*jsjson.Object)
	assetID := field(asset, "id").(string)
	setEmotion := func(value string) {
		t.Helper()
		_ = tags.Set("emotion", []any{value})
		wire, err := jsjson.Marshal(tags)
		if err != nil {
			t.Fatal(err)
		}
		appRequest(t, app, "PATCH", "/api/memes/"+assetID, map[string]any{"tags": json.RawMessage(wire)})
	}
	const tag = "fixture-stored-happiness"
	setEmotion(tag)
	assertStickerReuseRequests(t, ctx, app, emit, scene, png, tag, setEmotion)
}

func assertStickerReuseRequests(t *testing.T, ctx context.Context, app *Application, emit func(any), scene, png, tag string, setEmotion func(string)) *atomic.Int32 {
	t.Helper()
	configureScene := func(participation bool) {
		appRequest(t, app, "PUT", "/api/scenes/"+url.PathEscape(scene), map[string]any{
			"id": scene, "adapterId": "snowluma", "accountId": "42", "platformSceneId": "100",
			"kind": "group", "name": "Sticker Reuse Fixture", "participationEnabled": participation,
			"learningEnabled": false, "proactiveEnabled": false,
			"memoryReadEnabled": true, "personalizationEnabled": true,
		})
	}
	var decisions, plans, sends, vision atomic.Int32
	var revoke atomic.Bool
	entered, paused, opened := make(chan struct{}), make(chan struct{}), make(chan struct{})
	close(opened)
	type observation struct {
		stage string
		tag   bool
	}
	calls := make(chan observation, 12)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "request read", 400)
			return
		}
		r.Body = io.NopCloser(bytes.NewReader(raw))
		hit := bytes.Contains(raw, []byte(tag)) && bytes.Contains(raw, []byte("stored_library_observation"))
		switch {
		case bytes.Contains(raw, []byte(`"model":"sticker-vision"`)):
			vision.Add(1)
			fmt.Fprint(w, `{"choices":[{"finish_reason":"stop","message":{"role":"assistant","content":"Transient fallback"}}]}`)
		case bytes.Contains(raw, []byte(`"model":"sticker-replyer"`)):
			calls <- observation{"replyer", hit}
			fmt.Fprint(w, `{"choices":[{"finish_reason":"stop","message":{"role":"assistant","content":"Synthetic completed without a send"}}]}`)
		default:
			calls <- observation{"planner", hit}
			release := (<-chan struct{})(opened)
			if revoke.Load() {
				release = paused
			}
			if err := serveMixedLoadModel(w, r, entered, release, &decisions, &plans, &sends); err != nil {
				http.Error(w, err.Error(), 400)
			}
		}
	}))
	t.Cleanup(server.Close)
	setRoutes := func(withVision bool) {
		routes := `{"role":"planner","orderedModelIds":["sp"],"timeoutMs":10000},{"role":"replyer","orderedModelIds":["sr"],"timeoutMs":10000}`
		if withVision {
			routes += `,{"role":"vision","orderedModelIds":["sv"],"timeoutMs":10000}`
		}
		appRequest(t, app, "PUT", "/api/models", json.RawMessage(fmt.Sprintf(`{"providers":[{"id":"sticker-fixture","name":"Fixture","protocol":"openai-chat","baseUrl":%q,"retryCount":0,"timeoutMs":10000}],"models":[{"id":"sp","displayName":"Planner","providerId":"sticker-fixture","apiModelName":"mixed-load","capabilities":["text","tools"]},{"id":"sr","displayName":"Replyer","providerId":"sticker-fixture","apiModelName":"sticker-replyer","capabilities":["text","tools"]},{"id":"sv","displayName":"Vision","providerId":"sticker-fixture","apiModelName":"sticker-vision","capabilities":["text","vision"]}],"routes":[%s]}`, server.URL+"/v1", routes)))
	}
	start := func(id int, withVision bool) *botruntime.TurnHandle {
		t.Helper()
		setRoutes(withVision)
		configureScene(false)
		// Encode the same bytes under a new message and a different valid locator.
		emit(map[string]any{"self_id": 42, "post_type": "message", "message_type": "group", "group_id": 100,
			"user_id": 7, "message_id": id, "time": time.Now().Unix(), "message": []any{
				map[string]any{"type": "mface", "data": map[string]any{"file": "data:image/png;base64," + png}},
			}})
		wait, cancel := context.WithTimeout(ctx, 8*time.Second)
		defer cancel()
		tick := time.NewTicker(5 * time.Millisecond)
		defer tick.Stop()
		for {
			if state, err := app.Engine.Runtime.Snapshot(scene); err == nil && len(state.Topics) != 0 {
				for _, row := range state.Messages {
					if field(row, "id") != snow.MessageID(scene, strconv.Itoa(id)) {
						continue
					}
					configureScene(true)
					turn, err := app.Engine.Runtime.Run(scene, state.Topics[0].ID, false)
					if err != nil {
						t.Fatal(err)
					}
					return turn
				}
			}
			select {
			case <-tick.C:
			case <-wait.Done():
				t.Fatal("native sticker never reached the turn")
			}
		}
	}
	await := func(turn *botruntime.TurnHandle) botruntime.TurnResult {
		t.Helper()
		select {
		case <-turn.Done:
			return turn.Wait()
		case <-ctx.Done():
			t.Fatal(ctx.Err())
			return botruntime.TurnResult{}
		}
	}
	cases := []struct {
		id     int
		vision bool
	}{{961, true}, {962, false}}
	if setEmotion == nil {
		cases = cases[:1]
		cases[0].id = 982
	}
	for _, cfg := range cases {
		final := await(start(cfg.id, cfg.vision))
		for _, stage := range []string{"planner", "planner", "replyer"} {
			select {
			case c := <-calls:
				if c.stage != stage || !c.tag {
					t.Fatalf("%+v did not reuse stored emotion; want %s, turn %+v", c, stage, final)
				}
			default:
				t.Fatalf("missing %s reuse; turn %+v", stage, final)
			}
		}
	}
	if got := vision.Load(); got != 0 {
		t.Fatalf("same sticker caused %d repeat vision calls", got)
	}
	if setEmotion == nil {
		return &vision
	}
	revoke.Store(true)
	held := start(963, false)
	select {
	case c := <-calls:
		if c.stage != "planner" || !c.tag {
			t.Fatalf("held selection did not previously qualify stored tags: %+v", c)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	setEmotion("new-reviewed-tag")
	close(paused)
	if final := await(held); final.Status == "completed" {
		t.Fatalf("turn carried stale stored tags through a tag edit: %+v", final)
	}
	select {
	case c := <-calls:
		t.Fatalf("stale stored evidence reached another provider call: %+v", c)
	default:
	}
	return &vision
}
