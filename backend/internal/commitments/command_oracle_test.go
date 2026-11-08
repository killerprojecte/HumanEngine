package commitments

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"github.com/killerprojecte/HumanEngine/backend/internal/recovery"
	"os"
	"path/filepath"
	"testing"
)

// Recording current/afterWrite ports isolate source command semantics here.
// Actual SQL, temporal normalization and typed journal writes are retained.
func TestCommandActualSourceOracle(t *testing.T) {
	raw, e := os.ReadFile("testdata/command-source-oracle.json")
	must(t, e)
	oracle := object(parse(t, string(raw)))
	if get(oracle, "sourceNode") != "v24.22.1" {
		t.Fatal("unexpected source runtime")
	}
	for _, entry := range object(get(oracle, "sources")).Members() {
		contents, e := os.ReadFile(filepath.Join("../../..", entry.Key))
		must(t, e)
		if fmt.Sprintf("%x", sha256.Sum256(contents)) != entry.Value {
			t.Fatalf("source oracle stale: %s", entry.Key)
		}
	}
	f := setup(t)
	f.store.options.Compare = commandSourceCompare(oracle)
	f.now = str(get(oracle, "now"))
	must(t, f.store.Initialize(context.Background()))
	views := get(oracle, "views")
	id := 0
	for _, raw := range arr(get(oracle, "steps")) {
		step := object(raw)
		t.Run(str(get(step, "name")), func(t *testing.T) {
			f.events = []any{}
			before, e := f.ledger.Head(context.Background())
			must(t, e)
			var out CommandResult
			e = f.store.Transaction(context.Background(), func(st *Store) error {
				read := &ReadService{reader: st.reader(), check: st.requireTransaction, enabled: currentEnabled, current: func(_ Context, r Reference) (*Assertion, error) {
					v := get(views, r.ID)
					if v == nil || r.Version != 1 || r.DeletionEpoch != 0 {
						return nil, nil
					}
					return &Assertion{Record: object(get(v, "record")), Proofs: arr(get(v, "proofs"))}, nil
				}}
				service := &CommandService{store: st, read: read, owners: f.service.owners}
				var e error
				out, e = service.Admit(object(get(step, "ctx")), get(step, "backing"), get(step, "command"), CommandPolicy{LearningEnabled: true, ID: func() (string, error) {
					id++
					if id != 1 {
						t.Fatal("unexpected id")
					}
					return "command-1", nil
				}})
				return e
			})
			if has(step, "error") {
				if e == nil {
					t.Fatal("source schema failure accepted")
				}
				return
			}
			must(t, e)
			result := obj("status", out.Status)
			if out.Reason != "" {
				_ = result.Set("reason", out.Reason)
			}
			if out.Item != nil {
				_ = result.Set("item", out.Item)
			}
			if !deepEqual(result, get(step, "result")) {
				t.Fatalf("result differs\ngot %s\nwant %s", stringify(t, result), stringify(t, get(step, "result")))
			}
			if !deepEqual(f.events, get(step, "events")) {
				t.Fatalf("afterWrite differs\ngot %s\nwant %s", stringify(t, f.events), stringify(t, get(step, "events")))
			}
			must(t, f.store.Read(context.Background(), func(r *Reader) error {
				snapshot, e := r.Snapshot("command-1")
				if e != nil {
					return e
				}
				if !deepEqual(snapshot, get(step, "snapshot")) {
					t.Fatalf("snapshot differs\ngot %s\nwant %s", stringify(t, snapshot), stringify(t, get(step, "snapshot")))
				}
				return nil
			}))
			commands := rowsValue(t, f.main, "SELECT key,id,version FROM memory_commitment_commands ORDER BY key")
			if !deepEqual(commands, get(step, "commands")) {
				t.Fatal("source idempotency keys differ")
			}
			// Source duplicate/rejected operations emit no durable intent.
			gate, e := recovery.ReadGate(context.Background(), f.main)
			must(t, e)
			head, e := f.ledger.Head(context.Background())
			must(t, e)
			if head.Sequence-before.Sequence != int64(len(arr(get(step, "changes")))) || gate.Head != head {
				t.Fatalf("intent/ACK mismatch: %v %v", head, gate)
			}
			if len(arr(get(step, "changes"))) > 0 {
				var raw string
				must(t, f.external.QueryRow("SELECT payload FROM revocations ORDER BY sequence DESC LIMIT 1").Scan(&raw))
				event := object(parse(t, raw))
				change := arr(get(step, "changes"))[0]
				if get(event, "qualificationHash") != get(change, "qualificationHash") || get(event, "temporalVersion") != float64(1) || has(event, "privacyMarker") {
					t.Fatalf("wrong normal head intent: %s", raw)
				}
			}
		})
	}
}

