package botruntime

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"os"
	"sync"
	"testing"
	"time"
)

func TestExactPreviewActualSourceFixtures(t *testing.T) {
	raw, e := os.ReadFile("../experienceservice/testdata/runtime-source.json")
	if e != nil {
		t.Fatal(e)
	}
	var data struct {
		Previews []struct {
			Name                                            string
			N, Index                                        int
			Duplicate, Expired, Disabled, Timer, Simulation bool
			Result                                          json.RawMessage
		}
	}
	if e = json.Unmarshal(raw, &data); e != nil {
		t.Fatal(e)
	}
	for _, tc := range data.Previews {
		t.Run(tc.Name, func(t *testing.T) {
			r, a, clock := testRuntime(t, ModelFunc(func(context.Context, ModelRequest, func(Completion, error)) { t.Fatal("model invoked") }))
			clock.now = 1793232000000
			cfg := r.config()
			cfg.CacheEnabled = !tc.Disabled
			cfg.CacheHours = 1
			if e := r.Configure(cfg); e != nil {
				t.Fatal(e)
			}
			scene := fixtureScene(tc.Simulation)
			scene.ID = "s"
			if tc.Simulation {
				scene.ID = "sim:s"
			}
			if e := r.RegisterScene(scene); e != nil {
				t.Fatal(e)
			}
			s := r.state(scene.ID)
			s.mu.Lock()
			for i := 0; i < tc.N; i++ {
				id := fmt.Sprint(i)
				if tc.Duplicate {
					id = "same"
				}
				at := clock.now
				if tc.Expired {
					at -= 3600001
				}
				origin := "live"
				if tc.Simulation {
					origin = "simulation"
				}
				r.appendMessageLocked(s, obj("id", id, "sceneId", scene.ID, "senderId", "alice", "origin", origin, "status", "available", "receivedAt", iso(at), "parts", []any{obj("type", "text", "text", fmt.Sprint(i))}))
			}
			if tc.Timer {
				s.timers["x"] = &timerTicket{timer: &testTimer{clock: clock}}
			}
			r.publishLocked(s)
			// Keep the scene lock held throughout preview: even a slow owner cannot block
			// a scoped SQL proof. The test timeout is diagnostic, not a runtime limit.
			id := fmt.Sprint(tc.Index)
			if tc.Duplicate {
				id = "same"
			}
			done := make(chan error, 1)
			go func() {
				value, e := r.PreviewMessage(scene.ID, id, a)
				if e == nil {
					actual, _ := jsjson.Marshal(value)
					var av, bv any
					_ = json.Unmarshal(actual, &av)
					_ = json.Unmarshal(tc.Result, &bv)
					if !deepEqualFixture(av, bv) {
						e = fmt.Errorf("preview got %s want %s", actual, tc.Result)
					}
				}
				done <- e
			}()
			select {
			case e := <-done:
				s.mu.Unlock()
				if e != nil {
					t.Fatal(e)
				}
			case <-time.After(time.Second):
				s.mu.Unlock()
				t.Fatal("preview entered scene owner")
			}
		})
	}
}
func deepEqualFixture(a, b any) bool {
	aa, _ := json.Marshal(a)
	bb, _ := json.Marshal(b)
	return string(aa) == string(bb)
}
func TestExactPreviewWrongSceneFirstDuplicate(t *testing.T) {
	h := appendRecord(nil, &messageRecord{body: obj("id", "same", "sceneId", "foreign"), receivedAt: 1})
	h = appendRecord(h, &messageRecord{body: obj("id", "same", "sceneId", "s"), receivedAt: 1})
	if get(retainedMessage(h, "same", false, 0), "sceneId") != "foreign" {
		t.Fatal("skipped first ID before scene check")
	}
}

