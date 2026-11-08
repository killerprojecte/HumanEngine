package experienceservice

import (
	"context"
	"crypto/rand"
	"database/sql"
	"errors"
	"fmt"
	"github.com/killerprojecte/HumanEngine/backend/internal/recovery"
	"github.com/killerprojecte/HumanEngine/backend/internal/sourceevents"
	"strconv"
	"time"
)

type Settings struct {
	Enabled, AutoActivate, Hints, FeedbackTrials bool
	Revision                                     *int64
}

// Ports receive the SAME live session as the dependent writes. Implementations
// must use that capability (or immutable configuration), not another connection.
// Required callbacks have no permissive fallback. SourceCurrent is optional,
// matching the source; registered source-event proof is always checked locally.
type Ports struct {
	Compare                  func(string, string) (int, error)
	Settings                 func(*recovery.WriteSession) (Settings, error)
	LearningEnabled          func(*recovery.WriteSession, string) (bool, error)
	SceneAvailable           func(*recovery.WriteSession, string) (bool, error)
	AdmissionSourceAvailable func(*recovery.WriteSession, string, string, string) (bool, error)
	TargetAvailable          func(*recovery.WriteSession, string, string) (bool, error)
	SourceRevoked            func(*recovery.WriteSession, string, string) (bool, error)
	SourceCurrent            func(*recovery.WriteSession, string, string, *string) (bool, error)
	Validate                 func(Policy) (Receipt, error)
	BeforeTrialWrite         func(*recovery.WriteSession, recovery.ExperienceTrial) error
	Now                      func() int64
	ID                       func() string
}
type Service struct {
	main         *sql.DB
	authority    *recovery.Authority
	session      *recovery.WriteSession
	ports        Ports
	bindingError error
}
type call struct {
	store
	owner   *Service
	sources *sourceevents.View
}

func New(ctx context.Context, main *sql.DB, authority *recovery.Authority, p Ports) (*Service, error) {
	if main == nil || authority == nil || p.Settings == nil || p.LearningEnabled == nil || p.SceneAvailable == nil || p.AdmissionSourceAvailable == nil || p.TargetAvailable == nil || p.SourceRevoked == nil || p.Validate == nil || p.Compare == nil {
		return nil, errors.New("experience_ports_required")
	}
	if p.Now == nil {
		p.Now = func() int64 { return time.Now().UnixMilli() }
	}
	if p.ID == nil {
		p.ID = func() string {
			b := make([]byte, 16)
			if _, e := rand.Read(b); e != nil {
				panic(e)
			}
			b[6] = (b[6] & 15) | 64
			b[8] = (b[8] & 63) | 128
			return fmt.Sprintf("%x-%x-%x-%x-%x", b[:4], b[4:6], b[6:8], b[8:10], b[10:])
		}
	}
	s := &Service{main: main, authority: authority, ports: p}
	if e := authority.WithWriteSession(ctx, main, func(ws *recovery.WriteSession) error { return ws.EnsureExperienceStorageSchema() }); e != nil {
		return nil, e
	}
	return s, nil
}
func (s *Service) WithSession(ws *recovery.WriteSession) *Service {
	next := *s
	next.session = ws
	if ws == nil {
		next.bindingError = errors.New("experience_transaction_required")
	} else {
		next.bindingError = ws.CheckBinding(s.authority, s.main)
	}
	return &next
}
func (s *Service) run(ctx context.Context, fn func(*call)) error {
	if s.bindingError != nil {
		return s.bindingError
	}
	work := func(ws *recovery.WriteSession) error {
		return guarded(func() {
			check(ws.CheckBinding(s.authority, s.main))
			check(ws.AssertReady())
			v := take(sourceevents.BorrowReadScope(ws.AssertReady, func(q string, a ...any) (sourceevents.Rows, error) { return ws.Query(q, a...) }))
			fn(&call{store: store{ws}, owner: s, sources: v})
		})
	}
	if s.session != nil {
		return s.session.WithSavepoint(work)
	}
	return s.authority.WithWriteSession(ctx, s.main, work)
}

