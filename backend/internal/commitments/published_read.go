package commitments

import (
	"context"
	"database/sql"
	"errors"

	"github.com/killerprojecte/HumanEngine/backend/internal/assertionauthority"
	"github.com/killerprojecte/HumanEngine/backend/internal/assertions"
	"github.com/killerprojecte/HumanEngine/backend/internal/recovery"
)

// BindPublishedCurrent borrows an existing HTTP publication for both readers.
// It does not acquire another SQLite connection or recovery reservation.
func (s *Store) BindPublishedCurrent(publisher *recovery.ReadPublisher, assertionsStore *assertions.Store, options assertionauthority.Options) (func(context.Context, func(*assertions.Reader, Context) (bool, error), func(*assertions.Reader, *ReadService) ([]byte, error), func([]byte) error) error, error) {
	if s.bindingError != nil || s.maintenance != nil || s.session != nil || !publisher.OwnedBy(s.main, s.authority) {
		return nil, errors.New("commitment_publication_binding")
	}
	if err := assertionsStore.CheckReadBinding(s.authority, s.main, nil); err != nil {
		return nil, err
	}
	return func(ctx context.Context, enabled func(*assertions.Reader, Context) (bool, error), fn func(*assertions.Reader, *ReadService) ([]byte, error), emit func([]byte) error) error {
		return publisher.Publish(ctx, func(conn *sql.Conn) (body []byte, err error) {
			active := true
			defer func() { active = false }()
			check := func() error {
				if !active {
					return sql.ErrTxDone
				}
				return ctx.Err()
			}
			ar, err := assertionsStore.BorrowReader(check, func(q string, args ...any) (assertions.Rows, error) {
				return conn.QueryContext(ctx, q, args...)
			}, func(q string) (assertions.PreparedRead, error) {
				statement, err := conn.PrepareContext(ctx, q)
				return adminReadStatement{ctx, statement}, err
			})
			if err != nil {
				return nil, err
			}
			cr, err := BorrowReadScope(check, check, func(q string, args ...any) (Rows, error) {
				return conn.QueryContext(ctx, q, args...)
			}, s.options)
			if err != nil {
				return nil, err
			}
			err = ar.WithSupportReadStatements(func(scoped *assertions.Reader) error {
				read, err := newReadService(cr, scoped, func(c Context) (bool, error) { return enabled(scoped, c) }, options)
				if err != nil {
					return err
				}
				body, err = fn(scoped, read)
				return err
			})
			return body, err
		}, emit)
	}, nil
}

type adminReadStatement struct {
	ctx context.Context
	*sql.Stmt
}

func (s adminReadStatement) Query(args ...any) (assertions.Rows, error) {
	return s.QueryContext(s.ctx, args...)
}
