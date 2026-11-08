package workingset

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"
)

// Keep the real enclosing publication/write child alive after the inner loan
// exits. Without capability wrappers, retained native statements and rows would
// still be usable here because their native owner has not expired.
func TestReadScopeCapabilitiesExpireInsideActiveOwner(t *testing.T) {
	for _, write := range []bool{false, true} {
		name := "publication"
		if write {
			name = "active-write-child"
		}
		t.Run(name, func(t *testing.T) {
			f := setup(t)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			run := f.service.Read
			if write {
				run = f.service.Transaction
			}
			must(t, run(ctx, func(owner *Service) error {
				var held *Scope
				var prepared PreparedRead
				var directRows, preparedRows Rows
				e := owner.ReadScope(ctx, func(scope *Scope) error {
					held = scope
					var e error
					directRows, e = scope.Query("SELECT 11 UNION ALL SELECT 12")
					if e != nil {
						return e
					}
					prepared, e = scope.PrepareQuery("SELECT 21 UNION ALL SELECT 22")
					if e != nil {
						return e
					}
					preparedRows, e = prepared.Query()
					if e != nil {
						return e
					}
					for _, rows := range []Rows{directRows, preparedRows} {
						if !rows.Next() {
							t.Fatal("native read unavailable in live loan", rows.Err())
						}
						var n int
						if e = rows.Scan(&n); e != nil {
							return e
						}
						if e = rows.Err(); e != nil {
							return e
						}
					}
					return nil
				})
				if e != nil {
					return e
				}
				if !errors.Is(held.CheckScope(), sql.ErrTxDone) {
					t.Fatal("loan did not expire")
				}
				if rows, e := prepared.Query(); !errors.Is(e, sql.ErrTxDone) {
					if rows != nil {
						_ = rows.Close()
					}
					t.Fatal("prepared query escaped loan", e)
				}
				for _, rows := range []Rows{directRows, preparedRows} {
					// Test Scan first, while the retained native cursor is still on a row.
					var n int
					if e := rows.Scan(&n); !errors.Is(e, sql.ErrTxDone) {
						t.Fatal("scan escaped loan", e)
					}
					if rows.Next() {
						t.Fatal("next escaped loan")
					}
					if e := rows.Err(); !errors.Is(e, sql.ErrTxDone) {
						t.Fatal("rows error did not report expired loan", e)
					}
					if e := rows.Close(); e != nil {
						return e
					}
				}
				if e := prepared.Close(); e != nil {
					return e
				}
				// Loan expiry and cleanup must not expire the enclosing native owner.
				return owner.ReadScope(ctx, func(scope *Scope) error {
					rows, e := scope.Query("SELECT 31")
					if e != nil {
						return e
					}
					defer rows.Close()
					if !rows.Next() {
						t.Fatal("enclosing owner expired", rows.Err())
					}
					var n int
					if e = rows.Scan(&n); e != nil {
						return e
					}
					if n != 31 {
						t.Fatal("wrong native row")
					}
					return rows.Err()
				})
			}))
		})
	}
}

func TestReadScopeCapabilitiesRespectNativeChildSuspension(t *testing.T) {
	f := setup(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	must(t, f.service.Transaction(ctx, func(parent *Service) error {
		return parent.ReadScope(ctx, func(scope *Scope) error {
			statement, e := scope.PrepareQuery("SELECT 41 UNION ALL SELECT 42")
			if e != nil {
				return e
			}
			defer statement.Close()
			rows, e := statement.Query()
			if e != nil {
				return e
			}
			defer rows.Close()
			if !rows.Next() {
				t.Fatal("missing parent row", rows.Err())
			}
			e = parent.Transaction(ctx, func(child *Service) error {
				if got, e := statement.Query(); e == nil {
					if got != nil {
						_ = got.Close()
					}
					t.Fatal("suspended parent prepared query accepted")
				}
				var n int
				if rows.Scan(&n) == nil {
					t.Fatal("suspended parent scan accepted")
				}
				if rows.Next() {
					t.Fatal("suspended parent next accepted")
				}
				if rows.Err() == nil {
					t.Fatal("missing suspension error")
				}
				return child.ReadScope(ctx, func(childScope *Scope) error {
					own, e := childScope.Query("SELECT 51")
					if e != nil {
						return e
					}
					defer own.Close()
					if !own.Next() {
						t.Fatal("child cannot read", own.Err())
					}
					return own.Err()
				})
			})
			if e != nil {
				return e
			}
			// The wrapper must not permanently poison a temporarily suspended owner.
			var n int
			if e = rows.Scan(&n); e != nil {
				return e
			}
			if n != 41 {
				t.Fatal("lost parent cursor")
			}
			if !rows.Next() {
				t.Fatal("parent did not resume", rows.Err())
			}
			if e = rows.Scan(&n); e != nil {
				return e
			}
			if n != 42 {
				t.Fatal("wrong resumed row")
			}
			return rows.Err()
		})
	}))
}
