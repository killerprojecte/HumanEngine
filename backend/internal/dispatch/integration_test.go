package dispatch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

func testPorts(signal *Signal, adapter *Adapter, writer FlowWriter) (Services, *TurnContext, Guard) {
	context := &TurnContext{Scene: SceneRef{ID: "scene", AdapterID: "fixture", AccountID: "account", Kind: "group", PlatformSceneID: "group"}, Mode: "real", Signal: signal}
	services := Services{Writer: writer, Eligible: func(*TurnContext) bool { return !signal.Aborted() }, IsRecalled: func(string, string) bool { return false }, Adapter: func(string) *Adapter { return adapter }}
	guard := Guard{CheckGrounding: func([]Segment) bool { return false }, Validate: func(ValidationRequest, *Signal) *Future[bool] { return Resolved(true) }}
	return services, context, guard
}

func TestPhysicalHTTPCancellationNeverReplays(t *testing.T) {
	signal := NewSignal()
	entered, release, exited := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.URL.Path != "/scene/group" {
			t.Errorf("wrong physical scope: %s", r.URL.Path)
		}
		data, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
		}
		if !strings.Contains(string(data), `"text":"synthetic"`) {
			t.Errorf("segment not forwarded: %s", data)
		}
		close(entered)
		<-release
		_, _ = io.WriteString(w, `{"status":"sent","messageId":"physically-accepted"}`)
		close(exited)
	}))
	defer server.Close()
	adapterDone := make(chan struct{})
	adapter := &Adapter{Send: func(scene SceneRef, segment Segment, signal *Signal, gate func() error) *Future[Receipt] {
		future := NewFuture[Receipt]()
		go func() {
			defer close(adapterDone)
			raw, err := json.Marshal(segment)
			if err != nil {
				future.Reject(err)
				return
			}
			if err := gate(); err != nil {
				future.Resolve(Receipt{Status: Failed, Detail: text("send_invalidated")})
				return
			}
			request, err := http.NewRequestWithContext(signal, http.MethodPost, server.URL+"/scene/"+scene.PlatformSceneID, strings.NewReader(string(raw)))
			if err != nil {
				future.Reject(err)
				return
			}
			response, err := server.Client().Do(request)
			if err != nil {
				future.Reject(err)
				return
			}
			defer response.Body.Close()
			var receipt Receipt
			if err := json.NewDecoder(response.Body).Decode(&receipt); err != nil {
				future.Reject(err)
				return
			}
			future.Resolve(receipt)
		}()
		return future
	}}
	var mu sync.Mutex
	var flows []FlowEvent
	services, turn, guard := testPorts(signal, adapter, FlowWriterFunc(func(event FlowEvent) { mu.Lock(); flows = append(flows, event); mu.Unlock() }))
	done := make(chan Receipt, 1)
	go func() {
		receipt, failure := DispatchSegment(services, turn, Segment{Parts: []json.RawMessage{json.RawMessage(`{"type":"text","text":"synthetic"}`)}}, "operation", guard, nil, 0, 1)
		if failure != nil {
			t.Errorf("unexpected failure: %v", failure)
		}
		done <- receipt
	}()
	<-entered // The server has physically accepted a single request.
	signal.Abort(context.Canceled)
	receipt := <-done
	close(release)
	<-exited
	<-adapterDone
	if receipt.Status != Unknown || receipt.Detail == nil || *receipt.Detail != "request_cancelled" {
		t.Fatalf("cancelled physical request must remain uncertain: %+v", receipt)
	}
	if requests.Load() != 1 {
		t.Fatalf("physical dispatch was replayed: %d", requests.Load())
	}
	if listenerCount(signal) != 0 {
		t.Fatal("listeners retained")
	}
	mu.Lock()
	defer mu.Unlock()
	terminals := 0
	for _, event := range flows {
		if event.Stage == "adapter_send" && event.Status != "started" && event.Status != "cancel_requested" {
			terminals++
			if event.Status != "partial" {
				t.Fatalf("late response changed terminal: %+v", event)
			}
		}
	}
	if terminals != 1 {
		t.Fatalf("expected one physical terminal, got %d", terminals)
	}
}

func TestSignalCancellationFenceAndSettlementPublication(t *testing.T) {
	for _, first := range []string{"cancel", "resolve"} {
		t.Run(first, func(t *testing.T) {
			signal, future := NewSignal(), NewFuture[string]()
			entered, release := make(chan struct{}), make(chan struct{})
			signal.subscribe(func() { close(entered); <-release })
			done := make(chan struct {
				value   string
				failure *Failure
			}, 1)
			go func() {
				value, failure := AwaitSendStage(func() *Future[string] { return future }, signal)
				done <- struct {
					value   string
					failure *Failure
				}{value, failure}
			}()
			awaitObserved(future)
			if first == "resolve" {
				future.Resolve("authoritative")
			}
			cancelled := make(chan struct{})
			go func() { signal.Abort(errors.New("fixture_cancel")); close(cancelled) }()
			<-entered
			if first == "cancel" {
				future.Resolve("late")
			}
			// The callback remains in flight. Cancellation is already published,
			// but its continuation may not overtake synchronous abort listeners.
			if first == "cancel" {
				select {
				case result := <-done:
					t.Fatalf("crossed abort listener fence: %+v", result)
				default:
				}
			}
			close(release)
			<-cancelled
			result := <-done
			if first == "cancel" && result.failure == nil {
				t.Fatalf("late resolution won: %+v", result)
			}
			if first == "resolve" && (result.failure != nil || result.value != "authoritative") {
				t.Fatalf("published receipt lost: %+v", result)
			}
			if listenerCount(signal) != 0 {
				t.Fatal("listeners retained")
			}
		})
	}
}

