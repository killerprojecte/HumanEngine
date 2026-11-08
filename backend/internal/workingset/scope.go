package workingset

import (
	"context"
	"database/sql"
	"errors"

	"github.com/killerprojecte/HumanEngine/backend/internal/recovery"
)

type Rows interface {
	Next() bool
	Scan(...any) error
	Err() error
	Close() error
}

// Scope exposes the exact owner snapshot to the eligibility resolver. A
// publication scope is native read-only; a write scope borrows its real active
// session. Neither grants independent authorization or a second connection.
// Use synchronously, never concurrently with the owner's callback return.
type PreparedRead interface {
	Query(...any) (Rows, error)
	Close() error
}

type Scope struct {
	main        *sql.DB
	authority   *recovery.Authority
	prepare     func(string) (PreparedRead, error)
	session     *recovery.WriteSession
	maintenance *recovery.MaintenanceTx
	query       func(string, ...any) (Rows, error)
	check       func() error
	audit       func() (bool, error)
}

func (s *Scope) CheckScope() error {
	if s == nil || s.check == nil {
		return errors.New("working_set_scope_required")
	}
	return s.check()
}
func (s *Scope) Query(q string, args ...any) (Rows, error) {
	if e := s.CheckScope(); e != nil {
		return nil, e
	}
	return s.query(q, bind(args)...)
}

// WriteSession returns nil for publication scopes, never a fabricated writer.
func (s *Scope) WriteSession() *recovery.WriteSession {
	if s == nil {
		return nil
	}
	return s.session
}
func (s *Service) scope() *Scope {
	if s.maintenance != nil {
		return s.maintenanceScope()
	}
	if s.readScope != nil {
		return s.readScope
	}
	if s.session == nil {
		return nil
	}
	return &Scope{main: s.main, authority: s.authority, prepare: func(q string) (PreparedRead, error) {
		p, e := s.session.PrepareQuery(q)
		if e != nil {
			return nil, e
		}
		return writeStatement{p}, nil
	}, session: s.session, check: s.check, query: func(q string, args ...any) (Rows, error) { return s.session.Query(q, args...) }, audit: s.session.WorkingSetSchemaSupported}
}
func (s *Service) Read(ctx context.Context, fn func(*Service) error) error {
	if s.bindingError != nil {
		return s.bindingError
	}
	if s.session != nil || s.readScope != nil || s.maintenance != nil {
		if e := s.scope().CheckScope(); e != nil {
			return e
		}
		return fn(s)
	}
	return s.authority.WithWorkingSetRead(ctx, s.main, func(reader *recovery.WorkingSetReadSession) error {
		scope := &Scope{main: s.main, authority: s.authority, check: reader.CheckScope, audit: reader.SchemaSupported, prepare: func(q string) (PreparedRead, error) {
			p, e := reader.PrepareQuery(q)
			if e != nil {
				return nil, e
			}
			return publicationStatement{p}, nil
		}}
		scope.query = func(q string, args ...any) (Rows, error) {
			return reader.Query(q, args...)
		}
		b := *s
		b.readScope = scope
		return fn(&b)
	})
}

// CheckReadBinding verifies the concrete owner, without exposing its connection.
func (s *Scope) CheckReadBinding(a *recovery.Authority, main *sql.DB) error {
	if e := s.CheckScope(); e != nil {
		return e
	}
	if s.authority != a || s.main != main {
		return errors.New("working_set_read_binding")
	}
	if s.session != nil {
		return s.session.CheckBinding(a, main)
	}
	return nil
}

// PrepareQuery borrows a native statement, not cached rows. Owner exit closes it.
func (s *Scope) PrepareQuery(q string) (PreparedRead, error) {
	if e := s.CheckScope(); e != nil {
		return nil, e
	}
	if s.prepare == nil {
		return nil, errors.New("working_set_native_prepare_required")
	}
	return s.prepare(q)
}

type writeStatement struct{ p *recovery.SessionStatement }

func (s writeStatement) Query(args ...any) (Rows, error) { return s.p.Query(args...) }
func (s writeStatement) Close() error                    { return s.p.Close() }

type publicationStatement struct {
	p *recovery.WorkingSetReadStatement
}

func (s publicationStatement) Query(args ...any) (Rows, error) { return s.p.Query(args...) }
func (s publicationStatement) Close() error                    { return s.p.Close() }
