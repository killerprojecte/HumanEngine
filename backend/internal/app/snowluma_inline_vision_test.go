package app

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"image/png"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/killerprojecte/HumanEngine/backend/internal/botruntime"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/providertransport"
	snow "github.com/killerprojecte/HumanEngine/backend/plugins/snowluma/core"
)

type inlineVisionCounter struct {
	block   atomic.Bool
	entered chan struct{}
	release chan struct{}
}

func (c *inlineVisionCounter) Lookup(d providertransport.CounterTarget) providertransport.TextCounter {
	if d.APIModelName == "vision-synthetic" && c.block.Load() {
		return c
	}
	return nil
}
func (*inlineVisionCounter) Identity() (string, string) { return "vision-fixture", "1" }
func (c *inlineVisionCounter) Count([]byte) (float64, error) {
	close(c.entered)
	<-c.release
	return 1, nil
}

func assertInlineVisionModel(t *testing.T, ctx context.Context, app *Application, emit func(any), counter *inlineVisionCounter, tokenTest ...*opaqueVisionTestBarrier) {
	t.Helper()
	type actualCall struct {
		Model    string `json:"model"`
		Messages []struct {
			Content json.RawMessage `json:"content"`
		} `json:"messages"`
	}
	calls := make(chan actualCall, 8)
	var visionCalls atomic.Int32
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var call actualCall
		if r.URL.Path != "/v1/chat/completions" || json.NewDecoder(r.Body).Decode(&call) != nil {
			http.Error(w, "wrong wire", 400)
			return
		}
		calls <- call
		w.Header().Set("Content-Type", "application/json")
		switch call.Model {
		case "vision-synthetic":
			visionCalls.Add(1)
			fmt.Fprint(w, `{"choices":[{"finish_reason":"stop","message":{"role":"assistant","content":"Synthetic visual observation"}}]}`)
		case "planner-synthetic":
			fmt.Fprint(w, `{"choices":[{"finish_reason":"tool_calls","message":{"role":"assistant","tool_calls":[{"id":"decide","type":"function","function":{"name":"participation__decide","arguments":"{\"action\":\"silent\",\"reasonCode\":\"fixture\"}"}}]}}]}`)
		default:
			http.Error(w, "wrong model", 400)
		}
	}))
	t.Cleanup(provider.Close)
	t.Cleanup(func() {
		if count := visionCalls.Load(); count != 1 {
			t.Errorf("expected exactly one authorized model image request, saw %d", count)
		}
	})
	models := json.RawMessage(fmt.Sprintf(`{"providers":[{"id":"vision-fixture","name":"Synthetic vision","protocol":"openai-chat","baseUrl":%q,"retryCount":0,"timeoutMs":15000}],"models":[{"id":"vision","displayName":"Vision","providerId":"vision-fixture","apiModelName":"vision-synthetic","capabilities":["text","vision"]},{"id":"planner","displayName":"Planner","providerId":"vision-fixture","apiModelName":"planner-synthetic","capabilities":["text","tools"]}],"routes":[{"role":"vision","orderedModelIds":["vision"],"timeoutMs":15000},{"role":"planner","orderedModelIds":["planner"],"timeoutMs":15000}]}`, provider.URL+"/v1"))
	appRequest(t, app, "PUT", "/api/models", models)

	raw, err := os.ReadFile("testdata/managed-display-image-source.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct{ Base64 string }
	if err := json.Unmarshal(raw, &fixture); err != nil {
		t.Fatal(err)
	}
	begin := func(group int) (*botruntime.TurnHandle, string) {
		t.Helper()
		sceneID := snow.SceneID("snowluma", "42", "group", fmt.Sprint(group))
		emitImage := func(id int, data map[string]any) {
			at := time.Now().Unix()
			if id == 2 {
				at-- // make fixture priority independent of clock granularity
			}
			emit(map[string]any{"self_id": 42, "post_type": "message", "message_type": "group", "group_id": group, "user_id": 7,
				"message_id": id, "time": at, "message": []any{map[string]any{"type": "image", "data": data}}})
		}
		count := 1
		if len(tokenTest) == 0 {
			emitImage(1, map[string]any{"base64": fixture.Base64})
		} else {
			if group == 102 {
				// The newest guarded image outranks the older ready-inline image.
				emitImage(2, map[string]any{"base64": fixture.Base64})
				count++
			}
			if tokenTest[0].direct {
				emitImage(1, map[string]any{"url": "https://vision.invalid/original.png"})
			} else {
				emitImage(1, map[string]any{"file": "opaque-" + fmt.Sprint(group)})
			}
		}
		wait, cancel := context.WithTimeout(ctx, 8*time.Second)
		defer cancel()
		var topic string
		tick := time.NewTicker(5 * time.Millisecond)
		defer tick.Stop()
		for topic == "" {
			if v, err := app.Engine.Runtime.Snapshot(sceneID); err == nil && len(v.Topics) != 0 && len(v.Messages) == count {
				topic = v.Topics[0].ID
				break
			}
			select {
			case <-tick.C:
			case <-wait.Done():
				t.Fatal("native vision seed did not enter a recent topic")
			}
		}
		for _, candidate := range appRequest(t, app, "GET", "/api/scenes", nil).([]any) {
			if field(candidate, "id") != sceneID {
				continue
			}
			scene := candidate.(*jsjson.Object)
			if field(scene, "learningEnabled") != false {
				t.Fatal("fixture requires learning disabled")
			}
			_ = scene.Set("participationEnabled", true)
			wire, err := jsjson.Marshal(scene)
			if err != nil {
				t.Fatal(err)
			}
			appRequest(t, app, "PUT", "/api/scenes/"+url.PathEscape(sceneID), json.RawMessage(wire))
			handle, err := app.Engine.Runtime.Run(sceneID, topic, false)
			if err != nil {
				t.Fatal(err)
			}
			return handle, sceneID
		}
		t.Fatal("native vision seed did not persist its scene")
		return nil, ""
	}
	awaitCall := func(model string) actualCall {
		t.Helper()
		select {
		case call := <-calls:
			if call.Model != model {
				t.Fatalf("upstream order: got %s want %s", call.Model, model)
			}
			return call
		case <-time.After(8 * time.Second):
			t.Fatalf("missing %s upstream request", model)
			return actualCall{}
		}
	}
	awaitDone := func(handle *botruntime.TurnHandle) botruntime.TurnResult {
		t.Helper()
		select {
		case <-handle.Done:
			return handle.Wait()
		case <-time.After(8 * time.Second):
			t.Fatal("native vision turn did not settle")
			return botruntime.TurnResult{}
		}
	}
	good, _ := begin(102)
	vision := awaitCall("vision-synthetic")
	var actualImage string
	for _, message := range vision.Messages {
		var parts []struct {
			Type  string
			Image struct{ URL string } `json:"image_url"`
		}
		if json.Unmarshal(message.Content, &parts) != nil {
			continue
		}
		for _, part := range parts {
			if part.Type == "image_url" {
				actualImage = part.Image.URL
			}
		}
	}
	encoded, ok := strings.CutPrefix(actualImage, "data:image/png;base64,")
	data, err := base64.StdEncoding.DecodeString(encoded)
	if !ok || err != nil {
		t.Fatalf("upstream did not receive a PNG data URL: %v", err)
	}
	if _, err := png.Decode(bytes.NewReader(data)); err != nil {
		t.Fatalf("upstream inline image could not be decoded: %v", err)
	}
	decision := awaitCall("planner-synthetic")
	found := false
	for _, message := range decision.Messages {
		found = found || bytes.Contains(message.Content, []byte("Synthetic visual observation"))
	}
	if !found {
		t.Fatal("predecision request omitted vision observation")
	}
	if final := awaitDone(good); final.Status != "skipped" || final.Reason != "decision_silent" {
		t.Fatalf("first vision turn failed: %+v", final)
	}

	counter.block.Store(true)
	late, sceneID := begin(103)
	defer func() {
		if counter.block.Swap(false) {
			close(counter.release)
		}
	}()
	select {
	case <-counter.entered:
	case <-time.After(8 * time.Second):
		t.Fatal("late vision request did not reach serialized wire barrier")
	}
	recall := func(group int, sceneID string) {
		t.Helper()
		emit(map[string]any{"post_type": "notice", "notice_type": "group_recall", "self_id": 42,
			"group_id": group, "message_id": 1, "user_id": 7, "operator_id": 7, "time": time.Now().Unix()})
		wait, cancel := context.WithTimeout(ctx, 8*time.Second)
		defer cancel()
		tick := time.NewTicker(5 * time.Millisecond)
		defer tick.Stop()
		for {
			if v, err := app.Engine.Runtime.Snapshot(sceneID); err == nil && len(v.Messages) == 1 && field(v.Messages[0], "status") == "recalled" {
				break
			}
			select {
			case <-tick.C:
			case <-wait.Done():
				t.Fatal("native recall did not commit at wire barrier")
			}
		}
	}
	recall(103, sceneID)
	counter.block.Store(false)
	close(counter.release)
	if final := awaitDone(late); final.Status != "cancelled" {
		t.Fatalf("late recall did not cancel the image turn: %+v", final)
	}
	if len(tokenTest) != 0 {
		barrier := tokenTest[0]
		held, sceneID := begin(104)
		select {
		case <-barrier.entered:
		case <-time.After(8 * time.Second):
			t.Fatal("image request did not reach its guarded adapter boundary")
		}
		if barrier.setAvailable != nil {
			barrier.setAvailable(false)
		} else {
			recall(104, sceneID)
		}
		barrier.releaseHeld()
		if final := awaitDone(held); final.Status != "cancelled" {
			t.Fatalf("held image revocation did not cancel the image turn: %+v", final)
		}
		if barrier.setAvailable != nil {
			barrier.setAvailable(true)
		}

		routerWithoutVision, err := jsjson.ParseBytes(models)
		if err != nil {
			t.Fatal(err)
		}
		config := routerWithoutVision.(*jsjson.Object)
		routes, _ := config.Get("routes")
		_ = config.Set("routes", routes.([]any)[1:])
		wire, err := jsjson.Marshal(config)
		if err != nil {
			t.Fatal(err)
		}
		appRequest(t, app, "PUT", "/api/models", json.RawMessage(wire))
		noRoute, _ := begin(105)
		decision := awaitCall("planner-synthetic")
		describedMissing := false
		for _, message := range decision.Messages {
			describedMissing = describedMissing || bytes.Contains(message.Content, []byte("route_not_configured"))
		}
		if final := awaitDone(noRoute); final.Status != "skipped" || !describedMissing {
			t.Fatalf("route-less context failed: %+v", final)
		}
	}
}
