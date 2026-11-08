package learningbatch

import (
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsstring"
	"github.com/killerprojecte/HumanEngine/backend/internal/memorylexical"
	"github.com/killerprojecte/HumanEngine/backend/internal/providerwire"
	"github.com/killerprojecte/HumanEngine/backend/internal/sourceadmission"
	"regexp"
	"strings"
	"unicode/utf8"
)

func toObject(v any) (*Object, error) {
	if o, ok := v.(*Object); ok {
		return o, nil
	}
	raw, e := providerwire.EncodeJSON(v)
	if e != nil {
		return nil, e
	}
	x, e := jsjson.ParseBytes(raw)
	if e != nil {
		return nil, e
	}
	o, _ := x.(*Object)
	return o, nil
}
func subjectMessages(messages []*Object, parts bool) []any {
	out := []any{}
	for _, m := range messages {
		v := m.Clone()
		mentions, texts := []any{}, []any{}
		for i, p := range arr(get(m, "parts")) {
			if get(p, "type") == "mention" && get(p, "userId") != "all" && get(p, "userId") != "bot" {
				mentions = append(mentions, obj("personId", get(p, "userId"), "mentionPartIndex", float64(i)))
			}
			if get(p, "type") == "text" {
				texts = append(texts, obj("partIndex", float64(i), "text", get(p, "text")))
			}
		}
		if parts {
			_ = v.Set("textParts", texts)
		}
		if len(mentions) > 0 {
			_ = v.Set("subjectMentions", mentions)
		}
		out = append(out, v)
	}
	return out
}
func (b *batch) payload(messages []*Object) *Object {
	return obj("now", b.now(), "timezone", b.settings.Timezone, "messages", subjectMessages(messages, b.settings.KnowledgeEnabled))
}

var quoted = regexp.MustCompile("(?s)```.*?```|~~~.*?~~~|`[^`\\n]*`|“[^”]*”|‘[^’]*’|「[^」]*」|『[^』]*』|\"[^\"\\n]*\"")
var denySignal = regexp.MustCompile(`(?i)如果|假如|假設|假设|玩笑|尚未|未能|不能|不(?:是|要|需)?(?:确认|確認|更正)|\b(?:if|suppose|joking|not confirm|cannot confirm)\b`)
var durableSignal = regexp.MustCompile(`(?i)(?:正式更正|更正如下|正式修正|我(?:已|独立|獨立|亲自|親自)+(?:确认|確認|核实|核實))[^。！？!?\n]{4,}|\b(?:correction:|I (?:independently|personally) (?:confirm|verified|confirmed))[\t\n\v\f\r \x{00A0}\x{1680}\x{2000}-\x{200A}\x{2028}\x{2029}\x{202F}\x{205F}\x{3000}\x{FEFF}]+[^\r\n\x{2028}\x{2029}]{8,}`)

