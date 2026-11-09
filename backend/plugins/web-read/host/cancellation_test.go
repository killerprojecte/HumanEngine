//go:build web_read_candidate

package host_test

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/netip"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/killerprojecte/HumanEngine/backend/internal/toolresults"
	"github.com/killerprojecte/HumanEngine/backend/plugins/web-read/host"
)

// Every lease is observable. Drained means actual callback completion, not a
// request timeout or the return of an opaque operating-system resolver wrapper.
type trackedWork struct {
	n  atomic.Int32
	wg sync.WaitGroup
}

func (w *trackedWork) begin() (func(), bool) {
	w.n.Add(1)
	w.wg.Add(1)
	return func() { w.n.Add(-1); w.wg.Done() }, true
}
func TestLateDNSOutcomeAndPhysicalDrain(t *testing.T) {
	raw, e := os.ReadFile("../core/testdata/node-cancellation.json")
	if e != nil {
		t.Fatal(e)
	}
	var fixtures []struct {
		Name, NetworkError, ServiceStatus, SecondStatus                                 string
		ActiveAfterOutcome, CallbacksBeforeRelease, CallbacksAfterRelease, RequestCalls int
	}
	if e = json.Unmarshal(raw, &fixtures); e != nil {
		t.Fatal(e)
	}
	// JSON names differ only by initial capitalization, accepted by encoding/json.
	for _, tc := range fixtures {
		tc := tc
		t.Run(tc.Name, func(t *testing.T) {
			t.Parallel()
			var work trackedWork
			var calls, completed atomic.Int32
			release := make(chan struct{})
			jobs := make(chan struct{}, 2)
			lookup := func(context.Context, string) host.LookupJob {
				result := make(chan host.DNSResult)
				drained := make(chan struct{})
				jobs <- struct{}{}
				go func() {
					defer close(drained)
					<-release
					completed.Add(1)
					answer := host.DNSResult{Addresses: []netip.Addr{netip.MustParseAddr("8.8.8.8")}}
					if tc.Name == "late DNS failure" {
						answer.Err = errors.New("synthetic late DNS failure")
					}
					result <- answer
				}()
				return host.LookupJob{Result: result, Drained: drained}
			}
			network := host.Network{Life: context.Background(), CallGuestWithAdmission: admitNative(nativeGuest), BeginWork: work.begin, Lookup: lookup, DialContext: func(context.Context, string, string) (net.Conn, error) {
				calls.Add(1)
				return nil, errors.New("no socket expected")
			}}
			p := ports(nativeGuest, network.Fetch, &work.n)
			p.BeginWork = work.begin
			service, e := host.NewCandidate(p)
			if e != nil {
				t.Fatal(e)
			}
			first := make(chan json.RawMessage, 1)
			go func() {
				out, e := service.Execute(context.Background(), "web-read.request", json.RawMessage(`{"url":"http://fixture.invalid/"}`), nil)
				if e != nil {
					first <- json.RawMessage(`{"status":"unexpected_error"}`)
				} else {
					first <- out
				}
			}()
			direct := make(chan error, 1)
			go func() {
				_, e := network.Fetch(context.Background(), json.RawMessage(`{"url":"http://fixture.invalid/"}`), time.Minute)
				direct <- e
			}()
			<-jobs
			<-jobs
			e = <-direct
			if e == nil || e.Error() != tc.NetworkError {
				t.Fatal(e, tc.NetworkError)
			}
			var result map[string]any
			json.Unmarshal(<-first, &result)
			if result["status"] != tc.ServiceStatus {
				t.Fatal(result, tc.ServiceStatus)
			}
			second, e := service.Execute(context.Background(), "web-read.request", json.RawMessage(`{"url":"x"}`), nil)
			if e != nil {
				t.Fatal(e)
			}
			json.Unmarshal(second, &result)
			if result["status"] != tc.SecondStatus {
				t.Fatal(string(second), tc.SecondStatus)
			}
			if completed.Load() != int32(tc.CallbacksBeforeRelease) || work.n.Load() < 2 {
				t.Fatal("late DNS work escaped physical accounting", completed.Load(), work.n.Load())
			}
			close(release)
			work.wg.Wait()
			if calls.Load() != int32(tc.RequestCalls) || completed.Load() != int32(tc.CallbacksAfterRelease) {
				t.Fatal(calls.Load(), completed.Load())
			}
		})
	}
}
func TestCancelledGuestQueueDoesNotStartExecution(t *testing.T) {
	var work trackedWork
	life, cancelLife := context.WithCancel(context.Background())
	defer cancelLife()
	queued := make(chan struct{})
	var entered atomic.Int32
	call := func(wait, run context.Context, input []byte) ([]byte, error) {
		close(queued)
		<-wait.Done()
		return nil, wait.Err()
	}
	n := host.Network{Life: life, CallGuestWithAdmission: call, BeginWork: work.begin}
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		_, e := n.Fetch(ctx, json.RawMessage(`{"url":"http://fixture.invalid/"}`), time.Minute)
		result <- e
	}()
	<-queued
	cancel()
	select {
	case e := <-result:
		if !errors.Is(e, context.Canceled) {
			t.Fatal(e)
		}
	case <-time.After(time.Second):
		t.Fatal("request cancellation blocked behind queued guest")
	}
	work.wg.Wait()
	if life.Err() != nil || work.n.Load() != 0 || entered.Load() != 0 {
		t.Fatal("queued request changed generation or ran guest")
	}
}

