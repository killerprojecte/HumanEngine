// Package reconciliation owns synchronous proposal reconciliation. Its result
// is a decision, not a durable permission or mutation plan.
package reconciliation

import (
	"errors"
	"github.com/killerprojecte/HumanEngine/backend/internal/assertionauthority"
	"github.com/killerprojecte/HumanEngine/backend/internal/assertions"
	"time"
)

type Policy struct {
	reader  *assertions.Reader
	options assertionauthority.Options
}
type Result struct {
	Status      string
	Reason      string
	FP          string
	ExistingRow *assertions.AssertionRow
	Superseded  *Object
}

// New binds all reads to one actual publication/write-child capability.
func New(reader *assertions.Reader, options assertionauthority.Options) (*Policy, error) {
	if reader == nil {
		return nil, errors.New("reconciliation_reader_required")
	}
	if options.DateParser == nil {
		return nil, errors.New("reconciliation_time_policy_required")
	}
	if e := reader.CheckScope(); e != nil {
		return nil, e
	}
	return &Policy{reader, options}, nil
}
func (p *Policy) currentProofs(id string) ([]any, error) {
	proofs, e := p.reader.Proofs(id)
	if e != nil {
		return nil, e
	}
	out := []any{}
	for _, proof := range proofs {
		if truthy(get(proof, "invalidatedBy")) {
			continue
		}
		current, e := p.sourcesCurrent(get(proof, "sources"))
		if e != nil {
			return nil, e
		}
		if current {
			out = append(out, proof)
		}
	}
	return out, nil
}
func (p *Policy) sourcesCurrent(v any) (bool, error) {
	sources, ok := v.([]any)
	if !ok {
		return false, errors.New("reconciliation_sources_array_required")
	}
	for _, source := range sources {
		recalled, e := p.reader.Recalled(str(get(source, "sceneId")), str(get(source, "messageId")))
		if e != nil || recalled {
			return false, e
		}
	}
	return true, nil
}
func (p *Policy) protectedSubject(id string) (bool, error) {
	record, e := p.reader.Record(id)
	if e != nil {
		return false, e
	}
	proofs, e := p.currentProofs(id)
	if e != nil {
		return false, e
	}
	ep := get(record, "epistemicKind")
	if record == nil || get(record, "kind") != "claim" || (ep != "reported" && ep != "inferred" && ep != "hypothesis") || len(proofs) == 0 {
		return true, nil
	}
	for _, proof := range proofs {
		if get(get(proof, "subjectWitness"), "kind") != "mentioned" {
			return true, nil
		}
	}
	return false, nil
}

