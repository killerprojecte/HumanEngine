package experienceservice

import (
	"context"
	"errors"
	"github.com/killerprojecte/HumanEngine/backend/internal/botruntime"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/recovery"
	"github.com/killerprojecte/HumanEngine/backend/internal/sourceevents"
	"math"
	"reflect"
	"regexp"
	"strings"
	"sync"
	"unicode/utf8"
	"weak"
)

type runtimeTurn struct {
	scene    string
	epoch    uint64
	selected []Hint
	semantic bool
	applied  map[string]bool
	at       int64
}
type exposure struct {
	pending map[weak.Pointer[jsjson.Object]]Outcome
	exposed []Reference
}

// RuntimeHints keeps selection separate from actual delivered tool exposure.
// Its mutex protects only in-memory state, never models, transports or SQL.
type RuntimeHints struct {
	service   *Service
	sources   *sourceevents.Store
	preview   func(string, string, botruntime.ReadAuthority) (*jsjson.Object, error)
	mu        sync.Mutex
	turns     map[string]*runtimeTurn
	order     []string
	epochs    map[string]uint64
	exposures map[weak.Pointer[botruntime.TurnContext]]*exposure
}

func NewRuntimeHints(s *Service, sources *sourceevents.Store, preview func(string, string, botruntime.ReadAuthority) (*jsjson.Object, error)) (*RuntimeHints, error) {
	if s == nil || sources == nil || preview == nil {
		return nil, errors.New("experience_runtime_dependencies_required")
	}
	// Reject cross-owner composition before any outcome can be exposed.
	if e := sources.WithRuntimeReads(context.Background(), func(reads botruntime.ReadAuthority) error {
		return sources.UseRuntimeReadScope(reads, func(scope sourceevents.RuntimeReadScope) error { return scope.CheckBinding(s.main, s.authority) })
	}); e != nil {
		return nil, e
	}
	return &RuntimeHints{service: s, sources: sources, preview: preview, turns: map[string]*runtimeTurn{}, epochs: map[string]uint64{}, exposures: map[weak.Pointer[botruntime.TurnContext]]*exposure{}}, nil
}
func runtimeScope(c *botruntime.TurnContext) Context {
	return Context{SceneID: c.Scene.ID, Simulation: c.Mode == "simulation"}
}
func (r *RuntimeHints) LearningPaused(scene string) { r.mu.Lock(); r.epochs[scene]++; r.mu.Unlock() }
func (r *RuntimeHints) epoch(scene string) uint64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.epochs[scene]
}
func (r *RuntimeHints) exposure(c *botruntime.TurnContext) *exposure {
	for k := range r.exposures {
		if k.Value() == nil {
			delete(r.exposures, k)
		}
	}
	key := weak.Make(c)
	v := r.exposures[key]
	if v == nil {
		v = &exposure{pending: map[weak.Pointer[jsjson.Object]]Outcome{}, exposed: []Reference{}}
		r.exposures[key] = v
	}
	return v
}
func (r *RuntimeHints) scoped(reads botruntime.ReadAuthority, fn func(*RuntimeView) error) error {
	return r.sources.UseRuntimeReadScope(reads, func(scope sourceevents.RuntimeReadScope) error { return r.service.WithRuntimeView(scope, fn) })
}
func hintSnapshot(h Hint) Snapshot {
	return Snapshot{Reference: Reference{ID: h.ID, Version: h.Version}, Hash: h.Hash, FeedbackRevision: h.FeedbackRevision, TrialVersion: h.TrialVersion}
}
func runtimeAborted(c *botruntime.TurnContext) bool { return c.Signal().Aborted() }

var questionSelf = regexp.MustCompile(`(?i)I|my|me`)

