package workingset

import (
	"github.com/killerprojecte/HumanEngine/backend/internal/recovery"
	"testing"
)

func TestMaintenanceSessionEnabledSchedulesOrdinaryTimer(t *testing.T) {
	clock := &concurrentScheduler{}
	// This opaque pointer tests notification routing, not recovery authorization;
	// real active/expired/foreign capabilities are covered by the installed test.
	session := new(recovery.WriteSession)
	scoped, ordinary, drains := 0, 0, 0
	enabled := false
	m, err := NewMaintenance(MaintenancePorts{
		Enabled: func() (bool, error) { ordinary++; return enabled, nil },
		EnabledInSession: func(got *recovery.WriteSession) (bool, error) {
			if got != session {
				t.Fatal("wrong notification capability")
			}
			scoped++
			return true, nil
		},
		DrainOneBatch: func() (bool, error) { drains++; return false, nil },
		Failed:        func() { t.Fatal("unexpected failure") },
	}, 250, clock)
	if err != nil {
		t.Fatal(err)
	}
	m.Start()
	defer m.Dispose()
	if n, _ := clock.pending(); n != 0 {
		t.Fatal("disabled startup scheduled")
	}
	m.NotifyInSession(session)
	if scoped != 1 || ordinary != 1 {
		t.Fatalf("notification read routing: %d %d", scoped, ordinary)
	}
	if n, _ := clock.pending(); n != 1 {
		t.Fatalf("enabled session scheduled %d timers", n)
	}
	m.NotifyInSession(session)
	if scoped != 1 {
		t.Fatal("coalesced notification repeated enabled check")
	}
	// Mutating both variables before firing detects a timer retaining/reusing
	// session Enabled instead of performing the source's fresh ordinary check.
	session = nil
	enabled = true
	if !clock.fire() {
		t.Fatal("missing scheduled tick")
	}
	if ordinary != 2 || scoped != 1 || drains != 1 {
		t.Fatalf("timer routing %d %d %d", ordinary, scoped, drains)
	}
	if n, _ := clock.pending(); n != 0 {
		t.Fatal("false drain rescheduled")
	}
}
