//go:build humanengine_icu && cgo

package commitments

import (
	"github.com/killerprojecte/HumanEngine/backend/internal/topiccollation"
	"testing"
)

func TestNativeSourceCollationQualification(t *testing.T) {
	oracle := loadOracle(t)
	collator, e := topiccollation.New(str(get(oracle, "sourceLocale")))
	must(t, e)
	defer collator.Close()
	sample := get(oracle, "readSample")
	got, e := TemporalQualification(object(get(sample, "head")), arr(get(sample, "revisions")), object(get(sample, "timeline")), collator.Compare)
	must(t, e)
	if got != get(sample, "qualificationHash") {
		t.Fatalf("native qualification %s != %s", got, get(sample, "qualificationHash"))
	}
}
