package experienceservice

import (
	"crypto/sha256"
	"fmt"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/recovery"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"time"
)

type Positive struct {
	OutcomeID      string `json:"outcomeId"`
	OutcomeVersion int64  `json:"outcomeVersion"`
	SourceRoot     string `json:"sourceRoot"`
	TargetRoot     string `json:"targetRoot"`
	TurnID         string `json:"turnId"`
}
type Trial struct {
	SchemaVersion         int        `json:"schemaVersion"`
	PolicyID              string     `json:"policyId"`
	SceneID               string     `json:"sceneId"`
	Version               int64      `json:"version"`
	PolicyVersion         int64      `json:"policyVersion"`
	EvidencePolicyVersion int64      `json:"evidencePolicyVersion"`
	PolicyHash            string     `json:"policyHash"`
	RequesterID           string     `json:"requesterId"`
	SettingsRevision      int64      `json:"settingsRevision"`
	Phase                 string     `json:"phase"`
	CreatedAt             string     `json:"createdAt"`
	ExpiresAt             string     `json:"expiresAt"`
	Instruction           Reference  `json:"instruction"`
	ValidationID          string     `json:"validationId"`
	Positives             []Positive `json:"positives"`
	Reason                string     `json:"reason,omitempty"`
}

func ptr[T any](v T) *T { return &v }
func contains(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}
func validID(s string) bool        { n, e := jsjson.UTF16Len(s); return s != "" && e == nil && n <= 512 }
func safe(n int64, min int64) bool { return n >= min && n <= 9007199254740991 }
func validHash(s string) bool {
	if len(s) != 64 {
		return false
	}
	for _, c := range s {
		if !strings.ContainsRune("0123456789abcdef", c) {
			return false
		}
	}
	return true
}
func validateTrial(t Trial) {
	if t.Instruction.raw != nil {
		checkTrialObject(t.Instruction.raw, []string{"id", "version"})
	}
	if t.SchemaVersion != 1 || !validID(t.PolicyID) || !validID(t.SceneID) || !validID(t.RequesterID) || !validID(t.ValidationID) || !validID(t.Instruction.ID) || !safe(t.Version, 1) || !safe(t.PolicyVersion, 1) || !safe(t.EvidencePolicyVersion, 1) || !safe(t.Instruction.Version, 1) || !safe(t.SettingsRevision, 0) || !validHash(t.PolicyHash) || !contains([]string{"trial", "promoted", "suspended", "expired"}, t.Phase) || len(t.Positives) > 2 {
		fail("experience_trial_corrupt")
	}
	if t.Reason != "" && !contains([]string{"explicit_instruction_trial", "user_reported_helpfulness", "negative_feedback", "instruction_withdrawn", "expiry", "qualification_changed"}, t.Reason) {
		fail("experience_trial_corrupt")
	}
	if !trialDatetime(t.CreatedAt) || !trialDatetime(t.ExpiresAt) {
		fail("experience_trial_corrupt")
	}
	a, b := timestamp(t.CreatedAt), timestamp(t.ExpiresAt)
	if a == invalidTime || b == invalidTime || b <= a || b-a > 7*86400000 {
		fail("experience_trial_expiry_invalid")
	}
	ids, src, tgt, turn := map[string]bool{}, map[string]bool{}, map[string]bool{}, map[string]bool{}
	for _, p := range t.Positives {
		if !validID(p.OutcomeID) || !safe(p.OutcomeVersion, 1) || !validID(p.TurnID) || !validHash(p.SourceRoot) || !validHash(p.TargetRoot) {
			fail("experience_trial_corrupt")
		}
		if ids[p.OutcomeID] || src[p.SourceRoot] || tgt[p.TargetRoot] || turn[p.TurnID] {
			fail("experience_trial_independence_invalid")
		}
		ids[p.OutcomeID] = true
		src[p.SourceRoot] = true
		tgt[p.TargetRoot] = true
		turn[p.TurnID] = true
	}
	if t.Phase == "promoted" && len(t.Positives) != 2 {
		fail("experience_trial_support_missing")
	}
}

