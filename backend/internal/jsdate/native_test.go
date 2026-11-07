//go:build humanengine_icu && cgo

package jsdate

import "testing"

func TestNativeZones(t *testing.T) {
	for _, zone := range []string{"America/New_York", "Asia/Shanghai", "Pacific/Kiritimati", "Europe/Paris"} {
		if _, err := New(zone); err != nil {
			t.Fatal(zone, err)
		}
	}
	if _, err := New("Not/A_Timezone"); err == nil {
		t.Fatal("unknown timezone accepted")
	}
}