func ExplicitDurableSignal(text string) bool {
	plain := trim(unquoted(text))
	n, _ := jsjson.UTF16Len(plain)
	// JS /iu word boundaries include the two non-ASCII characters that fold
	// into ASCII word letters. RE2's boundary alone is ASCII-only.
	folded := strings.NewReplacer("ſ", "s", "K", "k").Replace(plain)
	return n >= 12 && !denySignal.MatchString(folded) && durableSignal.MatchString(folded)
}
func messageText(m *Object, sep string) string {
	p := []string{}
	for _, v := range arr(get(m, "parts")) {
		if get(v, "type") == "text" {
			p = append(p, str(get(v, "text")))
		}
	}
	return strings.Join(p, sep)
}
func (b *batch) eligible(m *Object) (bool, error) {
	if get(m, "sceneId") != b.scene || get(m, "origin") != "live" || get(m, "status") != "available" || get(m, "senderId") == "bot" {
		return false, nil
	}
	r, e := b.r.d.Reads.Recalled(b.ctx, b.scene, str(get(m, "id")))
	return !r, e
}
func firstEight(c []call) []call {
	if len(c) > 8 {
		return c[:8]
	}
	return c
}
func (b *batch) extract() ([]call, bool, error) {
	name := candidateName(b.settings)
	calls, e := b.observedComplete("learning.extract", "learning", "learning_review", "candidateCount", map[string]int{"inputCount": len(b.messages)}, b.prompt("extraction0"), b.payload(b.messages), []map[string]any{tool("learning.propose", "Propose a sourced memory candidate", b.r.schemas.raw[name]), tool("habits.propose", "Propose sourced soft scene habits or structured bot expression feedback only", b.r.schemas.raw["habit"])})
	if e != nil {
		return nil, false, e
	}
	review := []*Object{}
	for _, m := range b.messages {
		ok, e := b.eligible(m)
		if e != nil {
			return nil, false, e
		}
		if ok && ExplicitDurableSignal(messageText(m, " ")) {
			review = append(review, m)
		}
	}
	extra := false
	if len(calls) == 0 && len(review) > 0 {
		extra = true
		b.r.log("info", "learning_empty_extraction_review_started")
		if len(review) > 8 {
			review = review[:8]
			b.r.log("warn", "learning_empty_extraction_review_capped")
		}
		payload := obj("now", b.now(), "purpose", "source-only-empty-extraction-review", "timezone", b.settings.Timezone, "messages", subjectMessages(review, b.settings.KnowledgeEnabled))
		calls, e = b.observedComplete("learning.review", "", "learning_reconcile", "candidateCount", map[string]int{"inputCount": len(review)}, b.prompt("extraction1"), payload, []map[string]any{tool("learning.review_empty", "Review an omitted explicit durable correction or independent confirmation", b.r.schemas.raw[name])})
		if e != nil {
			return nil, true, e
		}
		if len(calls) > 8 {
			b.r.log("warn", "learning_empty_extraction_review_capped")
		}
		filtered := []call{}
		allowed := []any{}
		for _, m := range review {
			allowed = append(allowed, get(m, "id"))
		}
		for _, c := range firstEight(calls) {
			v, e := b.r.schemas.parse(c.Arguments, name)
			valid := e == nil && c.Name == "learning.review_empty"
			if valid {
				for _, id := range arr(get(v, "sourceMessageIds")) {
					if !contains(allowed, id) {
						valid = false
					}
				}
			}
			if valid {
				filtered = append(filtered, call{"learning.propose", c.Arguments})
			} else {
				b.r.log("warn", "learning_empty_extraction_review_rejected")
			}
		}
		calls = filtered
		b.r.log("info", "learning_empty_extraction_review_completed")
	} else {
		reason := "no_durable_review_signal"
		if len(calls) > 0 {
			reason = "candidates_already_extracted"
		}
		b.skipped("learning.review", reason, "learning_repair")
	}
	return firstEight(calls), extra, nil
}
func (b *batch) repair(calls []call, extra bool) error {
	rejected := []any{}
	eligible := map[int]bool{}
	for i, c := range calls {
		if c.Name != "learning.propose" {
			continue
		}
		v, e := b.r.schemas.parse(c.Arguments, candidateName(b.settings))
		if e != nil {
			continue
		}
		bad, e := b.r.d.Reads.UnsupportedPronouns(b.ctx, b.scene, v, b.evidence)
		if e != nil {
			return e
		}
		if !bad {
			continue
		}
		sources := []*Object{}
		ids := map[string]bool{}
		for _, m := range b.messages {
			ok, e := b.eligible(m)
			if e != nil {
				return e
			}
			if ok && contains(arr(get(v, "sourceMessageIds")), get(m, "id")) {
				sources = append(sources, m)
				ids[str(get(m, "id"))] = true
			}
		}
		unique := map[string]bool{}
		for _, id := range arr(get(v, "sourceMessageIds")) {
			unique[str(id)] = true
		}
		if len(ids) != len(unique) {
			continue
		}
		candidate := withSources(v, b.scene, b.evidence)
		attribution := sourceadmission.ResolveSubject(obj("sceneId", b.scene, "simulation", false, "botUserId", "bot", "sourceEvidence", b.evidence), candidate)
		if get(attribution, "ok") != true {
			continue
		}
		eligible[i] = true
		item := obj("candidateIndex", float64(i), "kind", get(v, "kind"))
		for _, k := range []string{"personId", "subject"} {
			if x, ok := v.Get(k); ok {
				_ = item.Set(k, x)
			}
		}
		_ = item.Set("messages", subjectMessages(sources, b.settings.KnowledgeEnabled))
		rejected = append(rejected, item)
	}
	if extra || len(rejected) == 0 {
		reason := "no_repair_candidates"
		if extra {
			reason = "extra_pass_already_used"
		}
		b.skipped("learning.repair", reason, "learning_reconcile")
		return nil
	}
	b.r.log("info", "learning_source_repair_started")
	schema := b.r.schemas.raw["learning.repair"]
	response, e := b.observedComplete("learning.repair", "", "learning_reconcile", "resultCount", map[string]int{"candidateCount": len(rejected)}, b.prompt("repair0"), obj("now", b.now(), "timezone", b.settings.Timezone, "purpose", "source-only-neutral-repair", "candidates", rejected), []map[string]any{tool("learning.repair", "Re-extract neutral content from an immutable original source window", schema)})
	if e != nil {
		return e
	}
	selected := map[int]bool{}
	for _, c := range firstEight(response) {
		i, ok := index(get(c.Arguments, "candidateIndex"))
		content, has := get(c.Arguments, "content").(string)
		if c.Name != "learning.repair" || !ok || !has || c.Arguments.Len() != 2 || !eligible[i] || selected[i] || trim(content) == "" {
			continue
		}
		n, _ := jsjson.CodePointCount(content)
		if n > 10000 {
			continue
		}
		selected[i] = true
		v := calls[i].Arguments.Clone()
		discarded := get(v, "content") != content && len(arr(get(v, "relations"))) > 0
		if get(v, "content") != content {
			v.Delete("relations")
			v.Delete("commitment")
			v.Delete("knowledge")
		}
		_ = v.Set("content", content)
		v, e = b.r.schemas.parse(v, candidateName(b.settings))
		if e != nil {
			return e
		}
		bad, e := b.r.d.Reads.UnsupportedPronouns(b.ctx, b.scene, v, b.evidence)
		if e != nil {
			return e
		}
		if !bad {
			calls[i].Arguments = v
			if discarded {
				b.r.log("info", "learning_relation_repair_discarded")
			}
			b.r.log("info", "learning_source_repair_accepted")
		} else {
			b.r.log("warn", "learning_source_repair_rejected")
		}
	}
	b.r.log("info", "learning_source_repair_completed")
	return nil
}
func index(v any) (int, bool) {
	n, ok := v.(float64)
	return int(n), ok && n >= 0 && n <= 7 && float64(int(n)) == n
}
func (b *batch) reconcile(calls []call, related []*Object) error {
	candidates := []*Object{}
	for i, c := range calls {
		if c.Name != "learning.propose" {
			continue
		}
		v, e := b.r.schemas.parse(c.Arguments, candidateName(b.settings))
		if e != nil {
			continue
		}
		bad, e := b.r.d.Reads.UnsupportedPronouns(b.ctx, b.scene, v, b.evidence)
		if e != nil {
			return e
		}
		if bad {
			continue
		}
		_ = v.Set("candidateIndex", float64(i))
		candidates = append(candidates, v)
	}
	scope, e := b.r.d.Reads.Targets(b.ctx, b.scene, candidates, related, b.evidence)
	if e != nil {
		return e
	}
	if scope.Coverage == "capped" {
		b.r.log("warn", "learning_reconciliation_targets_capped")
	}
	if len(scope.Targets) == 0 || len(candidates) == 0 {
		reason := "no_reconciliation_targets"
		if len(scope.Targets) > 0 {
			reason = "no_learning_candidates"
		}
		b.skipped("learning.reconcile", reason, "learning_persist")
		return nil
	}
	wire := []any{}
	for i, c := range calls {
		var v any
		if c.Name == "learning.propose" {
			v = c.Arguments
		}
		wire = append(wire, obj("candidateIndex", float64(i), "candidate", v))
	}
	schema := b.r.schemas.raw["learning.reconcile"]
	system := b.r.schemas.prompts["reconciliation0"]
	if b.settings.KnowledgeEnabled {
		system += " Never add or rewrite knowledge endpoints, spans or qualifiers. A matching entity title is not identity or merge authority; source-only structured knowledge cannot acquire existing graph endpoints from this reconciliation pass. Never convert scene-private knowledge to public review."
	}
	response, e := b.observedComplete("learning.reconcile", "", "learning_persist", "resultCount", map[string]int{"candidateCount": len(calls), "resultCount": len(scope.Targets)}, system, obj("candidates", wire, "relatedMemories", objects(scope.Targets), "now", b.now(), "timezone", b.settings.Timezone, "targetCoverage", scope.Coverage, "targetLimits", scope.Limits), []map[string]any{tool("learning.reconcile", "Choose a target without changing independent candidate content or evidence", schema)})
	if e != nil {
		b.r.log("warn", "learning_reconciliation_unavailable")
		return nil
	}
	selected := map[int]bool{}
	for _, c := range firstEight(response) {
		i, ok := index(get(c.Arguments, "candidateIndex"))
		action := str(get(c.Arguments, "action"))
		if !ok || i >= len(calls) || selected[i] || c.Name != "learning.reconcile" || c.Arguments.Len() != 3 || calls[i].Name != "learning.propose" || (action != "merge" && action != "supersede") {
			continue
		}
		var target *Object
		for _, v := range scope.Targets {
			if get(v, "id") == get(c.Arguments, "targetId") && get(v, "canRevise") == true && contains(arr(get(v, "candidateIndexes")), float64(i)) {
				target = v
				break
			}
		}
		if target == nil {
			continue
		}
		if action == "merge" && normalized(str(get(calls[i].Arguments, "content"))) != normalized(str(get(target, "content"))) {
			continue
		}
		key := "mergeIntoId"
		if action == "supersede" {
			key = "supersedesId"
		}
		v := calls[i].Arguments.Clone()
		_ = v.Set(key, get(target, "id"))
		calls[i].Arguments = v
		selected[i] = true
	}
	return nil
}
func normalized(s string) string { v, _ := jsstring.FingerprintText(s); return v }
func withSources(v *Object, scene string, evidence []any) *Object {
	v = v.Clone()
	sources := []any{}
	for _, id := range arr(get(v, "sourceMessageIds")) {
		sender := ""
		for _, source := range evidence {
			if get(source, "sceneId") == scene && get(source, "messageId") == id {
				sender = str(get(source, "senderId"))
				break
			}
		}
		sources = append(sources, obj("sceneId", scene, "messageId", id, "senderId", sender))
	}
	_ = v.Set("sources", sources)
	return v
}

var singleQuoted = regexp.MustCompile("'[^'\\n]*'")

func unquoted(text string) string {
	text = quoted.ReplaceAllString(text, " ")
	indices := singleQuoted.FindAllStringIndex(text, -1)
	var out strings.Builder
	last := 0
	for _, span := range indices {
		before, after := false, false
		if span[0] > 0 {
			r, _ := utf8.DecodeLastRuneInString(text[:span[0]])
			before = memorylexical.LetterOrNumber(r)
		}
		if span[1] < len(text) {
			r, _ := utf8.DecodeRuneInString(text[span[1]:])
			after = memorylexical.LetterOrNumber(r)
		}
		if before || after {
			continue
		}
		out.WriteString(text[last:span[0]])
		out.WriteByte(' ')
		last = span[1]
	}
	out.WriteString(text[last:])
	return out.String()
}
