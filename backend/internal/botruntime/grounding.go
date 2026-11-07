package botruntime

import (
	"encoding/json"
	"regexp"
	"sort"
	"strings"

	"github.com/killerprojecte/HumanEngine/backend/internal/dispatch"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
)

// Grounding is the source ReplyGrounding's per-turn state. A turn owns it
// sequentially and must not copy it after first use. Evidence and tool values use jsjson's ordered JSON/WTF-8 model;
// message parts have already passed the runtime's message/send boundary.
// These are the source's bounded factual rules, not a general truth classifier.
type Grounding struct {
	evidence         strings.Builder
	searchedMemes    bool
	feedbackSources  map[string]string
	feedbackReceipts map[string]map[string]groundReceipt
}

type groundReceipt struct{ positive, disabled bool }

func NewGrounding() *Grounding {
	return &Grounding{feedbackSources: map[string]string{}, feedbackReceipts: map[string]map[string]groundReceipt{}}
}

func (g *Grounding) HasFeedbackReceipts() bool { return len(g.feedbackReceipts) > 0 }
func (g *Grounding) IsFeedbackTarget(id string) bool {
	return id != "" && g.feedbackReceipts[id] != nil
}

// GroundingText concatenates only text parts. Join UTF-16 units before
// decoding: separate parts can supply the two halves of a surrogate pair.
func GroundingText(parts []json.RawMessage) string {
	return groundJoinText(parts, "")
}

func (g *Grounding) AddFeedbackSource(id string, parts []json.RawMessage) {
	if g.feedbackSources == nil {
		g.feedbackSources = map[string]string{}
	}
	g.feedbackSources[id] = GroundingText(parts)
}

// ObserveCoreHistory registers only the validated top-level core history
// records. It deliberately does not recurse or add chronology evidence.
func (g *Grounding) ObserveCoreHistory(result any, sceneID, mode string) {
	if mode != "real" {
		return
	}
	items, _ := groundGet(result, "items").([]any)
	for _, item := range items {
		if !groundHistoryItem(item, sceneID) {
			continue
		}
		parts, _ := groundGet(item, "parts").([]any)
		raw := make([]json.RawMessage, len(parts))
		for i, part := range parts {
			raw[i] = groundJSON(part)
		}
		g.AddFeedbackSource(groundString(groundGet(item, "id")), raw)
	}
}

func (g *Grounding) NormalizeFeedback(segments []dispatch.Segment) []dispatch.Segment {
	out := make([]dispatch.Segment, len(segments))
	copy(out, segments)
	for i, segment := range segments {
		if segment.ReplyToID == nil || *segment.ReplyToID == "" {
			continue
		}
		receipts := g.feedbackReceipts[*segment.ReplyToID]
		if len(receipts) == 0 {
			continue
		}
		locale := groundEnglish
		if groundHasHan(g.feedbackSources[*segment.ReplyToID]) {
			locale = groundChinese
		}
		text := locale.aggregate
		if len(receipts) == 1 {
			for _, receipt := range receipts {
				text = locale.negative
				if receipt.positive {
					text = locale.positive
				}
				if receipt.disabled {
					text += " " + locale.disabled
				}
			}
		}
		part, err := jsjson.NewObject(jsjson.Member{Key: "type", Value: "text"}, jsjson.Member{Key: "text", Value: text})
		if err != nil {
			panic(err)
		}
		out[i].Parts = []json.RawMessage{groundJSON(part)}
	}
	return out
}

// AddEvidence removes quoted examples exactly as the source does. It neither
// truncates evidence nor adds extra safety scans or inference rules.
func (g *Grounding) AddEvidence(value any) {
	switch v := value.(type) {
	case string:
		g.evidence.WriteByte('\n')
		g.evidence.WriteString(groundQuoted.ReplaceAllString(groundUnits(v), ""))
	case []any:
		for _, item := range v {
			g.AddEvidence(item)
		}
	case *jsjson.Object:
		if v != nil {
			for _, item := range v.Members() {
				g.AddEvidence(item.Value)
			}
		}
	}
}

func (g *Grounding) ObserveTool(name string, output any) {
	if name == "memes.search" {
		g.searchedMemes = true
	}
	if name == "memes.feedback" {
		positive, positiveOK := groundGet(output, "positive").(bool)
		disabled, disabledOK := groundGet(output, "disabled").(bool)
		asset, source := groundString(groundGet(output, "assetId")), groundString(groundGet(output, "sourceMessageId"))
		_, known := g.feedbackSources[source]
		if groundGet(output, "ok") == true && groundGet(output, "actionTaken") == "ranking_adjusted" && groundGet(output, "scope") == "asset" && asset != "" && source != "" && positiveOK && disabledOK && known {
			if g.feedbackReceipts == nil {
				g.feedbackReceipts = map[string]map[string]groundReceipt{}
			}
			receipts := g.feedbackReceipts[source]
			if receipts == nil {
				receipts = map[string]groundReceipt{}
				g.feedbackReceipts[source] = receipts
			}
			key := asset + ":false"
			if positive {
				key = asset + ":true"
			}
			receipts[key] = groundReceipt{positive: positive, disabled: disabled}
		}
	}
	if (strings.HasPrefix(name, "messages.") && name != "messages.send") || name == "memory.search" || name == "memory.person" {
		g.AddEvidence(output)
	}
}

