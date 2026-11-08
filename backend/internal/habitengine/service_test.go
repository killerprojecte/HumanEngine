package habitengine

import (
	"context"
	"github.com/killerprojecte/HumanEngine/backend/internal/contract"
	"os"
	"testing"

	"github.com/killerprojecte/HumanEngine/backend/internal/botruntime"
	"github.com/killerprojecte/HumanEngine/backend/internal/dispatch"
	"github.com/killerprojecte/HumanEngine/backend/internal/habitservice"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsdate"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/scenestore"
)

func engineFixture(t *testing.T) (habitFixture, *Service) {
	t.Helper()
	f := newHabitFixture(t)
	ctx := context.Background()
	h, e := habitservice.New(ctx, f.main, f.authority, habitservice.Options{Now: func(*habitservice.Service) float64 { return 1793577600000 }, ID: func() (string, error) { return "h", nil }})
	if e != nil {
		t.Fatal(e)
	}
	d, e := jsdate.New("UTC")
	if e != nil {
		t.Fatal(e)
	}
	s, e := New(h, f.events, d)
	if e != nil {
		t.Fatal(e)
	}
	return f, s
}
func turn() Turn {
	return Turn{Scene: botruntime.Scene{SceneRef: dispatch.SceneRef{ID: "s", Kind: "group", AdapterID: "synthetic", AccountID: "account", PlatformSceneID: "s"}}}
}
func TestActualEngineProposalCaptures(t *testing.T) {
	raw, e := os.ReadFile("testdata/source.json")
	if e != nil {
		t.Fatal(e)
	}
	v, e := jsjson.Parse(string(raw))
	if e != nil {
		t.Fatal(e)
	}
	for _, value := range get(v.(*Object), "cases").([]any) {
		c := value.(*Object)
		t.Run(str(c, "name"), func(t *testing.T) {
			_, s := engineFixture(t)
			input := get(c, "input").(*Object)
			tt := turn()
			tt.Simulation, _ = get(c, "simulation").(bool)
			for _, m := range get(c, "messages").([]any) {
				tt.Preview = append(tt.Preview, m.(*Object))
			}
			if tt.Simulation {
				r, e := s.Propose(context.Background(), tt, input)
				if e != nil || str(r, "reason") != "simulation_does_not_learn" {
					t.Fatalf("%v %v", r, e)
				}
				return
			}
			id, e := proposal(input)
			if get(c, "error") != nil {
				if e == nil {
					t.Fatal("source schema rejection missing")
				}
				return
			}
			if e != nil {
				t.Fatal(e)
			}
			rows, e := messages(tt)
			if e != nil {
				t.Fatal(e)
			}
			ev, e := s.evidence(tt, rows)
			if e != nil {
				t.Fatal(e)
			}
			captured := get(c, "capture").(*Object)
			want := get(captured, "context")
			actual := object("sceneId", "s", "simulation", false, "sourceEvidence", ev)
			a, _ := jsjson.Stringify(actual)
			b, _ := jsjson.Stringify(want)
			if a != b {
				t.Fatalf("context\ngot %s\nwant %s", a, b)
			}
			src := sources("s", id, rows)
			candidate := object("kind", "sceneRule", "content", get(input, "content"), "sources", src)
			if str(input, "kind") == "botExpression" {
				candidate = object("kind", "botExpression", "preference", get(input, "preference"), "direction", get(input, "direction"), "sources", src)
			}
			a, _ = jsjson.Stringify(candidate)
			b, _ = jsjson.Stringify(get(captured, "candidate"))
			if a != b {
				t.Fatalf("sources %s != %s", a, b)
			}
		})
	}
}
func TestNativeProposalFeedbackAndSnapshot(t *testing.T) {
	f, s := engineFixture(t)
	ctx := context.Background()
	scene := mustHabitObject(t, `{"id":"s","kind":"group","adapterId":"synthetic","accountId":"account","platformSceneId":"s","name":"Synthetic","participationEnabled":true,"learningEnabled":true,"proactiveEnabled":false}`)
	ss := scenestore.Store{Main: f.main, Authority: f.authority}
	if _, e := ss.Save(ctx, scene); e != nil {
		t.Fatal(e)
	}
	m := mustHabitObject(t, `{"id":"m","sceneId":"s","senderId":"p","origin":"live","status":"available","sentAt":"2026-11-02T00:00:00.000Z","parts":[{"type":"text","text":"assistant always use concise replies"}]}`)
	tt := turn()
	tt.Preview = []*Object{m}
	input := object("kind", "botExpression", "preference", "concise", "direction", float64(1), "sourceMessageIds", []any{"m"})
	r, e := s.ToolPropose(ctx, tt, input)
	if e != nil || str(r, "status") != "saved" {
		t.Fatalf("proposal %v %v", r, e)
	}
	var count int
	if e = f.main.QueryRow("SELECT count(*) FROM habits").Scan(&count); e != nil || count != 1 {
		t.Fatalf("persist %d %v", count, e)
	}
	// Real enrollment makes a preview fallback inadmissible; captured current ref
	// remains usable. The service does not manufacture a source snapshot.
	if _, e = f.main.Exec("INSERT INTO config VALUES('settings',?)", enabledSettings(t)); e != nil {
		t.Fatal(e)
	}
	_, trusted, e := s.authority.Observe(ctx, tt.Scene, m, nil)
	if e != nil {
		t.Fatal(e)
	}
	if trusted == nil {
		t.Fatal("missing real source ref")
	}
	r, e = s.ToolPropose(ctx, tt, input)
	if e != nil || str(r, "reason") != "source_snapshot_unavailable" {
		t.Fatalf("preview %v %v", r, e)
	}
	tt.SourceSnapshots = []*Object{trusted}
	r, e = s.ToolFeedback(ctx, tt, object("id", "h", "direction", float64(1), "sourceMessageIds", []any{"m"}))
	if e != nil {
		t.Fatal(e)
	}
	if r == nil {
		t.Fatal("missing feedback result")
	}
	tt.SourceSnapshots = []*Object{}
	r, e = s.ToolPropose(ctx, tt, input)
	if e != nil || str(r, "status") != "rejected" {
		t.Fatalf("empty capture %v %v", r, e)
	}
}

