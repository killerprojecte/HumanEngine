package memoryservice

import (
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"os"
	"testing"
)

func TestSemanticModelSnapshotActualSource(t *testing.T) {
	raw, e := os.ReadFile("../semantic/testdata/model-source.json")
	exposureMust(t, e)
	v, e := jsjson.Parse(string(raw))
	exposureMust(t, e)
	for _, entry := range epArr(epGet(v, "cases")) {
		t.Run(text(object(entry), "scenario"), func(t *testing.T) {
			page := object(epGet(entry, "page")).Clone()
			items := []any{}
			for _, v := range epArr(value(page, "items")) {
				c := object(epGet(v, "candidate"))
				if text(c, "kind") != "summary_candidate" || text(c, "status") == "admitted" {
					items = append(items, v)
				}
			}
			_ = page.Set("items", items)
			actual, e := semanticSnapshot("s", page)
			exposureMust(t, e)
			if !deepEqual(actual, epGet(entry, "model")) {
				a, _ := jsjson.Stringify(actual)
				b, _ := jsjson.Stringify(epGet(entry, "model"))
				t.Fatalf("actual=%s expected=%s", a, b)
			}
		})
	}
}