// Check returns the first source diagnostic, or the empty string on success.
// Ordering is significant: target binding, all chronology clauses, each
// inventory/delivery clause in order, then selected-image visual relations.
func (g *Grounding) Check(segments []dispatch.Segment) string {
	if g.HasFeedbackReceipts() {
		for _, segment := range segments {
			if segment.ReplyToID == nil || *segment.ReplyToID == "" {
				return "feedback_target_required"
			}
		}
		for _, segment := range segments {
			if _, ok := g.feedbackSources[*segment.ReplyToID]; !ok {
				return "feedback_target_unknown"
			}
		}
	}
	parts := make([]json.RawMessage, 0)
	for _, segment := range segments {
		parts = append(parts, segment.Parts...)
	}
	text := groundUnits(groundJoinText(parts, "\n"))
	for _, part := range groundClauses.FindAllString(text, -1) {
		clause := strings.TrimFunc(part, groundSpace)
		folded := groundASCII(clause)
		if groundNonAssertion.MatchString(folded) {
			continue
		}
		if groundChronology.MatchString(folded) && !strings.Contains(g.evidence.String(), clause) {
			return "unsupported_event_chronology"
		}
	}
	folded := groundASCII(text)
	for _, clause := range groundSplit.Split(folded, -1) {
		queryScoped := groundQuery.MatchString(clause)
		inventoryScope := groundInventory.MatchString(clause)
		absence := groundAbsence.MatchString(clause)
		if g.searchedMemes && ((inventoryScope && absence && !groundInventoryQuery.MatchString(clause)) || (!queryScoped && groundOtherImages.MatchString(clause))) {
			return "search_is_not_inventory_proof"
		}
		if groundFuture.MatchString(clause) && groundDelivery.MatchString(clause) && !groundNonCommitment.MatchString(clause) {
			return "unscheduled_future_delivery"
		}
	}
	describes := groundSelectedImage.MatchString(folded)
	if g.searchedMemes && !describes {
		for _, part := range parts {
			if groundGet(groundParse(part), "type") == "media" {
				describes = true
				break
			}
		}
	}
	if g.searchedMemes && describes && groundVisualRelation.MatchString(folded) {
		return "unsupported_visual_relation"
	}
	return ""
}

// GroundingFallback uses the latest non-bot message's concatenated text. The
// caller owns choosing that message; no-text and unsupported scripts use English.
func GroundingFallback(lastText string) string {
	if groundHasHan(lastText) {
		return groundChinese.fallback
	}
	return groundEnglish.fallback
}

type groundLocale struct{ negative, positive, disabled, aggregate, fallback string }

func groundHasHan(s string) bool {
	for _, r := range s {
		i := sort.Search(len(groundHanRanges), func(i int) bool { return groundHanRanges[i][1] >= r })
		if i < len(groundHanRanges) && groundHanRanges[i][0] <= r {
			return true
		}
	}
	return false
}

func groundGet(value any, key string) any {
	if o, ok := value.(*jsjson.Object); ok && o != nil {
		v, _ := o.Get(key)
		return v
	}
	return nil
}
func groundString(value any) string { s, _ := value.(string); return s }
func groundParse(raw json.RawMessage) any {
	v, err := jsjson.ParseBytes(raw)
	if err != nil {
		panic(err)
	}
	return v
}
func groundJSON(v any) json.RawMessage {
	raw, err := jsjson.Marshal(v)
	if err != nil {
		panic(err)
	}
	return raw
}
func groundJoinText(parts []json.RawMessage, separator string) string {
	var units []uint16
	first := true
	for _, raw := range parts {
		part := groundParse(raw)
		if groundGet(part, "type") != "text" {
			continue
		}
		if !first {
			for _, c := range separator {
				units = append(units, uint16(c))
			}
		}
		first = false
		u, err := jsjson.UTF16(groundString(groundGet(part, "text")))
		if err != nil {
			panic(err)
		}
		units = append(units, u...)
	}
	return jsjson.FromUTF16(units)
}

// The source's non-u regexes operate on UTF-16 code units. Project each unit
// to one valid Go rune; surrogate units occupy a disjoint private range. This
// preserves dot widths, lone-surrogate identity and exact substring matching.
func groundUnits(s string) string {
	units, err := jsjson.UTF16(s)
	if err != nil {
		panic(err)
	}
	var out strings.Builder
	for _, unit := range units {
		r := rune(unit)
		if unit >= 0xd800 && unit <= 0xdfff {
			r += 0xf0000
		}
		out.WriteRune(r)
	}
	return out.String()
}
func groundASCII(s string) string {
	return strings.Map(func(r rune) rune {
		if r >= 'A' && r <= 'Z' {
			return r + ('a' - 'A')
		}
		return r
	}, s)
}
func groundSpace(r rune) bool {
	return r == 0x9 || r == 0xb || r == 0xc || r == 0x20 || r == 0xa0 || r == 0xfeff || r == 0xa || r == 0xd || r == 0x2028 || r == 0x2029 || r == 0x1680 || (r >= 0x2000 && r <= 0x200a) || r == 0x202f || r == 0x205f || r == 0x3000
}

