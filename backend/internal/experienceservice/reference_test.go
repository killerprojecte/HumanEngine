package experienceservice

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/sourceevents"
	"math"
	"os"
	"reflect"
	"strings"
	"testing"
)

type referenceOracle struct {
	Node              string
	ServiceBoundaries []struct {
		Name          string
		Snapshot      bool
		Feedback      Feedback
		Accepted      bool
		Outcome       *Outcome
		Qualification *string
		Count         int
	}
	Refs []struct {
		Name, Raw string
		Accepted  bool
	}
	Trial json.RawMessage
	IDs   []struct {
		ID       string
		Accepted bool
	}
	Material struct {
		Input         Feedback
		Outcome       Outcome
		Qualification string
	}
	Duplicate []struct {
		Feedback Feedback
		Result   struct {
			Duplicate bool
			Error     string
		}
	}
}

func loadReferenceOracle(t *testing.T) referenceOracle {
	t.Helper()
	raw, e := os.ReadFile("testdata/reference-oracle.json")
	if e != nil {
		t.Fatal(e)
	}
	var o referenceOracle
	if e = json.Unmarshal(raw, &o); e != nil {
		t.Fatal(e)
	}
	if o.Node != "v24.22.1" {
		t.Fatal(o.Node)
	}
	return o
}
func TestSourceReferenceNumbersCannotBeTruncated(t *testing.T) {
	o := loadReferenceOracle(t)
	for _, item := range o.Refs {
		t.Run(item.Name, func(t *testing.T) {
			var s Source
			e := json.Unmarshal([]byte(`{"messageId":"m","senderId":"p","eventRef":`+item.Raw+`}`), &s)
			if e == nil {
				_, e = sourceevents.ParseRef(eventRefValue(s.EventRef, s.eventRefRaw))
			}
			if (e == nil) != item.Accepted {
				t.Fatalf("source acceptance differs: %v raw=%s", e, item.Raw)
			}
		})
	}
	for _, n := range []float64{math.NaN(), math.Inf(1), math.Inf(-1), 1.9, -1, 9007199254740992} {
		var r sourceevents.Ref
		if e := decodeDomain(object("eventId", "event", "revision", n, "deletionEpoch", float64(0)), reflect.ValueOf(&r).Elem()); e == nil {
			t.Fatalf("invalid revision converted: %v to %+v", n, r)
		}
	}
}
func TestTrialIdentifiersCountUTF16NotWTF8Bytes(t *testing.T) {
	o := loadReferenceOracle(t)
	for _, item := range o.IDs {
		if got := validID(item.ID); got != item.Accepted {
			n, _ := jsjson.UTF16Len(item.ID)
			t.Fatalf("length %d got %t want %t", n, got, item.Accepted)
		}
		var trial Trial
		if e := json.Unmarshal(o.Trial, &trial); e != nil {
			t.Fatal(e)
		}
		trial.RequesterID = item.ID
		e := guarded(func() { validateTrial(trial) })
		if (e == nil) != item.Accepted {
			t.Fatalf("trial acceptance %v", e)
		}
	}
}
func TestNestedReferenceMaterialRoundTrips(t *testing.T) {
	o := loadReferenceOracle(t)
	created, e := CreateOutcome(Context{SceneID: "scene"}, o.Material.Input, "outcome")
	if e != nil {
		t.Fatal(e)
	}
	actual := take(jsjson.Parse(encoded(created)))
	expected := take(jsjson.Parse(encoded(o.Material.Outcome)))
	if !reflect.DeepEqual(actual, expected) {
		t.Fatalf("nested material changed\n%s\n%s", encoded(created), encoded(o.Material.Outcome))
	}
	p := getValue(actual.(*jsjson.Object), "policyRef").(*jsjson.Object)
	if got := strings.Join(p.Keys(), ","); got != "version,id,extra" {
		t.Fatalf("policyRef order lost: %s", got)
	}
	source := getValue(actual.(*jsjson.Object), "source").(*jsjson.Object)
	ref := getValue(source, "eventRef").(*jsjson.Object)
	if getValue(ref, "extra") == nil {
		t.Fatal("eventRef extra was stripped")
	}
}
func TestNativeNestedReferenceQualificationAndRootConflict(t *testing.T) {
	o := loadReferenceOracle(t)
	f := newLiveFixture(t, false)
	mustExec(t, f.main, "UPDATE config SET value=json_set(value,'$.memoryExperienceHintsEnabled',json('false'),'$.memoryExperienceAutoActivateEnabled',json('false')) WHERE key='settings'")
	if got := qualification(o.Material.Outcome, f.service.ports.Compare); got != o.Material.Qualification {
		t.Fatalf("qualification %s want %s", got, o.Material.Qualification)
	}
	for i, step := range o.Duplicate {
		result, e := f.service.RecordFeedback(context.Background(), testScope, step.Feedback)
		if step.Result.Error != "" {
			if e == nil || e.Error() != step.Result.Error {
				t.Fatalf("duplicate step %d got %v want %s", i, e, step.Result.Error)
			}
		} else if e != nil || result == nil || result.Duplicate != step.Result.Duplicate {
			t.Fatalf("duplicate step %d result=%v error=%v", i, result, e)
		}
	}
}
func TestRegisteredSnapshotRejectsRetainedUnknownRefKeys(t *testing.T) {
	f := newLiveFixture(t, true)
	ctx := context.Background()
	identity := sourceevents.Identity{SceneID: "scene", AdapterID: "synthetic", AccountID: "account", MessageID: "instruction", SenderID: "alice"}
	observed, e := f.events.Observe(ctx, sourceevents.Change{Kind: "ingress", Identity: identity, BodyHash: strings.Repeat("a", 64)})
	if e != nil {
		t.Fatal(e)
	}
	input := feedback("request_exact_person_lookup")
	input.Source.EventRef = observed.Ref
	raw := eventRefValue(observed.Ref, nil).(*jsjson.Object)
	_ = raw.Set("extra", "must-not-be-stripped")
	input.Source.eventRefRaw = &rawReference{raw}
	if _, e = f.service.RecordFeedback(ctx, testScope, input); e == nil {
		t.Fatal("unknown source ref key silently authorized")
	}
}

