//go:build web_read_candidate

package host

import (
	"context"
	"errors"
	"net/netip"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestOSDNSNetworkLateOutcomes(t *testing.T) {
	for _, cancelled := range []bool{false, true} {
		for _, failure := range []bool{false, true} {
			name := "timeout_success"
			if cancelled {
				name = "cancel_success"
			}
			if failure {
				name += "_failure"
			}
			t.Run(name, func(t *testing.T) {
				entered, release := make(chan struct{}), make(chan struct{})
				var once sync.Once
				finish := func() { once.Do(func() { close(release) }) }
				defer finish()
				r := &OSResolver{lookup: func(string) DNSResult {
					close(entered)
					<-release
					if failure {
						return DNSResult{Err: errors.New("late_native_failure")}
					}
					return DNSResult{Addresses: []netip.Addr{netip.MustParseAddr("8.8.8.8")}}
				}}
				var active, chosen atomic.Int32
				retired := make(chan struct{})
				n := Network{Lookup: r.Lookup, BeginWork: func() (func(), bool) { active.Add(1); return func() { active.Add(-1); close(retired) }, true }, Life: context.Background(), CallGuestWithAdmission: func(context.Context, context.Context, []byte) ([]byte, error) {
					chosen.Add(1)
					return nil, errors.New("late selection must not execute")
				}}
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				out := make(chan error, 1)
				go func() { _, err := n.resolve(ctx, hop{Hostname: "fixture.invalid", DNSTimeoutMS: 5000}); out <- err }()
				<-entered
				if cancelled {
					cancel()
				}
				select {
				case err := <-out:
					want := "media_dns_timeout"
					if cancelled {
						want = "media_cancelled"
					}
					if err == nil || err.Error() != want {
						t.Fatal(err, want)
					}
				case <-time.After(8 * time.Second):
					t.Fatal("logical source settle did not complete")
				}
				if active.Load() != 1 {
					t.Fatal("physical lease released before native completion", active.Load())
				}
				finish()
				r.CloseAndWait()
				select {
				case <-retired:
				case <-time.After(time.Second):
					t.Fatal("physical relay did not retire")
				}
				if active.Load() != 0 || chosen.Load() != 0 {
					t.Fatal("late outcome escaped into guest/dial", active.Load(), chosen.Load())
				}
			})
		}
	}
}
