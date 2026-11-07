package assertions

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
)

func memorySnapshot(t *testing.T, db *sql.DB) string {
	t.Helper()
	rows, e := db.Query("SELECT name FROM sqlite_master WHERE type='table' AND (name LIKE 'memory_%' OR name='memories' OR name='vector_spaces') ORDER BY name")
	if e != nil {
		t.Fatal(e)
	}
	names := []string{}
	for rows.Next() {
		var name string
		if e = rows.Scan(&name); e != nil {
			t.Fatal(e)
		}
		names = append(names, name)
	}
	if e = errors.Join(rows.Err(), rows.Close()); e != nil {
		t.Fatal(e)
	}
	all := new(Object)
	for _, name := range names {
		rs, e := db.Query("SELECT * FROM " + name + " ORDER BY 1,2")
		if e != nil {
			t.Fatal(e)
		}
		columns, e := rs.Columns()
		if e != nil {
			t.Fatal(e)
		}
		array := []any{}
		for rs.Next() {
			values := make([]any, len(columns))
			dest := make([]any, len(values))
			for i := range values {
				dest[i] = &values[i]
			}
			if e = rs.Scan(dest...); e != nil {
				t.Fatal(e)
			}
			o := new(Object)
			for i, k := range columns {
				v := values[i]
				if n, ok := v.(int64); ok {
					v = float64(n)
				}
				_ = o.Set(k, v)
			}
			array = append(array, o)
		}
		if e = errors.Join(rs.Err(), rs.Close()); e != nil {
			t.Fatal(e)
		}
		_ = all.Set(name, array)
	}
	raw, e := jsjson.Stringify(all)
	if e != nil {
		t.Fatal(e)
	}
	return raw
}
func TestActualRepositoryCoordinatorTransitions(t *testing.T) {
	var oracle struct {
		Traces []struct {
			Name   string
			Retain bool
			Steps  []struct{ Op, Result, Error, Events, RowsHash string }
		}
	}
	raw, e := os.ReadFile("testdata/coordinator.json")
	if e != nil {
		t.Fatal(e)
	}
	if e = json.Unmarshal(raw, &oracle); e != nil {
		t.Fatal(e)
	}
	for _, trace := range oracle.Traces {
		t.Run(trace.Name, func(t *testing.T) {
			f := newFixture(t)
			events := []any{}
			fault := ""
			c, e := NewCoordinator(f.store, CoordinatorOptions{HistorySupports: func(s *Store, id string) ([]any, error) { return s.reader().Proofs(id) }, Retain: func(*Store, *Object) (bool, error) { return trace.Retain, nil }, RemoveProjection: func(_ *Store, id string) error { events = append(events, []any{"removeProjection", id}); return nil }, Committed: func(_ *Store, id string, version, epoch int64) error {
				events = append(events, []any{"committed", id, float64(version), float64(epoch)})
				if fault == "committed" {
					return errors.New("synthetic_committed")
				}
				return nil
			}, AfterCommit: func() error {
				events = append(events, []any{"afterCommit"})
				if fault == "afterCommit" {
					return errors.New("synthetic_afterCommit")
				}
				return nil
			}, BeforeCommitEligibility: func(s *Store, changes []EligibilityTransition) error {
				values := []any{}
				for _, change := range changes {
					before, e := EligibilityIncarnation(change.Before)
					if e != nil {
						return e
					}
					after, e := EligibilityIncarnation(change.After)
					if e != nil {
						return e
					}
					state := eligibilityState(change.After)
					var lineage any
					if state.LineageStatus != nil {
						lineage = *state.LineageStatus
					}
					var material any
					if !denied(state) {
						proofs, e := s.reader().Supports(str(get(change.After, "id")))
						if e != nil {
							return e
						}
						material, e = EligibilityMaterial(change.After, proofs)
						if e != nil {
							return e
						}
					}
					values = append(values, obj("before", before, "after", after, "state", obj("status", state.Status, "lineageStatus", lineage), "material", material))
				}
				events = append(events, []any{"eligibility", values})
				return s.RecordEligibilityChanges(changes)
			}})
			if e != nil {
				t.Fatal(e)
			}
			var actions func(*RevisionTxn, []any) error
			actions = func(tx *RevisionTxn, ops []any) error {
				for _, raw := range ops {
					op := raw.([]any)
					switch op[0] {
					case "raw":
						for _, statement := range strings.Split(op[1].(string), ";") {
							if strings.TrimSpace(statement) != "" {
								if e := tx.Store.exec(statement); e != nil {
									return e
								}
							}
						}
					case "write":
						if e := tx.Repository.Write(op[1].(*Object)); e != nil {
							return e
						}
					case "supports":
						if e := tx.Repository.SaveSupports(op[1].(string), op[2].([]any)); e != nil {
							return e
						}
					case "checkpoint":
						if e := tx.Checkpoint(); e != nil {
							return e
						}
					case "touch":
						if e := tx.Touch(op[1].(string)); e != nil {
							return e
						}
					case "throw":
						return errors.New(op[1].(string))
					case "fault":
						fault = op[1].(string)
					case "writeThenMutate":
						record := op[1].(*Object)
						if e := tx.Repository.Write(record); e != nil {
							return e
						}
						_ = record.Set("status", "active")
						_ = record.Set("lineageStatus", "verified")
					case "nested":
						e := tx.Transaction(context.Background(), func(child *RevisionTxn) error { return actions(child, op[1].([]any)) })
						if e != nil {
							if op[2] != true {
								return e
							}
							events = append(events, []any{"caught", e.Error()})
						}
					default:
						return fmt.Errorf("unknown action %v", op[0])
					}
				}
				return nil
			}
			for n, step := range trace.Steps {
				op := parse(t, step.Op).([]any)
				events = []any{}
				fault = ""
				var err error
				switch op[0] {
				case "seed":
					record := op[1].(*Object)
					fp, e := Fingerprint(record)
					if e != nil {
						t.Fatal(e)
					}
					recordRaw, _ := jsjson.Stringify(record)
					proofRaw, _ := jsjson.Stringify(op[2])
					_, err = f.main.Exec("INSERT INTO memories VALUES(?,?,?,?,?,?)", get(record, "id"), scope(record), get(get(record, "scope"), "personId"), get(record, "content"), fp, recordRaw)
					if err == nil {
						_, err = f.main.Exec("INSERT INTO memory_supports VALUES(?,?)", get(record, "id"), proofRaw)
					}
				case "outside":
					err = actions(c.bind(f.store, newTracking()), op[1].([]any))
				default:
					err = c.Transaction(context.Background(), func(tx *RevisionTxn) error { return actions(tx, op[1].([]any)) })
				}
				if (err != nil) != (step.Error != "") || (err != nil && err.Error() != step.Error) {
					t.Fatalf("step %d error=%v want=%s", n, err, step.Error)
				}
				actual, e := jsjson.Stringify(events)
				if e != nil {
					t.Fatal(e)
				}
				if actual != step.Events {
					t.Fatalf("step %d events=%s want=%s", n, actual, step.Events)
				}
				if hash := digest(memorySnapshot(t, f.main)); hash != step.RowsHash {
					t.Fatalf("step %d row hash=%s want=%s", n, hash, step.RowsHash)
				}
			}
		})
	}
}

