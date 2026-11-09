package memoryservice

import (
	"context"
	"errors"
	"testing"

	"github.com/killerprojecte/HumanEngine/backend/internal/botruntime"
	"github.com/killerprojecte/HumanEngine/backend/internal/commitments"
	"github.com/killerprojecte/HumanEngine/backend/internal/sourceevents"
)

// A leased WithRuntimeReads fixture hides accidental nested publication: every
// read can reuse its View. Runtime eligibleSignal instead passes the unbound
// RuntimeAuthority. With one real connection, any nested root Publish blocks.
// These checks deliberately do not wrap the authority in WithRuntimeReads.
func TestCommitmentExposureUnboundRuntimeAuthority(t *testing.T) {
	o := loadCommitmentRecordedOracle(t)
	s, f, c, current, history, timeline := recordedFixture(t, o.Outcomes[2])
	if f.main.Stats().MaxOpenConnections != 1 {
		t.Fatal("regression requires exactly one real SQLite connection")
	}
	ctx := context.Background()
	v, err := current.Query(ctx, c, nil)
	exposureMust(t, err)
	if len(v) == 0 {
		t.Fatal("current fixture did not produce a receipt")
	}
	delivered, err := current.Deliver(ctx, c, selectedMemory(v))
	exposureMust(t, err)
	if len(delivered) != len(v) {
		t.Fatal("current original receipts not delivered")
	}
	ref := commitments.Reference{ID: "plan-0", Version: 1}
	hv, err := history.Query(ctx, c, ref)
	exposureMust(t, err)
	if len(hv) != 1 {
		t.Fatal("history receipt missing")
	}
	delivered, err = history.Deliver(ctx, c, selectedMemory(hv))
	exposureMust(t, err)
	if len(delivered) != 1 {
		t.Fatal("history original receipt not delivered")
	}
	// Distinct delivered timeline queries must retain both real validations.
	for _, query := range []*Object{nil, epObj("limit", float64(2))} {
		tv, err := timeline.Query(ctx, c, ref, query)
		exposureMust(t, err)
		if len(tv) != 1 {
			t.Fatal("timeline receipt missing")
		}
		delivered, err = timeline.Deliver(ctx, c, selectedMemory(tv))
		exposureMust(t, err)
		if len(delivered) != 1 {
			t.Fatal("timeline original receipt not delivered")
		}
	}
	authority, err := sourceevents.NewRuntimeAuthority(f.events)
	exposureMust(t, err)
	guards := []*botruntime.ToolResultGuard{current.CaptureGuard(c), history.CaptureGuard(c), timeline.CaptureGuard(c)}
	for _, g := range guards {
		if g == nil {
			t.Fatal("delivered owner did not capture a guard")
		}
	}
	check := func(want []bool, turnValid bool) {
		t.Helper()
		var before, after int
		exposureMust(t, f.main.QueryRow("SELECT total_changes()").Scan(&before))
		for i, g := range guards {
			valid, err := g.Check(authority)
			exposureMust(t, err)
			if valid != want[i] {
				t.Fatalf("unbound guard %d: got %t, want %t", i, valid, want[i])
			}
		}
		// This calls the actual live turn's own unbound runtime authority, not
		// the explicit-authority test overload used by the prior scoped tests.
		valid, err := c.ValidateMemoryContext()
		exposureMust(t, err)
		if valid != turnValid {
			t.Fatalf("unbound turn validation: got %t, want %t", valid, turnValid)
		}
		exposureMust(t, f.main.QueryRow("SELECT total_changes()").Scan(&after))
		if before != after {
			t.Fatal("unbound validation wrote or refreshed persistent state")
		}
		if f.main.Stats().InUse != 0 {
			t.Fatal("unbound validation retained the only connection")
		}
	}
	check([]bool{true, true, true}, true)
	exposureWrite(t, f, "UPDATE config SET value=json_set(value,'$.memoryHistoricalReadEnabled',json('false')) WHERE key='settings'")
	check([]bool{true, false, false}, false)
	exposureWrite(t, f, "UPDATE config SET value=json_set(value,'$.memoryHistoricalReadEnabled',json('true')) WHERE key='settings'")
	check([]bool{true, true, true}, true)
	exposureWrite(t, f, "UPDATE config SET value=json_set(value,'$.memoryCommitmentsAnswerEnabled',json('false')) WHERE key='settings'")
	check([]bool{false, false, false}, false)
	exposureWrite(t, f, "UPDATE config SET value=json_set(value,'$.memoryCommitmentsAnswerEnabled',json('true')) WHERE key='settings'")
	check([]bool{true, true, true}, true)

	// Detaching from the old turn must preserve the original snapshot authority.
	c.Signal().Abort(errors.New("old turn completed"))
	check([]bool{true, true, true}, false)
	exposureMust(t, s.d.Commitments.Transaction(ctx, func(tx *commitments.Store) error {
		return tx.Read(ctx, func(r *commitments.Reader) error {
			head, err := r.Head(ref.ID)
			if err != nil {
				return err
			}
			_ = head.Set("mutationVersion", commitmentNumber(head, "mutationVersion")+1)
			return tx.Put(head, nil)
		})
	}))
	check([]bool{false, false, false}, false)
}

func TestHistoricalExposureUnboundRuntimeAuthority(t *testing.T) {
	o := loadHistoricalOracle(t)
	x := o.Outcomes[0]
	s, f, c, e := historicalFixture(t, o.Fixed, x.Record, x.Proofs)
	if f.main.Stats().MaxOpenConnections != 1 {
		t.Fatal("regression requires exactly one real SQLite connection")
	}
	ctx := context.Background()
	v, err := e.Query(ctx, c, epObj("memoryId", "history"))
	exposureMust(t, err)
	if len(v) != 1 {
		t.Fatal("historical receipt missing")
	}
	delivered, err := e.Deliver(ctx, c, selectedMemory(v))
	exposureMust(t, err)
	if len(delivered) != 1 {
		t.Fatal("historical original receipt not delivered")
	}
	guard := e.CaptureGuard(c)
	if guard == nil {
		t.Fatal("historical guard missing")
	}
	authority, err := sourceevents.NewRuntimeAuthority(f.events)
	exposureMust(t, err)
	check := func(want, turnValid bool) {
		t.Helper()
		var before, after int
		exposureMust(t, f.main.QueryRow("SELECT total_changes()").Scan(&before))
		valid, err := guard.Check(authority)
		exposureMust(t, err)
		if valid != want {
			t.Fatalf("unbound historical guard: got %t, want %t", valid, want)
		}
		valid, err = c.ValidateMemoryContext()
		exposureMust(t, err)
		if valid != turnValid {
			t.Fatalf("unbound historical turn: got %t, want %t", valid, turnValid)
		}
		exposureMust(t, f.main.QueryRow("SELECT total_changes()").Scan(&after))
		if before != after {
			t.Fatal("unbound historical validation wrote or refreshed state")
		}
		if f.main.Stats().InUse != 0 {
			t.Fatal("unbound historical validation retained the only connection")
		}
	}
	check(true, true)
	exposureWrite(t, f, "UPDATE config SET value=json_set(value,'$.memoryHistoricalReadEnabled',json('false')) WHERE key='settings'")
	check(false, false)
	exposureWrite(t, f, "UPDATE config SET value=json_set(value,'$.memoryHistoricalReadEnabled',json('true')) WHERE key='settings'")
	check(true, true)
	c.Signal().Abort(errors.New("old turn completed"))
	check(true, false)
	_, err = s.Lock(ctx, "history", true)
	exposureMust(t, err)
	check(false, false)
}
