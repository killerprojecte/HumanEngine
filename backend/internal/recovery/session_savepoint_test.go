package recovery

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
)

func sessionValue(t *testing.T, s *WriteSession) string {
	t.Helper()
	rows, err := s.Query("SELECT value FROM data")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	if !rows.Next() {
		t.Fatal(rows.Err())
	}
	var value string
	if err = rows.Scan(&value); err != nil {
		t.Fatal(err)
	}
	return value
}

func TestSessionSavepointNestedSuccessCaughtRollbackAndLifetime(t *testing.T) {
	f := newWriterFixture(t)
	wantErr := errors.New("synthetic-nested")
	var escaped *WriteSession
	var escapedRows *SessionRows
	err := f.authority.WithWriteSession(context.Background(), f.main, func(root *WriteSession) error {
		if _, err := root.Exec("UPDATE data SET value='outer'"); err != nil {
			return err
		}
		if err := root.WithSavepoint(func(child *WriteSession) error {
			escaped = child
			if err := child.CheckBinding(f.authority, f.main); err != nil {
				return err
			}
			if _, err := root.Exec("UPDATE data SET value='parent-escape'"); err == nil || !strings.Contains(err.Error(), "scope_inactive") {
				t.Fatal("parent was not suspended", err)
			}
			if _, err := child.Exec("UPDATE data SET value='nested'"); err != nil {
				return err
			}
			if err := child.WithSavepoint(func(grandchild *WriteSession) error {
				if _, err := grandchild.Exec("UPDATE data SET value='discarded'"); err != nil {
					return err
				}
				return wantErr
			}); !errors.Is(err, wantErr) {
				t.Fatal(err)
			}
			if got := sessionValue(t, child); got != "nested" {
				t.Fatal(got)
			}
			var err error
			escapedRows, err = child.Query("SELECT value FROM data")
			return err
		}); err != nil {
			return err
		}
		if _, err := escaped.Exec("UPDATE data SET value='expired'"); !errors.Is(err, sql.ErrTxDone) {
			t.Fatal(err)
		}
		if err := escaped.WithSavepoint(func(*WriteSession) error { t.Fatal("expired callback"); return nil }); !errors.Is(err, sql.ErrTxDone) {
			t.Fatal(err)
		}
		if escapedRows.Next() {
			t.Fatal("expired nested rows advanced")
		}
		var value string
		if err := escapedRows.Scan(&value); !errors.Is(err, sql.ErrTxDone) {
			t.Fatal(err)
		}
		if got := sessionValue(t, root); got != "nested" {
			t.Fatal(got)
		}
		if err := root.WithSavepoint(func(child *WriteSession) error {
			if _, err := child.Exec("UPDATE data SET value='discarded-again'"); err != nil {
				return err
			}
			return wantErr
		}); !errors.Is(err, wantErr) {
			t.Fatal(err)
		}
		if got := sessionValue(t, root); got != "nested" {
			t.Fatal(got)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	assertMain(t, f, "nested", 0, 1)
}

func TestSessionSavepointRollbackRetainsDurableIntentAndExactGate(t *testing.T) {
	for _, caught := range []bool{false, true} {
		t.Run(map[bool]string{false: "outer-rollback", true: "caught"}[caught], func(t *testing.T) {
			f := newWriterFixture(t)
			wantErr := errors.New("synthetic-after-nested-ack")
			err := f.authority.WithWriteSession(context.Background(), f.main, func(root *WriteSession) error {
				if err := root.RequireReader(2); err != nil {
					return err
				}
				if _, err := root.AppendIntent(Source{"scene", "outer"}); err != nil {
					return err
				}
				if _, err := root.Exec("UPDATE data SET value='outer'"); err != nil {
					return err
				}
				err := root.WithSavepoint(func(child *WriteSession) error {
					if err := child.RequireReader(11); err != nil {
						return err
					}
					if _, err := child.AppendIntent(Source{"scene", "nested"}); err != nil {
						return err
					}
					if _, err := child.Exec("UPDATE data SET value='discarded'"); err != nil {
						return err
					}
					return wantErr
				})
				if !errors.Is(err, wantErr) {
					t.Fatal(err)
				}
				if !caught {
					return err
				}
				if err := root.AssertReady(); err == nil || !strings.Contains(err.Error(), "head_changed") {
					t.Fatal("rolled-back ACK masked durable gap", err)
				}
				if err := root.RequireReader(11); err == nil || !strings.Contains(err.Error(), "head_changed") {
					t.Fatal("reader upgrade masked durable gap", err)
				}
				if _, err := root.AppendIntent(Source{"scene", "after-gap"}); err == nil || !strings.Contains(err.Error(), "head_changed") {
					t.Fatal("append masked durable gap", err)
				}
				// As in the source, no invented final head check blocks ordinary
				// SQL commit. Publication must subsequently reject this gap.
				_, err = root.Exec("UPDATE data SET value='continued'")
				return err
			})
			if caught {
				if err != nil {
					t.Fatal(err)
				}
				assertMain(t, f, "continued", 1, 2)
			} else {
				if !errors.Is(err, wantErr) {
					t.Fatal(err)
				}
				assertMain(t, f, "original", 0, 1)
			}
			head, err := f.authority.Head(context.Background())
			if err != nil || head.Sequence != 2 {
				t.Fatal(head, err)
			}
			assertRestartGap(t, f)
		})
	}
}

func TestSessionSavepointCancellationAndNativeLossRevokeRoot(t *testing.T) {
	for _, canceled := range []bool{false, true} {
		t.Run(map[bool]string{false: "native-rollback", true: "canceled"}[canceled], func(t *testing.T) {
			f := newWriterFixture(t)
			if !canceled {
				if _, err := f.main.Exec("CREATE TRIGGER fault BEFORE UPDATE ON data BEGIN SELECT RAISE(ROLLBACK,'synthetic'); END"); err != nil {
					t.Fatal(err)
				}
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			wantErr := sql.ErrTxDone
			if canceled {
				wantErr = context.Canceled
			}
			err := f.authority.WithWriteSession(ctx, f.main, func(root *WriteSession) error {
				if err := root.WithSavepoint(func(child *WriteSession) error {
					if _, err := child.AppendIntent(Source{"scene", "nested"}); err != nil {
						return err
					}
					if canceled {
						cancel()
					}
					if _, err := child.Exec("UPDATE data SET value='discarded'"); !errors.Is(err, wantErr) {
						t.Fatal(err)
					}
					return nil // Catching the failure cannot restore capability.
				}); !errors.Is(err, wantErr) {
					t.Fatal(err)
				}
				if _, err := root.Exec("INSERT INTO data VALUES('escaped')"); !errors.Is(err, wantErr) {
					t.Fatal(err)
				}
				if err := root.WithSavepoint(func(*WriteSession) error { t.Fatal("revoked callback"); return nil }); !errors.Is(err, wantErr) {
					t.Fatal(err)
				}
				return nil
			})
			if !errors.Is(err, wantErr) {
				t.Fatal(err)
			}
			assertMain(t, f, "original", 0, 1)
			head, err := f.authority.Head(context.Background())
			if err != nil || head.Sequence != 1 {
				t.Fatal(head, err)
			}
		})
	}
}

func TestSessionSavepointCaughtPanicAndProtectedSQL(t *testing.T) {
	f := newWriterFixture(t)
	err := f.authority.WithWriteSession(context.Background(), f.main, func(root *WriteSession) error {
		func() {
			defer func() {
				if recover() != "synthetic" {
					t.Fatal("missing nested panic")
				}
			}()
			_ = root.WithSavepoint(func(child *WriteSession) error {
				if _, err := child.Exec("UPDATE data SET value='discarded'"); err != nil {
					t.Fatal(err)
				}
				panic("synthetic")
			})
		}()
		if got := sessionValue(t, root); got != "original" {
			t.Fatal(got)
		}
		if err := root.WithSavepoint(nil); err == nil {
			t.Fatal("nil callback accepted")
		}
		if err := root.WithSavepoint(func(child *WriteSession) error {
			for _, query := range []string{"SAVEPOINT forged", "RELEASE recovery_write_scope", "ROLLBACK TO recovery_write_scope", "COMMIT", "ROLLBACK", "CREATE TABLE forbidden(value)", "UPDATE recovery_gate SET value='{}'", "PRAGMA foreign_keys=OFF"} {
				if _, err := child.Exec(query); err == nil {
					t.Fatal("nested caller SQL allowed", query)
				}
			}
			return child.EnsureAssertionHistorySchema()
		}); err != nil {
			return err
		}
		if _, err := root.Exec("CREATE TABLE after_release(value)"); err == nil {
			t.Fatal("authorizer not restored")
		}
		_, err := root.Exec("UPDATE data SET value='continued'")
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	assertMain(t, f, "continued", 0, 1)
}

func TestSessionSavepointConstraintErrorsRemainRecoverable(t *testing.T) {
	f := newWriterFixture(t)
	if _, err := f.main.Exec("CREATE UNIQUE INDEX unique_data ON data(value)"); err != nil {
		t.Fatal(err)
	}
	err := f.authority.WithWriteSession(context.Background(), f.main, func(root *WriteSession) error {
		if err := root.WithSavepoint(func(child *WriteSession) error {
			if _, err := child.Exec("UPDATE data SET value='discarded'"); err != nil {
				return err
			}
			_, err := child.Exec("INSERT INTO data VALUES('discarded')")
			return err
		}); err == nil {
			t.Fatal("missing constraint failure")
		}
		if got := sessionValue(t, root); got != "original" {
			t.Fatal("failed child retained earlier SQL", got)
		}
		return root.WithSavepoint(func(child *WriteSession) error {
			if _, err := child.Exec("INSERT INTO data VALUES('original')"); err == nil {
				t.Fatal("missing caught constraint failure")
			}
			_, err := child.Exec("UPDATE data SET value='continued'")
			return err
		})
	})
	if err != nil {
		t.Fatal(err)
	}
	assertMain(t, f, "continued", 0, 1)
}
