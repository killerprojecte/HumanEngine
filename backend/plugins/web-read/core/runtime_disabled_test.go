//go:build !web_read_candidate

package webread

import "testing"

func TestProductionRuntimeOperationsDisabled(t *testing.T) {
	for _, op := range []string{"toolValidate", "configurationValidate", "registrations", "plan", "networkPrepare", "networkChoose", "networkHeaders", "networkRedirect", "response", "error"} {
		r := Invoke([]byte(`{"operation":"` + op + `"}`)).(map[string]any)
		if r["error"] != "unknown_plugin_operation" {
			t.Fatal(op, r)
		}
	}
}
