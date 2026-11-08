package assertionproposal

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/killerprojecte/HumanEngine/backend/internal/assertionauthority"
	"github.com/killerprojecte/HumanEngine/backend/internal/assertionlifecycle"
	"github.com/killerprojecte/HumanEngine/backend/internal/assertions"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"os"
	"reflect"
	"strings"
	"testing"
)

// These recording owners are deliberately test-only. Production composition must
// use memoryservice's real graph/projection/workspace/privacy owners.
func testCommand(t *testing.T, f scopedFixture, id func() (string, error)) (*Command, *assertions.Coordinator) {
	t.Helper()
	active := func(s *assertions.Store, _ string) error {
		return s.Read(context.Background(), func(r *assertions.Reader) error { return r.CheckScope() })
	}
	opts := testOptions(t)
	command, e := New(assertionlifecycle.Owners{RemoveProjection: active, BeforePurgeHistory: active, BeforeInvalidateRecord: active}, opts, Runtime{ID: id, Now: func() string { return "2026-10-28T12:00:00.000Z" }})
	if e != nil {
		t.Fatal(e)
	}
	coordinator, e := assertions.NewCoordinator(f.store, assertions.CoordinatorOptions{
		HistorySupports: func(s *assertions.Store, id string) (out []any, err error) {
			err = s.Read(context.Background(), func(r *assertions.Reader) error {
				a, e := assertionauthority.New(r, opts)
				if e != nil {
					return e
				}
				out, e = a.HistorySupports(id)
				return e
			})
			return
		},
		Retain: func(_ *assertions.Store, r *Object) (bool, error) {
			return str(get(r, "status")) != "invalidated" && str(get(r, "lineageStatus")) != "quarantined", nil
		},
		RemoveProjection: active,
	})
	if e != nil {
		t.Fatal(e)
	}
	return command, coordinator
}
func comparableJSON(t *testing.T, v any) any {
	t.Helper()
	switch x := v.(type) {
	case Result:
		value := obj("status", x.Status)
		if x.Reason != "" {
			_ = value.Set("reason", x.Reason)
		}
		if x.Memory != nil {
			_ = value.Set("memory", x.Memory)
		}
		v = value
	case []*Object:
		if x == nil {
			v = nil
		} else {
			values := make([]any, len(x))
			for i, o := range x {
				values[i] = o
			}
			v = values
		}
	}
	raw, e := jsjson.Stringify(v)
	if e != nil {
		t.Fatal(e)
	}
	var out any
	if e = json.Unmarshal([]byte(raw), &out); e != nil {
		t.Fatal(e)
	}
	return out
}
func TestActualSourceSequentialCoreScenarios(t *testing.T) {
	raw, e := os.ReadFile("testdata/proposal-source.json")
	if e != nil {
		t.Fatal(e)
	}
	var oracle struct {
		Cases []struct {
			Name   string
			Traces []struct {
				Label                      string
				Context, Candidate, Result json.RawMessage
			}
		}
	}
	if e = json.Unmarshal(raw, &oracle); e != nil {
		t.Fatal(e)
	}
	executed := 0
	for _, scenario := range oracle.Cases {
		if scenario.Name != "exact-and-semantic-corroboration" && scenario.Name != "correction-reversion-and-source-reuse" && scenario.Name != "dependency-cycle-and-stale-reference" && scenario.Name != "subject-witness-mismatch" {
			continue
		}
		executed++
		t.Run(scenario.Name, func(t *testing.T) {
			f := scopedSetup(t)
			nextID := ""
			command, coordinator := testCommand(t, f, func() (string, error) {
				if nextID == "" {
					return "", errors.New("unexpected_id_allocation")
				}
				return nextID, nil
			})
			for _, step := range scenario.Traces {
				var expected struct {
					Status, Reason string
					Memory         struct{ ID string }
				}
				if e = json.Unmarshal(step.Result, &expected); e != nil {
					t.Fatal(e)
				}
				nextID = expected.Memory.ID
				var result Result
				e = coordinator.Transaction(context.Background(), func(tx *assertions.RevisionTxn) error {
					var err error
					result, err = command.ProposeAssertion(context.Background(), tx, parseObject(t, string(step.Context)), parseObject(t, string(step.Candidate)))
					return err
				})
				if e != nil {
					t.Fatalf("%s: %v", step.Label, e)
				}
				var want any
				if e = json.Unmarshal(step.Result, &want); e != nil {
					t.Fatal(e)
				}
				if got := comparableJSON(t, result); !reflect.DeepEqual(got, want) {
					t.Fatalf("%s\ngot %#v\nwant %#v", step.Label, got, want)
				}
			}
		})
	}
	if executed != 4 {
		t.Fatalf("sequential core scenarios: %d", executed)
	}
}
func simpleInput(t *testing.T, content, msg string) (*Object, *Object) {
	return parseObject(t, fmt.Sprintf(`{"sceneId":"s","simulation":false,"provenance":"source-only","sourceEvidence":[{"sceneId":"s","messageId":%q,"senderId":"p","available":true,"isBot":false}]}`, msg)), parseObject(t, fmt.Sprintf(`{"content":%q,"kind":"fact","personId":"p","sources":[{"sceneId":"s","messageId":%q,"senderId":"p"}]}`, content, msg))
}
func TestNativeProposalSQLFailureAndNestedRollback(t *testing.T) {
	f := scopedSetup(t)
	counter := 0
	command, coordinator := testCommand(t, f, func() (string, error) { counter++; return fmt.Sprintf("id-%d", counter), nil })
	call := func(tx *assertions.RevisionTxn, content, msg, supersedes string) (Result, error) {
		ctx, c := simpleInput(t, content, msg)
		if supersedes != "" {
			_ = c.Set("supersedesId", supersedes)
		}
		return command.ProposeAssertion(context.Background(), tx, ctx, c)
	}
	var root Result
	if e := coordinator.Transaction(context.Background(), func(tx *assertions.RevisionTxn) error {
		var e error
		root, e = call(tx, "Synthetic root", "m1", "")
		return e
	}); e != nil {
		t.Fatal(e)
	}
	rootID := str(get(root.Memory, "id"))
	if _, e := f.main.Exec("INSERT INTO vector_spaces VALUES('fixture-space','{}')"); e != nil {
		t.Fatal(e)
	}
	if _, e := f.main.Exec("INSERT INTO memory_vectors VALUES(?,?,?)", rootID, "fixture-space", []byte{1, 2, 3, 4}); e != nil {
		t.Fatal(e)
	}
	assertVector := func(want int) {
		t.Helper()
		var n int
		if e := f.main.QueryRow("SELECT count(*) FROM memory_vectors WHERE memory_id=?", rootID).Scan(&n); e != nil || n != want {
			t.Fatalf("vector count %d want %d: %v", n, want, e)
		}
	}

	if _, e := f.main.Exec(`CREATE TRIGGER fail_second BEFORE INSERT ON memories WHEN NEW.content='Synthetic fail' BEGIN SELECT RAISE(ABORT,'synthetic_second_write_failure'); END`); e != nil {
		t.Fatal(e)
	}
	e := coordinator.Transaction(context.Background(), func(tx *assertions.RevisionTxn) error { _, e := call(tx, "Synthetic fail", "m2", rootID); return e })
	if e == nil || !strings.Contains(e.Error(), "synthetic_second_write_failure") {
		t.Fatalf("expected SQL failure, got %v", e)
	}
	var raw string
	if e = f.main.QueryRow("SELECT value FROM memories WHERE id=?", rootID).Scan(&raw); e != nil {
		t.Fatal(e)
	}
	if str(get(parseObject(t, raw), "status")) != "active" {
		t.Fatal("correction did not roll back")
	}
	assertVector(1)
	if _, e = f.main.Exec("DROP TRIGGER fail_second"); e != nil {
		t.Fatal(e)
	}
	abort := errors.New("synthetic_child_abort")
	e = coordinator.Transaction(context.Background(), func(tx *assertions.RevisionTxn) error {
		err := tx.Transaction(context.Background(), func(child *assertions.RevisionTxn) error {
			if _, e := call(child, "Synthetic transient", "m3", rootID); e != nil {
				return e
			}
			return abort
		})
		if !errors.Is(err, abort) {
			return fmt.Errorf("child: %w", err)
		}
		_, err = call(tx, "Synthetic survivor", "m4", "")
		return err
	})
	if e != nil {
		t.Fatal(e)
	}
	var count int
	if e = f.main.QueryRow("SELECT count(*) FROM memories WHERE content='Synthetic transient'").Scan(&count); e != nil || count != 0 {
		t.Fatalf("transient persisted: %d %v", count, e)
	}
	if e = f.main.QueryRow("SELECT count(*) FROM memory_assertion_revisions WHERE assertion_id=?", rootID).Scan(&count); e != nil {
		t.Fatal(e)
	}
	if count != 1 {
		t.Fatalf("rolled-back child polluted history: %d", count)
	}
	assertVector(1)
	if e = coordinator.Transaction(context.Background(), func(tx *assertions.RevisionTxn) error {
		_, e := call(tx, "Synthetic successful correction", "m5", rootID)
		return e
	}); e != nil {
		t.Fatal(e)
	}
	assertVector(0)
	if e = f.main.QueryRow("SELECT value FROM memories WHERE id=?", rootID).Scan(&raw); e != nil {
		t.Fatal(e)
	}
	if str(get(parseObject(t, raw), "status")) != "superseded" {
		t.Fatal("successful correction missing")
	}

}
