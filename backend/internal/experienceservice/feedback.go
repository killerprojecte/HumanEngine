package experienceservice

import (
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/sourceevents"
	"golang.org/x/text/unicode/norm"
	"regexp"
	"strings"
)

// FeedbackMessage contains only the live adapter fields read by the source
// feedback adapter. The runtime caller must supply the authorized prior bot
// message, not an arbitrary message identified by an untrusted tool argument.
type FeedbackMessage struct {
	eventRefRaw                    *rawReference
	SceneID                        string            `json:"sceneId"`
	ID                             string            `json:"id"`
	SenderID                       string            `json:"senderId"`
	Origin                         string            `json:"origin"`
	Status                         string            `json:"status"`
	ReceivedAt                     string            `json:"receivedAt"`
	ReplyToID                      string            `json:"replyToId"`
	SourceEventRegistrationLimited bool              `json:"sourceEventRegistrationLimited,omitempty"`
	SourceEventRef                 *sourceevents.Ref `json:"-"`
	Parts                          []FeedbackPart    `json:"parts"`
}
type FeedbackPart struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
}
type AppliedPolicyFeedback struct {
	PolicyRef Reference `json:"policyRef"`
	Operation string    `json:"operation"`
}

var feedbackClock = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d+)?(?:Z|[+-]\d{2}:\d{2})$`)

func feedbackSpace(u uint16) bool {
	return u == 9 || u == 11 || u == 12 || u == 32 || u == 160 || u == 0xfeff || u == 10 || u == 13 || u == 0x2028 || u == 0x2029 || u == 0x1680 || (u >= 0x2000 && u <= 0x200a) || u == 0x202f || u == 0x205f || u == 0x3000
}
func normalizeFeedback(text string) string {
	units, _ := jsjson.UTF16(text)
	var b strings.Builder
	start := 0
	for i := 0; i < len(units); i++ {
		if units[i] >= 0xd800 && units[i] <= 0xdbff && i+1 < len(units) && units[i+1] >= 0xdc00 && units[i+1] <= 0xdfff {
			i++
			continue
		}
		if units[i] >= 0xd800 && units[i] <= 0xdfff {
			b.WriteString(norm.NFKC.String(jsjson.FromUTF16(units[start:i])))
			b.WriteString(jsjson.FromUTF16(units[i : i+1]))
			start = i + 1
		}
	}
	b.WriteString(norm.NFKC.String(jsjson.FromUTF16(units[start:])))
	units, _ = jsjson.UTF16(b.String())
	next := make([]uint16, 0, len(units))
	for _, u := range units {
		if !feedbackSpace(u) && u != '，' && u != ',' {
			next = append(next, u)
		}
	}
	for len(next) > 0 {
		u := next[len(next)-1]
		if u != '。' && u != '.' && u != '!' && u != '！' && u != '?' && u != '？' {
			break
		}
		next = next[:len(next)-1]
	}
	return jsjson.FromUTF16(next)
}
func validFeedbackReference(r *Reference) bool {
	return r != nil && r.ID != "" && r.Version > 0 && r.Version <= maxSafeVersion
}
func DeriveExplicitPolicyFeedback(scene string, message FeedbackMessage, prior *FeedbackMessage, selected *Reference, applied *AppliedPolicyFeedback) *Feedback {
	if prior == nil || message.SceneID != scene || prior.SceneID != scene || message.SourceEventRegistrationLimited || prior.SourceEventRegistrationLimited || message.Origin != "live" || message.Status != "available" || message.SenderID == "bot" || prior.Origin != "live" || prior.Status != "available" || prior.SenderID != "bot" || message.ReplyToID != prior.ID || message.ID == prior.ID {
		return nil
	}
	current, ok := dateMillis(message.ReceivedAt)
	before, bok := dateMillis(prior.ReceivedAt)
	if !feedbackClock.MatchString(message.ReceivedAt) || !feedbackClock.MatchString(prior.ReceivedAt) || !ok || !bok || before > current {
		return nil
	}
	if len(message.Parts) == 0 {
		return nil
	}
	var b strings.Builder
	for _, p := range message.Parts {
		if p.Type != "text" {
			return nil
		}
		b.WriteString(p.Text)
	}
	text := b.String()
	n, e := jsjson.UTF16Len(text)
	if e != nil || n > 80 {
		return nil
	}
	s := normalizeFeedback(text)
	code, operation := "", ""
	switch s {
	case "这次先查我的记忆有帮助":
		code = "user_reported_helpful"
		operation = "memory_person"
	case "这次先核对计划条件有帮助":
		code = "user_reported_helpful"
		operation = "plan_qualifiers_checked"
	case "这次先查我的记忆不合适":
		code = "counterexample"
		operation = "memory_person"
	case "这次先核对计划条件不合适":
		code = "counterexample"
		operation = "plan_qualifiers_checked"
	case "请先说结论", "先说结论", "先给结论再给细节", "请先给结论再给细节":
		code = "request_conclusion_first"
	case "涉及我的问题先查我的记忆", "涉及我的问题请先查我的记忆":
		code = "request_exact_person_lookup"
	case "回答计划问题前先核对条件", "回答计划问题前请先核对条件":
		code = "request_verify_plan_qualifiers"
	case "取消先查我的记忆", "不要再先查我的记忆":
		code = "withdraw_person_instruction"
	case "取消先核对计划条件", "不要再先核对计划条件":
		code = "withdraw_plan_instruction"
	case "不要先说结论", "取消先说结论", "不要再先说结论", "别先说结论", "取消先给结论再给细节", "不要再先给结论再给细节":
		code = "withdraw_instruction"
	default:
		return nil
	}
	if operation != "" && (strings.Contains(text, "?") || strings.Contains(text, "？")) {
		return nil
	}
	withdrawal := member(code, "withdraw_instruction", "withdraw_person_instruction", "withdraw_plan_instruction")
	if withdrawal && !validFeedbackReference(selected) {
		return nil
	}
	if operation != "" && (applied == nil || applied.Operation != operation || !validFeedbackReference(&applied.PolicyRef)) {
		return nil
	}
	out := &Feedback{Source: Source{MessageID: message.ID, SenderID: message.SenderID}, Target: Target{MessageID: prior.ID}, Code: code, ObservedAt: message.ReceivedAt}
	if referenceTruthy(eventRefValue(message.SourceEventRef, message.eventRefRaw)) {
		if message.SourceEventRef != nil {
			ref := *message.SourceEventRef
			out.Source.EventRef = &ref
		}
		out.Source.eventRefRaw = message.eventRefRaw
	}
	if referenceTruthy(eventRefValue(prior.SourceEventRef, prior.eventRefRaw)) {
		if prior.SourceEventRef != nil {
			ref := *prior.SourceEventRef
			out.Target.EventRef = &ref
		}
		out.Target.eventRefRaw = prior.eventRefRaw
	}
	if withdrawal {
		ref := *selected
		out.PolicyRef = &ref
	}
	if operation != "" {
		ref := applied.PolicyRef
		out.PolicyRef = &ref
	}
	return out
}

func (m FeedbackMessage) MarshalJSON() ([]byte, error)  { return marshalDomain(m) }
func (m *FeedbackMessage) UnmarshalJSON(b []byte) error { return unmarshalDomain(b, m) }
