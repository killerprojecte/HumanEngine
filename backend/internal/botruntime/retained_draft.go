package botruntime

import (
	"context"
	"github.com/killerprojecte/HumanEngine/backend/internal/dispatch"
)

// retainedDraft belongs to one live turn only. Its UUID is runtime-owned, not
// the model operation ID. The one review boundary seals all segments together;
// later ingress remains in the admission queue, never a restarted review.
type retainedDraft struct {
	id       string
	request  dispatch.ValidationRequest
	targets  []string
	reviewed bool
	narrowed bool
}

func (r *Runtime) retainDraft(t *turn, g *sendGuard, operationID string, segments []dispatch.Segment, narrow bool) {
	references := map[string]bool{}
	referenceOrder := []string{}
	addReference := func(id string) {
		if !references[id] {
			references[id] = true
			referenceOrder = append(referenceOrder, id)
		}
	}
	targets := []string{}
	for _, segment := range segments {
		if segment.ReplyToID != nil && *segment.ReplyToID != "" {
			addReference(*segment.ReplyToID)
			targets = append(targets, *segment.ReplyToID)
		}
	}
	for _, target := range targets {
		if quote := g.quoteReferences[target]; quote != "" {
			addReference(quote)
		}
	}
	cited := map[string]bool{}
	for _, fact := range array(get(g.plan, "facts")) {
		for _, id := range stringsOf(get(fact, "evidenceIds")) {
			cited[id] = true
		}
	}
	for _, asset := range array(get(g.plan, "assets")) {
		cited[str(get(asset, "evidenceId"))] = true
	}
	ordinary := map[string]bool{}
	for _, message := range g.ordinarySources {
		id := idOf(message)
		ordinary[id] = true
		if cited["message:"+id] {
			addReference(id)
		}
	}
	for _, message := range g.supportingQuotes {
		id := idOf(message)
		ordinary[id] = true
		if cited["message:"+id] {
			addReference(id)
		}
	}
	for _, receipt := range g.evidence {
		if !contains([]string{"messages.get", "messages.around", "messages.byUser"}, str(get(receipt, "toolName"))) {
			continue
		}
		for _, message := range array(get(get(receipt, "value"), "items")) {
			if get(message, "sceneId") != t.context.Scene.ID {
				continue
			}
			id := str(get(message, "id"))
			ordinary[id] = true
			if cited[idOf(receipt)] {
				addReference(id)
			}
		}
	}
	c := t.context
	if narrow {
		c.mu.Lock()
		// Trim ordinary evidence only; extensions retain their scoped authorization
		// callbacks and any independently acquired non-context dependencies.
		deps := []string{}
		for _, id := range c.dependencyOrder {
			if ordinary[id] && !references[id] && !contains(g.extensionDependencies, id) {
				delete(c.dependencies, id)
			} else {
				deps = append(deps, id)
			}
		}
		for _, id := range referenceOrder {
			if !c.dependencies[id] {
				c.dependencies[id] = true
				deps = append(deps, id)
			}
		}
		c.dependencyOrder = deps
		c.sources = nil
		c.sourceTimes = nil
		for _, message := range g.ordinarySources {
			if c.dependencies[idOf(message)] {
				c.sources = append(c.sources, message)
				c.sourceTimes = append(c.sourceTimes, t.records[message].receivedAt)
			}
		}
		c.mu.Unlock()
	}
	g.mu.Lock()
	g.draft = &retainedDraft{id: c.TurnID, request: dispatch.ValidationRequest{OperationID: operationID, Segments: cloneSegments(segments)}, targets: targets, narrowed: narrow}
	g.mu.Unlock()
}

func (r *Runtime) validateSend(t *turn, g *sendGuard, signal *dispatch.Signal) (bool, error) {
	c, s := t.context, t.state
	g.mu.Lock()
	draft := g.draft
	if draft == nil {
		g.mu.Unlock()
		return false, code("pending_draft_missing")
	}
	if draft.reviewed {
		g.mu.Unlock()
		return r.eligibleSignal(c, signal)
	}
	draft.reviewed = true
	g.mu.Unlock()
	s.mu.Lock()
	fresh := filterMessages(s.messages, func(m *Object) bool {
		id := idOf(m)
		if get(m, "status") != "available" || get(m, "senderId") == "bot" || t.initialMessageIDs[id] {
			return false
		}
		_, deferred := s.routing.Deferred.Get(id)
		ack, _ := s.routing.Acknowledged.Get(id)
		return !deferred && (contains(t.topic.messageIDs, id) || ack == t.topic.id)
	})
	fresh = append([]*Object{}, suffix(fresh, 24)...)
	s.mu.Unlock()
	if len(fresh) == 0 {
		return r.eligibleSignal(c, signal)
	}
	text, err := g.projection.Stringify(obj("now", iso(r.services.Clock.Now()), "timezone", t.settings.Timezone, "draftId", draft.id, "proposed", obj("operationId", draft.request.OperationID, "segments", segmentsValue(draft.request.Segments)), "newMessages", objects(fresh), "originalTarget", get(g.decision, "targetMessageIds")))
	if err != nil {
		return false, err
	}
	review := r.newPhase(t, t.settings.PlannerTimeoutMS, true)
	defer review.close()
	stop := context.AfterFunc(signal, func() { review.abort(code("context_cancelled")) })
	defer stop()
	if signal.Aborted() {
		review.abort(code("context_cancelled"))
	}
	tool := contractTool("reply.cancel")
	schema := get(tool, "inputSchema")
	draftSchema, _ := get(get(schema, "properties"), "draftId").(*Object)
	_ = draftSchema.Set("const", draft.id)
	result, err := r.completion(t, review, "planner", "validation", []*Object{obj("role", "system", "content", prompt("validation")), obj("role", "user", "content", text)}, []*Object{tool})
	if err != nil {
		valid, authorityErr := r.eligibleSignal(c, signal)
		if authorityErr != nil || !valid {
			return valid, authorityErr
		}
		reason := "draft_review_failed"
		if context.Cause(review) != nil {
			reason = "draft_review_timeout"
		}
		r.trace(Event{SceneID: c.Scene.ID, TurnID: c.TurnID, Stage: "validation", Status: "failed", Reason: reason})
		return true, nil
	}
	for _, call := range result.ToolCalls {
		if call.Name == "reply.cancel" && get(call.Arguments, "draftId") == draft.id {
			if _, ok := get(call.Arguments, "reason").(string); ok {
				c.root.Abort(code("draft_cancelled"))
				if g.admitted == nil {
					s.mu.Lock()
					s.pending[t.topic.id] = true
					s.mu.Unlock()
				}
				return false, nil
			}
		}
	}
	return r.eligibleSignal(c, signal)
}
