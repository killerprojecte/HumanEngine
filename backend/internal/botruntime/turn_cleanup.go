package botruntime

import (
	"errors"
	"fmt"
	"github.com/killerprojecte/HumanEngine/backend/internal/turnwait"
)

func (r *Runtime) finish(t *turn, issues int) {
	c := t.context
	{
		if err := r.services.Authority.Ready(); err != nil {
			if contains([]string{"recovery_authority_head_changed", "recovery_authority_not_bound", "recovery_gate_invalid", "recovery_chain_invalid", "recovery_format_unsupported"}, err.Error()) {
				c.root.Abort(code("recovery_quarantined"))
			} else {
				t.result.Status = "failed"
			}
			if t.result.Err == nil {
				t.result.Err = err
			}
		}
	}
	receipts := r.outcomes(t)
	counts := map[string]int{"sentSegments": 0, "failedSegments": 0, "unknownSegments": 0, "cancelledSegments": 0}
	for _, receipt := range receipts {
		counts[string(receipt.Status)+"Segments"]++
	}
	sent, unknown, failed, cancelled := counts["sentSegments"], counts["unknownSegments"], counts["failedSegments"], counts["cancelledSegments"]
	if c.root.Aborted() && t.result.Status != "failed" {
		t.result.Status = "cancelled"
		if sent+unknown > 0 {
			t.result.Status = "partial"
		}
		t.result.Reason = "context_cancelled"
		if unknown > 0 {
			t.result.Reason = "delivery_unknown"
		}
	} else if t.result.Status == "completed" && (unknown+failed) > 0 {
		t.result.Status = "failed"
		if sent > 0 {
			t.result.Status = "partial"
		}
		t.result.Reason = "delivery_failed"
		if unknown > 0 {
			t.result.Reason = "delivery_unknown"
		}
	} else if t.result.Status == "completed" && cancelled > 0 {
		t.result.Status = "cancelled"
		if sent > 0 {
			t.result.Status = "partial"
		}
		t.result.Reason = "send_cancelled"
	} else if t.result.Status == "completed" && sent > 0 {
		t.result.Reason = "reply_delivered"
		if c.Mode == "simulation" {
			t.result.Reason = "simulated_delivery"
		}
	}
	if issues > 0 && !c.root.Aborted() && !c.Signal().Aborted() {
		t.result.Status = "failed"
		if sent > 0 {
			t.result.Status = "partial"
		}
		t.result.Reason = "turn_tool_failures"
	}
	if t.result.Err == nil {
		t.result.Err = failureFromOutcomes(receipts)
	}
	if t.result.Err == nil && len(t.toolIssues) > 0 {
		t.result.Err = fmt.Errorf("turn_tool_failures: %w", errors.Join(t.toolIssues...))
	}
	t.result.Counts = counts
	t.result.Receipts = receipts
	r.trace(Event{SceneID: c.Scene.ID, TurnID: c.TurnID, Stage: "turn", Status: t.result.Status, Reason: t.result.Reason, Counts: counts})
	if err := r.cleanupTurn(t); err != nil {
		t.result.CleanupErr = err
		if t.result.Err == nil {
			t.result.Err = err
		}
		r.trace(Event{SceneID: c.Scene.ID, TurnID: c.TurnID, Stage: "turn_cleanup", Status: "failed", Reason: "turn_cleanup_failed"})
	} else {
		r.trace(Event{SceneID: c.Scene.ID, TurnID: c.TurnID, Stage: "turn_cleanup", Status: "completed", Reason: "turn_cleanup_finished"})
	}
	close(t.done)
}

func (r *Runtime) cleanupTurn(t *turn) (result error) {
	defer func() { result = errors.Join(result, t.context.releaseExtensionState()) }()
	c, s := t.context, t.state
	t.sendMu.Lock()
	t.guard = nil
	t.sendMu.Unlock()
	s.mu.Lock()
	s.routing.Turns.Delete(c.TurnID)
	r.publishLocked(s)
	s.mu.Unlock()
	c.root.Abort(code("turn_finished"))
	s.mu.Lock()
	s.historyDirty = true
	if s.tasks[t.topic.id] == t {
		delete(s.tasks, t.topic.id)
	}
	pending := s.pending[t.topic.id]
	delete(s.pending, t.topic.id)
	r.publishLocked(s)
	s.mu.Unlock()
	// Ready occurs after native owner removal, exactly like source cleanup. No
	// callback/SQL runs under the state mutex. Unknown failures stop cleanup here.
	ready := true
	if err := r.services.Authority.Ready(); err != nil {
		if !contains([]string{"recovery_authority_head_changed", "recovery_authority_not_bound", "recovery_gate_invalid", "recovery_chain_invalid", "recovery_format_unsupported"}, err.Error()) {
			return err
		}
		ready = false
		s.mu.Lock()
		if a := s.admissions[t.topic.id]; a != nil {
			a.Cancel()
		}
		if ticket := s.timers[t.topic.id]; ticket != nil {
			ticket.timer.Stop()
			delete(s.timers, t.topic.id)
		}
		r.publishLocked(s)
		s.mu.Unlock()
	}
	s.mu.Lock()
	if t.wake != nil {
		if a := s.admissions[t.topic.id]; a != nil {
			a.Finish(t.wake, turnwait.Result{WaitUntil: t.waitUntil})
		}
	}
	r.publishLocked(s)
	s.mu.Unlock()
	if ready && (pending || t.wake != nil) && c.Mode == "real" && r.state(c.Scene.ID) == s && !s.closed.Load() {
		enabled, err := r.services.Authority.SceneParticipationEnabled(c.Scene)
		if err != nil {
			return err
		}
		_, adapterPresent := r.adapters.Load(c.Scene.AdapterID)
		if enabled && adapterPresent {
			s.mu.Lock()
			if s.timers[t.topic.id] == nil && s.admissions[t.topic.id] != nil {
				r.scheduleLocked(s, t.topic.id)
			}
			r.publishLocked(s)
			s.mu.Unlock()
		}
	}
	if ready && c.Mode == "real" && !s.closed.Load() && r.state(c.Scene.ID) == s {
		enabled, err := r.services.Authority.SceneParticipationEnabled(c.Scene)
		if err != nil {
			return err
		}
		_, adapterPresent := r.adapters.Load(c.Scene.AdapterID)
		if enabled && adapterPresent {
			s.mu.Lock()
			for _, q := range s.queue.Entries() {
				r.scheduleLocked(s, q.Key)
			}
			r.publishLocked(s)
			s.mu.Unlock()
		}
	}
	if ready && c.Mode == "real" && t.settings.TopicAdmissionEnabled && r.state(c.Scene.ID) == s && !s.closed.Load() {
		if err := r.drainDeferred(s); err != nil {
			return err
		}
	}
	if c.Mode == "real" && ready && r.state(c.Scene.ID) == s && !s.closed.Load() {
		s.mu.Lock()
		if !t.settings.CacheEnabled && len(s.timers) == 0 {
			r.clearBodiesLocked(s)
		} else {
			r.trimLocked(s, false)
		}
		r.publishLocked(s)
		s.mu.Unlock()
	}
	return nil
}
