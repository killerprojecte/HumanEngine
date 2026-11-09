package app

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/coder/websocket"
	"github.com/killerprojecte/HumanEngine/backend/internal/botruntime"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/memeservice"
	"github.com/killerprojecte/HumanEngine/backend/internal/sourceadmission"
	snow "github.com/killerprojecte/HumanEngine/backend/plugins/snowluma/core"
	snowhost "github.com/killerprojecte/HumanEngine/backend/plugins/snowluma/host/runtime"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestInstalledSnowLumaOwnersAndDispatch(t *testing.T) {
	options := applicationOptions(t)
	fixture, e := os.ReadFile("testdata/snowluma-model.mjs")
	if e != nil {
		t.Fatal(e)
	}
	if e = os.WriteFile(filepath.Join(options.PluginRoot, "snow-model.mjs"), fixture, 0600); e != nil {
		t.Fatal(e)
	}
	options.Entries = []string{"snow-model.mjs"}
	// Budget includes multiple real compile/instantiate/physical-unload cycles.
	// Production HTTP/WS action deadlines are unchanged.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	connections := make(chan *websocket.Conn, 8)
	sent := make(chan map[string]any, 8)
	closed := make(chan struct{}, 8)
	var writes sync.Mutex
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer synthetic" {
			http.Error(w, "synthetic credential missing", 401)
			return
		}
		socket, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer socket.CloseNow()
		defer func() { closed <- struct{}{} }()
		connections <- socket
		sentCount := 10
		for {
			_, raw, err := socket.Read(ctx)
			if err != nil {
				return
			}
			var request map[string]any
			if err = json.Unmarshal(raw, &request); err != nil {
				return
			}
			action, _ := request["action"].(string)
			var data any = map[string]any{}
			switch action {
			case "get_login_info":
				data = map[string]any{"user_id": 42}
			case "send_group_msg":
				sent <- request
				sentCount++
				data = map[string]any{"message_id": sentCount}
			case "get_group_msg_history", "get_friend_msg_history":
				data = map[string]any{"messages": []any{}}
			}
			response, _ := json.Marshal(map[string]any{"echo": request["echo"], "status": "ok", "retcode": 0, "data": data})
			writes.Lock()
			err = socket.Write(ctx, websocket.MessageText, response)
			writes.Unlock()
			if err != nil {
				return
			}
		}
	}))
	defer server.Close()
	application, e := Open(ctx, options)
	if e != nil {
		if application != nil {
			application.Shutdown(context.Background())
		}
		t.Fatal(e)
	}
	defer func() {
		if e := application.Shutdown(context.Background()); e != nil {
			t.Error(e)
		}
	}()
	// Bound each explicit administrative phase independently of the peer's
	// overall lifetime. Compilation is fixture setup, not a wire-action timeout.
	snowAdmin := func(path string, body any) {
		t.Helper()
		phase, stop := context.WithTimeout(ctx, 3*time.Minute)
		defer stop()
		started := time.Now()
		t.Logf("phase start %s", path)
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		request := httptest.NewRequest(http.MethodPut, path, bytes.NewReader(raw)).WithContext(phase)
		request.Header.Set("Authorization", "Bearer synthetic-token")
		request.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		application.ServeHTTP(response, request)
		t.Logf("phase finish %s after %s", path, time.Since(started))
		if response.Code != http.StatusOK {
			t.Fatalf("%s: %d %s", path, response.Code, response.Body.String())
		}
	}
	assertPolicyUnloaded := func() {
		t.Helper()
		if application.WASM.Loaded("snowluma-policy") {
			t.Fatal("idle temporary Snow policy guest remains loaded")
		}
	}
	assertPolicyUnloaded()
	settings := appRequest(t, application, http.MethodGet, "/api/settings", nil).(*jsjson.Object)
	_ = settings.Set("sourceEventRevisionsEnabled", true)
	_ = settings.Set("promptContextWindowEnabled", false)
	_ = settings.Set("debounceMs", float64(0))
	_ = settings.Set("groupTurnCooldownMs", float64(0))
	wire, _ := jsjson.Marshal(settings)
	appRequest(t, application, http.MethodPut, "/api/settings", json.RawMessage(wire))
	appRequest(t, application, http.MethodPut, "/api/plugins/snow-model/config", map[string]any{"media": false})
	appRequest(t, application, http.MethodPut, "/api/plugins/snow-model/enabled", map[string]any{"enabled": true})
	models := json.RawMessage(`{"providers":[{"id":"p","name":"Synthetic","protocol":"snow-model:deterministic","baseUrl":"https://synthetic.invalid","retryCount":0,"timeoutMs":10000}],"models":[{"id":"m","displayName":"Synthetic","providerId":"p","apiModelName":"synthetic","capabilities":["text","tools"]}],"routes":[{"role":"planner","orderedModelIds":["m"],"timeoutMs":10000},{"role":"replyer","orderedModelIds":["m"],"timeoutMs":10000}]}`)
	appRequest(t, application, http.MethodPut, "/api/models", models)
	endpoint := "ws" + strings.TrimPrefix(server.URL, "http")
	t.Log("stage: beginning explicit Snow configuration with temporary policy guest")
	snowAdmin("/api/plugins/snowluma/config", map[string]any{"url": endpoint, "accountId": "42", "token": "synthetic"})
	assertPolicyUnloaded()
	t.Log("stage: beginning actual Snow runtime activation")
	snowAdmin("/api/plugins/snowluma/enabled", map[string]any{"enabled": true})
	assertPolicyUnloaded()
	var socket *websocket.Conn
	select {
	case socket = <-connections:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	sceneID := snow.SceneID("snowluma", "42", "group", "99")
	ingress := func(id int) {
		t.Helper()
		body, _ := json.Marshal(map[string]any{"self_id": 42, "post_type": "message", "message_type": "group", "group_id": 99, "user_id": 7, "message_id": id, "time": time.Now().Unix(), "message": []any{map[string]any{"type": "text", "data": map[string]any{"text": "Please reply to this synthetic request."}}}})
		writes.Lock()
		e := socket.Write(ctx, websocket.MessageText, body)
		writes.Unlock()
		if e != nil {
			t.Fatal(e)
		}
	}
	ingress(1)
	var saved *jsjson.Object
	for saved == nil {
		for _, v := range appRequest(t, application, http.MethodGet, "/api/scenes", nil).([]any) {
			if field(v, "id") == sceneID {
				saved = v.(*jsjson.Object)
			}
		}
		if saved == nil {
			select {
			case <-ctx.Done():
				t.Fatal("actual Snow ingress absent", ctx.Err())
			case <-time.After(time.Millisecond):
			}
		}
	}
	// Scene discovery precedes Runtime ingress. Wait for the seed to be retained
	// while participation is still disabled, before admitting the reply turn.
	for {
		snapshot, err := application.Engine.Runtime.Snapshot(sceneID)
		if err != nil && err.Error() != "scene_missing" {
			t.Fatal(err)
		}
		seedRetained := false
		for _, message := range snapshot.Messages {
			seedRetained = seedRetained || field(message, "id") == snow.MessageID(sceneID, "1")
		}
		if seedRetained && snapshot.Active == 0 && snapshot.Pending == 0 && snapshot.Waiting == 0 {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal("seed ingress did not settle before enabling participation", ctx.Err())
		case <-time.After(time.Millisecond):
		}
	}
	_ = saved.Set("participationEnabled", true)
	_ = saved.Set("learningEnabled", true)
	wire, _ = jsjson.Marshal(saved)
	appRequest(t, application, http.MethodPut, "/api/scenes/"+url.PathEscape(sceneID), json.RawMessage(wire))
	waitIdle := func(receipt string) {
		t.Helper()
		for {
			snapshot, e := application.Engine.Runtime.Snapshot(sceneID)
			if e != nil {
				t.Fatal(e)
			}
			if snapshot.Active == 0 && snapshot.Pending == 0 && snapshot.Waiting == 0 {
				found := false
				for _, message := range snapshot.Messages {
					if field(message, "id") == snow.MessageID(sceneID, receipt) {
						found = true
					}
				}
				if !found {
					t.Fatalf("wire send lacks actual committed Runtime receipt %s", receipt)
				}
				return
			}
			select {
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			case <-time.After(time.Millisecond):
			}
		}
	}
	ingress(2)
	var textSend map[string]any
	select {
	case textSend = <-sent:
	case <-ctx.Done():
		t.Fatal("actual registered Runtime text send absent", ctx.Err())
	}
	if textSend["action"] != "send_group_msg" {
		t.Fatal(textSend)
	}
	waitIdle("11")
	t.Log("stage: actual text dispatch and receipt11 committed")
	// The media original is admitted by the real installation/Engine source owner.
	root, release, e := application.Engine.RootContext(ctx)
	if e != nil {
		t.Fatal(e)
	}
	sourceID := snow.MessageID(sceneID, "1")
	snapshot, e := application.Engine.Runtime.Snapshot(sceneID)
	if e != nil {
		release()
		t.Fatal(e)
	}
	var evidence []*jsjson.Object
	e = application.Engine.Sources.WithRuntimeReads(root, func(reads botruntime.ReadAuthority) error {
		message, err := application.Engine.Runtime.PreviewMessage(sceneID, sourceID, reads)
		if err != nil {
			return err
		}
		trusted, err := sourceadmission.TrustedEvidence([]*jsjson.Object{message}, func(scene, id string) (bool, error) { return reads.IsRecalled(snapshot.Scene, id) })
		if err != nil {
			return err
		}
		for _, v := range trusted {
			evidence = append(evidence, v.(*jsjson.Object))
		}
		return nil
	})
	if e != nil {
		release()
		t.Fatal(e)
	}
	img := image.NewNRGBA(image.Rect(0, 0, 7, 5))
	img.SetNRGBA(1, 1, color.NRGBA{R: 200, G: 30, B: 90, A: 255})
	var imageBytes bytes.Buffer
	if e = png.Encode(&imageBytes, img); e != nil {
		release()
		t.Fatal(e)
	}
	source := memoryToolObject("sceneId", sceneID, "messageId", sourceID, "senderId", snow.UserID("snowluma", "42", "7"))
	asset, e := application.Engine.Memes.Intake(root, memeservice.Context{SceneID: sceneID, SourceEvidence: evidence}, imageBytes.Bytes(), source, false, application.Engine.Codec)
	if e != nil {
		release()
		t.Fatal(e)
	}
	assetID := field(asset, "id").(string)
	tags := field(asset, "tags")
	_, e = application.Engine.Memes.Update(root, assetID, memoryToolObject("tags", tags, "available", true))
	release()
	if e != nil {
		t.Fatal(e)
	}
	original, e := snowhost.OriginalReader(application.Engine)(ctx, assetID, sceneID)
	if e != nil || field(original, "path") == nil {
		t.Fatalf("actual Original %v %v", original, e)
	}
	t.Log("stage: actual Memes Intake/Update/Original authorized; starting media turn")
	appRequest(t, application, http.MethodPut, "/api/plugins/snow-model/config", map[string]any{"media": true})
	ingress(3)
	var mediaSend map[string]any
	select {
	case mediaSend = <-sent:
	case <-ctx.Done():
		t.Fatal("actual registered Runtime media send absent", ctx.Err())
	}
	params := mediaSend["params"].(map[string]any)
	hasImage := false
	for _, v := range params["message"].([]any) {
		segment := v.(map[string]any)
		if segment["type"] == "image" {
			file := segment["data"].(map[string]any)["file"].(string)
			hasImage = strings.HasPrefix(file, "base64://")
		}
	}
	if !hasImage {
		t.Fatal("source-authorized media was not encoded on actual peer", mediaSend)
	}
	waitIdle("12")
	t.Log("stage: actual media dispatch and receipt12 committed")
	// Reconfigure while enabled: old generation must physically close before its
	// replacement is installed, and the stored token survives omitted secret input.
	t.Log("stage: beginning hot configuration with physical runtime retirement")
	snowAdmin("/api/plugins/snowluma/config", map[string]any{"transport": "websocket", "url": endpoint, "accountId": "42"})
	select {
	case <-closed:
	case <-ctx.Done():
		t.Fatal("configuration did not physically close old generation")
	}
	select {
	case socket = <-connections:
		t.Log("stage: hot configuration old physical close and replacement connected")
	case <-ctx.Done():
		t.Fatal("replacement generation missing")
	}
	snowAdmin("/api/plugins/snowluma/enabled", map[string]any{"enabled": false})
	assertPolicyUnloaded()
	if application.WASM.Loaded("snowluma") {
		t.Fatal("disabled Snow runtime guest remains loaded")
	}
	select {
	case <-closed:
	case <-ctx.Done():
		t.Fatal("disable did not physically close replacement")
	}
	root, release, e = application.Engine.RootContext(ctx)
	if e != nil {
		t.Fatal(e)
	}
	_, e = application.Engine.Memes.AdminSetIncluded(root, assetID, false)
	release()
	if e != nil {
		t.Fatal(e)
	}
	if _, e = snowhost.OriginalReader(application.Engine)(ctx, assetID, sceneID); e == nil {
		t.Fatal("removed Original remained authorized")
	}
}
