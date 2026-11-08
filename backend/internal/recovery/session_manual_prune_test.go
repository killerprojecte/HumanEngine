package recovery

import (
	"context"
	"testing"

	"github.com/mattn/go-sqlite3"
)

// Keep the actual caller authorizer unchanged. SQLite's rowid-only NOT IN
// subquery emits a blank-database READ unless both table references are qualified.
func TestManualReceiptPruneUsesQualifiedMain(t *testing.T) {
	f := newWriterFixture(t)
	if _, e := f.main.Exec("ATTACH ':memory:' AS other; CREATE TABLE other.memory_command_receipts(command_key TEXT); INSERT INTO other.memory_command_receipts VALUES('foreign')"); e != nil {
		t.Fatal(e)
	}
	type operation struct {
		op                      int
		table, column, database string
	}
	denied := []operation{}
	e := f.authority.WithWriteSession(context.Background(), f.main, func(s *WriteSession) error {
		if e := s.EnsureManualCommandSchema(); e != nil {
			return e
		}
		if _, e := s.Exec("INSERT INTO main.memory_command_receipts VALUES('key','id','hash',0,1,'now')"); e != nil {
			return e
		}
		if e := s.conn.Raw(func(raw any) error {
			raw.(*sqlite3.SQLiteConn).RegisterAuthorizer(func(op int, table, column, database string) int {
				switch op {
				case sqlite3.SQLITE_SELECT, sqlite3.SQLITE_FUNCTION, sqliteRecursive:
					return sqlite3.SQLITE_OK
				case sqlite3.SQLITE_READ:
					if database == "main" {
						return sqlite3.SQLITE_OK
					}
				case sqlite3.SQLITE_INSERT, sqlite3.SQLITE_UPDATE, sqlite3.SQLITE_DELETE:
					if database == "main" && table != "sqlite_master" && table != "sqlite_schema" && table != "recovery_gate" {
						return sqlite3.SQLITE_OK
					}
				}
				denied = append(denied, operation{op, table, column, database})
				return sqlite3.SQLITE_DENY
			})
			return nil
		}); e != nil {
			return e
		}
		_, e := s.Exec("DELETE FROM memory_command_receipts WHERE rowid NOT IN (SELECT rowid FROM memory_command_receipts ORDER BY rowid DESC LIMIT 4096)")
		if e == nil || len(denied) != 1 || denied[0] != (operation{sqlite3.SQLITE_READ, "memory_command_receipts", "", ""}) {
			t.Fatalf("unqualified prune: %v %+v", e, denied)
		}
		if e = s.protect(0); e != nil {
			return e
		}
		if _, e = s.Exec("DELETE FROM main.memory_command_receipts WHERE rowid NOT IN (SELECT rowid FROM main.memory_command_receipts ORDER BY rowid DESC LIMIT 4096)"); e != nil {
			return e
		}
		var n int
		if e = s.conn.QueryRowContext(s.ctx, "SELECT count(*) FROM main.memory_command_receipts").Scan(&n); e != nil {
			return e
		}
		if n != 1 {
			t.Fatal("valid retry unexpectedly pruned")
		}
		if _, e = s.Exec("DELETE FROM main.memory_command_receipts WHERE rowid NOT IN (SELECT rowid FROM other.memory_command_receipts ORDER BY rowid DESC LIMIT 4096)"); e == nil {
			t.Fatal("attached read authorized")
		}
		if _, e = s.Exec("DELETE FROM other.memory_command_receipts"); e == nil {
			t.Fatal("attached write authorized")
		}
		return nil
	})
	if e != nil {
		t.Fatal(e)
	}
}
