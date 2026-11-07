package recovery

// These are history validators, not implementations of domain mutations or
// recovery replay. The transition rules match the corresponding source parsers.
func positiveVersion(value any) bool {
	return integer(value) && value.(float64) > 0
}

func nullableVersion(value any) bool { return value == nil || positiveVersion(value) }
func nullableHash(value any) bool    { return value == nil || hashString(value) }

func validExperienceTrial(event map[string]any) bool {
	if !exactKeys(event, []string{"kind", "schemaVersion", "operation", "policyId", "sceneId", "expectedPolicyVersion", "nextPolicyVersion", "expectedReceiptVersion", "nextReceiptVersion", "beforeHash", "afterHash"}, "nextPhase", "nextReason") ||
		event["schemaVersion"] != float64(1) || !oneOf(event["operation"], "start", "collect", "promote", "suspend", "expire", "erase") ||
		!codePointID(event["policyId"], 512) || !codePointID(event["sceneId"], 512) ||
		!positiveVersion(event["expectedPolicyVersion"]) || !positiveVersion(event["nextPolicyVersion"]) ||
		!nullableVersion(event["expectedReceiptVersion"]) || !nullableVersion(event["nextReceiptVersion"]) ||
		!nullableHash(event["beforeHash"]) || !nullableHash(event["afterHash"]) {
		return false
	}
	phase, hasPhase := event["nextPhase"]
	if hasPhase && !oneOf(phase, "trial", "promoted", "suspended", "expired") {
		return false
	}
	if reason, present := event["nextReason"]; present && !oneOf(reason, "explicit_instruction_trial", "user_reported_helpfulness", "negative_feedback", "expiry", "qualification_changed", "instruction_withdrawn") {
		return false
	}
	operation := event["operation"].(string)
	before, after := event["expectedReceiptVersion"], event["nextReceiptVersion"]
	if operation == "start" {
		if before != nil || after != float64(1) {
			return false
		}
	} else if before == nil || (operation != "erase" && after != before.(float64)+1) {
		return false
	}
	if (operation == "erase") != (after == nil) || (before == nil) != (event["beforeHash"] == nil) ||
		(after == nil) != (event["afterHash"] == nil) || (after != nil) != hasPhase {
		return false
	}
	if operation == "promote" {
		return event["nextPolicyVersion"] == event["expectedPolicyVersion"].(float64)+1 && phase == "promoted"
	}
	if event["nextPolicyVersion"] != event["expectedPolicyVersion"] {
		return false
	}
	switch operation {
	case "start", "collect":
		return phase == "trial"
	case "suspend":
		return phase == "suspended"
	case "expire":
		return phase == "expired"
	default:
		return true
	}
}

func validSemanticMutation(event map[string]any) bool {
	if !exactKeys(event, []string{"kind", "schemaVersion", "operation", "id", "sceneId", "previousVersion", "nextVersion", "beforeHash", "afterHash"}, "nextState") ||
		event["schemaVersion"] != float64(1) || !oneOf(event["operation"], "admit", "review", "privacy", "attempt") ||
		!codePointID(event["id"], 512) || !codePointID(event["sceneId"], 512) ||
		!nullableVersion(event["previousVersion"]) || !nullableVersion(event["nextVersion"]) ||
		!nullableHash(event["beforeHash"]) || !nullableHash(event["afterHash"]) {
		return false
	}
	stateValue, hasState := event["nextState"]
	var state map[string]any
	if hasState {
		var ok bool
		state, ok = stateValue.(map[string]any)
		if !ok || !exactKeys(state, []string{"status", "admission"}) ||
			!oneOf(state["status"], "candidate", "admitted", "rejected") ||
			!oneOf(state["admission"], "qualified_candidate", "structural_low_risk_summary", "administrator_review") {
			return false
		}
	}
	before, after := event["previousVersion"], event["nextVersion"]
	if (before == nil) != (event["beforeHash"] == nil) || (after == nil) != (event["afterHash"] == nil) {
		return false
	}
	operation := event["operation"].(string)
	if (operation == "admit" || operation == "review") != hasState {
		return false
	}
	switch operation {
	case "admit":
		return before == nil && after == float64(1)
	case "review":
		return before != nil && after == before.(float64)+1 && state["admission"] == "administrator_review" && state["status"] != "candidate"
	case "attempt":
		return hashString(event["id"]) && ((before == nil && after == float64(1)) || (before != nil && after == before.(float64)+1))
	default:
		return before != nil && (after == nil || after == before.(float64)+1)
	}
}

