package replyplan

import (
	"encoding/base64"
	"regexp"
	"strings"

	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"golang.org/x/text/encoding/unicode"
)

var projectionOmitted = stringSet([]string{"receivedAt", "senderAvatarUrl", "avatarUrl", "senderPlatformId", "platformMetadata", "raw", "rawEvent", "proofId", "backingFingerprint", "spanHash", "accountId", "adapterId", "platformSceneId"})
var messageKeys = stringSet([]string{"ids", "messageId", "messageIds", "targetMessageIds", "contextMessageIds", "sourceMessageId", "sourceMessageIds", "replyToId", "originalTarget"})
var userKeys = stringSet([]string{"senderId", "userId", "personId", "botUserId", "recipientId", "participantIds", "confirmedParticipantIds", "missingParticipantIds", "completedParticipantIds", "cancelledParticipantIds"})
var assetKeys = stringSet([]string{"assetId", "assetIds"})
var evidenceKeys = stringSet([]string{"eventId", "evidenceId", "evidenceIds", "stateEvidenceIds", "assertionId", "parents", "memoryIds", "selectedParentIds", "requiredMemoryIds", "entityId", "originEdgeId", "edgeId", "entityIds", "policyId", "memoryId", "candidateId", "habitId", "sourceId", "supersedesId", "mergeIntoId", "targetId", "cursor", "nextCursor"})
var shortReference = regexp.MustCompile(`^[\w.-]{1,32}$`)

// ContextProjection is per-turn state, not safe for concurrent use. Only trusted
// runtime data may be projected/registered. RestoreArguments cannot invent IDs.
type ContextProjection struct {
	SceneID          string
	forward, reverse map[string]string
	counts           map[string]int
}

