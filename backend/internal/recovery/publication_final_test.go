package recovery

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"

	"github.com/mattn/go-sqlite3"
)

func TestPublicationCanceledAfterProjectionDoesNotEmit(t *testing.T) {
	a, main, external, _ := fixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	emitted := false
	err := a.Publish(ctx, main, func(*sql.Conn) ([]byte, error) {
		cancel()
		return []byte("must not escape"), nil
	}, func([]byte) error { emitted = true; return nil })
	if !errors.Is(err, context.Canceled) || emitted {
		t.Fatal("canceled projection published", err)
	}
	if _, err = external.Exec("UPDATE identity SET format=format"); err != nil {
		t.Fatal("cancellation leaked reservation", err)
	}
}

func TestPublicationFinalGateAndSnapshotChecks(t *testing.T) {
	for _, mutation := range []string{
		"ROLLBACK",
		"UPDATE recovery_gate SET value=json_set(value,'$.state','quarantine') WHERE id=1",
		"UPDATE recovery_gate SET value=json_set(value,'$.head.hash',?) WHERE id=1",
	} {
		t.Run(mutation, func(t *testing.T) {
			f := newWriterFixture(t)
			emitted := false
			err := f.authority.Publish(context.Background(), f.main, func(conn *sql.Conn) ([]byte, error) {
				var args []any
				if strings.Contains(mutation, "?") {
					args = []any{strings.Repeat("f", 64)}
				}
				_, err := conn.ExecContext(context.Background(), mutation, args...)
				return []byte("must not escape"), err
			}, func([]byte) error { emitted = true; return nil })
			if err == nil || emitted {
				t.Fatal("changed publication snapshot emitted")
			}
			if _, err = f.external.Exec("UPDATE identity SET format=format"); err != nil {
				t.Fatal("final validation leaked reservation", err)
			}
		})
	}
}

func TestPublicationFinalCheckKeepsPinnedWALSnapshot(t *testing.T) {
	f := newWriterFixture(t)
	externalMain, err := sql.Open("sqlite3", f.mainPath)
	if err != nil {
		t.Fatal(err)
	}
	defer externalMain.Close()
	var published string
	err = f.authority.Publish(context.Background(), f.main, func(conn *sql.Conn) ([]byte, error) {
		var before string
		if err := conn.QueryRowContext(context.Background(), "SELECT value FROM data").Scan(&before); err != nil {
			return nil, err
		}
		// This raw fixture commit deliberately bypasses the authority protocol.
		// Final validation must still inspect the already pinned WAL snapshot,
		// never rebind an older body to a fresh main gate/head.
		_, err := externalMain.Exec("UPDATE data SET value='after'; UPDATE recovery_gate SET value=json_set(value,'$.state','quarantine') WHERE id=1")
		return []byte(before), err
	}, func(body []byte) error { published = string(body); return nil })
	if err != nil || published != "original" {
		t.Fatal("publication switched to a later snapshot", published, err)
	}
	called := false
	err = f.authority.Publish(context.Background(), f.main, func(*sql.Conn) ([]byte, error) { return nil, nil }, func([]byte) error { called = true; return nil })
	if err == nil || called {
		t.Fatal("next publication ignored new invalid gate")
	}
}

func TestPublicationAuthorityCleanupFailureCannotReplayAcceptance(t *testing.T) {
	a, main, external, _ := fixture(t)
	calls := 0
	err := a.Publish(context.Background(), main, func(*sql.Conn) ([]byte, error) { return []byte("accepted"), nil }, func([]byte) error {
		calls++
		return a.conn.Raw(func(raw any) error {
			raw.(*sqlite3.SQLiteConn).RegisterAuthorizer(func(op int, first, second, database string) int {
				if op == sqlite3.SQLITE_TRANSACTION && first == "ROLLBACK" {
					return sqlite3.SQLITE_DENY
				}
				return sqlite3.SQLITE_OK
			})
			return nil
		})
	})
	if err == nil || !strings.Contains(err.Error(), "cleanup_failed") || calls != 1 {
		t.Fatal("cleanup/acceptance boundary lost", calls, err)
	}
	if _, err = external.Exec("UPDATE identity SET format=format"); err != nil {
		t.Fatal("poisoned authority retained reservation", err)
	}
	err = a.Publish(context.Background(), main, func(*sql.Conn) ([]byte, error) { return nil, nil }, func([]byte) error { calls++; return nil })
	if err == nil || calls != 1 {
		t.Fatal("poisoned authority reused", calls, err)
	}
	if main.Stats().InUse != 0 {
		t.Fatal("cleanup failure retained main connection")
	}
}