// Inspect preserves source short-circuit ordering, including parsing the raw
// existing preimage only after reported-subject protection has been evaluated.
func (p *Policy) Inspect(ctx, candidate, scope, attribution *Object, parents []any) (Result, error) {
	reject := func(reason string) (Result, error) { return Result{Status: "rejected", Reason: reason}, nil }
	if e := p.reader.CheckScope(); e != nil {
		return Result{}, e
	}
	merging := has(candidate, "mergeIntoId")
	if merging && (!truthy(get(candidate, "mergeIntoId")) || has(candidate, "supersedesId")) {
		return reject("invalid_merge_request")
	}
	fp, e := assertions.Fingerprint(obj("scope", scope, "content", get(candidate, "content")))
	if e != nil {
		return Result{}, e
	}
	deleted, e := p.reader.FingerprintDeleted(fp)
	if e != nil {
		return Result{}, e
	}
	if deleted {
		return reject("manually_deleted")
	}
	key := fp
	if merging {
		key = str(get(candidate, "mergeIntoId"))
	}
	row, e := p.reader.ReconciliationRow(key, merging)
	if e != nil {
		return Result{}, e
	}
	if get(get(attribution, "witness"), "kind") == "mentioned" {
		if row != nil {
			protected, e := p.protectedSubject(row.ID)
			if e != nil {
				return Result{}, e
			}
			if protected {
				return reject("reported_subject_cannot_replace_self")
			}
		}
		if truthy(get(candidate, "supersedesId")) {
			protected, e := p.protectedSubject(str(get(candidate, "supersedesId")))
			if e != nil {
				return Result{}, e
			}
			if protected {
				return reject("reported_subject_cannot_replace_self")
			}
		}
	}
	var prior *Object
	if row != nil {
		prior, e = row.Record()
		if e != nil {
			return Result{}, e
		}
		proofs, e := p.currentProofs(str(get(prior, "id")))
		if e != nil {
			return Result{}, e
		}
		for _, proof := range proofs {
			if !optionalEqual(get(proof, "subjectWitness"), "kind", get(attribution, "witness"), "kind") {
				return reject("incompatible_subject_corroboration")
			}
		}
		if !optionalEqual(prior, "epistemicKind", candidate, "epistemicKind") {
			return reject("incompatible_subject_corroboration")
		}
	}
	if merging {
		if prior != nil && get(ctx, "provenance") == "source-only" {
			targetFP, e := assertions.Fingerprint(prior)
			if e != nil {
				return Result{}, e
			}
			if fp != targetFP {
				return reject("merge_requires_exact_content")
			}
		}
		targetScope := get(prior, "scope")
		if prior == nil || truthy(get(prior, "locked")) || get(prior, "origin") != "learned" || get(prior, "status") != "active" || get(targetScope, "kind") != "scene" || get(targetScope, "sceneId") != get(ctx, "sceneId") || !optionalEqual(targetScope, "personId", scope, "personId") {
			return reject("merge_not_allowed")
		}
		if has(prior, "validUntil") {
			// Invalid Date.parse is NaN: NaN <= now is false, not expiration.
			dateValue, e := jsDateString(get(prior, "validUntil"))
			if e != nil {
				return Result{}, e
			}
			ms, valid, e := p.options.DateParser.ParseMillis(dateValue)
			if e != nil {
				return Result{}, e
			}
			now := time.Now().UnixMilli()
			if p.options.NowMillis != nil {
				now = p.options.NowMillis()
			}
			if valid && ms <= now {
				return reject("merge_not_allowed")
			}
		}
		current, e := p.sourcesCurrent(get(prior, "sources"))
		if e != nil {
			return Result{}, e
		}
		if !current {
			return reject("merge_not_allowed")
		}
		deleted, e := p.reader.FingerprintDeleted(row.Fingerprint)
		if e != nil {
			return Result{}, e
		}
		if deleted {
			return reject("merge_not_allowed")
		}
	}
	var superseded *Object
	if truthy(get(candidate, "supersedesId")) {
		previous, e := p.reader.Record(str(get(candidate, "supersedesId")))
		if e != nil {
			return Result{}, e
		}
		previousScope := get(previous, "scope")
		sameScene := get(previousScope, "kind") == "scene" && get(previousScope, "sceneId") == get(ctx, "sceneId") && optionalEqual(previousScope, "personId", candidate, "personId")
		owningPublic := get(previousScope, "kind") == "sharedPublicKnowledge" && get(previous, "origin") == "learned" && get(previous, "status") == "active"
		if owningPublic {
			sources, ok := get(previous, "sources").([]any)
			if !ok {
				return Result{}, errors.New("reconciliation_sources_array_required")
			}
			owningPublic = len(sources) > 0
			for _, source := range sources {
				if get(source, "sceneId") != get(ctx, "sceneId") {
					owningPublic = false
					break
				}
			}
		}
		if previous == nil || truthy(get(previous, "locked")) || get(previous, "origin") == "manual" || (get(previous, "status") != "active" && get(previous, "status") != "disputed") || (!sameScene && !owningPublic) || (row != nil && row.ID == get(previous, "id")) {
			return reject("correction_not_allowed")
		}
		superseded = previous
	}
	if superseded != nil {
		for _, parent := range parents {
			if parent == get(superseded, "id") {
				return reject("correction_depends_on_superseded_memory")
			}
		}
	}
	return Result{Status: "accepted", FP: fp, ExistingRow: row, Superseded: superseded}, nil
}

// Undefined and explicit null differ under the source's strict equality.
func optionalEqual(a any, ak string, b any, bk string) bool {
	return has(a, ak) == has(b, bk) && get(a, ak) == get(b, bk)
}
