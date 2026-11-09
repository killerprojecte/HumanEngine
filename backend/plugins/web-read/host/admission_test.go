package host_test

import (
	"context"
	"encoding/json"
	"github.com/killerprojecte/HumanEngine/backend/internal/plugins"
	"github.com/killerprojecte/HumanEngine/backend/plugins/web-read/host"
	"os"
	"testing"
)

func TestProductionAdmissionBlocked(t *testing.T) {
	if e := host.ProductionAdmission(); e == nil || e.Error() != "builtin_runtime_not_implemented" {
		t.Fatal("unapproved web-read runtime admitted", e)
	}
}

// Both validation profiles load the independently built normal/default reactor.
// Explicit candidate tests must not silently replace the shipped build profile.
func TestRealProductionGuestRuntimeDisabled(t *testing.T) {
	path := os.Getenv("WEB_READ_PRODUCTION_WASM")
	if path == "" {
		t.Skip("set WEB_READ_PRODUCTION_WASM to default-profile reactor")
	}
	artifact, e := os.ReadFile(path)
	if e != nil {
		t.Fatal(e)
	}
	ctx := context.Background()
	h, e := plugins.NewWASMHost(ctx)
	if e != nil {
		t.Fatal(e)
	}
	defer h.Close(ctx)
	if e = h.Enable(ctx, "web-read", artifact); e != nil {
		t.Fatal(e)
	}
	for _, operation := range []string{"registrations", "plan", "networkPrepare", "networkChoose", "networkHeaders", "networkRedirect", "response", "error"} {
		raw, e := h.Call(ctx, "web-read", []byte(`{"operation":"`+operation+`"}`))
		if e != nil {
			t.Fatal(e)
		}
		var result map[string]any
		json.Unmarshal(raw, &result)
		if result["error"] != "unknown_plugin_operation" {
			t.Fatal(operation, string(raw))
		}
	}
}
