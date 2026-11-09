package app

import (
	"context"
	"errors"
	"github.com/killerprojecte/HumanEngine/backend/internal/installation"
	"github.com/killerprojecte/HumanEngine/backend/internal/recovery"
	"github.com/killerprojecte/HumanEngine/backend/internal/workingset"
	"sync"
	"testing"
	"time"
)

func TestMaintenanceSessionReadinessSingleConnection(t *testing.T) {
	ctx := context.Background()
	open := func() *installation.Store {
		options := applicationOptions(t)
		s, e := installation.Open(ctx, options.Installation)
		if e != nil {
			t.Fatal(e)
		}
		t.Cleanup(func() {
			if e := s.Close(); e != nil {
				t.Error(e)
			}
		})
		if s.DB.Stats().MaxOpenConnections != 1 {
			t.Fatal("regression requires maxconn1")
		}
		return s
	}
	a, b := open(), open()
	owner := func(s *installation.Store) Owners { return Owners{Main: s.DB, Authority: s.Authority} }
	var expired *recovery.WriteSession
	sentinel := errors.New("rollback witness")
	var outcome recovery.RootOutcome
	observed := false
	flags, failed, fallback := 0, 0, 0
	maintenance, e := workingset.NewMaintenance(workingset.MaintenancePorts{
		Enabled: func() (bool, error) { fallback++; return false, nil },
		EnabledInSession: func(ws *recovery.WriteSession) (bool, error) {
			return maintenanceEnabledInSession(owner(a), ws, func() bool { flags++; return false })
		},
		DrainOneBatch: func() (bool, error) { t.Error("disabled notification scheduled a drain"); return false, nil },
		Failed:        func() { failed++ },
	}, 250, nil)
	if e != nil {
		t.Fatal(e)
	}
	maintenance.Start()
	defer maintenance.Dispose()
	if e = a.Authority.WithWriteSession(ctx, a.DB, func(ws *recovery.WriteSession) error {
		expired = ws
		if yes, e := maintenanceEnabledInSession(owner(a), ws, func() bool { flags++; return true }); e != nil || !yes {
			t.Fatalf("active ready: %v %v", yes, e)
		}
		if _, e := maintenanceEnabledInSession(owner(b), ws, func() bool { t.Error("foreign flags read"); return true }); e == nil {
			t.Fatal("foreign capability accepted")
		}
		maintenance.NotifyInSession(ws)
		if flags != 2 || failed != 0 || fallback != 1 {
			t.Fatalf("synchronous notify: %d %d %d", flags, failed, fallback)
		}
		if e := ws.AfterRoot(func(got recovery.RootOutcome) { outcome = got; observed = true }); e != nil {
			return e
		}
		return sentinel
	}); !errors.Is(e, sentinel) {
		t.Fatalf("rollback error lost: %v", e)
	}
	if !observed || outcome.Status != recovery.RootRolledBack || !errors.Is(outcome.Err, sentinel) {
		t.Fatalf("root rollback outcome: %+v observed=%v", outcome, observed)
	}
	maintenance.NotifyInSession(expired)
	if flags != 2 || failed != 1 || fallback != 1 {
		t.Fatalf("expired notify bypassed failure: %d %d %d", flags, failed, fallback)
	}
	if e = b.Authority.WithWriteSession(ctx, b.DB, func(ws *recovery.WriteSession) error { maintenance.NotifyInSession(ws); return nil }); e != nil {
		t.Fatal(e)
	}
	if flags != 2 || failed != 2 {
		t.Fatal("foreign notify did not fail before reading flags")
	}
	legacyCalls := 0
	legacy, e := workingset.NewMaintenance(workingset.MaintenancePorts{Enabled: func() (bool, error) { legacyCalls++; return false, nil }, DrainOneBatch: func() (bool, error) { return false, nil }}, 250, nil)
	if e != nil {
		t.Fatal(e)
	}
	legacy.Start()
	defer legacy.Dispose()
	if e = a.Authority.WithWriteSession(ctx, a.DB, func(ws *recovery.WriteSession) error { legacy.NotifyInSession(ws); return nil }); e != nil {
		t.Fatal(e)
	}
	if legacyCalls != 2 {
		t.Fatalf("legacy owner fallback called %d times", legacyCalls)
	}
	maintenance.Notify()
	if fallback != 2 {
		t.Fatal("legacy fallback not retained")
	}
	// Each concurrent writer explicitly carries its own capability. No global
	// current transaction, goroutine-local session, or timer capture is involved.
	barrierCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	entered := make(chan struct{}, 2)
	release := make(chan struct{})
	var wg sync.WaitGroup
	failures := make(chan error, 2)
	for _, s := range []*installation.Store{a, b} {
		wg.Add(1)
		go func(s *installation.Store) {
			defer wg.Done()
			failures <- s.Authority.WithWriteSession(barrierCtx, s.DB, func(ws *recovery.WriteSession) error {
				entered <- struct{}{}
				select {
				case <-release:
				case <-barrierCtx.Done():
					return barrierCtx.Err()
				}
				_, e := maintenanceEnabledInSession(owner(s), ws, func() bool { return true })
				return e
			})
		}(s)
	}
	for i := 0; i < 2; i++ {
		select {
		case <-entered:
		case <-barrierCtx.Done():
			close(release)
			wg.Wait()
			t.Fatal("writers never overlapped", barrierCtx.Err())
		}
	}
	close(release)
	wg.Wait()
	close(failures)
	for e := range failures {
		if e != nil {
			t.Error(e)
		}
	}
}