func TestActualAssertionHashMaterial(t *testing.T) {
	var oracle struct {
		HashVectors []struct {
			Record, Proofs, Fingerprint, Incarnation, Material string
			Historical                                         json.RawMessage
			ProofKeys                                          []string
		}
	}
	raw, e := os.ReadFile("testdata/coordinator.json")
	if e != nil {
		t.Fatal(e)
	}
	if e = json.Unmarshal(raw, &oracle); e != nil {
		t.Fatal(e)
	}
	for i, v := range oracle.HashVectors {
		record := parse(t, v.Record).(*Object)
		proofs := parse(t, v.Proofs).([]any)
		checks := []struct {
			name, want string
			call       func() (string, error)
		}{{"fingerprint", v.Fingerprint, func() (string, error) { return Fingerprint(record) }}, {"historical", str(parse(t, string(v.Historical))), func() (string, error) { return HistoricalFingerprint(record) }}, {"incarnation", v.Incarnation, func() (string, error) { return EligibilityIncarnation(record) }}, {"material", v.Material, func() (string, error) { return EligibilityMaterial(record, proofs) }}}
		for _, c := range checks {
			got, e := c.call()
			if e != nil || got != c.want {
				t.Fatalf("vector %d %s=%s err=%v want=%s", i, c.name, got, e, c.want)
			}
		}
		for n, p := range proofs {
			got, e := CanonicalProofKey(p.(*Object))
			if e != nil || got != v.ProofKeys[n] {
				t.Fatalf("vector %d proof=%s err=%v want=%s", i, got, e, v.ProofKeys[n])
			}
		}
	}
}

