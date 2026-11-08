package assertions

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"github.com/killerprojecte/HumanEngine/backend/internal/recovery"
)

func TestCoordinatorBoundSessionDefersPostCommitToRootOwner(t *testing.T) {
	f := newFixture(t)
	after := 0
	c, e := NewCoordinator(f.store, CoordinatorOptions{
		Retain:           func(*Store, *Object) (bool, error) { return false, nil },
		HistorySupports:  func(*Store, string) ([]any, error) { return nil, nil },
		RemoveProjection: func(*Store, string) error { return nil },
		AfterCommit:      func() error { after++; return nil },
	})
	if e != nil {
		t.Fatal(e)
	}
	var held *Coordinator
	wanted := errors.New("synthetic_root_failure")
	e = f.authority.WithWriteSession(context.Background(), f.main, func(s *recovery.WriteSession) error {
		held = c.WithSession(s)
		if e := held.Transaction(context.Background(), func(tx *RevisionTxn) error { return tx.check() }); e != nil {
			return e
		}
		if after != 0 {
			t.Fatal("post-commit ran inside root writer")
		}
		return wanted
	})
	if !errors.Is(e, wanted) || after != 0 {
		t.Fatal(e, after)
	}
	if e = held.Transaction(context.Background(), func(*RevisionTxn) error { t.Fatal("expired callback executed"); return nil }); !errors.Is(e, sql.ErrTxDone) {
		t.Fatal(e)
	}
	if e = c.Transaction(context.Background(), func(*RevisionTxn) error { return nil }); e != nil || after != 1 {
		t.Fatal("original coordinator changed", e, after)
	}
}

func TestCoordinatorBoundSessionRejectsOtherOwner(t *testing.T) {
	f, other := newFixture(t), newFixture(t)
	c, e := NewCoordinator(f.store, CoordinatorOptions{
		Retain:           func(*Store, *Object) (bool, error) { return false, nil },
		HistorySupports:  func(*Store, string) ([]any, error) { return nil, nil },
		RemoveProjection: func(*Store, string) error { return nil },
	})
	if e != nil {
		t.Fatal(e)
	}
	e = other.authority.WithWriteSession(context.Background(), other.main, func(s *recovery.WriteSession) error {
		return c.WithSession(s).Transaction(context.Background(), func(*RevisionTxn) error { t.Fatal("foreign owner callback executed"); return nil })
	})
	if e == nil {
		t.Fatal("foreign owner accepted")
	}
}
