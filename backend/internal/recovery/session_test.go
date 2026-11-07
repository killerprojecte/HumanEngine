package recovery

import (
	"context"
	"database/sql"
	"errors"
	"github.com/mattn/go-sqlite3"
	"strings"
	"testing"
)

func TestSessionCaughtErrorsAndLostTransaction(t *testing.T) {
	for _, rollback := range []bool{false, true} {
		t.Run(map[bool]string{false: "abort", true: "rollback"}[rollback], func(t *testing.T) {
			f := newWriterFixture(t)
			raise := "ABORT"
			if rollback {
				raise = "ROLLBACK"
			}
			if _, e := f.main.Exec("CREATE TRIGGER fault BEFORE UPDATE ON data BEGIN SELECT RAISE(" + raise + ",'synthetic');END"); e != nil {
				t.Fatal(e)
			}
			var retained *WriteSession
			var continued error
			e := f.authority.WithWriteSession(context.Background(), f.main, func(s *WriteSession) error {
				retained = s
				if e := s.RequireReader(2); e != nil {
					return e
				}
				if _, e := s.AppendIntent(Source{"scene", "message"}); e != nil {
					return e
				}
				if _, e := s.Exec("UPDATE data SET value='bad'"); e == nil {
					t.Fatal("expected trigger fault")
				}
				_, continued = s.Exec("INSERT INTO data VALUES('continued')")
				return nil
			})
			if rollback {
				if e == nil || !errors.Is(continued, sql.ErrTxDone) {
					t.Fatal(e, continued)
				}
			} else if e != nil || continued != nil {
				t.Fatal(e, continued)
			}
			if _, e = retained.Exec("INSERT INTO data VALUES('escaped')"); !errors.Is(e, sql.ErrTxDone) {
				t.Fatal(e)
			}
			var count int
			_ = f.main.QueryRow("SELECT count(*) FROM data").Scan(&count)
			expected := 2
			if rollback {
				expected = 1
			}
			if count != expected {
				t.Fatal(count, expected)
			}
			gate, e := ReadGate(context.Background(), f.main)
			if e != nil {
				t.Fatal(e)
			}
			if (gate.Head.Sequence == 1) == rollback {
				t.Fatal("ACK rollback", gate)
			}
			var entries int
			_ = f.external.QueryRow("SELECT count(*) FROM revocations").Scan(&entries)
			if entries != 1 {
				t.Fatal(entries)
			}
		})
	}
}
func TestSessionGateFailureCaughtPreservesGap(t *testing.T) {
	f := newWriterFixture(t)
	_, e := f.main.Exec(`CREATE TRIGGER fault BEFORE UPDATE ON recovery_gate WHEN json_extract(NEW.value,'$.head.sequence')>0 BEGIN SELECT RAISE(ABORT,'gate-fault');END`)
	if e != nil {
		t.Fatal(e)
	}
	e = f.authority.WithWriteSession(context.Background(), f.main, func(s *WriteSession) error {
		if _, e := s.AppendIntent(Source{"scene", "message"}); e == nil {
			t.Fatal("expected gate failure")
		}
		_, e := s.Exec("UPDATE data SET value='caught'")
		return e
	})
	if e != nil {
		t.Fatal(e)
	}
	assertMain(t, f, "caught", 0, 1)
	if _, e = publishValue(f.authority, f.main); e == nil {
		t.Fatal("gap published")
	}
}
func TestSessionGateExtensionsAndExternalAdvance(t *testing.T) {
	f := newWriterFixture(t)
	_, e := f.main.Exec(`UPDATE recovery_gate SET value=json_set(value,'$.extension',json('{"surrogate":"\ud800","nested":[3]}'),'$.head.extra','keep-until-replaced')`)
	if e != nil {
		t.Fatal(e)
	}
	other, e := OpenWriter(context.Background(), f.gate.Authority)
	if e != nil {
		t.Fatal(e)
	}
	defer other.Close()
	e = f.authority.WithWriteSession(context.Background(), f.main, func(s *WriteSession) error {
		if e := s.RequireReader(2); e != nil {
			return e
		}
		var value string
		rows, e := s.Query("SELECT value FROM recovery_gate")
		if e != nil {
			return e
		}
		if !rows.Next() {
			t.Fatal(rows.Err())
		}
		_ = rows.Scan(&value)
		rows.Close()
		if !strings.Contains(value, "keep-until-replaced") || !strings.Contains(value, `\ud800`) {
			t.Fatal(value)
		}
		head, e := s.AppendIntent(Source{"scene", "own"})
		if e != nil {
			return e
		}
		if _, e = other.Append(context.Background(), Source{"scene", "external"}, head, false); e != nil {
			return e
		}
		_, e = s.Exec("UPDATE data SET value='committed-behind'")
		return e
	})
	if e != nil {
		t.Fatal(e)
	}
	assertMain(t, f, "committed-behind", 1, 2)
	var raw string
	_ = f.main.QueryRow("SELECT value FROM recovery_gate").Scan(&raw)
	if !strings.Contains(raw, `\ud800`) || strings.Contains(raw, "keep-until-replaced") {
		t.Fatal(raw)
	}
	if _, e = publishValue(f.authority, f.main); e == nil {
		t.Fatal("external gap published")
	}
}
func TestSessionCapabilitiesAndRowsExpire(t *testing.T) {
	f := newWriterFixture(t)
	var retained *SessionRows
	e := f.authority.WithWriteSession(context.Background(), f.main, func(s *WriteSession) error {
		for _, q := range []string{"COMMIT", "CREATE TABLE forbidden(value)", "UPDATE recovery_gate SET value='{}'", "PRAGMA user_version=2"} {
			if _, e := s.Exec(q); e == nil {
				t.Fatalf("allowed %s", q)
			}
		}
		if e := s.EnsureSourceEventSchema(); e != nil {
			return e
		}
		rows, e := s.Query("SELECT value FROM data")
		retained = rows
		return e
	})
	if e != nil {
		t.Fatal(e)
	}
	if retained.Next() {
		t.Fatal("retained rows advanced")
	}
	var value string
	if e = retained.Scan(&value); !errors.Is(e, sql.ErrTxDone) {
		t.Fatal(e)
	}
}
func TestSessionPanicAndUncertainCommit(t *testing.T) {
	f := newWriterFixture(t)
	func() {
		defer func() {
			if recover() == nil {
				t.Fatal("expected panic")
			}
		}()
		_ = f.authority.WithWriteSession(context.Background(), f.main, func(s *WriteSession) error { _, _ = s.Exec("UPDATE data SET value='rolled-back'"); panic("synthetic") })
	}()
	assertMain(t, f, "original", 0, 1)
	e := f.authority.withWriteSession(context.Background(), f.main, func(s *WriteSession) error { _, e := s.Exec("UPDATE data SET value='committed'"); return e }, func(ctx context.Context, c *sql.Conn, _ string) error {
		_, e := c.ExecContext(ctx, "COMMIT")
		if e != nil {
			return e
		}
		return errors.New("synthetic-after-commit")
	})
	if !errors.Is(e, ErrCommitOutcomeUnknown) {
		t.Fatal(e)
	}
	assertMain(t, f, "committed", 0, 1)
}

