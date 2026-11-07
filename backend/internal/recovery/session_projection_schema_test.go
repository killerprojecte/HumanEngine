package recovery

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/mattn/go-sqlite3"
)

// This regression documents the exact extra SQLite operation requested by FTS5
// under the pre-projection DML policy. The baseline policy is intentionally
// frozen here rather than silently broadening arbitrary pragma authority.
func TestProjectionFTSAuthorizerRequirements(t *testing.T) {
	for _, query := range []string{"INSERT INTO memory_fts VALUES('b','scene:s',NULL,'second canary')", "SELECT assertion_id FROM memory_fts WHERE memory_fts MATCH 'canary'"} {
		t.Run(query, func(t *testing.T) {
			f := newWriterFixture(t)
			ctx := context.Background()
			if e := f.authority.WithWriteSession(ctx, f.main, func(s *WriteSession) error { return s.EnsureProjectionProcessorSchema() }); e != nil {
				t.Fatal(e)
			}
			if _, e := f.main.Exec("INSERT INTO memory_fts VALUES('a','scene:s',NULL,'first canary')"); e != nil {
				t.Fatal(e)
			}
			type denied struct {
				op                      int
				first, second, database string
			}
			trace := []denied{}
			e := f.authority.WithWriteSession(ctx, f.main, func(s *WriteSession) error {
				e := s.conn.Raw(func(raw any) error {
					native := raw.(*sqlite3.SQLiteConn)
					native.RegisterAuthorizer(func(op int, first, second, database string) int {
						switch op {
						case sqlite3.SQLITE_SELECT, sqlite3.SQLITE_FUNCTION, sqliteRecursive:
							return sqlite3.SQLITE_OK
						case sqlite3.SQLITE_READ:
							if database == "main" {
								return sqlite3.SQLITE_OK
							}
						case sqlite3.SQLITE_INSERT, sqlite3.SQLITE_UPDATE, sqlite3.SQLITE_DELETE:
							if database == "main" && first != "sqlite_master" && first != "sqlite_schema" && first != "recovery_gate" {
								return sqlite3.SQLITE_OK
							}
						}
						trace = append(trace, denied{op, first, second, database})
						return sqlite3.SQLITE_DENY
					})
					return nil
				})
				if e != nil {
					return e
				}
				_, e = s.Exec(query)
				if e == nil {
					t.Fatal("baseline unexpectedly supports guarded FTS", query)
				}
				t.Logf("query=%s error=%v denied=%+v", query, e, trace)
				if len(trace) != 1 || trace[0] != (denied{sqlite3.SQLITE_PRAGMA, "data_version", "", "main"}) {
					t.Fatal("unexpected FTS authorizer requirements", trace, e)
				}
				return s.protect(0)
			})
			if e != nil {
				t.Fatal(e)
			}
		})
	}
}

