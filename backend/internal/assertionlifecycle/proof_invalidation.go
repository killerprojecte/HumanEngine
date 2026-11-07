// Package assertionlifecycle implements transaction-local proof invalidation.
// It is not the complete assertion recall/delete service.
package assertionlifecycle

import (
	"context"
	"errors"

	"github.com/killerprojecte/HumanEngine/backend/internal/assertionauthority"
	"github.com/killerprojecte/HumanEngine/backend/internal/assertions"
	"github.com/killerprojecte/HumanEngine/backend/internal/recovery"
)

type Object = assertions.Object
type IDSet map[string]struct{}

// Owners are mandatory, ordered effects of the still-separate privacy domains.
// Production composition must supply real implementations, not no-op adapters.
// Each callback receives the exact active Store and may bind its owner through
// WithWriteSession. They must not change current proofs during a purge traversal.
type Owners struct {
	// Source order: relations, projection processor, working-set unavailable.
	RemoveProjection func(*assertions.Store, string) error
	// Source order: relations and working-set unavailable, before history purge.
	BeforePurgeHistory func(*assertions.Store, string) error
	// Source order: knowledge and semantic purge, before the current row write.
	BeforeInvalidateRecord func(*assertions.Store, string) error
}

type ProofInvalidation struct {
	tx        *assertions.RevisionTxn
	owners    Owners
	authority assertionauthority.Options
}

// New binds an already-owned revision transaction. No independent transaction,
// authority, connection or global current-session lookup is created here.
func New(tx *assertions.RevisionTxn, owners Owners, options assertionauthority.Options) (*ProofInvalidation, error) {
	if tx == nil || tx.Store == nil || tx.Repository == nil || owners.RemoveProjection == nil || owners.BeforePurgeHistory == nil || owners.BeforeInvalidateRecord == nil {
		return nil, errors.New("assertion_lifecycle_dependencies_required")
	}
	if e := tx.Store.WithWriteSession(func(*recovery.WriteSession) error {
		return tx.Store.Read(context.Background(), func(r *assertions.Reader) error {
			_, e := assertionauthority.New(r, options)
			return e
		})
	}); e != nil {
		return nil, e
	}
	return &ProofInvalidation{tx, owners, options}, nil
}

// DependsOn preserves source proof/parent order and the caller's shared visited
// set, while using an iterative DFS rather than a growing language call stack.
func (p *ProofInvalidation) DependsOn(id, wanted string, seen IDSet) (found bool, err error) {
	if seen == nil {
		seen = IDSet{}
	}
	err = p.tx.Store.Read(context.Background(), func(r *assertions.Reader) error {
		if e := r.CheckScope(); e != nil {
			return e
		}
		type frame struct {
			id          string
			entered     bool
			proofs      []any
			proofIndex  int
			parents     []any
			parentIndex int
		}
		stack := []frame{{id: id}}
		for len(stack) > 0 {
			current := &stack[len(stack)-1]
			if !current.entered {
				if current.id == wanted {
					found = true
					return nil
				}
				if _, exists := seen[current.id]; exists {
					stack = stack[:len(stack)-1]
					continue
				}
				seen[current.id] = struct{}{}
				var e error
				current.proofs, e = r.Proofs(current.id)
				if e != nil {
					return e
				}
				current.entered = true
			}
			if current.parentIndex < len(current.parents) {
				parent, ok := current.parents[current.parentIndex].(string)
				if !ok {
					return errors.New("assertion_parent_id_required")
				}
				current.parentIndex++
				stack = append(stack, frame{id: parent})
				continue
			}
			if current.proofIndex == len(current.proofs) {
				stack = stack[:len(stack)-1]
				continue
			}
			var e error
			current.parents, e = proofParents(current.proofs[current.proofIndex])
			if e != nil {
				return e
			}
			current.proofIndex++
			current.parentIndex = 0
		}
		return nil
	})
	return
}

