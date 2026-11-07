package recovery

import (
	"context"
	"github.com/mattn/go-sqlite3"
	"testing"
)

// SQLite's count fast path omits the database authorizer argument unless the
// table is qualified. Keep the fixed query efficient and explicit, rather than
// granting ambiguous reads or converting COUNT(*) to an expression row scan.
func TestWorkingSetCountUsesQualifiedMain(t *testing.T) {
	f := newWriterFixture(t)
	if _, e := f.main.Exec("ATTACH ':memory:' AS other; CREATE TABLE other.memory_working_set_receipts(value); INSERT INTO other.memory_working_set_receipts VALUES(1),(2),(3)"); e != nil {
		t.Fatal(e)
	}
	type operation struct {
		op            int
		name, arg, db string
	}
	denied := []operation{}
	e := f.authority.WithWriteSession(context.Background(), f.main, func(s *WriteSession) error {
		if e := s.EnsureWorkingSetSchema(); e != nil {
			return e
		}
		// TEXT PRIMARY KEY is nullable in this rowid schema. COUNT(*) still counts it.
		if _, e := s.Exec("INSERT INTO memory_working_set_receipts VALUES('key',1000,'{}'),(NULL,1000,'{}')"); e != nil {
			return e
		}
		if e := s.conn.Raw(func(raw any) error {
			raw.(*sqlite3.SQLiteConn).RegisterAuthorizer(func(op int, name, arg, db string) int {
				switch op {
				case sqlite3.SQLITE_SELECT, sqlite3.SQLITE_FUNCTION, sqliteRecursive:
					return sqlite3.SQLITE_OK
				case sqlite3.SQLITE_READ:
					if db == "main" {
						return sqlite3.SQLITE_OK
					}
				case sqlite3.SQLITE_INSERT, sqlite3.SQLITE_UPDATE, sqlite3.SQLITE_DELETE:
					if db == "main" && name != "sqlite_master" && name != "sqlite_schema" && name != "recovery_gate" {
						return sqlite3.SQLITE_OK
					}
				}
				denied = append(denied, operation{op, name, arg, db})
				return sqlite3.SQLITE_DENY
			})
			return nil
		}); e != nil {
			return e
		}
		var count int
		e := s.conn.QueryRowContext(s.ctx, "SELECT count(*) FROM memory_working_set_receipts").Scan(&count)
		if e == nil || len(denied) != 1 || denied[0] != (operation{sqlite3.SQLITE_READ, "memory_working_set_receipts", "", ""}) {
			t.Fatalf("ambiguous count: %v %+v", e, denied)
		}
		if e = s.protect(0); e != nil {
			return e
		}
		if e = s.conn.QueryRowContext(s.ctx, "SELECT count(*) FROM main.memory_working_set_receipts").Scan(&count); e != nil {
			return e
		}
		if count != 2 {
			t.Fatalf("main count = %d", count)
		}
		if e = s.conn.QueryRowContext(s.ctx, "SELECT count(*) FROM other.memory_working_set_receipts").Scan(&count); e == nil {
			t.Fatal("attached count allowed")
		}
		return nil
	})
	if e != nil {
		t.Fatal(e)
	}
}
