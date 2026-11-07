//go:build !humanengine_icu || !cgo

package topiccollation

import (
	"errors"
	"testing"
)

func TestNoApproximationWithoutPinnedICU(t *testing.T) {
	if _, err := New("en-US"); !errors.Is(err, ErrUnavailable) {
		t.Fatal(err)
	}
}
