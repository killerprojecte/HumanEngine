//go:build humanengine_icu && cgo

package replyplan

import (
	"github.com/killerprojecte/HumanEngine/backend/internal/topiccollation"
	"testing"
)

func TestHandoffActualSourceNativeOracle(t *testing.T) {
	collator, err := topiccollation.New(loadHandoffOracle(t).Locale)
	if err != nil {
		t.Fatal(err)
	}
	defer collator.Close()
	runHandoffOracle(t, collator.Compare)
}
