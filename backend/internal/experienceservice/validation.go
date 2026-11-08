package experienceservice

import (
	_ "embed"
	"encoding/json"
	"errors"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"strings"
)

var actionTemplates = map[string]string{
	"exact_person_before_semantic": "For this resolved person question, prefer the existing exact scoped person lookup before semantic expansion. Existing authorization and evidence rules still apply.",
	"verify_plan_qualifiers":       "For this plan question, verify the available agreement, negation and conditions before summarizing. Preserve uncertainty; do not infer consent or permission.",
	"conclusion_before_detail":     "When appropriate, state the supported conclusion briefly before supporting details. Do not omit essential uncertainty, negation, conditions or safety constraints.",
}

func RenderExperienceAction(action string) (string, error) {
	s, ok := actionTemplates[action]
	if !ok {
		return "", errors.New("experience_action_unsupported")
	}
	return s, nil
}
func IsExperienceActionCondition(action string, c Condition) bool {
	keys := c.value().Keys()
	if action == "verify_plan_qualifiers" {
		return c.Kind == "plan_question" && len(keys) == 1 && keys[0] == "kind"
	}
	kind := ""
	if action == "exact_person_before_semantic" {
		kind = "person_question"
	} else if action == "conclusion_before_detail" {
		kind = "response_style"
	}
	n, e := jsjson.UTF16Len(c.PersonID)
	return kind != "" && c.Kind == kind && e == nil && n > 0 && n <= 512 && len(keys) == 2 && member("kind", keys...) && member("personId", keys...)
}

type OperationInput struct {
	Action       string         `json:"action"`
	Condition    Condition      `json:"condition"`
	Tool         string         `json:"tool"`
	Arguments    map[string]any `json:"arguments"`
	Value        any            `json:"value"`
	SemanticRead bool           `json:"semanticRead"`
	TurnValid    bool           `json:"turnValid"`
}

func AppliedExperienceOperation(in OperationInput) string {
	rows, ok := in.Value.([]any)
	if !in.TurnValid || !ok || len(rows) == 0 {
		return ""
	}
	if in.Tool == "memory.person" && !in.SemanticRead && in.Action == "exact_person_before_semantic" && in.Condition.Kind == "person_question" && in.Arguments["personId"] == in.Condition.PersonID {
		return "memory_person"
	}
	if in.Tool != "memory.relations" || in.Action != "verify_plan_qualifiers" || in.Condition.Kind != "plan_question" {
		return ""
	}
	evidence := false
	for _, row := range rows {
		var coverage, items any
		switch r := row.(type) {
		case map[string]any:
			coverage = r["relationCoverage"]
			items = r["relationEvidence"]
		case *jsjson.Object:
			coverage = getValue(r, "relationCoverage")
			items = getValue(r, "relationEvidence")
		default:
			return ""
		}
		a, ok := items.([]any)
		if coverage != "complete" || !ok {
			return ""
		}
		evidence = evidence || len(a) > 0
	}
	if evidence {
		return "plan_qualifiers_checked"
	}
	return ""
}

//go:embed testdata/behavior-holdouts.json
var behaviorJSON []byte

type BehaviorFixture struct {
	ID       string         `json:"id"`
	Input    OperationInput `json:"input"`
	Expected string         `json:"expected,omitempty"`
}
type BehaviorResult struct {
	ID     string `json:"id"`
	Passed bool   `json:"passed"`
}

func ExperienceBehaviorHoldouts() []BehaviorFixture {
	var v []BehaviorFixture
	if e := json.Unmarshal(behaviorJSON, &v); e != nil {
		panic(e)
	}
	return v
}
func RunExperienceBehaviorHoldouts() []BehaviorResult {
	v := ExperienceBehaviorHoldouts()
	r := make([]BehaviorResult, len(v))
	for i, x := range v {
		r[i] = BehaviorResult{x.ID, AppliedExperienceOperation(x.Input) == x.Expected}
	}
	return r
}

var mechanicalChecks = []string{"candidate_shape", "exact_semantic_hash", "action_condition_whitelist", "support_lineage", "holdout_root_disjoint", "holdout_family_disjoint", "bounded_expiry", "no_counterexamples", "fixed_protocol_holdouts", "fixed_operation_behavior_holdouts", "fixed_non_authoritative_templates"}
var mechanicalActions = []string{"exact_person_before_semantic", "verify_plan_qualifiers", "conclusion_before_detail"}

type protocolFixture struct {
	id, action string
	condition  Condition
	expected   bool
}

var protocolFixtures = []protocolFixture{
	{"exact-person", "exact_person_before_semantic", Condition{Kind: "person_question", PersonID: "holdout-person"}, true},
	{"plan-qualifiers", "verify_plan_qualifiers", Condition{Kind: "plan_question"}, true},
	{"style-scoped", "conclusion_before_detail", Condition{Kind: "response_style", PersonID: "holdout-person"}, true},
	{"cross-condition", "conclusion_before_detail", Condition{Kind: "plan_question"}, false},
	{"person-unresolved", "exact_person_before_semantic", Condition{Kind: "person_question", PersonID: ""}, false},
}

