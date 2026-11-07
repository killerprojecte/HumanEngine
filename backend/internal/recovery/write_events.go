package recovery

import (
	"errors"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"reflect"
	"strconv"
	"strings"
)

// Revocation is a supported, typed authority intent, not a replay instruction.
// The sealed interface prevents unknown domains from bypassing ValidateEvent.
// Existing history is never decoded and rewritten by the writer. Inputs must
// use canonical WTF-8 for JavaScript strings. Decode historical JSON with
// jsjson first: encoding/json would already have replaced lone surrogates.
// This is not a general migration/import writer.
type Revocation interface{ recoveryRecord() (string, any) }

type Source struct {
	SceneID   string `json:"sceneId"`
	MessageID string `json:"messageId"`
}

func (e Source) recoveryRecord() (string, any) {
	return "source", struct {
		Kind string `json:"kind"`
		Source
	}{"source", e}
}

type Fingerprint struct {
	Value string `json:"value"`
}

func (e Fingerprint) recoveryRecord() (string, any) {
	return "fingerprint", struct {
		Kind string `json:"kind"`
		Fingerprint
	}{"fingerprint", e}
}

type Assertion struct {
	ID             string `json:"id"`
	ThroughVersion int64  `json:"throughVersion"`
	Terminal       bool   `json:"terminal"`
}

func (e Assertion) recoveryRecord() (string, any) {
	return "assertion", struct {
		Kind string `json:"kind"`
		Assertion
	}{"assertion", e}
}

type SecurityState struct {
	Revision string `json:"revision"`
}

func (e SecurityState) recoveryRecord() (string, any) {
	return "security_state", struct {
		Kind string `json:"kind"`
		SecurityState
	}{"security_state", e}
}

type SourceEditHead struct {
	SceneID       string `json:"sceneId"`
	MessageID     string `json:"messageId"`
	EventID       string `json:"eventId"`
	Revision      int64  `json:"revision"`
	DeletionEpoch int64  `json:"deletionEpoch"`
	IdentityHash  string `json:"identityHash"`
	HeadHash      string `json:"headHash"`
}

func (e SourceEditHead) recoveryRecord() (string, any) {
	return "source_edit_head", struct {
		Kind          string `json:"kind"`
		SchemaVersion int    `json:"schemaVersion"`
		SourceEditHead
	}{"source_edit_head", 1, e}
}

type EligibilityState struct {
	Status        string  `json:"status"`
	LineageStatus *string `json:"lineageStatus"`
}
type EligibilityChange struct {
	ID              string           `json:"id"`
	IncarnationHash string           `json:"incarnationHash"`
	State           EligibilityState `json:"state"`
	MaterialHash    *string          `json:"materialHash"`
}
type AssertionEligibility struct {
	OperationID string              `json:"operationId"`
	Mode        string              `json:"mode"`
	Changes     []EligibilityChange `json:"changes"`
}

func (e AssertionEligibility) recoveryRecord() (string, any) {
	if e.Changes == nil {
		e.Changes = []EligibilityChange{}
	}
	return "assertion_eligibility", struct {
		Kind          string `json:"kind"`
		SchemaVersion int    `json:"schemaVersion"`
		AssertionEligibility
	}{"assertion_eligibility", 1, e}
}

type ConfigurationFlags struct {
	SchemaVersion int             `json:"schemaVersion"`
	Flags         map[string]bool `json:"flags"`
}

func (e ConfigurationFlags) recoveryRecord() (string, any) {
	return "configuration_flags", struct {
		Kind          string       `json:"kind"`
		SchemaVersion int          `json:"schemaVersion"`
		Flags         orderedFlags `json:"flags"`
	}{"configuration_flags", e.SchemaVersion, orderedFlags{e.Flags, configurationWriteOrder}}

}

type SceneFlags struct {
	SchemaVersion int             `json:"schemaVersion"`
	SceneID       string          `json:"sceneId"`
	IdentityHash  string          `json:"identityHash"`
	Flags         map[string]bool `json:"flags"`
}

func (e SceneFlags) recoveryRecord() (string, any) {
	return "scene_flags", struct {
		Kind          string       `json:"kind"`
		SchemaVersion int          `json:"schemaVersion"`
		SceneID       string       `json:"sceneId"`
		IdentityHash  string       `json:"identityHash"`
		Flags         orderedFlags `json:"flags"`
	}{"scene_flags", e.SchemaVersion, e.SceneID, e.IdentityHash, orderedFlags{e.Flags, sceneWriteOrder}}

}

