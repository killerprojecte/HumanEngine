package assertionauthority

import (
	"errors"

	"github.com/killerprojecte/HumanEngine/backend/internal/assertions"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
)

var ErrProofTraversalLimit = errors.New("memory_proof_budget_exhausted")

type path map[string]bool
type positiveCache map[string]bool
type budget struct{ remaining int }

func (b *budget) spend(n int) error {
	b.remaining -= n
	if b.remaining < 0 {
		return ErrProofTraversalLimit
	}
	return nil
}
func extend(p path, id string) path {
	n := make(path, len(p)+1)
	for k, v := range p {
		n[k] = v
	}
	n[id] = true
	return n
}

type parentTraversal struct {
	budget
	memo, provenance positiveCache
	provenanceBudget budget
}

func newParentTraversal() *parentTraversal {
	return &parentTraversal{budget: budget{512}, memo: positiveCache{}, provenance: positiveCache{}, provenanceBudget: budget{4096}}
}

func (a *Authority) Supported(memory *Object) (bool, error) {
	if e := a.read.CheckScope(); e != nil {
		return false, e
	}
	return a.supported(memory, path{}, positiveCache{}, &budget{4096})
}
func (a *Authority) supported(memory *Object, p path, cache positiveCache, b *budget) (bool, error) {
	if e := b.spend(1); e != nil {
		return false, e
	}
	if len(p) >= 64 {
		return false, ErrProofTraversalLimit
	}
	id := str(get(memory, "id"))
	status := get(memory, "status")
	if (status != "active" && status != "disputed" && status != "superseded") || get(memory, "lineageStatus") == "quarantined" || p[id] {
		return false, nil
	}
	if cache[id] {
		return true, nil
	}
	if get(memory, "lineageStatus") != "verified" {
		return true, nil
	}
	proofs, e := a.read.Proofs(id)
	if e != nil {
		return false, e
	}
	for _, value := range proofs {
		if e = b.spend(1); e != nil {
			return false, e
		}
		proof := object(value)
		if proof == nil {
			return false, errors.New("assertion_proof_object_required")
		}
		if truthy(get(proof, "invalidatedBy")) {
			continue
		}
		sources, e := fieldArray(proof, "sources")
		if e != nil {
			return false, e
		}
		if len(sources) == 0 || ParentBindingState(proof) == "invalid" {
			continue
		}
		available := true
		for _, source := range sources {
			if e = b.spend(1); e != nil {
				return false, e
			}
			recalled, e := a.recalled(source)
			if e != nil {
				return false, e
			}
			if recalled {
				available = false
				break
			}
		}
		if !available {
			continue
		}
		parents, _ := fieldArray(proof, "parents")
		for _, value := range parents {
			if e = b.spend(1); e != nil {
				return false, e
			}
			parent, e := a.read.Record(str(value))
			if e != nil {
				return false, e
			}
			if parent == nil {
				available = false
				break
			}
			allowed, e := a.parentAllowed(memory, parent, proof)
			if e != nil {
				return false, e
			}
			if !allowed {
				available = false
				break
			}
			ok, e := a.supported(parent, extend(p, id), cache, b)
			if e != nil {
				return false, e
			}
			if !ok {
				available = false
				break
			}
		}
		if available {
			cache[id] = true
			return true, nil
		}
	}
	// A cycle/path-dependent failure must not poison another OR alternative.
	return false, nil
}

func (a *Authority) parentAllowed(child, parent, proof *Object) (bool, error) {
	if !ParentScopeAllows(object(get(child, "scope")), object(get(parent, "scope"))) {
		return false, nil
	}
	ref := parentReference(proof, str(get(parent, "id")))
	if ref == nil {
		return get(parent, "status") != "superseded", nil
	}
	state, e := a.read.State(str(get(parent, "id")))
	if e != nil {
		return false, e
	}
	if state.Deleted || float64(state.DeletionEpoch) != get(ref, "deletionEpoch") {
		return false, nil
	}
	rev, e := a.read.Revision(str(get(parent, "id")), get(ref, "version").(float64), scopeKey(get(parent, "scope")))
	if e != nil || rev == nil {
		return false, e
	}
	record := object(get(rev, "record"))
	if !ParentScopeAllows(object(get(child, "scope")), object(get(record, "scope"))) || get(record, "lineageStatus") == "quarantined" || get(record, "status") == "invalidated" {
		return false, nil
	}
	return a.sourcesNotRecalled(get(record, "sources"), nil)
}
func (a *Authority) validParent(scope *Object, id string, cache positiveCache, b *budget) (bool, error) {
	parent, e := a.read.Record(id)
	if e != nil || parent == nil {
		return false, e
	}
	ok, e := a.supported(parent, path{}, cache, b)
	if e != nil || !ok {
		return false, e
	}
	return ParentScopeAllows(scope, object(get(parent, "scope"))), nil
}

// ParentResult is a read-applicability diagnosis, never a purge instruction.
type ParentResult struct {
	Available bool   `json:"available"`
	Reason    string `json:"reason"`
	ParentID  string `json:"parentId,omitempty"`
}
type ParentPorts struct {
	Current  func(string) (*assertions.State, error)
	Revision func(*Object) (bool, error)
}

