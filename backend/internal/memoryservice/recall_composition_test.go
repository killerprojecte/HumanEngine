package memoryservice

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/killerprojecte/HumanEngine/backend/internal/botruntime"
	"github.com/killerprojecte/HumanEngine/backend/internal/habitservice"
	"github.com/killerprojecte/HumanEngine/backend/internal/recovery"
	"github.com/killerprojecte/HumanEngine/backend/internal/sourceevents"
	"github.com/mattn/go-sqlite3"
)

type recallCompositionEvent struct {
	Type, Phase string
	Sequence    int64
}
type recallCompositionCase struct {
	Name, Fault, Error                        string
	Events                                    []recallCompositionEvent
	AuthoritySequenceDelta, GateSequenceDelta int64
	Habit                                     struct {
		Status      string
		SourceCount int
	}
}

// This is the real root + existing memory BindRecallAuthority + real habit
// preparation. It compares their ACK/tombstone subsequence with actual enrolled
// Engine.runtime.recall. Meme/experience/learning production wiring remains
// separate; filtering those source phases is explicit, never a full-engine claim.
func TestActualEngineRecallMemoryHabitACKSubsequence(t *testing.T) {
	raw, e := os.ReadFile("../habitservice/testdata/recall-composition.json")
	if e != nil {
		t.Fatal(e)
	}
	var fixture struct{ Cases []recallCompositionCase }
	if e = json.Unmarshal(raw, &fixture); e != nil {
		t.Fatal(e)
	}
	selected := map[string]bool{"success": true, "memory-ack-failure": true, "habit-ack-failure": true, "habit-tombstone-failure": true}
	for _, c := range fixture.Cases {
		if !selected[c.Name] {
			continue
		}
		t.Run(c.Name, func(t *testing.T) {
			ctx := context.Background()
			s, f := serviceSetup(t)
			habits, e := habitservice.New(ctx, f.main, f.recovery, habitservice.Options{Now: func(*habitservice.Service) float64 { return 1793577600000 }})
			if e != nil {
				t.Fatal(e)
			}
			const original = `{"id":"h","kind":"sceneRule","sceneId":"s","content":"Synthetic","evidenceVersion":2,"sources":[{"sceneId":"s","messageId":"m1","senderId":"p","direction":1,"timestamp":1793577600000}],"weight":0.05,"confidence":0.3333333333333333,"status":"active","locked":false,"learnedAt":"2026-11-02T00:00:00.000Z"}`
			if _, e = f.main.Exec("INSERT INTO habits VALUES('h','h',?)", original); e != nil {
				t.Fatal(e)
			}
			phase := "root"
			events := []recallCompositionEvent{}
			record := func(kind string, sequence int64) (int, error) {
				events = append(events, recallCompositionEvent{kind, phase, sequence})
				if c.Fault == phase+":"+kind {
					return 0, errors.New("synthetic_" + phase + "_" + kind)
				}
				return 0, nil
			}
			conn, e := f.main.Conn(ctx)
			if e != nil {
				t.Fatal(e)
			}
			e = conn.Raw(func(raw any) error {
				native := raw.(*sqlite3.SQLiteConn)
				if e := native.RegisterFunc("fixture_ack", func(sequence int64) (int, error) { return record("ack", sequence) }, false); e != nil {
					return e
				}
				return native.RegisterFunc("fixture_tombstone", func() (int, error) { return record("tombstone", 0) }, false)
			})
			conn.Close()
			if e != nil {
				t.Fatal(e)
			}
			_, e = f.main.Exec(`CREATE TRIGGER fixture_gate_ack AFTER UPDATE ON recovery_gate BEGIN SELECT fixture_ack(json_extract(NEW.value,'$.head.sequence')); END;
    CREATE TRIGGER fixture_source_attempt BEFORE INSERT ON recall_tombstones BEGIN SELECT fixture_tombstone(); END;`)
			if e != nil {
				t.Fatal(e)
			}
			before, e := f.recovery.Head(ctx)
			if e != nil {
				t.Fatal(e)
			}
			a, e := sourceevents.NewRuntimeAuthority(f.events)
			if e != nil {
				t.Fatal(e)
			}
			// Retain the real memory binding and its post-root projection completion.
			a = s.BindRecallAuthority(a)
			scene := botruntime.Scene{}
			scene.ID = "s"
			scene.Kind = "group"
			e = a.RecallWithSession(ctx, scene, "m1", func(ws *recovery.WriteSession, reads botruntime.ReadAuthority) (func() error, error) {
				apply, e := habits.RuntimeRecallPreparation()(ctx, ws, reads, scene, "m1")
				if e != nil {
					return nil, e
				}
				return func() error { phase = "habit"; return apply() }, nil
			}, func() error { phase = "memory"; return nil })
			if c.Error == "" && e != nil || c.Error != "" && (e == nil || !strings.Contains(e.Error(), c.Error)) {
				t.Fatalf("recall error %v want %q", e, c.Error)
			}
			expected := []recallCompositionEvent{}
			for _, event := range c.Events {
				if (event.Type == "ack" || event.Type == "tombstone") && (event.Phase == "root" || event.Phase == "memory" || event.Phase == "habit") {
					expected = append(expected, event)
				}
			}
			// Source traces start at head zero. Only ack sequence carries head numbers.
			for i := range events {
				if events[i].Type == "ack" {
					events[i].Sequence -= before.Sequence
				}
			}
			if !reflect.DeepEqual(events, expected) {
				t.Fatalf("ACK/tombstone order\ngot %#v\nwant %#v", events, expected)
			}
			head, e := f.recovery.Head(ctx)
			if e != nil {
				t.Fatal(e)
			}
			gate, e := recovery.ReadGate(ctx, f.main)
			if e != nil {
				t.Fatal(e)
			}
			if head.Sequence-before.Sequence != c.AuthoritySequenceDelta || gate.Head.Sequence-before.Sequence != c.GateSequenceDelta {
				t.Fatalf("head/gate delta %d/%d want %d/%d", head.Sequence-before.Sequence, gate.Head.Sequence-before.Sequence, c.AuthoritySequenceDelta, c.GateSequenceDelta)
			}
			var retained string
			if e = f.main.QueryRow("SELECT value FROM habits WHERE id='h'").Scan(&retained); e != nil {
				t.Fatal(e)
			}
			var actual struct {
				Status  string
				Sources []any
			}
			if e = json.Unmarshal([]byte(retained), &actual); e != nil {
				t.Fatal(e)
			}
			if actual.Status != c.Habit.Status || len(actual.Sources) != c.Habit.SourceCount {
				t.Fatalf("habit state %+v want %+v", actual, c.Habit)
			}
			if c.Error != "" && retained != original {
				t.Fatal("failed root changed persisted habit")
			}
			if c.Error != "" {
				if _, e = habits.AdminList(ctx); e == nil {
					t.Fatal("durable-intent gap allowed publication")
				}
			}
		})
	}
}