func selfQuestion(text string) bool {
	if strings.Contains(text, "我") {
		return true
	}
	word := func(r rune) bool {
		return r == '_' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == 'ſ' || r == 'K'
	}
	for _, at := range questionSelf.FindAllStringIndex(text, -1) {
		before, after := false, false
		if at[0] > 0 {
			ch, _ := utf8.DecodeLastRuneInString(text[:at[0]])
			before = word(ch)
		}
		if at[1] < len(text) {
			ch, _ := utf8.DecodeRuneInString(text[at[1]:])
			after = word(ch)
		}
		if !before && !after {
			return true
		}
	}
	return false
}

var questionQuote = regexp.MustCompile(`(?i)["“”‘’「」『』]|(?:他说|她说|引用|quote)`)
var questionPlan = regexp.MustCompile(`(?i)计划|安排|活动|plan|schedule`)
var questionMention = regexp.MustCompile(`(?i)[?？]|谁|如何|怎样|what|who|how`)

func (r *RuntimeHints) PlannerContext(c *botruntime.TurnContext, messages []*jsjson.Object) (any, error) {
	if c.Mode != "real" || runtimeAborted(c) {
		return nil, nil
	}
	var latest *jsjson.Object
	for i := len(messages) - 1; i >= 0; i-- {
		m := messages[i]
		if prop(m, "status") == "available" && prop(m, "senderId") != "bot" {
			latest = m
			break
		}
	}
	if latest == nil {
		return nil, nil
	}
	texts, mentions := []string{}, []string{}
	parts, _ := prop(latest, "parts").([]any)
	for _, part := range parts {
		p, _ := part.(*jsjson.Object)
		if prop(p, "type") == "text" {
			texts = append(texts, stringValue(prop(p, "text")))
		}
		if prop(p, "type") == "mention" {
			mentions = append(mentions, stringValue(prop(p, "userId")))
		}
	}
	text := strings.Join(texts, " ")
	sender := stringValue(prop(latest, "senderId"))
	scope := runtimeScope(c)
	hints, e := r.service.Hints(context.Background(), scope, HintQuery{RequesterID: sender, PersonID: sender, PersonQuestion: len(mentions) == 0 && strings.ContainsAny(text, "?？") && selfQuestion(text) && !questionQuote.MatchString(text), PlanQuestion: questionPlan.MatchString(text)})
	if e != nil {
		return nil, e
	}
	if len(mentions) == 1 && questionMention.MatchString(text) {
		more, e := r.service.Hints(context.Background(), scope, HintQuery{RequesterID: sender, PersonID: mentions[0], PersonQuestion: true})
		if e != nil {
			return nil, e
		}
		hints = append(hints, more...)
	}
	selected := []Hint{}
	actions := map[string]bool{}
	for _, h := range hints {
		key := encoded([]any{h.Action, h.Condition})
		if actions[key] {
			continue
		}
		actions[key] = true
		selected = append(selected, h)
		if len(selected) == 3 {
			break
		}
	}
	if len(selected) == 0 {
		return nil, nil
	}
	epoch := r.epoch(scope.SceneID)
	now := r.service.ports.Now()
	r.mu.Lock()
	order := []string{}
	for _, id := range r.order {
		if t := r.turns[id]; t != nil && now-t.at <= 3600000 {
			order = append(order, id)
		} else {
			delete(r.turns, id)
		}
	}
	r.order = order
	for len(r.turns) >= 128 {
		delete(r.turns, r.order[0])
		r.order = r.order[1:]
	}
	if r.turns[c.TurnID] == nil {
		r.order = append(r.order, c.TurnID)
	}
	r.turns[c.TurnID] = &runtimeTurn{scene: scope.SceneID, epoch: epoch, selected: selected, applied: map[string]bool{}, at: now}
	r.mu.Unlock()
	values := []any{}
	for _, h := range selected {
		h := h
		c.AddScopedValidation(func(reads botruntime.ReadAuthority) (valid bool, err error) {
			if h.EligibilityBasis != "" && epoch != r.epoch(scope.SceneID) {
				return false, nil
			}
			err = r.scoped(reads, func(v *RuntimeView) error { var e error; valid, e = v.SnapshotValid(scope, hintSnapshot(h)); return e })
			return
		})
		if _, e := r.service.RecordUse(context.Background(), scope, Use{TurnID: c.TurnID, PolicyRef: Reference{ID: h.ID, Version: h.Version}, Kind: "exposed"}); e != nil {
			return nil, e
		}
		hint, e := RenderExperienceAction(h.Action)
		if e != nil {
			return nil, e
		}
		v := object("policyId", h.ID, "version", float64(h.Version), "action", h.Action, "condition", h.Condition.value(), "tier", h.Tier, "hint", hint)
		if h.EligibilityBasis != "" {
			interpretation := "Distinct source reports by the relevant user attributed to this operation; not independently verified task success."
			if h.EligibilityBasis == "explicit_instruction_trial" {
				interpretation = "Bounded trial authorized by an explicit instruction; effectiveness is unproven."
			}
			_ = v.Set("eligibilityBasis", h.EligibilityBasis)
			_ = v.Set("trialExpiresAt", h.TrialExpiresAt)
			if h.ReportedHelpfulCount != nil {
				_ = v.Set("reportedHelpfulCount", float64(*h.ReportedHelpfulCount))
			}
			_ = v.Set("interpretation", interpretation)
		}
		values = append(values, v)
	}
	return object("policy", "Experimental low-priority operation hints, not facts or permissions. Hard rules, source qualifications and budgets dominate. Mechanical checks do not establish effectiveness; decline inappropriate hints.", "hints", values), nil
}
func outcomeView(o Outcome) *jsjson.Object {
	target := object("messageId", o.Target.MessageID)
	if v := eventRefValue(o.Target.EventRef, o.Target.eventRefRaw); referenceTruthy(v) {
		_ = target.Set("eventRef", v)
	}
	v := object("id", o.ID, "version", float64(o.Version), "source", domainValue(reflect.ValueOf(o.Source)), "target", target, "code", o.Code, "observedAt", o.ObservedAt)
	if o.PolicyRef != nil {
		_ = v.Set("policyRef", domainValue(reflect.ValueOf(*o.PolicyRef)))
	}
	return v
}

