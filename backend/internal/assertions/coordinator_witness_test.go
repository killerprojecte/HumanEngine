package assertions

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"github.com/killerprojecte/HumanEngine/backend/internal/recovery"
)

func TestCoordinatorReadWitnessFinalFlushAndPostCommitFailure(t *testing.T) {
	for _, failAfter := range []bool{false, true} {
		t.Run(map[bool]string{false: "final-flush", true: "after-commit-failure"}[failAfter], func(t *testing.T) {
			f := newFixture(t)
			after, writes := 0, 0
			failure := errors.New("synthetic_after_commit_failure")
			c, err := NewCoordinator(f.store, CoordinatorOptions{
				Retain:           func(*Store, *Object) (bool, error) { return true, nil },
				HistorySupports:  func(*Store, string) ([]any, error) { return []any{}, nil },
				RemoveProjection: func(*Store, string) error { return nil },
				BeforeCommitEligibility: func(st *Store, _ []EligibilityTransition) error {
					return st.Read(context.Background(), func(r *Reader) error { _, e := r.State("eligibility-only"); return e })
				},
				AfterCommit: func() error {
					after++
					if failAfter {
						return failure
					}
					return nil
				},
			})
			if err != nil {
				t.Fatal(err)
			}
			record := get(fixtureInput(t, -1), "record").(*Object)
			witness, err := c.TransactionWithReadWitness(context.Background(), func(tx *RevisionTxn) error { writes++; return tx.Repository.Write(record) })
			if failAfter && !errors.Is(err, failure) || !failAfter && err != nil || after != 1 || writes != 1 {
				t.Fatal("commit boundary", writes, after, err)
			}
			var version int
			if err = f.main.QueryRow("SELECT version FROM memory_assertion_state WHERE assertion_id='assertion'").Scan(&version); err != nil || version != 1 {
				t.Fatal("post-commit failure rolled back", version, err)
			}
			if failAfter {
				return
			}
			verify := func() error {
				return f.authority.Publish(context.Background(), f.main, func(conn *sql.Conn) ([]byte, error) { return nil, witness.Verify(context.Background(), conn) }, func([]byte) error { return nil })
			}
			if err = verify(); err != nil {
				t.Fatal("witness captured before flush", err)
			}
			if _, err = f.main.Exec("INSERT INTO memory_assertion_state(assertion_id,version,deletion_epoch,deleted) VALUES('eligibility-only',1,0,0)"); err != nil {
				t.Fatal(err)
			}
			if err = verify(); !errors.Is(err, recovery.ErrReadWitnessChanged) {
				t.Fatal("final eligibility read not captured", err)
			}
		})
	}
}