// Match configurationFlags()/sceneFlags() insertion order used by Store,
// rather than encoding/json's alphabetical map ordering. Exact payload bytes
// are part of the chain hash. Missing/version-incompatible keys are validated
// by ValidateEvent; unknown keys must never disappear during serialization.
var configurationWriteOrder = []string{
	"memoryExperienceFeedbackEnabled",
	"memorySemanticEnabled", "memorySemanticAutoAdmissionEnabled", "memorySemanticAnswerEnabled",
	"memoryKnowledgeEnabled", "memoryKnowledgeAutoAdmissionEnabled", "memoryKnowledgeAnswerEnabled",
	"topicAdmissionEnabled", "memoryWorkingSetEnabled", "memoryEpisodesEnabled",
	"memoryCommitmentsEnabled", "memoryCommitmentsAnswerEnabled", "memoryHistoricalReadEnabled",
	"sourceEventRevisionsEnabled", "cacheEnabled", "debugModelIO", "memoryProjectionEnabled",
	"memoryGraphEnabled", "memoryGraphAutoAdmissionEnabled", "memoryGraphAnswerEnabled", "memoryGraphShadowEnabled",
	"memoryExperienceEnabled", "memoryExperienceAutoActivateEnabled", "memoryExperienceHintsEnabled",
}
var sceneWriteOrder = []string{"participationEnabled", "learningEnabled", "proactiveEnabled", "memoryReadEnabled", "personalizationEnabled"}

type orderedFlags struct {
	values map[string]bool
	keys   []string
}

func (flags orderedFlags) MarshalJSON() ([]byte, error) {
	output := []byte{'{'}
	count := 0
	for _, key := range flags.keys {
		value, present := flags.values[key]
		if !present {
			continue
		}
		if count > 0 {
			output = append(output, ',')
		}
		output = strconv.AppendQuote(output, key)
		output = append(output, ':')
		output = strconv.AppendBool(output, value)
		count++
	}
	if count != len(flags.values) {
		return nil, errors.New("recovery_unknown_revocation")
	}
	return append(output, '}'), nil
}

type HabitDelete struct {
	ID          string `json:"id"`
	Fingerprint string `json:"fingerprint"`
}

func (e HabitDelete) recoveryRecord() (string, any) {
	return "habit_delete", struct {
		SchemaVersion int    `json:"schemaVersion"`
		Kind          string `json:"kind"`
		HabitDelete
	}{1, "habit_delete", e}
}

type MemeDelete struct {
	ID                string `json:"id"`
	ContentHash       string `json:"contentHash"`
	QualificationHash string `json:"qualificationHash"`
	Format            string `json:"format"`
}

func (e MemeDelete) recoveryRecord() (string, any) {
	return "meme_delete", struct {
		SchemaVersion int    `json:"schemaVersion"`
		Kind          string `json:"kind"`
		MemeDelete
	}{1, "meme_delete", e}
}

type MemeFlags struct {
	Available       bool `json:"available"`
	Disabled        bool `json:"disabled"`
	KnownOnly       bool `json:"knownOnly"`
	Shared          bool `json:"shared"`
	PrivacyReviewed bool `json:"privacyReviewed"`
}
type MemeState struct {
	ID                string    `json:"id"`
	ContentHash       string    `json:"contentHash"`
	QualificationHash string    `json:"qualificationHash"`
	State             MemeFlags `json:"state"`
}

func (e MemeState) recoveryRecord() (string, any) {
	return "meme_state", struct {
		SchemaVersion int    `json:"schemaVersion"`
		Kind          string `json:"kind"`
		MemeState
	}{1, "meme_state", e}
}

type ExperienceDelete struct {
	SceneID string `json:"sceneId"`
	ID      string `json:"id"`
}

func (e ExperienceDelete) recoveryRecord() (string, any) {
	return "experience_delete", struct {
		SchemaVersion int    `json:"schemaVersion"`
		Kind          string `json:"kind"`
		ExperienceDelete
	}{1, "experience_delete", e}
}

type ExperienceState struct {
	SceneID           string  `json:"sceneId"`
	ID                string  `json:"id"`
	QualificationHash string  `json:"qualificationHash"`
	Version           int64   `json:"version"`
	Status            string  `json:"status"`
	ValidationID      *string `json:"validationId,omitempty"`
	ActivationBasis   *string `json:"activationBasis,omitempty"`
	Reason            string  `json:"reason"`
}