// data_version is SQLite-intrinsic read-only, not a setting or a readiness
// token. Test both caller write scopes and the stricter preparation authorizer.
func TestProjectionDataVersionNarrowReadPolicy(t *testing.T) {
	f := newWriterFixture(t)
	ctx := context.Background()
	if e := f.authority.WithWriteSession(ctx, f.main, func(s *WriteSession) error { return s.EnsureProjectionProcessorSchema() }); e != nil {
		t.Fatal(e)
	}
	if _, e := f.main.Exec("INSERT INTO memory_fts VALUES('a','scene:s',NULL,'canary')"); e != nil {
		t.Fatal(e)
	}
	conn, e := f.main.Conn(ctx)
	if e != nil {
		t.Fatal(e)
	}
	defer conn.Close()
	// Attach only a synthetic in-memory database named authority. Production
	// authority files are not attached or made available to the guarded scope.
	if _, e = conn.ExecContext(ctx, "ATTACH DATABASE ':memory:' AS authority; CREATE TABLE authority.denied(value); CREATE TEMP TABLE denied(value)"); e != nil {
		t.Fatal(e)
	}
	// An active transaction makes COMMIT/ROLLBACK successful if the authorizer
	// accidentally permits them; an absent transaction would hide that defect.
	if _, e = conn.ExecContext(ctx, "BEGIN"); e != nil {
		t.Fatal(e)
	}
	defer func() {
		_ = protectMutation(conn, false)
		_, _ = conn.ExecContext(ctx, "ROLLBACK")
	}()
	readInteger := func(query string) int64 {
		var n int64
		if e := conn.QueryRowContext(ctx, query).Scan(&n); e != nil {
			t.Fatal(query, e)
		}
		return n
	}
	userBefore := readInteger("PRAGMA main.user_version")
	schemaBefore := readInteger("PRAGMA main.schema_version")
	s := &WriteSession{writeSessionState: &writeSessionState{conn: conn}}
	for _, readonly := range []bool{false, true} {
		if readonly {
			e = protectPreparation(conn)
		} else {
			e = s.protect(0)
		}
		if e != nil {
			t.Fatal(e)
		}
		var version int64
		if e = conn.QueryRowContext(ctx, "PRAGMA main.data_version").Scan(&version); e != nil {
			t.Fatal(readonly, e)
		}
		var count int
		if e = conn.QueryRowContext(ctx, "SELECT count(*) FROM memory_fts WHERE memory_fts MATCH 'canary'").Scan(&count); e != nil || count != 1 {
			t.Fatal(readonly, count, e)
		}
		changesBefore := readInteger("SELECT total_changes()")
		for _, query := range []string{"PRAGMA main.data_version=''", "PRAGMA main.data_version('')"} {
			if _, e = conn.ExecContext(ctx, query); e != nil {
				t.Fatal("empty arg is indistinguishable and intrinsically read-only", query, e)
			}
		}
		if changesAfter := readInteger("SELECT total_changes()"); changesAfter != changesBefore {
			t.Fatal("empty data_version argument wrote rows", changesBefore, changesAfter)
		}
		for _, query := range []string{"PRAGMA main.data_version=1", "PRAGMA main.data_version(1)", "PRAGMA main.data_version=' '", "PRAGMA main.user_version", "PRAGMA main.user_version=1", "PRAGMA foreign_keys=OFF", "PRAGMA main.writable_schema=ON", "PRAGMA temp.data_version", "PRAGMA authority.data_version", "SELECT value FROM authority.denied", "SELECT value FROM temp.denied", "CREATE TABLE forbidden(value)", "UPDATE recovery_gate SET value='{}'", "ATTACH DATABASE ':memory:' AS forbidden", "SAVEPOINT forged", "COMMIT", "ROLLBACK"} {
			if _, e = conn.ExecContext(ctx, query); e == nil {
				t.Fatal("forbidden caller operation accepted", readonly, query)
			}
		}
		if readonly {
			if _, e = conn.ExecContext(ctx, "INSERT INTO memory_fts VALUES('b','scene:s',NULL,'forbidden')"); e == nil {
				t.Fatal("preparation gained writes")
			}
		}
		if e = protectMutation(conn, false); e != nil {
			t.Fatal(e)
		}
		if userAfter, schemaAfter := readInteger("PRAGMA main.user_version"), readInteger("PRAGMA main.schema_version"); userAfter != userBefore || schemaAfter != schemaBefore {
			t.Fatal("pragma changed a cookie", userBefore, userAfter, schemaBefore, schemaAfter)
		}
	}
	if e = s.protect(3); e != nil {
		t.Fatal(e)
	}
	var controlVersion int64
	if e = conn.QueryRowContext(ctx, "PRAGMA main.data_version").Scan(&controlVersion); e != nil {
		t.Fatal("native segment flush requires the same read-only counter", e)
	}
	if e = protectMutation(conn, false); e != nil {
		t.Fatal(e)
	}
}

// Probe the native FTS work invoked by owned savepoint creation after a write.
func TestProjectionFTSSavepointRequirements(t *testing.T) {
	f := newWriterFixture(t)
	ctx := context.Background()
	type deniedOperation struct {
		op                        int
		table, argument, database string
	}
	denied := []deniedOperation{}
	e := f.authority.WithWriteSession(ctx, f.main, func(s *WriteSession) error {
		if e := s.EnsureProjectionProcessorSchema(); e != nil {
			return e
		}
		if _, e := s.Exec("INSERT INTO memory_fts VALUES('a','scene:s',NULL,'canary')"); e != nil {
			return e
		}
		e := s.conn.Raw(func(raw any) error {
			raw.(*sqlite3.SQLiteConn).RegisterAuthorizer(func(op int, first, second, database string) int {
				if op == sqlite3.SQLITE_SAVEPOINT {
					return sqlite3.SQLITE_OK
				}
				denied = append(denied, deniedOperation{op, first, second, database})
				t.Logf("savepoint denied: op=%d first=%q second=%q database=%q", op, first, second, database)
				return sqlite3.SQLITE_DENY
			})
			return nil
		})
		if e != nil {
			return e
		}
		_, e = s.conn.ExecContext(ctx, "SAVEPOINT recovery_write_scope")
		t.Logf("savepoint error: %v", e)
		if e == nil || len(denied) != 1 || denied[0] != (deniedOperation{sqlite3.SQLITE_INSERT, "memory_fts_data", "", "main"}) {
			t.Fatalf("unexpected frozen control policy result: %v %+v", e, denied)
		}
		return s.protect(0)
	})
	if e != nil {
		t.Fatal(e)
	}
}

