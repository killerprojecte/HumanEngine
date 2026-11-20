package contract

import (
	"errors"

	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
)

type MemoryPatchGuard struct {
	ExpectedVersion, ExpectedDeletionEpoch float64
	IdempotencyKey                         string
}

func (s *Schemas) ParseMemoryPatch(input any) (*jsjson.Object, *MemoryPatchGuard, error) {
	values, err := s.parseValue("MemoryPatch", input)
	if err != nil {
		return nil, nil, err
	}
	var guard *MemoryPatchGuard
	if raw, ok := values["guard"]; ok {
		fields := raw.(map[string]any)
		version, epoch := fields["expectedVersion"].(float64), fields["expectedDeletionEpoch"].(float64)
		if version > 9007199254740991 || epoch > 9007199254740991 {
			return nil, nil, errors.New("invalid memory guard revision")
		}
		guard = &MemoryPatchGuard{version, epoch, fields["idempotencyKey"].(string)}
	}
	// Zod emits schema order, not wire order; it participates in receipt hashes.
	patch := new(jsjson.Object)
	for _, key := range []string{"content", "locked", "status"} {
		if v, ok := values[key]; ok {
			_ = patch.Set(key, v)
		}
	}
	return patch, guard, nil
}