func TestExactPreviewTimerRemovalPublication(t *testing.T) {
	r, a, clock := testRuntime(t, ModelFunc(func(context.Context, ModelRequest, func(Completion, error)) { t.Fatal("unexpected model") }))
	cfg := r.config()
	cfg.CacheEnabled = false
	if e := r.Configure(cfg); e != nil {
		t.Fatal(e)
	}
	scene := fixtureScene(false)
	if e := r.RegisterScene(scene); e != nil {
		t.Fatal(e)
	}
	s := r.state(scene.ID)
	ticket := &timerTicket{timer: &testTimer{clock: clock}}
	s.mu.Lock()
	r.appendMessageLocked(s, fixtureMessage(scene.ID, "retained", "Synthetic"))
	s.timers["missing-admission"] = ticket
	r.publishLocked(s)
	s.mu.Unlock()
	r.wake(s, "missing-admission", ticket)
	if s.view.Load().timers != 0 {
		t.Fatal("removed timer remained published")
	}
	if body, e := r.PreviewMessage(scene.ID, "retained", a); e != nil || body != nil {
		t.Fatalf("idle cache-disabled preview retained a body: %v %v", body, e)
	}
}

func TestExactPreviewIrreversibleSourceSequences(t *testing.T) {
	raw, e := os.ReadFile("../experienceservice/testdata/runtime-source.json")
	if e != nil {
		t.Fatal(e)
	}
	var fixture struct {
		RetentionSequences []struct {
			Name                string
			Removed, Expanded   bool
			Previous            any
			EditStatus, NewText *string
		}
	}
	if e = json.Unmarshal(raw, &fixture); e != nil {
		t.Fatal(e)
	}
	if len(fixture.RetentionSequences) != 3 {
		t.Fatal("missing actual Runtime two-step oracle")
	}
	for _, tc := range fixture.RetentionSequences {
		t.Run(tc.Name, func(t *testing.T) {
			r, a, clock := testRuntime(t, ModelFunc(func(context.Context, ModelRequest, func(Completion, error)) { t.Fatal("unexpected model") }))
			cfg := r.config()
			cfg.CacheHours = 1
			cfg.CacheEnabled = tc.Name != "idle-reenable"
			if e := r.Configure(cfg); e != nil {
				t.Fatal(e)
			}
			scene := fixtureScene(false)
			if e := r.RegisterScene(scene); e != nil {
				t.Fatal(e)
			}
			a.enrolled = true
			old := fixtureMessage(scene.ID, "m", "old")
			if _, e := r.Ingest(context.Background(), scene.ID, old, false); e != nil {
				t.Fatal(e)
			}
			if tc.Name != "idle-reenable" {
				clock.mu.Lock()
				clock.now += 3600001
				clock.mu.Unlock()
			}
			removed, e := r.PreviewMessage(scene.ID, "m", a)
			if e != nil || (removed != nil) != tc.Removed {
				t.Fatalf("first preview %v %v", removed, e)
			}
			cfg.CacheEnabled = true
			cfg.CacheHours = 4
			if e := r.Configure(cfg); e != nil {
				t.Fatal(e)
			}
			expanded, e := r.PreviewMessage(scene.ID, "m", a)
			if e != nil || (expanded != nil) != tc.Expanded {
				t.Fatalf("resurrected removed body %v %v", expanded, e)
			}
			// Snapshot used to repeat the same reversible TTL-only filter. It now shares
			// the irreversible publication boundary rather than reviving removed rows.
			snapshot, e := r.Snapshot(scene.ID)
			if e != nil || len(snapshot.Messages) != 0 {
				t.Fatalf("snapshot resurrection %v %v", snapshot.Messages, e)
			}
			if tc.EditStatus != nil {
				previous := "not-called"
				r.AddExtension(&Extension{Edited: func(before, after *Object, _ Scene) error {
					if before == nil {
						previous = "absent"
					} else {
						previous = idOf(before)
					}
					return nil
				}})
				next := fixtureMessage(scene.ID, "m", "new")
				_ = next.Set("receivedAt", iso(clock.Now()))
				_ = next.Set("sentAt", iso(clock.Now()))
				status, e := r.Edit(context.Background(), scene.ID, next, obj("editId", "edit-1", "order", obj("kind", "platform_sequence", "sequence", float64(1))), false)
				if e != nil || status != *tc.EditStatus {
					t.Fatal(status, e)
				}
				got, e := r.PreviewMessage(scene.ID, "m", a)
				if e != nil || got == nil || previous != "absent" {
					t.Fatalf("actual edit re-admission %v %s %v", got, previous, e)
				}
				if text := get(array(get(got, "parts"))[0], "text"); text != *tc.NewText {
					t.Fatal(text)
				}
			}
		})
	}
}
func TestExactPreviewSnapshotAlsoCommitsExpiry(t *testing.T) {
	r, a, clock := testRuntime(t, ModelFunc(func(context.Context, ModelRequest, func(Completion, error)) { t.Fatal("unexpected model") }))
	cfg := r.config()
	cfg.CacheEnabled = true
	cfg.CacheHours = 1
	_ = r.Configure(cfg)
	scene := fixtureScene(false)
	_ = r.RegisterScene(scene)
	_, e := r.Ingest(context.Background(), scene.ID, fixtureMessage(scene.ID, "m", "old"), false)
	if e != nil {
		t.Fatal(e)
	}
	clock.mu.Lock()
	clock.now += 3600001
	clock.mu.Unlock()
	if snap, e := r.Snapshot(scene.ID); e != nil || len(snap.Messages) != 0 {
		t.Fatal(snap, e)
	}
	cfg.CacheHours = 4
	_ = r.Configure(cfg)
	if got, e := r.PreviewMessage(scene.ID, "m", a); e != nil || got != nil {
		t.Fatalf("snapshot expiry undone %v %v", got, e)
	}
}
func TestExactPreviewConcurrentOwnerReconciliation(t *testing.T) {
	r, a, clock := testRuntime(t, ModelFunc(func(context.Context, ModelRequest, func(Completion, error)) { t.Fatal("unexpected model") }))
	cfg := r.config()
	cfg.CacheEnabled = true
	cfg.CacheHours = 1
	_ = r.Configure(cfg)
	scene := fixtureScene(false)
	_ = r.RegisterScene(scene)
	_, e := r.Ingest(context.Background(), scene.ID, fixtureMessage(scene.ID, "expired", "old"), false)
	if e != nil {
		t.Fatal(e)
	}
	clock.mu.Lock()
	clock.now += 3600001
	clock.mu.Unlock()
	// Reader commits removal while an existing actor owns the scene. A body-only
	// replacement of the old membership must share that retention identity.
	s := r.state(scene.ID)
	s.mu.Lock()
	old := s.byID["expired"].body
	done := make(chan error, 1)
	go func() { _, e := r.PreviewMessage(scene.ID, "expired", a); done <- e }()
	if e := <-done; e != nil {
		s.mu.Unlock()
		t.Fatal(e)
	}
	replacement := cloneJSON(old).(*Object)
	_ = replacement.Set("status", "recalled")
	r.replaceRecordLocked(s, old, replacement)
	for i, m := range s.messages {
		if m == old {
			s.messages[i] = replacement
		}
	}
	r.rebuildMessagesLocked(s)
	r.publishLocked(s)
	s.mu.Unlock()
	if got, e := r.PreviewMessage(scene.ID, "expired", a); e != nil || got != nil {
		t.Fatalf("body replacement revived membership %v %v", got, e)
	}
	var wg sync.WaitGroup
	errs := make(chan error, 200)
	for worker := 0; worker < 4; worker++ {
		worker := worker
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 20; i++ {
				switch worker {
				case 0:
					_, e := r.PreviewMessage(scene.ID, "expired", a)
					if e != nil {
						errs <- e
					}
				case 1:
					c := cfg
					c.CacheHours = 4
					if e := r.Configure(c); e != nil {
						errs <- e
					}
				case 2:
					m := fixtureMessage(scene.ID, fmt.Sprintf("new-%d", i), "fresh")
					_ = m.Set("receivedAt", iso(clock.Now()))
					if _, e := r.Ingest(context.Background(), scene.ID, m, false); e != nil {
						errs <- e
					}
				case 3:
					m := fixtureMessage(scene.ID, fmt.Sprintf("new-%d", i), "edited")
					_ = m.Set("receivedAt", iso(clock.Now()))
					if _, e := r.Edit(context.Background(), scene.ID, m, obj(), false); e != nil {
						errs <- e
					}
				}
			}
		}()
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		t.Error(e)
	}
	if snap, e := r.Snapshot(scene.ID); e != nil {
		t.Fatal(e)
	} else {
		for _, m := range snap.Messages {
			if idOf(m) == "expired" {
				t.Fatal("concurrent owner publication revived body")
			}
		}
	}
}
