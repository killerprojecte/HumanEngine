package botruntime

import (
	"context"
	"sync"
	"testing"
	"time"
)

func TestSeedIngressCompletionBeforeParticipation(t *testing.T) {
	for _, joined := range []bool{false, true} {
		name := "early_idle_snapshot_schedules_seed"
		if joined {
			name = "joined_ingress_keeps_seed_disabled"
		}
		t.Run(name, func(t *testing.T) {
			r, _, _ := testRuntime(t, ModelFunc(func(context.Context, ModelRequest, func(Completion, error)) {
				t.Error("test clock must not start models")
			}))
			scene := fixtureScene(false)
			scene.ParticipationEnabled = false
			if err := r.RegisterScene(scene); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			entered, release, finished := make(chan struct{}), make(chan struct{}), make(chan error, 1)
			var once sync.Once
			unblock := func() { once.Do(func() { close(release) }) }
			defer unblock()
			r.AddExtension(&Extension{Received: func(m *Object, _ Scene) error {
				if idOf(m) == "seed" {
					close(entered)
					select {
					case <-release:
					case <-ctx.Done():
						return ctx.Err()
					}
				}
				return nil
			}})
			go func() {
				_, err := r.Ingest(ctx, scene.ID, fixtureMessage(scene.ID, "seed", "Synthetic seed."), true)
				finished <- err
			}()
			select {
			case <-entered:
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			snapshot, err := r.Snapshot(scene.ID)
			if err != nil {
				t.Fatal(err)
			}
			if len(snapshot.Messages) != 1 || idOf(snapshot.Messages[0]) != "seed" || snapshot.Active != 0 || snapshot.Pending != 0 || snapshot.Waiting != 0 {
				t.Fatal("old seed gate must be true while Received is still held", snapshot)
			}
			select {
			case err := <-finished:
				t.Fatal("ingress returned before release", err)
			default:
			}
			enable := func() {
				scene.ParticipationEnabled = true
				if err := r.RegisterScene(scene); err != nil {
					t.Fatal(err)
				}
			}
			if !joined {
				enable()
			}
			unblock()
			select {
			case err := <-finished:
				if err != nil {
					t.Fatal(err)
				}
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			if joined {
				enable()
			}
			// The manual clock preserves the exact scheduling decision without
			// sleeps, background model work, or a probabilistic timing assertion.
			s := r.state(scene.ID)
			s.mu.Lock()
			queued, timers := s.queue.Len(), len(s.timers)
			s.mu.Unlock()
			want := 1
			if joined {
				want = 0
			}
			if queued != want || timers != want {
				t.Fatalf("seed scheduled queue=%d timers=%d want=%d", queued, timers, want)
			}
			if joined {
				if _, err := r.Ingest(ctx, scene.ID, fixtureMessage(scene.ID, "fresh", "Synthetic fresh request."), true); err != nil {
					t.Fatal(err)
				}
				s.mu.Lock()
				queued, timers = s.queue.Len(), len(s.timers)
				s.mu.Unlock()
				if queued != 1 || timers != 1 {
					t.Fatalf("fresh enabled ingress did not schedule: queue=%d timers=%d", queued, timers)
				}
			}
		})
	}
}
