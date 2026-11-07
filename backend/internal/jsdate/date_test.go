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
			Units  []uint16
			Finite []bool
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
