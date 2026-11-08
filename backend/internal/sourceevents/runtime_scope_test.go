package sourceevents

import (
	"context"
	"database/sql"
	"errors"
	"github.com/killerprojecte/HumanEngine/backend/internal/botruntime"
	"github.com/killerprojecte/HumanEngine/backend/internal/recovery"
	"testing"
)

func TestRuntimeReadsSameWriterAndExpiredScopes(t *testing.T) {
	f, _ := runtimeFixture(t, Options{})
	other, _ := runtimeFixture(t, Options{})
	ctx := context.Background()
	var held botruntime.ReadAuthority
	var scope RuntimeReadScope
	e := f.authority.WithWriteSession(ctx, f.main, func(ws *recovery.WriteSession) error {
		if _, e := ws.Exec("INSERT INTO config VALUES('uncommitted','same-owner')"); e != nil {
			return e
		}
		if e := f.store.WithSession(ws).WithRuntimeReads(ctx, func(reads botruntime.ReadAuthority) error {
			held = reads
			if e := other.store.UseRuntimeReadScope(reads, func(RuntimeReadScope) error { return nil }); e == nil {
				t.Fatal("foreign owner accepted")
			}
			return f.store.UseRuntimeReadScope(reads, func(s RuntimeReadScope) error {
				scope = s
				if e := s.CheckBinding(f.main, f.authority); e != nil {
					return e
				}
				rows, e := s.Query("SELECT value FROM config WHERE key='uncommitted'")
				if e != nil {
					return e
				}
				defer rows.Close()
				if !rows.Next() {
					t.Fatal("missing same-writer uncommitted value")
				}
				var value string
				if e := rows.Scan(&value); e != nil {
					return e
				}
				if value != "same-owner" {
					t.Fatal(value)
				}
				return rows.Err()
			})
		}); e != nil {
			return e
		}
		if !errors.Is(held.Ready(), sql.ErrTxDone) {
			t.Fatal("read capability survived callback")
		}
		if !errors.Is(scope.Ready(), sql.ErrTxDone) {
			t.Fatal("query scope survived callback")
		}
		if _, e := scope.Query("SELECT 1"); !errors.Is(e, sql.ErrTxDone) {
			t.Fatal("expired query", e)
		}
		return ws.AssertReady()
	})
	if e != nil {
		t.Fatal(e)
	}
	if e = f.store.WithRuntimeReads(ctx, func(reads botruntime.ReadAuthority) error { held = reads; return reads.Ready() }); e != nil {
		t.Fatal(e)
	}
	if !errors.Is(held.Ready(), sql.ErrTxDone) {
		t.Fatal("publication capability survived callback")
	}
}
