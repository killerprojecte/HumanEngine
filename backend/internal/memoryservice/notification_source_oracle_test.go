package memoryservice

import (
	"errors"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"os"
	"testing"
)

func TestActualSourceNotificationStackAndFailures(t *testing.T) {
	raw, e := os.ReadFile("../semantic/testdata/owner-source.json")
	if e != nil {
		t.Fatal(e)
	}
	parsed, e := jsjson.Parse(string(raw))
	if e != nil {
		t.Fatal(e)
	}
	fixture := object(parsed)
	for _, item := range epArr(value(fixture, "notificationCases")) {
		scenario := text(object(item), "scenario")
		t.Run(scenario, func(t *testing.T) {
			var current *Object
			revision := "0"
			trace := []any{}
			stack := []func(){}
			queue, e := NewNotificationQueue(NotificationPorts{Enabled: func(string) bool { return true }, PolicyIdentity: func(string) string { return revision }, Current: func(string, Reference) (*Object, error) { return current, nil }, Episodes: func(string, Reference) ([]*Object, error) { return []*Object{}, nil }, PostStack: func(fn func()) { stack = append(stack, fn) }, Failed: func() { trace = append(trace, []any{"failed"}) }})
			if e != nil {
				t.Fatal(e)
			}
			_, e = queue.Subscribe(func(d *Object) error {
				trace = append(trace, []any{"notify", value(d, "reason"), epGet(value(d, "trigger"), "ref")})
				if scenario == "listener-failure" {
					return errors.New("synthetic listener failure")
				}
				return nil
			})
			if e != nil {
				t.Fatal(e)
			}
			if e = queue.Notify("scene", Reference{"a", 1, 0}, "assertion_committed"); e != nil {
				t.Fatal(e)
			}
			trace = append(trace, []any{"checkpoint", float64(len(trace))})
			if scenario == "replaced-reason" {
				if e = queue.Notify("scene", Reference{"a", 1, 0}, "source_qualification_changed"); e != nil {
					t.Fatal(e)
				}
			}
			if scenario != "rolled-back" {
				current = epObj("record", epObj("scope", epObj("kind", "scene", "personId", "person")), "derived", false, "rootCoverage", "complete", "dependencyKey", "synthetic")
			}
			if scenario == "policy-changed" {
				revision = "1"
			}
			for _, fn := range stack {
				fn()
			}
			queue.Dispose()
			if !epEqual(trace, epGet(item, "trace")) {
				t.Fatal(trace, epGet(item, "trace"))
			}
		})
	}
}
