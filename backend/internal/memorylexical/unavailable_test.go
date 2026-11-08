//go:build !humanengine_icu || !cgo

package memorylexical

import (
	"errors"
	"github.com/killerprojecte/HumanEngine/backend/internal/nodeicu"
	"testing"
)

func TestUnavailableDoesNotSubstituteSourceSegmentation(t *testing.T) {
	for _, run := range []func(string) ([]string, error){Words, WordLikeWords, TokenRuns, Terms} {
		if _, e := run("synthetic 中文"); !errors.Is(e, nodeicu.ErrUnavailable) {
			t.Fatalf("missing native dependency was not explicit: %v", e)
		}
	}
}