func TestCoordinatorRequiresBoundHistoryProofProvider(t *testing.T) {
	f := newFixture(t)
	options := CoordinatorOptions{Retain: func(*Store, *Object) (bool, error) { return true, nil }, RemoveProjection: func(*Store, string) error { return nil }}
	if _, e := NewCoordinator(f.store, options); e == nil {
		t.Fatal("missing history authority accepted")
	}
	calls := 0
	options.HistorySupports = func(s *Store, id string) ([]any, error) {
		calls++
		var out []any
		e := s.Read(context.Background(), func(r *Reader) error {
			proofs, e := r.Proofs(id)
			if e != nil {
				return e
			}
			for _, p := range proofs {
				if get(p, "invalidatedBy") == nil {
					out = append(out, p)
				}
			}
			if out == nil {
				out = []any{}
			}
			return nil
		})
		return out, e
	}
	c, e := NewCoordinator(f.store, options)
	if e != nil {
		t.Fatal(e)
	}
	input := fixtureInput(t, -1)
	record := get(input, "record").(*Object)
	proofs := array(get(input, "supports"))
	_ = proofs[0].(*Object).Set("invalidatedBy", "recalled")
	if e = c.Transaction(context.Background(), func(tx *RevisionTxn) error {
		if e := tx.Repository.Write(record); e != nil {
			return e
		}
		return tx.Repository.SaveSupports("assertion", proofs)
	}); e != nil {
		t.Fatal(e)
	}
	if calls != 1 {
		t.Fatalf("history provider calls=%d", calls)
	}
	if e = f.store.Read(context.Background(), func(r *Reader) error {
		v, e := r.Revision("assertion", 1, "scene:scene")
		if e != nil {
			return e
		}
		if len(array(get(v, "supports"))) != 1 {
			t.Fatal("unfiltered history proof")
		}
		stored, e := r.Proofs("assertion")
		if e != nil {
			return e
		}
		if len(stored) != 2 {
			t.Fatal("history projection rewrote raw supports")
		}
		return nil
	}); e != nil {
		t.Fatal(e)
	}
}

func TestBorrowedWriterEnablesForeignKeysBeforeBegin(t *testing.T) {
	f := newFixture(t)
	f.main.SetMaxOpenConns(1)
	conn, e := f.main.Conn(context.Background())
	if e != nil {
		t.Fatal(e)
	}
	if _, e = conn.ExecContext(context.Background(), "PRAGMA foreign_keys=OFF"); e != nil {
		t.Fatal(e)
	}
	conn.Close()
	err := f.store.Transaction(context.Background(), func(s *Store) error { return s.exec("INSERT INTO memory_supports VALUES('missing','[]')") })
	if err == nil || !strings.Contains(err.Error(), "FOREIGN KEY constraint failed") {
		t.Fatalf("borrowed connection allowed orphan: %v", err)
	}
	var count int
	if e = f.main.QueryRow("SELECT COUNT(*) FROM memory_supports").Scan(&count); e != nil || count != 0 {
		t.Fatalf("orphan count=%d err=%v", count, e)
	}
}

