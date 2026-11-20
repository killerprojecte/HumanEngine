package snowlumaruntime

import (
	"encoding/json"
	"os"
	"strconv"
	"testing"

	"github.com/killerprojecte/HumanEngine/backend/internal/botruntime"
	"github.com/killerprojecte/HumanEngine/backend/internal/dispatch"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	snow "github.com/killerprojecte/HumanEngine/backend/plugins/snowluma/core"
)

func TestRetainedDirectLinksActualSource(t *testing.T) {
	raw, err := os.ReadFile("../../../../internal/app/testdata/snowluma-retained-direct-links-source.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Node     string
		Locators []json.RawMessage
		Rows     []struct {
			Surface string
			Items   []struct {
				Parts []struct{ Type, MediaURL string }
			}
		}
	}
	if err := json.Unmarshal(raw, &fixture); err != nil || fixture.Node != "v24.22.1" || len(fixture.Locators) != 5 || len(fixture.Rows) != 2 {
		t.Fatalf("invalid retained direct link oracle: %v", err)
	}
	defer func() {
		if t.Failed() {
			t.Log("TS promotes presentation based on shape; Go additionally requires the retained native locator class and target transport")
		}
	}()
	classes := [...]botruntime.NativeImageLink{botruntime.NativeImageBrowserDirect, botruntime.NativeImageBrowserDirect, botruntime.NativeImageBrowserDirect, botruntime.NativeImageInline, 0}
	const adapter, account, peer = "snowluma", "42", "104"
	scene := botruntime.Scene{SceneRef: dispatch.SceneRef{ID: snow.SceneID(adapter, account, "group", peer), Kind: "group", AdapterID: adapter, AccountID: account, PlatformSceneID: peer}}
	for i, wire := range fixture.Locators {
		parts, err := jsjson.ParseBytes(wire)
		if err != nil {
			t.Fatal(err)
		}
		event := obj("self_id", float64(42), "message_type", "group", "group_id", float64(104), "user_id", float64(7), "message_id", float64(i+1), "time", float64(4070908800+i), "message", parts)
		// The production guest determines the exact locator; no source URLs
		// or synthetic locator kind are injected into the classifier.
		msg, _, err := snow.Normalize(adapter, account, event, "live", "2099-01-01T00:00:00.000Z", false, nil)
		if err != nil {
			t.Fatal(err)
		}
		rawParts := get(msg, "parts").([]any)
		for _, row := range fixture.Rows {
			expected := row.Items[i].Parts
			for index, p := range expected {
				if p.Type != "media" {
					continue
				}
				if len(p.MediaURL) == 0 || p.MediaURL[len(p.MediaURL)-1:] != strconv.Itoa(index) {
					t.Fatalf("source %s row %d did not retain the expected part index %d", row.Surface, i, index)
				}
				part := rawParts[index].(*jsjson.Object)
				want := classes[i]
				if want == botruntime.NativeImageBrowserDirect && !publicBrowserImageSupported {
					want = 0
				}
				if got := classifyRetainedImage(scene, str(part, "assetId")); got != want {
					t.Fatalf("%s row %d index %d kind=%d want=%d", row.Surface, i, index, got, want)
				}
			}
		}
	}
}
