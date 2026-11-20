package botruntime

import (
	"context"
	"testing"
)

func TestSceneStateUsesRetainedAvailableMetadata(t *testing.T) {
	r, _, clock := testRuntime(t, ModelFunc(func(context.Context, ModelRequest, func(Completion, error)) { t.Fatal("state read used a model") }))
	scene := fixtureScene(false)
	if err := r.RegisterScene(scene); err != nil {
		t.Fatal(err)
	}
	empty := r.SceneState(scene.ID)
	if empty.Topics == nil || len(empty.Topics) != 0 || empty.SecondsSinceBotSpoke != nil || empty.TopicRouting.MaxTopics != 4 || empty.TopicRouting.MaxDeferred != 60 {
		t.Fatalf("empty state: %+v", empty)
	}
	if _, err := r.Ingest(context.Background(), scene.ID, fixtureMessage(scene.ID, "user", "hello"), false); err != nil {
		t.Fatal(err)
	}
	s := r.state(scene.ID)
	s.mu.Lock()
	bot := fixtureMessage(scene.ID, "reply", "hi")
	_ = bot.Set("senderId", "bot")
	r.appendMessageLocked(s, bot)
	s.routing.Overflowed = 2
	s.topics.Set("fixture-topic", &topic{id: "fixture-topic", messageIDs: []string{"user", "reply", "missing"}, attention: 2, lastAt: clock.Now()})
	r.publishLocked(s)
	s.mu.Unlock()
	clock.Advance(1600)
	got := r.SceneState(scene.ID)
	if len(got.Topics) == 0 {
		t.Fatal("expected retained topic")
	}
	last := got.Topics[len(got.Topics)-1]
	if got.TopicRouting.Overflowed != 2 || got.SecondsSinceBotSpoke == nil || *got.SecondsSinceBotSpoke != 2 || last.ID != "fixture-topic" || last.MessageCount != 2 || last.AgeMs != 1600 || last.Attention <= 0 || last.Attention >= 2 {
		t.Fatalf("state: %+v", got)
	}
	clock.mu.Lock()
	clock.now += r.lifetime(r.config()) + 1
	clock.mu.Unlock()
	got = r.SceneState(scene.ID)
	if got.SecondsSinceBotSpoke != nil || len(got.Topics) == 0 || got.Topics[len(got.Topics)-1].MessageCount != 0 {
		t.Fatalf("expired state: %+v", got)
	}
}

func TestSceneStateTrimsExpiredSimulatedBodies(t *testing.T) {
	r, _, clock := testRuntime(t, ModelFunc(func(context.Context, ModelRequest, func(Completion, error)) { t.Fatal("state read used a model") }))
	scene := fixtureScene(true)
	if err := r.RegisterScene(scene); err != nil {
		t.Fatal(err)
	}
	s := r.state(scene.ID)
	s.mu.Lock()
	bot := fixtureMessage(scene.ID, "reply", "hi")
	_ = bot.Set("senderId", "bot")
	r.appendMessageLocked(s, bot)
	r.publishLocked(s)
	s.mu.Unlock()
	if r.SceneState(scene.ID).SecondsSinceBotSpoke == nil {
		t.Fatal("missing live bot time")
	}
	clock.mu.Lock()
	clock.now += r.lifetime(r.config()) + 1
	clock.mu.Unlock()
	if r.SceneState(scene.ID).SecondsSinceBotSpoke != nil || s.view.Load().history != nil {
		t.Fatal("expired simulated body retained")
	}
}
