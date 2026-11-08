package recoverydomains

import (
	"context"
	"errors"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsstring"
	"github.com/killerprojecte/HumanEngine/backend/internal/recovery"
	"golang.org/x/text/unicode/norm"
	"sort"
	"strings"
	"unicode/utf16"
)

var conceptAliases = []struct{ concept, word string }{{"happy", "happy"}, {"happy", "cheerful"}, {"happy", "joy"}, {"happy", "joyful"}, {"happy", "开心"}, {"happy", "開心"}, {"happy", "高兴"}, {"happy", "高興"}, {"happy", "快乐"}, {"happy", "快樂"}, {"celebrate", "celebrate"}, {"celebrate", "celebration"}, {"celebrate", "congratulations"}, {"celebrate", "congrats"}, {"celebrate", "庆祝"}, {"celebrate", "慶祝"}, {"celebrate", "祝贺"}, {"celebrate", "祝賀"}, {"sad", "sad"}, {"sad", "sadness"}, {"sad", "unhappy"}, {"sad", "not happy"}, {"sad", "dejected"}, {"sad", "难过"}, {"sad", "難過"}, {"sad", "伤心"}, {"sad", "傷心"}, {"sad", "不开心"}, {"sad", "不開心"}, {"confused", "confused"}, {"confused", "confusion"}, {"confused", "puzzled"}, {"confused", "困惑"}, {"confused", "疑惑"}, {"confused", "迷惑"}, {"confused", "不解"}, {"angry", "angry"}, {"angry", "anger"}, {"angry", "生气"}, {"angry", "生氣"}, {"angry", "愤怒"}, {"angry", "憤怒"}, {"thanks", "thanks"}, {"thanks", "thank you"}, {"thanks", "gratitude"}, {"thanks", "感谢"}, {"thanks", "感謝"}, {"thanks", "谢谢"}, {"thanks", "謝謝"}, {"comfort", "comfort"}, {"comfort", "comforting"}, {"comfort", "安慰"}, {"comfort", "抱抱"}, {"greeting", "greeting"}, {"greeting", "hello"}, {"greeting", "welcome"}, {"greeting", "问候"}, {"greeting", "問候"}, {"greeting", "你好"}, {"greeting", "欢迎"}, {"greeting", "歡迎"}, {"love", "love"}, {"love", "affection"}, {"love", "喜欢"}, {"love", "喜歡"}, {"love", "爱"}, {"love", "愛"}, {"tired", "tired"}, {"tired", "sleepy"}, {"tired", "疲惫"}, {"tired", "疲憊"}, {"tired", "困了"}, {"surprise", "surprise"}, {"surprise", "surprised"}, {"surprise", "惊讶"}, {"surprise", "驚訝"}, {"surprise", "震惊"}, {"surprise", "震驚"}}

func concepts(input string) ([]string, error) {
	remaining, e := jsstring.Lower(norm.NFKC.String(input))
	if e != nil {
		return nil, e
	}
	// Trimming cannot alter concept matches: boundaries exclude only ASCII letters.
	aliases := append([]struct{ concept, word string }{}, conceptAliases...)
	sort.SliceStable(aliases, func(i, j int) bool {
		return len(utf16.Encode([]rune(aliases[i].word))) > len(utf16.Encode([]rune(aliases[j].word)))
	})
	out := []string{}
	seen := map[string]bool{}
	for _, a := range aliases {
		english := false
		for _, c := range a.word {
			if c >= 'a' && c <= 'z' {
				english = true
			}
		}
		offset := 0
		found := false
		for offset <= len(remaining) {
			n := strings.Index(remaining[offset:], a.word)
			if n < 0 {
				break
			}
			n += offset
			end := n + len(a.word)
			letter := func(b byte) bool { return b >= 'a' && b <= 'z' }
			if english && (n > 0 && letter(remaining[n-1]) || end < len(remaining) && letter(remaining[end])) {
				offset = end
				continue
			}
			found = true
			remaining = remaining[:n] + strings.Repeat(" ", len(a.word)) + remaining[end:]
			offset = end
		}
		if found && !seen[a.concept] {
			seen[a.concept] = true
			out = append(out, a.concept)
		}
	}
	return out, nil
}
func (o *Owner) indexConcepts(ctx context.Context, tx *recovery.MaintenanceTx, a *object) error {
	if e := exec(ctx, tx, "DELETE FROM meme_search_concepts WHERE asset_id=?", get(a, "id")); e != nil {
		return e
	}
	tags := obj(get(a, "tags"))
	if tags == nil {
		return errors.New("recovery_domain_material_invalid")
	}
	texts := []string{}
	for _, k := range []string{"appearance", "style", "subject", "emotion", "intent", "tone", "intensity", "memeMeaning"} {
		items, ok := get(tags, k).([]any)
		if !ok {
			return errors.New("recovery_domain_material_invalid")
		}
		for _, v := range items {
			if _, ok := v.(string); !ok {
				return errors.New("recovery_domain_material_invalid")
			}
			texts = append(texts, str(v))
		}
	}
	if _, ok := get(tags, "ocr").(string); !ok {
		return errors.New("recovery_domain_material_invalid")
	}
	texts = append(texts, str(get(tags, "ocr")))
	seen := map[string]bool{}
	for _, text := range texts {
		found, e := concepts(text)
		if e != nil {
			return e
		}
		for _, c := range found {
			if seen[c] {
				continue
			}
			seen[c] = true
			if e := exec(ctx, tx, "INSERT INTO meme_search_concepts VALUES(?,?)", get(a, "id"), c); e != nil {
				return e
			}
		}
	}
	return nil
}
