package sourceevents

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"testing"

	"github.com/killerprojecte/HumanEngine/backend/internal/contract"
	"github.com/killerprojecte/HumanEngine/backend/internal/dispatch"
	"github.com/killerprojecte/HumanEngine/backend/internal/ingressrouting"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
)

type routingProbe struct {
	ingressrouting.Repository
	reads    int
	failure  string
	timeline []string
	report   func(string)
}

func (p *routingProbe) record(event string) {
	p.timeline = append(p.timeline, event)
	if p.report != nil {
		p.report(event)
	}
}

func (p *routingProbe) Scene(ctx context.Context, id string) (any, bool, error) {
	p.reads++
	if p.reads == 2 {
		p.record("observeIdentity")
		if p.failure == "observeIdentity" {
			return nil, false, errors.New("synthetic_observeIdentity")
		}
	}
	return p.Repository.Scene(ctx, id)
}
func (p *routingProbe) SaveScene(ctx context.Context, scene *jsjson.Object) error {
	p.record("saveScene")
	if p.failure == "saveScene" {
		return errors.New("synthetic_saveScene")
	}
	return p.Repository.SaveScene(ctx, scene)
}
func TestActualIngressRoutingPersistence(t *testing.T) {
	raw, err := os.ReadFile("testdata/ingress-routing.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Cases []struct {
			Spec     struct{ Name, Failure string }
			Before   struct{ Scene, Identity *string }
			Input    struct{ Scene, Message string }
			Expected struct {
				Error                             *string
				PersistedScene, PersistedIdentity *string
				Timeline                          []any
			}
		}
	}
	if err = json.Unmarshal(raw, &fixture); err != nil {
		t.Fatal(err)
	}
	if len(fixture.Cases) != 32 {
		t.Fatal("missing ingress source cases")
	}
	for _, c := range fixture.Cases {
		if c.Spec.Name == "history-skips-routing" || c.Spec.Name == "mismatch-skips-routing" {
			continue
		} // Runtime rejection precedes this domain.
		t.Run(c.Spec.Name, func(t *testing.T) {
			f, _ := runtimeFixture(t, Options{})
			var scene dispatch.SceneRef
			if err = json.Unmarshal([]byte(c.Input.Scene), &scene); err != nil {
				t.Fatal(err)
			}
			message := runtimeObject(t, c.Input.Message)
			sender := str(message, "senderId")
			key := "identity:" + scene.ID + ":" + sender
			if c.Before.Scene != nil {
				runtimePut(t, f.main, "scenes", scene.ID, *c.Before.Scene)
			}
			if c.Before.Identity != nil {
				runtimePut(t, f.main, "config", key, *c.Before.Identity)
			}
			repository, e := ingressrouting.NewSQLiteRepository(f.main, f.authority)
			if e != nil {
				t.Fatal(e)
			}
			probe := &routingProbe{Repository: repository, failure: c.Spec.Failure, timeline: []string{}}
			router, e := ingressrouting.New(probe)
			if e != nil {
				t.Fatal(e)
			}
			e = router.Route(context.Background(), scene, message)
			expectedError := c.Expected.Error
			if c.Spec.Failure == "recalled" {
				expectedError = nil
			}
			if (e != nil) != (expectedError != nil) || e != nil && e.Error() != *expectedError {
				t.Fatalf("error=%v expected=%v", e, expectedError)
			}
			expectedTimeline := []string{}
			for _, item := range c.Expected.Timeline {
				if s, ok := item.(string); ok && (s == "saveScene" || s == "observeIdentity") {
					expectedTimeline = append(expectedTimeline, s)
				}
			}
			if !reflect.DeepEqual(probe.timeline, expectedTimeline) {
				t.Fatalf("timeline %v != %v", probe.timeline, expectedTimeline)
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
				t.Fatalf("scene %v != %v", got, c.Expected.PersistedScene)
			}
			if got := read("SELECT value FROM config WHERE key=?", key); !reflect.DeepEqual(got, c.Expected.PersistedIdentity) {
				t.Fatalf("identity %v != %v", got, c.Expected.PersistedIdentity)
			}
			head, e := f.authority.Head(context.Background())
			if e != nil || head.Sequence != 0 {
				t.Fatalf("invented revocation %+v %v", head, e)
			}
		})
	}
}

func ingressSettings(t *testing.T, enabled bool) string {
	t.Helper()
	value := runtimeObject(t, string(contract.DefaultSettingsInput()))
	_ = value.Set("sourceEventRevisionsEnabled", enabled)
	return stringify(t, value)
}
