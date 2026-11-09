package app

import (
	"context"
	"github.com/killerprojecte/HumanEngine/backend/internal/observability"
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
	if err = options.Logs.Emit(observability.Event{ID: "synthetic-retained", Level: "info", Code: "synthetic-retained"}); err != nil {
		t.Fatal(err)
	}
	result := appRequest(t, app, "GET", "/api/logs", nil)
	rows, ok := result.([]any)
	if !ok || len(rows) == 0 || field(rows[0], "id") != "synthetic-retained" {
		t.Fatal(result)
	}
	if app.Bridge.Host.PID() != 0 {
		t.Fatal("logs read spawned Node without V1 plugins")
	}
}
