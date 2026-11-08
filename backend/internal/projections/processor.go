package projections

import (
	"context"
	"errors"

	"github.com/killerprojecte/HumanEngine/backend/internal/assertions"
	"github.com/killerprojecte/HumanEngine/backend/internal/recovery"
	"github.com/killerprojecte/HumanEngine/backend/internal/storage"
)

type Object = assertions.Object

// EligibleReader is a trusted database-only callback. Its session is exactly
// Complete's active child. No independent publication connection is permitted.
type EligibleReader func(*recovery.WriteSession, string) (*Object, error)
type Processor struct {
	outbox              *Outbox
	readEligible        EligibleReader
	maintenanceEligible func(*recovery.MaintenanceTx, string) (*Object, error)
	now                 func() float64
}
type DrainResult struct {
	Claimed   int `json:"claimed"`
	Completed int `json:"completed"`
	Invalid   int `json:"invalid"`
	Failed    int `json:"failed"`
}

var errUnavailable = errors.New("projection_source_unavailable")

func NewProcessor(ctx context.Context, outbox *Outbox, readEligible EligibleReader, now func() float64) (*Processor, error) {
	if e := storage.RequireFTS5(); e != nil {
		return nil, e
	}
	if outbox == nil || readEligible == nil {
		return nil, errors.New("projection_processor_dependencies_required")
	}
	if now == nil {
		now = outbox.options.Now
	}
	p := &Processor{outbox: outbox, readEligible: readEligible, now: now}
	e := outbox.Transaction(ctx, func(b *Outbox) error { return b.session.EnsureProjectionProcessorSchema() })
	if e != nil {
		return nil, e
	}
	return p, nil
}
func (p *Processor) WithSession(s *recovery.WriteSession) *Processor {
	n := *p
	n.outbox = p.outbox.WithSession(s)
	return &n
}
func get(o *Object, k string) any {
	if o == nil {
		return nil
	}
	v, _ := o.Get(k)
	return v
}
func str(v any) string { s, _ := v.(string); return s }
func (p *Processor) Drain(ctx context.Context, maxJobs int) (result DrainResult, err error) {
	if maxJobs < 1 || maxJobs > 64 {
		return result, errors.New("invalid_projection_drain_limit")
	}
	for result.Claimed < maxJobs {
		limit := maxJobs - result.Claimed
		if limit > 16 {
			limit = 16
		}
		leases, e := p.outbox.Claim(ctx, 30000, limit)
		if e != nil {
			return result, e
		}
		if len(leases) == 0 {
			break
		}
		result.Claimed += len(leases)
		for _, lease := range leases {
			completed, e := p.outbox.Complete(ctx, lease, func(b *Outbox) error {
				var record *Object
				var e error
				if b.maintenance != nil {
					record, e = p.maintenanceEligible(b.maintenance.tx, lease.AssertionID)
				} else {
					record, e = p.readEligible(b.session, lease.AssertionID)
				}
				if e != nil {
					return e
				}
				if record == nil || get(record, "id") != lease.AssertionID || get(record, "status") == "invalidated" || get(record, "lineageStatus") == "quarantined" {
					return errUnavailable
				}
				if get(record, "scope") == nil {
					return errors.New("projection_record_scope_required")
				}
				scope, _ := get(record, "scope").(*Object)
				key := "shared:public"
				var person any
				if get(scope, "kind") == "scene" {
					key = "scene:" + str(get(scope, "sceneId"))
					person = get(scope, "personId")
				}
				if lease.Kind == Lexical {
					if _, e = b.exec("DELETE FROM memory_fts WHERE assertion_id=?", lease.AssertionID); e != nil {
						return e
					}
					_, e = b.exec("INSERT INTO memory_fts(assertion_id,scope_key,person_id,content) VALUES(?,?,?,?)", lease.AssertionID, key, person, get(record, "content"))
					return e
				}
				now, e := clock(p.now)
				if e != nil {
					return e
				}
				_, e = b.exec(`INSERT INTO memory_person_projection_state VALUES(?,?,?,?,?,?) ON CONFLICT(assertion_id) DO UPDATE SET version=excluded.version,deletion_epoch=excluded.deletion_epoch,scope_key=excluded.scope_key,person_id=excluded.person_id,refreshed_at=excluded.refreshed_at`, lease.AssertionID, lease.Version, lease.DeletionEpoch, key, person, now)
				return e
			})
			if e == nil {
				if completed {
					result.Completed++
				} else {
					result.Invalid++
				}
				continue
			}
			unavailable := errors.Is(e, errUnavailable)
			reason := "projection_failed"
			if unavailable {
				reason = "source_unavailable"
			}
			if _, failure := p.outbox.Fail(ctx, lease, reason); failure != nil {
				return result, failure
			}
			if unavailable {
				result.Invalid++
			} else {
				result.Failed++
			}
		}
	}
	_, err = p.outbox.PruneTerminal(ctx, 500)
	return
}

// Remove is deliberately unconditional even when enqueue/drain are disabled.
// Its source order is lexical, person state, then invalidation of live jobs.
func (p *Processor) Remove(id string) error {
	if e := p.outbox.check(); e != nil {
		return e
	}
	if _, e := p.outbox.exec("DELETE FROM memory_fts WHERE assertion_id=?", id); e != nil {
		return e
	}
	if _, e := p.outbox.exec("DELETE FROM memory_person_projection_state WHERE assertion_id=?", id); e != nil {
		return e
	}
	return p.outbox.Invalidate(id)
}
