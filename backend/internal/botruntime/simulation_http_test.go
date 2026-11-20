package botruntime

import (
	"context"
	"sync"
	"testing"
	"time"
)

func TestSimulationEndKeepsBoundPostOutOfReplacement(t *testing.T) {
	r, a, _ := testRuntime(t, ModelFunc(func(context.Context, ModelRequest, func(Completion, error)) {
		t.Error("closed simulation started a model turn")
	}))
	id, err := r.CreateSimulation()
	if err != nil {
		t.Fatal(err)
	}
	real := fixtureScene(false)
	real.ID = id
	if _, err := r.IngestScene(context.Background(), real, fixtureMessage(id, "real-intrusion", "synthetic"), false); err != errSimulationSceneCollision {
		t.Fatalf("real ingress changed simulation binding: %v", err)
	}
	received, release := make(chan struct{}), make(chan struct{})
	t.Cleanup(func() {
		select {
		case <-release:
		default:
			close(release)
		}
	})
	detach := r.AddExtension(&Extension{Received: func(_ *Object, scene Scene) error {
		if scene.Kind == "simulation" {
			close(received)
			<-release
		}
		return nil
	}})
	defer detach()
	result := make(chan error, 1)
	go func() {
		_, err := r.Simulate(context.Background(), id, "bound to an old simulation")
		result <- err
	}()
	select {
	case <-received:
	case <-time.After(2 * time.Second):
		t.Fatal("simulation did not reach bound ingress")
	}
	r.EndSimulation(id)
	if err := r.RegisterScene(real); err != nil {
		t.Fatal(err)
	}
	close(release)
	select {
	case err := <-result:
		if err == nil || err.Error() != "scene_closed" {
			t.Fatalf("ended simulation ran against replacement: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("ended simulation did not release")
	}
	r.EndSimulation(id)
	if scene, ok := r.RegisteredScene(id); !ok || scene.Kind != real.Kind {
		t.Fatal("simulation delete removed replacement")
	}
	view, err := r.Snapshot(id)
	if err != nil || len(view.Messages) != 0 || view.Active != 0 || a.writes != 0 {
		t.Fatalf("in-flight simulation entered real state: %+v, writes=%d, err=%v", view, a.writes, err)
	}
}

func TestConcurrentSimulationCreationKeepsExistingFIFOSize(t *testing.T) {
	r, _, _ := testRuntime(t, ModelFunc(func(context.Context, ModelRequest, func(Completion, error)) {
		t.Error("creating a simulation started a model turn")
	}))
	oldest, err := r.CreateSimulation()
	if err != nil {
		t.Fatal(err)
	}
	var creators sync.WaitGroup
	for range 80 {
		creators.Go(func() {
			if _, err := r.CreateSimulation(); err != nil {
				t.Error(err)
			}
		})
	}
	creators.Wait()
	active := 0
	r.scenes.Range(func(_, value any) bool {
		if value.(*sceneState).view.Load().scene.Kind == "simulation" {
			active++
		}
		return true
	})
	if active != 64 || r.state(oldest) != nil {
		t.Fatalf("FIFO cap: active=%d, oldest remains=%v", active, r.state(oldest) != nil)
	}
}
