package sourceevents

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/killerprojecte/HumanEngine/backend/internal/botruntime"
	"github.com/killerprojecte/HumanEngine/backend/internal/dispatch"
	"github.com/killerprojecte/HumanEngine/backend/internal/ingressrouting"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsdate"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/replyplan"
)

type ingressRuntimeAuthority struct {
	*RuntimeAuthority
	timeline *[]any
	failure  string
}

func (a *ingressRuntimeAuthority) IsRecalled(scene botruntime.Scene, id string) (bool, error) {
	*a.timeline = append(*a.timeline, "recalled")
	if a.failure == "recalled" {
		return false, errors.New("synthetic_recalled")
	}
	return a.RuntimeAuthority.IsRecalled(scene, id)
}
func TestActualIngressRuntimeWithSQLite(t *testing.T) {
	raw, err := os.ReadFile("testdata/ingress-routing.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Cases []struct {
			Spec struct {
				Name, Failure            string
				Repeat, Recalled, Closed bool
				Admission                *bool
			}
			Before   struct{ Scene, Identity *string }
			Input    struct{ Scene, Message string }
			Expected struct {
				Error                             *string
				PersistedScene, PersistedIdentity *string
				Registered, StateExists           bool
				Timeline                          []any
				Messages                          []json.RawMessage
			}
		}
	}
	if err = json.Unmarshal(raw, &fixture); err != nil {
		t.Fatal(err)
	}
	if len(fixture.Cases) != 32 {
		t.Fatal("missing actual ingress oracle")
	}
	for _, c := range fixture.Cases {
		if c.Spec.Closed {
			continue
		} // Internal closed-owner lifecycle is separately covered in botruntime.
		t.Run(c.Spec.Name, func(t *testing.T) {
			f, base := runtimeFixture(t, Options{})
			runtimePut(t, f.main, "config", "settings", ingressSettings(t, false))
			var ref dispatch.SceneRef
			if e := json.Unmarshal([]byte(c.Input.Scene), &ref); e != nil {
				t.Fatal(e)
			}
			scene := botruntime.Scene{SceneRef: ref}
			message := runtimeObject(t, c.Input.Message)
			key := "identity:" + scene.ID + ":" + str(message, "senderId")
			if c.Before.Scene != nil {
				runtimePut(t, f.main, "scenes", scene.ID, *c.Before.Scene)
			}
			if c.Before.Identity != nil {
				runtimePut(t, f.main, "config", key, *c.Before.Identity)
			}
			timeline := []any{}
			authority := &ingressRuntimeAuthority{RuntimeAuthority: base, timeline: &timeline}
			dates, e := jsdate.New("UTC")
			if e != nil {
				t.Fatal(e)
			}
			compare := func(a, b string) (int, error) { return strings.Compare(a, b), nil }
			policy, e := replyplan.New(compare, dates)
			if e != nil {
				t.Fatal(e)
			}
			cfg := botruntime.DefaultSettings()
			cfg.TopicAdmissionEnabled = c.Spec.Admission == nil || *c.Spec.Admission
			r, e := botruntime.New(cfg, botruntime.Services{Authority: authority, Clock: runtimeTestClock{}, Dates: dates, Policy: policy, Compare: compare, Models: botruntime.ModelFunc(func(context.Context, botruntime.ModelRequest, func(botruntime.Completion, error)) {
				t.Error("unexpected model")
			})})
			if e != nil {
				t.Fatal(e)
			}
			defer r.Close()
			if c.Spec.Repeat {
				if len(c.Expected.Messages) == 0 {
					t.Fatal("missing initial repeated source")
				}
				if _, e = r.IngestScene(context.Background(), scene, runtimeObject(t, string(c.Expected.Messages[0])), false); e != nil {
					t.Fatal(e)
				}
			}
			if c.Spec.Recalled {
				if e = base.Recall(context.Background(), scene, str(message, "id"), nil, nil); e != nil {
					t.Fatal(e)
				}
			}
			timeline = []any{}
			repository, e := ingressrouting.NewSQLiteRepository(f.main, f.authority)
			if e != nil {
				t.Fatal(e)
			}
			probe := &routingProbe{Repository: repository, failure: c.Spec.Failure, report: func(event string) { timeline = append(timeline, event) }}
			base.routing, e = ingressrouting.New(probe)
			if e != nil {
				t.Fatal(e)
			}
			authority.failure = c.Spec.Failure
			r.AddExtension(&botruntime.Extension{Received: func(m *jsjson.Object, s botruntime.Scene) error {
				view, e := r.Snapshot(s.ID)
				if e != nil {
					return e
				}
				topics := []any{}
				for _, topic := range view.Topics {
					ids := []any{}
					if topic.MessageCount == 1 {
						ids = append(ids, str(m, "id"))
					}
					topics = append(topics, map[string]any{"messageIds": ids, "version": float64(topic.Version)})
				}
				timeline = append(timeline, map[string]any{"kind": "received", "topics": topics})
				return nil
			}})
			_, e = r.IngestScene(context.Background(), scene, message, false)
			if (e != nil) != (c.Expected.Error != nil) || e != nil && e.Error() != *c.Expected.Error {
				t.Fatalf("ingress error=%v expected=%v", e, c.Expected.Error)
			}
			if !reflect.DeepEqual(jsonDomain(t, timeline), c.Expected.Timeline) {
				t.Fatalf("timeline %#v != %#v", timeline, c.Expected.Timeline)
			}
			_, registered := r.RegisteredScene(scene.ID)
			if registered != c.Expected.Registered {
				t.Fatalf("registered=%v expected=%v", registered, c.Expected.Registered)
			}
			if registered {
				view, e := r.Snapshot(scene.ID)
				if e != nil {
					t.Fatal(e)
				}
				if len(view.Messages) != len(c.Expected.Messages) {
					t.Fatalf("message count %d != %d", len(view.Messages), len(c.Expected.Messages))
				}
				for i, m := range view.Messages {
					expected := runtimeObject(t, string(c.Expected.Messages[i]))
					if str(m, "id") != str(expected, "id") || str(m, "status") != str(expected, "status") {
						t.Fatal("message identity/status differs")
					}
				}
			}
			read := func(query, key string) *string {
				t.Helper()
				var value string
				e := f.main.QueryRow(query, key).Scan(&value)
				if errors.Is(e, sql.ErrNoRows) {
					return nil
				}
				if e != nil {
					t.Fatal(e)
				}
				return &value
			}
			if got := read("SELECT value FROM scenes WHERE id=?", scene.ID); !reflect.DeepEqual(got, c.Expected.PersistedScene) {
				t.Fatalf("scene differs: %v != %v", got, c.Expected.PersistedScene)
			}
			if got := read("SELECT value FROM config WHERE key=?", key); !reflect.DeepEqual(got, c.Expected.PersistedIdentity) {
				t.Fatalf("identity differs: %v != %v", got, c.Expected.PersistedIdentity)
			}
		})
	}
}
