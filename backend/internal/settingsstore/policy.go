// Package settingsstore implements the candidate Store.set('settings', value)
// persistence boundary. It does not replace transport SettingsSchema validation.
package settingsstore

import (
	"crypto/rand"
	"errors"
	"fmt"
	"math"

	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/recovery"
)

const maxSafeInteger = 9007199254740991

var configurationKeys = []string{
	"memoryExperienceFeedbackEnabled",
	"memorySemanticEnabled", "memorySemanticAutoAdmissionEnabled", "memorySemanticAnswerEnabled",
	"memoryKnowledgeEnabled", "memoryKnowledgeAutoAdmissionEnabled", "memoryKnowledgeAnswerEnabled",
	"topicAdmissionEnabled", "memoryWorkingSetEnabled", "memoryEpisodesEnabled",
	"memoryCommitmentsEnabled", "memoryCommitmentsAnswerEnabled", "memoryHistoricalReadEnabled",
	"sourceEventRevisionsEnabled", "cacheEnabled", "debugModelIO", "memoryProjectionEnabled",
	"memoryGraphEnabled", "memoryGraphAutoAdmissionEnabled", "memoryGraphAnswerEnabled", "memoryGraphShadowEnabled",
	"memoryExperienceEnabled", "memoryExperienceAutoActivateEnabled", "memoryExperienceHintsEnabled",
}

func configurationFlags(value any) map[string]bool {
	flags := make(map[string]bool, len(configurationKeys))
	for _, key := range configurationKeys {
		flag, ok := property(value, key).(bool)
		if key == "cacheEnabled" {
			flags[key] = !ok || flag
		} else {
			flags[key] = ok && flag
		}
	}
	return flags
}
func feedbackEnabled(value any) bool {
	v, ok := property(value, "memoryExperienceFeedbackEnabled").(bool)
	return ok && v
}
func policyFlags(value any, prefix string) string {
	values := []any{}
	for _, suffix := range []string{"Enabled", "AutoAdmissionEnabled", "AnswerEnabled"} {
		v := property(value, prefix+suffix)
		if v == nil {
			v = false
		}
		values = append(values, v)
	}
	raw, _ := jsjson.Stringify(values)
	return raw
}

type configUpdate struct{ key, raw string }
type plan struct {
	updates         []configUpdate
	intents         []recovery.Revocation
	securityChanged bool
	// Parsing occurs after configuration revocation in the original transaction.
	// Keep that ordering even when security projection rejects the write.
	afterFlagsError error
}

func prepare(previous, input any, readRevision func(string) (any, error)) (plan, error) {
	result := plan{}
	policies := []struct {
		key, message string
		changed      bool
	}{
		{"memory-experience-feedback-revision", "experience_feedback_policy_invalid", feedbackEnabled(previous) != feedbackEnabled(input)},
		{"memory-knowledge-policy-revision", "knowledge_policy_invalid", policyFlags(previous, "memoryKnowledge") != policyFlags(input, "memoryKnowledge")},
		{"memory-semantic-policy-revision", "semantic_policy_invalid", policyFlags(previous, "memorySemantic") != policyFlags(input, "memorySemantic")},
	}
	for _, policy := range policies {
		if !policy.changed {
			continue
		}
		value, err := readRevision(policy.key)
		if err != nil {
			return plan{}, err
		}
		revision, ok := value.(float64)
		if !ok || math.IsNaN(revision) || math.IsInf(revision, 0) || math.Trunc(revision) != revision || revision < 0 || revision >= maxSafeInteger {
			return plan{}, errors.New(policy.message)
		}
		raw, _ := jsjson.Stringify(revision + 1)
		result.updates = append(result.updates, configUpdate{policy.key, raw})
	}
	before, after := configurationFlags(previous), configurationFlags(input)
	for _, key := range configurationKeys {
		if before[key] != after[key] {
			result.intents = append(result.intents, recovery.ConfigurationFlags{SchemaVersion: 8, Flags: after})
			break
		}
	}
	previousBudget, err := budgetProjection(previous)
	if err != nil {
		result.afterFlagsError = err
		return result, nil
	}
	nextBudget, err := budgetProjection(input)
	if err != nil {
		result.afterFlagsError = err
		return result, nil
	}
	previousRaw, _ := jsjson.Stringify(previousBudget)
	nextRaw, _ := jsjson.Stringify(nextBudget)
	result.securityChanged = previousRaw != nextRaw
	return result, nil
}

func securityRevision() (string, error) {
	var bytes [16]byte
	if _, err := rand.Read(bytes[:]); err != nil {
		return "", err
	}
	bytes[6] = (bytes[6] & 0x0f) | 0x40
	bytes[8] = (bytes[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", bytes[0:4], bytes[4:6], bytes[6:8], bytes[8:10], bytes[10:]), nil
}

// Snapshot a validated jsjson graph without replacing non-finite numbers or
// lone surrogate strings. Stringify validates cycles/types before this runs.
func clone(value any) any {
	switch v := value.(type) {
	case *jsjson.Object:
		o := object()
		for _, m := range v.Members() {
			_ = o.Set(m.Key, clone(m.Value))
		}
		return o
	case []any:
		a := make([]any, len(v))
		for i, item := range v {
			a[i] = clone(item)
		}
		return a
	default:
		return value
	}
}
