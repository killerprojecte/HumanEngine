package experienceservice

import (
	"errors"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/sourceevents"
)

// RuntimeView is a synchronous native read-only view. It does not expose Hints:
// source hints/list perform expiry writes and must run outside read validation.
type RuntimeView struct {
	scope   sourceevents.RuntimeReadScope
	owner   *Service
	sources *sourceevents.View
}

func (s *Service) WithRuntimeView(scope sourceevents.RuntimeReadScope, fn func(*RuntimeView) error) error {
	if scope.Ready == nil || scope.Query == nil || scope.CheckBinding == nil || fn == nil {
		return errors.New("experience_read_scope_required")
	}
	if e := scope.CheckBinding(s.main, s.authority); e != nil {
		return e
	}
	sources, e := sourceevents.BorrowReadScope(scope.Ready, scope.Query)
	if e != nil {
		return e
	}
	v := &RuntimeView{scope: scope, owner: s, sources: sources}
	if e := fn(v); e != nil {
		return e
	}
	return scope.Ready()
}
func (c *RuntimeView) values(q string, a ...any) []string {
	check(c.scope.Ready())
	rows := take(c.scope.Query(q, sqlArgs(a)...))
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var raw string
		check(rows.Scan(&raw))
		out = append(out, raw)
	}
	check(rows.Err())
	check(rows.Close())
	return out
}
func (c *RuntimeView) exists(q string, a ...any) bool {
	check(c.scope.Ready())
	rows := take(c.scope.Query(q, sqlArgs(a)...))
	defer rows.Close()
	yes := rows.Next()
	check(rows.Err())
	check(rows.Close())
	return yes
}
func readOne[T any](c *RuntimeView, q string, a ...any) *T {
	rows := c.values(q, a...)
	if len(rows) == 0 {
		return nil
	}
	return decode[T](rows[0])
}
func (c *RuntimeView) outcome(id string) *Outcome {
	return readOne[Outcome](c, "SELECT value FROM memory_experience_outcomes WHERE id=?", id)
}
func (c *RuntimeView) policy(id string) *Policy {
	return readOne[Policy](c, "SELECT value FROM memory_experience_policies WHERE id=?", id)
}
func (c *RuntimeView) receipt(id string) *Receipt {
	return readOne[Receipt](c, "SELECT value FROM memory_experience_validations WHERE id=?", id)
}
func (c *RuntimeView) tombstoned(id string) bool {
	return c.exists("SELECT 1 FROM memory_experience_tombstones WHERE key=?", id)
}
func (c *RuntimeView) revoked(scene, id string) bool {
	return c.exists("SELECT 1 FROM recall_tombstones WHERE scene_id=? AND message_id=?", scene, id)
}
func (c *RuntimeView) nativeValue(q string, a ...any) *jsjson.Object {
	rows := c.values(q, a...)
	if len(rows) == 0 {
		return nil
	}
	v := take(jsjson.Parse(rows[0]))
	if v == nil {
		return nil
	}
	o, ok := v.(*jsjson.Object)
	if !ok {
		fail("experience_stored_value_invalid")
	}
	return o
}
func prop(o *jsjson.Object, k string) any {
	if o == nil {
		return nil
	}
	v, _ := o.Get(k)
	return v
}
func (c *RuntimeView) scene(id string) *jsjson.Object {
	return c.nativeValue("SELECT value FROM scenes WHERE id=?", id)
}
func (c *RuntimeView) flags() Settings {
	o := c.nativeValue("SELECT value FROM config WHERE key='settings'")
	f := Settings{Enabled: prop(o, "memoryExperienceEnabled") == true, AutoActivate: prop(o, "memoryExperienceAutoActivateEnabled") == true, Hints: prop(o, "memoryExperienceHintsEnabled") == true, FeedbackTrials: prop(o, "memoryExperienceFeedbackEnabled") == true}
	rows := c.values("SELECT value FROM config WHERE key='memory-experience-feedback-revision'")
	if len(rows) == 0 {
		f.Revision = ptr(int64(0))
	} else if n, ok := take(jsjson.Parse(rows[0])).(float64); ok && safeRefNumber(n, 0) {
		f.Revision = ptr(int64(n))
	}
	return f
}
func (c *RuntimeView) now() int64 { return c.owner.ports.Now() }
func (c *RuntimeView) enabled(ctx Context) bool {
	check(c.scope.Ready())
	if ctx.Simulation {
		return false
	}
	k := prop(c.scene(ctx.SceneID), "kind")
	return (k == "group" || k == "private") && c.flags().Enabled
}
func (c *RuntimeView) learning(ctx Context) bool {
	return c.enabled(ctx) && prop(c.scene(ctx.SceneID), "learningEnabled") == true
}
func (c *RuntimeView) nativeCurrent(scene, id string, sender *string) bool {
	if c.revoked(scene, id) {
		return false
	}
	head := take(c.sources.Registered(scene, id))
	if head == nil {
		return true
	}
	o := c.scene(scene)
	if o == nil {
		return false
	}
	author := head.Identity.SenderID
	if sender != nil {
		author = *sender
	}
	if prop(o, "adapterId") != head.Identity.AdapterID || prop(o, "accountId") != head.Identity.AccountID || author != head.Identity.SenderID {
		return false
	}
	return !take(c.sources.HasEdits(head.Identity))
}
func (c *RuntimeView) uses(scene, target string) []Use {
	out := []Use{}
	for _, raw := range c.values("SELECT value FROM memory_experience_uses WHERE scene=? AND target=?", scene, target) {
		out = append(out, *decode[Use](raw))
	}
	return out
}
func (c *RuntimeView) trialsExist() bool {
	return c.exists("SELECT 1 FROM sqlite_master WHERE type='table' AND name='memory_experience_trials'")
}
func (c *RuntimeView) sourceCurrent(scene, message string, sender *string, snapshot bool, ref *sourceevents.Ref, raw ...*rawReference) bool {
	if c.revoked(scene, message) {
		return false
	}
	if !c.nativeCurrent(scene, message, sender) {
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

func (c *RuntimeView) outcomeAvailable(o Outcome, current bool) bool {
	if c.tombstoned(SourceKey(o.SceneID, o.Source.MessageID)) || c.revoked(o.SceneID, o.Source.MessageID) || c.revoked(o.SceneID, o.Target.MessageID) {
		return false
	}
	return !current || (c.sourceCurrent(o.SceneID, o.Source.MessageID, &o.Source.SenderID, o.SnapshotBound, o.Source.EventRef, o.Source.eventRefRaw) && c.sourceCurrent(o.SceneID, o.Target.MessageID, nil, o.SnapshotBound, o.Target.EventRef, o.Target.eventRefRaw))
}

func (c *RuntimeView) supported(p Policy, current bool) bool {
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

func (c *RuntimeView) snapshotValid(ctx Context, ref Snapshot) bool {
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

func (c *RuntimeView) trialFlags(scene string) bool {
	f := c.flags()
	return f.Enabled && f.Hints && f.FeedbackTrials && f.Revision != nil && safe(*f.Revision, 0) && prop(c.scene(scene), "learningEnabled") == true
}

func (c *RuntimeView) applied(p Policy, o Outcome, version int64, turn string) *Use {
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

func (c *RuntimeView) current(p Policy) *Trial {
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

func (s *RuntimeView) trial(id string) *Trial {
	if !s.trialsExist() {
		return nil
	}
	rows := take(s.scope.Query("SELECT policy,scene,version,value FROM memory_experience_trials WHERE policy=?", take(jsjson.UTF8Text(id))))
	defer rows.Close()
	if !rows.Next() {
		check(rows.Err())
		return nil
	}
	var policy, scene string
	var version int64
	var raw *string
	check(rows.Scan(&policy, &scene, &version, &raw))
	check(rows.Err())
	check(rows.Close())
	if !validID(policy) || !validID(scene) || !safe(version, 1) {
		fail("experience_trial_corrupt")
	}
	if raw == nil {
		return nil
	}
	t := decode[Trial](*raw)
	validateTrial(*t)
	if t.PolicyID != policy || t.SceneID != scene || t.Version != version {
		fail("experience_trial_corrupt")
	}
	return t
}

func (c *RuntimeView) SnapshotValid(scope Context, ref Snapshot) (valid bool, err error) {
	err = guarded(func() { check(c.scope.Ready()); valid = c.snapshotValid(scope, ref); check(c.scope.Ready()) })
	return
}
func (c *RuntimeView) ListOutcomes(scope Context, limit int) (out []Outcome, err error) {
	out = []Outcome{}
	err = guarded(func() {
		check(c.scope.Ready())
		if !c.learning(scope) {
			return
		}
		if limit < 1 || limit > 20 {
			fail("experience_invalid_limit")
		}
		for _, raw := range c.values("SELECT value FROM memory_experience_outcomes WHERE scene=? ORDER BY id LIMIT ?", scope.SceneID, limit+1) {
			o := *decode[Outcome](raw)
			if c.outcomeAvailable(o, true) && len(out) < limit {
				out = append(out, o)
			}
		}
		check(c.scope.Ready())
	})
	return
}
