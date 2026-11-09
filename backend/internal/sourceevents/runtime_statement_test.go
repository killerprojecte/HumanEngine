package sourceevents

import (
	"context"
	"database/sql"
	"errors"
	"github.com/killerprojecte/HumanEngine/backend/internal/botruntime"
	"github.com/killerprojecte/HumanEngine/backend/internal/recovery"
	"testing"
)

func TestRuntimeAssertionPreparedReadsLeaseWriterAndLoss(t *testing.T) {
	for _, writer := range []bool{false, true} {
		t.Run(map[bool]string{false: "publication", true: "writer"}[writer], func(t *testing.T) {
			f, _ := runtimeFixture(t, Options{})
			ctx := context.Background()
			f.main.SetMaxOpenConns(1)
			if _, e := f.main.Exec("CREATE TABLE IF NOT EXISTS memories(id TEXT PRIMARY KEY,value TEXT);INSERT INTO memories(id,value) VALUES('a','old')"); e != nil {
				t.Fatal(e)
			}
			var held RuntimeReadStatement
			var rows Rows
			var saved RuntimeReadScope
			check := func(s *Store, want string) error {
				return s.WithRuntimeReads(ctx, func(reads botruntime.ReadAuthority) error {
					return s.UseRuntimeReadScope(reads, func(scope RuntimeReadScope) error {
						saved = scope
						var e error
						if _, e = scope.PrepareAssertionRead("DELETE FROM memories RETURNING value"); e == nil {
							t.Fatal("write statement accepted")
						}
						held, e = scope.PrepareAssertionRead("SELECT value FROM memories WHERE id=?")
						if e != nil {
							return e
						}
						rows, e = held.Query("a")
						if e != nil {
							return e
						}
						if !rows.Next() {
							t.Fatal("row absent")
						}
						var value string
						if e = rows.Scan(&value); e != nil {
							return e
						}
						if value != want {
							t.Fatalf("got %s want %s", value, want)
						}
						return nil
					})
				})
			}
			if writer {
				e := f.authority.WithWriteSession(ctx, f.main, func(ws *recovery.WriteSession) error {
					if _, e := ws.Exec("UPDATE memories SET value='uncommitted' WHERE id='a'"); e != nil {
						return e
					}
					if e := check(f.store.WithSession(ws), "uncommitted"); e != nil {
						return e
					}
					if _, e := held.Query("a"); !errors.Is(e, sql.ErrTxDone) {
						t.Fatalf("statement survived lease: %v", e)
					}
					return ws.AssertReady()
				})
				if e != nil {
					t.Fatal(e)
				}
			} else if e := check(f.store, "old"); e != nil {
				t.Fatal(e)
			}
			if _, e := saved.PrepareAssertionRead("SELECT value FROM memories WHERE id=?"); !errors.Is(e, sql.ErrTxDone) {
				t.Fatal("prepare survived", e)
			}
			if _, e := held.Query("a"); !errors.Is(e, sql.ErrTxDone) {
				t.Fatal("query survived", e)
			}
			if rows.Next() || !errors.Is(rows.Err(), sql.ErrTxDone) {
				t.Fatal("rows survived", rows.Err())
			}
			var out string
			if e := rows.Scan(&out); !errors.Is(e, sql.ErrTxDone) {
				t.Fatal("scan survived", e)
			}
			_ = rows.Close()
			_ = held.Close()
		})
	}
	f, _ := runtimeFixture(t, Options{})
	ctx := context.Background()
	f.main.SetMaxOpenConns(1)
	if _, e := f.main.Exec("CREATE TABLE IF NOT EXISTS memories(id TEXT PRIMARY KEY,value TEXT); INSERT INTO memories(id,value) VALUES('a','old'); CREATE TRIGGER lose_assertion_read BEFORE INSERT ON config BEGIN SELECT RAISE(ROLLBACK,'synthetic_loss'); END"); e != nil {
		t.Fatal(e)
	}
	e := f.authority.WithWriteSession(ctx, f.main, func(ws *recovery.WriteSession) error {
		return f.store.WithSession(ws).WithRuntimeReads(ctx, func(reads botruntime.ReadAuthority) error {
			return f.store.UseRuntimeReadScope(reads, func(scope RuntimeReadScope) error {
				statement, e := scope.PrepareAssertionRead("SELECT value FROM memories WHERE id=?")
				if e != nil {
					return e
				}
				if _, e = ws.Exec("INSERT INTO config VALUES('loss','rollback')"); e == nil {
					t.Fatal("loss not raised")
				}
				if _, e = statement.Query("a"); !errors.Is(e, sql.ErrTxDone) {
					t.Fatal("lost transaction reused", e)
				}
				if _, e = scope.PrepareAssertionRead("SELECT value FROM memories WHERE id=?"); !errors.Is(e, sql.ErrTxDone) {
					t.Fatal("lost transaction prepared", e)
				}
				if _, e = ws.Exec("UPDATE memories SET value='escaped'"); !errors.Is(e, sql.ErrTxDone) {
					t.Fatal("autocommit escape", e)
				}
				return nil
			})
		})
	})
	if e == nil {
		t.Fatal("swallowed native loss committed")
	}
	var raw string
	if e := f.main.QueryRow("SELECT value FROM memories WHERE id='a'").Scan(&raw); e != nil || raw != "old" {
		t.Fatal("lost write survived", raw, e)
	}
}
