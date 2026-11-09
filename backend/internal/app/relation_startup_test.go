package app

import (
	"context"
	"database/sql"
	"errors"
	"github.com/killerprojecte/HumanEngine/backend/internal/memoryservice"
	"testing"
)

func TestApplicationRelationExposureFailureDrainsOwners(t *testing.T) {
	ctx := context.Background()
	foreign, err := Open(ctx, applicationOptions(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := foreign.Shutdown(ctx); err != nil {
			t.Error(err)
		}
	})
	options := applicationOptions(t)
	var captured *Application
	var main *sql.DB
	var original error
	calls := 0
	broken := options
	broken.relationExposurePort = func(a *Application, o Owners) (*memoryservice.ExposurePort, error) {
		calls++
		captured = a
		main = o.Main
		port, err := memoryservice.NewExposurePort(a.Semantic.Memory, foreign.Engine.Sources, o.Main, o.Authority)
		original = err
		return port, err
	}
	partial, err := Open(ctx, broken)
	if original == nil || err == nil || !errors.Is(err, original) || calls != 1 {
		t.Fatalf("original constructor error lost: calls=%d original=%v returned=%v", calls, original, err)
	}
	if partial != nil || captured == nil || !captured.finalized || captured.finalErr != nil {
		t.Fatalf("failed startup did not finish physical drain: partial=%p captured=%p error=%v", partial, captured, err)
	}
	if captured.Engine.Runtime.ActiveTasks() != 0 {
		t.Fatal("runtime remained active")
	}
	if stats := captured.Contextual.Stats(); stats.Running != 0 || stats.Queued != 0 {
		t.Fatal("contextual work remained", stats)
	}
	if main == nil || main.PingContext(ctx) == nil {
		t.Fatal("installation database not closed after owner drain")
	}
	reopened, err := Open(ctx, options)
	if err != nil {
		t.Fatal("installation not reusable after exact constructor failure", err)
	}
	if err = reopened.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
}
