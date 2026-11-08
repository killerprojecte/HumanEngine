package memorymanual

import (
	"encoding/json"
	"errors"
	"math"
)

// A missing or null JSON precondition is not numeric zero. Typed Go callers
// can explicitly supply zero for the legacy state, just as source callers do.
func (g *Guard) UnmarshalJSON(raw []byte) error {
	var fields map[string]json.RawMessage
	if e := json.Unmarshal(raw, &fields); e != nil {
		return e
	}
	g.ExpectedVersion = math.NaN()
	g.ExpectedDeletionEpoch = math.NaN()
	g.IdempotencyKey = ""
	for key, dest := range map[string]*float64{"expectedVersion": &g.ExpectedVersion, "expectedDeletionEpoch": &g.ExpectedDeletionEpoch} {
		if v, ok := fields[key]; ok && string(v) != "null" {
			if e := json.Unmarshal(v, dest); e != nil {
				return errors.New("invalid_memory_command")
			}
		}
	}
	if v, ok := fields["idempotencyKey"]; ok {
		if e := json.Unmarshal(v, &g.IdempotencyKey); e != nil {
			return errors.New("invalid_memory_command")
		}
	}
	return nil
}
