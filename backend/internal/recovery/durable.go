package recovery

import (
	"context"
	"database/sql"
	"errors"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"sync"
)

// DurableTicket is an opaque, process-local capability for asynchronous file
// work. A ticket never owns a database connection, transaction, or authority lock.
// The authority intent is durable while main remains deliberately unacknowledged.
type DurableTicket struct {
	mu            sync.Mutex
	self          *DurableTicket
	authority     *Authority
	main          *sql.DB
	before, after Head
	consumed      bool
}

// BeginDurable captures qualification under the main writer, then durably
// appends forced intent WITHOUT changing the main gate. prepare must only read.
func (a *Authority) BeginDurable(ctx context.Context, main *sql.DB, prepare func(*WriteSession) (Revocation, error)) (*DurableTicket, error) {
	if prepare == nil {
		return nil, errors.New("recovery_write_invalid")
	}
	if e := a.RequireAsyncRoot(ctx, main); e != nil {
		return nil, e
	}
	var ticket *DurableTicket
	err := a.WithWriteSession(ctx, main, func(s *WriteSession) error {
		if e := s.AssertReady(); e != nil {
			return e
		}
		input, e := prepare(s)
		if e != nil {
			return e
		}
		event, e := encodeRevocation(input)
		if e != nil {
			return e
		}
		s.mu.Lock()
		defer s.mu.Unlock()
		if e = s.check(); e != nil {
			return e
		}
		g, _, e := s.gate()
		if e != nil {
			return e
		}
		if e = s.ready(g); e != nil {
			return e
		}
		if e = a.mu.LockContext(ctx); e != nil {
			return e
		}
		after, e := a.appendLocked(ctx, event, g.Head, true, nil)
		a.mu.Unlock()
		if e != nil {
			return e
		}
		ticket = &DurableTicket{authority: a, main: main, before: g.Head, after: after}
		ticket.self = ticket
		return nil
	})
	if err != nil {
		return nil, err
	}
	return ticket, nil
}

// CommitDurable acknowledges only the completed local synchronous effects. Any
// callback failure rolls main back, retaining the authority gap for recovery.
func (a *Authority) CommitDurable(ctx context.Context, main *sql.DB, t *DurableTicket, apply func(*WriteSession) error) (result error) {
	if t == nil || t.self != t || t.authority != a || t.main != main || apply == nil {
		return errors.New("recovery_ticket_invalid")
	}
	if e := a.RequireAsyncRoot(ctx, main); e != nil {
		return e
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.consumed {
		return errors.New("recovery_ticket_invalid")
	}
	// The final synchronous check retains the short authority snapshot through
	// main COMMIT. No filesystem/provider operation occurs under this lease.
	leased := false
	defer func() {
		if leased {
			cleanup := rollbackOwned(a.conn)
			if cleanup != nil {
				a.cached = false
			}
			result = errors.Join(result, cleanup)
			a.mu.Unlock()
		}
	}()
	err := a.WithWriteSession(ctx, main, func(s *WriteSession) error {
		e := func() error {
			s.mu.Lock()
			defer s.mu.Unlock()
			if e := s.check(); e != nil {
				return e
			}
			g, raw, e := s.gate()
			if e != nil {
				return e
			}
			if g.Head != t.before {
				return errors.New("recovery_authority_head_changed")
			}
			g.Head = t.after
			if e = s.ready(g); e != nil {
				return e
			}
			h, _ := jsjson.NewObject(jsjson.Member{Key: "instanceId", Value: t.after.InstanceID}, jsjson.Member{Key: "sequence", Value: float64(t.after.Sequence)}, jsjson.Member{Key: "hash", Value: t.after.Hash})
			_ = raw.Set("head", h)
			return s.saveGate(raw)
		}()
		if e != nil {
			return e
		}
		if e = apply(s); e != nil {
			return e
		}
		e = func() error {
			s.mu.Lock()
			defer s.mu.Unlock()
			if e := s.check(); e != nil {
				return e
			}
			g, raw, e := s.gate()
			if e != nil {
				return e
			}
			if g.Head != t.after {
				return errors.New("recovery_authority_head_changed")
			}
			if e = a.mu.LockContext(ctx); e != nil {
				return e
			}
			leased = true
			if e = beginOwned(ctx, a.conn, "BEGIN"); e != nil {
				return e
			}
			head, e := a.current(ctx)
			if e != nil {
				return e
			}
			if head != t.after {
				return errors.New("recovery_authority_head_changed")
			}
			return s.saveGate(raw)
		}()
		if e != nil {
			return e
		}
		t.consumed = true
		return nil
	})
	return err
}
