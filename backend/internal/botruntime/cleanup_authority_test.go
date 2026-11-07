package botruntime

import (
	"context"
	"encoding/json"
	"os"
	"testing"

	"github.com/killerprojecte/HumanEngine/backend/internal/turnwait"
)

func TestActualSourceCleanupFailureState(t *testing.T) {
	raw, err := os.ReadFile("../sourceevents/testdata/runtime-authority-faults.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		CleanupCases []struct {
			Code                   string
			Armed                  bool
			RunError               *string
			Tasks, Pending, Timers int
			WakeCurrent            bool
			Retained               turnwait.Retention
		}
	}
	if err = json.Unmarshal(raw, &fixture); err != nil {
		t.Fatal(err)
	}
	if len(fixture.CleanupCases) != 2 {
		t.Fatal("missing actual source cleanup cases")
	}
	for _, tc := range fixture.CleanupCases {
		t.Run(tc.Code, func(t *testing.T) {
			r, a, clock := testRuntime(t, ModelFunc(func(context.Context, ModelRequest, func(Completion, error)) { t.Error("unexpected model") }))
			scene := fixtureScene(false)
			if err := r.RegisterScene(scene); err != nil {
				t.Fatal(err)
			}
			topic, err := r.Ingest(context.Background(), scene.ID, fixtureMessage(scene.ID, "source", "Synthetic."), false)
			if err != nil {
				t.Fatal(err)
			}
			admission, err := turnwait.New(turnwait.Config{QuietMS: 0, MaxWaitMS: 100, AddressedQuietMS: 0, MaxModelWaitMS: 100, MaxPending: 8}, clock.Now)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = admission.Observe(turnwait.Event{ID: "source", Meaningful: true}); err != nil {
				t.Fatal(err)
			}
			wake := admission.TakeReady()
			if wake == nil {
				t.Fatal("missing wake")
			}
			s := r.state(scene.ID)
			s.mu.Lock()
			s.admissions[topic] = admission
			turn, err := r.startLocked(s, topic, false, wake)
			if err != nil {
				s.mu.Unlock()
				t.Fatal(err)
			}
			s.pending[topic] = true
			s.timers[topic] = &timerTicket{timer: clock.AfterFunc(60000, func() { t.Error("unexpected timer") })}
			turn.guard = &sendGuard{}
			s.mu.Unlock()
			a.mu.Lock()
			a.readyErr = code(tc.Code)
			a.mu.Unlock()
			err = r.cleanupTurn(turn)
			if !tc.Armed {
				t.Fatal("source fault not armed")
			}
			if (err != nil) != (tc.RunError != nil) {
				t.Fatal(err, tc.RunError)
			}
			if err != nil && err.Error() != *tc.RunError {
				t.Fatal(err, *tc.RunError)
			}
			s.mu.Lock()
			tasks, pending, timers := len(s.tasks), len(s.pending), len(s.timers)
			retained := admission.Retention()
			current := admission.IsCurrent(wake)
			_, routing := s.routing.Turns.Get(turn.context.TurnID)
			s.mu.Unlock()
			if tasks != tc.Tasks || pending != tc.Pending || timers != tc.Timers || current != tc.WakeCurrent || retained != tc.Retained {
				t.Fatalf("state %d/%d/%d current=%v retained=%+v expected=%+v", tasks, pending, timers, current, retained, tc)
			}
			if turn.guard != nil || routing || !turn.context.root.Aborted() {
				t.Fatal("owner cleanup incomplete")
			}
		})
	}
}
