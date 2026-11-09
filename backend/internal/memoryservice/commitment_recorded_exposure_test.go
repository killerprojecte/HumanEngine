package memoryservice

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/killerprojecte/HumanEngine/backend/internal/botruntime"
	"github.com/killerprojecte/HumanEngine/backend/internal/commitments"
	"github.com/killerprojecte/HumanEngine/backend/internal/recovery"
)

func loadCommitmentRecordedOracle(t *testing.T) commitmentExposureOracle {
	t.Helper()
	raw, err := os.ReadFile("testdata/commitment-recorded-exposure-source.json")
	exposureMust(t, err)
	var x commitmentExposureOracle
	exposureMust(t, json.Unmarshal(raw, &x))
	return x
}
func recordedFixture(t *testing.T, x commitmentOracleCase) (*Service, scopedFixture, *botruntime.TurnContext, *CommitmentExposure, *CommitmentHistoryExposure, *CommitmentTimelineExposure) {
	t.Helper()
	s, f, c, current := commitmentExposureFixture(t, x)
	exposureWrite(t, f, "UPDATE config SET value=json_set(value,'$.memoryHistoricalReadEnabled',json('true')) WHERE key='settings'")
	history, err := NewCommitmentHistoryExposure(current.port.memory, current)
	exposureMust(t, err)
	timeline, err := NewCommitmentTimelineExposure(current.port.memory, current)
	exposureMust(t, err)
	return s, f, c, current, history, timeline
}
func TestCommitmentRecordedExposureActualSourceOracle(t *testing.T) {
	o := loadCommitmentRecordedOracle(t)
	for _, test := range o.Outcomes {
		t.Run(test.Name, func(t *testing.T) {
			s, f, c, current, h, tm := recordedFixture(t, test)
			ctx := context.Background()
			plan := commitments.Reference{ID: "plan-0", Version: 1}
			qh := func(c *botruntime.TurnContext, ref commitments.Reference) []*Object {
				v, err := h.Query(ctx, c, ref)
				exposureMust(t, err)
				return v
			}
			qt := func(c *botruntime.TurnContext, q *Object) []*Object {
				v, err := tm.Query(ctx, c, plan, q)
				exposureMust(t, err)
				return v
			}
			dh := func(c *botruntime.TurnContext, v []any) int {
				out, err := h.Deliver(ctx, c, v)
				exposureMust(t, err)
				return len(out)
			}
			dt := func(c *botruntime.TurnContext, v []any) int {
				out, err := tm.Deliver(ctx, c, v)
				exposureMust(t, err)
				return len(out)
			}
			deliverCurrent := func() {
				v, err := current.Query(ctx, c, nil)
				exposureMust(t, err)
				_, err = current.Deliver(ctx, c, selectedMemory(v))
				exposureMust(t, err)
			}
			var got any
			switch test.Name {
			case "original_current_anchor_required":
				before := []int{len(qh(c, plan)), len(qt(c, nil))}
				v, err := current.Query(ctx, c, nil)
				exposureMust(t, err)
				prepared := []int{len(qh(c, plan)), len(qt(c, nil))}
				_, err = current.Deliver(ctx, c, selectedMemory(v))
				exposureMust(t, err)
				hv, tv := qh(c, plan), qt(c, nil)
				forged := []int{dh(c, []any{exposureClone(hv[0])}), dt(c, []any{exposureClone(tv[0])})}
				delivered := []int{dh(c, selectedMemory(hv)), dt(c, selectedMemory(tv))}
				got = map[string]any{"before": before, "prepared": prepared, "hv": commitmentReceiptJSON(t, hv), "tv": commitmentReceiptJSON(t, tv), "forged": forged, "delivered": delivered, "repeat": []int{dh(c, selectedMemory(hv)), dt(c, selectedMemory(tv))}, "valid": []bool{exposureCheck(t, f, h.CaptureGuard(c)), exposureCheck(t, f, tm.CaptureGuard(c))}, "currentRefs": commitmentRefsJSON(t, current, c), "dependencies": withoutTurnSource(c.Dependencies()), "simulation": []int{len(qh(&botruntime.TurnContext{Scene: c.Scene, Mode: "simulation"}, plan)), len(qt(&botruntime.TurnContext{Scene: c.Scene, Mode: "simulation"}, nil))}}
			case "history_retains_last20_without_new_authority":
				deliverCurrent()
				v := qh(c, commitments.Reference{ID: "plan-0", Version: 24})
				dh(c, selectedMemory(v))
				got = map[string]any{"values": commitmentReceiptJSON(t, v), "refs": commitmentRefsJSON(t, current, c), "valid": exposureCheck(t, f, h.CaptureGuard(c)), "wrongVersion": len(qh(c, plan))}
			case "timeline_real64_window_and_time_queries":
				deliverCurrent()
				fixed, err := time.Parse(time.RFC3339Nano, o.Fixed)
				exposureMust(t, err)
				asof := fixed.Add(10 * time.Second).Format("2006-01-02T15:04:05.000Z")
				failures := []any{}
				for _, q := range []*Object{epObj("limit", float64(0)), epObj("limit", float64(21)), epObj("asOfRecorded", nil), epObj("asOfRecorded", "not-a-time")} {
					_, err := tm.Query(ctx, c, plan, q)
					if err == nil {
						t.Fatal("invalid timeline accepted")
					}
					failures = append(failures, map[string]any{"error": err.Error()})
				}
				got = map[string]any{"full": commitmentReceiptJSON(t, qt(c, nil)), "two": commitmentReceiptJSON(t, qt(c, epObj("limit", float64(2)))), "asOf": commitmentReceiptJSON(t, qt(c, epObj("asOfRecorded", asof))), "early": commitmentReceiptJSON(t, qt(c, epObj("asOfRecorded", o.Fixed))), "errors": failures}
			case "detached_guards_keep_each_delivered_query":
				deliverCurrent()
				h1, h2, t1, t2 := qh(c, plan), qh(c, plan), qt(c, epObj("limit", float64(2))), qt(c, epObj("limit", float64(1)))
				dh(c, selectedMemory(h2))
				dh(c, selectedMemory(h1))
				dt(c, selectedMemory(t2))
				dt(c, selectedMemory(t1))
				gh, gt := h.CaptureGuard(c), tm.CaptureGuard(c)
				before := []bool{exposureCheck(t, f, gh), exposureCheck(t, f, gt)}
				exposureMust(t, s.d.Commitments.Transaction(ctx, func(tx *commitments.Store) error {
					return tx.Read(ctx, func(r *commitments.Reader) error {
						head, err := r.Head(plan.ID)
						if err != nil {
							return err
						}
						_ = head.Set("mutationVersion", commitmentNumber(head, "mutationVersion")+1)
						return tx.Put(head, nil)
					})
				}))
				got = map[string]any{"before": before, "after": []bool{exposureCheck(t, f, gh), exposureCheck(t, f, gt)}, "present": []bool{h.CaptureGuard(c) != nil, tm.CaptureGuard(c) != nil}, "stale": []int{dh(c, selectedMemory(h1)), dt(c, selectedMemory(t1))}, "turnValid": exposureValid(t, f, c)}
			case "history_flag_revokes_both_original_guards", "source_recall_requalifies_retained_views":
				deliverCurrent()
				dh(c, selectedMemory(qh(c, plan)))
				dt(c, selectedMemory(qt(c, nil)))
				gh, gt := h.CaptureGuard(c), tm.CaptureGuard(c)
				if test.Name == "history_flag_revokes_both_original_guards" {
					exposureWrite(t, f, "UPDATE config SET value=json_set(value,'$.memoryHistoricalReadEnabled',json('false')) WHERE key='settings'")
				} else {
					exposureWrite(t, f, "INSERT INTO recall_tombstones VALUES(?,?,?)", "s", "proposal-0", o.Fixed)
				}
				got = map[string]any{"guards": []bool{exposureCheck(t, f, gh), exposureCheck(t, f, gt)}, "query": []int{len(qh(c, plan)), len(qt(c, nil))}, "currentRefs": commitmentRefsJSON(t, current, c), "turnValid": exposureValid(t, f, c)}
			default:
				t.Fatal(test.Name)
			}
			raw, err := json.Marshal(got)
			exposureMust(t, err)
			var normalized any
			exposureMust(t, json.Unmarshal(raw, &normalized))
			if !reflect.DeepEqual(normalized, test.Value) {
				want, _ := json.Marshal(test.Value)
				t.Fatalf("source mismatch\ngot %s\nwant %s", raw, want)
			}
		})
	}
}
func TestCommitmentRecordedExposureSameWriterAndDetachedAuthority(t *testing.T) {
	o := loadCommitmentRecordedOracle(t)
	_, f, c, current, h, tm := recordedFixture(t, o.Outcomes[2])
	ctx := context.Background()
	v, err := current.Query(ctx, c, nil)
	exposureMust(t, err)
	_, err = current.Deliver(ctx, c, selectedMemory(v))
	exposureMust(t, err)
	ref := commitments.Reference{ID: "plan-0", Version: 1}
	hv, err := h.Query(ctx, c, ref)
	exposureMust(t, err)
	tv, err := tm.Query(ctx, c, ref, nil)
	exposureMust(t, err)
	_, err = h.Deliver(ctx, c, selectedMemory(hv))
	exposureMust(t, err)
	_, err = tm.Deliver(ctx, c, selectedMemory(tv))
	exposureMust(t, err)
	guards := []*botruntime.ToolResultGuard{h.CaptureGuard(c), tm.CaptureGuard(c)}
	var before, after int
	exposureMust(t, f.main.QueryRow("SELECT total_changes()").Scan(&before))
	for _, g := range guards {
		if !exposureCheck(t, f, g) {
			t.Fatal("initial guard")
		}
	}
	exposureMust(t, f.main.QueryRow("SELECT total_changes()").Scan(&after))
	if before != after {
		t.Fatal("guard wrote")
	}
	rollback := errors.New("rollback")
	err = f.recovery.WithWriteSession(ctx, f.main, func(ws *recovery.WriteSession) error {
		if _, err := ws.Exec("UPDATE config SET value=json_set(value,'$.memoryHistoricalReadEnabled',json('false')) WHERE key='settings'"); err != nil {
			return err
		}
		if err := f.events.WithSession(ws).WithRuntimeReads(ctx, func(reads botruntime.ReadAuthority) error {
			for _, g := range guards {
				valid, err := g.Check(reads)
				if valid || err != nil {
					t.Fatal("writer flag ignored", err)
				}
			}
			return nil
		}); err != nil {
			return err
		}
		return rollback
	})
	if !errors.Is(err, rollback) {
		t.Fatal(err)
	}
	// An uncommitted timeline row change must affect only the timeline guard,
	// even though both guards reuse the same current commitment anchor.
	err = f.recovery.WithWriteSession(ctx, f.main, func(ws *recovery.WriteSession) error {
		if _, err := ws.Exec("UPDATE memory_commitment_transitions SET value=json_set(value,'$.recordedAt',?) WHERE id='plan-0' AND mutation_version=70", "2026-10-28T12:02:00.000Z"); err != nil {
			return err
		}
		if err := f.events.WithSession(ws).WithRuntimeReads(ctx, func(reads botruntime.ReadAuthority) error {
			historyValid, err := guards[0].Check(reads)
			if err != nil || !historyValid {
				t.Fatal("timeline edit changed history authority", err)
			}
			timelineValid, err := guards[1].Check(reads)
			if err != nil || timelineValid {
				t.Fatal("same-writer timeline edit was not observed", err)
			}
			return nil
		}); err != nil {
			return err
		}
		return rollback
	})
	if !errors.Is(err, rollback) {
		t.Fatal(err)
	}
	var escaped botruntime.ReadAuthority
	exposureMust(t, f.events.WithRuntimeReads(ctx, func(r botruntime.ReadAuthority) error { escaped = r; return nil }))
	for _, g := range guards {
		if valid, err := g.Check(escaped); valid || err != nil {
			t.Fatal("expired accepted", err)
		}
	}
	_, foreign := serviceSetup(t)
	exposureMust(t, foreign.events.WithRuntimeReads(ctx, func(r botruntime.ReadAuthority) error {
		for _, g := range guards {
			if valid, err := g.Check(r); valid || err != nil {
				t.Fatal("foreign accepted", err)
			}
		}
		return nil
	}))
	c.Signal().Abort(errors.New("old turn ended"))
	for _, g := range guards {
		if !exposureCheck(t, f, g) {
			t.Fatal("detached guard lost authority after rollback/cancellation")
		}
	}
}
