package botruntime

import (
	"context"
	"github.com/killerprojecte/HumanEngine/backend/internal/dispatch"
	"github.com/killerprojecte/HumanEngine/backend/internal/replyplan"
	"sync"
)

type GroundingError struct{ Code string }

func (e *GroundingError) Error() string { return e.Code }

type sendGuard struct {
	plan                  *Object
	grounding             *Grounding
	recipients            []string
	projection            *replyplan.ContextProjection
	decision              *Object
	admitted              map[string]bool
	ordinarySources       []*Object
	supportingQuotes      []*Object
	quoteReferences       map[string]string
	evidence              []*Object
	extensionDependencies []string
	draft                 *retainedDraft
	deliveryStarted       bool
	mu                    sync.Mutex
}

func receiptsValue(receipts []dispatch.Receipt) []any {
	a := []any{}
	for _, r := range receipts {
		o := obj("status", string(r.Status))
		if r.MessageID != nil {
			_ = o.Set("messageId", *r.MessageID)
		}
		if r.Detail != nil {
			_ = o.Set("detail", *r.Detail)
		}
		a = append(a, o)
	}
	return a
}
func ptr(s string) *string { return &s }
func segmentsFrom(v any) []dispatch.Segment {
	out := []dispatch.Segment{}
	for _, x := range array(v) {
		s := dispatch.Segment{Parts: rawParts(x)}
		if id, ok := get(x, "replyToId").(string); ok {
			s.ReplyToID = &id
		}
		out = append(out, s)
	}
	return out
}
func segmentsValue(s []dispatch.Segment) []any {
	out := []any{}
	for _, x := range s {
		o := obj("parts", fromRawParts(x.Parts))
		if x.ReplyToID != nil {
			_ = o.Set("replyToId", *x.ReplyToID)
		}
		out = append(out, o)
	}
	return out
}

