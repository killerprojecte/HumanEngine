package app

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/killerprojecte/HumanEngine/backend/internal/botruntime"
	"github.com/killerprojecte/HumanEngine/backend/internal/ingressrouting"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/memoryservice"
	"github.com/killerprojecte/HumanEngine/backend/internal/pluginmanager"
	"github.com/killerprojecte/HumanEngine/backend/internal/plugins"
	"github.com/killerprojecte/HumanEngine/backend/internal/sourceevents"
	snow "github.com/killerprojecte/HumanEngine/backend/plugins/snowluma/core"
	snowhost "github.com/killerprojecte/HumanEngine/backend/plugins/snowluma/host/runtime"
)

// This composition exercises the exact bundled guest and HTTP path without
// recompiling transient manager policy guests for configuration or replacement.
func TestSnowLumaInlineImageComposed(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	var owners Owners
	options := applicationOptions(t)
	options.Entries = []string{}
	counter := &inlineVisionCounter{entered: make(chan struct{}), release: make(chan struct{})}
	options.Counter = counter
	options.relationExposurePort = func(a *Application, o Owners) (*memoryservice.ExposurePort, error) {
		owners = o
		return memoryservice.NewExposurePort(a.Semantic.Memory, o.Sources, o.Main, o.Authority)
	}
	app, err := Open(ctx, options)
	if err != nil {
		t.Fatal(err)
	}
	defer app.Shutdown(context.Background())
	const guest = "snowluma-inline-composed"
	artifact, err := plugins.BuiltinArtifact("snowluma")
	if err != nil {
		t.Fatal(err)
	}
	bind := func(target *Application) (snowhost.EngineIngress, func(context.Context, []byte) ([]byte, error)) {
		t.Helper()
		repository, err := ingressrouting.NewSQLiteRepository(owners.Main, owners.Authority)
		if err != nil {
			t.Fatal(err)
		}
		identity, err := ingressrouting.New(repository)
		if err != nil {
			t.Fatal(err)
		}
		t.Log("compile and instantiate pinned SnowLuma guest")
		if err := target.WASM.Enable(ctx, guest, artifact); err != nil {
			t.Fatal(err)
		}
		call, err := target.WASM.BindCall(guest)
		if err != nil {
			t.Fatal(err)
		}
		return snowhost.EngineIngress{Engine: target.Engine, Compare: owners.Compare, Identity: identity}, call
	}
	ingress, call := bind(app)
	settings := appRequest(t, app, "GET", "/api/settings", nil).(*jsjson.Object)
	_ = settings.Set("sourceEventRevisionsEnabled", true)
	wire, err := jsjson.Marshal(settings)
	if err != nil {
		t.Fatal(err)
	}
	appRequest(t, app, "PUT", "/api/settings", json.RawMessage(wire))

	connections := make(chan *websocket.Conn, 2)
	unexpected := make(chan string, 8)
	mediaTokens := newSnowTokenFixture(t)
	var remoteReads atomic.Int32
	var opaqueListing atomic.Bool
	held, releaseHeld := make(chan struct{}), make(chan struct{})
	peer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer synthetic" {
			http.Error(w, "missing fixture token", http.StatusUnauthorized)
			return
		}
		socket, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer socket.CloseNow()
		connections <- socket
		for {
			_, wire, err := socket.Read(ctx)
			if err != nil {
				return
			}
			var request struct {
				Action string
				Echo   any
				Params struct {
					File      string
					MessageID any `json:"message_id"`
					GroupID   any `json:"group_id"`
				}
			}
			if json.Unmarshal(wire, &request) != nil {
				return
			}
			var data any = map[string]any{}
			switch request.Action {
			case "send_group_msg":
				if fmt.Sprint(request.Params.GroupID) != "144" {
					unexpected <- request.Action
					return
				}
				data = map[string]any{"message_id": 1499}
			case "get_login_info":
				data = map[string]any{"user_id": 42}
			case "get_group_msg_history":
				messages := []any{}
				for _, id := range []int{2, 900} {
					messages = append(messages, map[string]any{
						"message_type": "group", "group_id": 100, "user_id": 7, "message_id": id, "time": time.Now().Unix(),
						"message": []any{map[string]any{"type": "image", "data": map[string]any{"base64": "YQ=="}}},
					})
				}
				if opaqueListing.Load() {
					messages = append(messages, map[string]any{
						"message_type": "group", "group_id": 100, "user_id": 7, "message_id": 903, "time": time.Now().Unix(),
						"message": []any{map[string]any{"type": "image", "data": map[string]any{"file": "opaque-inline"}}},
					})
				}
				data = map[string]any{"messages": messages}
			case "get_image":
				result, ok := mediaTokens.action(ctx, request.Params.File)
				if ok {
					data = result
				} else {
					unexpected <- request.Action
				}
			case "get_msg":
				id := fmt.Sprint(request.Params.MessageID)
				remoteReads.Add(1)
				if id == "901" {
					close(held)
					select {
					case <-releaseHeld:
					case <-ctx.Done():
						return
					}
				}
				if id == "900" || id == "901" || id == "903" {
					locator := map[string]any{"base64": mediaTokens.encoded}
					if id != "900" {
						locator = map[string]any{"file": "opaque-inline"}
					}
					data = map[string]any{
						"message_type": "group", "group_id": 100, "user_id": 7, "message_id": request.Params.MessageID, "time": time.Now().Unix(),
						"message": []any{map[string]any{"type": "image", "data": locator}},
					}
				} else {
					unexpected <- request.Action
				}
			}
			out, _ := json.Marshal(map[string]any{"echo": request.Echo, "status": "ok", "retcode": 0, "data": data})
			if err := socket.Write(ctx, websocket.MessageText, out); err != nil {
				return
			}
		}
	}))
	defer func() { cancel(); peer.Close() }()
	config, err := json.Marshal(map[string]string{
		"url": "ws" + strings.TrimPrefix(peer.URL, "http"), "accountId": "42", "transport": "websocket",
	})
	if err != nil {
		t.Fatal(err)
	}
	// A published topic does not mean ingress finished its participation check.
	// Wait for that actual receiver callback before the manual model probe
	// enables participation, or ingress can also schedule an automatic turn.
	type probeEvent struct {
		Kind    string `json:"post_type"`
		Group   int    `json:"group_id"`
		Message int    `json:"message_id"`
	}
	isVisionProbe := func(event probeEvent) bool {
		return event.Kind == "message" && event.Group >= 102 && event.Group <= 105
	}
	type completedProbe struct {
		event probeEvent
		err   error
	}
	received := make(chan completedProbe, 1)
	activate := func(target *Application, registry *snowhost.Registry, revision uint64) (pluginmanager.BuiltinLifetime, *websocket.Conn) {
		t.Helper()
		t.Logf("connect authenticated native generation %d", revision)
		buildReceive := ingress.ReceiverFactory(func() {}, time.Now)
		factory := snowhost.Factory(snowhost.InstallationPorts{
			Registry: registry, Sender: snowhost.Ports{ReadOriginal: snowhost.OriginalReader(target.Engine)},
			BuildReceive: func(sender *snowhost.Sender) (func(context.Context, string) error, error) {
				receive, err := buildReceive(sender)
				if err != nil {
					return nil, err
				}
				return func(call context.Context, raw string) error {
					err := receive(call, raw)
					var event probeEvent
					if json.Unmarshal([]byte(raw), &event) == nil && isVisionProbe(event) {
						select {
						case received <- completedProbe{event, err}:
						case <-call.Done():
						}
					}
					return err
				}, nil
			},
			Stopped: target.cancelAdapterScenes,
			Error:   func(code string, err error) { t.Logf("native fixture transport %s: %v", code, err) },
		}, func(string) (string, bool) { return "", false })
		life, stop := context.WithCancel(ctx)
		t.Cleanup(stop)
		owner, err := factory(ctx, pluginmanager.BuiltinRuntimeInput{
			ID: guest, Revision: revision, Config: config, Life: life, Host: target.WASM, CallGuest: call,
			Current: func() bool { return life.Err() == nil },
			Secret: func(_ context.Context, key string) (string, bool, error) {
				if key == "token" {
					return "synthetic", true, nil
				}
				return "", false, nil
			},
		})
		if owner != nil {
			t.Cleanup(func() { _ = owner.CloseAndWait(context.Background()) })
		}
		if err != nil {
			t.Fatalf("native activation %d: %v", revision, err)
		}
		select {
		case socket := <-connections:
			return owner, socket
		case <-ctx.Done():
			t.Fatal(ctx.Err())
			return nil, nil
		}
	}
	registry := snowhost.NewRegistry(app.Engine.Runtime)
	first, socket := activate(app, registry, 1)
	defer first.CloseAndWait(context.Background())
	t.Log("exercise native ingress and original-byte browser route")
	emit := func(event any) {
		t.Helper()
		raw, err := json.Marshal(event)
		if err != nil {
			t.Fatal(err)
		}
		if err := socket.Write(ctx, websocket.MessageText, raw); err != nil {
			t.Fatal(err)
		}
		var probe probeEvent
		if err := json.Unmarshal(raw, &probe); err == nil && isVisionProbe(probe) {
			select {
			case complete := <-received:
				if complete.event != probe || complete.err != nil {
					t.Fatalf("native probe ingress mismatch: %+v, %v", complete.event, complete.err)
				}
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
		}
	}
	scene := snow.SceneID("snowluma", "42", "group", "100")
	appRequest(t, app, "PUT", "/api/scenes/"+url.PathEscape(scene), map[string]any{
		"id": scene, "adapterId": "snowluma", "accountId": "42", "platformSceneId": "100",
		"kind": "group", "name": "Sticker Intake Fixture", "participationEnabled": false,
		"learningEnabled": false, "proactiveEnabled": false,
	})
	verifyRetired := assertSnowLumaInlineImageActualIngress(t, ctx, app, emit, unexpected, true, func(part *botruntime.DisplayImageSource) {
		assertNativeStickerIntake(t, ctx, app, scene)
		if err := app.Engine.IntakeNativeSticker(ctx, part); !errors.Is(err, botruntime.ErrDisplayImageUnavailable) {
			t.Fatalf("native intake accepted an exact proof captured before recall: %v", err)
		}
	})
	var vision int
	if err := owners.Main.QueryRowContext(ctx, "SELECT count(*) FROM model_calls WHERE role='vision'").Scan(&vision); err != nil || vision != 0 {
		t.Fatalf("known-only intake attempted vision: count %d, err %v", vision, err)
	}
	messageID := snow.MessageID(scene, "901")
	getRemote := func(id string, authorized bool) *httptest.ResponseRecorder {
		path := "/api/scenes/" + url.PathEscape(scene) + "/messages/" + url.PathEscape(id) + "/media/0"
		req := httptest.NewRequestWithContext(ctx, http.MethodGet, path, nil)
		sourceFixtureAuth(req, authorized)
		res := httptest.NewRecorder()
		app.ServeHTTP(res, req)
		return res
	}
	before := remoteReads.Load()
	if res := getRemote(messageID, false); res.Code != http.StatusUnauthorized || remoteReads.Load() != before {
		t.Fatalf("unauthorized remote media reached get_msg: %d", res.Code)
	}
	result := make(chan *httptest.ResponseRecorder, 1)
	go func() { result <- getRemote(messageID, true) }()
	select {
	case <-held:
	case <-ctx.Done():
		t.Fatal("guarded get_msg did not reach relay")
	}
	root, release, err := app.Engine.RootContext(ctx)
	if err != nil {
		t.Fatal(err)
	}
	err = app.Engine.Runtime.Recall(root, scene, messageID)
	release()
	if err != nil {
		t.Fatal(err)
	}
	close(releaseHeld)
	select {
	case res := <-result:
		if res.Code != http.StatusNotFound {
			t.Fatalf("late recall exposed remote media: %d %s", res.Code, res.Body)
		}
	case <-ctx.Done():
		t.Fatal("recalled remote get_msg did not finish")
	}
	if res := getRemote(messageID, true); res.Code != http.StatusNotFound || remoteReads.Load() != before+1 {
		t.Fatalf("tombstoned media triggered another get_msg: %d, calls %d", res.Code, remoteReads.Load()-before)
	}

	enrolledID := snow.MessageID(scene, "902")
	enrolled, err := owners.Sources.Observe(ctx, sourceevents.Change{
		Kind: "ingress", BodyHash: strings.Repeat("a", 64),
		Identity: sourceevents.Identity{SceneID: scene, AdapterID: "snowluma", AccountID: "42", MessageID: enrolledID, SenderID: "synthetic-person"},
	})
	if err != nil || enrolled.Ref == nil {
		t.Fatalf("could not establish revision-only source prestate: %+v %v", enrolled, err)
	}
	if res := getRemote(enrolledID, true); res.Code != http.StatusNotFound || remoteReads.Load() != before+1 {
		t.Fatalf("enrolled source without retained body triggered get_msg: %d, calls %d", res.Code, remoteReads.Load()-before)
	}

	opaqueListing.Store(true)
	afterRecall := remoteReads.Load()
	history := appRequest(t, app, http.MethodGet, "/api/scenes/"+url.PathEscape(scene)+"/history", nil)
	opaqueID := snow.MessageID(scene, "903")
	found := false
	for _, row := range field(history, "items").([]any) {
		if field(row, "id") == opaqueID {
			part := field(row, "parts").([]any)[0]
			found = field(part, "mediaUrl") != nil && field(part, "assetId") == nil
		}
	}
	if !found || remoteReads.Load() != afterRecall {
		t.Fatal("remote listing did not mint a private opaque link without exact-message fetch")
	}
	select {
	case token := <-mediaTokens.calls:
		t.Fatalf("listing or recalled get_msg performed get_image: %s", token)
	default:
	}
	res := getRemote(opaqueID, true)
	encoded := base64.StdEncoding.EncodeToString(res.Body.Bytes())
	if res.Code != http.StatusOK || encoded != mediaTokens.encoded || remoteReads.Load() != afterRecall+1 {
		t.Fatalf("opaque remote get_msg continuation: HTTP %d, calls %d", res.Code, remoteReads.Load()-afterRecall)
	}
	select {
	case token := <-mediaTokens.calls:
		if token != "opaque-inline" {
			t.Fatalf("wrong remote platform token: %s", token)
		}
	default:
		t.Fatal("remote GET did not invoke get_image")
	}
	select {
	case token := <-mediaTokens.calls:
		t.Fatalf("extra get_image %s", token)
	default:
	}
	opaqueListing.Store(false)

	assertInlineVisionModel(t, ctx, app, emit, counter)
	verifyTokenRetired := mediaTokens.assertImages(t, ctx, app, emit)
	// The guest's ID is distinct from its registered adapter ID. Stop must
	// revoke the durable Snow scene's work, not all application media work.
	const unrelated = "independent-adapter-scene"
	appRequest(t, app, http.MethodPut, "/api/scenes/"+unrelated, map[string]any{
		"id": unrelated, "adapterId": "isolated", "accountId": "42", "platformSceneId": "100",
		"kind": "group", "name": "Independent Fixture", "participationEnabled": false,
		"learningEnabled": false, "proactiveEnabled": false,
	})
	admit := func(sceneID string) context.Context {
		t.Helper()
		entered := make(chan context.Context, 1)
		task, err := app.MediaTasks.Accept(sceneID, "stop-witness", func(work context.Context) error {
			entered <- work
			<-work.Done()
			return work.Err()
		})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			app.MediaTasks.CancelMessage(sceneID, "stop-witness")
			if err := task.Wait(context.Background()); !errors.Is(err, context.Canceled) {
				t.Errorf("media work did not drain: %v", err)
			}
		})
		select {
		case work := <-entered:
			return work
		case <-ctx.Done():
			t.Fatal("media stop witness was not admitted")
			return nil
		}
	}
	targetWork, unrelatedWork := admit(scene), admit(unrelated)
	if err := first.CloseAndWait(ctx); err != nil {
		t.Fatal(err)
	}
	if targetWork.Err() != context.Canceled || unrelatedWork.Err() != nil {
		t.Fatalf("adapter stop revoked wrong work: target=%v unrelated=%v", targetWork.Err(), unrelatedWork.Err())
	}
	app.MediaTasks.CancelMessage(unrelated, "stop-witness")
	second, currentSocket := activate(app, registry, 2)
	defer second.CloseAndWait(context.Background())
	verifyRetired()
	verifyTokenRetired()
	socket = currentSocket
	assertNativeStickerPersonalization(t, ctx, app, emit, scene, mediaTokens.encoded)
	assertNativeStickerTagReuse(t, ctx, app, emit, scene, mediaTokens.encoded)
	assertSlowVisionSceneIsolation(t, ctx, app, owners.Main, emit, mediaTokens.encoded)
	assetID, png, calls := assertAutomaticStickerAnalysisHeldRecall(t, ctx, app, emit, scene)
	if err := second.CloseAndWait(ctx); err != nil {
		t.Fatal(err)
	}
	if err := app.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(ctx, options)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Shutdown(context.Background())
	asset, err := reopened.Engine.Memes.AdminGet(ctx, assetID)
	if err != nil || field(asset, "analyzed") != true || field(asset, "knownOnly") != true ||
		field(asset, "available") != false || field(asset, "shared") != false ||
		!reflect.DeepEqual(field(field(asset, "tags"), "intent"), []any{"verified-automatic-label"}) {
		t.Fatalf("tag-only disposition was not restored: %v", err)
	}
	ingress, call = bind(reopened)
	third, currentSocket := activate(reopened, snowhost.NewRegistry(reopened.Engine.Runtime), 3)
	defer third.CloseAndWait(context.Background())
	socket = currentSocket
	visionCalls := assertStickerReuseRequests(t, ctx, reopened, emit, scene, png, "verified-automatic-label", nil)
	asset, err = reopened.Engine.Memes.AdminGet(ctx, assetID)
	if err != nil || field(asset, "analyzed") != true || field(asset, "knownOnly") != true ||
		field(asset, "available") != false || field(asset, "shared") != false || field(asset, "privacyReviewed") != false {
		t.Fatalf("cold duplicate changed stored sticker disposition: %v", err)
	}
	if err := third.CloseAndWait(ctx); err != nil {
		t.Fatal(err)
	}
	appRequest(t, reopened, "PUT", "/api/scenes/"+url.PathEscape(scene), map[string]any{
		"id": scene, "adapterId": "snowluma", "accountId": "42", "platformSceneId": "100",
		"kind": "group", "name": "Automatic Sticker Fixture", "participationEnabled": false,
		"learningEnabled": true, "proactiveEnabled": false, "memoryReadEnabled": true, "personalizationEnabled": true,
	})
	if err := reopened.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 5 || visionCalls.Load() != 0 {
		t.Fatal("reopening or re-enabling learning unexpectedly scheduled analysis")
	}
}