// PurgeHistoryDependents snapshots current reverse links once per call. History
// links remain live and are read before each node is purged. The caller may share
// seen/alreadyPurged across related roots in the same synchronous operation.
func (p *ProofInvalidation) PurgeHistoryDependents(id string, seen, alreadyPurged IDSet) error {
	if seen == nil {
		seen = IDSet{}
	}
	return p.tx.Store.Read(context.Background(), func(r *assertions.Reader) error {
		if e := r.CheckScope(); e != nil {
			return e
		}
		if _, exists := seen[id]; exists {
			return nil
		}
		records, e := allRecords(r)
		if e != nil {
			return e
		}
		graph, e := currentDependents(r, records)
		if e != nil {
			return e
		}
		return p.purgeWithGraph(r, id, seen, alreadyPurged, graph)
	})
}

func currentDependents(r *assertions.Reader, records []*Object) (map[string][]string, error) {
	graph := map[string][]string{}
	seen := map[string]IDSet{}
	for _, record := range records {
		id := text(record, "id")
		proofs, e := r.Proofs(id)
		if e != nil {
			return nil, e
		}
		for _, proof := range proofs {
			parents, e := proofParents(proof)
			if e != nil {
				return nil, e
			}
			for _, value := range parents {
				parent, ok := value.(string)
				// Non-string keys cannot be reached by an assertion ID or a
				// historical SQL ID. Do not reject unrelated malformed links.
				if !ok {
					continue
				}
				if seen[parent] == nil {
					seen[parent] = IDSet{}
				}
				if _, exists := seen[parent][id]; !exists {
					seen[parent][id] = struct{}{}
					graph[parent] = append(graph[parent], id)
				}
			}
		}
	}
	return graph, nil
}

func (p *ProofInvalidation) purgeWithGraph(r *assertions.Reader, id string, seen, alreadyPurged IDSet, graph map[string][]string) error {
	pending := []string{id}
	for len(pending) > 0 {
		current := pending[len(pending)-1]
		pending = pending[:len(pending)-1]
		if _, exists := seen[current]; exists {
			continue
		}
		seen[current] = struct{}{}
		children, e := r.Dependents(current)
		if e != nil {
			return e
		}
		childSet := IDSet{}
		for _, child := range children {
			childSet[child] = struct{}{}
		}
		for _, child := range graph[current] {
			if _, exists := childSet[child]; !exists {
				childSet[child] = struct{}{}
				children = append(children, child)
			}
		}
		if e = p.owners.RemoveProjection(p.tx.Store, current); e != nil {
			return e
		}
		if _, purged := alreadyPurged[current]; !purged {
			if e = p.owners.BeforePurgeHistory(p.tx.Store, current); e != nil {
				return e
			}
			if _, e = p.tx.Store.Purge(current); e != nil {
				return e
			}
		}
		record, e := r.Record(current)
		if e != nil {
			return e
		}
		if record != nil {
			if e = p.tx.Touch(current); e != nil {
				return e
			}
		}
		for i := len(children) - 1; i >= 0; i-- {
			pending = append(pending, children[i])
		}
	}
	return nil
}

// InvalidateDependents preserves independent alternatives and proof metadata.
// A nil predicate selects every dependent proof. Like the source, the selector
// is called by the preliminary short-circuit scan, then again while mapping all
// proofs for a changed row. Predicate failures propagate before SaveSupports.
func (p *ProofInvalidation) InvalidateDependents(id string, shouldInvalidate func(*Object) (bool, error)) error {
	if shouldInvalidate == nil {
		shouldInvalidate = func(*Object) (bool, error) { return true, nil }
	}
	selected := func(value any) (bool, error) {
		parents, e := proofParents(value)
		if e != nil {
			return false, e
		}
		for _, parent := range parents {
			if parentID, ok := parent.(string); ok && parentID == id {
				return shouldInvalidate(value.(*Object))
			}
		}
		return false, nil
	}
	return p.tx.Store.Read(context.Background(), func(r *assertions.Reader) error {
		records, e := allRecords(r)
		if e != nil {
			return e
		}
		for _, record := range records {
			proofs, e := r.Proofs(text(record, "id"))
			if e != nil {
				return e
			}
			changed := false
			for _, value := range proofs {
				yes, e := selected(value)
				if e != nil {
					return e
				}
				if yes {
					changed = true
					break
				}
			}
			if !changed {
				continue
			}
			next := append([]any{}, proofs...)
			for i, value := range proofs {
				yes, e := selected(value)
				if e != nil {
					return e
				}
				if yes {
					copy := value.(*Object).Clone()
					_ = copy.Set("invalidatedBy", id)
					next[i] = copy
				}
			}
			if e = p.tx.Repository.SaveSupports(text(record, "id"), next); e != nil {
				return e
			}
		}
		return nil
	})
}

