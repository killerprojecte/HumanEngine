package workingset

import (
	"context"
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
type Scope struct {
	session *recovery.WriteSession
	query   func(string, ...any) (Rows, error)
	check   func() error
	audit   func() (bool, error)
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
	if s.readScope != nil {
		return s.readScope
	}
	if s.session == nil {
		return nil
	}
	return &Scope{session: s.session, check: s.check, query: func(q string, args ...any) (Rows, error) { return s.session.Query(q, args...) }, audit: s.session.WorkingSetSchemaSupported}
}
func (s *Service) Read(ctx context.Context, fn func(*Service) error) error {
	if s.bindingError != nil {
		return s.bindingError
	}
	if s.session != nil || s.readScope != nil {
		if e := s.scope().CheckScope(); e != nil {
			return e
		}
		return fn(s)
	}
	return s.authority.WithWorkingSetRead(ctx, s.main, func(reader *recovery.WorkingSetReadSession) error {
		scope := &Scope{check: reader.CheckScope, audit: reader.SchemaSupported}
		scope.query = func(q string, args ...any) (Rows, error) {
			return reader.Query(q, args...)
		}
		b := *s
		b.readScope = scope
		return fn(&b)
	})
}
