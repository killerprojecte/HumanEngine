package learningowner

import (
	"context"
	"github.com/killerprojecte/HumanEngine/backend/internal/habitservice"
	"github.com/killerprojecte/HumanEngine/backend/internal/modelread"
	"net/http"
	"testing"
)

func TestLearningOwnerRejectsForeignDatabaseOwners(t *testing.T) {
	owner, _ := ownerFixture(t, func(http.ResponseWriter, *http.Request) { t.Error("binding check called provider") })
	foreignMemory, foreign := serviceSetup(t)
	foreignRead, e := modelread.New(foreign.store)
	if e != nil {
		t.Fatal(e)
	}
	foreignHabits, e := habitservice.New(context.Background(), foreign.main, foreign.recovery, habitservice.Options{})
	if e != nil {
		t.Fatal(e)
	}
	cases := []struct {
		name string
		edit func(*Dependencies)
	}{
		{"assertions", func(d *Dependencies) { d.Assertions = foreign.store }},
		{"memory", func(d *Dependencies) { d.Memory = foreignMemory }},
		{"habits", func(d *Dependencies) { d.Habits = foreignHabits }},
		{"model-read", func(d *Dependencies) { d.Read = foreignRead }},
		{"source-events", func(d *Dependencies) { d.Sources = foreign.events }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			d := owner.d
			c.edit(&d)
			if runtime, e := New(d); e == nil || runtime != nil {
				t.Fatalf("foreign owner accepted: %v %v", runtime, e)
			}
		})
	}
}
