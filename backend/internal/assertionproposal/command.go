// Package assertionproposal composes only the inner assertion proposal write.
// Typed knowledge, relation and commitment admission belongs to the outer owner.
package assertionproposal

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"github.com/killerprojecte/HumanEngine/backend/internal/assertionauthority"
	"github.com/killerprojecte/HumanEngine/backend/internal/assertionlifecycle"
	"github.com/killerprojecte/HumanEngine/backend/internal/assertions"
	"github.com/killerprojecte/HumanEngine/backend/internal/reconciliation"
	"github.com/killerprojecte/HumanEngine/backend/internal/sourceadmission"
	"math"
	"time"
)

type Result struct {
	Status string  `json:"status"`
	Reason string  `json:"reason,omitempty"`
	Memory *Object `json:"memory,omitempty"`
}
type Runtime struct {
	ID  func() (string, error)
	Now func() string
}
type Command struct {
	owners  assertionlifecycle.Owners
	options assertionauthority.Options
	runtime Runtime
}

func New(owners assertionlifecycle.Owners, options assertionauthority.Options, runtime Runtime) (*Command, error) {
	if owners.RemoveProjection == nil || owners.BeforePurgeHistory == nil || owners.BeforeInvalidateRecord == nil || options.DateParser == nil || options.Compare == nil {
		return nil, errors.New("assertion_proposal_dependencies_required")
	}
	if runtime.ID == nil {
		runtime.ID = uuid
	}
	if runtime.Now == nil {
		runtime.Now = func() string { return time.Now().UTC().Format("2006-01-02T15:04:05.000Z") }
	}
	return &Command{owners, options, runtime}, nil
}
func uuid() (string, error) {
	var b [16]byte
	if _, e := rand.Read(b[:]); e != nil {
		return "", e
	}
	b[6] = (b[6] & 15) | 64
	b[8] = (b[8] & 63) | 128
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[:4], b[4:6], b[6:8], b[8:10], b[10:]), nil
}
func reject(reason string) Result { return Result{Status: "rejected", Reason: reason} }

