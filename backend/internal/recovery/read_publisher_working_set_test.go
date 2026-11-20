package recovery

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"
)

func TestReadPublisherWorkingSetSnapshotLifetime(t *testing.T) {
	f := newWriterFixture(t)
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	publisher, err := NewReadPublisher(ctx, f.main, f.authority)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := publisher.CloseAndWait(context.Background()); err != nil {
			t.Error(err)
		}
	})
	writer, err := f.main.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	if _, err = writer.ExecContext(ctx, "BEGIN IMMEDIATE; UPDATE data SET value='uncommitted'"); err != nil {
		t.Fatal(err)
	}
	var held *WorkingSetReadSession
	var statement *WorkingSetReadStatement
	var rows *WorkingSetReadRows
	var emitted string
	err = publisher.PublishWorkingSet(ctx, func(scope *WorkingSetReadSession) ([]byte, error) {
		held = scope
		if _, err := scope.Query("UPDATE main.data SET value='not read only' RETURNING value"); err == nil {
			t.Fatal("publisher loan admitted a write")
		}
		statement, err = scope.PrepareQuery("SELECT value FROM main.data")
		if err != nil {
			return nil, err
		}
		rows, err = statement.Query()
		if err != nil {
			return nil, err
		}
		var value string
		if !rows.Next() {
			t.Fatal(rows.Err())
		}
		err = rows.Scan(&value)
		return []byte(value), err
	}, func(body []byte) error {
		if !errors.Is(held.CheckScope(), sql.ErrTxDone) || !errors.Is(rows.Err(), sql.ErrTxDone) {
			t.Fatal("projection capability survived into the publication callback")
		}
		if _, err := statement.Query(); !errors.Is(err, sql.ErrTxDone) {
			t.Fatalf("native statement outlived projection: %v", err)
		}
		emitted = string(body)
		return nil
	})
	if err != nil || emitted != "original" {
		t.Fatalf("independent working-set publication: %q %v", emitted, err)
	}
	if _, err = writer.ExecContext(ctx, "ROLLBACK"); err != nil {
		t.Fatal(err)
	}
	if _, err = writer.ExecContext(ctx, "UPDATE recovery_gate SET value=json_set(value,'$.state','quarantine')"); err != nil {
		t.Fatal(err)
	}
	called := false
	err = publisher.PublishWorkingSet(ctx, func(*WorkingSetReadSession) ([]byte, error) {
		called = true
		return nil, nil
	}, func([]byte) error {
		called = true
		return nil
	})
	if err == nil || called {
		t.Fatalf("quarantined scope reached projection or output: %v", err)
	}
}
