package app

import (
	"context"
	"errors"
	"fmt"
	"github.com/killerprojecte/HumanEngine/backend/internal/botruntime"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/replyplan"
)

// Called inside actual runtime ContextSelected after the real V1 planner has
// selected memory.person, memory.identities and memory.commitments. History
// requires those already delivered anchors, not research-stage preparations.
func checkApplicationStateReceipts(a *Application, ctx context.Context, c *botruntime.TurnContext) (err error) {
	o := a.StateExposures
	if o.history.CaptureGuard(c) == nil || o.timeline.CaptureGuard(c) == nil || o.historical.CaptureGuard(c) == nil {
		return errors.New("model_history_receipts_not_delivered")
	}
	if !o.current.HasExposure(c) {
		return errors.New("current_receipt_not_selected_by_model")
	}
	refs, err := o.current.ReadExposedRefs(ctx, c)
	if err != nil {
		return err
	}
	if len(refs) != 1 {
		return fmt.Errorf("expected_original_commitment_ref: %d", len(refs))
	}
	ref := refs[0].Reference
	memories := a.MemoryExposure.ReadExposed(c)
	if len(memories) == 0 {
		return errors.New("historical_memory_anchor_missing")
	}
	memoryID := memories[0].ID
	anchor := a.historicalAnchor(memoryID, c)
	budget, err := replyplan.NewEvidenceBudget(nil)
	if err != nil {
		return err
	}
	ext := o.compose(a, botruntime.Extension{})
	for _, name := range []string{"memory.commitments", "memory.commitmentHistory", "memory.commitmentTimeline", "memory.history"} {
		var values []*jsjson.Object
		var query func(*botruntime.TurnContext) ([]*jsjson.Object, error)
		var deliver func(context.Context, *botruntime.TurnContext, []any) ([]*jsjson.Object, error)
		args := memoryToolObject("id", ref.ID, "version", ref.Version, "deletionEpoch", ref.DeletionEpoch)
		switch name {
		case "memory.commitments":
			args = memoryToolObject("lifecycle", "all")
			query = func(turn *botruntime.TurnContext) ([]*jsjson.Object, error) { return o.current.Query(ctx, turn, args) }
			deliver = o.current.Deliver
		case "memory.commitmentHistory":
			query = func(turn *botruntime.TurnContext) ([]*jsjson.Object, error) { return o.history.Query(ctx, turn, ref) }
			deliver = o.history.Deliver
		case "memory.commitmentTimeline":
			query = func(turn *botruntime.TurnContext) ([]*jsjson.Object, error) {
				return o.timeline.Query(ctx, turn, ref, new(jsjson.Object))
			}
			deliver = o.timeline.Deliver
		case "memory.history":
			args = memoryToolObject("memoryId", memoryID)
			query = func(turn *botruntime.TurnContext) ([]*jsjson.Object, error) {
				return o.historical.Query(ctx, turn, memoryToolObject("memoryId", memoryID, "anchor", anchor))
			}
			deliver = o.historical.Deliver
		}
		values, err = query(c)
		if err != nil {
			return err
		}
		if len(values) == 0 {
			return fmt.Errorf("%s_empty_real_receipt", name)
		}
		clone := make([]any, len(values))
		for i, v := range values {
			clone[i] = v.Clone()
		}
		if got, e := deliver(ctx, c, clone); e != nil {
			return e
		} else if len(got) != 0 {
			return fmt.Errorf("%s_accepted_cloned_receipt", name)
		}
		foreign := &botruntime.TurnContext{Scene: c.Scene, Mode: "real"}
		if got, e := deliver(ctx, foreign, memoryToolArray(values)); e != nil {
			return e
		} else if len(got) != 0 {
			return fmt.Errorf("%s_accepted_foreign_turn", name)
		}
		// Scope mutation is isolated to a synthetic context owned solely by this
		// test. Never mutate the Scene field of the concurrently active native turn.
		probe, closeProbe, e := stateExposureProbe(a, ctx, c.Scene)
		if e != nil {
			return e
		}
		defer func() { err = errors.Join(err, closeProbe()) }()
		probeCurrent, e := o.current.Query(ctx, probe, memoryToolObject("lifecycle", "all"))
		if e != nil {
			return e
		}
		if _, e = o.current.Deliver(ctx, probe, memoryToolArray(probeCurrent)); e != nil {
			return e
		}
		probeValues, e := query(probe)
		if e != nil {
			return e
		}
		if len(probeValues) == 0 {
			return fmt.Errorf("%s_scope_probe_empty", name)
		}
		probe.Scene.ID = "foreign-scene"
		changed, e := deliver(ctx, probe, memoryToolArray(probeValues))
		if e == nil && len(changed) != 0 {
			return fmt.Errorf("%s_accepted_changed_scope", name)
		}
		receipt, e := budget.Receipt(nil, memoryToolObject("id", "state:"+name, "toolName", name, "value", memoryToolArray(values)), "call", nil)
		if e != nil {
			return e
		}
		raw, _ := receipt.Receipt.Get("value")
		selected, ok := raw.([]any)
		if !ok || len(selected) != len(values) {
			return fmt.Errorf("%s_lost_receipt_array", name)
		}
		for i, v := range selected {
			if v != values[i] {
				return fmt.Errorf("%s_lost_original_receipt_identity", name)
			}
		}
		if _, e = jsjson.Stringify(receipt.Message); e != nil {
			return e
		}
		committed, e := ext.Delivered(name, selected, c, args)
		if e != nil {
			return e
		}
		if got, ok := committed.([]any); !ok || len(got) != len(values) {
			return fmt.Errorf("%s_delivery_failed", name)
		}
		// Public tool execution uses the same real owner and validates arguments.
		actual, e := o.execute(a, ctx, name, args, c)
		if e != nil {
			return e
		}
		if got, ok := actual.([]any); !ok || len(got) == 0 {
			return fmt.Errorf("%s_execution_failed", name)
		}
	}
	if o.history.CaptureGuard(c) == nil || o.timeline.CaptureGuard(c) == nil || o.historical.CaptureGuard(c) == nil {
		return errors.New("missing_delivered_state_guard")
	}
	return nil
}