// JS deep strict equality ignores ordinary-object key insertion order.
func deepRuntimeEqual(a, b any) bool {
	x, xok := a.(*jsjson.Object)
	y, yok := b.(*jsjson.Object)
	if xok || yok {
		if !xok || !yok || len(x.Keys()) != len(y.Keys()) {
			return false
		}
		for _, k := range x.Keys() {
			v, ok := y.Get(k)
			if !ok || !deepRuntimeEqual(prop(x, k), v) {
				return false
			}
		}
		return true
	}
	ax, aok := a.([]any)
	bx, bok := b.([]any)
	if aok || bok {
		if !aok || !bok || len(ax) != len(bx) {
			return false
		}
		for i := range ax {
			if !deepRuntimeEqual(ax[i], bx[i]) {
				return false
			}
		}
		return true
	}
	if x, ok := a.(float64); ok {
		y, ok := b.(float64)
		if !ok {
			return false
		}
		if math.IsNaN(x) && math.IsNaN(y) {
			return true
		}
		return x == y && (x != 0 || math.Signbit(x) == math.Signbit(y))
	}
	return reflect.DeepEqual(a, b)
}
func (r *RuntimeHints) Outcomes(c *botruntime.TurnContext) ([]any, error) {
	if runtimeAborted(c) {
		return []any{}, nil
	}
	outcomes, e := r.service.ListOutcomes(context.Background(), runtimeScope(c), 20)
	if e != nil {
		return nil, e
	}
	values := []any{}
	r.mu.Lock()
	ex := r.exposure(c)
	for key := range ex.pending {
		if key.Value() == nil {
			delete(ex.pending, key)
		}
	}
	for _, o := range outcomes {
		v := outcomeView(o)
		ex.pending[weak.Make(v)] = *decode[Outcome](encoded(o))
		values = append(values, v)
	}
	r.mu.Unlock()
	if c.MemoryExposureDeferred() {
		return values, nil
	}
	return r.DeliverOutcomes(c, values)
}
func (r *RuntimeHints) DeliverOutcomes(c *botruntime.TurnContext, values []any) ([]any, error) {
	if runtimeAborted(c) {
		return []any{}, nil
	}
	current, e := r.service.ListOutcomes(context.Background(), runtimeScope(c), 20)
	if e != nil {
		return nil, e
	}
	byID := map[string]Outcome{}
	for _, o := range current {
		byID[o.ID] = o
	}
	r.mu.Lock()
	ex := r.exposure(c)
	accepted := []*jsjson.Object{}
	for _, value := range values {
		v, ok := value.(*jsjson.Object)
		if !ok || v == nil {
			r.mu.Unlock()
			return []any{}, nil
		}
		prepared, ok := ex.pending[weak.Make(v)]
		present, exists := byID[prepared.ID]
		if !ok || !exists || !deepRuntimeEqual(v, outcomeView(prepared)) || !deepRuntimeEqual(domainValue(reflect.ValueOf(present)), domainValue(reflect.ValueOf(prepared))) {
			r.mu.Unlock()
			return []any{}, nil
		}
		accepted = append(accepted, v)
	}
	fresh := []*jsjson.Object{}
	for _, v := range accepted {
		id := stringValue(prop(v, "id"))
		seen := false
		for _, ref := range ex.exposed {
			if ref.ID == id {
				seen = true
				break
			}
		}
		if !seen {
			ex.exposed = append(ex.exposed, Reference{ID: id, Version: int64(prop(v, "version").(float64))})
			fresh = append(fresh, v)
		}
	}
	r.mu.Unlock()
	for _, v := range fresh {
		source, _ := prop(v, "source").(*jsjson.Object)
		target, _ := prop(v, "target").(*jsjson.Object)
		c.AddDependency(stringValue(prop(source, "messageId")))
		c.AddDependency(stringValue(prop(target, "messageId")))
		v := v
		c.AddScopedValidation(func(reads botruntime.ReadAuthority) (valid bool, err error) {
			err = r.scoped(reads, func(view *RuntimeView) error {
				rows, e := view.ListOutcomes(runtimeScope(c), 20)
				if e != nil {
					return e
				}
				for _, o := range rows {
					if deepRuntimeEqual(outcomeView(o), v) {
						valid = true
						break
					}
				}
				return nil
			})
			return
		})
	}
	return values, nil
}

