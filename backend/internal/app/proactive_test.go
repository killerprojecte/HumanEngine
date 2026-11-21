package app

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
	"time"

	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/proactiveowner"
)

type appProactiveClock struct {
	period  time.Duration
	tick    func()
	stopped bool
}

func (c *appProactiveClock) Every(period time.Duration, tick func()) proactiveowner.Stopper {
	c.period, c.tick = period, tick
	return c
}
func (c *appProactiveClock) Stop() { c.stopped = true }

func TestInstalledProactiveTimerUsesCurrentSceneAndDrains(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	clock := new(appProactiveClock)
	entered := make(chan bool, 4)
	settled := make(chan struct{}, 4)
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() { settled <- struct{}{} }()
		var call struct {
			Messages []struct{ Role, Content string }
		}
		if err := json.NewDecoder(r.Body).Decode(&call); err != nil {
			t.Error(err)
			return
		}
		valid := false
		for _, msg := range call.Messages {
			if msg.Role != "user" {
				continue
			}
			var body struct {
				Proactive bool
				Messages  []struct{ Parts []struct{ Text string } }
			}
			if json.Unmarshal([]byte(msg.Content), &body) == nil && body.Proactive &&
				slices.ContainsFunc(body.Messages, func(item struct{ Parts []struct{ Text string } }) bool {
					return len(item.Parts) == 1 && item.Parts[0].Text == "Proactive fixture eligible"
				}) {
				valid = true
			}
		}
		entered <- valid
		<-r.Context().Done()
	}))
	defer provider.Close()
	options := applicationOptions(t)
	options.Entries = []string{}
	options.proactiveClock = clock
	app, err := Open(ctx, options)
	if err != nil {
		t.Fatal(err)
	}
	defer app.Shutdown(context.Background())
	if clock.period != time.Minute || clock.tick == nil {
		t.Fatal("installed runtime did not start the one-minute producer")
	}
	appRequest(t, app, "PUT", "/api/models", json.RawMessage(fmt.Sprintf(`{
"providers":[{"id":"loopback","name":"Fixture","protocol":"openai-chat","baseUrl":%q,"retryCount":0,"timeoutMs":30000}],
"models":[{"id":"m","providerId":"loopback","displayName":"Fixture","apiModelName":"proactive","capabilities":["text","tools"]}],
"routes":[{"role":"planner","orderedModelIds":["m"],"timeoutMs":30000}]}`, provider.URL+"/v1")))
	settings := appRequest(t, app, "GET", "/api/settings", nil).(*jsjson.Object)
	_ = settings.Set("promptContextWindowEnabled", false)
	_ = settings.Set("topicAdmissionEnabled", false)
	wire, err := jsjson.Marshal(settings)
	if err != nil {
		t.Fatal(err)
	}
	appRequest(t, app, "PUT", "/api/settings", json.RawMessage(wire))
	type setup struct {
		id, kind                 string
		participation, proactive bool
	}
	configure := func(s setup) {
		appRequest(t, app, "PUT", "/api/scenes/"+s.id, map[string]any{
			"id": s.id, "name": s.id, "kind": s.kind, "adapterId": "fixture", "accountId": "42",
			"platformSceneId": s.id, "participationEnabled": s.participation,
			"learningEnabled": false, "proactiveEnabled": s.proactive,
		})
	}
	for _, s := range []setup{
		{"private", "private", true, false},
		{"eligible", "group", true, false},
		{"disabled", "group", false, true},
	} {
		configure(s)
		now := time.Now().UTC().Format("2006-01-02T15:04:05.000Z")
		message, err := jsjson.Parse(fmt.Sprintf(`{"id":%q,"sceneId":%q,"senderId":"person","origin":"live","status":"available","sentAt":%q,"receivedAt":%q,"parts":[{"type":"text","text":%q}]}`, s.id+"-message", s.id, now, now, "Proactive fixture "+s.id))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := app.Engine.Runtime.Ingest(ctx, s.id, message.(*jsjson.Object), false); err != nil {
			t.Fatal(err)
		}
	}
	scenes, err := app.proactiveScenes()
	if err != nil || len(scenes) != 3 || scenes[0].ID != "disabled" || scenes[1].ID != "eligible" || scenes[2].ID != "private" {
		t.Fatalf("wrong persisted order: %#v: %v", scenes, err)
	}
	assertActive := func(sceneID string, want int) {
		t.Helper()
		snapshot, err := app.Engine.Runtime.Snapshot(sceneID)
		if err != nil || snapshot.Active != want {
			t.Fatalf("scene %q has %d active turns, want %d: %v", sceneID, snapshot.Active, want, err)
		}
	}
	clock.tick()
	for _, scene := range scenes {
		assertActive(scene.ID, 0)
	}
	configure(setup{"eligible", "group", true, true})
	clock.tick()
	select {
	case valid := <-entered:
		if !valid {
			t.Fatal("model saw wrong turn or wrong scene")
		}
	case <-ctx.Done():
		t.Fatalf("enabled group did not reach Planner: %v", ctx.Err())
	}
	// A second opportunity while the first remains held must not start another
	// turn for its topic. Shutdown stops the clock and cancels the accepted turn.
	clock.tick()
	assertActive("eligible", 1)
	if err := app.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case <-settled:
	case <-ctx.Done():
		t.Fatal("the held model request did not retire")
	}
	if !clock.stopped {
		t.Fatal("timer remains attached after shutdown")
	}
	clock.tick() // Simulate a queued timer callback arriving after Close.
	if len(entered) != 0 || len(settled) != 0 {
		t.Fatal("ineligible, duplicate, or post-shutdown proactive turn")
	}
}