// TransactionWithSession composes runtime admission and service writes. A
// caller may catch an ordinary error and continue; lost native transactions
// still poison the shared capability and cannot be acknowledged or committed.
func (s *Service) TransactionWithSession(ctx context.Context, fn func(*Service, *recovery.WriteSession) error) error {
	if fn == nil {
		return errors.New("experience_transaction_required")
	}
	return s.run(ctx, func(c *call) { check(fn(s.WithSession(c.session), c.session)) })
}
func (c *call) flags() Settings { return take(c.owner.ports.Settings(c.session)) }
func (c *call) now() int64      { return c.owner.ports.Now() }
func (c *call) enabled(ctx Context) bool {
	check(c.session.AssertReady())
	return !ctx.Simulation && take(c.owner.ports.SceneAvailable(c.session, ctx.SceneID)) && c.flags().Enabled
}
func (c *call) learning(ctx Context) bool {
	return c.enabled(ctx) && take(c.owner.ports.LearningEnabled(c.session, ctx.SceneID))
}
func (c *call) revoked(scene, message string) bool {
	return take(c.owner.ports.SourceRevoked(c.session, scene, message))
}
func (c *call) sourceCurrent(scene, message string, sender *string, snapshot bool, ref *sourceevents.Ref, raw ...*rawReference) bool {
	if c.revoked(scene, message) {
		return false
	}
	if p := c.owner.ports.SourceCurrent; p != nil && !take(p(c.session, scene, message, sender)) {
		return false
	}
	registered := take(c.sources.Registered(scene, message))
	var material *rawReference
	if len(raw) > 0 {
		material = raw[0]
	}
	if registered == nil {
		return !snapshot || (ref == nil && material == nil)
	}
	if sender != nil && registered.Identity.SenderID != *sender {
		return false
	}
	if snapshot {
		value := eventRefValue(ref, material)
		if !referenceTruthy(value) {
			return false
		}
		checked := take(sourceevents.ParseRef(value))
		return take(c.sources.Current(checked, registered.Identity)).Eligible
	}
	return !take(c.sources.HasEdits(registered.Identity))
}
func (c *call) outcomeAvailable(o Outcome, current bool) bool {
	if c.tombstoned(SourceKey(o.SceneID, o.Source.MessageID)) || c.revoked(o.SceneID, o.Source.MessageID) || c.revoked(o.SceneID, o.Target.MessageID) {
		return false
	}
	return !current || (c.sourceCurrent(o.SceneID, o.Source.MessageID, &o.Source.SenderID, o.SnapshotBound, o.Source.EventRef, o.Source.eventRefRaw) && c.sourceCurrent(o.SceneID, o.Target.MessageID, nil, o.SnapshotBound, o.Target.EventRef, o.Target.eventRefRaw))
}
func (c *call) supported(p Policy, current bool) bool {
	if len(p.SupportIDs) == 0 {
		return false
	}
	for _, id := range p.SupportIDs {
		o := c.outcome(id)
		if o == nil || o.SceneID != p.SceneID || !c.outcomeAvailable(*o, current) {
			return false
		}
	}
	return true
}
func (c *call) get(ctx Context, id string, version *int64) Policy {
	p := c.policy(id)
	if !c.enabled(ctx) || p == nil || p.SceneID != ctx.SceneID {
		fail("experience_unavailable")
	}
	if version != nil && (!safe(*version, -9007199254740991) || p.Version != *version) {
		fail("experience_version_conflict")
	}
	return *p
}

const invalidTime int64 = -9223372036854775808

func timestamp(s string) int64 {
	n, ok := dateMillis(s)
	if !ok {
		return invalidTime
	}
	return n
}
func expired(at string, now int64) bool { t := timestamp(at); return t != invalidTime && t <= now }
func (c *call) save(old, next Policy) Policy {
	next.Version = old.Version + 1
	next.Hash = CandidateHash(next)
	if contains([]string{"active", "suspended", "retired"}, next.Status) {
		supports, counters := []any{}, []any{}
		for _, id := range next.SupportIDs {
			supports = append(supports, c.outcome(id))
		}
		for _, id := range next.CounterexampleIDs {
			counters = append(counters, c.outcome(id))
		}
		v := map[string]any{"id": next.ID, "sceneId": next.SceneID, "hash": next.Hash, "supports": supports, "counterexamples": counters}
		if next.ValidationID == "" {
			v["validation"] = nil
		} else if r := c.receipt(next.ValidationID); r != nil {
			v["validation"] = r
		}
		event := recovery.ExperienceState{SceneID: next.SceneID, ID: next.ID, QualificationHash: qualification(v, c.owner.ports.Compare), Version: next.Version, Status: next.Status, Reason: next.Reason}
		if next.ValidationID != "" {
			event.ValidationID = &next.ValidationID
		}
		if next.ActivationBasis != "" {
			event.ActivationBasis = &next.ActivationBasis
		}
		take(c.session.AppendIntent(event))
	}
	c.putPolicy(next, &old.Version)
	return next
}