// Both indexes are maintained under operationMu; turn checks never inspect
// another turn's operations, even when the global source ledger is full.
func (r *Runtime) outcomes(t *turn) []dispatch.Receipt {
	r.operationMu.Lock()
	defer r.operationMu.Unlock()
	entries := t.operations.Entries()
	out := []dispatch.Receipt{}
	for _, e := range entries {
		out = append(out, e.Value.receipts...)
	}
	t.outcomesCollected = true
	if r.closed.Load() {
		for _, e := range entries {
			r.deleteOperationLocked(e.Key)
		}
	}
	return out
}
func (r *Runtime) hasOutcome(t *turn) bool {
	r.operationMu.Lock()
	defer r.operationMu.Unlock()
	return t.outcomeCount > 0
}
func (r *Runtime) deleteOperationLocked(key string) {
	if op, ok := r.operations.Get(key); ok {
		op.owner.outcomeCount -= len(op.receipts)
		op.owner.operations.Delete(key)
		delete(op.owner.state.operationKeys, key)
		if op.previous != nil {
			op.previous.next = op.next
		} else {
			r.operationFirst = op.next
		}
		if op.next != nil {
			op.next.previous = op.previous
		} else {
			r.operationLast = op.previous
		}
		r.operations.Delete(key)
	}
}
func validateSegments(g *sendGuard, segments []dispatch.Segment) error {
	for _, segment := range segments {
		if segment.ReplyToID != nil && *segment.ReplyToID != "" && !contains(stringsOf(get(g.plan, "targetMessageIds")), *segment.ReplyToID) {
			return &GroundingError{"plan_target_mismatch"}
		}
		for _, part := range fromRawParts(segment.Parts) {
			if get(part, "type") == "media" {
				known := false
				for _, asset := range array(get(g.plan, "assets")) {
					known = known || get(asset, "assetId") == get(part, "assetId")
				}
				if !known {
					return &GroundingError{"plan_asset_mismatch"}
				}
			}
			if get(part, "type") == "mention" && !contains(g.recipients, str(get(part, "userId"))) {
				return &GroundingError{"plan_recipient_mismatch"}
			}
		}
	}
	return nil
}
func (r *Runtime) send(t *turn, ctx context.Context, input any, retained ...bool) (observed []dispatch.Receipt, err error) {
	c := t.context
	t.sendMu.Lock()
	g := t.guard
	if g == nil {
		t.sendMu.Unlock()
		return []dispatch.Receipt{{Status: dispatch.Cancelled}}, nil
	}
	parsed, e := schemaValue(get(contracts, "sendSchema"), input)
	if e != nil {
		t.sendMu.Unlock()
		return nil, e
	}
	operationID := str(get(parsed, "operationId"))
	segments := g.grounding.NormalizeFeedback(segmentsFrom(get(parsed, "segments")))
	// Normalized receipts are still checked against the original source SendSchema.
	normalized := obj("operationId", operationID, "segments", segmentsValue(segments))
	if _, e = schemaValue(get(contracts, "sendSchema"), normalized); e != nil {
		t.sendMu.Unlock()
		return nil, e
	}
	if e = validateSegments(g, segments); e != nil {
		t.sendMu.Unlock()
		return nil, e
	}
	if failure := g.grounding.Check(segments); failure != "" {
		t.sendMu.Unlock()
		return nil, &GroundingError{failure}
	}
	key := c.Scene.ID + "|" + c.TurnID + "|" + operationID
	r.operationMu.Lock()
	previous, duplicate := r.operations.Get(key)
	r.operationMu.Unlock()
	if duplicate {
		t.sendMu.Unlock()
		return append([]dispatch.Receipt{}, previous.receipts...), nil
	}
	if g.deliveryStarted {
		t.sendMu.Unlock()
		return []dispatch.Receipt{{Status: dispatch.Cancelled, Detail: ptr("turn_delivery_already_started")}}, nil
	}
	g.deliveryStarted = true
	coreDraft := len(retained) > 0 && retained[0]
	if coreDraft {
		r.retainDraft(t, g, operationID, segments, coreDraft)
		c.stage.Store(nil)
		ctx = c.root
	}
	var typing dispatch.TypingDelay
	var typingLife context.Context
	if c.Mode != "simulation" {
		typing, typingLife = r.typingDelay()
	}
	if typingLife != nil {
		// The exact generation owns this whole send, including time spent
		// between segment waits. Retiring it does not abort the whole turn.
		operation, cancelOperation := context.WithCancelCause(ctx)
		stop := context.AfterFunc(typingLife, func() { cancelOperation(context.Cause(typingLife)) })
		defer func() { stop(); cancelOperation(nil) }()
		if typingLife.Err() != nil {
			cancelOperation(context.Cause(typingLife))
		}
		ctx = operation
	}
	r.operationMu.Lock()
	if !r.closed.Load() {
		op := &receiptOperation{key: key, owner: t, receipts: []dispatch.Receipt{}}
		op.previous = r.operationLast
		if r.operationLast != nil {
			r.operationLast.next = op
		} else {
			r.operationFirst = op
		}
		r.operationLast = op
		r.operations.Set(key, op)
		t.operations.Set(key, op)
		t.state.operationKeys[key] = true
	}
	r.operationMu.Unlock()
	bound := []*string{}
	for _, segment := range segments {
		if segment.ReplyToID != nil && g.grounding.IsFeedbackTarget(*segment.ReplyToID) {
			bound = append(bound, segment.ReplyToID)
		} else {
			bound = append(bound, nil)
		}
	}
	t.sendMu.Unlock()
	appendReceipt := func(receipt dispatch.Receipt) {
		observed = append(observed, receipt)
		r.operationMu.Lock()
		if current, exists := r.operations.Get(key); exists {
			current.receipts = append(current.receipts, receipt)
			t.outcomeCount++
		}
		r.operationMu.Unlock()
	}
	reset := func() {
		t.sendMu.Lock()
		g.deliveryStarted = false
		t.sendMu.Unlock()
		r.operationMu.Lock()
		r.deleteOperationLocked(key)
		r.operationMu.Unlock()
	}
	defer func() {
		if r.config().TopicAdmissionEnabled {
			protect := false
			sent := false
			for _, receipt := range observed {
				sent = sent || receipt.Status == dispatch.Sent
				protect = protect || receipt.Status == dispatch.Unknown
			}
			if sent {
				protect = protect || err != nil
				for _, receipt := range observed {
					protect = protect || receipt.Status != dispatch.Sent
				}
			}
			if protect {
				t.state.mu.Lock()
				if !t.state.closed.Load() {
					t.state.routing.ProtectedSends.Set(t.topic.id, struct{}{})
					r.publishLocked(t.state)
				}
				t.state.mu.Unlock()
			}
		}
		if err != nil && (c.Signal().Aborted() || typingLife != nil && typingLife.Err() != nil) && len(observed) == 0 {
			appendReceipt(dispatch.Receipt{Status: dispatch.Cancelled, Detail: ptr("context_cancelled")})
			err = nil
		}
	}()
	r.extensionMu.RLock()
	hooks := append([]registeredHook{}, r.hooks...)
	r.extensionMu.RUnlock()
	for _, hook := range hooks {
		if valid, err := r.eligible(c); err != nil {
			return observed, err
		} else if !valid {
			appendReceipt(dispatch.Receipt{Status: dispatch.Cancelled})
			return observed, nil
		}
		if typingLife != nil && typingLife.Err() != nil {
			appendReceipt(dispatch.Receipt{Status: dispatch.Cancelled, Detail: ptr("context_cancelled")})
			return observed, nil
		}
		hookContext := ctx
		var phase *stage
		var stopHook func() bool
		if coreDraft {
			phase = r.newPhase(t, t.settings.ReplyerTimeoutMS, true)
			hookContext = phase
			if typingLife != nil {
				stopHook = context.AfterFunc(ctx, func() { phase.abort(context.Cause(ctx)) })
				if ctx.Err() != nil {
					phase.abort(context.Cause(ctx))
				}
			}
		}
		result, e := trackedWithin(r, hookContext, func(settle func(HookResult, error)) { hook.Run(hookContext, cloneSegments(segments), c, settle) })
		if stopHook != nil {
			stopHook()
		}
		if phase != nil {
			phase.close()
		}
		if e != nil {
			return observed, e
		}
		if result.Cancel {
			appendReceipt(dispatch.Receipt{Status: dispatch.Cancelled, Detail: ptr("hook_cancelled")})
			return observed, nil
		}
		value := obj("operationId", operationID, "segments", segmentsValue(result.Segments))
		checked, e := schemaValue(get(contracts, "sendSchema"), value)
		if e != nil {
			return observed, e
		}
		segments = segmentsFrom(get(checked, "segments"))
	}
	for index, target := range bound {
		if target != nil && (index >= len(segments) || segments[index].ReplyToID == nil || *segments[index].ReplyToID != *target) {
			reset()
			return observed, &GroundingError{"feedback_binding_changed"}
		}
	}
	segments = g.grounding.NormalizeFeedback(segments)
	if e = validateSegments(g, segments); e != nil {
		reset()
		return observed, e
	}
	if failure := g.grounding.Check(segments); failure != "" {
		reset()
		return observed, &GroundingError{failure}
	}
	// Validate only the final post-hook payload, never every search candidate.
	if valid, checkErr := r.outgoingEligible(c, segments); checkErr != nil {
		return observed, checkErr
	} else if !valid {
		appendReceipt(dispatch.Receipt{Status: dispatch.Cancelled, Detail: ptr("send_invalidated")})
		return observed, nil
	}
	// The final post-hook payload, not an individual segment, is the review unit.
	r.retainDraft(t, g, operationID, segments, coreDraft)
	signal := c.Signal()
	if ctx != nil && ctx != c.stage.Load() && ctx != signal {
		combined := dispatch.NewSignal()
		stop1 := context.AfterFunc(signal, func() { combined.Abort(signal.Reason()) })
		stop2 := context.AfterFunc(ctx, func() { combined.Abort(context.Cause(ctx)) })
		defer stop1()
		defer stop2()
		if signal.Aborted() {
			combined.Abort(signal.Reason())
		}
		if ctx.Err() != nil {
			combined.Abort(context.Cause(ctx))
		}
		signal = combined
	}
	adapterAtSend, _ := r.adapters.Load(c.Scene.AdapterID)
	dc := &dispatch.TurnContext{Scene: c.Scene.SceneRef, Mode: c.Mode, TurnID: c.TurnID, Signal: signal}
	services := dispatch.Services{Writer: dispatch.FlowWriterFunc(func(e dispatch.FlowEvent) {
		r.trace(Event{SceneID: c.Scene.ID, TurnID: c.TurnID, Stage: e.Stage, Status: e.Status, Reason: e.Reason, Counts: e.Counts})
	}), EligibleChecked: func(d *dispatch.TurnContext) (bool, error) {
		return r.dispatchRead(func() (bool, error) { return r.eligibleSignal(c, d.Signal) })
	}, IsRecalledChecked: func(_ string, id string) (bool, error) {
		return r.dispatchRead(func() (bool, error) { return r.recalled(t.state, id) })
	}, Adapter: func(id string) *dispatch.Adapter {
		if a, ok := r.adapters.Load(id); ok {
			return a.(*Adapter).Transport
		}
		return nil
	}}
	guard := dispatch.Guard{CheckGrounding: func(s []dispatch.Segment) bool { return g.grounding.Check(s) != "" },
		BeginTransmission: func(parent *dispatch.Signal) (*dispatch.Signal, func()) {
			phase := r.newPhase(t, t.settings.ReplyerTimeoutMS, true)
			stop := context.AfterFunc(parent, func() { phase.abort(code("context_cancelled")) })
			if parent.Aborted() {
				phase.abort(code("context_cancelled"))
			}
			return phase.signal, func() { stop(); phase.close() }
		},
		Validate: func(request dispatch.ValidationRequest, signal *dispatch.Signal) *dispatch.Future[bool] {
			future := dispatch.NewFuture[bool]()
			done, ok := r.work.Child()
			if !ok {
				future.Reject(code("runtime_closed"))
				return future
			}
			go func() {
				defer done()
				valid, e := r.validateSend(t, g, signal)
				if e != nil {
					future.Reject(e)
				} else {
					future.Resolve(valid)
				}
			}()
			return future
		}}
	if typingLife != nil {
		// Actual adapter callbacks recheck after potentially blocking source
		// reads, not just at logical submission or asynchronous cancellation.
		guard.CaptureTransmitGuard = func() func() bool {
			return func() bool { return typingLife.Err() == nil }
		}
	}

	for i, segment := range segments {
		segmentServices := services
		// Capture this exact segment, so a retained asynchronous transmit callback
		// cannot accidentally validate a later segment from the same draft.
		segmentServices.EligibleChecked = func(d *dispatch.TurnContext) (bool, error) {
			return r.dispatchRead(func() (bool, error) {
				if valid, checkErr := r.eligibleSignal(c, d.Signal); checkErr != nil || !valid {
					return valid, checkErr
				}
				if typingLife != nil && typingLife.Err() != nil {
					return false, nil
				}
				return r.outgoingEligible(c, []dispatch.Segment{segment})
			})
		}
		receipt, failure := dispatch.DispatchSegment(segmentServices, dc, segment, operationID, guard, typing, i, len(segments))
		if failure != nil {
			return observed, failure
		}
		appendReceipt(receipt)
		if receipt.Status == dispatch.Sent && c.Mode == "real" {
			r.emitPluginEvent(obj("type", "message.sent", "version", float64(1), "scene", sceneEventValue(c.Scene), "turnId", c.TurnID, "segment", segmentsValue([]dispatch.Segment{segment})[0], "result", receiptsValue([]dispatch.Receipt{receipt})[0]))
		}
		s := t.state
		var reservations []*SentReservation
		var mediaReservations []*SegmentSentReservation
		published := false
		s.mu.Lock()
		func() {
			defer func() {
				s.mu.Unlock()
				for _, reservation := range mediaReservations {
					reservation.Release()
				}
				// Complete reservations even if publication panics. No callback
				// can run until scene ownership has been released.
				for _, reservation := range reservations {
					if published {
						reservation.Release()
					} else {
						reservation.Cancel()
					}
				}
			}()
			if receipt.Status == dispatch.Sent && c.Mode == "real" {
				for _, extension := range r.exts() {
					for _, sequence := range extension.SegmentSentSequencers {
						if sequence != nil {
							mediaReservations = append(mediaReservations, sequence.reserve(r, c.Scene, c.TurnID, segment, receipt))
						}
					}
				}
			}
			if !s.closed.Load() {
				if r.config().TopicAdmissionEnabled {
					adapterNow, _ := r.adapters.Load(c.Scene.AdapterID)
					if receipt.Status == dispatch.Sent && receipt.MessageID != nil && adapterNow == adapterAtSend && !signal.Aborted() {
						s.routing.Acknowledge(*receipt.MessageID, t.topic.id)
					}
					if receipt.Status == dispatch.Unknown || (receipt.Status != dispatch.Sent && anySent(observed)) {
						s.routing.ProtectedSends.Set(t.topic.id, struct{}{})
					}
				}
				if receipt.Status == dispatch.Sent {
					var id string
					if receipt.MessageID != nil {
						id = *receipt.MessageID
					} else {
						id = newID()
					}
					exists := s.byID[id] != nil
					if !exists {
						now := iso(r.services.Clock.Now())
						origin := "live"
						if c.Mode == "simulation" {
							origin = "simulation"
						}
						m := obj("id", id, "sceneId", c.Scene.ID, "senderId", "bot", "sentAt", now, "receivedAt", now, "origin", origin, "parts", fromRawParts(segment.Parts), "status", "available")
						if segment.ReplyToID != nil {
							_ = m.Set("replyToId", *segment.ReplyToID)
						}
						var managed *displayMediaScene
						if c.Mode == "real" {
							// A scene may have been remapped while this physical send
							// was pending. The receipt belongs to its dispatch target.
							managed = bindManagedReceipt(c.Scene, m)
						}
						r.appendBoundMessageLocked(s, m, managed)
					}
				}
				if receipt.Status == dispatch.Sent && c.Mode == "real" && receipt.MessageID != nil {
					for _, extension := range r.exts() {
						for _, sequence := range extension.SentSequencers {
							if sequence != nil {
								reservations = append(reservations, sequence.reserve(c.Scene, c.TurnID, *receipt.MessageID))
							}
						}
					}
				}
				r.publishLocked(s)
				published = true
			}
		}()
		if receipt.Status == dispatch.Sent && c.Mode == "real" {
			r.lastReply.Store(int64(r.services.Clock.Now()))
			if receipt.MessageID != nil {
				for _, extension := range r.exts() {
					if extension.Sent != nil {
						extension.Sent(c.Scene, c.TurnID, *receipt.MessageID)
					}
				}
			}
			r.trace(Event{SceneID: c.Scene.ID, TurnID: c.TurnID, Stage: "message.sent", Status: "completed", Reason: "segment_delivered"})
		}
		if receipt.Status != dispatch.Sent {
			break
		}
	}
	r.operationMu.Lock()
	if r.operations.Len() > 1000 {
		r.deleteOperationLocked(r.operationFirst.key)
	}
	r.operationMu.Unlock()
	return observed, nil
}
func anySent(r []dispatch.Receipt) bool {
	for _, x := range r {
		if x.Status == dispatch.Sent {
			return true
		}
	}
	return false
}
func cloneSegments(segments []dispatch.Segment) []dispatch.Segment {
	return segmentsFrom(segmentsValue(segments))
}
