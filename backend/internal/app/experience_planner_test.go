package app

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"testing"

	"github.com/killerprojecte/HumanEngine/backend/internal/botruntime"
	"github.com/killerprojecte/HumanEngine/backend/internal/modelread"
)

type plannerOrderPolicy struct {
	name   string
	calls  []string
	ticket *modelread.Ticket
}

func (p *plannerOrderPolicy) PersonalizationAllowed(context.Context, string) (bool, error) {
	p.calls = append(p.calls, "allowed")
	return p.name != "disabled", nil
}
func (p *plannerOrderPolicy) Capture(context.Context, string) (*modelread.Ticket, error) {
	p.calls = append(p.calls, "capture")
	if p.name == "capture-disabled" {
		return nil, nil
	}
	return p.ticket, nil
}
func (p *plannerOrderPolicy) Assert(_ context.Context, t *modelread.Ticket) error {
	p.calls = append(p.calls, "assert")
	if t != p.ticket && !(p.name == "capture-disabled" && t == nil) {
		return errors.New("wrong ticket")
	}
	if p.name == "assert-failure" || p.name == "capture-disabled" {
		return errors.New("memory_read_revoked")
	}
	return nil
}
func (p *plannerOrderPolicy) Begin(context.Context, *botruntime.TurnContext) (*modelread.Ticket, error) {
	p.calls = append(p.calls, "begin")
	if p.name == "begin-failure" {
		return nil, errors.New("memory_read_revoked")
	}
	return p.ticket, nil
}

// This boundary-order test deliberately instruments policy calls, as the actual
// Engine source oracle does. It is not evidence for SQL policy implementation.
func TestExperiencePlannerActualSourceOrder(t *testing.T) {
	raw, err := os.ReadFile("testdata/experience-planner-source.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Cases []struct {
			Name   string
			Calls  []string
			Result any
			Error  *string
		}
	}
	if err = json.Unmarshal(raw, &fixture); err != nil {
		t.Fatal(err)
	}
	for _, spec := range fixture.Cases {
		t.Run(spec.Name, func(t *testing.T) {
			p := &plannerOrderPolicy{name: spec.Name, calls: []string{}, ticket: new(modelread.Ticket)}
			c := &botruntime.TurnContext{Mode: "real"}
			if spec.Name == "simulation" {
				c.Mode = "simulation"
			}
			c.Scene.ID = "s"
			value, err := runExperiencePlanner(context.Background(), c, p, func() (any, error) {
				p.calls = append(p.calls, "planner")
				if spec.Name == "empty" {
					return nil, nil
				}
				return map[string]any{"experienceHints": []any{}}, nil
			})
			if !reflect.DeepEqual(p.calls, spec.Calls) {
				t.Fatalf("calls %v want %v", p.calls, spec.Calls)
			}
			if spec.Error == nil {
				if err != nil {
					t.Fatal(err)
				}
			} else if err == nil || err.Error() != *spec.Error {
				t.Fatalf("error %v want %s", err, *spec.Error)
			}
			if !reflect.DeepEqual(value, spec.Result) {
				t.Fatalf("result %#v want %#v", value, spec.Result)
			}
		})
	}
}
