// Package scenestore owns scene persistence policy. Transport schema validation
// is separate, just as Store.saveScene receives an already typed Scene.
package scenestore

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"math"

	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/recovery"
)

const maxSafeInteger = 9007199254740991

var flagKeys = []string{"participationEnabled", "learningEnabled", "proactiveEnabled", "memoryReadEnabled", "personalizationEnabled"}

type Plan struct {
	Next          *jsjson.Object
	Intent        *recovery.SceneFlags
	MinimumReader int
}

// Prepare preserves missing controls, discards caller-supplied revisions and
// computes the next revision from the transaction's previous scene only.
// Inputs must not be mutated concurrently. Neither input is modified.
func Prepare(previous, input *jsjson.Object) (Plan, error) {
	if input == nil {
		return Plan{}, errors.New("scene_required")
	}
	next := input.Clone()
	if previous != nil {
		for _, key := range []string{"memoryReadEnabled", "personalizationEnabled"} {
			if _, present := next.Get(key); !present {
				if value, exists := previous.Get(key); exists {
					_ = next.Set(key, value)
				}
			}
		}
	}
	revision := float64(0)
	if previous != nil {
		if value, present := previous.Get("memoryReadRevision"); present && value != nil {
			var ok bool
			revision, ok = value.(float64)
			if !ok {
				return Plan{}, errors.New("memory_read_policy_invalid")
			}
		}
	}
	if math.IsNaN(revision) || math.IsInf(revision, 0) || revision < 0 || revision > maxSafeInteger || math.Trunc(revision) != revision {
		return Plan{}, errors.New("memory_read_policy_invalid")
	}
	identity, err := IdentityHash(next)
	if err != nil {
		return Plan{}, err
	}
	changed := false
	previousHash := ""
	if previous != nil {
		previousHash, err = IdentityHash(previous)
		if err != nil {
			return Plan{}, err
		}
		changed = enabled(previous, "memoryReadEnabled") != enabled(next, "memoryReadEnabled") || enabled(previous, "personalizationEnabled") != enabled(next, "personalizationEnabled") || previousHash != identity
	}
	next.Delete("memoryReadRevision")
	if revision != 0 || changed {
		if revision >= maxSafeInteger {
			return Plan{}, errors.New("memory_read_policy_invalid")
		}
		if changed {
			revision++
		}
		_ = next.Set("memoryReadRevision", revision)
	}
	plan := Plan{Next: next, MinimumReader: 1}
	for _, key := range []string{"memoryReadEnabled", "personalizationEnabled", "memoryReadRevision"} {
		if _, present := next.Get(key); present {
			plan.MinimumReader = 5
		}
	}
	if previous != nil {
		flags, err := sceneFlags(next)
		if err != nil {
			return Plan{}, err
		}
		oldFlags, err := sceneFlags(previous)
		if err != nil {
			return Plan{}, err
		}
		different := previousHash != identity
		for _, key := range flagKeys {
			if flags[key] != oldFlags[key] {
				different = true
			}
		}
		if different {
			id, ok := value(next, "id").(string)
			if !ok {
				return Plan{}, errors.New("scene_id_invalid")
			}
			plan.Intent = &recovery.SceneFlags{SchemaVersion: 2, SceneID: id, IdentityHash: identity, Flags: flags}
		}
	}
	return plan, nil
}
func value(o *jsjson.Object, key string) any    { v, _ := o.Get(key); return v }
func enabled(o *jsjson.Object, key string) bool { v, ok := value(o, key).(bool); return !ok || v }
func sceneFlags(scene *jsjson.Object) (map[string]bool, error) {
	result := make(map[string]bool, len(flagKeys))
	for _, key := range flagKeys {
		if key == "memoryReadEnabled" || key == "personalizationEnabled" {
			result[key] = enabled(scene, key)
			continue
		}
		v, ok := value(scene, key).(bool)
		if !ok {
			return nil, errors.New("scene_flags_invalid")
		}
		result[key] = v
	}
	return result, nil
}
func IdentityHash(scene *jsjson.Object) (string, error) {
	identity := []any{}
	for _, key := range []string{"id", "adapterId", "accountId", "platformSceneId", "kind"} {
		identity = append(identity, value(scene, key))
	}
	raw, err := jsjson.Stringify(identity)
	if err != nil {
		return "", err
	}
	hash := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(hash[:]), nil
}
