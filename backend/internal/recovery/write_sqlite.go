package recovery

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"

	"github.com/mattn/go-sqlite3"
)

// ErrCommitOutcomeUnknown means COMMIT returned an error after SQLite left its
// transaction (or its native state could not be read). The caller must inspect
// durable state; blindly retrying/replaying the mutation is not safe.
var ErrCommitOutcomeUnknown = errors.New("recovery_commit_outcome_unknown")

type CommitError struct {
	Database string
	Cause    error
}

func (e *CommitError) Error() string {
	return fmt.Sprintf("%s: %s: %v", ErrCommitOutcomeUnknown, e.Database, e.Cause)
}
func (e *CommitError) Unwrap() []error { return []error{ErrCommitOutcomeUnknown, e.Cause} }

type commitExec func(context.Context, *sql.Conn, string) error

func execCommit(ctx context.Context, conn *sql.Conn, _ string) error {
	_, err := conn.ExecContext(ctx, "COMMIT")
	return err
}
func commitOwned(ctx context.Context, conn *sql.Conn, database string, exec commitExec) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if exec == nil {
		exec = execCommit
	}
	if err := exec(ctx, conn, database); err != nil {
		active, stateErr := transactionActive(conn)
		if stateErr != nil || !active {
			return &CommitError{database, errors.Join(err, stateErr)}
		}
		return err
	}
	return nil
}

// Even BEGIN can finish in SQLite just as the driver returns cancellation.
// Clean up its native outcome before returning a failed acquisition.
func beginOwned(ctx context.Context, conn *sql.Conn, statement string) error {
	active, err := transactionActive(conn)
	if err != nil {
		return err
	}
	if active {
		return errors.New("recovery_transaction_active")
	}
	if _, err = conn.ExecContext(ctx, statement); err != nil {
		return errors.Join(err, rollbackOwned(conn))
	}
	return nil
}
func transactionActive(conn *sql.Conn) (active bool, err error) {
	err = conn.Raw(func(raw any) error {
		native, ok := raw.(*sqlite3.SQLiteConn)
		if !ok {
			return errors.New("recovery_sqlite_driver_required")
		}
		active = !native.AutoCommit()
		return nil
	})
	return
}

// SQLite can automatically roll back on interrupts, I/O errors or trigger
// RAISE(ROLLBACK). Consult sqlite3_get_autocommit, not a Go ownership counter.
// A connection whose rollback cannot be verified is discarded, never pooled.
func rollbackOwned(conn *sql.Conn) error {
	active, err := transactionActive(conn)
	if err == nil && !active {
		return nil
	}
	if err == nil {
		_, err = conn.ExecContext(context.Background(), "ROLLBACK")
		stillActive, stateErr := transactionActive(conn)
		if stateErr == nil && !stillActive {
			return err
		}
		err = errors.Join(err, stateErr, errors.New("recovery_transaction_cleanup_failed"))
	}
	_ = conn.Raw(func(any) error { return driver.ErrBadConn })
	return err
}

// SQLITE_RECURSIVE is a stable SQLite authorizer opcode, not exported by go-sqlite3.
const sqliteRecursive = 33

func protectMutation(conn *sql.Conn, enabled bool) error { return protectMain(conn, enabled, true) }
func protectPreparation(conn *sql.Conn) error            { return protectMain(conn, true, false) }
func protectMain(conn *sql.Conn, enabled, writable bool) error {
	return conn.Raw(func(raw any) error {
		native, ok := raw.(*sqlite3.SQLiteConn)
		if !ok {
			return errors.New("recovery_sqlite_driver_required")
		}
		if !enabled {
			native.RegisterAuthorizer(nil)
			return nil
		}
		native.RegisterAuthorizer(func(op int, table, _ string, database string) int {
			switch op {
			case sqlite3.SQLITE_SELECT, sqlite3.SQLITE_FUNCTION, sqliteRecursive:
				return sqlite3.SQLITE_OK
			case sqlite3.SQLITE_READ:
				if database == "main" {
					return sqlite3.SQLITE_OK
				}
			case sqlite3.SQLITE_INSERT, sqlite3.SQLITE_UPDATE, sqlite3.SQLITE_DELETE:
				if writable && database == "main" && table != "recovery_gate" && table != "sqlite_master" && table != "sqlite_schema" {
					return sqlite3.SQLITE_OK
				}
			}
			// No caller COMMIT/ROLLBACK, SAVEPOINT, ATTACH, PRAGMA, schema mutation,
			// or gate forgery. This also applies to statements executed by triggers.
			return sqlite3.SQLITE_DENY
		})
		return nil
	})
}
