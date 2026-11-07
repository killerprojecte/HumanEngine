package profileidentity

import (
	"encoding/json"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"os"
	"testing"
)

func TestActualSourceProfileIdentity(t *testing.T) {
	raw, err := os.ReadFile("../sourceevents/testdata/ingress-routing.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		ProfileNames []struct {
			Input    string
			Expected *string
		}
		RawAvatars []struct {
			Input           string
			Group           bool
			Expected, Error *string
		}
		Avatars []struct {
			ID       string
			Group    bool
			Expected *string
		}
	}
	if err = json.Unmarshal(raw, &fixture); err != nil {
		t.Fatal(err)
	}
	if len(fixture.ProfileNames) < 16 || len(fixture.Avatars) != 22 {
		t.Fatal("missing actual profile oracle")
	}
	for _, c := range fixture.ProfileNames {
		input, e := jsjson.Parse(c.Input)
		if e != nil {
			t.Fatal(e)
		}
		name, exists, e := DisplayName(input)
		if e != nil {
			t.Fatal(e)
		}
		if exists != (c.Expected != nil) {
			t.Fatalf("name existence %s: %v", c.Input, exists)
		}
		if exists {
			got, e := jsjson.Stringify(name)
			if e != nil || got != *c.Expected {
				t.Fatalf("name %s: %s != %s (%v)", c.Input, got, *c.Expected, e)
			}
		}
	}
	for _, c := range fixture.Avatars {
		got, exists := QQAvatar(c.ID, c.Group)
		if exists != (c.Expected != nil) || exists && got != *c.Expected {
			t.Fatalf("avatar %q/group=%v: %q/%v", c.ID, c.Group, got, exists)
		}
	}
	if len(fixture.RawAvatars) != 22 {
		t.Fatal("missing raw avatar oracle")
	}
	for _, c := range fixture.RawAvatars {
		input, err := jsjson.Parse(c.Input)
		if err != nil {
			t.Fatal(err)
		}
		got, exists, err := AvatarValue(input, c.Group)
		if (err != nil) != (c.Error != nil) || err != nil && err.Error() != *c.Error {
			t.Fatalf("raw avatar error %s: %v, expected %v", c.Input, err, c.Error)
		}
		if err == nil && (exists != (c.Expected != nil) || exists && got != *c.Expected) {
			t.Fatalf("raw avatar %s: %q/%v", c.Input, got, exists)
		}
	}

}
