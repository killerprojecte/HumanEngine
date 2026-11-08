package learningowner

import (
	"context"
	"github.com/killerprojecte/HumanEngine/backend/internal/memorylexical"
	"regexp"
	"strings"
	"unicode/utf8"
)

var quotes = regexp.MustCompile("(?s)```.*?```|~~~.*?~~~|`[^`\\n]*`|“[^”]*”|‘[^’]*’|「[^」]*」|『[^』]*』|\"[^\"\\n]*\"|'[^'\\n]*'")
var contractions = regexp.MustCompile(`(?i)\b(he|she)['’](?:s|d|ll)\b`)
var nonPersonal = regexp.MustCompile(`^(?:他们|他們|她们|她們|他人|他乡|他鄉|他日|他国|他國|他处|他處|他方|他律|他山)`)
var hypothetical = regexp.MustCompile(`(?i)如果|假如|假设|假設|可能|也许|也許|玩笑|才怪|\b(?:if|hypothetical|joking|joke|perhaps|maybe|suppose|assume)\b`)
var denial = regexp.MustCompile(`(?i)不|别|別|未|并非|並非|\b(?:not|never|no|don't|doesn't|isn't)\b`)
var female = regexp.MustCompile(`(?i)^(?:我|本人)(?:是|为|為)(?:一个|一個|一位|一名)?(?:女性|女生|女人|女孩)$|^I am (?:a )?(?:woman|girl|female)$`)
var male = regexp.MustCompile(`(?i)^(?:我|本人)(?:是|为|為)(?:一个|一個|一位|一名)?(?:男性|男生|男人|男孩)$|^I am (?:a )?(?:man|boy|male)$`)
var declaration = regexp.MustCompile(`(?i)^(?:我的代词是|我的代詞是|我使用的代词是|我使用的代詞是|my pronouns are)\s*([^\r\n\x{2028}\x{2029}]+)$`)
var clauses = regexp.MustCompile(`[。！？.!?\n，,；;]`)

func unquoted(text string) string {
	var out strings.Builder
	consumed, search := 0, 0
	for search < len(text) {
		span := quotes.FindStringIndex(text[search:])
		if span == nil {
			break
		}
		a, b := search+span[0], search+span[1]
		if text[a] == '\'' {
			before, after := rune(0), rune(0)
			if a > 0 {
				before, _ = utf8.DecodeLastRuneInString(text[:a])
			}
			if b < len(text) {
				after, _ = utf8.DecodeRuneInString(text[b:])
			}
			if memorylexical.LetterOrNumber(before) || memorylexical.LetterOrNumber(after) {
				search = a + 1
				continue
			}
		}
		out.WriteString(text[consumed:a])
		out.WriteByte(' ')
		consumed = b
		search = b
	}
	out.WriteString(text[consumed:])
	return out.String()
}

func singularPronouns(text string) (uint8, error) {
	words, e := memorylexical.Words(contractions.ReplaceAllString(unquoted(text), "$1"))
	if e != nil {
		return 0, e
	}
	var result uint8
	for _, segment := range words {
		word, e := memorylexical.Lower(segment)
		if e != nil {
			return 0, e
		}
		if word == "she" || word == "her" || word == "hers" || word == "herself" || (strings.HasPrefix(word, "她") && !nonPersonal.MatchString(word)) {
			result |= 1
		}
		if word == "he" || word == "him" || word == "his" || word == "himself" || (strings.HasPrefix(word, "他") && !nonPersonal.MatchString(word)) {
			result |= 2
		}
	}
	return result, nil
}
func personalPronounsSupported(content string, authors, literals []string) (bool, error) {
	required, e := singularPronouns(content)
	if e != nil || required == 0 {
		return e == nil, e
	}
	normalized := func(s string) string {
		return strings.Join(strings.FieldsFunc(s, func(r rune) bool { return jsWhitespace(r) }), " ")
	}
	for _, text := range literals {
		if normalized(text) == normalized(content) {
			return true, nil
		}
	}
	var supported uint8
	for _, text := range authors {
		plain := unquoted(text)
		if hypothetical.MatchString(unicodeBoundaryFold(plain)) {
			continue
		}
		for _, part := range clauses.Split(plain, -1) {
			clause := strings.TrimFunc(part, func(r rune) bool { return jsWhitespace(r) })
			if denial.MatchString(unicodeBoundaryFold(clause)) {
				continue
			}
			if female.MatchString(clause) {
				supported |= 1
			}
			if male.MatchString(clause) {
				supported |= 2
			}
			if m := declaration.FindStringSubmatch(clause); m != nil {
				p, e := singularPronouns(m[1])
				if e != nil {
					return false, e
				}
				supported |= p
			}
		}
	}
	return required&supported == required, nil
}
func (o *Owner) UnsupportedPronouns(ctx context.Context, scene string, candidate *Object, evidence []any) (bool, error) {
	ids := map[string]bool{}
	for _, id := range arr(get(candidate, "sourceMessageIds")) {
		s, _ := id.(string)
		ids[s] = true
	}
	authors, literals := []string{}, []string{}
	person := str(candidate, "personId")
	for _, raw := range evidence {
		m, _ := raw.(*Object)
		id := str(m, "messageId")
		if !ids[id] || str(m, "sceneId") != scene || str(m, "origin") != "live" || get(m, "available") != true {
			continue
		}
		recalled, e := o.Recalled(ctx, scene, id)
		if e != nil {
			return false, e
		}
		if recalled {
			continue
		}
		parts := []string{}
		for _, raw := range arr(get(m, "parts")) {
			part, _ := raw.(*Object)
			if str(part, "type") == "text" {
				parts = append(parts, str(part, "text"))
			}
		}
		text := strings.Join(parts, " ")
		if person != "" && str(m, "senderId") == person {
			authors = append(authors, text)
		}
		if person == "" || str(m, "senderId") == person {
			literals = append(literals, text)
		}
	}
	ok, e := personalPronounsSupported(str(candidate, "content"), authors, literals)
	return !ok, e
}

// ECMAScript WhiteSpace and LineTerminator, excluding Unicode NEL.
func jsWhitespace(r rune) bool {
	return r == 0x9 || r == 0xa || r == 0xb || r == 0xc || r == 0xd || r == 0x20 || r == 0xa0 || r == 0x1680 || (r >= 0x2000 && r <= 0x200a) || r == 0x2028 || r == 0x2029 || r == 0x202f || r == 0x205f || r == 0x3000 || r == 0xfeff
}

// JS /iu word boundaries add the non-ASCII folds to ASCII word characters.
func unicodeBoundaryFold(s string) string {
	return strings.NewReplacer("ſ", "s", "K", "k").Replace(s)
}