func TestConcurrentIndependentScenesAndCancellation(t *testing.T) {
	const count = 64
	var wait sync.WaitGroup
	for i := 0; i < count; i++ {
		wait.Add(1)
		go func(i int) {
			defer wait.Done()
			signal := NewSignal()
			future := NewFuture[Receipt]()
			sceneID := fmt.Sprintf("scene-%d", i)
			var sends atomic.Int32
			adapter := &Adapter{Send: func(scene SceneRef, _ Segment, _ *Signal, gate func() error) *Future[Receipt] {
				sends.Add(1)
				if scene.ID != sceneID {
					t.Errorf("cross-scene send: %s != %s", scene.ID, sceneID)
				}
				if err := gate(); err != nil {
					return Rejected[Receipt](err)
				}
				return future
			}}
			services, turn, guard := testPorts(signal, adapter, nil)
			turn.Scene.ID = sceneID
			done := make(chan Receipt, 1)
			go func() {
				r, err := DispatchSegment(services, turn, Segment{}, "operation", guard, nil, 0, 1)
				if err != nil {
					t.Error(err)
				}
				done <- r
			}()
			awaitObserved(future)
			if i%2 == 0 {
				signal.Abort(context.Canceled)
			} else {
				future.Resolve(Receipt{Status: Sent, MessageID: text(sceneID)})
			}
			r := <-done
			future.Resolve(Receipt{Status: Sent, MessageID: text("late")})
			expected := Sent
			if i%2 == 0 {
				expected = Unknown
			}
			if r.Status != expected || sends.Load() != 1 || listenerCount(signal) != 0 {
				t.Errorf("scene %d: %+v, sends=%d, listeners=%d", i, r, sends.Load(), listenerCount(signal))
			}
			if expected == Sent && (r.MessageID == nil || *r.MessageID != sceneID) {
				t.Errorf("cross-scene receipt: %+v", r)
			}
		}(i)
	}
	wait.Wait()
}

func TestLoggingFailureDoesNotChangeReceiptOrRetry(t *testing.T) {
	signal := NewSignal()
	calls := 0
	adapter := &Adapter{Send: func(SceneRef, Segment, *Signal, func() error) *Future[Receipt] {
		calls++
		return Resolved(Receipt{Status: Sent, MessageID: text("receipt")})
	}}
	services, turn, guard := testPorts(signal, adapter, FlowWriterFunc(func(FlowEvent) { panic("broken logger") }))
	receipt, failure := DispatchSegment(services, turn, Segment{}, "operation", guard, nil, 0, 1)
	if failure != nil || receipt.Status != Sent || calls != 1 || listenerCount(signal) != 0 {
		t.Fatalf("logger affected send: %+v %v %d", receipt, failure, calls)
	}
}

func TestOneShotReceiptAndNoDuplicateSettlement(t *testing.T) {
	signal, future := NewSignal(), NewFuture[Receipt]()
	done := make(chan Receipt, 1)
	go func() {
		r, err := AwaitSendStage(func() *Future[Receipt] { return future }, signal)
		if err != nil {
			t.Error(err)
		}
		done <- r
	}()
	awaitObserved(future)
	original := PreserveOutcomeError(Receipt{Status: Failed, Detail: text("send_invalidated")}, errors.New("private"), ErrorContext{"inner", "inner"})
	if !future.Resolve(original) || future.Resolve(Receipt{Status: Sent}) || future.Reject(errors.New("late")) {
		t.Fatal("future settlement was not one-shot")
	}
	actual := <-done
	signal.Abort(context.Canceled)
	if !reflect.DeepEqual(actual, original) {
		t.Fatalf("authoritative receipt changed: %+v", actual)
	}
}

// Deferred workers cannot claim inline-return priority by winning a scheduling
// race against the caller's return. The handshake deliberately forces that race.
func TestDeferredCompletionBeforeInvocationReturnIsNotInline(t *testing.T) {
	signal, future := NewSignal(), NewFuture[string]()
	value, failure := AwaitSendStage(func() *Future[string] {
		published := make(chan struct{})
		go func() {
			signal.Abort(errors.New("cancelled"))
			future.Resolve("late")
			close(published)
		}()
		<-published
		return future
	}, signal)
	if failure == nil || value != "" {
		t.Fatalf("deferred completion acquired inline priority: %q, %v", value, failure)
	}
	if listenerCount(signal) != 0 {
		t.Fatal("listener retained")
	}
}

func TestAlreadySettledFutureIsAuthoritativeWithinNewInvocation(t *testing.T) {
	signal, future := NewSignal(), NewFuture[string]()
	future.Resolve("preexisting")
	value, failure := AwaitSendStage(func() *Future[string] {
		signal.Abort(errors.New("cancelled"))
		return future
	}, signal)
	if failure != nil || value != "preexisting" {
		t.Fatalf("preexisting settlement lost: %q, %v", value, failure)
	}
}