func TestActualServiceRetainsHistoricalRefButRejectsSnapshotRef(t *testing.T) {
	o := loadReferenceOracle(t)
	for _, step := range o.ServiceBoundaries {
		t.Run(fmt.Sprintf("%s-snapshot=%t", step.Name, step.Snapshot), func(t *testing.T) {
			f := newLiveFixture(t, true)
			mustExec(t, f.main, "UPDATE config SET value=json_set(value,'$.memoryExperienceHintsEnabled',json('false'),'$.memoryExperienceAutoActivateEnabled',json('false')) WHERE key='settings'")
			identity := sourceevents.Identity{SceneID: "scene", AdapterID: "synthetic", AccountID: "account", MessageID: "instruction", SenderID: "alice"}
			if _, e := f.events.Observe(context.Background(), sourceevents.Change{Kind: "ingress", Identity: identity, BodyHash: strings.Repeat("a", 64)}); e != nil {
				t.Fatal(e)
			}
			result, e := f.service.RecordFeedback(context.Background(), testScope, step.Feedback)
			if (e == nil) != step.Accepted {
				t.Fatalf("source service acceptance differs: %v", e)
			}
			var count int
			if e := f.main.QueryRow("SELECT count(*) FROM memory_experience_outcomes").Scan(&count); e != nil || count != step.Count {
				t.Fatalf("stored %d want %d: %v", count, step.Count, e)
			}
			if step.Accepted {
				if result == nil {
					t.Fatal("historical feedback returned no outcome")
				}
				if encoded(result.Outcome) != encoded(step.Outcome) {
					t.Fatalf("historical raw material changed: got %s want %s", encoded(result.Outcome), encoded(step.Outcome))
				}
				if step.Qualification == nil || qualification(result.Outcome, f.service.ports.Compare) != *step.Qualification {
					t.Fatal("historical qualification changed")
				}
				var stored string
				if e := f.main.QueryRow("SELECT value FROM memory_experience_outcomes").Scan(&stored); e != nil {
					t.Fatal(e)
				}
				var restored Outcome
				if e := json.Unmarshal([]byte(stored), &restored); e != nil {
					t.Fatal(e)
				}
				if qualification(restored, f.service.ports.Compare) != *step.Qualification {
					t.Fatal("stored historical ref changed after decoding")
				}
			}
		})
	}
}