func EvaluateProofParents(proof *Object, mode string, ports ParentPorts) (ParentResult, error) {
	no := func(reason, id string) (ParentResult, error) { return ParentResult{Reason: reason, ParentID: id}, nil }
	if truthy(get(proof, "invalidatedBy")) {
		return no("invalidated", "")
	}
	binding := ParentBindingState(proof)
	if binding == "invalid" {
		return no("invalid_binding", "")
	}
	parents, _ := fieldArray(proof, "parents")
	for _, value := range parents {
		id := str(value)
		state, e := ports.Current(id)
		if e != nil {
			return ParentResult{}, e
		}
		if state == nil || state.Deleted {
			return no("parent_unavailable", id)
		}
		if binding == "legacy_unknown" {
			continue
		}
		ref := parentReference(proof, id)
		if float64(state.DeletionEpoch) != get(ref, "deletionEpoch") {
			return no("parent_epoch_changed", id)
		}
		if mode == "current" && float64(state.Version) != get(ref, "version") {
			return no("parent_version_changed", id)
		}
		ok, e := ports.Revision(ref)
		if e != nil {
			return ParentResult{}, e
		}
		if !ok {
			return no("parent_revision_unavailable", id)
		}
	}
	return ParentResult{Available: true, Reason: binding}, nil
}
func (a *Authority) ProofParentsAvailable(scope, proof *Object, mode string) (bool, error) {
	if e := a.read.CheckScope(); e != nil {
		return false, e
	}
	return a.boundParents(scope, proof, mode, path{}, newParentTraversal())
}
func (a *Authority) boundParents(scope, proof *Object, mode string, p path, t *parentTraversal) (bool, error) {
	if len(p) >= 64 {
		return false, ErrProofTraversalLimit
	}
	if e := t.spend(1); e != nil {
		return false, e
	}
	current := func(id string) (*Object, *assertions.State, error) {
		record, e := a.read.Record(id)
		if e != nil || record == nil {
			return nil, nil, e
		}
		ok, e := a.validParent(scope, id, t.provenance, &t.provenanceBudget)
		if e != nil || !ok {
			return nil, nil, e
		}
		state, e := a.read.State(id)
		return record, &state, e
	}
	result, e := EvaluateProofParents(proof, mode, ParentPorts{
		Current: func(id string) (*assertions.State, error) {
			if e := t.spend(1); e != nil {
				return nil, e
			}
			_, state, e := current(id)
			return state, e
		},
		Revision: func(ref *Object) (bool, error) {
			if e := t.spend(1); e != nil {
				return false, e
			}
			key, e := jsjson.Stringify([]any{get(ref, "id"), get(ref, "version"), get(ref, "deletionEpoch")})
			if e != nil {
				return false, e
			}
			if p[key] {
				return false, nil
			}
			parent, _, e := current(str(get(ref, "id")))
			if e != nil || parent == nil {
				return false, e
			}
			memoKey, e := jsjson.Stringify([]any{scope, key})
			if e != nil {
				return false, e
			}
			if t.memo[memoKey] {
				return true, nil
			}
			revision, e := a.read.Revision(str(get(ref, "id")), get(ref, "version").(float64), scopeKey(get(parent, "scope")))
			if e != nil || revision == nil {
				return false, e
			}
			record := object(get(revision, "record"))
			if !ParentScopeAllows(scope, object(get(record, "scope"))) || get(record, "lineageStatus") == "quarantined" || get(record, "status") == "invalidated" {
				return false, nil
			}
			ok, e := a.sourcesNotRecalled(get(record, "sources"), &t.budget)
			if e != nil || !ok {
				return false, e
			}
			available := get(record, "lineageStatus") != "verified"
			if !available {
				supports, e := fieldArray(revision, "supports")
				if e != nil {
					return false, e
				}
				for _, v := range supports {
					if e = t.spend(1); e != nil {
						return false, e
					}
					support := object(v)
					if support == nil {
						return false, errors.New("assertion_proof_object_required")
					}
					if truthy(get(support, "invalidatedBy")) {
						continue
					}
					sources, e := fieldArray(support, "sources")
					if e != nil {
						return false, e
					}
					if len(sources) == 0 {
						continue
					}
					ok, e := a.sourcesNotRecalled(sources, &t.budget)
					if e != nil {
						return false, e
					}
					if !ok {
						continue
					}
					ok, e = a.boundParents(object(get(record, "scope")), support, "history", extend(p, key), t)
					if e != nil {
						return false, e
					}
					if ok {
						available = true
						break
					}
				}
			}
			if available {
				t.memo[memoKey] = true
			}
			return available, nil
		},
	})
	return result.Available, e
}
func (a *Authority) sourcesNotRecalled(value any, b *budget) (bool, error) {
	sources, e := array(value)
	if e != nil {
		return false, e
	}
	for _, source := range sources {
		if b != nil {
			if e = b.spend(1); e != nil {
				return false, e
			}
		}
		recalled, e := a.recalled(source)
		if e != nil {
			return false, e
		}
		if recalled {
			return false, nil
		}
	}
	return true, nil
}
