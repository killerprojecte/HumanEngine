//go:build web_read_candidate

package host

import (
	"context"
	"errors"
	"net/netip"
	"reflect"
	"sync"
	"testing"
	"time"
)

func requireDNSDrain(t *testing.T, job LookupJob) {
	t.Helper()
	select {
	case <-job.Drained:
	case <-time.After(3 * time.Second):
		t.Fatal("lookup did not drain")
	}
}

func TestOSDNSCancelledBeforeAdmission(t *testing.T) {
	r := &OSResolver{lookup: func(string) DNSResult { t.Error("cancelled work admitted"); return DNSResult{} }}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	job := r.Lookup(ctx, "fixture.invalid")
	requireDNSDrain(t, job)
	if !errors.Is((<-job.Result).Err, context.Canceled) {
		t.Fatal("missing cancellation")
	}
	r.CloseAndWait()
}

func TestOSDNSJoinIncludesCleanupAndAbandonedResult(t *testing.T) {
	for _, failure := range []bool{false, true} {
		t.Run(map[bool]string{false: "success", true: "failure"}[failure], func(t *testing.T) {
			entered, releaseCall, cleaning, releaseCleanup := make(chan struct{}), make(chan struct{}), make(chan struct{}), make(chan struct{})
			var callOnce, cleanupOnce sync.Once
			release := func() { callOnce.Do(func() { close(releaseCall) }); cleanupOnce.Do(func() { close(releaseCleanup) }) }
			defer release()
			nativeFailure := errors.New("synthetic_native_failure")
			r := &OSResolver{lookup: func(string) DNSResult {
				defer func() { close(cleaning); <-releaseCleanup }()
				close(entered)
				<-releaseCall
				if failure {
					return DNSResult{Err: nativeFailure}
				}
				return DNSResult{Addresses: []netip.Addr{netip.MustParseAddr("8.8.8.8")}}
			}}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			job := r.Lookup(ctx, "fixture.invalid")
			<-entered
			cancel()
			r.Close()
			closed := r.Lookup(context.Background(), "another.invalid")
			if !errors.Is((<-closed.Result).Err, ErrOSDNSClosed) {
				t.Fatal("admission reopened")
			}
			requireDNSDrain(t, closed)
			joined := make(chan struct{})
			go func() { r.Wait(); close(joined) }()
			select {
			case <-job.Drained:
				t.Fatal("cancelled native call falsely drained")
			default:
			}
			select {
			case <-joined:
				t.Fatal("shutdown passed blocked call")
			default:
			}
			callOnce.Do(func() { close(releaseCall) })
			<-cleaning
			select {
			case <-job.Drained:
				t.Fatal("cleanup still running after drain")
			default:
			}
			select {
			case <-job.Result:
				t.Fatal("published before cleanup")
			default:
			}
			cleanupOnce.Do(func() { close(releaseCleanup) })
			// Do not consume Result until after physical completion. No relay is needed
			// for a producer to finish, unlike the older synthetic unbuffered ports.
			requireDNSDrain(t, job)
			<-joined
			answer, ok := <-job.Result
			if !ok || (failure && !errors.Is(answer.Err, nativeFailure)) || (!failure && len(answer.Addresses) != 1) {
				t.Fatal(answer, ok)
			}
			if _, ok := <-job.Result; ok {
				t.Fatal("more than one result")
			}
			r.CloseAndWait()
		})
	}
}

func TestOSDNSAlreadyCompletedBeforeObservation(t *testing.T) {
	want := []netip.Addr{netip.MustParseAddr("2001:4860:4860::8888"), netip.MustParseAddr("8.8.8.8"), netip.MustParseAddr("8.8.8.8")}
	r := &OSResolver{lookup: func(string) DNSResult { return DNSResult{Addresses: want} }}
	job := r.Lookup(context.Background(), "fixture.invalid")
	r.CloseAndWait()
	requireDNSDrain(t, job)
	if got := <-job.Result; !reflect.DeepEqual(got.Addresses, want) {
		t.Fatal(got)
	}
}

func TestOSDNSNativeFamilyPreserved(t *testing.T) {
	v4, _ := nativeDNSAddress([]byte{8, 8, 8, 8})
	mapped := netip.MustParseAddr("::ffff:8.8.8.8").As16()
	v6, _ := nativeDNSAddress(mapped[:])
	if !v4.Is4() || !v6.Is6() || v6.Is4() {
		t.Fatal(v4, v6)
	}
}

func TestOSDNSAvailability(t *testing.T) {
	r, err := NewOSResolver()
	if !osDNSAvailable {
		if !errors.Is(err, ErrOSDNSUnavailable) || r != nil {
			t.Fatal(r, err)
		}
		return
	}
	if err != nil {
		t.Fatal(err)
	}
	r.CloseAndWait()
	var zero OSResolver
	job := zero.Lookup(context.Background(), "fixture.invalid")
	requireDNSDrain(t, job)
	if !errors.Is((<-job.Result).Err, ErrOSDNSUnavailable) {
		t.Fatal("zero resolver silently usable")
	}
}