// InvalidateLegacyDependents is the source content-change selector. Absent
// parentReferences is distinct from an explicitly null/empty binding value.
func (p *ProofInvalidation) InvalidateLegacyDependents(id string) error {
	return p.InvalidateDependents(id, func(proof *Object) (bool, error) {
		_, present := proof.Get("parentReferences")
		return !present, nil
	})
}

// RefreshSupport must run inside the caller's revision transaction. Complete
// all provenance analysis and the current graph before the first irreversible
// intent. A read/budget failure therefore cannot apply only a prefix of the plan.
func (p *ProofInvalidation) RefreshSupport() (invalidated []string, err error) {
	invalidated = []string{}
	err = p.tx.Store.Read(context.Background(), func(reader *assertions.Reader) error {
		return reader.WithSupportReadStatements(func(r *assertions.Reader) error {
			records, e := allRecords(r)
			if e != nil {
				return e
			}
			candidates := []*Object{}
			for _, record := range records {
				status := text(record, "status")
				if text(record, "lineageStatus") == "verified" && (status == "active" || status == "disputed") {
					candidates = append(candidates, record)
				}
			}
			authority, e := assertionauthority.New(r, p.authority)
			if e != nil {
				return e
			}
			available, e := authority.AnalyzeSupported(candidates)
			if e != nil {
				return e
			}
			unsupported := []*Object{}
			for i, record := range candidates {
				if !available[i] {
					unsupported = append(unsupported, record)
				}
			}
			if len(unsupported) == 0 {
				return nil
			}
			graph, e := currentDependents(r, records)
			if e != nil {
				return e
			}
			purged := IDSet{}
			for _, record := range unsupported {
				id := text(record, "id")
				if e = p.purgeWithGraph(r, id, purged, nil, graph); e != nil {
					return e
				}
				if e = p.owners.BeforeInvalidateRecord(p.tx.Store, id); e != nil {
					return e
				}
				next := record.Clone()
				_ = next.Set("status", "invalidated")
				if e = p.tx.Repository.Write(next); e != nil {
					return e
				}
				if e = p.tx.Store.RemoveVector(id); e != nil {
					return e
				}
				invalidated = append(invalidated, id)
			}
			return nil
		})
	})
	if err != nil {
		return nil, err
	}
	return
}

func allRecords(r *assertions.Reader) ([]*Object, error) {
	values, e := r.All()
	if e != nil {
		return nil, e
	}
	records := make([]*Object, len(values))
	for i, value := range values {
		var ok bool
		records[i], ok = value.(*Object)
		if !ok || records[i] == nil {
			return nil, errors.New("assertion_record_object_required")
		}
	}
	return records, nil
}

func proofParents(value any) ([]any, error) {
	proof, ok := value.(*Object)
	if !ok || proof == nil {
		return nil, errors.New("assertion_proof_object_required")
	}
	v, _ := proof.Get("parents")
	values, ok := v.([]any)
	if !ok {
		return nil, errors.New("assertion_array_required")
	}
	return values, nil
}

func text(value *Object, key string) string {
	v, _ := value.Get(key)
	s, _ := v.(string)
	return s
}
