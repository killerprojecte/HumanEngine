package httpapi

import (
	"context"
	"encoding/json"
	"io"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/killerprojecte/HumanEngine/backend/internal/botruntime"
	"github.com/killerprojecte/HumanEngine/backend/internal/plugins"
	"github.com/killerprojecte/HumanEngine/backend/internal/v1adapterruntime"
)

func TestAdapterStatusActualV1Runtime(t *testing.T) {
	_, _, engine := memeHTTP(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	bridge, err := v1adapterruntime.New(engine, "node", io.Discard, plugins.V1HostOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer bridge.Close(context.Background())
	path, err := filepath.Abs("testdata/adapter-status.mjs")
	if err != nil {
		t.Fatal(err)
	}
	for generation := 0; generation < 2; generation++ {
		raw, err := bridge.Host.Enable(ctx, "status-fixture", path, map[string]any{})
		if err != nil {
			t.Fatal(err)
		}
		var loaded struct {
			Registrations []struct{ Kind, Name, Handle string }
		}
		if err = json.Unmarshal(raw, &loaded); err != nil {
			t.Fatal(err)
		}
		var action string
		for _, r := range loaded.Registrations {
			if r.Kind == "action" && r.Name == "status" {
				action = r.Handle
			}
		}
		if action == "" {
			t.Fatal("missing actual status action")
		}
		for _, value := range []string{"connected", "connecting", "disconnected", "disposed"} {
			raw, err = bridge.Host.Call(ctx, "status-fixture", action, []any{value})
			if err != nil || string(raw) != "0" {
				t.Fatalf("factory getter read or RPC failed: %s %v", raw, err)
			}
			expected := value
			if value == "disposed" {
				expected = "disconnected"
			} // source ignores this input
			got, err := engine.Runtime.AdapterStatuses(ctx)
			if err != nil || !reflect.DeepEqual(got, []botruntime.AdapterStatus{{ID: "status-fixture:transport", Status: expected}}) {
				t.Fatalf("%v %v", got, err)
			}
		}
		if err = bridge.Host.Disable(ctx, "status-fixture"); err != nil {
			t.Fatal(err)
		}
		got, err := engine.Runtime.AdapterStatuses(ctx)
		if err != nil || got == nil || len(got) != 0 || bridge.Host.PID() != 0 {
			t.Fatalf("retired owner: %v %v pid=%d", got, err, bridge.Host.PID())
		}
	}
}