func TestSessionCaughtQueryStepScanAndActiveRows(t *testing.T) {
	f := newWriterFixture(t)
	e := f.authority.WithWriteSession(context.Background(), f.main, func(s *WriteSession) error {
		if _, e := s.Query("SELECT missing_column FROM data"); e == nil {
			t.Fatal("missing query error")
		}
		rows, e := s.Query("SELECT abs(-9223372036854775808)")
		if e == nil {
			if rows.Next() || rows.Err() == nil {
				t.Fatal("missing step error")
			}
			_ = rows.Close()
		}
		rows, e = s.Query("SELECT value FROM data")
		if e != nil {
			return e
		}
		if !rows.Next() {
			return rows.Err()
		}
		var integer int
		if e = rows.Scan(&integer); e == nil {
			t.Fatal("missing scan error")
		}
		_ = rows.Close()
		for i := 0; i < 1000; i++ {
			r, e := s.Query("SELECT value FROM data")
			if e != nil {
				return e
			}
			if !r.Next() {
				return r.Err()
			}
			var value string
			if e = r.Scan(&value); e != nil {
				return e
			}
			if e = r.Close(); e != nil {
				return e
			}
		}
		s.mu.Lock()
		active := len(s.rows)
		s.mu.Unlock()
		if active != 0 {
			t.Fatal("retained closed handles", active)
		}
		_, e = s.Exec("UPDATE data SET value='continued'")
		return e
	})
	if e != nil {
		t.Fatal(e)
	}
	assertMain(t, f, "continued", 0, 1)
}
func TestSessionQueryRollbackCannotAutocommit(t *testing.T) {
	f := newWriterFixture(t)
	if _, e := f.main.Exec(`CREATE TRIGGER fault BEFORE UPDATE ON data BEGIN SELECT RAISE(ROLLBACK,'query-rollback');END`); e != nil {
		t.Fatal(e)
	}
	e := f.authority.WithWriteSession(context.Background(), f.main, func(s *WriteSession) error {
		rows, e := s.Query("UPDATE data SET value='rolled-back' RETURNING value")
		if e == nil {
			if rows.Next() {
				t.Fatal("unexpected row")
			}
			e = rows.Err()
			rows.Close()
		}
		if e == nil {
			t.Fatal("missing query rollback")
		}
		if _, e = s.Exec("INSERT INTO data VALUES('escaped')"); !errors.Is(e, sql.ErrTxDone) {
			t.Fatal(e)
		}
		return nil
	})
	if !errors.Is(e, sql.ErrTxDone) {
		t.Fatal(e)
	}
	assertMain(t, f, "original", 0, 1)
}
func TestSessionReadyLayerAndBinding(t *testing.T) {
	f := newWriterFixture(t)
	other := newWriterFixture(t)
	_, e := f.main.Exec(`UPDATE recovery_gate SET value=json_set(value,'$.state','quarantine','$.minimumReader',99)`)
	if e != nil {
		t.Fatal(e)
	}
	e = f.authority.WithWriteSession(context.Background(), f.main, func(s *WriteSession) error {
		if e := s.CheckBinding(other.authority, other.main); e == nil {
			t.Fatal("foreign binding")
		}
		if e := s.AssertReady(); e == nil {
			t.Fatal("ready gate accepted")
		}
		if e := s.RequireReader(2); e == nil {
			t.Fatal("reader gate accepted")
		}
		_, e := s.AppendIntent(Source{"scene", "layered"})
		return e
	})
	if e != nil {
		t.Fatal("append primitive matches TS revoke", e)
	}
	var raw string
	if e = f.main.QueryRow("SELECT value FROM recovery_gate").Scan(&raw); e != nil {
		t.Fatal(e)
	}
	gate, _, e := decodeGate(raw)
	if e != nil || gate.Head.Sequence != 1 || gate.MinimumReader != 99 || gate.State != "quarantine" {
		t.Fatal(gate, e)
	}
}

