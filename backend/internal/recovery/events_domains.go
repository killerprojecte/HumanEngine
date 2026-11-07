package recovery

import "github.com/killerprojecte/HumanEngine/backend/internal/jsjson"

func codePointID(value any, maximum int) bool {
	text, ok := value.(string)
	if !ok || text == "" {
		return false
	}
	length, err := jsjson.CodePointCount(text)
	return err == nil && length <= maximum
}
func oneOf(value any, options ...string) bool {
	text, ok := value.(string)
	if !ok {
		return false
	}
	for _, option := range options {
		if text == option {
			return true
		}
	}
	return false
}
func exactKeys(value map[string]any, required []string, optional ...string) bool {
	allowed := map[string]bool{}
	for _, key := range required {
		if _, ok := value[key]; !ok {
			return false
		}
		allowed[key] = true
	}
	for _, key := range optional {
		allowed[key] = true
	}
	for key := range value {
		if !allowed[key] {
			return false
		}
	}
	return true
}
func validDomainEvent(event map[string]any) bool {
	if event["schemaVersion"] != float64(1) {
		return false
	}
	switch event["kind"] {
	case "source_edit_head":
		if !exactKeys(event, []string{"kind", "schemaVersion", "sceneId", "messageId", "eventId", "revision", "deletionEpoch", "identityHash", "headHash"}) {
			return false
		}
		return codePointID(event["sceneId"], 512) && codePointID(event["messageId"], 512) && codePointID(event["eventId"], 512) && integer(event["revision"]) && event["revision"].(float64) > 0 && integer(event["deletionEpoch"]) && hashString(event["identityHash"]) && hashString(event["headHash"])
	case "habit_delete":
		return exactKeys(event, []string{"kind", "schemaVersion", "id", "fingerprint"}) && codePointID(event["id"], 2000) && hashString(event["fingerprint"])
	case "meme_delete":
		return exactKeys(event, []string{"kind", "schemaVersion", "id", "contentHash", "qualificationHash", "format"}) && hashString(event["id"]) && hashString(event["contentHash"]) && hashString(event["qualificationHash"]) && oneOf(event["format"], "png", "jpeg", "gif", "webp")
	case "meme_state":
		if !exactKeys(event, []string{"kind", "schemaVersion", "id", "contentHash", "qualificationHash", "state"}) || !hashString(event["id"]) || !hashString(event["contentHash"]) || !hashString(event["qualificationHash"]) {
			return false
		}
		state, ok := event["state"].(map[string]any)
		if !ok || len(state) != 5 {
			return false
		}
		for _, key := range []string{"available", "disabled", "knownOnly", "shared", "privacyReviewed"} {
			if _, ok := state[key].(bool); !ok {
				return false
			}
		}
		return true
	case "experience_delete":
		return exactKeys(event, []string{"kind", "schemaVersion", "sceneId", "id"}) && codePointID(event["sceneId"], 2000) && codePointID(event["id"], 2000)
	case "experience_state":
		if !exactKeys(event, []string{"kind", "schemaVersion", "sceneId", "id", "qualificationHash", "version", "status", "reason"}, "validationId", "activationBasis") || !codePointID(event["sceneId"], 2000) || !codePointID(event["id"], 2000) || !hashString(event["qualificationHash"]) || !integer(event["version"]) || event["version"].(float64) <= 0 || !oneOf(event["status"], "active", "suspended", "retired") || !oneOf(event["reason"], "experimental_mechanical_hint", "review_suspended", "review_retired", "explicit_counterexample", "explicit_instruction_withdrawn", "support_or_expiry_invalid") {
			return false
		}
		if value, ok := event["validationId"]; ok && !codePointID(value, 2000) {
			return false
		}
		if value, ok := event["activationBasis"]; ok && !oneOf(value, "explicit_preference", "administrator_review") {
			return false
		}
		return true
	}
	return false
}

var configurationIntroduced = map[string]int{
	"cacheEnabled": 1, "debugModelIO": 1, "memoryProjectionEnabled": 1, "memoryGraphEnabled": 1, "memoryGraphAutoAdmissionEnabled": 1, "memoryGraphAnswerEnabled": 1, "memoryGraphShadowEnabled": 1, "memoryExperienceEnabled": 1, "memoryExperienceAutoActivateEnabled": 1, "memoryExperienceHintsEnabled": 1,
	"sourceEventRevisionsEnabled": 2, "memoryCommitmentsEnabled": 3, "memoryCommitmentsAnswerEnabled": 3, "memoryHistoricalReadEnabled": 3, "memoryWorkingSetEnabled": 4, "memoryEpisodesEnabled": 4, "topicAdmissionEnabled": 5, "memoryKnowledgeEnabled": 6, "memoryKnowledgeAutoAdmissionEnabled": 6, "memoryKnowledgeAnswerEnabled": 6, "memorySemanticEnabled": 7, "memorySemanticAutoAdmissionEnabled": 7, "memorySemanticAnswerEnabled": 7, "memoryExperienceFeedbackEnabled": 8,
}

func validConfiguration(event map[string]any) bool {
	if !integer(event["schemaVersion"]) {
		return false
	}
	version := int(event["schemaVersion"].(float64))
	flags, ok := event["flags"].(map[string]any)
	if !ok {
		return false
	}
	var introduced map[string]int
	if event["kind"] == "configuration_flags" {
		if version < 1 || version > 8 || !exactKeys(event, []string{"kind", "schemaVersion", "flags"}) {
			return false
		}
		introduced = configurationIntroduced
	} else {
		if version < 1 || version > 2 || !exactKeys(event, []string{"kind", "schemaVersion", "flags", "sceneId", "identityHash"}) || !validString(event["sceneId"]) || !configurationHash(event["identityHash"]) {
			return false
		}
		introduced = map[string]int{"participationEnabled": 1, "learningEnabled": 1, "proactiveEnabled": 1, "memoryReadEnabled": 2, "personalizationEnabled": 2}
	}
	count := 0
	for key, since := range introduced {
		if since <= version {
			count++
			if _, ok := flags[key].(bool); !ok {
				return false
			}
		}
	}
	return count == len(flags)
}

// The legacy manual scene-flags parser applies RegExp.test, which coerces JSON
// arrays to strings. Preserve that read contract; do not change the event value.
func configurationHash(value any) bool {
	for {
		array, ok := value.([]any)
		if !ok {
			return hashString(value)
		}
		if len(array) != 1 {
			return false
		}
		value = array[0]
	}
}
