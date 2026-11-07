package recovery

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
)

func preparedValue(t *testing.T, h *SessionStatement, want string) {
	t.Helper()
	rows, err := h.Query()
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	if !rows.Next() {
		t.Fatal("missing row", rows.Err())
	}
	var got string
	if err = rows.Scan(&got); err != nil || got != want {
		t.Fatal(got, want, err)
	}
}

func TestSessionPreparedQueriesOwnLifetimeAndSavepoints(t *testing.T) {
	f := newWriterFixture(t)
	var escaped, nested *SessionStatement
	var escapedRows *SessionRows
	rollback := errors.New("synthetic-rollback")
	err := f.authority.WithWriteSession(context.Background(), f.main, func(root *WriteSession) error {
		var e error
		escaped, e = root.PrepareQuery("SELECT value FROM data")
		if e != nil {
			return e
		}
		preparedValue(t, escaped, "original")
		if _, e = root.Exec("UPDATE data SET value='parent'"); e != nil {
			return e
		}
		preparedValue(t, escaped, "parent")
		if e = root.WithSavepoint(func(child *WriteSession) error {
			if _, e := escaped.Query(); e == nil || !strings.Contains(e.Error(), "scope_inactive") {
				t.Fatal("parent statement not suspended", e)
			}
			if _, e := root.PrepareQuery("SELECT value FROM data"); e == nil || !strings.Contains(e.Error(), "scope_inactive") {
				t.Fatal("parent preparation not suspended", e)
			}
			if _, e := child.Exec("UPDATE data SET value='nested'"); e != nil {
				return e
			}
			nested, e = child.PrepareQuery("SELECT value FROM data")
			if e != nil {
				return e
			}
			preparedValue(t, nested, "nested")
			escapedRows, e = nested.Query()
			if e != nil {
				return e
			}
			return rollback
		}); !errors.Is(e, rollback) {
			t.Fatal(e)
		}
		if _, e := nested.Query(); !errors.Is(e, sql.ErrTxDone) {
			t.Fatal("expired nested statement", e)
		}
		if escapedRows.Next() {
			t.Fatal("expired prepared rows advanced")
		}
		var value string
		if e := escapedRows.Scan(&value); !errors.Is(e, sql.ErrTxDone) {
			t.Fatal(e)
		}
		preparedValue(t, escaped, "parent")
		if len(root.statements) != 1 || len(root.rows) != 0 {
			t.Fatal("leaked child registration", len(root.statements), len(root.rows))
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = escaped.Query(); !errors.Is(err, sql.ErrTxDone) {
		t.Fatal("escaped statement", err)
	}
	if len(escaped.owner.statements) != 0 {
		t.Fatal("retained root handles")
	}
	if err = escaped.Close(); err != nil {
		t.Fatal(err)
	}
	assertMain(t, f, "parent", 0, 1)
}

func TestSessionPreparedQueriesPreserveAuthorizerAndRecoverableErrors(t *testing.T) {
	f := newWriterFixture(t)
	err := f.authority.WithWriteSession(context.Background(), f.main, func(s *WriteSession) error {
		for _, query := range []string{"COMMIT", "ROLLBACK", "SAVEPOINT forged", "CREATE TABLE forbidden(value)", "UPDATE recovery_gate SET value='{}' RETURNING value", "PRAGMA foreign_keys=OFF", "ATTACH DATABASE ':memory:' AS foreign_db", "SELECT value FROM missing_table"} {
			statement, err := s.PrepareQuery(query)
			if err == nil {
				statement.Close()
				t.Fatal("preparation granted forbidden SQL", query)
			}
		}
		statement, err := s.PrepareQuery("SELECT value FROM data")
		if err != nil {
			return err
		}
		if err = s.EnsureSourceEventSchema(); err != nil {
			return err
		}
		preparedValue(t, statement, "original")
		rows, err := statement.Query()
		if err != nil {
			return err
		}
		if err = statement.Close(); err != nil {
			return err
		}
		if rows.Next() {
			t.Fatal("closed statement rows advanced")
		}
		var value string
		if err = rows.Scan(&value); !errors.Is(err, sql.ErrTxDone) {
			t.Fatal(err)
		}
		if _, err = statement.Query(); !errors.Is(err, sql.ErrTxDone) {
			t.Fatal(err)
		}
		if len(s.statements) != 0 || len(s.rows) != 0 {
			t.Fatal("closed statement retained resources")
		}
		// The scope and authorizer still work after explicit handle close.
		if _, err = s.PrepareQuery("UPDATE recovery_gate SET value='{}'"); err == nil {
			t.Fatal("authorizer not restored")
		}
		_, err = s.Exec("UPDATE data SET value='continued'")
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	assertMain(t, f, "continued", 0, 1)
}

func TestSessionPreparedQueryStepLossAndCaughtErrors(t *testing.T) {
	for _, rollback := range []bool{false, true} {
		raise := "ABORT"
		if rollback {
			raise = "ROLLBACK"
		}
		t.Run(raise, func(t *testing.T) {
			f := newWriterFixture(t)
			if _, err := f.main.Exec("CREATE TRIGGER fault BEFORE UPDATE ON data BEGIN SELECT RAISE(" + raise + ",'synthetic'); END"); err != nil {
				t.Fatal(err)
			}
			err := f.authority.WithWriteSession(context.Background(), f.main, func(s *WriteSession) error {
				read, err := s.PrepareQuery("SELECT value FROM data")
				if err != nil {
					return err
				}
				preparedValue(t, read, "original")
				query, err := s.PrepareQuery("UPDATE data SET value='failed' RETURNING value")
				if err != nil {
					return err
				}
				rows, err := query.Query()
				if err == nil {
					if rows.Next() {
						t.Fatal("failed update returned a row")
					}
					err = rows.Err()
					_ = rows.Close()
				}
				if err == nil {
					t.Fatal("missing prepared step error")
				}
				if rollback {
					if !errors.Is(err, sql.ErrTxDone) {
						t.Fatal(err)
					}
					if _, err = read.Query(); !errors.Is(err, sql.ErrTxDone) {
						t.Fatal(err)
					}
					if _, err = s.PrepareQuery("SELECT value FROM data"); !errors.Is(err, sql.ErrTxDone) {
						t.Fatal(err)
					}
					if _, err = s.Exec("INSERT INTO data VALUES('escaped')"); !errors.Is(err, sql.ErrTxDone) {
						t.Fatal(err)
					}
				} else {
					preparedValue(t, read, "original")
				}
				return nil // A caught ordinary error is not a root transaction error.
			})
			if rollback {
				if !errors.Is(err, sql.ErrTxDone) {
					t.Fatal(err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			assertMain(t, f, "original", 0, 1)
		})
	}
}

func TestSessionPreparedQueriesCancellationAndPanic(t *testing.T) {
	f := newWriterFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var statement *SessionStatement
	err := f.authority.WithWriteSession(ctx, f.main, func(s *WriteSession) error {
		var e error
		statement, e = s.PrepareQuery("SELECT value FROM data")
		if e != nil {
			return e
		}
		cancel()
		if _, e = statement.Query(); !errors.Is(e, context.Canceled) {
			t.Fatal(e)
		}
		return nil
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if !statement.closed || len(statement.owner.statements) != 0 {
		t.Fatal("canceled handles not released")
	}
	func() {
		defer func() {
			if recover() != "synthetic" {
				t.Fatal("missing panic")
			}
		}()
		_ = f.authority.WithWriteSession(context.Background(), f.main, func(s *WriteSession) error {
			var e error
			statement, e = s.PrepareQuery("SELECT value FROM data")
			if e != nil {
				t.Fatal(e)
			}
			panic("synthetic")
		})
	}()
	if _, err = statement.Query(); !errors.Is(err, sql.ErrTxDone) {
		t.Fatal(err)
	}
	if len(statement.owner.statements) != 0 {
		t.Fatal("panicking owner retained handles")
	}
}
