package habitservice

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/recovery"
)

var ErrPublicationChanged = errors.New("habit_publication_changed")

type adminRow struct {
	rowid   int64
	id, raw string
}

func adminRows(ctx context.Context, c *sql.Conn) ([]adminRow, error) {
	rows, e := c.QueryContext(ctx, "SELECT rowid,id,value FROM habits ORDER BY rowid")
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []adminRow{}
	for rows.Next() {
		var r adminRow
		if e = rows.Scan(&r.rowid, &r.id, &r.raw); e != nil {
			return nil, e
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
func (s *Service) publicationRoot(emit func([]byte) error) error {
	if s.bindingError != nil {
		return s.bindingError
	}
	if s.session != nil {
		return errors.New("habit_publication_requires_root")
	}
	if emit == nil {
		return errors.New("habit_publication_required")
	}
	return nil
}

// PublishAdminList captures only response-contributing rows. Clock callbacks and
// recalculation run outside authority and SQL reservations; final acceptance
// revalidates both their exact ordered input and authority before emitting.
// emit must enqueue owned bytes synchronously, never write a socket.
func (s *Service) PublishAdminList(ctx context.Context, emit func([]byte) error) error {
	if e := s.publicationRoot(emit); e != nil {
		return e
	}
	var captured []adminRow
	var gate recovery.Gate
	e := s.authority.Publish(ctx, s.main, func(c *sql.Conn) ([]byte, error) {
		var e error
		gate, e = recovery.ReadGate(ctx, c)
		if e != nil {
			return nil, e
		}
		captured, e = adminRows(ctx, c)
		return nil, e
	}, func([]byte) error { return nil })
	if e != nil {
		return e
	}
	values := make([]any, 0, len(captured))
	for _, r := range captured {
		record, e := parseRecord(r.raw)
		if e != nil {
			return e
		}
		if e = s.recalculate(record); e != nil {
			return e
		}
		values = append(values, record)
	}
	body, e := jsjson.Stringify(values)
	if e != nil {
		return e
	}
	return s.authority.Publish(ctx, s.main, func(c *sql.Conn) ([]byte, error) {
		now, e := recovery.ReadGate(ctx, c)
		if e != nil {
			return nil, e
		}
		if now != gate {
			return nil, ErrPublicationChanged
		}
		rows, e := adminRows(ctx, c)
		if e != nil {
			return nil, e
		}
		if len(rows) != len(captured) {
			return nil, ErrPublicationChanged
		}
		for i := range rows {
			if rows[i] != captured[i] {
				return nil, ErrPublicationChanged
			}
		}
		return []byte(body), nil
	}, emit)
}
func sessionGate(ws *recovery.WriteSession) (g recovery.Gate, err error) {
	rows, err := ws.Query("SELECT value FROM recovery_gate WHERE id=1")
	if err != nil {
		return g, err
	}
	defer rows.Close()
	if !rows.Next() {
		return g, errors.New("recovery_gate_missing")
	}
	var raw string
	if err = rows.Scan(&raw); err != nil {
		return g, err
	}
	err = json.Unmarshal([]byte(raw), &g)
	return
}

// UpdateAndPublish does not accept a response before the owned COMMIT succeeds.
// A postcommit publication failure leaves the write committed; callers must not
// retry it automatically or reinterpret that failure as rollback.
func (s *Service) UpdateAndPublish(ctx context.Context, id string, patch *Object, emit func([]byte) error) error {
	if e := s.publicationRoot(emit); e != nil {
		return e
	}
	var body string
	var gate recovery.Gate
	e := s.TransactionWithSession(ctx, func(b *Service, ws *recovery.WriteSession) error {
		r, e := b.Update(ctx, id, patch)
		if e != nil {
			return e
		}
		body, e = jsjson.Stringify(r)
		if e != nil {
			return e
		}
		gate, e = sessionGate(ws)
		return e
	})
	if e != nil {
		return e
	}
	return s.authority.Publish(ctx, s.main, func(c *sql.Conn) ([]byte, error) {
		g, e := recovery.ReadGate(ctx, c)
		if e != nil {
			return nil, e
		}
		if g != gate {
			return nil, ErrPublicationChanged
		}
		var raw string
		e = c.QueryRowContext(ctx, "SELECT value FROM habits WHERE id=?", sqlText(id)).Scan(&raw)
		if e != nil {
			return nil, e
		}
		if raw != body {
			return nil, ErrPublicationChanged
		}
		return []byte(body), nil
	}, emit)
}
func (s *Service) DeleteAndPublish(ctx context.Context, id string, emit func([]byte) error) error {
	if e := s.publicationRoot(emit); e != nil {
		return e
	}
	var gate recovery.Gate
	e := s.TransactionWithSession(ctx, func(b *Service, ws *recovery.WriteSession) error {
		if e := b.Delete(ctx, id); e != nil {
			return e
		}
		var e error
		gate, e = sessionGate(ws)
		return e
	})
	if e != nil {
		return e
	}
	return s.authority.Publish(ctx, s.main, func(c *sql.Conn) ([]byte, error) {
		g, e := recovery.ReadGate(ctx, c)
		if e != nil {
			return nil, e
		}
		if g != gate {
			return nil, ErrPublicationChanged
		}
		return []byte(`{"ok":true}`), nil
	}, emit)
}

// OwnedBy validates constructor composition without opening a database scope.
func (s *Service) OwnedBy(main *sql.DB, authority *recovery.Authority) bool {
	return s != nil && s.main == main && s.authority == authority && s.session == nil && s.bindingError == nil
}