func NewContextProjection(sceneID string) *ContextProjection {
	return &ContextProjection{SceneID: sceneID, forward: map[string]string{}, reverse: map[string]string{}, counts: map[string]int{}}
}
func decodeReference(value string) any {
	// Buffer.from(base64url) accepts ordinary/base64url alphabets, ignores garbage,
	// stops at padding and tolerates missing padding or an incomplete final sextet.
	units, err := jsjson.UTF16(value)
	if err != nil {
		return nil
	}
	data := []byte{}
	for _, u := range units {
		b := byte(u)
		if b == '=' {
			break
		}
		if b == '-' {
			b = '+'
		} else if b == '_' {
			b = '/'
		}
		if b >= 'A' && b <= 'Z' || b >= 'a' && b <= 'z' || b >= '0' && b <= '9' || b == '+' || b == '/' {
			data = append(data, b)
		}
	}
	if len(data)%4 == 1 {
		data = data[:len(data)-1]
	}
	decoded, err := base64.RawStdEncoding.DecodeString(string(data))
	if err != nil {
		return nil
	}
	text, err := unicode.UTF8.NewDecoder().Bytes(decoded)
	if err != nil {
		return nil
	}
	v, err := jsjson.ParseBytes(text)
	if err != nil {
		return nil
	}
	return v
}
func (p *ContextProjection) register(kind, canonical string) (string, error) {
	key := kind + ":" + canonical
	if existing, ok := p.forward[key]; ok {
		return existing, nil
	}
	var candidate any = canonical
	if kind == "m" && strings.HasPrefix(canonical, "ob:s:") {
		prefix := p.SceneID + ":m:"
		if !strings.HasPrefix(canonical, prefix) {
			return "", errCode("context_reference_cross_scene")
		}
		candidate = decodeReference(canonical[len(prefix):])
	} else if kind == "u" && strings.HasPrefix(canonical, "ob:u:") {
		decoded := array(decodeReference(canonical[5:]))
		candidate = nil
		if len(decoded) == 3 {
			candidate = decoded[2]
		}
	}
	ref := ""
	if s, ok := candidate.(string); kind != "a" && ok && shortReference.MatchString(s) {
		ref = s
	}
	if _, exists := p.reverse[kind+":"+ref]; ref == "" || exists {
		for {
			p.counts[kind]++
			ref = kind + integerString(p.counts[kind])
			if _, exists := p.reverse[kind+":"+ref]; !exists {
				break
			}
		}
	}
	p.forward[key] = ref
	p.reverse[kind+":"+ref] = canonical
	return ref, nil
}
func (p *ContextProjection) restore(kind, ref string) (string, error) {
	v, ok := p.reverse[kind+":"+ref]
	if !ok {
		return "", errCode("context_reference_unknown")
	}
	return v, nil
}
func (p *ContextProjection) walk(value any, inbound bool, field string, parent *Object, idKind string) (any, error) {
	kind := ""
	switch {
	case field == "paths" && get(parent, "interpretation") == "attributed_claims_not_transitive_truth":
		kind = "e"
	case messageKeys[field]:
		kind = "m"
	case userKeys[field]:
		kind = "u"
	case assetKeys[field]:
		kind = "a"
	case evidenceKeys[field]:
		kind = "e"
	case field == "id":
		if has(parent, "parts") && has(parent, "senderId") {
			kind = "m"
		} else if has(parent, "format") && has(parent, "tags") {
			kind = "a"
		} else {
			kind = idKind
			if kind == "" {
				kind = "e"
			}
		}
	}
	if s, ok := value.(string); ok {
		if field == "topicId" {
			if !inbound {
				return p.register("e", s)
			}
			if known := p.reverse["e:"+s]; known != "" {
				return known, nil
			}
			if shortReference.MatchString(s) {
				return s, nil
			}
			return nil, errCode("context_reference_unknown")
		}
		if field == "sceneId" {
			if inbound {
				if s != "scene" {
					return nil, errCode("context_reference_cross_scene")
				}
				return p.SceneID, nil
			}
			if s != p.SceneID {
				return nil, errCode("context_reference_cross_scene")
			}
			return "scene", nil
		}
		if kind != "" {
			if inbound {
				return p.restore(kind, s)
			}
			return p.register(kind, s)
		}
		return s, nil
	}
	if a, ok := value.([]any); ok {
		out := make([]any, len(a))
		for i, item := range a {
			v, err := p.walk(item, inbound, field, parent, idKind)
			if err != nil {
				return nil, err
			}
			out[i] = v
		}
		return out, nil
	}
	o := object(value)
	if o == nil {
		return value, nil
	}
	if !inbound && isString(get(o, "sceneId")) && get(o, "sceneId") != p.SceneID {
		return nil, errCode("context_reference_cross_scene")
	}
	out := obj()
	for _, m := range o.Members() {
		if !inbound && (projectionOmitted[m.Key] || m.Key == "sceneId" && has(o, "parts") && has(o, "senderId")) {
			continue
		}
		k := idKind
		if m.Key == "value" {
			if get(o, "toolName") == "memes.search" {
				k = "a"
			} else {
				name, err := propertyString(o, "toolName")
				if err != nil {
					return nil, err
				}
				if includes([]string{"messages.get", "messages.around", "messages.byUser"}, name) {
					k = "m"
				}
			}
		}
		v, err := p.walk(m.Value, inbound, m.Key, o, k)
		if err != nil {
			return nil, err
		}
		set(out, m.Key, v)
	}
	return out, nil
}
func (p *ContextProjection) Project(value any) (any, error) { return p.walk(value, false, "", nil, "") }
func (p *ContextProjection) RestoreArguments(value any) (any, error) {
	return p.walk(value, true, "", nil, "")
}
func (p *ContextProjection) Stringify(value any) (string, error) {
	return p.StringifyTrailing(value, nil)
}
func (p *ContextProjection) StringifyTrailing(value any, trailingFields []string) (string, error) {
	projected, err := p.Project(value)
	if err != nil {
		return "", err
	}
	o := object(projected)
	if len(trailingFields) == 0 || o == nil {
		return stringify(projected)
	}
	out := obj()
	trailing := stringSet(trailingFields)
	for _, m := range o.Members() {
		if !trailing[m.Key] {
			set(out, m.Key, m.Value)
		}
	}
	for _, m := range o.Members() {
		if trailing[m.Key] {
			set(out, m.Key, m.Value)
		}
	}
	return stringify(out)
}
