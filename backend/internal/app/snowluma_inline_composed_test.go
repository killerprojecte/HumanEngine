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
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
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
	repository, err := ingressrouting.NewSQLiteRepository(owners.Main, owners.Authority)
	if err != nil {
		t.Fatal(err)
	}
	identity, err := ingressrouting.New(repository)
	if err != nil {
		t.Fatal(err)
	}
	const guest = "snowluma-inline-composed"
	artifact, err := plugins.BuiltinArtifact("snowluma")
	if err != nil {
		t.Fatal(err)
	}
	t.Log("compile and instantiate pinned SnowLuma guest")
	if err := app.WASM.Enable(ctx, guest, artifact); err != nil {
		t.Fatal(err)
	}
	call, err := app.WASM.BindCall(guest)
	if err != nil {
		t.Fatal(err)
	}
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
				}
			}
			if json.Unmarshal(wire, &request) != nil {
				return
			}
			var data any = map[string]any{}
			switch request.Action {
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
	ingress := snowhost.EngineIngress{Engine: app.Engine, Compare: owners.Compare, Identity: identity}
	factory := snowhost.Factory(snowhost.InstallationPorts{
		Registry: app.snowRegistry, Sender: snowhost.Ports{ReadOriginal: snowhost.OriginalReader(app.Engine)},
		BuildReceive: ingress.ReceiverFactory(func() {}, time.Now), Stopped: app.snowStopped,
		Error: func(code string, err error) { t.Logf("native fixture transport %s: %v", code, err) },
	}, func(string) (string, bool) { return "", false })
	activate := func(revision uint64) (pluginmanager.BuiltinLifetime, *websocket.Conn) {
		t.Helper()
		t.Logf("connect authenticated native generation %d", revision)
		life, stop := context.WithCancel(ctx)
		t.Cleanup(stop)
		owner, err := factory(ctx, pluginmanager.BuiltinRuntimeInput{
			ID: guest, Revision: revision, Config: config, Life: life, Host: app.WASM, CallGuest: call,
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
	first, socket := activate(1)
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
	if err := first.CloseAndWait(ctx); err != nil {
		t.Fatal(err)
	}
	second, currentSocket := activate(2)
	defer second.CloseAndWait(context.Background())
	verifyRetired()
	verifyTokenRetired()
	socket = currentSocket
	assertNativeStickerPersonalization(t, ctx, app, emit, scene, mediaTokens.encoded)
	assertNativeStickerTagReuse(t, ctx, app, emit, scene, mediaTokens.encoded)
}