func TestActualRepositoryRawJSONRoots(t *testing.T) {
	var oracle struct {
		RawReadCases []struct{ Input, Get, All, Supports string }
	}
	raw, e := os.ReadFile("testdata/coordinator.json")
	if e != nil {
		t.Fatal(e)
	}
	if e = json.Unmarshal(raw, &oracle); e != nil {
		t.Fatal(e)
	}
	if len(oracle.RawReadCases) == 0 {
		t.Fatal("raw read oracle empty")
	}
	for i, v := range oracle.RawReadCases {
		f := newFixture(t)
		if _, e = f.main.Exec("INSERT INTO memories VALUES('assertion','scene:scene','person','Synthetic','fingerprint',?)", v.Input); e != nil {
			t.Fatal(e)
		}
		if _, e = f.main.Exec("INSERT INTO memory_supports VALUES('assertion',?)", v.Input); e != nil {
			t.Fatal(e)
		}
		if e = f.store.Read(context.Background(), func(r *Reader) error {
			value, found, e := r.Get("assertion")
			if e != nil {
				return e
			}
			if !found {
				t.Fatal("existing JSON null conflated with missing row")
			}
			got, e := jsjson.Stringify(value)
			if e != nil {
				return e
			}
			if got != v.Get {
				t.Fatalf("case %d raw get=%s want=%s", i, got, v.Get)
			}
			all, e := r.All()
			if e != nil {
				return e
			}
			got, e = jsjson.Stringify(all)
			if e != nil {
				return e
			}
			if got != v.All {
				t.Fatalf("case %d raw all=%s want=%s", i, got, v.All)
			}
			supports, e := r.Supports("assertion")
			if e != nil {
				return e
			}
			got, e = jsjson.Stringify(supports)
			if e != nil {
				return e
			}
			if got != v.Supports {
				t.Fatalf("case %d raw supports=%s want=%s", i, got, v.Supports)
			}
			missing, found, e := r.Get("missing")
			if e != nil {
				return e
			}
			if found || missing != nil {
				t.Fatal("missing row reported present")
			}
			return nil
		}); e != nil {
			t.Fatal(e)
		}
	}
}

func TestCoordinatorCaughtNestedPanicRestoresTracking(t *testing.T) {
	f := newFixture(t)
	c, e := NewCoordinator(f.store, CoordinatorOptions{HistorySupports: func(s *Store, id string) ([]any, error) { return s.reader().Proofs(id) }, Retain: func(*Store, *Object) (bool, error) { return true, nil }, RemoveProjection: func(*Store, string) error { return nil }})
	if e != nil {
		t.Fatal(e)
	}
	record := get(fixtureInput(t, -1), "record").(*Object)
	if e = c.Transaction(context.Background(), func(tx *RevisionTxn) error {
		if e := tx.Repository.Write(record); e != nil {
			return e
		}
		func() {
			defer func() {
				if recover() != "synthetic_panic" {
					t.Fatal("nested panic did not propagate")
				}
			}()
			_ = tx.Transaction(context.Background(), func(child *RevisionTxn) error {
				changed := record.Clone()
				_ = changed.Set("status", "invalidated")
				if e := child.Repository.Write(changed); e != nil {
					t.Fatal(e)
				}
				if e := child.Checkpoint(); e != nil {
					t.Fatal(e)
				}
				panic("synthetic_panic")
			})
		}()
		return nil
	}); e != nil {
		t.Fatal(e)
	}
	if e = f.store.Read(context.Background(), func(r *Reader) error {
		value, _, e := r.Get("assertion")
		if e != nil {
			return e
		}
		if get(value, "status") != "active" {
			t.Fatal("nested SQL state escaped rollback")
		}
		history, e := r.Revisions("assertion", "scene:scene", nil)
		if e != nil {
			return e
		}
		if len(history) != 1 || get(get(history[0], "record"), "status") != "active" {
			t.Fatal("nested tracking escaped rollback")
		}
		return nil
	}); e != nil {
		t.Fatal(e)
	}
}
