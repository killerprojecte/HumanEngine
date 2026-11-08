package assertionproposal

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/killerprojecte/HumanEngine/backend/internal/assertions"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/recovery"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
)

type sourceSnapshot map[string][]map[string]any
type sourceStep struct {
	Operation, Label, Error, ID                  string
	DeferredCommit                               bool
	Context, Candidate, Result, Current, History json.RawMessage
	Before, After                                sourceSnapshot
}
type sourceScenario struct {
	Name          string
	SourceSchemas []struct{ Name, SQL string }
	Traces        []sourceStep
}
type sourceOracle struct {
	Runtime     json.RawMessage
	RuntimeHash string
	Sources     map[string]string
	Cases       []sourceScenario
}

var comparedTables = []string{"scenes", "recall_tombstones", "memories", "memory_sources", "memory_supports", "memory_vectors", "memory_deleted", "memory_assertion_state", "memory_assertion_revisions", "memory_assertion_revision_sources", "memory_assertion_revision_parents", "source_event_heads", "source_event_revisions", "source_event_edit_keys", "source_event_tombstones"}

func loadPinnedSource(t *testing.T) sourceOracle {
	t.Helper()
	raw, e := os.ReadFile("testdata/proposal-source.json")
	if e != nil {
		t.Fatal(e)
	}
	var data sourceOracle
	if e = json.Unmarshal(raw, &data); e != nil {
		t.Fatal(e)
	}
	const runtimeHash = "b5702f593fcc596fff0f5163b361c56ace9917ff87efe76558370e3270bf4f4d"
	var compact bytes.Buffer
	if e = json.Compact(&compact, data.Runtime); e != nil {
		t.Fatal(e)
	}
	if data.RuntimeHash != runtimeHash || fmt.Sprintf("%x", sha256.Sum256(compact.Bytes())) != runtimeHash {
		t.Fatal("source runtime pin mismatch")
	}
	required := []string{"src/memory/write/proposal-command.ts", "src/memory/write/source-admission.ts", "src/memory/write/reconciliation-policy.ts", "src/memory/domain/record-values.ts", "src/memory/storage/assertion-repository.ts", "src/memory/write/revision-coordinator.ts", "src/memory.ts", "src/store.ts", "src/memory/read/assertion-authority.ts", "src/memory/lifecycle/proof-invalidation.ts", "src/memory/storage/event-revisions.ts", "src/memory/domain/proofs.ts", "src/memory/storage/assertion-history.ts", "src/memory/projections/coordinator.ts", "src/memory/projections/processor.ts", "src/memory/projections/people.ts", "src/memory/storage/projection-outbox.ts", "src/memory/lifecycle/assertion-lifecycle.ts", "src/memory/lifecycle/knowledge-privacy.ts", "src/memory/lifecycle/semantic-privacy.ts", "src/memory/lifecycle/commitment-privacy.ts", "package-lock.json", "backend/tests/generate-assertion-proposal-fixtures.mjs"}
	if len(data.Sources) != len(required) {
		t.Fatal("source provenance set differs")
	}
	for _, p := range required {
		b, e := os.ReadFile(filepath.Join("..", "..", "..", filepath.FromSlash(p)))
		if e != nil {
			t.Fatal(e)
		}
		if fmt.Sprintf("%x", sha256.Sum256(b)) != data.Sources[p] {
			t.Fatalf("source/generator/lock drift: %s", p)
		}
	}
	if len(data.Cases) != 9 {
		t.Fatalf("source scenarios: %d", len(data.Cases))
	}
	return data
}
func seedPrestate(t *testing.T, f scopedFixture, c sourceScenario, step sourceStep) {
	t.Helper()
	for _, schema := range c.SourceSchemas {
		if !strings.HasPrefix(schema.Name, "source_event_") {
			t.Fatal("unexpected source schema")
		}
		if _, e := f.main.Exec(schema.SQL); e != nil {
			t.Fatal(e)
		}
	}
	// Foreign owners' lock/delete/recall results are recorded setup, not a native
	// facade implementation. Only this command/history operation is under test.
	for i := len(comparedTables) - 1; i >= 0; i-- {
		table := comparedTables[i]
		if _, present := step.Before[table]; !present {
			continue
		}
		if _, e := f.main.Exec("DELETE FROM " + table); e != nil {
			t.Fatal(e)
		}
	}
	for _, table := range comparedTables {
		for _, row := range step.Before[table] {
			keys := make([]string, 0, len(row))
			for k := range row {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			args := make([]any, len(keys))
			marks := make([]string, len(keys))
			for i, k := range keys {
				args[i] = row[k]
				marks[i] = "?"
			}
			if _, e := f.main.Exec("INSERT INTO "+table+"("+strings.Join(keys, ",")+") VALUES("+strings.Join(marks, ",")+")", args...); e != nil {
				t.Fatalf("seed %s: %v", table, e)
			}
		}
	}
}

type snapshotRows interface {
	Next() bool
	Scan(...any) error
	Columns() ([]string, error)
	Err() error
	Close() error
}

func parseProofArray(raw string) ([]any, error) {
	v, e := jsjson.Parse(raw)
	if e != nil {
		return nil, e
	}
	a, ok := v.([]any)
	if !ok {
		return nil, errors.New("proof_array_required")
	}
	return a, nil
}
func querySnapshot(t *testing.T, query func(string, ...any) (snapshotRows, error), wanted sourceSnapshot) sourceSnapshot {
	t.Helper()
	out := sourceSnapshot{}
	for _, table := range comparedTables {
		if _, ok := wanted[table]; !ok {
			continue
		}
		rows, e := query("SELECT * FROM " + table)
		if e != nil {
			t.Fatal(e)
		}
		cols, e := rows.Columns()
		if e != nil {
			t.Fatal(e)
		}
		values := []map[string]any{}
		for rows.Next() {
			cells := make([]any, len(cols))
			ptrs := make([]any, len(cols))
			for i := range cells {
				ptrs[i] = &cells[i]
			}
			if e = rows.Scan(ptrs...); e != nil {
				t.Fatal(e)
			}
			r := map[string]any{}
			for i, k := range cols {
				switch v := cells[i].(type) {
				case int64:
					r[k] = float64(v)
				case []byte:
					r[k] = string(v)
				default:
					r[k] = v
				}
			}
			values = append(values, r)
		}
		if e = errors.Join(rows.Err(), rows.Close()); e != nil {
			t.Fatal(e)
		}
		out[table] = values
	}
	return out
}
func compareSnapshot(t *testing.T, got, want sourceSnapshot) {
	t.Helper()
	for _, table := range comparedTables {
		expected, present := want[table]
		if !present {
			continue
		}
		encode := func(rows []map[string]any) []string {
			out := []string{}
			for _, row := range rows {
				b, e := json.Marshal(row)
				if e != nil {
					t.Fatal(e)
				}
				out = append(out, string(b))
			}
			sort.Strings(out)
			return out
		}
		if a, b := encode(got[table]), encode(expected); !reflect.DeepEqual(a, b) {
			t.Fatalf("%s state mismatch\ngot %v\nwant %v", table, a, b)
		}
	}
}
func TestEverySourceProposalAndHistoryAgainstRecordedPrestate(t *testing.T) {
	data := loadPinnedSource(t)
	proposals, histories, boundaries := 0, 0, 0
	for _, scenario := range data.Cases {
		for _, step := range scenario.Traces {
			switch step.Operation {
			case "proposal":
				proposals++
			case "correctionHistory":
				histories++
			case "outerCommit", "recallBoundary":
				boundaries++
				continue
			default:
				t.Fatalf("unknown operation %s", step.Operation)
			}
			t.Run(scenario.Name+"/"+step.Label, func(t *testing.T) {
				f := scopedSetup(t)
				seedPrestate(t, f, scenario, step)
				if step.Operation == "correctionHistory" {
					e := f.store.Read(context.Background(), func(r *assertions.Reader) error {
						got, e := correctionHistory(r, step.ID, parseObject(t, string(step.Current)))
						if e != nil {
							return e
						}
						var want any
						if e = json.Unmarshal(step.History, &want); e != nil {
							return e
						}
						if !reflect.DeepEqual(comparableJSON(t, got), want) {
							t.Fatalf("history mismatch: got %v want %v", got, want)
						}
						return nil
					})
					if e != nil {
						t.Fatal(e)
					}
					compareSnapshot(t, querySnapshot(t, func(q string, a ...any) (snapshotRows, error) { return f.main.Query(q, a...) }, step.Before), step.Before)
					return
				}
				var expected struct{ Memory struct{ ID string } }
				_ = json.Unmarshal(step.Result, &expected)
				command, coordinator := testCommand(t, f, func() (string, error) {
					if expected.Memory.ID != "" {
						return expected.Memory.ID, nil
					}
					return "synthetic-failed-id", nil
				})
				if step.Error != "" {
					if step.Error != "synthetic_second_write_failure" {
						t.Fatalf("unknown injection %s", step.Error)
					}
					if _, e := f.main.Exec(`CREATE TRIGGER source_fault BEFORE INSERT ON memories WHEN NEW.content='Synthetic fail' BEGIN SELECT RAISE(ABORT,'synthetic_second_write_failure'); END`); e != nil {
						t.Fatal(e)
					}
				}
				// Uncommitted new records have no revision state. Recreate their genuine
				// pending writes through Repository so nested BeforeWrite matches the source.
				pending := []map[string]any{}
				if step.DeferredCommit {
					states := map[string]bool{}
					for _, row := range step.Before["memory_assertion_state"] {
						states[row["assertion_id"].(string)] = true
					}
					for _, row := range step.Before["memories"] {
						id := row["id"].(string)
						if !states[id] {
							pending = append(pending, row)
							if _, e := f.main.Exec("DELETE FROM memories WHERE id=?", id); e != nil {
								t.Fatal(e)
							}
						}
					}
				}
				stop := errors.New("stop_after_deferred_source_observation")
				var result Result
				e := coordinator.Transaction(context.Background(), func(tx *assertions.RevisionTxn) error {
					for _, row := range pending {
						record := parseObject(t, row["value"].(string))
						if e := tx.Repository.Write(record); e != nil {
							return e
						}
						for _, proof := range step.Before["memory_supports"] {
							if proof["memory_id"] == row["id"] {
								// jsjson preserves object property order required by canonical proofs.
								value, e := parseProofArray(proof["value"].(string))
								if e != nil {
									return e
								}
								if e = tx.Repository.SaveSupports(row["id"].(string), value); e != nil {
									return e
								}
							}
						}
					}
					if len(pending) > 0 {
						if e := tx.Store.WithWriteSession(func(ws *recovery.WriteSession) error {
							compareSnapshot(t, querySnapshot(t, func(q string, a ...any) (snapshotRows, error) { return ws.Query(q, a...) }, step.Before), step.Before)
							return nil
						}); e != nil {
							return e
						}
					}
					var err error
					result, err = command.ProposeAssertion(context.Background(), tx, parseObject(t, string(step.Context)), parseObject(t, string(step.Candidate)))
					if err != nil {
						return err
					}
					if step.DeferredCommit {
						if e := tx.Store.WithWriteSession(func(ws *recovery.WriteSession) error {
							compareSnapshot(t, querySnapshot(t, func(q string, a ...any) (snapshotRows, error) { return ws.Query(q, a...) }, step.After), step.After)
							return nil
						}); e != nil {
							return e
						}
						return stop
					}
					return nil
				})
				if step.Error != "" {
					if e == nil || !strings.Contains(e.Error(), step.Error) {
						t.Fatalf("got error %v want %s", e, step.Error)
					}
				} else {
					if e != nil && !errors.Is(e, stop) {
						t.Fatal(e)
					}
					var want any
					if err := json.Unmarshal(step.Result, &want); err != nil {
						t.Fatal(err)
					}
					if !reflect.DeepEqual(comparableJSON(t, result), want) {
						t.Fatalf("result mismatch\ngot %#v\nwant %#v", comparableJSON(t, result), want)
					}
				}
				if !step.DeferredCommit {
					compareSnapshot(t, querySnapshot(t, func(q string, a ...any) (snapshotRows, error) { return f.main.Query(q, a...) }, step.After), step.After)
				}
			})
		}
	}
	if proposals != 41 || histories != 3 || boundaries != 3 {
		t.Fatalf("coverage counts proposals=%d history=%d externalBoundaries=%d", proposals, histories, boundaries)
	}
}
