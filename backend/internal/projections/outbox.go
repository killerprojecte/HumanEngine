// Package projections implements deterministic, optional shadow indexes. Jobs
// contain metadata only and never authorize reads, extraction, models or sends.
package projections

import (
	"context"
	"crypto/rand"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/recovery"
)

type Kind string

const (
	Lexical Kind = "lexical"
	Person  Kind = "person"
)

type Snapshot struct {
	AssertionID   string `json:"assertionId"`
	Version       int64  `json:"version"`
	DeletionEpoch int64  `json:"deletionEpoch"`
	Kind          Kind   `json:"kind"`
}
type Lease struct {
	Snapshot
	ID         int64  `json:"id"`
	LeaseToken string `json:"leaseToken"`
	Attempts   int64  `json:"attempts"`
}
type Options struct {
	// Float64 preserves JavaScript safe-integer clock validation, including NaN.
	Now   func() float64
	Token func() (string, error)
}
type Outbox struct {
	main         *sql.DB
	authority    *recovery.Authority
	session      *recovery.WriteSession
	bindingError error
	options      Options
}

func NewOutbox(ctx context.Context, main *sql.DB, authority *recovery.Authority, options Options) (*Outbox, error) {
	if main == nil || authority == nil {
		return nil, errors.New("projection_authority_required")
	}
	if options.Now == nil {
		options.Now = func() float64 { return float64(time.Now().UnixMilli()) }
	}
	if options.Token == nil {
		options.Token = randomToken
	}
	o := &Outbox{main: main, authority: authority, options: options}
	if e := o.Transaction(ctx, func(b *Outbox) error { return b.session.EnsureProjectionOutboxSchema() }); e != nil {
		return nil, e
	}
	return o, nil
}
func randomToken() (string, error) {
	var b [16]byte
	if _, e := rand.Read(b[:]); e != nil {
		return "", e
	}
	b[6] = (b[6] & 15) | 64
	b[8] = (b[8] & 63) | 128
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[:4], b[4:6], b[6:8], b[8:10], b[10:]), nil
}
func (o *Outbox) WithSession(s *recovery.WriteSession) *Outbox {
	n := *o
	n.session = s
	if s == nil {
		n.bindingError = errors.New("projection_transaction_required")
	} else {
		n.bindingError = s.CheckBinding(o.authority, o.main)
	}
	return &n
}
func (o *Outbox) check() error {
	if o.bindingError != nil {
		return o.bindingError
	}
	if o.session == nil {
		return errors.New("projection_transaction_required")
	}
	return o.session.CheckBinding(o.authority, o.main)
}
func (o *Outbox) Transaction(ctx context.Context, fn func(*Outbox) error) error {
	if o.bindingError != nil {
		return o.bindingError
	}
	if fn == nil {
		return errors.New("projection_callback_required")
	}
	run := func(s *recovery.WriteSession) error { return fn(o.WithSession(s)) }
	if o.session != nil {
		return o.session.WithSavepoint(run)
	}
	return o.authority.WithWriteSession(ctx, o.main, run)
}
func safe(v int64) bool { return v >= 0 && v <= 9007199254740991 }
func clock(now func() float64) (int64, error) {
	v := now()
	if math.IsNaN(v) || math.IsInf(v, 0) || v < 0 || v > 9007199254740991 || math.Trunc(v) != v {
		return 0, errors.New("invalid_projection_clock")
	}
	return int64(v), nil
}
func (o *Outbox) exec(q string, args ...any) (sql.Result, error) {
	if e := o.check(); e != nil {
		return nil, e
	}
	return o.session.Exec(q, bind(args)...)
}
func (o *Outbox) one(q string, args []any, dest ...any) (found bool, err error) {
	if err = o.check(); err != nil {
		return
	}
	rows, e := o.session.Query(q, bind(args)...)
	if e != nil {
		return false, e
	}
	defer func() { err = errors.Join(err, rows.Close()) }()
	if rows.Next() {
		return true, rows.Scan(dest...)
	}
	return false, rows.Err()
}
func (o *Outbox) current(s Snapshot) (bool, error) {
	var one int
	return o.one("SELECT 1 FROM memory_assertion_state WHERE assertion_id=? AND version=? AND deletion_epoch=? AND deleted=0", []any{s.AssertionID, s.Version, s.DeletionEpoch}, &one)
}
func (o *Outbox) Enqueue(s Snapshot) (int64, error) {
	if e := o.check(); e != nil {
		return 0, e
	}
	if s.AssertionID == "" || !safe(s.Version) || s.Version == 0 || !safe(s.DeletionEpoch) || (s.Kind != Lexical && s.Kind != Person) {
		return 0, errors.New("invalid_projection_snapshot")
	}
	current, e := o.current(s)
	if e != nil {
		return 0, e
	}
	if !current {
		return 0, errors.New("projection_snapshot_stale")
	}
	var id int64
	found, e := o.one("SELECT id FROM memory_projection_outbox WHERE assertion_id=? AND version=? AND deletion_epoch=? AND kind=?", []any{s.AssertionID, s.Version, s.DeletionEpoch, string(s.Kind)}, &id)
	if e != nil || found {
		return id, e
	}
	now, e := clock(o.options.Now)
	if e != nil {
		return 0, e
	}
	_, e = o.exec(`UPDATE memory_projection_outbox SET state='invalid',lease_token=NULL,lease_until=NULL,reason='snapshot_stale',updated_at=? WHERE assertion_id=? AND kind=? AND state IN ('pending','leased') AND (version!=? OR deletion_epoch!=?)`, now, s.AssertionID, string(s.Kind), s.Version, s.DeletionEpoch)
	if e != nil {
		return 0, e
	}
	result, e := o.exec(`INSERT INTO memory_projection_outbox(assertion_id,version,deletion_epoch,kind,state,available_at,updated_at) VALUES(?,?,?,?,'pending',?,?)`, s.AssertionID, s.Version, s.DeletionEpoch, string(s.Kind), now, now)
	if e != nil {
		return 0, e
	}
	return result.LastInsertId()
}
func (o *Outbox) Claim(ctx context.Context, leaseMs int64, limit int) (leases []Lease, err error) {
	if !safe(leaseMs) || leaseMs < 1 || leaseMs > 300000 || limit < 1 || limit > 16 {
		return nil, errors.New("invalid_projection_lease")
	}
	leases = []Lease{}
	err = o.Transaction(ctx, func(b *Outbox) error {
		now, e := clock(b.options.Now)
		if e != nil {
			return e
		}
		_, e = b.exec(`UPDATE memory_projection_outbox SET state='invalid',lease_token=NULL,lease_until=NULL,reason='snapshot_stale',updated_at=? WHERE state IN ('pending','leased') AND NOT EXISTS (SELECT 1 FROM memory_assertion_state AS current WHERE current.assertion_id=memory_projection_outbox.assertion_id AND current.deleted=0 AND current.version=memory_projection_outbox.version AND current.deletion_epoch=memory_projection_outbox.deletion_epoch)`, now)
		if e != nil {
			return e
		}
		_, e = b.exec(`UPDATE memory_projection_outbox SET state=CASE WHEN attempts>=3 THEN 'failed' ELSE 'pending' END,available_at=?,lease_token=NULL,lease_until=NULL,reason='lease_expired',updated_at=? WHERE state='leased' AND lease_until<=?`, now, now, now)
		if e != nil {
			return e
		}
		rows, e := b.session.Query(`SELECT id,assertion_id,version,deletion_epoch,kind,attempts FROM memory_projection_outbox WHERE state='pending' AND available_at<=? ORDER BY available_at,id LIMIT ?`, now, limit)
		if e != nil {
			return e
		}
		for rows.Next() {
			var l Lease
			if e = rows.Scan(&l.ID, &l.AssertionID, &l.Version, &l.DeletionEpoch, &l.Kind, &l.Attempts); e != nil {
				break
			}
			leases = append(leases, l)
		}
		e = errors.Join(e, rows.Err(), rows.Close())
		if e != nil {
			return e
		}
		for i := range leases {
			l := &leases[i]
			l.LeaseToken, e = b.options.Token()
			if e != nil {
				return e
			}
			l.Attempts++
			_, e = b.exec(`UPDATE memory_projection_outbox SET state='leased',attempts=attempts+1,lease_token=?,lease_until=?,updated_at=? WHERE id=? AND state='pending'`, l.LeaseToken, float64(now)+float64(leaseMs), now, l.ID)
			if e != nil {
				return e
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return
}
func (o *Outbox) owned(l Lease) (bool, error) {
	now, e := clock(o.options.Now)
	if e != nil {
		return false, e
	}
	var row Lease
	found, e := o.one(`SELECT assertion_id,version,deletion_epoch,kind,attempts FROM memory_projection_outbox WHERE id=? AND state='leased' AND lease_token=? AND lease_until>?`, []any{l.ID, l.LeaseToken, now}, &row.AssertionID, &row.Version, &row.DeletionEpoch, &row.Kind, &row.Attempts)
	if e != nil || !found || row.Snapshot != l.Snapshot || row.Attempts != l.Attempts {
		return false, e
	}
	return o.current(l.Snapshot)
}

// Complete supplies the active child capability, not its suspended parent.
// Go's callback type is synchronous; callbacks must perform database-only work.
func (o *Outbox) Complete(ctx context.Context, l Lease, apply func(*Outbox) error) (completed bool, err error) {
	if apply == nil {
		return false, errors.New("projection_callback_required")
	}
	err = o.Transaction(ctx, func(b *Outbox) error {
		ok, e := b.owned(l)
		if e != nil || !ok {
			return e
		}
		if e = apply(b); e != nil {
			return e
		}
		ok, e = b.owned(l)
		if e != nil {
			return e
		}
		if !ok {
			return errors.New("projection_changed_during_commit")
		}
		now, e := clock(b.options.Now)
		if e != nil {
			return e
		}
		_, e = b.exec(`UPDATE memory_projection_outbox SET state='done',lease_token=NULL,lease_until=NULL,reason=NULL,updated_at=? WHERE id=? AND lease_token=?`, now, l.ID, l.LeaseToken)
		completed = e == nil
		return e
	})
	if err != nil {
		completed = false
	}
	return
}
func (o *Outbox) Fail(ctx context.Context, l Lease, reason string) (changed bool, err error) {
	if reason != "projection_failed" && reason != "source_unavailable" {
		return false, errors.New("invalid_projection_reason")
	}
	err = o.Transaction(ctx, func(b *Outbox) error {
		ok, e := b.owned(l)
		if e != nil || !ok {
			return e
		}
		terminal := "pending"
		if reason == "source_unavailable" {
			terminal = "invalid"
		} else if l.Attempts >= 3 {
			terminal = "failed"
		}
		now, e := clock(b.options.Now)
		if e != nil {
			return e
		}
		delay := math.Min(30000, 1000*math.Pow(2, float64(l.Attempts-1)))
		_, e = b.exec(`UPDATE memory_projection_outbox SET state=?,available_at=?,lease_token=NULL,lease_until=NULL,reason=?,updated_at=? WHERE id=? AND lease_token=?`, terminal, float64(now)+delay, reason, now, l.ID, l.LeaseToken)
		changed = e == nil
		return e
	})
	if err != nil {
		changed = false
	}
	return
}
func (o *Outbox) Invalidate(id string) error {
	if e := o.check(); e != nil {
		return e
	}
	now, e := clock(o.options.Now)
	if e != nil {
		return e
	}
	_, e = o.exec(`UPDATE memory_projection_outbox SET state='invalid',lease_token=NULL,lease_until=NULL,reason='snapshot_stale',updated_at=? WHERE assertion_id=? AND state IN ('pending','leased')`, now, id)
	return e
}
func (o *Outbox) PruneTerminal(ctx context.Context, limit int) (count int64, err error) {
	if limit < 1 || limit > 1000 {
		return 0, errors.New("invalid_projection_prune_limit")
	}
	err = o.Transaction(ctx, func(b *Outbox) error {
		r, e := b.exec(`DELETE FROM memory_projection_outbox WHERE id IN (SELECT id FROM (SELECT job.id FROM memory_projection_outbox AS job WHERE job.state IN ('done','invalid','failed') AND NOT EXISTS (SELECT 1 FROM memory_assertion_state AS current WHERE current.assertion_id=job.assertion_id AND current.version=job.version AND current.deletion_epoch=job.deletion_epoch AND current.deleted=0) ORDER BY job.id DESC LIMIT -1 OFFSET 4096) ORDER BY id ASC LIMIT ?)`, limit)
		if e != nil {
			return e
		}
		count, e = r.RowsAffected()
		return e
	})
	if err != nil {
		count = 0
	}
	return
}

// Match the assertion repository's JS-to-SQL text boundary, including lone
// UTF-16 surrogate replacement. Values remain parameters, never SQL text.
func bind(args []any) []any {
	out := make([]any, len(args))
	for i, v := range args {
		if text, ok := v.(string); ok {
			if utf8, e := jsjson.UTF8Text(text); e == nil {
				v = utf8
			}
		}
		out[i] = v
	}
	return out
}
