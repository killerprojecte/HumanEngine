package botruntime

import (
	"encoding/json"
	"github.com/killerprojecte/HumanEngine/backend/internal/dispatch"
	"testing"
)

func TestMemePlaceholderOnlyRejectsStandaloneSearchedMarkers(t *testing.T) {
	for _, row := range []struct {
		text   string
		reject bool
	}{
		{"[表情包]", true}, {"【meme: congratulations】", true}, {"[STICKER：yay]", true},
		{"\u2003[表情包\u3000：\u2028庆祝]\ufeff", true}, {"[meme:\n]", true},
		{"不用表情包也想说一句：太好了！", false}, {"The marker [meme] is plain text.", false},
		{"[表情包：第一行\n第二行]", false}, {"[image]", false},
	} {
		t.Run(row.text, func(t *testing.T) {
			raw, _ := json.Marshal(map[string]string{"type": "text", "text": row.text})
			segments := []dispatch.Segment{{Parts: []json.RawMessage{raw}}}
			g := NewGrounding()
			if got := g.Check(segments); got == "meme_placeholder_not_media" {
				t.Fatal("unsearched discussion rejected")
			}
			g.ObserveTool("memes.search", obj("items", []any{}))
			got := g.Check(segments)
			if (got == "meme_placeholder_not_media") != row.reject {
				t.Fatalf("diagnostic %q, reject %v", got, row.reject)
			}
		})
	}
}
