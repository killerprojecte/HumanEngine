package botruntime

import (
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/promptcache"
	"github.com/killerprojecte/HumanEngine/backend/internal/replyplan"
	"math"
)

func (r *Runtime) participateTurn(t *turn, f *turnFrame, issues *int) error {
	c, s, cfg := t.context, t.state, t.settings
	authority := f.authority
	admitted := f.admitted
	window := f.window
	participationKey := f.participationKey
	recent := f.recent
	manualContext := f.manualContext
	projection := f.projection
	imageContext := f.imageContext
	extensionDependencies := f.extensionDependencies
	decisionTicket, err := r.prepareTicket(participationKey, recent, projection, []any{authority, manualContext}, window.Enabled && r.services.PromptBreakpoints != nil && r.services.PromptBreakpoints("planner"), false)
	if err != nil {
		return err
	}
	decisionStage := r.newStage(t, "planner")
	f.stages = append(f.stages, decisionStage)
	decisionMessages, err := r.decisionMessages(t, projection, recent, manualContext, imageContext, decisionTicket)
	if err != nil {
		return err
	}
	completion, err := r.completion(t, decisionStage, "planner", "decision", decisionMessages, []*Object{contractTool("participation.decide")})
	if err != nil {
		return r.stageError(t, decisionStage, "planner", err)
	}
	var decision *Object
	for _, call := range completion.ToolCalls {
		if call.Name == "participation.decide" {
			args, e := projection.RestoreArguments(call.Arguments)
			if e != nil {
				return e
			}
			decision, err = parseDecision(args)
			if err == nil && codePoints(str(get(args, "reasonCode"))) > 200 {
				r.trace(Event{SceneID: c.Scene.ID, Level: "warn", Module: "runtime", Code: "planner_reason_omitted_over_limit", Reason: "planner_reason_omitted_over_limit"})
			}
			break
		}
	}
	if err != nil {
		return err
	}
	if decision == nil {
		return code("planner_missing_decision")
	}
	action := str(get(decision, "action"))
	if action != "reply" && decisionTicket != nil && r.eligible(c) {
		decisionTicket.Commit()
	}
	if action == "wait" {
		wait := cfg.MaxWaitMS
		if n, ok := get(decision, "waitForMs").(float64); ok {
			wait = n
		}
		until := r.services.Clock.Now() + wait
		t.waitUntil = &until
	}
	if action != "reply" || !r.eligible(c) {
		t.result.Status = "skipped"
		t.result.Reason = "decision_" + action
		if c.root.Aborted() {
			t.result.Status = "cancelled"
			t.result.Reason = "context_cancelled"
		}
		return nil
	}
	decisionTicket.Commit()
	targets := stringsOf(get(decision, "targetMessageIds"))
	for _, id := range targets {
		if !contains(ids(recent), id) {
			return code("planner_unknown_target")
		}
		if admitted != nil && !r.admitted(t, id, admitted, false) {
			return code("planner_target_outside_admitted_topic")
		}
	}
	replyerRecent, selected, err := r.reassign(t, decision, recent, extensionDependencies)
	if err != nil {
		return err
	}
	plannerKey, _ := jsjson.Stringify([]any{c.Mode, c.Scene.ID, t.topic.id, "planner"})
	if window.Enabled {
		s.mu.Lock()
		owned := append([]string{}, t.topic.messageIDs...)
		for _, e := range s.routing.Acknowledged.Entries() {
			if e.Value == t.topic.id {
				owned = append(owned, e.Key)
			}
		}
		for _, m := range s.messages {
			if get(m, "senderId") == "bot" && contains(owned, str(get(m, "replyToId"))) {
				owned = append(owned, idOf(m))
			}
		}
		for body, record := range s.byBody {
			t.records[body] = record
		}
		offered := ids(replyerRecent)
		cutoff := r.services.Clock.Now() - r.lifetime(cfg)
		replyerRecent = filterMessages(s.messages, func(m *Object) bool {
			id := idOf(m)
			_, deferred := s.routing.Deferred.Get(id)
			return get(m, "status") == "available" && !s.recalled[id] && (c.Mode == "simulation" || s.byBody[m].receivedAt >= cutoff) && (admitted == nil || admitted[id] && !deferred) && (contains(offered, id) || contains(owned, id)) && (selected == nil || selected[id] || selected[str(get(m, "replyToId"))])
		})
		s.mu.Unlock()
	}
	replyerRecent = filterMessages(replyerRecent, func(m *Object) bool { return !r.recalled(s, idOf(m)) })
	replyerRecent, err = r.selectWindow(plannerKey, replyerRecent, window, authority, targets)
	if err != nil {
		return err
	}
	for _, id := range targets {
		if !contains(ids(replyerRecent), id) {
			return code("prompt_context_target_budget_exhausted")
		}
	}
	c.setDependencies(extensionDependencies, replyerRecent)
	c.setSources(replyerRecent)
	if cfg.PromptContextWindowEnabled {
		projection = replyplan.NewContextProjection(c.Scene.ID)
		for _, m := range replyerRecent {
			if _, err := projection.Project(obj("evidenceId", "message:"+idOf(m), "value", m)); err != nil {
				return err
			}
		}
	}
	f.decision = decision
	f.targets = targets
	f.replyerRecent = replyerRecent
	f.plannerKey = plannerKey
	f.projection = projection
	f.proceed = true
	return nil
}
func (r *Runtime) decisionMessages(t *turn, p *replyplan.ContextProjection, recent []*Object, manual string, images []any, ticket *promptcache.Ticket) ([]*Object, error) {
	cfg := t.settings
	s := t.state
	now := r.services.Clock.Now()
	hour, err := cfg.Hour(now, cfg.Timezone)
	if err != nil {
		return nil, err
	}
	inclination := "balanced"
	for _, w := range cfg.ParticipationWindows {
		if hour >= w.StartHour && hour < w.EndHour {
			inclination = w.Inclination
			break
		}
	}
	topics := []any{}
	s.mu.Lock()
	for _, e := range s.topics.Entries() {
		topic := e.Value
		topics = append(topics, obj("id", topic.id, "messageIds", stringArray(suffix(topic.messageIDs, 24)), "attention", r.attention(topic), "ageMs", now-topic.lastAt))
	}
	s.mu.Unlock()
	v := s.view.Load()
	localTopics := []any{}
	for _, topic := range v.topics {
		localTopics = append(localTopics, obj("id", topic.ID, "attention", topic.Attention*math.Exp(-(now-topic.LastAt)/300000), "ageMs", math.Max(0, now-topic.LastAt), "messageCount", float64(topic.MessageCount)))
	}
	var since any
	if last := r.lastReply.Load(); last != 0 {
		since = math.Floor((now-float64(last))/1000 + .5)
	}
	var localSince any
	for _, record := range bodyRecords(v.history) {
		m := record.body
		if get(m, "senderId") == "bot" && get(m, "status") == "available" {
			localSince = math.Max(0, math.Floor((now-record.receivedAt)/1000+.5))
		}
	}
	s.mu.Lock()
	provisional := 0
	for _, e := range s.routing.Metadata.Entries() {
		if e.Value.Provisional {
			provisional++
		}
	}
	overflowed := s.routing.Overflowed
	s.mu.Unlock()
	localState := obj("activeTasks", float64(v.active), "topicRouting", obj("enabled", r.config().TopicAdmissionEnabled, "deferred", float64(v.deferred), "overflowed", overflowed, "provisional", float64(provisional), "maxTopics", float64(4), "maxDeferred", float64(60)), "secondsSinceBotSpoke", localSince, "topics", localTopics)
	payload := obj("sceneKind", t.context.Scene.Kind, "botUserId", "bot", "now", iso(now), "timezone", cfg.Timezone, "proactive", t.proactive, "participationInclination", inclination, "topics", topics, "localState", localState, "globalState", obj("secondsSinceBotSpoke", since), "messages", objects(recent), "imageContext", images)
	text, err := p.StringifyTrailing(payload, []string{"topics", "localState", "globalState", "now"})
	if err != nil {
		return nil, err
	}
	user := obj("role", "user", "content", text)
	r.layout(user, "messages", cfg, ticket)
	return []*Object{obj("role", "system", "content", cfg.Persona+"\n"+manual+"\n"+prompt("decision")), user}, nil
}
