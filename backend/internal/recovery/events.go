package recovery

import (
	"errors"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"regexp"
)

var hashPattern = regexp.MustCompile(`^[a-f0-9]{64}$`)
var uuidPattern = regexp.MustCompile(`(?i)^[0-9a-f]{8}-[0-9a-f]{4}-[1-8][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)
var uuid4Pattern = regexp.MustCompile(`(?i)^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

func validString(v any) bool {
	s, ok := v.(string)
	if !ok || s == "" {
		return false
	}
	length, err := jsjson.UTF16Len(s)
	return err == nil && length <= 2000
}
func hashString(v any) bool { s, ok := v.(string); return ok && hashPattern.MatchString(s) }
func integer(v any) bool {
	x, ok := v.(float64)
	return ok && x >= 0 && x <= 9007199254740991 && x == float64(int64(x))
}

// ValidateEvent is extended alongside each migrated domain. Unsupported domains
// fail explicitly; a hash-valid unknown payload never grants data access.
func ValidateEvent(payload string) error {
	decoded, err := jsjson.Parse(payload)
	e, ok := validationValue(decoded).(map[string]any)
	if err != nil || !ok {
		return errors.New("recovery_unknown_revocation")
	}
	valid := false
	switch e["kind"] {
	case "source":
		valid = len(e) == 3 && validString(e["sceneId"]) && validString(e["messageId"])
	case "fingerprint":
		valid = len(e) == 2 && hashString(e["value"])
	case "assertion":
		_, boolean := e["terminal"].(bool)
		valid = len(e) == 4 && validString(e["id"]) && integer(e["throughVersion"]) && boolean
	case "security_state":
		revision, ok := e["revision"].(string)
		valid = len(e) == 2 && ok && uuid4Pattern.MatchString(revision)
	case "source_edit_head", "habit_delete", "meme_delete", "meme_state", "experience_delete", "experience_state":
		valid = validDomainEvent(e)
	case "configuration_flags", "scene_flags":
		valid = validConfiguration(e)
	case "assertion_eligibility":
		valid = validEligibility(e)
	case "external_cost_receipt":
		valid = validExternalCost(e)
	case "experience_trial":
		valid = validExperienceTrial(e)
	case "semantic_mutation":
		valid = validSemanticMutation(e)
	case "identity_mutation":
		valid = validIdentityMutation(e)
	case "working_set_pin":
		valid = validWorkingSetPin(e)
	case "commitment_head", "commitment_delete":
		valid = validCommitment(e)
	default:
		return errors.New("recovery_event_domain_not_migrated")
	}
	if !valid {
		return errors.New("recovery_unknown_revocation")
	}
	return nil
}
func validEligibility(e map[string]any) bool {
	op, ok := e["operationId"].(string)
	if !ok || !(uuidPattern.MatchString(op) || op == "00000000-0000-0000-0000-000000000000" || op == "ffffffff-ffff-ffff-ffff-ffffffffffff") || len(e) != 5 || e["schemaVersion"] != float64(1) {
		return false
	}
	mode := e["mode"]
	if mode != "baseline" && mode != "transition" {
		return false
	}
	changes, ok := e["changes"].([]any)
	if !ok || (mode == "transition" && len(changes) == 0) {
		return false
	}
	seen := map[string]bool{}
	for _, raw := range changes {
		change, ok := raw.(map[string]any)
		if !ok || len(change) != 4 {
			return false
		}
		id, ok := change["id"].(string)
		if !ok || id == "" || seen[id] || !hashString(change["incarnationHash"]) {
			return false
		}
		seen[id] = true
		state, ok := change["state"].(map[string]any)
		if !ok || len(state) != 2 {
			return false
		}
		status := state["status"]
		if status != "active" && status != "disputed" && status != "superseded" && status != "invalidated" {
			return false
		}
		lineage, present := state["lineageStatus"]
		if !present || (lineage != nil && lineage != "verified" && lineage != "unverified" && lineage != "quarantined") {
			return false
		}
		material, present := change["materialHash"]
		if !present || (material != nil && !hashString(material)) {
			return false
		}
		denied := status == "invalidated" || lineage == "quarantined"
		if denied != (material == nil) || (mode == "baseline" && !denied) {
			return false
		}
	}
	return true
}

// The validator does not need insertion order, but string keys and values must
// retain canonical WTF-8 identity. Never round-trip through encoding/json here.
func validationValue(value any) any {
	switch v := value.(type) {
	case *jsjson.Object:
		result := make(map[string]any, v.Len())
		for _, member := range v.Members() {
			result[member.Key] = validationValue(member.Value)
		}
		return result
	case []any:
		result := make([]any, len(v))
		for i, item := range v {
			result[i] = validationValue(item)
		}
		return result
	default:
		return value
	}
}
