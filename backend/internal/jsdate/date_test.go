package jsdate

import (
	"encoding/json"
	"errors"
	"github.com/killerprojecte/HumanEngine/backend/internal/nodeicu"
	"os"
	"testing"

	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
)

func TestSourceOracle(t *testing.T) {
	b, err := os.ReadFile("testdata/oracle.json")
	if err != nil {
		t.Fatal(err)
	}
	var f struct {
		Node  string
		ICU   string
		Zones []string
		Cases []struct {
			Units        []uint16
			Finite       []bool
			Milliseconds []*int64
		}
	}
	if err = json.Unmarshal(b, &f); err != nil {
		t.Fatal(err)
	}
	if f.Node != "v24.22.1" || f.ICU != "78.3" {
		t.Fatal(f.Node)
	}
	for z, zone := range f.Zones {
		t.Run(zone, func(t *testing.T) {
			p, err := New(zone)
			if err != nil {
				if zone == "UTC" || !errors.Is(err, nodeicu.ErrUnavailable) {
					t.Fatal(err)
				}
				t.Skip(err)
			}
			for _, c := range f.Cases {
				if len(c.Finite) != len(f.Zones) {
					t.Fatal("invalid fixture")
				}
				s := jsjson.FromUTF16(c.Units)
				got, err := p.Finite(s)
				if err != nil {
					t.Fatal(err)
				}
				if got != c.Finite[z] {
					t.Errorf("input=%q got=%v want=%v", s, got, c.Finite[z])
				}
				if len(c.Milliseconds) != len(f.Zones) {
					t.Fatal("missing millisecond oracle")
				}
				ms, valid, err := p.ParseMillis(s)
				if err != nil || valid != c.Finite[z] {
					t.Fatalf("numeric input=%q valid=%v err=%v", s, valid, err)
				}
				if c.Milliseconds[z] == nil {
					if valid {
						t.Fatalf("invalid input returned milliseconds: %q", s)
					}
				} else if !valid || ms != *c.Milliseconds[z] {
					t.Errorf("numeric input=%q got=%d valid=%v want=%d", s, ms, valid, *c.Milliseconds[z])
				}

			}
		})
	}
}
func TestConfiguration(t *testing.T) {
	if _, err := New(""); err == nil {
		t.Fatal("empty zone accepted")
	}
	var p *Parser
	if _, err := p.Finite("2000"); err == nil {
		t.Fatal("nil parser accepted")
	}
}

func TestMillisBoundaryErrorsAndEpoch(t *testing.T) {
	var unconfigured *Parser
	if _, _, err := unconfigured.ParseMillis("2000"); err == nil {
		t.Fatal("nil parser accepted")
	}
	p, _ := New("UTC")
	if _, _, err := p.ParseMillis(string([]byte{0xff})); err == nil {
		t.Fatal("noncanonical Go bytes accepted")
	}
	// Preserve the old boolean-only convenience behavior for non-JS inputs.
	if valid, err := p.Finite(string([]byte{0xff})); valid || err != nil {
		t.Fatal(valid, err)
	}
	for _, input := range []string{"1970-01-01T00:00:00.000Z", "1970-01-01T01:00:00+01:00"} {
		ms, valid, err := p.ParseMillis(input)
		if ms != 0 || !valid || err != nil {
			t.Fatal(input, ms, valid, err)
		}
	}
	ms, valid, err := p.ParseMillis("not a date")
	if ms != 0 || valid || err != nil {
		t.Fatal(ms, valid, err)
	}
}
