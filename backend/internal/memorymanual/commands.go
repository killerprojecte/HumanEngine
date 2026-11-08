// Package memorymanual owns reviewed edits and guarded retries. Automatic
// learning admission remains with the proposal command.
package memorymanual

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"fmt"
	"github.com/killerprojecte/HumanEngine/backend/internal/assertionauthority"
	"github.com/killerprojecte/HumanEngine/backend/internal/assertionlifecycle"
	"github.com/killerprojecte/HumanEngine/backend/internal/assertionproposal"
	"github.com/killerprojecte/HumanEngine/backend/internal/assertions"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"math"
	"strings"
	"time"
)

type Runtime = assertionproposal.Runtime
type Result = assertionproposal.Result
type Context struct {
	SceneID    string
	Simulation bool
}
type Guard struct {
	ExpectedVersion       float64 `json:"expectedVersion"`
	ExpectedDeletionEpoch float64 `json:"expectedDeletionEpoch"`
	IdempotencyKey        string  `json:"idempotencyKey"`
}
type Command struct {
	revisions *assertions.Coordinator
	owners    assertionlifecycle.Owners
	options   assertionauthority.Options
	runtime   Runtime
}

func New(revisions *assertions.Coordinator, owners assertionlifecycle.Owners, options assertionauthority.Options, runtime Runtime) (*Command, error) {
	if revisions == nil || owners.RemoveProjection == nil || owners.BeforePurgeHistory == nil || owners.BeforeInvalidateRecord == nil || options.DateParser == nil || options.Compare == nil {
		return nil, errors.New("manual_memory_dependencies_required")
	}
	if runtime.Now == nil {
		runtime.Now = func() string { return time.Now().UTC().Format("2006-01-02T15:04:05.000Z") }
	}
	if runtime.ID == nil {
		runtime.ID = func() (string, error) {
			var b [16]byte
			if _, e := rand.Read(b[:]); e != nil {
				return "", e
			}
			b[6] = (b[6] & 15) | 64
			b[8] = (b[8] & 63) | 128
			return fmt.Sprintf("%x-%x-%x-%x-%x", b[:4], b[4:6], b[6:8], b[8:10], b[10:]), nil
		}
	}
	return &Command{revisions, owners, options, runtime}, nil
}
func trim(s string) string {
	return strings.TrimFunc(s, func(r rune) bool {
		return r == 0xFEFF || r == 0xA0 || r == 0x1680 || (r >= 0x2000 && r <= 0x200A) || r == 0x2028 || r == 0x2029 || r == 0x202F || r == 0x205F || r == 0x3000 || r == ' ' || (r >= 9 && r <= 13)
	})
}
func defaultValue(o *Object, k string, d any) any {
	v := get(o, k)
	if v == nil {
		return d
	}
	return v
}
func (c *Command) Create(ctx context.Context, input *Object) (out *Object, err error) {
	id, e := c.runtime.ID()
	if e != nil {
		return nil, e
	}
	sources, e := assertionproposal.UniqueSources(arr(get(input, "sources")))
	if e != nil {
		return nil, e
	}
	out = obj("id", id, "scope", get(input, "scope"), "content", trim(str(get(input, "content"))), "kind", get(input, "kind"), "sources", sources, "importance", defaultValue(input, "importance", "medium"), "confidence", defaultValue(input, "confidence", float64(1)), "learnedAt", c.runtime.Now(), "status", "active", "locked", defaultValue(input, "locked", false), "origin", "manual")
	for _, k := range []string{"validFrom", "epistemicKind", "occurredAt", "validUntil"} {
		if truthy(get(input, k)) {
			_ = out.Set(k, get(input, k))
		}
	}
	if e = assertionproposal.ValidateMemory(out, c.options.DateParser); e != nil {
		return nil, e
	}
	err = c.revisions.Transaction(ctx, func(tx *assertions.RevisionTxn) error {
		return tx.Store.Read(ctx, func(r *assertions.Reader) error {
			for _, source := range sources {
				recalled, e := r.Recalled(str(get(source, "sceneId")), str(get(source, "messageId")))
				if e != nil {
					return e
				}
				if recalled {
					return errors.New("recalled_source")
				}
			}
			return tx.Repository.Write(out)
		})
	})
	if err != nil {
		return nil, err
	}
	return
}
func (c *Command) Update(ctx context.Context, id string, patch *Object) (out *Object, err error) {
	err = c.revisions.Transaction(ctx, func(tx *assertions.RevisionTxn) error { var e error; out, e = c.update(ctx, tx, id, patch); return e })
	if err != nil {
		return nil, err
	}
	return
}
func (c *Command) update(ctx context.Context, tx *assertions.RevisionTxn, id string, patch *Object) (out *Object, err error) {
	err = tx.Store.Read(ctx, func(r *assertions.Reader) error {
		previous, e := r.Record(id)
		if e != nil {
			return e
		}
		if previous == nil {
			return errors.New("memory_not_found")
		}
		if e = tx.BeforeWrite(id); e != nil {
			return e
		}
		reviewed := has(patch, "content")
		lockOnly := true
		out = previous.Clone()
		for _, m := range patch.Members() {
			_ = out.Set(m.Key, m.Value)
			if m.Key != "locked" {
				lockOnly = false
			}
		}
		for _, k := range []string{"id", "scope", "sources"} {
			_ = out.Set(k, get(previous, k))
		}
		if lockOnly {
			_ = out.Set("origin", get(previous, "origin"))
		} else {
			_ = out.Set("origin", "manual")
		}
		if reviewed {
			out.Delete("lineageStatus")
		} else if has(previous, "lineageStatus") {
			_ = out.Set("lineageStatus", get(previous, "lineageStatus"))
		} else {
			out.Delete("lineageStatus")
		}
		if e = assertionproposal.ValidateMemory(out, c.options.DateParser); e != nil {
			return e
		}
		changed := get(out, "content") != get(previous, "content")
		if changed {
			if e = tx.Store.RememberManualFingerprint(previous, c.runtime.Now()); e != nil {
				return e
			}
		}
		lifecycle, e := assertionlifecycle.New(tx, c.owners, c.options)
		if e != nil {
			return e
		}
		if changed {
			if e = lifecycle.InvalidateLegacyDependents(id); e != nil {
				return e
			}
		}
		if reviewed {
			if e = tx.Store.DeleteManualSupports(id); e != nil {
				return e
			}
		}
		if e = tx.Repository.Write(out); e != nil {
			return e
		}
		if _, e = lifecycle.RefreshSupport(); e != nil {
			return e
		}
		if changed || get(out, "status") == "invalidated" || get(out, "status") == "superseded" {
			return tx.Store.RemoveVector(id)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return
}
func (c *Command) Lock(ctx context.Context, id string, locked bool) (*Object, error) {
	return c.Update(ctx, id, obj("locked", locked))
}
func safe(n float64) bool {
	return !math.IsNaN(n) && !math.IsInf(n, 0) && n >= 0 && n <= 9007199254740991 && math.Trunc(n) == n
}
func (c *Command) UpdateGuarded(ctx context.Context, id string, patch *Object, g Guard) (out *Object, err error) {
	err = c.revisions.Transaction(ctx, func(tx *assertions.RevisionTxn) error {
		units, e := jsjson.UTF16(g.IdempotencyKey)
		if e != nil {
			return e
		}
		if len(units) == 0 || len(units) > 160 || !safe(g.ExpectedVersion) || !safe(g.ExpectedDeletionEpoch) {
			return errors.New("invalid_memory_command")
		}
		raw, e := jsjson.Stringify([]any{id, patch, g.ExpectedVersion, g.ExpectedDeletionEpoch})
		if e != nil {
			return e
		}
		hash := fmt.Sprintf("%x", sha256.Sum256([]byte(raw)))
		if e = tx.Store.EnsureManualCommands(); e != nil {
			return e
		}
		return tx.Store.Read(ctx, func(r *assertions.Reader) error {
			existing, e := r.ManualReceipt(g.IdempotencyKey)
			if e != nil {
				return e
			}
			before, e := r.State(id)
			if e != nil {
				return e
			}
			if existing != nil {
				if existing.AssertionID != id || existing.PayloadHash != hash {
					return errors.New("memory_command_conflict")
				}
				if before.Deleted || before.DeletionEpoch != existing.DeletionEpoch || before.Version != existing.ResultVersion {
					return errors.New("memory_command_superseded")
				}
				out, e = r.Record(id)
				if e != nil {
					return e
				}
				if out == nil {
					return errors.New("memory_not_found")
				}
				return nil
			}
			if before.Deleted || float64(before.DeletionEpoch) != g.ExpectedDeletionEpoch {
				return errors.New("assertion_deleted_or_stale_epoch")
			}
			if float64(before.Version) != g.ExpectedVersion {
				return errors.New("assertion_version_conflict")
			}
			if e = tx.Transaction(ctx, func(child *assertions.RevisionTxn) error { _, e := c.update(ctx, child, id, patch); return e }); e != nil {
				return e
			}
			out, e = r.Record(id)
			if e != nil {
				return e
			}
			if e = tx.Checkpoint(); e != nil {
				return e
			}
			after, e := r.State(id)
			if e != nil {
				return e
			}
			return tx.Store.SaveManualReceipt(g.IdempotencyKey, assertions.ManualCommandReceipt{AssertionID: id, PayloadHash: hash, DeletionEpoch: after.DeletionEpoch, ResultVersion: after.Version}, c.runtime.Now())
		})
	})
	if err != nil {
		return nil, err
	}
	return
}
func reject(reason string) Result { return Result{Status: "rejected", Reason: reason} }
func (c *Command) PromotePublic(ctx context.Context, input Context, id string) (out Result, err error) {
	if input.Simulation {
		return reject("simulation_does_not_learn"), nil
	}
	err = c.revisions.Transaction(ctx, func(tx *assertions.RevisionTxn) error {
		return tx.Store.Read(ctx, func(r *assertions.Reader) error {
			scene, e := r.Scene(input.SceneID)
			if e != nil {
				return e
			}
			if !truthy(get(scene, "learningEnabled")) {
				out = reject("learning_disabled")
				return nil
			}
			m, e := r.Record(id)
			if e != nil {
				return e
			}
			scope := get(m, "scope")
			out = reject("public_promotion_not_allowed")
			if m == nil || get(m, "origin") != "learned" || truthy(get(m, "locked")) || get(m, "status") != "active" || get(scope, "kind") != "scene" || get(scope, "sceneId") != input.SceneID || has(scope, "personId") || get(m, "lineageStatus") != "verified" {
				return nil
			}
			proofs, e := r.Proofs(id)
			if e != nil {
				return e
			}
			for _, proof := range proofs {
				for _, parent := range arr(get(proof, "parents")) {
					record, e := r.Record(str(parent))
					if e != nil {
						return e
					}
					if get(get(record, "scope"), "kind") != "sharedPublicKnowledge" {
						return nil
					}
				}
			}
			if (get(m, "kind") != "fact" && get(m, "kind") != "term") || has(m, "validUntil") || len(arr(get(m, "sources"))) == 0 {
				return nil
			}
			for _, source := range arr(get(m, "sources")) {
				if get(source, "sceneId") != input.SceneID {
					return nil
				}
				recalled, e := r.Recalled(str(get(source, "sceneId")), str(get(source, "messageId")))
				if e != nil {
					return e
				}
				if recalled {
					return nil
				}
			}
			promoted := m.Clone()
			_ = promoted.Set("scope", obj("kind", "sharedPublicKnowledge"))
			fp, e := assertions.Fingerprint(promoted)
			if e != nil {
				return e
			}
			deleted, e := r.FingerprintDeleted(fp)
			if e != nil {
				return e
			}
			if deleted {
				out = reject("manually_deleted")
				return nil
			}
			exists, e := r.HasManualFingerprint(fp)
			if e != nil {
				return e
			}
			if exists {
				out = reject("public_memory_already_exists")
				return nil
			}
			if e = assertionproposal.ValidateMemory(promoted, c.options.DateParser); e != nil {
				return e
			}
			if _, e = tx.Store.Purge(id); e != nil {
				return e
			}
			if e = tx.Repository.Write(promoted); e != nil {
				return e
			}
			if e = tx.Store.RemoveVector(id); e != nil {
				return e
			}
			authority, e := assertionauthority.New(r, c.options)
			if e != nil {
				return e
			}
			sanitized, e := authority.Sanitize(promoted)
			if e != nil {
				return e
			}
			out = Result{Status: "saved", Memory: sanitized}
			return nil
		})
	})
	return
}
