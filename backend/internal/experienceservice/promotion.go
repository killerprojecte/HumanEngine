package experienceservice

func operation(action string) string {
	switch action {
	case "exact_person_before_semantic":
		return "memory_person"
	case "verify_plan_qualifiers":
		return "plan_qualifiers_checked"
	default:
		return "conclusion_first_response"
	}
}
func (c *call) trialFlags(scene string) bool {
	f := c.flags()
	return f.Enabled && f.Hints && f.FeedbackTrials && f.Revision != nil && safe(*f.Revision, 0) && take(c.owner.ports.LearningEnabled(c.session, scene))
}
func (c *call) applied(p Policy, o Outcome, version int64, turn string) *Use {
	if p.Action != "exact_person_before_semantic" && p.Action != "verify_plan_qualifiers" {
		return nil
	}
	for _, u := range c.uses(p.SceneID, o.Target.MessageID) {
		if u.Kind == "applied" && u.Operation == operation(p.Action) && u.PolicyRef.ID == p.ID && u.PolicyRef.Version == version && (turn == "" || u.TurnID == turn) {
			return &u
		}
	}
	return nil
}
func (c *call) current(p Policy) *Trial {
	t := c.trial(p.ID)
	if t == nil || !c.trialFlags(p.SceneID) || !contains([]string{"trial", "promoted"}, t.Phase) || t.SceneID != p.SceneID || t.PolicyVersion != p.Version || t.PolicyHash != p.Hash || CandidateHash(p) != p.Hash || c.flags().Revision == nil || t.SettingsRevision != *c.flags().Revision || expired(t.ExpiresAt, c.now()) || expired(p.ExpiresAt, c.now()) || p.Status != "validated" || len(p.CounterexampleIDs) > 0 {
		return nil
	}
	instruction, r := c.outcome(t.Instruction.ID), c.receipt(t.ValidationID)
	evidence := p
	evidence.Version = t.EvidencePolicyVersion - 1
	expected := "request_verify_plan_qualifiers"
	if p.Action == "exact_person_before_semantic" {
		expected = "request_exact_person_lookup"
	}
	if instruction == nil || !instruction.SnapshotBound || instruction.Version != t.Instruction.Version || instruction.Source.SenderID != t.RequesterID || instruction.SceneID != p.SceneID || !c.outcomeAvailable(*instruction, true) || !contains(p.SupportIDs, instruction.ID) || r == nil || !r.Passed || r.RuleVersion != "experience-mechanical-v2" || r.DatasetVersion != "experience-fixed-behavior-holdout-v2" || !ValidMechanicalReceipt(evidence, *r) || instruction.Code != expected || r.CandidateHash != p.Hash || r.CandidateVersion != t.EvidencePolicyVersion-1 {
		return nil
	}
	for _, item := range t.Positives {
		o := c.outcome(item.OutcomeID)
		if o == nil || o.Version != item.OutcomeVersion || o.Code != "user_reported_helpful" || !o.SnapshotBound || o.SceneID != p.SceneID || o.Source.SenderID != t.RequesterID || o.PolicyRef == nil || o.PolicyRef.ID != p.ID || o.PolicyRef.Version != t.EvidencePolicyVersion || !c.outcomeAvailable(*o, true) || SourceKey(o.SceneID, o.Source.MessageID) != item.SourceRoot || SourceKey(o.SceneID, o.Target.MessageID) != item.TargetRoot || c.applied(p, *o, t.EvidencePolicyVersion, item.TurnID) == nil {
			return nil
		}
	}
	return t
}
func (c *call) start(p Policy, instruction Outcome) *Trial {
	if !c.trialFlags(p.SceneID) || p.Status != "validated" || p.ValidationID == "" || p.Action == "conclusion_before_detail" {
		return nil
	}
	now := c.now()
	r := c.receipt(p.ValidationID)
	previous := p
	previous.Version--
	if r == nil || r.RuleVersion != "experience-mechanical-v2" || r.DatasetVersion != "experience-fixed-behavior-holdout-v2" || !ValidMechanicalReceipt(previous, *r) {
		fail("experience_trial_validation_required")
	}
	if !c.outcomeAvailable(instruction, true) || !instruction.SnapshotBound || expired(p.ExpiresAt, now) {
		fail("experience_trial_ineligible")
	}
	expires := now + 7*86400000
	if timestamp(p.ExpiresAt) < expires {
		expires = timestamp(p.ExpiresAt)
	}
	t := &Trial{SchemaVersion: 1, PolicyID: p.ID, SceneID: p.SceneID, Version: 1, PolicyVersion: p.Version, EvidencePolicyVersion: p.Version, PolicyHash: p.Hash, RequesterID: instruction.Source.SenderID, SettingsRevision: *c.flags().Revision, Phase: "trial", CreatedAt: isoMillis(now), ExpiresAt: isoMillis(expires), Instruction: Reference{ID: instruction.ID, Version: instruction.Version}, ValidationID: p.ValidationID, Positives: []Positive{}, Reason: "explicit_instruction_trial"}
	c.writeTrial(t, nil, "start", nil)
	return t
}
func (c *call) collect(p Policy, o Outcome) Policy {
	prev := c.current(p)
	if prev == nil || prev.Phase != "trial" || o.PolicyRef == nil || o.PolicyRef.ID != p.ID || o.PolicyRef.Version != p.Version || o.Code != "user_reported_helpful" || o.Source.SenderID != prev.RequesterID || !c.outcomeAvailable(o, true) || !o.SnapshotBound {
		fail("experience_feedback_trial_ineligible")
	}
	use := c.applied(p, o, p.Version, "")
	if use == nil {
		fail("experience_unverified_policy_use")
	}
	item := Positive{o.ID, o.Version, SourceKey(o.SceneID, o.Source.MessageID), SourceKey(o.SceneID, o.Target.MessageID), use.TurnID}
	instruction := c.outcome(prev.Instruction.ID)
	if instruction == nil {
		fail("experience_feedback_trial_ineligible")
	}
	if item.SourceRoot == SourceKey(o.SceneID, instruction.Source.MessageID) {
		fail("experience_feedback_not_independent")
	}
	for _, v := range prev.Positives {
		if v.SourceRoot == item.SourceRoot || v.TargetRoot == item.TargetRoot || v.TurnID == item.TurnID {
			fail("experience_feedback_not_independent")
		}
	}
	promote := len(prev.Positives) == 1
	nextPolicy := p
	next := *prev
	next.Version++
	next.Positives = append(append([]Positive{}, prev.Positives...), item)
	next.Reason = "explicit_instruction_trial"
	op := "collect"
	if promote {
		nextPolicy.Version++
		nextPolicy = applyPolicyPresentationPatch(nextPolicy, "version")
		next.Phase = "promoted"
		next.Reason = "user_reported_helpfulness"
		op = "promote"
	}
	next.PolicyVersion = nextPolicy.Version
	c.writeTrial(&next, prev, op, func() {
		c.putOutcome(o)
		if promote {
			c.putPolicy(nextPolicy, &p.Version)
		}
	})
	return nextPolicy
}
func (c *call) suspend(p Policy, reason string) {
	prev := c.trial(p.ID)
	if prev != nil && contains([]string{"trial", "promoted"}, prev.Phase) {
		next := *prev
		next.Version++
		next.Phase = "suspended"
		next.Reason = reason
		c.writeTrial(&next, prev, "suspend", nil)
	}
}
func (c *call) expire(p Policy) {
	prev := c.trial(p.ID)
	if prev != nil && contains([]string{"trial", "promoted"}, prev.Phase) && expired(prev.ExpiresAt, c.now()) {
		next := *prev
		next.Version++
		next.Phase = "expired"
		next.Reason = "expiry"
		c.writeTrial(&next, prev, "expire", nil)
	}
}
