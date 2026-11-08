package sourceevents

import (
	"context"
	"database/sql"
	"errors"
	"github.com/killerprojecte/HumanEngine/backend/internal/botruntime"
	"github.com/killerprojecte/HumanEngine/backend/internal/recovery"
	"sync/atomic"
)

// WithRuntimeReads supplies one synchronous, callback-leased read capability.
// On a WithSession store it uses that exact writer and CheckBinding, with no
// nested publication/transaction. Unbound stores hold one fixed publication.
// Retaining reads after return always fails, even while its writer remains live.
func (s *Store) WithRuntimeReads(ctx context.Context, fn func(botruntime.ReadAuthority) error) error {
	if fn == nil {
		return errors.New("runtime_read_callback_required")
	}
	if s.bindingError != nil {
		return s.bindingError
	}
	return s.Read(ctx, func(v *View) error {
		if err := v.ready(); err != nil {
			return err
		}
		var lease atomic.Bool
		lease.Store(true)
		defer lease.Store(false)
		reads := &runtimeReads{store: s, view: v, lease: &lease}
		if err := fn(reads); err != nil {
			return err
		}
		return reads.Ready()
	})
}

// RuntimeReadScope exposes trusted domain composition, not a tool capability.
// Both callbacks expire at the end of UseRuntimeReadScope. Query must be used
// only for reads and all rows must be closed synchronously inside the callback.
type RuntimeReadScope struct {
	CheckBinding func(*sql.DB, *recovery.Authority) error
	Ready        func() error
	Query        func(string, ...any) (Rows, error)
}

// UseRuntimeReadScope verifies the owner before composing another domain's
// reads. A validation given recall reads therefore cannot silently reopen the
// authority or switch database owners.
func (s *Store) UseRuntimeReadScope(reads botruntime.ReadAuthority, fn func(RuntimeReadScope) error) error {
	var r *runtimeReads
	switch v := reads.(type) {
	case *runtimeReads:
		r = v
	case *RuntimeAuthority:
		r = v.runtimeReads
	default:
		return errors.New("runtime_read_scope_foreign")
	}
	if r == nil || r.store == nil || r.store.main != s.main || r.store.authority != s.authority {
		return errors.New("runtime_read_scope_foreign")
	}
	if s.bindingError != nil {
		return s.bindingError
	}
	if fn == nil {
		return errors.New("runtime_read_callback_required")
	}
	return r.read(func(v *View) error {
		var active atomic.Bool
		active.Store(true)
		defer active.Store(false)
		ready := func() error {
			if !active.Load() {
				return sql.ErrTxDone
			}
			if r.lease != nil && !r.lease.Load() {
				return sql.ErrTxDone
			}
			return v.ready()
		}
		scope := RuntimeReadScope{CheckBinding: func(db *sql.DB, a *recovery.Authority) error {
			if db != r.store.main || a != r.store.authority {
				return errors.New("runtime_read_scope_foreign")
			}
			return ready()
		}, Ready: ready, Query: func(q string, a ...any) (Rows, error) {
			if e := ready(); e != nil {
				return nil, e
			}
			rows, e := v.query(q, a...)
			if e != nil {
				return nil, e
			}
			return &scopedRuntimeRows{Rows: rows, ready: ready}, nil
		}}
		if e := fn(scope); e != nil {
			return e
		}
		return ready()
	})
}

// scopedRuntimeRows cannot outlive the callback even on an unbound sql.Conn.
// Close remains available after expiry so misuse does not leak the resource.
type scopedRuntimeRows struct {
	Rows
	ready func() error
}

func (r *scopedRuntimeRows) Next() bool {
	if r.ready() != nil {
		return false
	}
	return r.Rows.Next()
}
func (r *scopedRuntimeRows) Scan(v ...any) error {
	if e := r.ready(); e != nil {
		return e
	}
	return r.Rows.Scan(v...)
}
func (r *scopedRuntimeRows) Err() error {
	if e := r.ready(); e != nil {
		return e
	}
	return r.Rows.Err()
}