type FeedbackResult struct {
	Outcome   Outcome `json:"outcome"`
	Candidate *Policy `json:"candidate,omitempty"`
	Duplicate bool    `json:"duplicate"`
}

func (c *call) findCandidate(o Outcome) *Policy {
	ps := c.policiesForOutcome(o.SceneID, o.ID)
	if len(ps) == 0 {
		return nil
	}
	return &ps[0]
}
func (s *Service) RecordFeedback(ctx context.Context, scope Context, input Feedback) (result *FeedbackResult, err error) {
	err = s.run(ctx, func(c *call) {
		if !c.learning(scope) {
			return
		}
		result = c.recordFeedback(scope, input)
	})
	return
}
func (c *call) recordFeedback(ctx Context, input Feedback) *FeedbackResult {
	if !take(c.owner.ports.AdmissionSourceAvailable(c.session, ctx.SceneID, input.Source.MessageID, input.Source.SenderID)) || !take(c.owner.ports.TargetAvailable(c.session, ctx.SceneID, input.Target.MessageID)) || !c.sourceCurrent(ctx.SceneID, input.Source.MessageID, &input.Source.SenderID, false, nil) || !c.sourceCurrent(ctx.SceneID, input.Target.MessageID, nil, false, nil) {
		fail("experience_feedback_not_authorized")
	}
	o := *take(CreateOutcome(ctx, input, c.owner.ports.ID()))
	operationTrial := input.Code == "user_reported_helpful" || (c.flags().FeedbackTrials && contains([]string{"request_exact_person_lookup", "request_verify_plan_qualifiers"}, input.Code))
	if operationTrial {
		if !c.flags().FeedbackTrials || !c.sourceCurrent(ctx.SceneID, input.Source.MessageID, &input.Source.SenderID, true, input.Source.EventRef, input.Source.eventRefRaw) || !c.sourceCurrent(ctx.SceneID, input.Target.MessageID, nil, true, input.Target.EventRef, input.Target.eventRefRaw) {
			fail("experience_feedback_snapshot_stale")
		}
		o.SnapshotBound = true
	}
	if c.tombstoned(SourceKey(ctx.SceneID, input.Source.MessageID)) || c.tombstoned(o.RootID) {
		fail("experience_source_revoked")
	}
	if previous := c.root(o.RootID); previous != nil {
		if previous.Code != input.Code || encoded(previous.PolicyRef) != encoded(input.PolicyRef) {
			fail("experience_root_conflict")
		}
		return &FeedbackResult{*previous, c.findCandidate(*previous), true}
	}
	if input.Code == "user_reported_helpful" {
		p := c.get(ctx, input.PolicyRef.ID, &input.PolicyRef.Version)
		next := c.collect(p, o)
		return &FeedbackResult{o, &next, false}
	}
	withdrawal := contains([]string{"withdraw_instruction", "withdraw_person_instruction", "withdraw_plan_instruction"}, input.Code)
	if input.Code == "counterexample" || withdrawal {
		ref := input.PolicyRef
		p := c.get(ctx, ref.ID, &ref.Version)
		expected := "conclusion_before_detail"
		if input.Code == "withdraw_person_instruction" {
			expected = "exact_person_before_semantic"
		} else if input.Code == "withdraw_plan_instruction" {
			expected = "verify_plan_qualifiers"
		}
		if withdrawal && p.Action != expected {
			fail("experience_instruction_mismatch")
		}
		found := false
		kind := "applied"
		if withdrawal {
			kind = "exposed"
		}
		for _, u := range c.uses(ctx.SceneID, input.Target.MessageID) {
			if u.Kind == kind && sameReference(u.PolicyRef, *ref) {
				found = true
				break
			}
		}
		if !found {
			fail("experience_unverified_policy_use")
		}
		trial := c.trial(p.ID)
		if withdrawal && input.Code != "withdraw_instruction" && trial == nil {
			fail("experience_instruction_mismatch")
		}
		if trial != nil {
			if !c.sourceCurrent(ctx.SceneID, input.Source.MessageID, &input.Source.SenderID, true, input.Source.EventRef, input.Source.eventRefRaw) || !c.sourceCurrent(ctx.SceneID, input.Target.MessageID, nil, true, input.Target.EventRef, input.Target.eventRefRaw) {
				fail("experience_feedback_snapshot_stale")
			}
			o.SnapshotBound = true
		}
		if (trial != nil && trial.RequesterID != input.Source.SenderID) || (p.Condition.Kind != "plan_question" && p.Condition.PersonID != input.Source.SenderID) {
			fail("experience_feedback_person_mismatch")
		}
		reason, policyReason := "negative_feedback", "explicit_counterexample"
		if withdrawal {
			reason = "instruction_withdrawn"
			policyReason = "explicit_instruction_withdrawn"
		}
		c.suspend(p, reason)
		c.putOutcome(o)
		next := p
		next.Status = "suspended"
		next.Reason = policyReason
		next.CounterexampleIDs = append([]string{}, p.CounterexampleIDs...)
		if !contains(next.CounterexampleIDs, o.ID) {
			next.CounterexampleIDs = append(next.CounterexampleIDs, o.ID)
		}
		next = c.save(p, next)
		return &FeedbackResult{o, &next, false}
	}
	c.putOutcome(o)
	p := *take(CandidateFromOutcome(o, c.owner.ports.ID(), c.now()))
	c.putPolicy(p, nil)
	f := c.flags()
	if p.Action == "conclusion_before_detail" && f.AutoActivate && f.Hints {
		p = c.validate(ctx, p.ID, p.Version)
		if p.Status == "validated" {
			p = c.review(ctx, p.ID, p.Version, "active", "explicit_preference")
		}
	}
	if p.Action != "conclusion_before_detail" && f.FeedbackTrials && f.Hints {
		p = c.validate(ctx, p.ID, p.Version)
		c.start(p, o)
	}
	return &FeedbackResult{o, &p, false}
}
func (c *call) validate(ctx Context, id string, version int64) Policy {
	p := c.get(ctx, id, &version)
	if !contains([]string{"candidate", "suspended"}, p.Status) || !c.supported(p, true) || expired(p.ExpiresAt, c.now()) || len(p.CounterexampleIDs) > 0 {
		fail("experience_validation_ineligible")
	}
	copy := *decode[Policy](encoded(p))
	receipt := take(c.owner.ports.Validate(copy))
	if !ValidMechanicalReceipt(p, receipt) {
		fail("experience_validation_failed")
	}
	id = c.owner.ports.ID()
	c.putReceipt(id, receipt)
	next := p
	next.Status = "validated"
	next.ValidationID = id
	next.Reason = "mechanical_only"
	return c.save(p, next)
}
func (s *Service) Validate(ctx context.Context, scope Context, id string, version int64) (p Policy, err error) {
	err = s.run(ctx, func(c *call) { p = c.validate(scope, id, version) })
	return
}
func (c *call) review(ctx Context, id string, version int64, status, basis string) Policy {
	p := c.get(ctx, id, &version)
	if !contains([]string{"active", "suspended", "retired"}, status) || p.Status == "retired" {
		fail("experience_invalid_transition")
	}
	if status == "active" {
		if basis == "explicit_preference" && (!c.learning(ctx) || !c.flags().AutoActivate || !c.flags().Hints || p.Action != "conclusion_before_detail") {
			fail("experience_activation_ineligible")
		}
		r := c.receipt(p.ValidationID)
		if p.Status != "validated" || r == nil || r.CandidateVersion != p.Version-1 || r.CandidateHash != p.Hash || !c.supported(p, true) || expired(p.ExpiresAt, c.now()) {
			fail("experience_activation_ineligible")
		}
	}
	c.suspend(p, "qualification_changed")
	next := p
	next.Status = status
	if status == "active" {
		next.ActivationBasis = basis
		next.Reason = "experimental_mechanical_hint"
	} else {
		next.Reason = "review_" + status
	}
	return c.save(p, next)
}
func (s *Service) Review(ctx context.Context, scope Context, id string, version int64, status, basis string) (p Policy, err error) {
	if basis == "" {
		basis = "administrator_review"
	}
	err = s.run(ctx, func(c *call) { p = c.review(scope, id, version, status, basis) })
	return
}

