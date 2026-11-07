package assertions

import (
	"context"
	"errors"
	"fmt"

	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
)

type CoordinatorOptions struct {
	HistorySupports  func(*Store, string) ([]any, error)
	Retain           func(*Store, *Object) (bool, error)
	RemoveProjection func(*Store, string) error
	Committed        func(*Store, string, int64, int64) error
	AfterCommit      func() error
	// Nil uses the real live eligibility producer. Explicit overrides are for
	// another complete owner (or a recording test), not implicit no-op bridges.
	BeforeCommitEligibility func(*Store, []EligibilityTransition) error
}
type Coordinator struct {
	store   *Store
	options CoordinatorOptions
}
type revisionTracking struct {
	dirty       []string
	dirtySet    map[string]bool
	before      map[string]*Object
	beforeOrder []string
	after       map[string]*Object
}
type RevisionTxn struct {
	Store      *Store
	Repository *Repository
	owner      *Coordinator
	tracking   *revisionTracking
}

func NewCoordinator(store *Store, options CoordinatorOptions) (*Coordinator, error) {
	if store == nil || options.Retain == nil || options.HistorySupports == nil || options.RemoveProjection == nil {
		return nil, errors.New("assertion_coordinator_dependencies_required")
	}
	return &Coordinator{store, options}, nil
}
func newTracking() *revisionTracking {
	return &revisionTracking{dirtySet: map[string]bool{}, before: map[string]*Object{}, after: map[string]*Object{}}
}
func (c *Coordinator) bind(store *Store, tracking *revisionTracking) *RevisionTxn {
	tx := &RevisionTxn{Store: store, owner: c, tracking: tracking}
	tx.Repository, _ = NewRepository(store, Mutations{BeforeWrite: tx.BeforeWrite, AfterWrite: tx.AfterWrite, RemoveProjection: func(id string) error { return c.options.RemoveProjection(store, id) }})
	return tx
}
func (c *Coordinator) Transaction(ctx context.Context, fn func(*RevisionTxn) error) error {
	if fn == nil {
		return errors.New("assertion_transaction_callback_required")
	}
	err := c.store.Transaction(ctx, func(store *Store) error {
		tx := c.bind(store, newTracking())
		if e := fn(tx); e != nil {
			return e
		}
		if e := tx.flush(); e != nil {
			return e
		}
		return tx.flushEligibility()
	})
	if err == nil && c.options.AfterCommit != nil {
		return c.options.AfterCommit()
	}
	return err
}