// Qualification uses the caller's pinned source-locale comparator, including
// retained unknown properties from legacy JSON. Never substitute host sorting.
func canonical(v any, compare func(string, string) (int, error)) any {
	switch x := v.(type) {
	case *jsjson.Object:
		keys := x.Keys()
		sort.SliceStable(keys, func(i, j int) bool { return take(compare(keys[i], keys[j])) < 0 })
		o := new(jsjson.Object)
		for _, k := range keys {
			v, _ := x.Get(k)
			check(o.Set(k, canonical(v, compare)))
		}
		return o
	case []any:
		out := make([]any, len(x))
		for i, v := range x {
			out[i] = canonical(v, compare)
		}
		return out
	}
	return v
}
func qualification(v any, compare func(string, string) (int, error)) string {
	parsed := take(jsjson.Parse(encoded(v)))
	raw := take(jsjson.Stringify(canonical(parsed, compare)))
	return fmt.Sprintf("%x", sha256.Sum256([]byte(raw)))
}
func trialHash(t *Trial, compare func(string, string) (int, error)) *string {
	if t == nil {
		return nil
	}
	return ptr(qualification(t, compare))
}
func (s store) trialsExist() bool {
	return s.exists("SELECT 1 FROM sqlite_master WHERE type='table' AND name='memory_experience_trials'")
}
func (s store) trials(scene string) []Trial {
	out := []Trial{}
	if !s.trialsExist() {
		return out
	}
	q := "SELECT policy,scene,version,value FROM memory_experience_trials"
	args := []any{}
	if scene != "" {
		q += " WHERE scene=?"
		args = append(args, scene)
	}
	rows := take(s.session.Query(q, sqlArgs(args)...))
	defer rows.Close()
	count := 0
	for rows.Next() {
		count++
		if count > 4096 {
			fail("experience_trial_capacity")
		}
		var id, sc string
		var version int64
		var raw *string
		check(rows.Scan(&id, &sc, &version, &raw))
		if !validID(id) || !validID(sc) || !safe(version, 1) {
			fail("experience_trial_corrupt")
		}
		if raw == nil {
			continue
		}
		t := decode[Trial](*raw)
		validateTrial(*t)
		if t.PolicyID != id || t.SceneID != sc || t.Version != version {
			fail("experience_trial_corrupt")
		}
		out = append(out, *t)
	}
	check(rows.Err())
	check(rows.Close())
	return out
}
func (s store) trial(id string) *Trial {
	if !s.trialsExist() {
		return nil
	}
	rows := take(s.session.Query("SELECT policy,scene,version,value FROM memory_experience_trials WHERE policy=?", take(jsjson.UTF8Text(id))))
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
func (c *call) writeTrial(next, previous *Trial, operation string, apply func()) {
	if next != nil {
		validateTrial(*next)
	}
	identity := next
	if identity == nil {
		identity = previous
	}
	if identity == nil {
		fail("experience_trial_missing")
	}
	stored := c.trial(identity.PolicyID)
	if !reflect.DeepEqual(trialHash(stored, c.owner.ports.Compare), trialHash(previous, c.owner.ports.Compare)) {
		fail("experience_trial_version_conflict")
	}
	if next != nil && previous != nil {
		if next.Version != previous.Version+1 || next.SceneID != previous.SceneID || next.PolicyID != previous.PolicyID {
			fail("experience_trial_version_conflict")
		}
		if !reflect.DeepEqual(next.Instruction, previous.Instruction) || next.ValidationID != previous.ValidationID || next.PolicyHash != previous.PolicyHash || next.RequesterID != previous.RequesterID || next.SettingsRevision != previous.SettingsRevision || next.EvidencePolicyVersion != previous.EvidencePolicyVersion || next.CreatedAt != previous.CreatedAt || next.ExpiresAt != previous.ExpiresAt {
			fail("experience_trial_immutable")
		}
		delta := int64(0)
		if operation == "promote" {
			delta = 1
		}
		if next.PolicyVersion != previous.PolicyVersion+delta {
			fail("experience_trial_version_conflict")
		}
	}
	phase := map[string]string{"start": "trial", "collect": "trial", "promote": "promoted", "suspend": "suspended", "expire": "expired"}[operation]
	if (operation == "start") != (previous == nil) || (operation == "erase") != (next == nil) || (next != nil && next.Phase != phase) {
		fail("experience_trial_transition_invalid")
	}
	if next != nil && previous == nil {
		if next.Version != 1 {
			fail("experience_trial_version_conflict")
		}
		if c.trialsExist() {
			if c.exists("SELECT 1 FROM memory_experience_trials WHERE policy=?", next.PolicyID) {
				fail("experience_trial_consumed")
			}
			r := take(c.session.Query("SELECT count(*) FROM memory_experience_trials"))
			var n int
			if !r.Next() {
				check(r.Err())
				fail("experience_trial_corrupt")
			}
			check(r.Scan(&n))
			check(r.Close())
			if n >= 4096 {
				fail("experience_trial_capacity")
			}
		}
	}
	change := recovery.ExperienceTrial{Operation: operation, PolicyID: identity.PolicyID, SceneID: identity.SceneID, ExpectedPolicyVersion: float64(identity.PolicyVersion), NextPolicyVersion: float64(identity.PolicyVersion), BeforeHash: trialHash(previous, c.owner.ports.Compare), AfterHash: trialHash(next, c.owner.ports.Compare)}
	if previous != nil {
		change.ExpectedPolicyVersion = float64(previous.PolicyVersion)
		change.ExpectedReceiptVersion = ptr(float64(previous.Version))
	}
	if next != nil {
		change.NextPolicyVersion = float64(next.PolicyVersion)
		change.NextReceiptVersion = ptr(float64(next.Version))
		change.NextPhase = ptr(next.Phase)
		if next.Reason != "" {
			change.NextReason = ptr(next.Reason)
		}
	}
	if c.owner.ports.BeforeTrialWrite != nil {
		check(c.owner.ports.BeforeTrialWrite(c.session, change))
	} else {
		check(c.session.RequireReader(9))
		take(c.session.AppendIntent(change))
	}
	check(c.session.EnsureExperienceTrialSchema())
	if apply != nil {
		apply()
	}
	var raw any
	if next != nil {
		raw = encoded(next)
	}
	if previous != nil {
		version := previous.Version + 1
		if next != nil {
			version = next.Version
		}
		c.exec("UPDATE memory_experience_trials SET version=?,value=? WHERE policy=?", version, raw, identity.PolicyID)
	} else {
		c.exec("INSERT INTO memory_experience_trials VALUES(?,?,?,?)", identity.PolicyID, identity.SceneID, identity.Version, raw)
	}
}

func (t Trial) MarshalJSON() ([]byte, error) { return marshalDomain(t) }
func (t *Trial) UnmarshalJSON(raw []byte) error {
	return guarded(func() {
		v := take(jsjson.Parse(string(raw)))
		o, ok := v.(*jsjson.Object)
		if !ok {
			fail("experience_trial_corrupt")
		}
		checkTrialObject(o, []string{"schemaVersion", "policyId", "sceneId", "version", "policyVersion", "evidencePolicyVersion", "policyHash", "requesterId", "settingsRevision", "phase", "createdAt", "expiresAt", "instruction", "validationId", "positives", "reason"})
		for _, k := range []string{"schemaVersion", "version", "policyVersion", "evidencePolicyVersion", "settingsRevision"} {
			checkTrialNumber(o, k)
		}
		instruction, ok := getValue(o, "instruction").(*jsjson.Object)
		if !ok {
			fail("experience_trial_corrupt")
		}
		checkTrialObject(instruction, []string{"id", "version"})
		checkTrialNumber(instruction, "version")
		positives, ok := getValue(o, "positives").([]any)
		if !ok {
			fail("experience_trial_corrupt")
		}
		for _, v := range positives {
			p, ok := v.(*jsjson.Object)
			if !ok {
				fail("experience_trial_corrupt")
			}
			checkTrialObject(p, []string{"outcomeId", "outcomeVersion", "sourceRoot", "targetRoot", "turnId"})
			checkTrialNumber(p, "outcomeVersion")
		}
		check(unmarshalDomain(raw, t))
		validateTrial(*t)
	})
}
func checkTrialObject(o *jsjson.Object, allowed []string) {
	for _, k := range o.Keys() {
		if !contains(allowed, k) {
			fail("experience_trial_corrupt")
		}
	}
}
func checkTrialNumber(o *jsjson.Object, k string) {
	n, ok := getValue(o, k).(float64)
	if !ok || n < 0 || n > 9007199254740991 || float64(int64(n)) != n {
		fail("experience_trial_corrupt")
	}
}

var trialClock = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}T(?:[01]\d|2[0-3]):[0-5]\d:[0-5]\d(?:\.\d+)?(?:Z|[+-](?:[01]\d|2[0-3]):[0-5]\d)$`)

func trialDatetime(v string) bool {
	if !trialClock.MatchString(v) {
		return false
	}
	_, e := time.Parse("2006-01-02", v[:10])
	return e == nil
}
