package sourceevents

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/killerprojecte/HumanEngine/backend/internal/botruntime"
	"github.com/killerprojecte/HumanEngine/backend/internal/dispatch"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsdate"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/replyplan"
)

func TestActualAcceptedEditIngressMutations(t *testing.T) {
	raw, err := os.ReadFile("testdata/ingress-routing.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		EditCases []struct {
			Mode, Scene, Initial, Current string
			Expected                      struct {
				Error, Status  *string
				Messages, Head string
			}
		}
	}
	if err = json.Unmarshal(raw, &fixture); err != nil {
		t.Fatal(err)
	}
	if len(fixture.EditCases) != 9 {
		t.Fatal("missing actual accepted-edit oracle")
	}
	for _, c := range fixture.EditCases {
		t.Run(c.Mode, func(t *testing.T) {
			f, a := runtimeFixture(t, Options{})
			runtimePut(t, f.main, "config", "settings", ingressSettings(t, true))
			var ref dispatch.SceneRef
			if e := json.Unmarshal([]byte(c.Scene), &ref); e != nil {
				t.Fatal(e)
			}
			scene := botruntime.Scene{SceneRef: ref}
			dates, e := jsdate.New("UTC")
			if e != nil {
				t.Fatal(e)
			}
			compare := func(a, b string) (int, error) { return strings.Compare(a, b), nil }
			policy, e := replyplan.New(compare, dates)
			if e != nil {
				t.Fatal(e)
			}
			r, e := botruntime.New(botruntime.DefaultSettings(), botruntime.Services{Authority: a, Clock: runtimeTestClock{}, Dates: dates, Policy: policy, Compare: compare, Models: botruntime.ModelFunc(func(context.Context, botruntime.ModelRequest, func(botruntime.Completion, error)) {
				t.Error("unexpected model")
			})})
			if e != nil {
				t.Fatal(e)
			}
			defer r.Close()
			r.AddAdapter(scene.AdapterID, &botruntime.Adapter{BotUserID: "bot-user"})
			if _, e = r.IngestScene(context.Background(), scene, runtimeObject(t, c.Initial), false); e != nil {
				t.Fatal(e)
			}
			r.AddExtension(&botruntime.Extension{Edited: func(_ *jsjson.Object, current *jsjson.Object, _ botruntime.Scene) error {
				switch c.Mode {
				case "body-mutation":
					_ = current.Set("parts", []any{obj("type", "text", "text", "Changed by callback.")})
				case "ref-replacement":
					_ = current.Set("sourceEventRef", obj("eventId", "forged", "revision", float64(999), "deletionEpoch", float64(0)))
				case "ref-mutation":
					ref := get(current, "sourceEventRef").(*jsjson.Object)
					_ = ref.Set("revision", get(ref, "revision").(float64)+1)
				case "history":
					_ = current.Set("origin", "history")
				case "scene-mismatch":
					_ = current.Set("sceneId", "other")
				case "bot-mutation":
					_ = current.Set("senderId", "bot")
					_ = current.Set("parts", []any{obj("type", "text", "text", "Changed by callback.")})
				}
				return nil
			}})
			status, e := r.Edit(context.Background(), scene.ID, runtimeObject(t, c.Current), obj("editId", "edit-1", "order", obj("kind", "platform_sequence", "sequence", float64(1))), false)
			if (e != nil) != (c.Expected.Error != nil) || e != nil && e.Error() != *c.Expected.Error {
				t.Fatalf("edit error=%v expected=%v", e, c.Expected.Error)
			}
			if e == nil && (c.Expected.Status == nil || status != *c.Expected.Status) {
				t.Fatalf("status %s expected %v", status, c.Expected.Status)
			}
			snapshot, e := r.Snapshot(scene.ID)
			if e != nil {
				t.Fatal(e)
			}
			expected := runtimeObjects(t, c.Expected.Messages)
			if len(snapshot.Messages) != len(expected) {
				t.Fatalf("message count %d != %d", len(snapshot.Messages), len(expected))
			}
			for i, got := range snapshot.Messages {
				for _, key := range []string{"id", "sceneId", "senderId", "origin", "status", "parts", "sourceEventRef"} {
					gv, gp := got.Get(key)
					ev, ep := expected[i].Get(key)
					if gp != ep || stringify(t, gv) != stringify(t, ev) {
						t.Fatalf("message field %s differs: %s != %s", key, stringify(t, gv), stringify(t, ev))
					}
				}
			}
			var head *Metadata
			e = f.store.Read(context.Background(), func(view *View) error { var e error; head, e = view.Registered(scene.ID, "source"); return e })
			if e != nil || head == nil {
				t.Fatal("missing durable revision", e)
			}
			if got := stringify(t, head.Value()); got != c.Expected.Head {
				t.Fatalf("head differs\n%s\n%s", got, c.Expected.Head)
			}
		})
	}
}
