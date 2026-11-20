package app

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"reflect"
	"testing"

	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
)

func TestSceneMessagesActualSource(t *testing.T) {
	raw, err := os.ReadFile("testdata/scene-messages-source.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Scene    json.RawMessage
		Messages []json.RawMessage
		Recall   string
		Cases    []struct {
			Method, Path string
			Authorized   bool
			Status       int
			Body         any
		}
	}
	if err := json.Unmarshal(raw, &fixture); err != nil {
		t.Fatal(err)
	}
	options := applicationOptions(t)
	options.Entries = []string{}
	ctx := context.Background()
	app, err := Open(ctx, options)
	if err != nil {
		t.Fatal(err)
	}
	defer app.Shutdown(ctx)
	appRequest(t, app, "PUT", "/api/scenes/fixture", fixture.Scene)
	for _, raw := range fixture.Messages {
		row, err := jsjson.ParseBytes(raw)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = app.Engine.Runtime.Ingest(ctx, "fixture", row.(*jsjson.Object), false); err != nil {
			t.Fatal(err)
		}
	}
	if err := app.Engine.Runtime.Recall(ctx, "fixture", fixture.Recall); err != nil {
		t.Fatal(err)
	}
	for _, tc := range fixture.Cases {
		t.Run(tc.Method+tc.Path, func(t *testing.T) {
			request := httptest.NewRequest(tc.Method, tc.Path, nil)
			sourceFixtureAuth(request, tc.Authorized)
			result := httptest.NewRecorder()
			app.ServeHTTP(result, request)
			if result.Code != tc.Status {
				t.Fatalf("status: got %d want %d: %s", result.Code, tc.Status, result.Body)
			}
			var got any = result.Body.String()
			if tc.Method != "HEAD" {
				if err := json.Unmarshal(result.Body.Bytes(), &got); err != nil {
					t.Fatal(err)
				}
			}
			// Original bytes are still unmigrated. The preview must not advertise
			// the TypeScript URL until the corresponding Go route serves it.
			if rows, ok := tc.Body.([]any); ok {
				for _, row := range rows {
					for _, part := range row.(map[string]any)["parts"].([]any) {
						delete(part.(map[string]any), "mediaUrl")
					}
				}
			}
			if !reflect.DeepEqual(got, tc.Body) {
				t.Fatalf("body mismatch: got %s; want %+v", result.Body, tc.Body)
			}
		})
	}
	snapshot, err := app.Engine.Runtime.Snapshot("fixture")
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range snapshot.Messages {
		if field(row, "id") == "media" {
			parts := field(row, "parts").([]any)
			if len(parts) != 4 || field(parts[0], "assetId") != "private-reference" {
				t.Fatalf("browser projection mutated the runtime source: %+v", parts)
			}
			return
		}
	}
	t.Fatal("media source missing from runtime snapshot")
}
