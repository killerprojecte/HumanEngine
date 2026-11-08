package learningowner

import (
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"os"
	"testing"
)

func TestActualSourcePronounPolicy(t *testing.T) {
	requireLexicalICU(t)
	raw, e := os.ReadFile("testdata/source.json")
	if e != nil {
		t.Fatal(e)
	}
	v, e := jsjson.ParseBytes(raw)
	if e != nil {
		t.Fatal(e)
	}
	root := v.(*Object)
	stringsOf := func(v any) []string {
		out := []string{}
		for _, x := range arr(v) {
			out = append(out, x.(string))
		}
		return out
	}
	for _, v := range arr(get(root, "pronouns")) {
		c := v.(*Object)
		t.Run(str(c, "name"), func(t *testing.T) {
			got, e := personalPronounsSupported(str(c, "content"), stringsOf(get(c, "authors")), stringsOf(get(c, "literals")))
			if e != nil {
				t.Fatal(e)
			}
			if got != get(c, "expected") {
				t.Fatalf("got %v want %v", got, get(c, "expected"))
			}
		})
	}
}