func stringsValid(v []string, nonempty bool) bool {
	if v == nil || len(v) > 64 || (nonempty && len(v) == 0) {
		return false
	}
	seen := map[string]bool{}
	for _, s := range v {
		n, e := jsjson.UTF16Len(s)
		if e != nil || n == 0 || n > 512 || seen[s] {
			return false
		}
		seen[s] = true
	}
	return true
}
func ValidateExperiencePolicy(c Policy) Receipt {
	failures := []string{}
	fail := func(name string, passed bool) {
		if !passed {
			failures = append(failures, name)
		}
	}
	roots, families := []string{}, []string{}
	if stringsValid(c.GenerationRoots, false) {
		roots = append(roots, c.GenerationRoots...)
	}
	if stringsValid(c.GenerationFamilies, false) {
		families = append(families, c.GenerationFamilies...)
	}
	heldRoots, heldFamilies := []string{}, []string{}
	protocol := []any{}
	for _, f := range protocolFixtures {
		protocol = append(protocol, object("id", f.id, "action", f.action, "condition", f.condition.value(), "expected", f.expected))
		heldRoots = append(heldRoots, "holdout-root:"+digestValue(f.id))
		heldFamilies = append(heldFamilies, "engineering-family:"+digestValue(f.id))
	}
	behavior, _ := jsjson.Parse(string(behaviorJSON))
	for _, f := range ExperienceBehaviorHoldouts() {
		heldRoots = append(heldRoots, "holdout-root:"+digestValue(f.ID))
		heldFamilies = append(heldFamilies, "engineering-family:"+digestValue(f.ID))
	}
	templates := []any{}
	for _, a := range mechanicalActions {
		templates = append(templates, []any{a, actionTemplates[a]})
	}
	validID := func(s string) bool { n, e := jsjson.UTF16Len(s); return e == nil && n > 0 && n <= 512 }
	fail("candidate_shape", !c.unknownFields && validID(c.ID) && c.Version >= 1 && c.Version <= maxSafeVersion && validID(c.SceneID) && member(c.Status, "candidate", "validated", "active", "suspended", "retired") && (c.ActivationBasis == "" || member(c.ActivationBasis, "explicit_preference", "administrator_review")))
	fail("exact_semantic_hash", c.Hash == CandidateHash(c))
	fail("action_condition_whitelist", IsExperienceActionCondition(c.Action, c.Condition))
	fail("support_lineage", stringsValid(c.SupportIDs, true) && stringsValid(c.GenerationRoots, true) && stringsValid(c.GenerationFamilies, true))
	fail("holdout_root_disjoint", disjoint(roots, heldRoots))
	fail("holdout_family_disjoint", disjoint(families, heldFamilies))
	start, sok := dateMillis(c.CreatedAt)
	end, eok := dateMillis(c.ExpiresAt)
	fail("bounded_expiry", sok && eok && end > start && end-start <= 366*86400000)
	fail("no_counterexamples", c.CounterexampleIDs != nil && len(c.CounterexampleIDs) == 0)
	protocolOK := true
	for _, f := range protocolFixtures {
		protocolOK = protocolOK && IsExperienceActionCondition(f.action, f.condition) == f.expected
	}
	fail("fixed_protocol_holdouts", protocolOK)
	behaviorOK := true
	for _, r := range RunExperienceBehaviorHoldouts() {
		behaviorOK = behaviorOK && r.Passed
	}
	fail("fixed_operation_behavior_holdouts", behaviorOK)
	templatesOK := true
	for _, a := range mechanicalActions {
		n, e := jsjson.UTF16Len(actionTemplates[a])
		templatesOK = templatesOK && e == nil && n <= 500 && !strings.Contains(actionTemplates[a], "holdout-person")
	}
	fail("fixed_non_authoritative_templates", templatesOK)
	return Receipt{CandidateID: c.ID, CandidateVersion: c.Version, CandidateHash: c.Hash, Tier: "mechanical_only", RuleVersion: "experience-mechanical-v2", RuleHash: digestValue(object("ruleVersion", "experience-mechanical-v2", "checks", stringsValue(mechanicalChecks), "templates", templates)), DatasetVersion: "experience-fixed-behavior-holdout-v2", DatasetHash: digestValue(object("protocol", protocol, "behavior", behavior)), GenerationRoots: roots, GenerationFamilies: families, HeldoutRoots: heldRoots, HeldoutFamilies: heldFamilies, Checks: append([]string{}, mechanicalChecks...), Failures: failures, Passed: len(failures) == 0}
}
