package intltime

import (
	"encoding/json"
	"errors"
	"github.com/killerprojecte/HumanEngine/backend/internal/nodeicu"
	"github.com/killerprojecte/HumanEngine/backend/internal/timezones"
	"os"
	"testing"
)

func TestSourceHours(t *testing.T) {
	var fixture struct {
		Timestamps []int64
		Cases      []struct {
			Zone  string
			Hours []int
		}
	}
	data, err := os.ReadFile("oracle.json")
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(data, &fixture); err != nil {
		t.Fatal(err)
	}
	unavailable := false
	for _, c := range fixture.Cases {
		_, _, fixed, valid := timezones.Resolve(c.Zone)
		if !valid {
			t.Fatalf("source zone rejected: %s", c.Zone)
		}
		for i, ms := range fixture.Timestamps {
			got, err := Hour(ms, c.Zone)
			if errors.Is(err, nodeicu.ErrUnavailable) && !fixed {
				unavailable = true
				continue
			}
			if err != nil || got != c.Hours[i] {
				t.Fatalf("zone=%q ms=%d got=%d err=%v want=%d", c.Zone, ms, got, err, c.Hours[i])
			}
		}
	}
	if unavailable {
		t.Log("non-native build: named zones correctly refuse host fallback; fixed offsets fully checked")
	}
}

func TestInvalidInputs(t *testing.T) {
	for _, zone := range []string{"", "Local", "Not/AZone", "+24:00", "+12:60", " UTC", "Ａmerica/New_York"} {
		if _, err := Hour(0, zone); err == nil {
			t.Fatalf("accepted %q", zone)
		}
	}
	for _, ms := range []int64{-8640000000000001, 8640000000000001} {
		if _, err := Hour(ms, "UTC"); err == nil {
			t.Fatal(ms)
		}
	}
}
