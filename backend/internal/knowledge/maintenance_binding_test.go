package knowledge

import (
	"context"
	"github.com/killerprojecte/HumanEngine/backend/internal/recovery"
	"testing"
)

func TestMaintenanceBindingRejectsMissingCapabilities(t *testing.T) {
	ctx := context.Background()
	if _, e := NewMaintenanceStore(nil, nil, ctx); e == nil {
		t.Fatal("missing maintenance owner accepted")
	}
	if _, e := NewMaintenanceStore(new(recovery.Maintenance), nil, ctx); e == nil {
		t.Fatal("missing transaction accepted")
	}
	if _, e := NewMaintenanceStore(new(recovery.Maintenance), nil, nil); e == nil {
		t.Fatal("missing context accepted")
	}
	live := new(Store)
	if e := live.WithMaintenanceTx(ctx, nil).RequireTransaction(); e == nil {
		t.Fatal("live store rebound to maintenance")
	}
	offline := &Store{maintenance: new(recovery.Maintenance), maintenanceContext: ctx}
	if e := offline.WithSession(nil).RequireTransaction(); e == nil {
		t.Fatal("maintenance store rebound to live")
	}
	called := false
	if e := offline.WithWriteSession(func(*recovery.WriteSession) error { called = true; return nil }); e == nil || called {
		t.Fatal("maintenance leaked into live callback")
	}
}
