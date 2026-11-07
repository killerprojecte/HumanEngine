package recovery

import (
	"context"
	"database/sql"
	"errors"
	"testing"
)

func TestWorkingSetPreparedQueriesAreNativeScopedAndFresh(t *testing.T) {
	f := newWriterFixture(t)
	ctx := context.Background()
	if _, e := f.main.Exec("CREATE TABLE prepared_values(id INTEGER PRIMARY KEY,value TEXT);INSERT INTO prepared_values VALUES(1,'first'),(2,'second')"); e != nil {
		t.Fatal(e)
	}
	var held *WorkingSetReadStatement
	e := f.authority.WithWorkingSetRead(ctx, f.main, func(r *WorkingSetReadSession) error {
		var e error
		held, e = r.PrepareQuery("SELECT value FROM main.prepared_values WHERE id=?")
		if e != nil {
			return e
		}
		if held.statement == nil || len(r.statements) != 1 {
			t.Fatal("no owned native statement")
		}
		for _, id := range []int{1, 2, 1} {
			rows, e := held.Query(id)
			if e != nil {
				return e
			}
			if !rows.Next() {
				t.Fatal("missing bound result", rows.Err())
			}
			var value string
			if e = rows.Scan(&value); e != nil {
				return e
			}
			want := "first"
			if id == 2 {
				want = "second"
			}
			if value != want {
				t.Fatalf("cached row %q want %q", value, want)
			}
			if e = rows.Close(); e != nil {
				return e
			}
		}
		for _, q := range []string{"DELETE FROM prepared_values RETURNING id", "CREATE TABLE forbidden(id)", "COMMIT", "PRAGMA writable_schema=ON", "ATTACH ':memory:' AS other"} {
			s, e := r.PrepareQuery(q)
			if e == nil {
				s.Close()
				t.Fatalf("prepared forbidden SQL %s", q)
			}
		}
		return nil
	})
	if e != nil {
		t.Fatal(e)
	}
	if !held.closed {
		t.Fatal("scope did not close statement")
	}
	if _, e = held.Query(1); !errors.Is(e, sql.ErrTxDone) {
		t.Fatal("statement escaped", e)
	}
	if e = held.Close(); e != nil {
		t.Fatal(e)
	}
	if _, e = f.main.Exec("UPDATE prepared_values SET value='changed' WHERE id=1"); e != nil {
		t.Fatal(e)
	}
	if e = f.authority.WithWorkingSetRead(ctx, f.main, func(r *WorkingSetReadSession) error {
		s, e := r.PrepareQuery("SELECT value FROM main.prepared_values WHERE id=1")
		if e != nil {
			return e
		}
		rows, e := s.Query()
		if e != nil {
			return e
		}
		defer rows.Close()
		if !rows.Next() {
			return rows.Err()
		}
		var v string
		if e = rows.Scan(&v); e != nil {
			return e
		}
		if v != "changed" {
			t.Fatal("old snapshot cached", v)
		}
		return nil
	}); e != nil {
		t.Fatal(e)
	}
}

func TestWorkingSetPreparedQueryNativeLossLatch(t *testing.T) {
	f := newWriterFixture(t)
	ctx := context.Background()
	var held *WorkingSetReadStatement
	e := f.authority.WithWorkingSetRead(ctx, f.main, func(r *WorkingSetReadSession) error {
		var e error
		held, e = r.PrepareQuery("SELECT json_extract(?, '$.value')")
		if e != nil {
			return e
		}
		// Engine fault injection only; the public statement cannot control transactions.
		if e = protectMutation(r.conn, false); e != nil {
			return e
		}
		if _, e = r.conn.ExecContext(ctx, "ROLLBACK"); e != nil {
			return e
		}
		if e = protectPreparation(r.conn); e != nil {
			return e
		}
		rows, e := held.Query("{")
		if e == nil {
			for rows.Next() {
			}
			e = rows.Err()
			rows.Close()
		}
		if !errors.Is(e, sql.ErrTxDone) {
			t.Fatalf("lost prepared query not latched %v", e)
		}
		if _, e = held.Query("{}"); !errors.Is(e, sql.ErrTxDone) {
			t.Fatal("caught loss revived", e)
		}
		return nil
	})
	if e == nil {
		t.Fatal("lost publication accepted")
	}
	if !held.closed {
		t.Fatal("lost statement not closed")
	}
}
