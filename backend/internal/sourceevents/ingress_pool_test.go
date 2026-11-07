package sourceevents

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/killerprojecte/HumanEngine/backend/internal/botruntime"
	"github.com/killerprojecte/HumanEngine/backend/internal/dispatch"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
)

type participationPoolProbe struct {
	*RuntimeAuthority
	entered chan struct{}
	once    sync.Once
}

func (a *participationPoolProbe) SceneParticipationEnabled(scene botruntime.Scene) (bool, error) {
	if scene.ID == "scene-a" {
		a.once.Do(func() { close(a.entered) })
	}
	return a.RuntimeAuthority.SceneParticipationEnabled(scene)
}

// A one-connection fixture exposes a lock cycle; it is not a production pool cap
// or a latency benchmark. Recall retains main while derived callbacks inspect
// every scene, so ingress must not retain its owner mutex while acquiring main.
func TestIngressParticipationAndCrossSceneRecallSinglePool(t *testing.T) {
	for _, admission := range []bool{false, true} {
		t.Run(fmt.Sprint(admission), func(t *testing.T) {
			f, base := runtimeFixture(t, Options{})
			runtimePut(t, f.main, "config", "settings", ingressSettings(t, false))
			scenes := []botruntime.Scene{}
			for _, id := range []string{"scene-a", "scene-b"} {
				scenes = append(scenes, botruntime.Scene{SceneRef: dispatch.SceneRef{ID: id, Kind: "group", AdapterID: "fixture", AccountID: "bot", PlatformSceneID: id}, ParticipationEnabled: true})
				runtimePut(t, f.main, "scenes", id, `{"id":"`+id+`","kind":"group","adapterId":"fixture","accountId":"bot","platformSceneId":"`+id+`","participationEnabled":true,"learningEnabled":false,"proactiveEnabled":false}`)
			}
			f.main.SetMaxOpenConns(1)
			probe := &participationPoolProbe{RuntimeAuthority: base, entered: make(chan struct{})}
			r := newSourceRuntime(t, probe)
			cfg := botruntime.DefaultSettings()
			cfg.TopicAdmissionEnabled = admission
			if err := r.Configure(cfg); err != nil {
				t.Fatal(err)
			}
			for _, scene := range scenes {
				if err := r.RegisterScene(scene); err != nil {
					t.Fatal(err)
				}
			}
			received, advanceIngress := make(chan struct{}), make(chan struct{})
			reserved, advanceRecall := make(chan struct{}), make(chan struct{})
			var releaseIngress, releaseRecall sync.Once
			unblockIngress := func() { releaseIngress.Do(func() { close(advanceIngress) }) }
			unblockRecall := func() { releaseRecall.Do(func() { close(advanceRecall) }) }
			defer unblockIngress()
			defer unblockRecall()
			r.AddExtension(&botruntime.Extension{
				Received: func(_ *jsjson.Object, scene botruntime.Scene) error {
					if scene.ID == "scene-a" {
						close(received)
						<-advanceIngress
					}
					return nil
				},
				PrepareRecallScoped: func(_ botruntime.ReadAuthority, scene, id string) (func() error, error) {
					if scene == "scene-b" {
						close(reserved)
						<-advanceRecall
					}
					return nil, nil
				},
			})
			ingressDone, recallDone := make(chan error, 1), make(chan error, 1)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			message := runtimeObject(t, `{"id":"source","sceneId":"scene-a","senderId":"user","origin":"live","status":"available","sentAt":"2026-10-28T00:00:00.000Z","receivedAt":"2026-10-28T00:00:00.000Z","parts":[{"type":"text","text":"Synthetic."}]}`)
			go func() { _, err := r.IngestScene(ctx, scenes[0], message, true); ingressDone <- err }()
			wait := func(ch <-chan struct{}, label string) {
				t.Helper()
				select {
				case <-ch:
				case <-time.After(5 * time.Second):
					cancel()
					f.main.SetMaxOpenConns(2)
					unblockIngress()
					unblockRecall()
					t.Fatalf("did not reach %s", label)
				}
			}
			wait(received, "received callback")
			go func() { recallDone <- r.Recall(ctx, "scene-b", "unrelated") }()
			wait(reserved, "recall main reservation")
			unblockIngress()
			wait(probe.entered, "persisted participation read")
			unblockRecall()
			for _, done := range []<-chan error{recallDone, ingressDone} {
				select {
				case err := <-done:
					if err != nil {
						t.Fatal(err)
					}
				case <-time.After(5 * time.Second):
					// Release an old-regression deadlock so fixture cleanup cannot hang CI.
					f.main.SetMaxOpenConns(2)
					cancel()
					select {
					case <-done:
					case <-time.After(5 * time.Second):
					}
					t.Fatal("owner/pool lock cycle prevented completion")
				}
			}
		})
	}
}
