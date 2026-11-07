//go:build humanengine_icu && cgo

package replyplan

import (
	"github.com/killerprojecte/HumanEngine/backend/internal/topiccollation"
	"testing"
)

func TestActualSourceNativeOracle(t *testing.T) {
	f := loadOracle(t)
	collator, err := topiccollation.New(f.Locale)
	if err != nil {
		t.Fatal(err)
	}
	defer collator.Close()
	runOracle(t, f, collator.Compare)
}