func TestSessionExhaustedRowsReleaseRegistration(t *testing.T) {
	f := newWriterFixture(t)
	e := f.authority.WithWriteSession(context.Background(), f.main, func(s *WriteSession) error {
		for i := 0; i < 1000; i++ {
			rows, e := s.Query("SELECT value FROM data")
			if e != nil {
				return e
			}
			for rows.Next() {
				var v string
				if e = rows.Scan(&v); e != nil {
					return e
				}
			}
			if e = rows.Err(); e != nil {
				return e
			}
		}
		if len(s.rows) != 0 {
			t.Fatal("EOF retained rows", len(s.rows))
		}
		return nil
	})
	if e != nil {
		t.Fatal(e)
	}
}
func TestSessionCountAuthorizerAndOtherDatabases(t *testing.T) {
	f := newWriterFixture(t)
	ctx := context.Background()
	conn, e := f.main.Conn(ctx)
	if e != nil {
		t.Fatal(e)
	}
	var observed bool
	e = conn.Raw(func(raw any) error {
		native := raw.(*sqlite3.SQLiteConn)
		native.RegisterAuthorizer(func(op int, table, column, database string) int {
			if op == sqlite3.SQLITE_READ && table == "data" && column == "" && database == "" {
				observed = true
			}
			return sqlite3.SQLITE_OK
		})
		return nil
	})
	if e != nil {
		t.Fatal(e)
	}
	var count int
	if e = conn.QueryRowContext(ctx, "SELECT count(*) FROM data").Scan(&count); e != nil {
		t.Fatal(e)
	}
	_ = conn.Raw(func(raw any) error { raw.(*sqlite3.SQLiteConn).RegisterAuthorizer(nil); return nil })
	if !observed {
		t.Fatal("driver no longer emits the measured count(*) blank-database read")
	}
	if _, e = conn.ExecContext(ctx, "ATTACH DATABASE ':memory:' AS auxiliary;CREATE TABLE auxiliary.data(value);INSERT INTO auxiliary.data VALUES('foreign');CREATE TEMP TABLE temporary(value);INSERT INTO temporary VALUES('foreign')"); e != nil {
		t.Fatal(e)
	}
	conn.Close()
	e = f.authority.WithWriteSession(ctx, f.main, func(s *WriteSession) error {
		rows, e := s.Query("SELECT count(*) FROM main.data")
		if e != nil {
			return e
		}
		if !rows.Next() {
			return rows.Err()
		}
		if e = rows.Scan(&count); e != nil {
			return e
		}
		rows.Close()
		if count != 1 {
			t.Fatal(count)
		}
		for _, query := range []string{"SELECT count(value) FROM auxiliary.data", "SELECT count(*) FROM auxiliary.data", "SELECT value FROM temporary", "SELECT count(*) FROM temporary", "PRAGMA database_list"} {
			rows, e := s.Query(query)
			if e == nil {
				rows.Close()
				t.Fatal("foreign/pragma allowed", query)
			}
		}
		return nil
	})
	if e != nil {
		t.Fatal(e)
	}
}

func TestSessionCancellationExpiresCapability(t *testing.T) {
	f := newWriterFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	e := f.authority.WithWriteSession(ctx, f.main, func(s *WriteSession) error {
		if _, e := s.Exec("UPDATE data SET value='cancelled'"); e != nil {
			return e
		}
		cancel()
		if _, e := s.Exec("INSERT INTO data VALUES('escaped')"); !errors.Is(e, context.Canceled) {
			t.Fatal(e)
		}
		return nil
	})
	if !errors.Is(e, context.Canceled) {
		t.Fatal(e)
	}
	assertMain(t, f, "original", 0, 1)
}
