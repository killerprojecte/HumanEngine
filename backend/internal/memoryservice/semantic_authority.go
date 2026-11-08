package memoryservice

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"github.com/killerprojecte/HumanEngine/backend/internal/assertionauthority"
	"github.com/killerprojecte/HumanEngine/backend/internal/commitments"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"sort"
)

func semanticHash(v any) (string, error) {
	raw, e := jsjson.Stringify(v)
	if e != nil {
		return "", e
	}
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:]), nil
}
func semanticRef(v *Object) commitments.Reference {
	version, _ := value(v, "version").(float64)
	epoch, _ := value(v, "deletionEpoch").(float64)
	return commitments.Reference{ID: text(v, "id"), Version: version, DeletionEpoch: epoch}
}

// SemanticCurrent is a real narrow semanticAssertion owner: its provenance
// walk and source-head capture share one authentic scoped ReadCurrent. No
// metadata snapshot or supplied record can substitute for current authority.
func (s *Service) SemanticCurrent(ctx context.Context, c Context, ref Reference) (out *Object, err error) {
	err = s.d.Commitments.ReadCurrent(ctx, s.d.Assertions, s.d.CommitmentsEnabled, s.d.Authority, func(read *commitments.ReadService) error {
		var e error
		out, e = semanticCurrent(read, c, refObject(ref))
		return e
	})
	return
}
func semanticCurrent(read *commitments.ReadService, c Context, ref *Object) (*Object, error) {
	if e := read.AssertReady(); e != nil {
		return nil, e
	}
	if c.Simulation {
		return nil, nil
	}
	remaining := 512
	visiting := map[string]bool{}
	snapshots := map[string]any{}
	roots := map[string]bool{}
	var visit func(*Object, int) (*commitments.Assertion, error)
	visit = func(reference *Object, depth int) (*commitments.Assertion, error) {
		remaining--
		if remaining < 0 || depth > 32 {
			return nil, errors.New("semantic_provenance_limit")
		}
		key, e := jsjson.Stringify(reference)
		if e != nil {
			return nil, e
		}
		if visiting[key] {
			return nil, errors.New("semantic_provenance_cycle")
		}
		v, e := read.CurrentAssertion(commitments.Context{SceneID: c.SceneID}, semanticRef(reference))
		if e != nil || v == nil {
			return nil, e
		}
		if truthy(value(v.Record, "internalUse")) || len(v.Proofs) == 0 {
			return nil, nil
		}
		visiting[key] = true
		heads := []any{}
		for _, raw := range v.Proofs {
			proof := object(raw)
			if assertionauthority.ParentBindingState(proof) != "bound" {
				return nil, nil
			}
			sources, _ := value(proof, "sources").([]any)
			for _, rawSource := range sources {
				remaining--
				if remaining < 0 {
					return nil, errors.New("semantic_provenance_limit")
				}
				source := object(rawSource)
				scene, message := text(source, "sceneId"), text(source, "messageId")
				if scene != c.SceneID {
					return nil, nil
				}
				root, e := semanticHash([]any{scene, message})
				if e != nil {
					return nil, e
				}
				roots[root] = true
				head, e := read.SourceHead(scene, message)
				if e != nil {
					return nil, e
				}
				var headValue any
				if head != nil {
					headValue = head
				}
				heads = append(heads, []any{source, headValue})
			}
			parents, _ := value(proof, "parentReferences").([]any)
			for _, parent := range parents {
				v, e := visit(object(parent), depth+1)
				if e != nil || v == nil {
					return nil, e
				}
			}
		}
		delete(visiting, key)
		snapshots[key] = []any{reference, v.Record, v.Proofs, heads}
		return v, nil
	}
	v, e := visit(ref, 0)
	if e != nil || v == nil {
		return nil, e
	}
	if len(roots) == 0 || len(roots) > 128 {
		return nil, nil
	}
	keys := make([]string, 0, len(snapshots))
	for k := range snapshots {
		keys = append(keys, k)
	}
	// Source '<' compares UTF-16 code units, not Unicode scalars or locale.
	units := map[string][]uint16{}
	for _, k := range keys {
		u, e := jsjson.UTF16(k)
		if e != nil {
			return nil, e
		}
		units[k] = u
	}
	sort.Slice(keys, func(i, j int) bool {
		a, b := units[keys[i]], units[keys[j]]
		for n := 0; n < len(a) && n < len(b); n++ {
			if a[n] != b[n] {
				return a[n] < b[n]
			}
		}
		return len(a) < len(b)
	})
	entries := []any{}
	for _, k := range keys {
		entries = append(entries, []any{k, snapshots[k]})
	}
	dependency, e := semanticHash(entries)
	if e != nil {
		return nil, e
	}
	rootKeys := make([]string, 0, len(roots))
	for k := range roots {
		rootKeys = append(rootKeys, k)
	}
	sort.Strings(rootKeys)
	rootValues := []any{}
	for _, k := range rootKeys {
		rootValues = append(rootValues, k)
	}
	out := new(Object)
	_ = out.Set("record", v.Record)
	_ = out.Set("proofs", v.Proofs)
	_ = out.Set("dependencyKey", dependency)
	_ = out.Set("roots", rootValues)
	_ = out.Set("rootCoverage", "complete")
	_ = out.Set("derived", false)
	return out, nil
}
func truthy(v any) bool {
	switch x := v.(type) {
	case nil:
		return false
	case bool:
		return x
	case string:
		return x != ""
	case float64:
		return x != 0
	}
	return true
}