// Transaction is an actual owned nested savepoint. The child must be used in
// its callback; a caught failure restores both SQL and revision bookkeeping.
func (tx *RevisionTxn) Transaction(ctx context.Context, fn func(*RevisionTxn) error) error {
	if fn == nil {
		return errors.New("assertion_transaction_callback_required")
	}
	previous := tx.tracking.snapshot()
	returned := false
	defer func() {
		if !returned {
			*tx.tracking = *previous
		}
	}()
	err := tx.Store.Transaction(ctx, func(store *Store) error { return fn(tx.owner.bind(store, tx.tracking)) })
	if err != nil {
		*tx.tracking = *previous
	}
	returned = true
	return err
}
func (t *revisionTracking) snapshot() *revisionTracking {
	n := newTracking()
	n.dirty = append(n.dirty, t.dirty...)
	n.beforeOrder = append(n.beforeOrder, t.beforeOrder...)
	for k, v := range t.dirtySet {
		n.dirtySet[k] = v
	}
	for k, v := range t.before {
		n.before[k] = v
	}
	for k, v := range t.after {
		n.after[k] = v
	}
	return n
}
func (tx *RevisionTxn) check() error {
	if e := tx.Store.requireTransaction(); e != nil {
		if e.Error() == "assertion_transaction_required" {
			return errors.New("memory_write_requires_transaction")
		}
		return e
	}
	return nil
}
func (tx *RevisionTxn) BeforeWrite(id string) error {
	if e := tx.check(); e != nil {
		return e
	}
	t := tx.tracking
	_, hasBefore := t.before[id]
	if t.dirtySet[id] && hasBefore {
		return nil
	}
	previous, e := tx.Store.reader().Record(id)
	if e != nil {
		return e
	}
	if !hasBefore {
		t.before[id] = previous
		t.beforeOrder = append(t.beforeOrder, id)
	}
	if t.dirtySet[id] {
		return nil
	}
	if previous != nil {
		retain, e := tx.owner.options.Retain(tx.Store, previous)
		if e != nil {
			return e
		}
		if retain {
			state, e := tx.Store.reader().State(id)
			if e != nil {
				return e
			}
			if state.Version == 0 && state.DeletionEpoch == 0 {
				proofs, e := tx.owner.options.HistorySupports(tx.Store, id)
				if e != nil {
					return e
				}
				if _, e = tx.Store.ImportLegacy(previous, proofs); e != nil {
					return e
				}
			}
		}
	}
	t.dirtySet[id] = true
	t.dirty = append(t.dirty, id)
	return nil
}
func (tx *RevisionTxn) AfterWrite(record *Object) error {
	if e := tx.check(); e != nil {
		return e
	}
	tx.tracking.after[str(get(record, "id"))] = obj("status", get(record, "status"), "lineageStatus", get(record, "lineageStatus"))
	return nil
}
func (tx *RevisionTxn) Touch(id string) error {
	if e := tx.check(); e != nil {
		return e
	}
	if !tx.tracking.dirtySet[id] {
		tx.tracking.dirtySet[id] = true
		tx.tracking.dirty = append(tx.tracking.dirty, id)
	}
	return nil
}
func (tx *RevisionTxn) Checkpoint() error {
	if e := tx.check(); e != nil {
		return e
	}
	return tx.flush()
}
func (tx *RevisionTxn) flush() error {
	t := tx.tracking
	for i := 0; i < len(t.dirty); i++ {
		id := t.dirty[i]
		record, e := tx.Store.reader().Record(id)
		if e != nil {
			return e
		}
		if record == nil {
			continue
		}
		retain, e := tx.owner.options.Retain(tx.Store, record)
		if e != nil {
			return e
		}
		if !retain {
			continue
		}
		state, e := tx.Store.reader().State(id)
		if e != nil {
			return e
		}
		if state.Deleted {
			return errors.New("assertion_deleted_or_stale_epoch")
		}
		proofs, e := tx.owner.options.HistorySupports(tx.Store, id)
		if e != nil {
			return e
		}
		previous, e := tx.Store.reader().LatestRevision(id, scope(record))
		if e != nil {
			return e
		}
		if previous != nil {
			sameRecord, e := equalJSON(get(previous, "record"), record)
			if e != nil {
				return e
			}
			sameProof, e := equalJSON(get(previous, "supports"), proofs)
			if e != nil {
				return e
			}
			if sameRecord && sameProof {
				continue
			}
		}
		recordedAt := tx.Store.now()
		from, e := tx.dateField(get(record, "validFrom"))
		if e != nil {
			return e
		}
		to, e := tx.dateField(get(record, "validUntil"))
		if e != nil {
			return e
		}
		kind := "learned_assertion"
		if get(record, "origin") == "manual" {
			kind = "admin_assertion"
		}
		input := obj("record", record, "supports", proofs, "expectedVersion", float64(state.Version), "expectedDeletionEpoch", float64(state.DeletionEpoch), "idempotencyKey", commitKey(state), "recordedAt", recordedAt, "validFrom", from, "validTo", to, "eventKind", kind)
		revision, e := tx.Store.Append(input)
		if e != nil {
			return e
		}
		if f := tx.owner.options.Committed; f != nil {
			if e = f(tx.Store, id, int64(get(revision, "version").(float64)), state.DeletionEpoch); e != nil {
				return e
			}
		}
	}
	t.dirty = nil
	t.dirtySet = map[string]bool{}
	return nil
}
func (tx *RevisionTxn) dateField(v any) (any, error) {
	if v == nil || v == "" || v == false {
		return nil, nil
	}
	s, ok := v.(string)
	if !ok {
		return nil, errors.New("Invalid time value")
	}
	ms, valid, e := tx.Store.options.DateParser.ParseMillis(s)
	if e != nil {
		return nil, e
	}
	if !valid {
		return nil, errors.New("Invalid time value")
	}
	return isoMillis(ms), nil
}
func (tx *RevisionTxn) flushEligibility() error {
	t := tx.tracking
	changes := []EligibilityTransition{}
	for _, id := range t.beforeOrder {
		before, final := t.before[id], t.after[id]
		if before == nil || final == nil || !permissionChanged(before, final) {
			continue
		}
		after, e := tx.Store.reader().Record(id)
		if e != nil {
			return e
		}
		if after != nil {
			changes = append(changes, EligibilityTransition{before, after})
		}
	}
	f := tx.owner.options.BeforeCommitEligibility
	var e error
	if f != nil {
		e = f(tx.Store, changes)
	} else {
		e = tx.Store.RecordEligibilityChanges(changes)
	}
	if e != nil {
		return e
	}
	t.before = map[string]*Object{}
	t.beforeOrder = nil
	t.after = map[string]*Object{}
	return nil
}
func equalJSON(a, b any) (bool, error) {
	left, e := jsjson.Stringify(a)
	if e != nil {
		return false, e
	}
	right, e := jsjson.Stringify(b)
	return left == right, e
}

func commitKey(s State) string { return fmt.Sprintf("commit:%d:%d", s.DeletionEpoch, s.Version+1) }