func commandSourceCompare(oracle *Object) func(string, string) (int, error) {
	return func(a, b string) (int, error) {
		row := get(get(oracle, "comparisons"), a)
		if !has(row, b) {
			return 0, fmt.Errorf("command source comparison missing %q/%q", a, b)
		}
		return int(num(get(row, b))), nil
	}
}
func TestCommandActualSourceCaughtSQLPhases(t *testing.T) {
	raw, e := os.ReadFile("testdata/command-source-oracle.json")
	must(t, e)
	oracle := object(parse(t, string(raw)))
	for _, raw := range arr(get(oracle, "phases")) {
		phase := object(raw)
		t.Run(str(get(phase, "phase")), func(t *testing.T) {
			f := setup(t)
			f.store.options.Compare = commandSourceCompare(oracle)
			f.now = str(get(oracle, "now"))
			must(t, f.store.Initialize(context.Background()))
			_, e := f.main.Exec(`CREATE TABLE phase_probe(value TEXT); CREATE TRIGGER phase_abort BEFORE INSERT ON phase_probe WHEN NEW.value='abort' BEGIN SELECT RAISE(ABORT,'synthetic_phase_abort'); END`)
			must(t, e)
			if get(phase, "phase") == "remember" {
				_, e = f.main.Exec(`CREATE TRIGGER remember_abort BEFORE INSERT ON memory_commitment_commands BEGIN SELECT RAISE(ABORT,'synthetic_phase_abort'); END`)
				must(t, e)
			}
			calls := 0
			step := arr(get(oracle, "steps"))[0]
			views := get(oracle, "views")
			var caught error
			must(t, f.store.Transaction(context.Background(), func(st *Store) error {
				read := &ReadService{reader: st.reader(), check: st.requireTransaction, enabled: currentEnabled, current: func(_ Context, r Reference) (*Assertion, error) {
					v := get(views, r.ID)
					if v == nil {
						return nil, nil
					}
					return &Assertion{Record: object(get(v, "record")), Proofs: arr(get(v, "proofs"))}, nil
				}}
				service := &CommandService{store: st, read: read, owners: Owners{AfterWrite: func(st *Store, _ *Object, _, _ []any) error {
					calls++
					if get(phase, "phase") == "afterWrite" {
						return st.exec("INSERT INTO phase_probe VALUES('abort')")
					}
					return nil
				}}}
				_, caught = service.Admit(object(get(step, "ctx")), get(step, "backing"), get(step, "command"), CommandPolicy{LearningEnabled: true, ID: func() (string, error) { return "phase-plan", nil }})
				return st.exec("INSERT INTO phase_probe VALUES('continued')") // Source owner catches ordinary ABORT and continues.
			}))
			if caught == nil || caught.Error() != get(phase, "error") || float64(calls) != get(phase, "afterCalls") {
				t.Fatalf("caught phase mismatch %v calls %d", caught, calls)
			}
			must(t, f.store.Read(context.Background(), func(r *Reader) error {
				snapshot, e := r.Snapshot("phase-plan")
				if e == nil && !deepEqual(snapshot, get(phase, "snapshot")) {
					t.Fatal("source caught prefix changed")
				}
				return e
			}))
			if !deepEqual(rowsValue(t, f.main, "SELECT key,id,version FROM memory_commitment_commands ORDER BY key"), get(phase, "commands")) || !deepEqual(rowsValue(t, f.main, "SELECT value FROM phase_probe"), get(phase, "continued")) {
				t.Fatal("caught ABORT did not preserve exact source phase")
			}
			gate, e := recovery.ReadGate(context.Background(), f.main)
			must(t, e)
			head, e := f.ledger.Head(context.Background())
			must(t, e)
			if head.Sequence != 1 || gate.Head != head {
				t.Fatal("caught ordinary phase lost ACK")
			}
		})
	}
}

func TestCommandActualSourcePolicyBoundaries(t *testing.T) {
	raw, e := os.ReadFile("testdata/command-source-oracle.json")
	must(t, e)
	oracle := object(parse(t, string(raw)))
	for _, raw := range arr(get(oracle, "policies")) {
		policy := object(raw)
		t.Run(str(get(policy, "name")), func(t *testing.T) {
			f := setup(t)
			f.store.options.Compare = commandSourceCompare(oracle)
			f.now = str(get(oracle, "now"))
			must(t, f.store.Initialize(context.Background()))
			views := get(oracle, "views")
			if initial := get(policy, "initial"); initial != nil {
				must(t, f.store.Transaction(context.Background(), func(st *Store) error {
					head := object(get(initial, "head"))
					for _, revision := range arr(get(initial, "revisions")) {
						if e := st.Put(head, object(revision)); e != nil {
							return e
						}
					}
					return st.ReplaceTimeline("policy-plan", object(get(initial, "timeline")))
				}))
			}
			for _, raw := range arr(get(policy, "cases")) {
				step := object(raw)
				var out CommandResult
				e := f.store.Transaction(context.Background(), func(st *Store) error {
					read := &ReadService{reader: st.reader(), check: st.requireTransaction, enabled: currentEnabled, current: func(_ Context, r Reference) (*Assertion, error) {
						v := get(views, r.ID)
						if v == nil {
							return nil, nil
						}
						return &Assertion{Record: object(get(v, "record")), Proofs: arr(get(v, "proofs"))}, nil
					}}
					service := &CommandService{store: st, read: read, owners: f.service.owners}
					var e error
					out, e = service.Admit(object(get(step, "ctx")), get(step, "backing"), get(step, "command"), CommandPolicy{LearningEnabled: true, ID: func() (string, error) { return "policy-plan", nil }})
					return e
				})
				if has(step, "error") {
					if !errors.Is(e, ErrSchema) {
						t.Fatalf("source schema bound accepted: %v", e)
					}
					continue
				}
				must(t, e)
				result := obj("status", out.Status)
				if out.Reason != "" {
					_ = result.Set("reason", out.Reason)
				}
				if out.Item != nil {
					_ = result.Set("item", out.Item)
				}
				if !deepEqual(result, get(step, "result")) {
					t.Fatalf("policy result differs %s", stringify(t, result))
				}
				must(t, f.store.Read(context.Background(), func(r *Reader) error {
					snapshot, e := r.Snapshot("policy-plan")
					if e == nil && !deepEqual(snapshot, get(step, "snapshot")) {
						t.Fatal("policy state differs")
					}
					return e
				}))
			}
		})
	}
}