func enabledSettings(t *testing.T) string {
	t.Helper()
	v, e := jsjson.Parse(string(contract.DefaultSettingsInput()))
	if e != nil {
		t.Fatal(e)
	}
	o := v.(*Object)
	_ = o.Set("sourceEventRevisionsEnabled", true)
	raw, e := jsjson.Stringify(o)
	if e != nil {
		t.Fatal(e)
	}
	return raw
}
func TestSimulationCapturedSnapshotNeverTouchesUnavailableDB(t *testing.T) {
	f, s := engineFixture(t)
	if e := f.main.Close(); e != nil {
		t.Fatal(e)
	}
	if e := f.authority.Close(); e != nil {
		t.Fatal(e)
	}
	tt := turn()
	tt.Simulation = true
	tt.SourceSnapshots = []*Object{}
	input := object("kind", "sceneRule", "content", "x", "sourceMessageIds", []any{"m"})
	r, e := s.ToolPropose(context.Background(), tt, input)
	if e != nil || str(r, "reason") != "simulation_does_not_learn" {
		t.Fatalf("captured simulation %v %v", r, e)
	}
	if _, e = s.ToolPropose(context.Background(), tt, object("kind", "invalid")); e == nil {
		t.Fatal("schema must precede simulation")
	}
	tt.SourceSnapshots = nil
	if _, e = s.ToolPropose(context.Background(), tt, input); e == nil {
		t.Fatal("nil snapshot must still read registration before simulation")
	}
}

func TestActualToolBranchCaptures(t *testing.T) {
	raw, e := os.ReadFile("testdata/source.json")
	if e != nil {
		t.Fatal(e)
	}
	value, e := jsjson.Parse(string(raw))
	if e != nil {
		t.Fatal(e)
	}
	for _, v := range get(value.(*Object), "tools").([]any) {
		c := v.(*Object)
		t.Run(str(c, "name"), func(t *testing.T) {
			_, s := engineFixture(t)
			tt := turn()
			tt.Simulation, _ = get(c, "simulation").(bool)
			input := get(c, "input").(*Object)
			if a, ok := get(c, "snapshots").([]any); ok {
				tt.SourceSnapshots = []*Object{}
				for _, m := range a {
					tt.SourceSnapshots = append(tt.SourceSnapshots, m.(*Object))
				}
			}
			for _, m := range get(c, "preview").([]any) {
				tt.Preview = append(tt.Preview, m.(*Object))
			}
			if get(c, "capture") == nil {
				var result *Object
				var err error
				if str(c, "tool") == "habits.propose" {
					result, err = s.ToolPropose(context.Background(), tt, input)
				} else {
					result, err = s.ToolFeedback(context.Background(), tt, input)
				}
				if get(c, "error") != nil {
					if err == nil {
						t.Fatal("source schema error missing")
					}
					return
				}
				if err != nil {
					t.Fatal(err)
				}
				a, _ := jsjson.Stringify(result)
				b, _ := jsjson.Stringify(get(c, "result"))
				if a != b {
					t.Fatalf("result %s != %s", a, b)
				}
				return
			}
			rows, e := messages(tt)
			if e != nil {
				t.Fatal(e)
			}
			ev, e := s.evidence(tt, rows)
			if e != nil {
				t.Fatal(e)
			}
			id, e := ids(input)
			if e != nil {
				t.Fatal(e)
			}
			ctx := object("sceneId", "s", "simulation", false, "sourceEvidence", ev)
			capture := get(c, "capture").(*Object)
			a, _ := jsjson.Stringify(ctx)
			b, _ := jsjson.Stringify(get(capture, "context"))
			if a != b {
				t.Fatalf("context %s != %s", a, b)
			}
			src := sources("s", id, rows)
			var candidate *Object
			if str(c, "tool") == "habits.feedback" {
				candidate = object("direction", get(input, "direction"), "sources", src)
				if str(capture, "id") != str(input, "id") {
					t.Fatal("feedback id")
				}
			} else {
				if _, e = proposal(input); e != nil {
					t.Fatal(e)
				}
				if str(input, "kind") == "sceneRule" {
					candidate = object("kind", "sceneRule", "content", get(input, "content"), "sources", src)
				} else {
					candidate = object("kind", "botExpression", "preference", get(input, "preference"), "direction", get(input, "direction"), "sources", src)
				}
			}
			a, _ = jsjson.Stringify(candidate)
			b, _ = jsjson.Stringify(get(capture, "candidate"))
			if a != b {
				t.Fatalf("candidate %s != %s", a, b)
			}
		})
	}
}
