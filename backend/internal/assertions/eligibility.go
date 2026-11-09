package assertions

import (
	"crypto/rand"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/recovery"
)

func lessUTF16(a, b string) bool {
	left, _ := jsjson.UTF16(a)
	right, _ := jsjson.UTF16(b)
	for i := 0; i < len(left) && i < len(right); i++ {
		if left[i] != right[i] {
			return left[i] < right[i]
		}
	}
	return len(left) < len(right)
}

// Canonical material hashes sort object keys by JS code units, but never reorder
// array elements or discard unknown proof/record fields.
func canonical(v any) (string, error) {
	switch x := v.(type) {
	case *Object:
		keys := x.Keys()
		sort.Slice(keys, func(i, j int) bool { return lessUTF16(keys[i], keys[j]) })
		parts := []string{}
		for _, k := range keys {
			if jsjson.IsUndefined(get(x, k)) {
				continue
			}
			key, e := jsjson.Stringify(k)
			if e != nil {
				return "", e
			}
			value, e := canonical(get(x, k))
			if e != nil {
				return "", e
			}
			parts = append(parts, key+":"+value)
		}
		return "{" + strings.Join(parts, ",") + "}", nil
	case []any:
		parts := make([]string, len(x))
		for i, v := range x {
			if jsjson.IsUndefined(v) {
				continue
			}
			s, e := canonical(v)
			if e != nil {
				return "", e
			}
			parts[i] = s
		}
		return "[" + strings.Join(parts, ",") + "]", nil
	}
	return jsjson.Stringify(v)
}
func EligibilityIncarnation(record *Object) (string, error) {
	raw, e := canonical([]any{get(record, "id"), get(record, "learnedAt")})
	if e != nil {
		return "", e
	}
	return digest(raw), nil
}
func EligibilityMaterial(record *Object, proofs any) (string, error) {
	material := record.Clone()
	material.Delete("status")
	material.Delete("lineageStatus")
	raw, e := canonical([]any{material, proofs})
	if e != nil {
		return "", e
	}
	return digest(raw), nil
}
func eligibilityState(record *Object) recovery.EligibilityState {
	s := recovery.EligibilityState{Status: str(get(record, "status"))}
	if v := get(record, "lineageStatus"); v != nil {
		lineage := str(v)
		s.LineageStatus = &lineage
	}
	return s
}
func denied(s recovery.EligibilityState) bool {
	return s.Status == "invalidated" || s.LineageStatus != nil && *s.LineageStatus == "quarantined"
}
func permissionChanged(a, b *Object) bool {
	return (get(a, "status") == "invalidated") != (get(b, "status") == "invalidated") || (get(a, "lineageStatus") == "quarantined") != (get(b, "lineageStatus") == "quarantined")
}

type EligibilityTransition struct{ Before, After *Object }

// RecordEligibilityChanges emits one final transition batch. It is a live writer
// operation, not maintenance replay or automatic baseline enrollment.
func (s *Store) RecordEligibilityChanges(records []EligibilityTransition) error {
	// Source eligibility.ts suppresses offline transition synthesis entirely.
	if s.maintenance != nil {
		return s.requireTransaction()
	}
	changes := []recovery.EligibilityChange{}
	for _, pair := range records {
		if !permissionChanged(pair.Before, pair.After) {
			continue
		}
		before, e := EligibilityIncarnation(pair.Before)
		if e != nil {
			return e
		}
		after, e := EligibilityIncarnation(pair.After)
		if e != nil {
			return e
		}
		if before != after {
			return errors.New("recovery_eligibility_incarnation_mismatch")
		}
		state := eligibilityState(pair.After)
		change := recovery.EligibilityChange{ID: str(get(pair.After, "id")), IncarnationHash: after, State: state}
		if !denied(state) {
			proofs, e := s.reader().Supports(change.ID)
			if e != nil {
				return e
			}
			hash, e := EligibilityMaterial(pair.After, proofs)
			if e != nil {
				return e
			}
			change.MaterialHash = &hash
		}
		changes = append(changes, change)
	}
	if len(changes) == 0 {
		return nil
	}
	if e := s.requireTransaction(); e != nil {
		return e
	}
	if e := s.requireReader(11); e != nil {
		return e
	}
	var id string
	var e error
	if s.options.OperationID != nil {
		id = s.options.OperationID()
	} else {
		id, e = operationID()
		if e != nil {
			return e
		}
	}
	_, e = s.appendIntent(recovery.AssertionEligibility{OperationID: id, Mode: "transition", Changes: changes})
	return e
}
func operationID() (string, error) {
	var b [16]byte
	if _, e := rand.Read(b[:]); e != nil {
		return "", e
	}
	b[6] = (b[6] & 15) | 64
	b[8] = (b[8] & 63) | 128
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[:4], b[4:6], b[6:8], b[8:10], b[10:]), nil
}
