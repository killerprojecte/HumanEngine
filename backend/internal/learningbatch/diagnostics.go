package learningbatch

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"sync"
	"time"
)

// FlowEvent contains source diagnostic metadata only, never source text, model
// arguments, credentials or error payloads. Observation cannot change results.
type FlowEvent struct {
	Stage, Status, Reason, NextStep, Role, SceneID        string
	TraceID, BatchID, ParentTraceID, SpanID, ParentSpanID string
	LinkedTraceIDs                                        []string
	Counts                                                map[string]int
	DurationMS                                            float64
}
type flowContextKey struct{}

func WithFlow(ctx context.Context, trace, batch, parent string, linked []string) context.Context {
	return context.WithValue(ctx, flowContextKey{}, FlowEvent{TraceID: trace, BatchID: batch, ParentTraceID: parent, LinkedTraceIDs: append([]string{}, linked...)})
}

type span struct {
	mu               sync.Mutex
	b                *batch
	event            FlowEvent
	started          time.Time
	ended, cancelled bool
	stop             func() bool
	callbackDone     chan struct{}
}

func (b *batch) emit(event FlowEvent) {
	if b.r.d.Event != nil {
		defer func() { _ = recover() }()
		b.r.d.Event(event)
	}
}
func flowID() string {
	var id [16]byte
	if _, e := rand.Read(id[:]); e != nil {
		return ""
	}
	return hex.EncodeToString(id[:])
}
func (b *batch) start(stage, role string, counts map[string]int, bind bool) *span {
	parent, _ := b.ctx.Value(flowContextKey{}).(FlowEvent)
	e := FlowEvent{Stage: stage, Role: role, SceneID: b.scene, TraceID: parent.TraceID, BatchID: parent.BatchID, ParentTraceID: parent.ParentTraceID, LinkedTraceIDs: append([]string{}, parent.LinkedTraceIDs...), SpanID: flowID(), ParentSpanID: parent.SpanID, Counts: counts}
	s := &span{b: b, event: e, started: time.Now()}
	s.send("started", "", "", nil)
	if bind {
		s.callbackDone = make(chan struct{})
		s.stop = context.AfterFunc(b.ctx, func() { defer close(s.callbackDone); s.mu.Lock(); defer s.mu.Unlock(); s.cancelLocked() })
	}
	return s
}
func (s *span) send(status, reason, next string, counts map[string]int) {
	e := s.event
	e.Status = status
	e.Reason = reason
	e.NextStep = next
	e.Counts = map[string]int{}
	for k, v := range s.event.Counts {
		e.Counts[k] = v
	}
	for k, v := range counts {
		e.Counts[k] = v
	}
	if status != "started" {
		e.DurationMS = float64(time.Since(s.started)) / float64(time.Millisecond)
	}
	s.b.emit(e)
}
func (s *span) cancelLocked() {
	if !s.ended && !s.cancelled {
		s.cancelled = true
		s.send("cancel_requested", "cancellation_requested", "wait_for_cleanup", nil)
	}
}
func (s *span) finish(status, reason, next string, counts map[string]int) {
	s.mu.Lock()
	if s.ended {
		s.mu.Unlock()
		return
	}
	if s.stop != nil && s.b.ctx.Err() != nil {
		s.cancelLocked()
	}
	s.ended = true
	s.send(status, reason, next, counts)
	s.mu.Unlock()
	if s.stop != nil && !s.stop() {
		<-s.callbackDone
	}
}
func (b *batch) skipped(stage, reason, next string) {
	b.start(stage, "", nil, false).finish("skipped", reason, next, nil)
}
func (b *batch) observedComplete(stage, role, next, countKey string, counts map[string]int, system string, payload *Object, tools []map[string]any) ([]call, error) {
	s := b.start(stage, role, counts, true)
	calls, e := b.complete(system, payload, tools)
	if e != nil {
		status, reason, step := "failed", ModelFailureReason(e), "learning_failure_policy"
		if b.ctx.Err() != nil {
			status, reason, step = "cancelled", "request_cancelled", "stop"
		}
		s.finish(status, reason, step, nil)
	} else if b.ctx.Err() != nil {
		s.finish("cancelled", "request_cancelled", "stop", nil)
	} else {
		s.finish("completed", "", next, map[string]int{countKey: len(calls)})
	}
	return calls, e
}

type proposalResult struct{ status, reason string }

var rejectionReasons = map[string]bool{"simulation_does_not_learn": true, "learning_disabled": true, "missing_source_evidence": true, "invalid_source_evidence": true, "unsupported_personal_pronoun": true, "request_cancelled": true, "invalid_memory_dependencies": true, "invalid_merge_request": true, "manually_deleted": true, "merge_requires_exact_content": true, "merge_not_allowed": true, "correction_not_allowed": true, "correction_depends_on_superseded_memory": true, "cyclic_memory_dependencies": true, "manual_or_invalid_record": true, "incompatible_evidence_scope": true, "invalid_candidate": true, "locked_legacy_candidate": true, "locked_or_invalid_feedback": true, "locked_or_invalidated": true, "not_available": true}

func resultReason(r proposalResult) string {
	if r.status == "saved" {
		return "candidate_saved"
	}
	if rejectionReasons[r.reason] {
		return r.reason
	}
	if r.status == "rejected" {
		return "candidate_rejected"
	}
	return "candidate_unchanged"
}
func (b *batch) observedPersistence(task func() (proposalResult, error)) (proposalResult, error) {
	s := b.start("learning.persist", "", map[string]int{"candidateCount": 1}, true)
	r, e := task()
	if e != nil {
		s.finish("partial", "partial_commit_no_replay", "inspect_partial_learning", map[string]int{"savedCount": 0, "unconfirmedCandidateCount": 1})
	} else if b.ctx.Err() != nil {
		if r.status == "saved" {
			s.finish("partial", "partial_commit_no_replay", "inspect_partial_learning", map[string]int{"savedCount": 1, "unconfirmedCandidateCount": 0})
		} else {
			s.finish("cancelled", "request_cancelled", "stop", nil)
		}
	} else {
		status := "completed"
		saved, rejected := 0, 0
		if r.status == "saved" {
			saved = 1
		}
		if r.status == "rejected" {
			status = "skipped"
			rejected = 1
		}
		s.finish(status, resultReason(r), "next_candidate", map[string]int{"savedCount": saved, "rejectedCount": rejected})
	}
	return r, e
}
