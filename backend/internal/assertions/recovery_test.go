package assertions

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/recovery"
)

func TestActualAssertionDualDatabaseFaults(t *testing.T) {
	var oracle struct {
		Cases []struct {
			Stage                                          string
			Error, ReadyError                              *string
			GateSequence, MinimumReader, AuthoritySequence int64
			Record, Events                                 string
			State                                          struct {
				Version       int64
				DeletionEpoch int64 `json:"deletion_epoch"`
				Deleted       int
			}
		}
	}
	raw, e := os.ReadFile("testdata/recovery.json")
	if e != nil {
		t.Fatal(e)
	}
	if e = json.Unmarshal(raw, &oracle); e != nil {
		t.Fatal(e)
	}
	for _, test := range oracle.Cases {
		t.Run(test.Stage, func(t *testing.T) {
			f := newFixture(t)
			armed := false
			record := parse(t, `{"id":"assertion","scope":{"kind":"scene","sceneId":"scene"},"content":"Synthetic authority fixture","kind":"claim","sources":[],"importance":"medium","confidence":0.6,"learnedAt":"2026-01-02T00:00:00.000Z","status":"active","locked":false,"origin":"manual","lineageStatus":"verified"}`).(*Object)
			c, e := NewCoordinator(f.store, CoordinatorOptions{HistorySupports: func(s *Store, id string) ([]any, error) { return s.reader().Proofs(id) }, Retain: func(*Store, *Object) (bool, error) { return true, nil }, RemoveProjection: func(*Store, string) error { return nil }, AfterCommit: func() error {
				if armed && test.Stage == "after-commit" {
					return errors.New("synthetic_after_commit")
				}
				return nil
			}, BeforeCommitEligibility: func(s *Store, changes []EligibilityTransition) error {
				if e := s.RecordEligibilityChanges(changes); e != nil {
					return e
				}
				if armed && test.Stage == "after-intent" {
					return errors.New("synthetic_after_intent")
				}
				return nil
			}})
			if e != nil {
				t.Fatal(e)
			}
			if e = c.Transaction(context.Background(), func(tx *RevisionTxn) error {
				if e := tx.Repository.Write(record); e != nil {
					return e
				}
				return tx.Repository.SaveSupports("assertion", []any{})
			}); e != nil {
				t.Fatal(e)
			}
			armed = true
			err := c.Transaction(context.Background(), func(tx *RevisionTxn) error {
				updated := record.Clone()
				_ = updated.Set("status", "invalidated")
				if test.Stage == "nested-caught" {
					e := tx.Transaction(context.Background(), func(child *RevisionTxn) error {
						if e := child.Repository.Write(updated); e != nil {
							return e
						}
						if e := child.Checkpoint(); e != nil {
							return e
						}
						return errors.New("nested_rollback")
					})
					if e == nil || e.Error() != "nested_rollback" {
						t.Fatalf("nested result=%v", e)
					}
					outer := record.Clone()
					_ = outer.Set("content", "accepted outer")
					return tx.Repository.Write(outer)
				}
				if test.Stage == "incarnation" {
					_ = updated.Set("learnedAt", "2026-02-01T00:00:00.000Z")
				}
				return tx.Repository.Write(updated)
			})
			if (err == nil) != (test.Error == nil) || (err != nil && err.Error() != *test.Error) {
				t.Fatalf("error=%v want=%v", err, test.Error)
			}
			var gateRaw, recordRaw string
			if e = f.main.QueryRow("SELECT value FROM recovery_gate WHERE id=1").Scan(&gateRaw); e != nil {
				t.Fatal(e)
			}
			var gate recovery.Gate
			if e = json.Unmarshal([]byte(gateRaw), &gate); e != nil {
				t.Fatal(e)
			}
			head, e := f.authority.Head(context.Background())
			if e != nil {
				t.Fatal(e)
			}
			if gate.Head.Sequence != test.GateSequence || gate.MinimumReader != test.MinimumReader || head.Sequence != test.AuthoritySequence {
				t.Fatalf("gate/head mismatch: gate=%+v head=%+v", gate, head)
			}
			if e = f.main.QueryRow("SELECT value FROM memories WHERE id='assertion'").Scan(&recordRaw); e != nil {
				t.Fatal(e)
			}
			if recordRaw != test.Record {
				t.Fatalf("record=%s want=%s", recordRaw, test.Record)
			}
			var version, epoch int64
			var deleted int
			if e = f.main.QueryRow("SELECT version,deletion_epoch,deleted FROM memory_assertion_state WHERE assertion_id='assertion'").Scan(&version, &epoch, &deleted); e != nil {
				t.Fatal(e)
			}
			if version != test.State.Version || epoch != test.State.DeletionEpoch || deleted != test.State.Deleted {
				t.Fatalf("state=%d/%d/%d", version, epoch, deleted)
			}
			readyErr := f.store.Read(context.Background(), func(*Reader) error { return nil })
			if (readyErr == nil) != (test.ReadyError == nil) || (readyErr != nil && readyErr.Error() != *test.ReadyError) {
				t.Fatalf("ready error=%v want=%v", readyErr, test.ReadyError)
			}
			db, e := sql.Open("sqlite3", filepath.Join(gate.Authority, "authority.sqlite"))
			if e != nil {
				t.Fatal(e)
			}
			defer db.Close()
			rows, e := db.Query("SELECT payload FROM revocations ORDER BY sequence")
			if e != nil {
				t.Fatal(e)
			}
			events := []any{}
			for rows.Next() {
				var raw string
				if e = rows.Scan(&raw); e != nil {
					t.Fatal(e)
				}
				v := parse(t, raw).(*Object)
				v.Delete("operationId")
				events = append(events, v)
			}
			if e = errors.Join(rows.Err(), rows.Close()); e != nil {
				t.Fatal(e)
			}
			actual, e := jsjson.Stringify(events)
			if e != nil {
				t.Fatal(e)
			}
			if actual != test.Events {
				t.Fatalf("events=%s want=%s", actual, test.Events)
			}
		})
	}
}
