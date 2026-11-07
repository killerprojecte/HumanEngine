package recovery

import (
	"context"
	"errors"
)

// WithSavepoint runs a synchronous nested operation in an owned SQLite
// savepoint. Use the supplied child, not the suspended parent, inside fn. The
// child and its rows expire when fn returns. A caught callback/statement error
// can roll back this scope without undoing previous main work. Authority intents
// are independent: rolling back their main ACK leaves a readiness gap.
//
// Cancellation, native transaction loss or failed savepoint cleanup revoke the
// whole root capability, even if the callback catches the error. This method
// does not assert business readiness or acquire authority before the root's
// existing main writer reservation. No caller-controlled SQL runs unprotected.
func (s *WriteSession) WithSavepoint(fn func(*WriteSession) error) (err error) {
	s.mu.Lock()
	if err = s.check(); err == nil && fn == nil {
		err = errors.New("recovery_write_invalid")
	}
	if err == nil {
		err = s.savepointControl(s.ctx, "SAVEPOINT recovery_write_scope")
		err = s.after(err)
	}
	if err != nil {
		s.mu.Unlock()
		return err
	}
	child := &WriteSession{writeSessionState: s.writeSessionState, active: true}
	s.current = child
	s.mu.Unlock()

	returned := false
	defer func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		err = errors.Join(err, child.finishLocked())
		s.current = s
		// A panic must roll back even though no error value was returned.
		if returned && err == nil {
			err = s.savepointControl(s.ctx, "RELEASE recovery_write_scope")
			err = s.after(err)
			if err == nil {
				return
			}
		}
		active, stateErr := transactionActive(s.conn)
		if stateErr != nil || !active {
			err = errors.Join(err, stateErr, s.check())
			return
		}
		// Cleanup is not canceled with the operation. Do not issue RELEASE if
		// ROLLBACK TO fails: doing so could retain failed nested main writes.
		cleanup := s.savepointControl(context.Background(), "ROLLBACK TO recovery_write_scope")
		if cleanup == nil {
			cleanup = s.savepointControl(context.Background(), "RELEASE recovery_write_scope")
		}
		if cleanup != nil {
			s.fatal = errors.Join(s.fatal, cleanup, errors.New("recovery_savepoint_cleanup_failed"))
		}
		err = errors.Join(err, cleanup, s.check())
	}()
	err = fn(child)
	returned = true
	return err
}

// Call only with the shared root mutex held and a fixed statement from above.
// The authorizer permits savepoint control and the fixed FTS segment-table
// operations that SQLite itself may invoke while flushing xSavepoint/xRelease.
// No caller SQL or callback runs until the ordinary authorizer is restored.
func (s *WriteSession) savepointControl(ctx context.Context, statement string) (err error) {
	if err = s.protect(3); err != nil {
		return err
	}
	defer func() {
		if restore := s.protect(0); restore != nil {
			s.fatal = errors.Join(s.fatal, restore)
			err = errors.Join(err, restore)
		}
	}()
	_, err = s.conn.ExecContext(ctx, statement)
	return err
}
