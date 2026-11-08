package experienceservice

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/sourceevents"
	"os"
	"reflect"
	"strings"
	"testing"
)

type presenceCase struct {
	Name, Side           string
	Registered, Snapshot bool
	Feedback             json.RawMessage
	Accepted             bool
	Outcome              json.RawMessage
	Qualification        *string
	Error                *string
	Count                int
}

func presenceCases(t *testing.T) []presenceCase {
	t.Helper()
	raw, e := os.ReadFile("testdata/presence-oracle.json")
	if e != nil {
		t.Fatal(e)
	}
	var o struct {
		Node  string
		Cases []presenceCase
	}
	if e = json.Unmarshal(raw, &o); e != nil {
		t.Fatal(e)
	}
	if o.Node != "v24.22.1" || len(o.Cases) != 112 {
		t.Fatalf("unexpected oracle %s %d", o.Node, len(o.Cases))
	}
	return o.Cases
}
func TestRawReferencePresenceAndAllJSONKindsRoundTrip(t *testing.T) {
	for _, c := range presenceCases(t) {
		var f Feedback
		if e := json.Unmarshal(c.Feedback, &f); e != nil {
			t.Fatalf("%s %s decode: %v", c.Side, c.Name, e)
		}
		want := take(jsjson.Parse(string(c.Feedback))).(*jsjson.Object)
		got := take(jsjson.Parse(encoded(f))).(*jsjson.Object)
		wantSide := getValue(want, c.Side).(*jsjson.Object)
		gotSide := getValue(got, c.Side).(*jsjson.Object)
		wv, wp := wantSide.Get("eventRef")
		gv, gp := gotSide.Get("eventRef")
		if wp != gp || !reflect.DeepEqual(wv, gv) {
			t.Fatalf("%s %s presence/value changed: got %s want %s", c.Side, c.Name, encoded(f), c.Feedback)
		}
		var typed *sourceevents.Ref
		var raw *rawReference
		if c.Side == "source" {
			typed = f.Source.EventRef
			raw = f.Source.eventRefRaw
		} else {
			typed = f.Target.EventRef
			raw = f.Target.eventRefRaw
		}
		if (raw != nil) != wp {
			t.Fatalf("%s presence conflated", c.Name)
		}
		if typed != nil {
			if _, e := sourceevents.ParseRef(eventRefValue(typed, raw)); e != nil {
				t.Fatalf("malformed raw acquired typed authority %s: %v", c.Name, e)
			}
		}
	}
}
func TestActualServiceCompleteRawReferenceMatrix(t *testing.T) {
	for _, c := range presenceCases(t) {
		t.Run(fmt.Sprintf("%s/%s/registered=%t/snapshot=%t", c.Side, c.Name, c.Registered, c.Snapshot), func(t *testing.T) {
			f := newLiveFixture(t, true)
			mustExec(t, f.main, "UPDATE config SET value=json_set(value,'$.memoryExperienceHintsEnabled',json('false'),'$.memoryExperienceAutoActivateEnabled',json('false')) WHERE key='settings'")
			if c.Registered {
				message, sender := "instruction", "alice"
				if c.Side == "target" {
					message, sender = "intro", "bot"
				}
				identity := sourceevents.Identity{SceneID: "scene", AdapterID: "synthetic", AccountID: "account", MessageID: message, SenderID: sender}
				if _, e := f.events.Observe(context.Background(), sourceevents.Change{Kind: "ingress", Identity: identity, BodyHash: strings.Repeat("a", 64)}); e != nil {
					t.Fatal(e)
				}
			}
			var feedback Feedback
			if e := json.Unmarshal(c.Feedback, &feedback); e != nil {
				t.Fatal(e)
			}
			result, e := f.service.RecordFeedback(context.Background(), testScope, feedback)
			if (e == nil) != c.Accepted {
				t.Fatalf("source acceptance differs: %v", e)
			}
			if c.Error != nil && strings.HasPrefix(*c.Error, "experience_") && (e == nil || e.Error() != *c.Error) {
				t.Fatalf("error branch got %v want %s", e, *c.Error)
			}
			var n int
			if e := f.main.QueryRow("SELECT count(*) FROM memory_experience_outcomes").Scan(&n); e != nil || n != c.Count {
				t.Fatalf("stored count %d want %d: %v", n, c.Count, e)
			}
			if !c.Accepted {
				return
			}
			if result == nil {
				t.Fatal("missing outcome")
			}
			var want Outcome
			if e := json.Unmarshal(c.Outcome, &want); e != nil {
				t.Fatal(e)
			}
			if encoded(result.Outcome) != encoded(want) {
				t.Fatalf("retained outcome changed\ngot %s\nwant %s", encoded(result.Outcome), encoded(want))
			}
			if c.Qualification == nil || qualification(result.Outcome, f.service.ports.Compare) != *c.Qualification {
				t.Fatal("source qualification mismatch")
			}
			var stored string
			if e := f.main.QueryRow("SELECT value FROM memory_experience_outcomes").Scan(&stored); e != nil {
				t.Fatal(e)
			}
			var restored Outcome
			if e := json.Unmarshal([]byte(stored), &restored); e != nil {
				t.Fatal(e)
			}
			if encoded(restored) != encoded(result.Outcome) || qualification(restored, f.service.ports.Compare) != *c.Qualification {
				t.Fatal("persisted presence/value changed on decode")
			}
		})
	}
}