type ProposalResult struct {
	Candidate Policy `json:"candidate"`
	Duplicate bool   `json:"duplicate"`
}

func (s *Service) ProposePolicy(ctx context.Context, scope Context, p Proposal, exposed []Reference) (result ProposalResult, err error) {
	err = s.run(ctx, func(c *call) {
		if !c.learning(scope) {
			fail("experience_learning_disabled")
		}
		if len(p.OutcomeRefs) != 1 {
			fail("experience_invalid_support")
		}
		ref := p.OutcomeRefs[0]
		seen := false
		for _, r := range exposed {
			if sameReference(r, ref) {
				seen = true
			}
		}
		if !seen {
			fail("experience_outcome_not_exposed")
		}
		o := c.outcome(ref.ID)
		if o == nil || o.Version != ref.Version || o.SceneID != scope.SceneID || !c.outcomeAvailable(*o, true) {
			fail("experience_outcome_unavailable")
		}
		candidate := *take(CandidateFromOutcome(*o, c.owner.ports.ID(), c.now()))
		if candidate.Action != p.Action || encoded(candidate.Condition) != encoded(p.Condition) {
			fail("experience_instruction_mismatch")
		}
		if previous := c.findCandidate(*o); previous != nil {
			result = ProposalResult{*previous, true}
			return
		}
		c.putPolicy(candidate, nil)
		result = ProposalResult{candidate, false}
	})
	return
}
func (s *Service) ListOutcomes(ctx context.Context, scope Context, limit int) (out []Outcome, err error) {
	out = []Outcome{}
	err = s.run(ctx, func(c *call) {
		if !c.learning(scope) {
			return
		}
		if limit < 1 || limit > 20 {
			fail("experience_invalid_limit")
		}
		for _, o := range c.outcomes(scope.SceneID, limit+1) {
			if c.outcomeAvailable(o, true) && len(out) < limit {
				out = append(out, o)
			}
		}
	})
	return
}