func groundHistoryItem(v any, sceneID string) bool {
	for _, key := range []string{"id", "senderId", "sentAt", "receivedAt"} {
		if groundString(groundGet(v, key)) == "" {
			return false
		}
	}
	if groundGet(v, "sceneId") != sceneID || groundGet(v, "status") != "available" {
		return false
	}
	if origin := groundGet(v, "origin"); origin != "live" && origin != "history" {
		return false
	}
	parts, ok := groundGet(v, "parts").([]any)
	if !ok {
		return false
	}
	for _, part := range parts {
		if !groundHistoryPart(part) {
			return false
		}
	}
	return true
}
func groundHistoryPart(v any) bool {
	o, ok := v.(*jsjson.Object)
	if !ok || o == nil {
		return false
	}
	switch groundGet(o, "type") {
	case "text":
		s, ok := groundGet(o, "text").(string)
		if !ok || o.Len() != 2 {
			return false
		}
		n, err := jsjson.CodePointCount(s)
		return err == nil && n >= 1 && n <= 12000
	case "mention":
		return o.Len() == 2 && groundString(groundGet(o, "userId")) != ""
	case "media":
		_, mediaOK := groundGet(o, "mediaType").(string)
		return o.Len() == 3 && groundString(groundGet(o, "assetId")) != "" && mediaOK
	case "unsupported":
		_, ok := groundGet(o, "description").(string)
		return ok
	default:
		return false
	}
}

// All case-insensitive source patterns use only ASCII/Han literals. ASCII fold
// matches their non-u ECMAScript /i semantics; Go (?i) would incorrectly admit
// long-s and Kelvin-sign matches. JS dot also excludes all four line terminators.
func groundRE(pattern string) *regexp.Regexp {
	pattern = strings.ReplaceAll(pattern, ".{", `[^\n\r\x{2028}\x{2029}]{`)
	return regexp.MustCompile(groundASCII(pattern))
}

var (
	groundQuoted         = regexp.MustCompile(`“[^”]*”|「[^」]*」|『[^』]*』|"[^"\n]*"`)
	groundClauses        = regexp.MustCompile(`[^，。！？,!?;；\n]+[？?]?`)
	groundNonAssertion   = groundRE(`[？?]$|怎么样|是否|如果|假如|可能|也许|记得|建议|不确定|不知道|\b(?:if|perhaps|maybe|could|might|remember to)\b`)
	groundChronology     = groundRE(`今天上午|今天早上|今天下午|下午就|this morning|this afternoon`)
	groundSplit          = groundRE(`[，。！？,!?;；\n]|但是|不过|但|\b(?:but|however)\b`)
	groundQuery          = groundRE(`这次|本次|当前检索|搜索结果|检索结果|this (?:search|query)|these results`)
	groundInventory      = groundRE(`图库|库里|库存|存货|这边|手头|library|inventory|in stock|on hand`)
	groundAbsence        = groundRE(`没有|没别|只有|仅有|只剩|no |none|only|nothing|out of`)
	groundInventoryQuery = groundRE(`(?:图库|库存|库里).{0,6}(?:搜索|检索|查询)(?:结果)?|(?:library|inventory) (?:search|query) results`)
	groundOtherImages    = groundRE(`没有(?:其他|别的).{0,12}(?:图|表情)|no other.{0,20}(?:image|meme)`)
	groundFuture         = groundRE(`改天|明天|稍后|晚点|以后|下次|过会|later|tomorrow|next time`)
	groundDelivery       = groundRE(`(?:给你|给大家|给你们).{0,12}(?:发|补|送)|(?:发|补|送).{0,8}(?:给你|一张|表情|图)|(?:I(?:'ll| will)|I'll).{0,30}(?:send|post)|send you`)
	groundNonCommitment  = groundRE(`你(?:下次|再|到时|之后)*.{0,4}(?:问我|找我|请求)|(?:if|when) you (?:ask|request)|要不要|祝你|祝大家|祝愿|希望|你(?:打算|计划|说)|[？?]|\b(?:would you|hope|wish|you plan)\b`)
	groundSelectedImage  = groundRE(`这张|这只|图里|画面|this (?:image|picture|cat|meme)`)
	groundVisualRelation = groundRE(`捧着|抱着|拿着|握着|叼着|戴着|旁边.{0,6}(?:有|是)|头上|脚下|holding|wearing|beside|on (?:its|their) head`)
)