func TestUnbufferedDNSResultAfterCancellationDrains(t *testing.T) {
	var work trackedWork
	entered, release := make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	lookup := func(context.Context, string) host.LookupJob {
		result := make(chan host.DNSResult)
		drained := make(chan struct{})
		close(entered)
		go func() {
			defer close(drained)
			<-release
			result <- host.DNSResult{Addresses: []netip.Addr{netip.MustParseAddr("8.8.8.8")}}
		}()
		return host.LookupJob{Result: result, Drained: drained}
	}
	network := host.Network{Life: context.Background(), CallGuestWithAdmission: admitNative(nativeGuest), BeginWork: work.begin, Lookup: lookup, DialContext: func(context.Context, string, string) (net.Conn, error) {
		calls.Add(1)
		return nil, errors.New("no dial expected")
	}}
	ctx, cancel := context.WithCancel(context.Background())
	out := make(chan error, 1)
	go func() {
		_, e := network.Fetch(ctx, json.RawMessage(`{"url":"http://fixture.invalid/"}`), time.Minute)
		out <- e
	}()
	<-entered
	cancel()
	if e := <-out; e == nil || e.Error() != "media_cancelled" {
		t.Fatal(e)
	}
	if work.n.Load() != 1 {
		t.Fatal("physical DNS lease lost before late callback", work.n.Load())
	}
	close(release)
	done := make(chan struct{})
	go func() { work.wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("unbuffered late DNS sender cannot finish")
	}
	if calls.Load() != 0 {
		t.Fatal("late result caused a dial")
	}
}
func TestServicePolicyQueueTimeoutAndSlotRelease(t *testing.T) {
	for _, operation := range []string{"cachePolicy", "error", "response"} {
		t.Run(operation, func(t *testing.T) {
			var work trackedWork
			var blocked atomic.Bool
			call := func(wait, life context.Context, input []byte) ([]byte, error) {
				var in map[string]any
				json.Unmarshal(input, &in)
				if in["operation"] == operation && blocked.CompareAndSwap(false, true) {
					<-wait.Done()
					return nil, wait.Err()
				}
				return admitNative(nativeGuest)(wait, life, input)
			}
			fetch := func(context.Context, json.RawMessage, time.Duration) (host.Resource, error) {
				if operation == "error" {
					return host.Resource{}, errors.New("synthetic_error")
				}
				return host.Resource{StatusCode: 200, Source: toolresults.Text("x"), ContentType: "text/plain", Headers: map[string]any{}, RawHeaders: []string{}, Finish: func() {}}, nil
			}
			p := ports(nativeGuest, fetch, &work.n)
			p.Config = json.RawMessage(`{"maxConcurrency":1,"timeoutMs":500}`)
			p.BeginWork = work.begin
			p.CallGuestWithAdmission = call
			s, e := host.NewCandidate(p)
			if e != nil {
				t.Fatal(e)
			}
			cache := func(context.Context, toolresults.Source, toolresults.Options) (toolresults.Result, error) {
				return toolresults.Result{ContentType: "text/plain", Content: "x", Encoding: toolresults.UTF8}, nil
			}
			raw, e := s.Execute(context.Background(), "web-read.request", json.RawMessage(`{"url":"http://fixture.invalid/"}`), cache)
			if e != nil {
				t.Fatal(e)
			}
			var result map[string]any
			json.Unmarshal(raw, &result)
			if result["status"] != "timeout" {
				t.Fatal(operation, string(raw))
			}
			raw, e = s.Execute(context.Background(), "web-read.request", json.RawMessage(`{"url":"http://fixture.invalid/"}`), cache)
			if e != nil {
				t.Fatal(e)
			}
			json.Unmarshal(raw, &result)
			if result["status"] == "busy" || result["status"] == "timeout" {
				t.Fatal("timed-out queue retained logical slot", string(raw))
			}
			work.wg.Wait()
		})
	}
}
func TestAdmittedSourcePolicyKeepsErrorAndResponsePriority(t *testing.T) {
	raw, e := os.ReadFile("../core/testdata/node-sync-policy.json")
	if e != nil {
		t.Fatal(e)
	}
	var fixtures []struct{ Operation, Status, Text string }
	json.Unmarshal(raw, &fixtures)
	for _, fixture := range fixtures {
		t.Run(fixture.Operation, func(t *testing.T) {
			var work trackedWork
			call := func(wait, life context.Context, input []byte) ([]byte, error) {
				if wait.Err() != nil {
					return nil, wait.Err()
				}
				var in map[string]any
				json.Unmarshal(input, &in)
				if in["operation"] == fixture.Operation {
					<-wait.Done()
				}
				return nativeGuest(life, input)
			}
			fetch := func(context.Context, json.RawMessage, time.Duration) (host.Resource, error) {
				if fixture.Operation == "error" {
					return host.Resource{}, errors.New("synthetic_error")
				}
				return host.Resource{StatusCode: 200, Source: toolresults.Text("source text"), ContentType: "text/plain", Headers: map[string]any{}, RawHeaders: []string{}, Finish: func() {}}, nil
			}
			p := ports(nativeGuest, fetch, &work.n)
			p.Config = json.RawMessage(`{"maxConcurrency":1,"timeoutMs":500}`)
			p.BeginWork = work.begin
			p.CallGuestWithAdmission = call
			s, e := host.NewCandidate(p)
			if e != nil {
				t.Fatal(e)
			}
			result, e := s.Execute(context.Background(), "web-read.read", json.RawMessage(`{"url":"http://fixture.invalid/"}`), func(context.Context, toolresults.Source, toolresults.Options) (toolresults.Result, error) {
				return toolresults.Result{ContentType: "text/plain", Content: "source text", Encoding: toolresults.UTF8}, nil
			})
			if e != nil {
				t.Fatal(e)
			}
			var got map[string]any
			json.Unmarshal(result, &got)
			if got["status"] != fixture.Status {
				t.Fatal("interrupted synchronous source callback", string(result), fixture.Status)
			}
			if fixture.Text != "" && got["text"] != fixture.Text {
				t.Fatal(string(result))
			}
			work.wg.Wait()
		})
	}
}