type ListOptions struct {
	Limit  *int
	Status string
}
type ListResult struct {
	Items    []Policy `json:"items"`
	Coverage string   `json:"coverage"`
}

func (c *call) list(ctx Context, options ListOptions) ListResult {
	limit := 40
	if options.Limit != nil {
		limit = *options.Limit
	}
	if limit < 1 || limit > 40 {
		fail("experience_invalid_limit")
	}
	out := ListResult{[]Policy{}, "complete"}
	if !c.enabled(ctx) {
		return out
	}
	rows := c.policies(ctx.SceneID)
	if len(rows) > 200 {
		out.Coverage = "limited"
		rows = rows[:200]
	}
	for _, p := range rows {
		c.expire(p)
	}
	for _, p := range rows {
		if p.Status == "active" && (!c.supported(p, false) || expired(p.ExpiresAt, c.now())) {
			next := p
			next.Status = "suspended"
			next.Reason = "support_or_expiry_invalid"
			p = c.save(p, next)
		}
		if options.Status == "" || p.Status == options.Status {
			out.Items = append(out.Items, p)
		}
	}
	if len(out.Items) > limit {
		out.Items = out.Items[:limit]
		out.Coverage = "limited"
	}
	return out
}
func (s *Service) List(ctx context.Context, scope Context, options ListOptions) (result ListResult, err error) {
	err = s.run(ctx, func(c *call) { result = c.list(scope, options) })
	return
}

type HintQuery struct {
	PersonID, RequesterID        string
	PersonQuestion, PlanQuestion bool
}