func validIdentityMutation(event map[string]any) bool {
	if !exactKeys(event, []string{"kind", "schemaVersion", "operation", "operationId", "expectedRevision", "nextRevision", "beforeHash", "qualificationHash", "targets"}) ||
		event["schemaVersion"] != float64(1) || !oneOf(event["operation"], "link", "unlink", "split", "alias_put", "alias_remove", "source_recall") ||
		!codePointID(event["operationId"], 160) || !integer(event["expectedRevision"]) || !integer(event["nextRevision"]) ||
		event["nextRevision"] != event["expectedRevision"].(float64)+1 || !hashString(event["beforeHash"]) || !hashString(event["qualificationHash"]) {
		return false
	}
	targets, ok := event["targets"].(map[string]any)
	if !ok || !exactKeys(targets, []string{"endpoints", "aliasIds"}, "source") {
		return false
	}
	endpoints, ok := targets["endpoints"].([]any)
	if !ok || len(endpoints) > 32 {
		return false
	}
	for _, raw := range endpoints {
		endpoint, ok := raw.(map[string]any)
		if !ok || !exactKeys(endpoint, []string{"adapterId", "accountId", "personId"}) ||
			!codePointID(endpoint["adapterId"], 160) || !codePointID(endpoint["accountId"], 160) || !codePointID(endpoint["personId"], 160) {
			return false
		}
	}
	aliases, ok := targets["aliasIds"].([]any)
	if !ok || len(aliases) > 4096 {
		return false
	}
	for _, alias := range aliases {
		if !codePointID(alias, 160) {
			return false
		}
	}
	sourceValue, hasSource := targets["source"]
	if hasSource {
		source, ok := sourceValue.(map[string]any)
		if !ok || !exactKeys(source, []string{"sceneId", "messageId"}) || !codePointID(source["sceneId"], 160) || !codePointID(source["messageId"], 160) {
			return false
		}
	}
	// The source parser does not require distinct endpoints/aliases, and permits
	// source on alias_put and nonempty aliasIds on source_recall.
	switch event["operation"] {
	case "link", "unlink":
		return len(endpoints) == 2 && len(aliases) == 0 && !hasSource
	case "split":
		return len(endpoints) > 0 && len(aliases) == 0 && !hasSource
	case "alias_put":
		return len(endpoints) == 1 && len(aliases) == 1
	case "alias_remove":
		return len(endpoints) == 0 && len(aliases) == 1 && !hasSource
	default:
		return len(endpoints) == 0 && hasSource
	}
}

func validWorkingSetPin(event map[string]any) bool {
	if !exactKeys(event, []string{"kind", "schemaVersion", "policy"}) || event["schemaVersion"] != float64(1) {
		return false
	}
	policy, ok := event["policy"].(map[string]any)
	if !ok || !exactKeys(policy, []string{"schemaVersion", "sceneId", "selection", "policyRevision", "pinned", "terminal"}) ||
		policy["schemaVersion"] != float64(1) || !codePointID(policy["sceneId"], 512) || !positiveVersion(policy["policyRevision"]) {
		return false
	}
	pinned, pinOK := policy["pinned"].(bool)
	terminal, terminalOK := policy["terminal"].(bool)
	if !pinOK || !terminalOK || (terminal && pinned) {
		return false
	}
	selection, ok := policy["selection"].(map[string]any)
	if !ok || !exactKeys(selection, []string{"ref", "dependencyKey"}) || !hashString(selection["dependencyKey"]) {
		return false
	}
	ref, ok := selection["ref"].(map[string]any)
	if !ok || !codePointID(ref["id"], 512) || !positiveVersion(ref["version"]) || !integer(ref["deletionEpoch"]) {
		return false
	}
	switch ref["kind"] {
	case "assertion":
		return exactKeys(ref, []string{"kind", "id", "version", "deletionEpoch"})
	case "commitment":
		return exactKeys(ref, []string{"kind", "id", "version", "deletionEpoch", "mutationVersion"}) && positiveVersion(ref["mutationVersion"])
	default:
		return false
	}
}
