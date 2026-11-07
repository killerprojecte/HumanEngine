package botruntime

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"
)

func TestIngressBlockedRoutingCannotResurrectClosedRuntime(t *testing.T) {
	r, base, _ := testRuntime(t, ModelFunc(func(context.Context, ModelRequest, func(Completion, error)) {}))
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	defer unblock()
	r.services.Authority = &ingressAuthority{testAuthority: base, route: func(Scene, *Object) error {
		close(entered)
		<-release
		return nil
	}, recall: func(Scene, string) (bool, error) { return false, nil }}
	scene := fixtureScene(false)
	finished := make(chan error, 1)
	go func() {
		_, err := r.IngestScene(context.Background(), scene, fixtureMessage(scene.ID, "source", "Synthetic"), false)
		finished <- err
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("routing did not start")
	}
	closed := make(chan struct{})
	go func() { r.Close(); close(closed) }()
	select {
	case <-closed:
	case <-time.After(5 * time.Second):
		t.Fatal("Close held behind routing IO")
	}
	unblock()
	select {
	case err := <-finished:
		if err == nil || err.Error() != "runtime_closed" {
			t.Fatalf("error=%v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("ingress did not finish")
	}
	if r.state(scene.ID) != nil {
		t.Fatal("owner resurrected after Close")
	}
	if _, ok := r.RegisteredScene(scene.ID); ok {
		t.Fatal("registration resurrected after Close")
	}
}

// The race detector exercises atomic-pointer publication while an existing
// scene's true feature flags are merged into incoming false transport flags.
func TestIngressRegistrationSnapshotsRemainImmutable(t *testing.T) {
	r, _, _ := testRuntime(t, ModelFunc(func(context.Context, ModelRequest, func(Completion, error)) {}))
	scene := fixtureScene(false)
	if err := r.RegisterScene(scene); err != nil {
		t.Fatal(err)
	}
	stop, done := make(chan struct{}), make(chan struct{})
	invalid := make(chan struct{}, 1)
	go func() {
		defer close(done)
		for {
			select {
			case <-stop:
				return
			default:
				registered, ok := r.RegisteredScene(scene.ID)
				if ok && (!registered.ParticipationEnabled || !registered.LearningEnabled || !registered.ProactiveEnabled || registered.ID != scene.ID) {
					invalid <- struct{}{}
					return
				}
			}
		}
	}()
	defer func() {
		close(stop)
		<-done
		select {
		case <-invalid:
			t.Error("reader observed mutable/unmerged registration")
		default:
		}
	}()
	incoming := scene
	incoming.ParticipationEnabled = false
	incoming.LearningEnabled = false
	incoming.ProactiveEnabled = false
	for i := 0; i < 200; i++ {
		m := fixtureMessage(scene.ID, fmt.Sprint(i), "Synthetic")
		_ = m.Set("senderId", "bot")
		if _, err := r.IngestScene(context.Background(), incoming, m, false); err != nil {
			t.Fatal(err)
		}
		registered, ok := r.RegisteredScene(scene.ID)
		if !ok || !registered.ParticipationEnabled || !registered.LearningEnabled || !registered.ProactiveEnabled {
			t.Fatal("registration did not preserve merged immutable flags")
		}
	}
}

type participationReadAuthority struct {
	*testAuthority
	read func(Scene) (bool, error)
}

func (a *participationReadAuthority) SceneParticipationEnabled(s Scene) (bool, error) {
	return a.read(s)
}

func TestIngressParticipationReadDoesNotHoldOwnerLock(t *testing.T) {
	for _, admission := range []bool{false, true} {
		t.Run(fmt.Sprint(admission), func(t *testing.T) {
			r, base, _ := testRuntime(t, ModelFunc(func(context.Context, ModelRequest, func(Completion, error)) {}))
			cfg := r.config()
			cfg.TopicAdmissionEnabled = admission
			r.settings.Store(&cfg)
			scene := fixtureScene(false)
			received, reads := false, 0
			r.AddExtension(&Extension{Received: func(*Object, Scene) error { received = true; return nil }})
			r.services.Authority = &participationReadAuthority{testAuthority: base, read: func(s Scene) (bool, error) {
				reads++
				owner := r.state(s.ID)
				if !owner.mu.TryLock() {
					return false, fmt.Errorf("participation read held owner lock")
				}
				defer owner.mu.Unlock()
				if !received {
					return false, fmt.Errorf("participation read before received callback")
				}
				if reads == 1 && ((owner.topics.Len() == 0) != admission) {
					return false, fmt.Errorf("participation read at wrong topic bookkeeping stage")
				}
				return false, nil
			}}
			if _, err := r.IngestScene(context.Background(), scene, fixtureMessage(scene.ID, "source", "Synthetic"), true); err != nil {
				t.Fatal(err)
			}
			if reads == 0 {
				t.Fatal("participation not read")
			}
		})
	}
}
