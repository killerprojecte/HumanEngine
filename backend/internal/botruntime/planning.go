package botruntime

import (
	"context"
	"fmt"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/replyplan"
	"strings"
)

func (r *Runtime) planTurn(t *turn, f *turnFrame, issues *int) error {
	c, cfg := t.context, t.settings
	extensions := f.extensions
	allTools := f.allTools
	authority := f.authority
	admitted := f.admitted
	window := f.window
	manualContext := f.manualContext
	projection := f.projection
	imageContext := f.imageContext
	learned := f.learned
	decision := f.decision
	targets := f.targets
	replyerRecent := f.replyerRecent
	plannerKey := f.plannerKey
	grounding := NewGrounding()
	for _, m := range replyerRecent {
		grounding.AddFeedbackSource(idOf(m), rawParts(m))
		if get(m, "senderId") != "bot" {
			grounding.AddEvidence(get(m, "parts"))
		}
	}
	grounding.AddEvidence(learned)
	deliveries := []*Object{}
	for _, tool := range allTools {
		if get(tool, "name") == "messages.send" || get(tool, "effect") == "send" {
			deliveries = append(deliveries, tool)
		}
	}
	actions := []*Object{}
	for _, tool := range replyplan.PlannerActionTools(allTools) {
		if get(tool, "name") != "learning.propose" || c.Scene.LearningEnabled {
			actions = append(actions, tool)
		}
	}
	reads := replyplan.PlannerReadTools(allTools)
	evidence := []*Object{}
	for _, m := range replyerRecent {
		evidence = append(evidence, obj("id", "message:"+idOf(m), "value", m))
	}
	for i, v := range learned {
		evidence = append(evidence, obj("id", fmt.Sprintf("context:%d", i), "value", v))
	}
	plannerTicket, err := r.prepareTicket(plannerKey, replyerRecent, projection, []any{authority, manualContext, objects(reads)}, window.Enabled, true)
	if err != nil {
		return err
	}
	planningStage := r.newStage(t, "planner")
	f.stages = append(f.stages, planningStage)
	policyContext := []any{}
	plannerContextMessages := replyerRecent
	if len(targets) > 0 {
		plannerContextMessages = filterMessages(replyerRecent, func(m *Object) bool { return contains(targets, idOf(m)) })
	}
	for _, e := range extensions {
		if e.PlannerContextData != nil {
			v, err := e.PlannerContextData(c, plannerContextMessages)
			if err != nil {
				return err
			}
			if truthy(v) {
				policyContext = append(policyContext, v)
			}
		}
	}
	payload := obj("decision", decision, "now", iso(r.services.Clock.Now()), "timezone", cfg.Timezone, "sceneKind", c.Scene.Kind)
	if len(policyContext) > 0 {
		_ = payload.Set("policyContext", policyContext)
	}
	_ = payload.Set("evidence", objects(evidence))
	_ = payload.Set("imageContext", imageContext)
	_ = payload.Set("actionTools", objects(actions))
	_ = payload.Set("deliveryTools", objects(deliveries))
	_ = payload.Set("planContract", obj("targetMessageIds", stringArray(targets), "contextMessageIds", stringArray(ids(replyerRecent))))
	serialized, err := projection.StringifyTrailing(payload, []string{"decision", "policyContext", "planContract", "now"})
	if err != nil {
		return err
	}
	user := obj("role", "user", "content", serialized)
	r.layout(user, "evidence", cfg, plannerTicket)
	template := "plan"
	if cfg.ReplySuggestionsEnabled {
		template = "planSuggestions"
	}
	planningMessages := []*Object{obj("role", "system", "content", cfg.Persona+"\n"+manualContext+"\n"+prompt(template)), user}
	planned, err := r.services.Policy.BuildReplyPlan(planningStage, replyplan.BuildInput{ValidationInput: replyplan.ValidationInput{TargetMessageIDs: targets, ContextMessageIDs: ids(replyerRecent), Evidence: evidence, ActionTools: actions, DeliveryTools: deliveries, ReplySuggestionsEnabled: cfg.ReplySuggestionsEnabled}, Messages: planningMessages, EvidenceMaxBytes: &cfg.PlannerEvidenceMaxBytes, Projection: projection, ReadTools: reads, Eligible: func() bool { return r.eligible(c) }, Complete: func(ctx context.Context, messages, tools []*Object, settle func(replyplan.Completion, error)) {
		if !r.eligible(c) {
			settle(replyplan.Completion{}, code("planner_context_ineligible"))
			return
		}
		r.services.Models.Start(ctx, ModelRequest{"planner", "plan", messages, tools, c, planningStage.progress}, func(out Completion, e error) {
			if e == nil && r.eligible(c) {
				plannerTicket.Commit()
			}
			settle(out.Completion, e)
		})
	}, Deliver: func(name string, value, args any) (any, error) {
		for _, e := range extensions {
			if toolNamed(e.Tools, name) != nil && e.Delivered != nil {
				return e.Delivered(name, value, c, args)
			}
		}
		return value, nil
	}, Execute: func(ctx context.Context, name string, args any, settle func(any, error)) {
		go func() {
			previous := c.deferMemoryExposure.Load()
			c.deferMemoryExposure.Store(true)
			output, e := r.executeTool(t, ctx, name, args)
			c.deferMemoryExposure.Store(previous)
			if e == nil && strings.HasPrefix(name, "messages.") {
				grounding.ObserveCoreHistory(output, c.Scene.ID, c.Mode)
				observations := []any{}
				rows := []*Object{}
				for _, m := range array(get(output, "items")) {
					if o, ok := m.(*Object); ok {
						rows = append(rows, o)
					}
				}
				for _, extension := range extensions {
					if extension.DescribeImages != nil {
						v, err := replyplan.WithinSignal(ctx, func(done func(any, error)) { extension.DescribeImages(ctx, c, rows, done) })
						if err != nil {
							e = err
							break
						}
						if truthy(v) {
							observations = append(observations, v)
						}
					}
				}
				if o, ok := output.(*Object); ok {
					o = o.Clone()
					_ = o.Set("imageContext", observations)
					output = o
				}
				imageContext = append(imageContext, observations...)
			}
			if e == nil {
				grounding.ObserveTool(name, output)
			}
			settle(output, e)
		}()
	}})
	if err != nil {
		return r.stageError(t, planningStage, "planner", err)
	}
	plan := planned.Plan
	if admitted != nil {
		for _, id := range stringsOf(get(plan, "targetMessageIds")) {
			if !r.admitted(t, id, admitted, false) {
				return code("planner_target_outside_admitted_topic")
			}
		}
		c.AddValidation(func() bool {
			for _, id := range stringsOf(get(plan, "targetMessageIds")) {
				if !r.admitted(t, id, admitted, true) {
					return false
				}
			}
			return true
		})
	}
	if !r.eligible(c) {
		t.result.Status = "skipped"
		t.result.Reason = "context_cancelled"
		return nil
	}
	actionReceipts := []*Object{}
	executed := map[string]bool{}
	for _, action := range array(get(plan, "actions")) {
		if t.topic.version.Load() != t.version {
			c.root.Abort(code("context_changed"))
			return code("planner_stale_action")
		}
		if err := context.Cause(planningStage); err != nil {
			return err
		}
		if !r.eligible(c) {
			return code("planner_context_ineligible")
		}
		signature, err := jsjson.Stringify(action)
		if err != nil {
			return err
		}
		if executed[signature] {
			return code("planner_duplicate_action")
		}
		executed[signature] = true
		name := str(get(action, "toolName"))
		if toolNamed(actions, name) == nil {
			return code("planner_forbidden_action")
		}
		output, err := r.executeTool(t, planningStage, name, get(action, "arguments"))
		if err != nil {
			return r.stageError(t, planningStage, "planner", err)
		}
		grounding.ObserveTool(name, output)
		actionReceipts = append(actionReceipts, obj("id", fmt.Sprintf("action:%d", len(actionReceipts)+1), "toolName", name, "value", output))
	}
	if !r.eligible(c) {
		return code("planner_context_ineligible")
	}
	if get(plan, "action") == "noReply" {
		t.result.Status = "skipped"
		t.result.Reason = "planner_no_reply"
		return nil
	}
	selectedMessages := append([]*Object{}, replyerRecent...)
	seen := ids(selectedMessages)
	for _, receipt := range planned.Evidence {
		if contains([]string{"messages.get", "messages.around", "messages.byUser"}, str(get(receipt, "toolName"))) {
			for _, v := range array(get(get(receipt, "value"), "items")) {
				m, ok := v.(*Object)
				if ok && get(m, "sceneId") == c.Scene.ID && get(m, "status") == "available" && !contains(seen, idOf(m)) {
					selectedMessages = append(selectedMessages, m)
					seen = append(seen, idOf(m))
				}
			}
		}
	}
	selectedMessages = filterMessages(selectedMessages, func(m *Object) bool { return contains(stringsOf(get(plan, "contextMessageIds")), idOf(m)) })
	recipients := []string{}
	related := []string{}
	for _, m := range selectedMessages {
		recipients = append(recipients, str(get(m, "senderId")))
		if id := str(get(m, "replyToId")); id != "" {
			related = append(related, id)
		}
	}
	t.sendMu.Lock()
	t.guard = &sendGuard{plan: plan, grounding: grounding, recipients: recipients, projection: projection, decision: decision, admitted: admitted, version: t.version, validated: t.version}
	t.sendMu.Unlock()
	brief, err := r.services.Policy.BuildReplyBrief(plan, planned.Evidence, actionReceipts, imageContext, related)
	if err != nil {
		return err
	}
	upstream := []*Object{}
	for _, e := range planned.Evidence {
		coverage := get(e, "coverage")
		if get(coverage, "status") == "limited" && strings.HasPrefix(str(get(e, "toolName")), "memory.") && get(e, "toolName") != "memory.source" {
			upstream = append(upstream, obj("evidenceId", get(e, "id"), "reason", "planner_evidence_budget", "omittedRecords", get(coverage, "omittedRecords")))
		}
	}
	layout := ""
	if cfg.PromptContextWindowEnabled {
		layout = "messages"
	}
	packed := r.services.Policy.PackReplyEvidence(replyplan.PackReplyEvidenceInput{Brief: brief, ContextLayout: layout, UpstreamOmissions: upstream, Envelope: obj("now", iso(r.services.Clock.Now()), "timezone", cfg.Timezone, "sceneKind", c.Scene.Kind, "messages", objects(selectedMessages)), Serialize: func(v replyplan.ReadonlyJSON) (string, error) {
		return v.ProjectedStringify(projection, []string{"now"})
	}, Budget: replyplan.SelectionBudget{Mode: "bytes", Limit: cfg.ReplyEvidenceMaxBytes}})
	status, _ := packed.Get("status").String()
	packedText, hasText := packed.Get("serialized").String()
	if status != "selected" || !hasText {
		return code("reply_evidence_budget_exhausted")
	}
	if !r.eligible(c) {
		return code("planner_context_ineligible")
	}
	for _, e := range extensions {
		if e.ContextSelected != nil {
			b, _ := packed.Get("brief").Copy().(*Object)
			if err := e.ContextSelected(c, b); err != nil {
				return err
			}
		}
	}
	if !r.eligible(c) {
		return code("planner_context_ineligible")
	}
	f.grounding = grounding
	f.plan = plan
	f.deliveries = deliveries
	f.packedText = packedText
	f.proceed = true
	return nil
}
