package commitments

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/killerprojecte/HumanEngine/backend/internal/recovery"
	"github.com/killerprojecte/HumanEngine/backend/internal/workingset"
	"github.com/mattn/go-sqlite3"
)

func TestWorkingSetBorrowedCurrentNativePublication(t *testing.T) {
	f, as, options := currentFixture(t)
	f.main.SetMaxOpenConns(1)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var held *ReadService
	var heldScope *workingset.Scope
	calls := 0
	w, e := workingset.New(ctx, f.main, f.ledger, workingset.Ports{
		Enabled: func() bool { return true },
		ResolveCurrent: func(scope *workingset.Scope, _ workingset.Context, _ []workingset.Reference) ([]workingset.Snapshot, error) {
			calls++
			heldScope = scope
			e := f.store.ReadWorkingSetCurrent(ctx, scope, as, currentEnabled, options, func(read *ReadService) error {
				held = read
				item, e := read.Current(Context{SceneID: "scene"}, Reference{ID: "commitment-1", Version: 1})
				if e == nil && (item == nil || get(item, "confirmation") != "confirmed") {
					t.Fatal("lost actual current authority")
				}
				return e
			})
			if e == nil {
				if _, expired := held.Current(Context{}, Reference{}); !errors.Is(expired, sql.ErrTxDone) {
					t.Fatalf("inner service escaped while owner active: %v", expired)
				}
				if alive := scope.CheckScope(); alive != nil {
					return nil, alive
				}
			}
			return []workingset.Snapshot{}, e
		},
	})
	must(t, e)
	selection := []workingset.Selection{{Ref: workingset.Reference{Kind: "assertion", ID: "root", Version: 1}, DependencyKey: strings.Repeat("a", 64)}}
	_, e = w.View(ctx, workingset.Context{SceneID: "scene"}, selection, true)
	must(t, e)
	if calls != 1 {
		t.Fatal("resolver not invoked", calls)
	}
	if _, e := held.Current(Context{}, Reference{}); !errors.Is(e, sql.ErrTxDone) {
		t.Fatal("escaped current", e)
	}
	if e := heldScope.CheckReadBinding(f.ledger, f.main); !errors.Is(e, sql.ErrTxDone) {
		t.Fatal("escaped owner", e)
	}
}

func TestWorkingSetBorrowedScopeRejectsWrongOwnerAndWrites(t *testing.T) {
	f, as, options := currentFixture(t)
	other, otherAssertions, _ := currentFixture(t)
	ctx := context.Background()
	checked := false
	w, e := workingset.New(ctx, f.main, f.ledger, workingset.Ports{
		Enabled: func() bool { return true },
		ObserveReady: func(scope *workingset.Scope) error {
			checked = true
			if e := other.store.ReadWorkingSetCurrent(ctx, scope, otherAssertions, currentEnabled, options, func(*ReadService) error { t.Fatal("wrong owner callback"); return nil }); e == nil {
				t.Fatal("wrong owner accepted")
			}
			return f.store.ReadWorkingSetCurrent(ctx, scope, as, currentEnabled, options, func(*ReadService) error {
				if p, e := scope.PrepareQuery("DELETE FROM memory_commitment_heads RETURNING id"); e == nil {
					p.Close()
					t.Fatal("native prepare accepted write")
				} else {
					var native sqlite3.Error
					if !errors.As(e, &native) || native.Code != sqlite3.ErrAuth {
						t.Fatalf("native prepare returned %v", e)
					}
				}
				rows, e := scope.Query("DELETE FROM memory_commitment_heads RETURNING id")
				if e == nil {
					rows.Next()
					e = rows.Err()
					rows.Close()
				}
				var native sqlite3.Error
				if !errors.As(e, &native) || native.Code != sqlite3.ErrAuth {
					t.Fatalf("expected native SQLITE_AUTH, got %v", e)
				}
				return nil
			})
		},
		ResolveCurrent: func(*workingset.Scope, workingset.Context, []workingset.Reference) ([]workingset.Snapshot, error) {
			return nil, nil
		},
	})
	must(t, e)
	_, e = w.View(ctx, workingset.Context{SceneID: "scene"}, nil, true)
	must(t, e)
	if !checked {
		t.Fatal("readiness scope not invoked")
	}
}

func TestWorkingSetBorrowedCurrentActiveChild(t *testing.T) {
	f, as, options := currentFixture(t)
	ctx := context.Background()
	var parentScope *workingset.Scope
	w, e := workingset.New(ctx, f.main, f.ledger, workingset.Ports{
		Enabled:      func() bool { return true },
		ObserveReady: func(scope *workingset.Scope) error { parentScope = scope; return nil },
		ResolveCurrent: func(*workingset.Scope, workingset.Context, []workingset.Reference) ([]workingset.Snapshot, error) {
			return nil, nil
		},
	})
	must(t, e)
	must(t, f.ledger.WithWriteSession(ctx, f.main, func(parent *recovery.WriteSession) error {
		_, e := w.WithSession(parent).View(ctx, workingset.Context{SceneID: "scene"}, nil, true)
		if e != nil {
			return e
		}
		return parent.WithSavepoint(func(child *recovery.WriteSession) error {
			if e := f.store.ReadWorkingSetCurrent(ctx, parentScope, as, currentEnabled, options, func(*ReadService) error { t.Fatal("suspended owner callback"); return nil }); e == nil {
				t.Fatal("suspended owner accepted")
			}
			_, e := w.WithSession(child).View(ctx, workingset.Context{SceneID: "scene"}, nil, true)
			if e != nil {
				return e
			}
			return f.store.ReadWorkingSetCurrent(ctx, parentScope, as, currentEnabled, options, func(read *ReadService) error {
				item, e := read.Current(Context{SceneID: "scene"}, Reference{ID: "commitment-1", Version: 1})
				if e == nil && item == nil {
					t.Fatal("child lost current")
				}
				if e != nil {
					return e
				}
				if _, e := child.Exec("UPDATE memories SET value=json_set(value,'$.status','invalidated') WHERE id='root'"); e != nil {
					return e
				}
				item, e = read.Current(Context{SceneID: "scene"}, Reference{ID: "commitment-1", Version: 1})
				if e == nil && len(arr(get(item, "evidence"))) != 0 {
					t.Fatal("child-local withdrawal not visible")
				}
				return e
			})
		})
	}))
}
