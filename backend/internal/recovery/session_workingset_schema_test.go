package recovery

import (
	"context"
	"database/sql"
	"errors"
	"testing"
)

func TestWorkingSetPublicationNativeLossIsPermanent(t *testing.T) {
	f := newWriterFixture(t)
	ctx := context.Background()
	var held *WorkingSetReadSession
	e := f.authority.WithWorkingSetRead(ctx, f.main, func(r *WorkingSetReadSession) error {
		held = r
		// Fault injection only: caller capabilities cannot issue transaction control.
		// Emulate SQLite ending the snapshot after an engine/storage fault.
		if e := protectMutation(r.conn, false); e != nil {
			return e
		}
		if _, e := r.conn.ExecContext(ctx, "ROLLBACK"); e != nil {
			return e
		}
		if e := protectPreparation(r.conn); e != nil {
			return e
		}
		if _, e := r.Query("SELECT value FROM main.missing_after_loss"); !errors.Is(e, sql.ErrTxDone) {
			t.Fatalf("autocommit read after snapshot loss: %v", e)
		}
		// Even if something outside the capability starts another transaction, it
		// must not revive this authority-verified snapshot.
		if e := protectMutation(r.conn, false); e != nil {
			return e
		}
		if _, e := r.conn.ExecContext(ctx, "BEGIN"); e != nil {
			return e
		}
		if e := protectPreparation(r.conn); e != nil {
			return e
		}
		if e := r.CheckScope(); !errors.Is(e, sql.ErrTxDone) {
			t.Fatal(e)
		}
		return nil // Catching the operation error must not make publication succeed.
	})
	if e == nil {
		t.Fatal("lost publication succeeded")
	}
	if e = held.CheckScope(); !errors.Is(e, sql.ErrTxDone) {
		t.Fatal(e)
	}
	if _, e = f.main.Exec("INSERT INTO data VALUES('after')"); e != nil {
		t.Fatal("authorizer not restored", e)
	}
}
func TestWorkingSetAuditMainSchemaAndRestoredProtection(t *testing.T) {
	f := newWriterFixture(t)
	ctx := context.Background()
	if e := f.authority.WithWriteSession(ctx, f.main, func(s *WriteSession) error { return s.EnsureWorkingSetSchema() }); e != nil {
		t.Fatal(e)
	}
	if _, e := f.main.Exec("CREATE TEMP TABLE memory_working_set_pins(wrong);ATTACH ':memory:' AS elsewhere;CREATE TABLE elsewhere.memory_working_set_pins(wrong)"); e != nil {
		t.Fatal(e)
	}
	if e := f.authority.WithWorkingSetRead(ctx, f.main, func(r *WorkingSetReadSession) error {
		yes, e := r.SchemaSupported()
		if e != nil || !yes {
			t.Fatal("audited temp shadow instead of main", yes, e)
		}
		for _, q := range []string{"PRAGMA main.table_info(memory_working_set_pins)", "PRAGMA temp.table_info(memory_working_set_pins)", "PRAGMA elsewhere.table_info(memory_working_set_pins)", "INSERT INTO main.data VALUES('escaped') RETURNING value"} {
			rows, e := r.Query(q)
			if e == nil {
				rows.Close()
				t.Fatal("schema window escaped", q)
			}
		}
		return nil
	}); e != nil {
		t.Fatal(e)
	}
}

func TestWorkingSetPublicationDelayedRowsErrorLatchesLoss(t *testing.T) {
	f := newWriterFixture(t)
	ctx := context.Background()
	e := f.authority.WithWorkingSetRead(ctx, f.main, func(r *WorkingSetReadSession) error {
		rows, e := r.Query("SELECT value FROM main.data UNION ALL SELECT json_extract('broken','$')")
		if e != nil {
			return e
		}
		if !rows.Next() {
			t.Fatal("missing first row", rows.Err())
		}
		var value string
		if e = rows.Scan(&value); e != nil {
			return e
		}
		// Simulate snapshot loss before a delayed SQLite stepping failure. Public
		// scope methods cannot issue this raw rollback; this is an engine-fault test.
		if e = protectMutation(r.conn, false); e != nil {
			return e
		}
		if _, e = r.conn.ExecContext(ctx, "ROLLBACK"); e != nil {
			return e
		}
		if e = protectPreparation(r.conn); e != nil {
			return e
		}
		if rows.Next() {
			t.Fatal("malformed JSON should fail during step")
		}
		// Do not call rows.Err before checking the owner: Next(false) itself must
		// report the delayed failure, so omitting Err cannot bypass the fatal latch.
		if e = r.CheckScope(); !errors.Is(e, sql.ErrTxDone) {
			t.Fatal("delayed error did not latch", e)
		}
		if _, e = r.Query("SELECT value FROM main.data"); !errors.Is(e, sql.ErrTxDone) {
			t.Fatal("new autocommit snapshot", e)
		}
		return nil
	})
	if e == nil {
		t.Fatal("caught delayed error made publication succeed")
	}
}