func (e ExperienceState) recoveryRecord() (string, any) {
	return "experience_state", struct {
		SchemaVersion int    `json:"schemaVersion"`
		Kind          string `json:"kind"`
		ExperienceState
	}{1, "experience_state", e}
}

type writeEvent struct {
	payload      string
	alwaysAppend bool
}

func encodeRevocation(event Revocation) (writeEvent, error) {
	if nilRevocation(event) || !validWriteStrings(reflect.ValueOf(event)) {
		return writeEvent{}, errors.New("recovery_write_string_unsupported")
	}
	kind, record := event.recoveryRecord()
	value, err := eventJSONValue(reflect.ValueOf(record))
	if err != nil {
		return writeEvent{}, err
	}
	encoded, err := jsjson.Stringify(value)
	if err != nil {
		return writeEvent{}, err
	}
	if err := ValidateEvent(encoded); err != nil {
		return writeEvent{}, err
	}
	repeated := kind == "assertion_eligibility" || kind == "configuration_flags" || kind == "scene_flags" || kind == "meme_state" || kind == "experience_state"
	return writeEvent{encoded, repeated}, nil
}

// Only canonical WTF-8 strings enter the JS-compatible typed encoder.
func validWriteStrings(value reflect.Value) bool {
	switch value.Kind() {
	case reflect.Interface, reflect.Pointer:
		if value.IsNil() {
			return value.Kind() != reflect.Interface
		}
		return validWriteStrings(value.Elem())
	case reflect.String:
		return jsjson.ValidateString(value.String()) == nil
	case reflect.Struct:
		for i := 0; i < value.NumField(); i++ {
			if !validWriteStrings(value.Field(i)) {
				return false
			}
		}
	case reflect.Array, reflect.Slice:
		for i := 0; i < value.Len(); i++ {
			if !validWriteStrings(value.Index(i)) {
				return false
			}
		}
	case reflect.Map:
		iter := value.MapRange()
		for iter.Next() {
			if !validWriteStrings(iter.Key()) || !validWriteStrings(iter.Value()) {
				return false
			}
		}
	}
	return true
}

// This encoder is private to the sealed recovery record types. Struct declaration
// order and embedded fields follow their original JSON protocol; arbitrary maps
// are deliberately unsupported. Flags use their explicit source-defined order.
func eventJSONValue(v reflect.Value) (any, error) {
	if v.IsValid() && v.CanInterface() {
		if flags, ok := v.Interface().(orderedFlags); ok {
			object := new(jsjson.Object)
			count := 0
			for _, key := range flags.keys {
				if value, present := flags.values[key]; present {
					if err := object.Set(key, value); err != nil {
						return nil, err
					}
					count++
				}
			}
			if count != len(flags.values) {
				return nil, errors.New("recovery_unknown_revocation")
			}
			return object, nil
		}
	}
	switch v.Kind() {
	case reflect.Interface, reflect.Pointer:
		if v.IsNil() {
			return nil, nil
		}
		return eventJSONValue(v.Elem())
	case reflect.String:
		return v.String(), jsjson.ValidateString(v.String())
	case reflect.Bool:
		return v.Bool(), nil
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return float64(v.Int()), nil
	case reflect.Slice, reflect.Array:
		if v.Kind() == reflect.Slice && v.IsNil() {
			return nil, nil
		}
		result := make([]any, v.Len())
		for i := range result {
			value, err := eventJSONValue(v.Index(i))
			if err != nil {
				return nil, err
			}
			result[i] = value
		}
		return result, nil
	case reflect.Struct:
		result := new(jsjson.Object)
		typ := v.Type()
		for i := 0; i < v.NumField(); i++ {
			field := typ.Field(i)
			tag := field.Tag.Get("json")
			parts := strings.Split(tag, ",")
			name := parts[0]
			if name == "-" {
				continue
			}
			if len(parts) > 1 && parts[1] == "omitempty" && v.Field(i).IsZero() {
				continue
			}
			child, err := eventJSONValue(v.Field(i))
			if err != nil {
				return nil, err
			}
			if field.Anonymous && name == "" {
				embedded, ok := child.(*jsjson.Object)
				if !ok {
					return nil, errors.New("recovery_record_shape_invalid")
				}
				for _, member := range embedded.Members() {
					if err = result.Set(member.Key, member.Value); err != nil {
						return nil, err
					}
				}
				continue
			}
			if name == "" {
				name = field.Name
			}
			if err = result.Set(name, child); err != nil {
				return nil, err
			}
		}
		return result, nil
	}
	return nil, errors.New("recovery_record_shape_invalid")
}