// ProposeAssertion always creates the actual nested revision savepoint. Its
// result is not a complete MemoryService.Propose and does not admit typed graphs.
func (c *Command) ProposeAssertion(ctx context.Context, parent *assertions.RevisionTxn, input, candidate *Object) (out Result, err error) {
	if truthy(get(input, "simulation")) {
		return reject("simulation_does_not_learn"), nil
	}
	if parent == nil {
		return out, errors.New("assertion_proposal_transaction_required")
	}
	err = parent.Transaction(ctx, func(tx *assertions.RevisionTxn) error {
		lifecycle, e := assertionlifecycle.New(tx, c.owners, c.options)
		if e != nil {
			return e
		}
		return tx.Store.Read(ctx, func(reader *assertions.Reader) error {
			admission, e := sourceadmission.New(reader, c.options)
			if e != nil {
				return e
			}
			a, e := admission.Prepare(input, candidate)
			if e != nil {
				return e
			}
			if a.Status == "rejected" {
				out = reject(a.Reason)
				return nil
			}
			candidate = a.Candidate
			policy, e := reconciliation.New(reader, c.options)
			if e != nil {
				return e
			}
			r, e := policy.Inspect(input, candidate, a.Scope, a.Attribution, a.Parents)
			if e != nil {
				return e
			}
			if r.Status == "rejected" {
				out = reject(r.Reason)
				return nil
			}
			authority, e := assertionauthority.New(reader, c.options)
			if e != nil {
				return e
			}
			correction := func() error {
				if r.Superseded == nil {
					return nil
				}
				record := r.Superseded.Clone()
				_ = record.Set("status", "superseded")
				if e := tx.Repository.Write(record); e != nil {
					return e
				}
				return tx.Store.RemoveVector(str(get(record, "id")))
			}
			saved := func(record *Object) error {
				if r.Superseded != nil {
					if _, e := lifecycle.RefreshSupport(); e != nil {
						return e
					}
				}
				clean, e := authority.Sanitize(record)
				if e != nil {
					return e
				}
				out = Result{Status: "saved", Memory: clean}
				return nil
			}
			var existing *Object
			if r.ExistingRow != nil {
				existing, e = r.ExistingRow.Record()
				if e != nil {
					return e
				}
			}
			if existing != nil {
				id := str(get(existing, "id"))
				for _, p := range a.Parents {
					if p == id {
						out = reject("cyclic_memory_dependencies")
						return nil
					}
				}
				for _, p := range a.Parents {
					depends, e := lifecycle.DependsOn(str(p), id, nil)
					if e != nil {
						return e
					}
					if depends {
						out = reject("cyclic_memory_dependencies")
						return nil
					}
				}
			}
			var history []*Object
			if existing != nil && r.Superseded != nil {
				history, e = correctionHistory(reader, str(get(existing, "id")), r.Superseded)
				if e != nil {
					return e
				}
			}
			reverting := existing != nil && get(existing, "status") == "superseded" && !truthy(get(existing, "locked")) && get(existing, "origin") == "learned" && len(arr(get(existing, "sources"))) > 0
			if reverting {
				for _, s := range arr(get(existing, "sources")) {
					recalled, e := reader.Recalled(str(get(s, "sceneId")), str(get(s, "messageId")))
					if e != nil {
						return e
					}
					if recalled {
						reverting = false
						break
					}
				}
			}
			reverting = reverting && r.Superseded != nil && history != nil
			if reverting {
				for _, s := range arr(get(candidate, "sources")) {
					if inHistory(s, history) {
						reverting = false
						break
					}
				}
			}
			if existing != nil && !reverting {
				if truthy(get(existing, "locked")) || get(existing, "origin") == "manual" || member(get(existing, "status"), "superseded", "invalidated") || get(existing, "lineageStatus") == "quarantined" {
					out = reject("manual_or_invalid_record")
					return nil
				}
				if truthy(get(existing, "supersedesId")) {
					history, e := correctionHistory(reader, str(get(existing, "id")), existing)
					if e != nil {
						return e
					}
					if history == nil {
						out = reject("invalid_source_evidence")
						return nil
					}
					for _, s := range arr(get(candidate, "sources")) {
						if !inSources(s, arr(get(existing, "sources"))) && inHistory(s, history[1:]) {
							out = reject("invalid_source_evidence")
							return nil
						}
					}
				}
				sources, e := UniqueSources(append(append([]any{}, arr(get(existing, "sources"))...), arr(get(candidate, "sources"))...))
				if e != nil {
					return e
				}
				confidence, numeric := get(existing, "confidence").(float64)
				if !numeric {
					confidence = math.NaN()
				}
				if senders(sources) > senders(arr(get(existing, "sources"))) {
					confidence += 0.05
				}
				next := existing.Clone()
				_ = next.Set("sources", sources)
				_ = next.Set("confidence", math.Min(0.85, confidence))
				if e = ValidateMemory(next, c.options.DateParser); e != nil {
					return e
				}
				if e = correction(); e != nil {
					return e
				}
				if e = tx.Repository.Write(next); e != nil {
					return e
				}
				if truthy(get(input, "provenance")) {
					fp, e := assertions.Fingerprint(existing)
					if e != nil {
						return e
					}
					if r.FP == fp {
						proofs, e := reader.Proofs(str(get(existing, "id")))
						if e != nil {
							return e
						}
						if e = tx.Repository.SaveSupports(str(get(existing, "id")), append(proofs, a.Proof)); e != nil {
							return e
						}
						_ = next.Set("lineageStatus", "verified")
						if e = tx.Repository.Write(next); e != nil {
							return e
						}
					}
				}
				return saved(next)
			}
			id, e := c.runtime.ID()
			if e != nil {
				return e
			}
			sources, e := UniqueSources(arr(get(candidate, "sources")))
			if e != nil {
				return e
			}
			importance := get(candidate, "importance")
			if importance == nil {
				importance = "medium"
			}
			confidence := get(candidate, "confidence")
			if confidence == nil {
				confidence = float64(0.5)
			}
			number, ok := confidence.(float64)
			if !ok {
				number = math.NaN()
			}
			memory := obj("id", id, "scope", a.Scope, "content", trim(str(get(candidate, "content"))), "kind", get(candidate, "kind"), "sources", sources, "importance", importance, "confidence", math.Min(number, 0.7), "learnedAt", c.runtime.Now(), "status", "active", "locked", false, "origin", "learned")
			if truthy(get(input, "internalDeclaration")) {
				use := "commitment_declaration"
				if get(input, "internalDeclaration") == "knowledge_declaration" {
					use = "knowledge_declaration"
				}
				_ = memory.Set("internalUse", use)
			}
			lineage := "unverified"
			if truthy(get(input, "provenance")) {
				lineage = "verified"
			}
			_ = memory.Set("lineageStatus", lineage)
			for _, key := range []string{"validFrom", "epistemicKind", "occurredAt", "validUntil", "supersedesId"} {
				if truthy(get(candidate, key)) {
					_ = memory.Set(key, get(candidate, key))
				}
			}
			if e = ValidateMemory(memory, c.options.DateParser); e != nil {
				return e
			}
			if reverting {
				if e = tx.Store.RetireProposalFingerprint(existing); e != nil {
					return e
				}
			}
			if e = correction(); e != nil {
				return e
			}
			if e = tx.Repository.Write(memory); e != nil {
				return e
			}
			if truthy(get(input, "provenance")) {
				proof := a.Proof.Clone()
				_ = proof.Set("sources", sources)
				if e = tx.Repository.SaveSupports(id, []any{proof}); e != nil {
					return e
				}
			}
			return saved(memory)
		})
	})
	if err != nil {
		return Result{}, err
	}
	return
}
func correctionHistory(reader *assertions.Reader, id string, current *Object) ([]*Object, error) {
	visited := map[string]bool{}
	history := []*Object{}
	found := false
	previous := current
	for previous != nil && !visited[str(get(previous, "id"))] {
		history = append(history, previous)
		key := str(get(previous, "id"))
		if key == id {
			found = true
		}
		visited[key] = true
		if !truthy(get(previous, "supersedesId")) {
			break
		}
		var e error
		previous, e = reader.Record(str(get(previous, "supersedesId")))
		if e != nil {
			return nil, e
		}
		if previous == nil {
			return nil, nil
		}
	}
	if found && previous != nil && !truthy(get(previous, "supersedesId")) {
		return history, nil
	}
	return nil, nil
}
func inSources(source any, sources []any) bool {
	for _, s := range sources {
		if get(source, "sceneId") == get(s, "sceneId") && get(source, "messageId") == get(s, "messageId") {
			return true
		}
	}
	return false
}
func inHistory(source any, history []*Object) bool {
	for _, r := range history {
		if inSources(source, arr(get(r, "sources"))) {
			return true
		}
	}
	return false
}
func senders(sources []any) int {
	seen := map[any]bool{}
	for _, s := range sources {
		seen[get(s, "senderId")] = true
	}
	return len(seen)
}
