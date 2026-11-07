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
	plan               *Object
	grounding          *Grounding
	recipients         []string
	projection         *replyplan.ContextProjection
	decision           *Object
	admitted           map[string]bool
	version, validated uint64
	deliveryStarted    bool
	mu                 sync.Mutex
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
	out := []dispatch.Receipt{}
	for _, e := range t.operations.Entries() {
		out = append(out, e.Value.receipts...)
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
func (r *Runtime) send(t *turn, ctx context.Context, input any) (observed []dispatch.Receipt, err error) {
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
		if err != nil && c.Signal().Aborted() && len(observed) == 0 {
			appendReceipt(dispatch.Receipt{Status: dispatch.Cancelled, Detail: ptr("context_cancelled")})
			err = nil
		}
	}()
	r.extensionMu.RLock()
	hooks := append([]Hook{}, r.hooks...)
	r.extensionMu.RUnlock()
	for _, hook := range hooks {
		if valid, err := r.eligible(c); err != nil {
			return observed, err
		} else if !valid {
			appendReceipt(dispatch.Receipt{Status: dispatch.Cancelled})
			return observed, nil
		}
		result, e := replyplan.WithinSignal(ctx, func(settle func(HookResult, error)) { hook.Run(ctx, cloneSegments(segments), c, settle) })
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
		return r.eligibleSignal(c, d.Signal)
	}, IsRecalledChecked: func(_ string, id string) (bool, error) { return r.recalled(t.state, id) }, Adapter: func(id string) *dispatch.Adapter {
		if a, ok := r.adapters.Load(id); ok {
			return a.(*Adapter).Transport
		}
		return nil
	}}
	guard := dispatch.Guard{CheckGrounding: func(s []dispatch.Segment) bool { return g.grounding.Check(s) != "" }, CaptureTransmitGuard: func() func() bool {
		g.mu.Lock()
		version := g.validated
		g.mu.Unlock()
		return func() bool { return t.topic.version.Load() == version }
	}, Validate: func(request dispatch.ValidationRequest, _ *dispatch.Signal) *dispatch.Future[bool] {
		if t.topic.version.Load() == g.version {
			version := t.topic.version.Load()
			valid, err := r.eligible(c)
			if err != nil {
				future := dispatch.NewFuture[bool]()
				future.Reject(err)
				return future
			}
			valid = valid && t.topic.version.Load() == version
			if valid {
				g.mu.Lock()
				g.validated = version
				g.mu.Unlock()
			}
			return dispatch.Resolved(valid)
		}
		future := dispatch.NewFuture[bool]()
		go func() {
			valid, e := r.validateSend(t, g, request)
			if e != nil {
				future.Reject(e)
			} else {
				future.Resolve(valid)
			}
		}()
		return future
	}}
	typing := r.services.TypingDelay
	if c.Mode == "simulation" {
		typing = nil
	}
	for i, segment := range segments {
		receipt, failure := dispatch.DispatchSegment(services, dc, segment, operationID, guard, typing, i, len(segments))
		if failure != nil {
			return observed, failure
		}
		appendReceipt(receipt)
		s := t.state
		s.mu.Lock()
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
				id := newID()
				if receipt.MessageID != nil {
					id = *receipt.MessageID
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
					r.appendMessageLocked(s, m)
				}
			}
			r.publishLocked(s)
		}
		s.mu.Unlock()
		if receipt.Status == dispatch.Sent && c.Mode == "real" {
			r.lastReply.Store(int64(r.services.Clock.Now()))
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
func (r *Runtime) validateSend(t *turn, g *sendGuard, request dispatch.ValidationRequest) (bool, error) {
	c, s := t.context, t.state
	checkedVersion := t.topic.version.Load()
	s.mu.Lock()
	fresh := filterMessages(s.messages, func(m *Object) bool {
		if get(m, "status") != "available" {
			return false
		}
		if g.admitted == nil {
			return true
		}
		id := idOf(m)
		_, deferred := s.routing.Deferred.Get(id)
		ack, _ := s.routing.Acknowledged.Get(id)
		return !deferred && (contains(t.topic.messageIDs, id) || ack == t.topic.id)
	})
	fresh = append([]*Object{}, suffix(fresh, 24)...)
	s.mu.Unlock()
	text, err := g.projection.Stringify(obj("now", iso(r.services.Clock.Now()), "timezone", t.settings.Timezone, "proposed", obj("operationId", request.OperationID, "segments", segmentsValue(request.Segments)), "newMessages", objects(fresh), "originalTarget", get(g.decision, "targetMessageIds")))
	if err != nil {
		return false, err
	}
	stage := c.stage.Load()
	result, err := r.completion(t, stage, "planner", "validation", []*Object{obj("role", "system", "content", prompt("validation")), obj("role", "user", "content", text)}, []*Object{contractTool("reply.validate")})
	if err != nil {
		return false, err
	}
	valid := false
	for _, call := range result.ToolCalls {
		if call.Name == "reply.validate" {
			valid = get(call.Arguments, "valid") == true
			break
		}
	}
	if !valid || t.topic.version.Load() != checkedVersion {
		c.root.Abort(code("context_changed"))
		if g.admitted == nil {
			s.mu.Lock()
			s.pending[t.topic.id] = true
			s.mu.Unlock()
		}
		return false, nil
	}
	for _, m := range fresh {
		c.AddDependency(idOf(m))
	}
	expected := t.topic.version.Load()
	validNow, err := r.eligible(c)
	if err != nil {
		return false, err
	}
	if !validNow || t.topic.version.Load() != expected {
		return false, nil
	}
	g.mu.Lock()
	g.validated = expected
	g.mu.Unlock()
	return true, nil
}
