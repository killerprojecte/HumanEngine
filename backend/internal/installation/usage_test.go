package installation

import (
	"encoding/json"
	"errors"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsdate"
	"github.com/killerprojecte/HumanEngine/backend/internal/nodeicu"
	"os"
	"testing"
)

func TestSourceUsageBuckets(t *testing.T) {
	testSourceUsageBuckets(t, os.Getenv("HUMANENGINE_TEST_REQUIRE_ICU") == "1")
}
func testSourceUsageBuckets(t *testing.T, requireICU bool) {
	raw, e := os.ReadFile("testdata/usage.json")
	if e != nil {
		t.Fatal(e)
	}
	var cases []struct{ At, Zone, Day, Hour string }
	if e = json.Unmarshal(raw, &cases); e != nil {
		t.Fatal(e)
	}
	p, e := jsdate.New("UTC")
	if e != nil {
		t.Fatal(e)
	}
	for _, c := range cases {
		for _, bucket := range []string{"day", "hour"} {
			t.Run(c.Zone+"/"+c.At+"/"+bucket, func(t *testing.T) {
				got, e := usageBucket(p, c.At, c.Zone, bucket)
				if errors.Is(e, nodeicu.ErrUnavailable) {
					if requireICU {
						t.Fatalf("native ICU execution required: %v", e)
					}
					t.Skip("named zone requires pinned native ICU; case did not execute")
				}
				want := c.Day
				if bucket == "hour" {
					want = c.Hour
				}
				if e != nil || got != want {
					t.Fatalf("got %s %v want %s", got, e, want)
				}
			})
		}
	}
}
