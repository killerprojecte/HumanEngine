package app

import (
	"context"
	"testing"

	"github.com/killerprojecte/HumanEngine/backend/internal/observability"
)

func TestApplicationStatusReadsActualOwners(t *testing.T) {
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
	before := appRequest(t, app, "GET", "/api/status", nil)
	if field(before, "vectorVersion") == "" || field(before, "startedAt") == "" {
		t.Fatal(before)
	}
	initial := field(before, "recentErrors").(float64)
	if err = options.Logs.Emit(observability.Event{Level: "error", Code: "synthetic-status-error"}); err != nil {
		t.Fatal(err)
	}
	after := appRequest(t, app, "GET", "/api/status", nil)
	if field(after, "recentErrors") != initial+1 {
		t.Fatal(after)
	}
	if app.Bridge.Host.PID() != 0 {
		t.Fatal("status spawned Node without V1 plugins")
	}
}