// withTurnWriter validates and writes on one native owner, without holding the
// runtime map lock. The read lease ends before a service savepoint is opened.
func (r *RuntimeHints) withTurnWriter(c *botruntime.TurnContext, fn func(*Service) error) (bool, error) {
	valid := false
	err := r.service.TransactionWithSession(context.Background(), func(bound *Service, ws *recovery.WriteSession) error {
		if e := r.sources.WithSession(ws).WithRuntimeReads(context.Background(), func(reads botruntime.ReadAuthority) error {
			var e error
			valid, e = c.ValidateMemoryContextWith(reads)
			return e
		}); e != nil {
			return e
		}
		if !valid || runtimeAborted(c) {
			valid = false
			return nil
		}
		return fn(bound)
	})
	return valid, err
}
func (r *RuntimeHints) Propose(c *botruntime.TurnContext, input any) (any, error) {
	var result ProposalResult
	valid, e := r.withTurnWriter(c, func(bound *Service) error {
		p, e := parseRuntimeProposal(input)
		if e != nil {
			return e
		}
		r.mu.Lock()
		refs := append([]Reference{}, r.exposure(c).exposed...)
		r.mu.Unlock()
		result, e = bound.ProposePolicy(context.Background(), runtimeScope(c), p, refs)
		return e
	})
	if e != nil {
		return nil, e
	}
	if !valid {
		return nil, errors.New("experience_context_invalid")
	}
	return object("status", "candidate", "candidateId", result.Candidate.ID, "duplicate", result.Duplicate, "active", result.Candidate.Status == "active", "validationTier", "mechanical_only"), nil
}
func (r *RuntimeHints) DeliveredTool(c *botruntime.TurnContext, name string, args any, value any) error {
	r.mu.Lock()
	turn := r.turns[c.TurnID]
	exists := turn != nil && turn.scene == c.Scene.ID
	r.mu.Unlock()
	if !exists {
		return nil
	}
	applied := []string{}
	_, err := r.withTurnWriter(c, func(bound *Service) error {
		r.mu.Lock()
		if name == "memory.search" {
			turn.semantic = true
		}
		semantic := turn.semantic
		selected := append([]Hint{}, turn.selected...)
		r.mu.Unlock()
		rows, ok := value.([]any)
		if !ok || len(rows) == 0 {
			return nil
		}
		a := map[string]any{}
		if o, ok := args.(*jsjson.Object); ok {
			for _, k := range o.Keys() {
				a[k] = prop(o, k)
			}
		}
		for _, h := range selected {
			operation := AppliedExperienceOperation(OperationInput{Action: h.Action, Condition: h.Condition, Tool: name, Arguments: a, Value: value, SemanticRead: semantic, TurnValid: true})
			if operation == "" {
				continue
			}
			ok, e := bound.RecordUse(context.Background(), runtimeScope(c), Use{TurnID: c.TurnID, PolicyRef: Reference{ID: h.ID, Version: h.Version}, Kind: "applied", Operation: operation})
			if e != nil {
				return e
			}
			if ok {
				applied = append(applied, h.ID)
			}
		}
		return nil
	})
	if err == nil {
		r.mu.Lock()
		for _, id := range applied {
			turn.applied[id] = true
		}
		r.mu.Unlock()
	}
	return err
}
func (r *RuntimeHints) Sent(scene, turnID, target string) error {
	return r.sentContext(context.Background(), scene, turnID, target)
}
func (r *RuntimeHints) sentContext(ctx context.Context, scene, turnID, target string) error {
	r.mu.Lock()
	turn := r.turns[turnID]
	if turn == nil || turn.scene != scene {
		r.mu.Unlock()
		return nil
	}
	selected := append([]Hint{}, turn.selected...)
	epoch := turn.epoch
	applied := map[string]bool{}
	for k, v := range turn.applied {
		applied[k] = v
	}
	r.mu.Unlock()
	return r.service.TransactionWithSession(ctx, func(bound *Service, _ *recovery.WriteSession) error {
		scope := Context{SceneID: scene}
		for _, h := range selected {
			if h.EligibilityBasis != "" && epoch != r.epoch(scene) {
				continue
			}
			valid, e := bound.SnapshotValid(ctx, scope, hintSnapshot(h))
			if e != nil {
				return e
			}
			if !valid {
				continue
			}
			u := Use{TurnID: turnID, PolicyRef: Reference{ID: h.ID, Version: h.Version}, Kind: "exposed", TargetMessageID: target}
			if _, e = bound.RecordUse(ctx, scope, u); e != nil {
				return e
			}
			if applied[h.ID] {
				u.Kind = "applied"
				u.Operation = "plan_qualifiers_checked"
				if h.Action == "exact_person_before_semantic" {
					u.Operation = "memory_person"
				}
				if _, e = bound.RecordUse(ctx, scope, u); e != nil {
					return e
				}
			}
		}
		return nil
	})
	// A transport ACK does not establish conclusion-first expression ordering.
}
func (r *RuntimeHints) Received(message *jsjson.Object) error {
	reply := stringValue(prop(message, "replyToId"))
	if reply == "" {
		return nil
	}
	return r.service.TransactionWithSession(context.Background(), func(bound *Service, ws *recovery.WriteSession) error {
		scene := stringValue(prop(message, "sceneId"))
		scope := Context{SceneID: scene, Simulation: prop(message, "origin") == "simulation"}
		var target *jsjson.Object
		e := r.sources.WithSession(ws).WithRuntimeReads(context.Background(), func(reads botruntime.ReadAuthority) error {
			var e error
			target, e = r.preview(scene, reply, reads)
			return e
		})
		if e != nil || target == nil {
			return e
		}
		uses, e := bound.TargetUses(context.Background(), scope, stringValue(prop(target, "id")))
		if e != nil {
			return e
		}
		selected, applied := []Use{}, []Use{}
		for _, u := range uses {
			if u.Kind == "exposed" {
				selected = append(selected, u)
			}
			if u.Kind == "applied" && (u.Operation == "memory_person" || u.Operation == "plan_qualifiers_checked") {
				applied = append(applied, u)
			}
		}
		var policy *Reference
		var use *AppliedPolicyFeedback
		if len(selected) == 1 {
			policy = &selected[0].PolicyRef
		}
		if len(applied) == 1 {
			use = &AppliedPolicyFeedback{PolicyRef: applied[0].PolicyRef, Operation: applied[0].Operation}
		}
		var m, t FeedbackMessage
		raw, e := jsjson.Marshal(message)
		if e != nil {
			return e
		}
		if e = m.UnmarshalJSON(raw); e != nil {
			return e
		}
		raw, e = jsjson.Marshal(target)
		if e != nil {
			return e
		}
		if e = t.UnmarshalJSON(raw); e != nil {
			return e
		}
		feedback := DeriveExplicitPolicyFeedback(scene, m, &t, policy, use)
		if feedback == nil {
			return nil
		}
		_, e = bound.RecordFeedback(context.Background(), scope, *feedback)
		return e
	})
}
func parseRuntimeProposal(input any) (Proposal, error) {
	bad := errors.New("experience_proposal_invalid")
	o, ok := input.(*jsjson.Object)
	if !ok || o == nil || len(o.Keys()) != 3 {
		return Proposal{}, bad
	}
	for _, k := range o.Keys() {
		if k != "action" && k != "condition" && k != "outcomeRefs" {
			return Proposal{}, bad
		}
	}
	action := stringValue(prop(o, "action"))
	if !contains(mechanicalActions, action) {
		return Proposal{}, bad
	}
	cond, ok := prop(o, "condition").(*jsjson.Object)
	if !ok || cond == nil {
		return Proposal{}, bad
	}
	kind := stringValue(prop(cond, "kind"))
	if kind == "plan_question" {
		if len(cond.Keys()) != 1 {
			return Proposal{}, bad
		}
	} else if kind == "person_question" || kind == "response_style" {
		if len(cond.Keys()) != 2 || !runtimeProposalID(stringValue(prop(cond, "personId"))) {
			return Proposal{}, bad
		}
	} else {
		return Proposal{}, bad
	}
	refs, ok := prop(o, "outcomeRefs").([]any)
	if !ok || len(refs) != 1 {
		return Proposal{}, bad
	}
	ref, ok := refs[0].(*jsjson.Object)
	if !ok || ref == nil || len(ref.Keys()) != 2 || !runtimeProposalID(stringValue(prop(ref, "id"))) {
		return Proposal{}, bad
	}
	version, ok := prop(ref, "version").(float64)
	if !ok || !safeRefNumber(version, 1) {
		return Proposal{}, bad
	}
	return Proposal{Action: action, Condition: Condition{Kind: kind, PersonID: stringValue(prop(cond, "personId")), raw: cond}, OutcomeRefs: []Reference{{ID: stringValue(prop(ref, "id")), Version: int64(version)}}}, nil
}

// Zod 4 maxLength uses Unicode code points, including lone surrogate units.
func runtimeProposalID(s string) bool {
	n, e := jsjson.CodePointCount(s)
	return e == nil && n >= 1 && n <= 512
}