// Capture the complete set of native operations during segment flush/merge,
// not just the first denied insert. The recorder applies the production narrow
// predicate and restores ordinary protection before any caller operation.
func TestProjectionFTSControlFlushMergeAndRollback(t *testing.T) {
	f := newWriterFixture(t)
	ctx := context.Background()
	type operation struct {
		op                        int
		table, argument, database string
	}
	observed := map[operation]bool{}
	e := f.authority.WithWriteSession(ctx, f.main, func(s *WriteSession) error {
		if e := s.EnsureProjectionProcessorSchema(); e != nil {
			return e
		}
		control := func(statement string) error {
			e := s.conn.Raw(func(raw any) error {
				raw.(*sqlite3.SQLiteConn).RegisterAuthorizer(func(op int, table, argument, database string) int {
					if op == sqlite3.SQLITE_SAVEPOINT {
						return sqlite3.SQLITE_OK
					}
					item := operation{op, table, argument, database}
					observed[item] = true
					if projectionSavepointIO(op, table, argument, database) {
						return sqlite3.SQLITE_OK
					}
					t.Errorf("unexpected native control operation: %+v", item)
					return sqlite3.SQLITE_DENY
				})
				return nil
			})
			if e != nil {
				return e
			}
			_, e = s.conn.ExecContext(ctx, statement)
			return errors.Join(e, s.protect(0))
		}
		// Independent segments force SQLite's default automatic merge and its
		// deletes. This is fixture size, not a new runtime admission/retention cap.
		for i := 0; i < 48; i++ {
			content := strings.Repeat(fmt.Sprintf("term%d alpha beta gamma delta ", i), 200)
			if _, e := s.Exec("INSERT INTO memory_fts VALUES(?, 'scene:s',NULL,?)", fmt.Sprintf("id-%d", i), content); e != nil {
				return e
			}
			if e := control("SAVEPOINT recovery_write_scope"); e != nil {
				return e
			}
			if _, e := s.Exec("INSERT INTO memory_fts VALUES('discard','scene:s',NULL,'discardcanary')"); e != nil {
				return e
			}
			if e := control("ROLLBACK TO recovery_write_scope"); e != nil {
				return e
			}
			if e := control("RELEASE recovery_write_scope"); e != nil {
				return e
			}
		}
		// Real child scopes must resume the parent, expire the child, and roll back
		// flushed grandchildren together with their failed parent's lexical rows.
		var escaped *WriteSession
		rollback := errors.New("synthetic_child_rollback")
		e := s.WithSavepoint(func(child *WriteSession) error {
			escaped = child
			if _, e := child.Exec("INSERT INTO memory_fts VALUES('rolledback','scene:s',NULL,'rollbackcanary')"); e != nil {
				return e
			}
			if e := child.WithSavepoint(func(grandchild *WriteSession) error {
				_, e := grandchild.Exec("INSERT INTO memory_fts VALUES('grandchild','scene:s',NULL,'grandchildcanary')")
				return e
			}); e != nil {
				return e
			}
			return rollback
		})
		if !errors.Is(e, rollback) {
			return fmt.Errorf("caught child rollback: %w", e)
		}
		if _, e := escaped.Exec("DELETE FROM memory_fts"); !errors.Is(e, sql.ErrTxDone) {
			return fmt.Errorf("escaped child reused: %v", e)
		}
		rows, e := s.Query("SELECT count(*) FROM memory_fts WHERE memory_fts MATCH 'discardcanary OR rollbackcanary OR grandchildcanary'")
		if e != nil {
			return e
		}
		var n int
		if !rows.Next() {
			return errors.Join(rows.Err(), rows.Close(), errors.New("missing count"))
		}
		e = rows.Scan(&n)
		e = errors.Join(e, rows.Close())
		if e != nil {
			return e
		}
		if n != 0 {
			return fmt.Errorf("rollback leaked %d rows", n)
		}
		// Caller code never runs with control privileges, even after restoration.
		for _, query := range []string{"SAVEPOINT forged", "RELEASE recovery_write_scope", "ROLLBACK TO recovery_write_scope", "COMMIT", "ROLLBACK", "CREATE TABLE forbidden(value)", "UPDATE recovery_gate SET value='{}'"} {
			if _, e := s.Exec(query); e == nil {
				return fmt.Errorf("caller control accepted: %s", query)
			}
		}
		_, e = s.Exec("INSERT INTO memory_fts VALUES('kept','scene:s',NULL,'keptcanary')")
		return e
	})
	if e != nil {
		t.Fatal(e)
	}
	for item := range observed {
		t.Logf("native control operation: %+v", item)
	}
	for _, item := range []operation{{sqlite3.SQLITE_READ, "memory_fts_config", "k", "main"}, {sqlite3.SQLITE_READ, "memory_fts_config", "v", "main"}, {sqlite3.SQLITE_PRAGMA, "data_version", "", "main"}, {sqlite3.SQLITE_INSERT, "memory_fts_data", "", "main"}, {sqlite3.SQLITE_INSERT, "memory_fts_idx", "", "main"}, {sqlite3.SQLITE_DELETE, "memory_fts_data", "", "main"}, {sqlite3.SQLITE_DELETE, "memory_fts_idx", "", "main"}} {
		if !observed[item] {
			t.Errorf("fixture did not exercise segment operation: %+v", item)
		}
	}
	var n int
	if e := f.main.QueryRow("SELECT count(*) FROM memory_fts WHERE memory_fts MATCH 'keptcanary'").Scan(&n); e != nil || n != 1 {
		t.Fatal(n, e)
	}
}

