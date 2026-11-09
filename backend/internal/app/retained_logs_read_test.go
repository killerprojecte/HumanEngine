package app

import (
	"context"
	"github.com/killerprojecte/HumanEngine/backend/internal/observability"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestApplicationRetainedLogsReadActualOwner(t *testing.T) {
	options := applicationOptions(t)
	options.Entries = []string{}
	app, err := Open(context.Background(), options)
	if err != nil {
		if app != nil {
			app.Shutdown(context.Background())
		}
		t.Fatal(err)
	}
	defer app.Shutdown(context.Background())
	if err = app.Logger().Emit(observability.Event{ID: "synthetic-retained", Level: "info", Code: "synthetic-retained"}); err != nil {
		t.Fatal(err)
	}
	result := appRequest(t, app, "GET", "/api/logs", nil)
	rows, ok := result.([]any)
	if !ok || len(rows) == 0 || field(rows[0], "id") != "synthetic-retained" {
		t.Fatal(result)
	}
	// Delivery status uses the same default source owner as retained reads.
	request := httptest.NewRequest(http.MethodGet, "/api/logs/status", nil)
	request.Header.Set("Authorization", "Bearer synthetic-token")
	response := httptest.NewRecorder()
	app.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatal("source log status route unavailable", response.Code, response.Body.String())
	}
	if app.Bridge.Host.PID() != 0 {
		t.Fatal("logs read spawned Node without V1 plugins")
	}
}
