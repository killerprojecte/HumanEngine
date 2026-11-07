//go:build humanengine_icu && cgo

package intltime

import "testing"

func TestPinnedNativeRequired(t *testing.T) {
	if _, err := Hour(0, "America/New_York"); err != nil {
		t.Fatal(err)
	}
}
