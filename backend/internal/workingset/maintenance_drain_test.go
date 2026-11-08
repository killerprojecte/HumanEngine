package workingset

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
)

func TestMaintenanceDrainRetainsActualOwnerCallbacks(t *testing.T) {
	for _, phase := range []string{"enabled", "drain", "failure"} {
		t.Run(phase, func(t *testing.T) {
			entered, release, returned := make(chan struct{}), make(chan struct{}), make(chan struct{})
			clock := &concurrentScheduler{}
			var hold atomic.Bool
			block := func() { close(entered); <-release }
			m, err := NewMaintenance(MaintenancePorts{
				Enabled: func() (bool, error) {
					if phase == "enabled" && hold.Load() {
						block()
					}
					return true, nil
				},
				DrainOneBatch: func() (bool, error) {
					if phase == "drain" {
						block()
					}
					if phase == "failure" {
						return false, errors.New("synthetic")
					}
					return true, nil
				},
				Failed: func() {
					if phase == "failure" {
						block()
					}
				},
			}, 10, clock)
			if err != nil {
				t.Fatal(err)
			}
			m.Start()
			hold.Store(true)
			go func() { defer close(returned); clock.fire() }()
			<-entered
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			if err = m.CloseAndWait(ctx); !errors.Is(err, context.Canceled) {
				t.Fatalf("physical callback escaped: %v", err)
			}
			close(release)
			<-returned
			if err = m.CloseAndWait(context.Background()); err != nil {
				t.Fatal(err)
			}
			m.Start()
			m.Notify()
			m.Configure()
			if n, _ := clock.pending(); n != 0 {
				t.Fatal("closed maintenance resurrected")
			}
		})
	}
}
