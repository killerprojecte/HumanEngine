package usageview

import (
	"github.com/killerprojecte/HumanEngine/backend/internal/jsdate"
	"net/url"
	"testing"
)

func TestUsageRangeBoundaryInstants(t *testing.T) {
	parser, e := jsdate.New("UTC")
	if e != nil {
		t.Fatal(e)
	}
	for _, c := range []struct {
		start, end, want string
		valid            bool
	}{
		{"0000-01-01T00:00:00+23:59", "0001-01-01T00:00:00Z", "-000001-12-31T00:01:00.000Z", true},
		{"9999-12-31T23:59:59-23:59", "9999-12-31T23:59:59.999-23:59", "+010000-01-01T23:58:59.000Z", true},
		{"2000-02-29T00:00:00Z", "2000-03-01T00:00:00Z", "2000-02-29T00:00:00.000Z", true},
		{"1900-02-29T00:00:00Z", "2000-03-01T00:00:00Z", "", false},
		{"2000-01-01T00:00:00.000001Z", "2000-01-01T00:00:00.000002Z", "", false},
	} {
		q, e := ParseQuery(url.Values{"start": {c.start}, "end": {c.end}}, parser)
		if !c.valid {
			if e == nil {
				t.Fatal("invalid accepted", c)
			}
			continue
		}
		if e != nil || q.Start != c.want {
			t.Fatal(c, q, e)
		}
	}
}