func (s *Service) Hints(ctx context.Context, scope Context, q HintQuery) (out []Hint, err error) {
	out = []Hint{}
	err = s.run(ctx, func(c *call) {
		if !c.enabled(scope) || !c.flags().Hints {
			return
		}
		for _, p := range c.list(scope, ListOptions{}).Items {
			if !c.supported(p, true) {
				continue
			}
			var t *Trial
			if p.Status != "active" {
				t = c.current(p)
			}
			if p.Status != "active" && (t == nil || q.RequesterID == "" || t.RequesterID != q.RequesterID) {
				continue
			}
			match := q.PlanQuestion
			if p.Condition.Kind != "plan_question" {
				match = p.Condition.PersonID == q.PersonID && (p.Condition.Kind == "response_style" || q.PersonQuestion)
			}
			if !match {
				continue
			}
			h := Hint{ID: p.ID, Version: p.Version, Hash: p.Hash, Action: p.Action, Condition: p.Condition, ExpiresAt: p.ExpiresAt, Tier: "mechanical_only"}
			if p.Status != "active" && t != nil {
				h.EligibilityBasis = "explicit_instruction_trial"
				if t.Phase == "promoted" {
					h.EligibilityBasis = "user_reported_support"
				}
				h.FeedbackRevision = &t.SettingsRevision
				h.TrialVersion = &t.Version
				h.TrialExpiresAt = t.ExpiresAt
				h.ReportedHelpfulCount = ptr(len(t.Positives))
			}
			out = append(out, h)
		}
		if len(out) > 3 {
			out = out[:3]
		}
	})
	return
}

type Snapshot struct {
	Reference
	Hash                           string
	FeedbackRevision, TrialVersion *int64
}

func (c *call) snapshotValid(ctx Context, ref Snapshot) bool {
	if !c.enabled(ctx) || !c.flags().Hints {
		return false
	}
	p := c.policy(ref.ID)
	if p == nil || p.SceneID != ctx.SceneID {
		return false
	}
	var t *Trial
	if p.Status != "active" || ref.TrialVersion != nil {
		t = c.current(*p)
	}
	if p.Status != "active" && t == nil {
		return false
	}
	f := c.flags()
	if ref.FeedbackRevision != nil && (!f.FeedbackTrials || f.Revision == nil || *ref.FeedbackRevision != *f.Revision) {
		return false
	}
	if ref.TrialVersion != nil && (t == nil || *ref.TrialVersion != t.Version) {
		return false
	}
	return p.Version == ref.Version && p.Hash == ref.Hash && c.supported(*p, true) && timestamp(p.ExpiresAt) > c.now()
}
func (s *Service) SnapshotValid(ctx context.Context, scope Context, ref Snapshot) (valid bool, err error) {
	err = s.run(ctx, func(c *call) { valid = c.snapshotValid(scope, ref) })
	return
}
func (s *Service) RecordUse(ctx context.Context, scope Context, u Use) (ok bool, err error) {
	err = s.run(ctx, func(c *call) {
		if !c.enabled(scope) || !c.flags().Hints {
			return
		}
		p := c.get(scope, u.PolicyRef.ID, &u.PolicyRef.Version)
		if !c.snapshotValid(scope, Snapshot{Reference: u.PolicyRef, Hash: p.Hash}) {
			return
		}
		expected := operation(p.Action)
		if u.TurnID == "" || !contains([]string{"exposed", "applied", "declined"}, u.Kind) || (u.Kind == "applied" && u.Operation != expected) || (u.TargetMessageID != "" && !take(c.owner.ports.TargetAvailable(c.session, scope.SceneID, u.TargetMessageID))) {
			fail("experience_invalid_use")
		}
		c.putUse(Key(scope.SceneID, u.TurnID, p.ID, strconv.FormatInt(p.Version, 10), u.Kind, u.TargetMessageID), scope.SceneID, u)
		ok = true
	})
	return
}
func (s *Service) TargetUses(ctx context.Context, scope Context, target string) (out []Use, err error) {
	out = []Use{}
	err = s.run(ctx, func(c *call) {
		if !c.enabled(scope) {
			return
		}
		for _, u := range c.uses(scope.SceneID, target) {
			p := c.policy(u.PolicyRef.ID)
			if p != nil && p.SceneID == scope.SceneID && p.Status != "retired" && c.supported(*p, true) && timestamp(p.ExpiresAt) > c.now() && len(out) < 20 {
				out = append(out, u)
			}
		}
	})
	return
}