func TestProjectionControlPermissionBoundary(t *testing.T) {
	// This verifies the actual registered mode, with real active transaction and
	// schemas, not only a boolean helper's synthetic argument tests.
	f := newWriterFixture(t)
	ctx := context.Background()
	if e := f.authority.WithWriteSession(ctx, f.main, func(s *WriteSession) error { return s.EnsureProjectionProcessorSchema() }); e != nil {
		t.Fatal(e)
	}
	conn, e := f.main.Conn(ctx)
	if e != nil {
		t.Fatal(e)
	}
	defer conn.Close()
	if _, e = conn.ExecContext(ctx, `ATTACH ':memory:' AS authority;CREATE TABLE authority.memory_fts_data(id,block); CREATE TEMP TABLE memory_fts_data(id,block); BEGIN`); e != nil {
		t.Fatal(e)
	}
	defer func() { _ = protectMutation(conn, false); _, _ = conn.ExecContext(ctx, "ROLLBACK") }()
	s := &WriteSession{writeSessionState: &writeSessionState{conn: conn}}
	if e = s.protect(3); e != nil {
		t.Fatal(e)
	}
	for _, query := range []string{"INSERT INTO data VALUES('forbidden')", "UPDATE recovery_gate SET value='{}'", "INSERT INTO memory_fts_content(id,c0) VALUES(1,'forbidden')", "INSERT INTO memory_fts_docsize VALUES(1,x'00')", "INSERT INTO memory_fts_config VALUES('forbidden_new_config',4)", "UPDATE memory_fts_config SET v=5", "DELETE FROM memory_fts_config", "UPDATE main.memory_fts_data SET block=x'00'", "SELECT value FROM data", "SELECT name FROM sqlite_master", "INSERT INTO authority.memory_fts_data VALUES(1,x'00')", "INSERT INTO temp.memory_fts_data VALUES(1,x'00')", "PRAGMA main.data_version=1", "PRAGMA main.data_version(1)", "PRAGMA main.data_version=' '", "PRAGMA temp.data_version", "PRAGMA authority.data_version", "PRAGMA main.user_version=2", "SELECT random()", "CREATE TABLE forbidden(value)", "ATTACH ':memory:' AS forbidden", "COMMIT", "ROLLBACK"} {
		if _, e = conn.ExecContext(ctx, query); e == nil {
			t.Fatal("control mode expanded beyond shadow IO", query)
		}
	}
	if active, e := transactionActive(conn); e != nil || !active {
		t.Fatal("denied control lost transaction", active, e)
	}
	if e = protectMutation(conn, false); e != nil {
		t.Fatal(e)
	}
}
