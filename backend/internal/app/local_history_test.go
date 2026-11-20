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

func TestLocalHistoryActualSource(t *testing.T) {
	raw, err := os.ReadFile("testdata/local-history-source.json")
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
		parsed, err := jsjson.ParseBytes(raw)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := app.Engine.Runtime.Ingest(ctx, "fixture", parsed.(*jsjson.Object), false); err != nil {
			t.Fatal(err)
		}
	}
	if err := app.Engine.Runtime.Recall(ctx, "fixture", fixture.Recall); err != nil {
		t.Fatal(err)
	}
	for _, tc := range fixture.Cases {
		t.Run(tc.Path, func(t *testing.T) {
			request := httptest.NewRequest(tc.Method, tc.Path, nil)
			if tc.Authorized {
				request.Header.Set("Authorization", "Bearer synthetic-token")
			}
			response := httptest.NewRecorder()
			app.ServeHTTP(response, request)
			if response.Code != tc.Status {
				t.Fatalf("status: got %d want %d: %s", response.Code, tc.Status, response.Body)
			}
			var got any
			if err := json.Unmarshal(response.Body.Bytes(), &got); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, tc.Body) {
				t.Fatalf("body mismatch: got %s; want %+v", response.Body, tc.Body)
			}
		})
	}
}
