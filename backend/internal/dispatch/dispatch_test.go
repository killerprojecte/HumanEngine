package dispatch

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"reflect"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"testing"
)

type fixtureConfig struct {
	AfterTransmitChange string  `json:"afterTransmitChange"`
	AfterTransmitAbort  bool    `json:"afterTransmitAbort"`
	OmitMessageID       bool    `json:"omitMessageId"`
	MessageID           *string `json:"messageId"`
	Name                string  `json:"name"`
	Action              string  `json:"action"`
	Abort               bool    `json:"abort"`
	Order               string  `json:"order"`
	PreAbort            bool    `json:"preAbort"`
	Status              Status  `json:"status"`
	SendAbort           bool    `json:"sendAbort"`
	Contained           bool    `json:"contained"`
	Mode                string  `json:"mode"`
	Initial             string  `json:"initial"`
	Typing              string  `json:"typing"`
	TypingChange        string  `json:"typingChange"`
	Validate            string  `json:"validate"`
	ValidationChange    string  `json:"validationChange"`
	Missing             bool    `json:"missing"`
	LookupThrow         bool    `json:"lookupThrow"`
	BeforeInvokeAbort   bool    `json:"beforeInvokeAbort"`
	Send                string  `json:"send"`
	Cancel              string  `json:"cancel"`
	TransmitChange      string  `json:"transmitChange"`
	Reply               bool    `json:"reply"`
	EmptyReply          bool    `json:"emptyReply"`
	NoReply             bool    `json:"noReply"`
	PrivateError        bool    `json:"privateError"`
	Detail              *string `json:"detail"`
	Index               int     `json:"index"`
	Total               int     `json:"total"`
}
type fixtureCase struct {
	Name     string          `json:"name"`
	Config   fixtureConfig   `json:"config"`
	Expected json.RawMessage `json:"expected"`
}
type fixtures struct {
	Version  int               `json:"version"`
	Node     string            `json:"node"`
	Baseline string            `json:"baseline"`
	Sources  map[string]string `json:"sources"`
	Stages   []fixtureCase     `json:"stages"`
	Dispatch []fixtureCase     `json:"dispatch"`
}

func readFixtures(t *testing.T) fixtures {
	t.Helper()
	raw, err := os.ReadFile("testdata/traces-node.json")
	if err != nil {
		t.Fatal(err)
	}
	var f fixtures
	if err := json.Unmarshal(raw, &f); err != nil {
		t.Fatal(err)
	}
	if f.Version != 1 || f.Node != "v24.22.1" || f.Baseline != "24fe8a773d87f2f09996e259fca46524c2c02928" {
		t.Fatalf("unexpected oracle metadata: %+v", f)
	}
	for path, expected := range f.Sources {
		data, err := os.ReadFile("../../../" + path)
		if err != nil {
			t.Fatal(err)
		}
		hash := sha256.Sum256(data)
		if hex.EncodeToString(hash[:]) != expected {
			t.Fatalf("oracle changed: %s", path)
		}
	}
	return f
}
func assertProjection(t *testing.T, actual any, expected json.RawMessage) {
	t.Helper()
	raw, err := json.Marshal(actual)
	if err != nil {
		t.Fatal(err)
	}
	var a, b any
	if err := json.Unmarshal(raw, &a); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(expected, &b); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(a, b) {
		pretty, _ := json.MarshalIndent(a, "", "  ")
		t.Fatalf("trace differs\nactual: %s\nexpected: %s", pretty, expected)
	}
}
func causeProjection(cause any) map[string]any {
	if err, ok := cause.(error); ok {
		return map[string]any{"error": err.Error()}
	}
	return map[string]any{"value": cause}
}
func listenerCount(s *Signal) int {
	settlement.Lock()
	defer settlement.Unlock()
	return len(s.listeners)
}

// This tests the invocation-close boundary, not scheduler timing. The future's
// observer is installed atomically when the invocation closes. No sleeps select
// a winner and no provider/transport work is used in these fixtures.
func awaitObserved[T any](future *Future[T]) {
	for {
		settlement.Lock()
		ready := len(future.listeners) > 0
		settlement.Unlock()
		if ready {
			return
		}
		runtime.Gosched()
	}
}
func cancellation(kind string) any {
	if kind == "timeout" {
		return CancellationReason{Name: "TimeoutError", Message: "fixture_timeout"}
	}
	if kind == "string" {
		return "context_changed"
	}
	return errors.New("fixture_cancel")
}
func TestStageDifferential(t *testing.T) {
	for _, f := range readFixtures(t).Stages {
		t.Run(f.Name, func(t *testing.T) {
			c, signal, pending := f.Config, NewSignal(), NewFuture[string]()
			if c.PreAbort {
				signal.Abort(errors.New("fixture_cancel"))
			}
			calls := 0
			done := make(chan map[string]any, 1)
			go func() {
				value, failure := AwaitSendStage(func() *Future[string] {
					calls++
					if c.Abort {
						signal.Abort(errors.New("fixture_cancel"))
					}
					switch c.Action {
					case "resolve":
						return Resolved("receipt")
					case "reject":
						return Rejected[string](errors.New("fixture_error"))
					case "throw":
						panic(errors.New("fixture_error"))
					default:
						return pending
					}
				}, signal)
				result := map[string]any{"calls": calls, "listeners": listenerCount(signal)}
				if failure == nil {
					result["result"] = value
				} else {
					result["error"] = causeProjection(failure.Cause)
				}
				done <- result
			}()
			if c.Action == "pending" && !c.Abort {
				awaitObserved(pending)
				order := c.Order
				if order == "" {
					order = "resolve_abort"
				}
				for _, op := range strings.Split(order, "_") {
					switch op {
					case "resolve":
						pending.Resolve("receipt")
					case "reject":
						pending.Reject(errors.New("fixture_error"))
					case "abort":
						signal.Abort(errors.New("fixture_cancel"))
					// Publication order already represents the oracle microtask
					// fence. A scheduler yield cannot affect the outcome.
					case "later":
						runtime.Gosched()
					}
				}
			}
			actual := <-done
			pending.Resolve("late")
			pending.Reject(errors.New("late"))
			assertProjection(t, actual, f.Expected)
		})
	}
}

type fixtureState struct {
	sync.Mutex
	eligible, recalled, grounding bool
	version, sends, physical      int
	current                       *Adapter
	calls                         []string
	flows                         []FlowEvent
}

func (s *fixtureState) call(value string) { s.Lock(); s.calls = append(s.calls, value); s.Unlock() }
func (s *fixtureState) change(what string) {
	s.Lock()
	defer s.Unlock()
	switch what {
	case "eligibility":
		s.eligible = false
	case "reply":
		s.recalled = true
	case "grounding":
		s.grounding = true
	case "topic":
		s.version++
	case "adapter":
		clone := *s.current
		s.current = &clone
	}
}
func TestDispatchDifferential(t *testing.T) {
	for _, f := range readFixtures(t).Dispatch {
		t.Run(f.Name, func(t *testing.T) { assertProjection(t, executeDispatch(t, f.Config), f.Expected) })
	}
}
func executeDispatch(t *testing.T, c fixtureConfig) map[string]any {
	t.Helper()
	signal := NewSignal()
	state := &fixtureState{eligible: true, version: 1, calls: []string{}, flows: []FlowEvent{}}
	context := &TurnContext{Scene: SceneRef{ID: "s", Kind: "group", AdapterID: "a", AccountID: "b", PlatformSceneID: "g"}, Mode: "real", TurnID: "t", Signal: signal}
	if c.Mode != "" {
		context.Mode = c.Mode
	}
	segment := Segment{Parts: []json.RawMessage{json.RawMessage(`{"type":"text","text":"fixture 😀"}`), json.RawMessage(`{"type":"media","assetId":"opaque:asset","mediaType":"image"}`)}}
	if !c.NoReply {
		segment.ReplyToID = text("reply")
		if c.EmptyReply {
			segment.ReplyToID = text("")
		}
	}
	receipt := func() Receipt {
		status := c.Status
		if status == "" {
			status = Sent
		}
		r := Receipt{Status: status, Detail: c.Detail}
		if status == Sent && !c.OmitMessageID {
			r.MessageID = text("receipt")
			if c.MessageID != nil {
				r.MessageID = c.MessageID
			}
		}
		if c.PrivateError {
			r = PreserveOutcomeError(r, errors.New("private-error-canary"), ErrorContext{"inner.send", "inner"})
		}
		return r
	}
	validationPending, sendPending := NewFuture[bool](), NewFuture[Receipt]()
	type heldSend struct{ gate func() error }
	held := make(chan heldSend, 1)
	adapter := &Adapter{Send: func(scene SceneRef, got Segment, s *Signal, gate func() error) *Future[Receipt] {
		if !reflect.DeepEqual(scene, context.Scene) || !reflect.DeepEqual(got, segment) || s != signal {
			panic("fixture_argument_identity")
		}
		state.Lock()
		state.sends++
		state.Unlock()
		state.call("send")
		if c.SendAbort {
			signal.Abort(cancellation("error"))
		}
		switch c.Send {
		case "throw":
			panic(errors.New("fixture_send"))
		case "reject":
			return Rejected[Receipt](errors.New("fixture_send"))
		case "pending", "microtask_abort_receipt":
			return sendPending
		case "held", "held_throw":
			held <- heldSend{gate}
			return sendPending
		}
		state.Lock()
		state.physical++
		state.Unlock()
		state.change(c.AfterTransmitChange)
		if c.AfterTransmitAbort {
			signal.Abort(cancellation("error"))
		}
		return Resolved(receipt())
	}}
	if c.Contained {
		AbortContainedAdapter(adapter)
	}
	state.current = adapter
	state.change(c.Initial)
	if c.PreAbort {
		signal.Abort(cancellation("error"))
	}
	services := Services{
		Writer: FlowWriterFunc(func(event FlowEvent) { state.Lock(); defer state.Unlock(); state.flows = append(state.flows, event) }),
		Eligible: func(*TurnContext) bool {
			state.call("eligible")
			state.Lock()
			eligible := state.eligible
			state.Unlock()
			return eligible && !signal.Aborted()
		},
		IsRecalled: func(scene, id string) bool {
			if scene != "s" || id != "reply" {
				panic("fixture_recall_scope")
			}
			state.call("recall")
			state.Lock()
			defer state.Unlock()
			return state.recalled
		},
		Adapter: func(id string) *Adapter {
			if id != "a" {
				panic("fixture_adapter_scope")
			}
			state.call("adapter")
			if c.LookupThrow {
				panic(errors.New("fixture_lookup"))
			}
			if c.BeforeInvokeAbort {
				signal.Abort(cancellation("error"))
			}
			if c.Missing {
				return nil
			}
			state.Lock()
			defer state.Unlock()
			return state.current
		},
	}
	guard := Guard{
		CheckGrounding: func(segments []Segment) bool {
			if len(segments) != 1 || !reflect.DeepEqual(segments[0], segment) {
				panic("fixture_grounding_scope")
			}
			state.call("grounding")
			state.Lock()
			defer state.Unlock()
			return state.grounding
		},
		CaptureTransmitGuard: func() func() bool {
			state.call("capture")
			state.Lock()
			version := state.version
			state.Unlock()
			return func() bool { state.call("topic"); state.Lock(); defer state.Unlock(); return version == state.version }
		},
		Validate: func(request ValidationRequest, s *Signal) *Future[bool] {
			state.call("validate")
			if request.OperationID != "operation" || !reflect.DeepEqual(request.Segments, []Segment{segment}) || s != signal {
				panic("fixture_validation_scope")
			}
			state.change(c.ValidationChange)
			if strings.HasPrefix(c.Validate, "abort_") {
				signal.Abort(cancellation("error"))
			}
			switch c.Validate {
			case "throw":
				panic(errors.New("fixture_validation"))
			case "reject", "abort_reject":
				return Rejected[bool](errors.New("fixture_validation"))
			case "pending_abort", "pending_string_abort", "later_self_abort":
				return validationPending
			default:
				return Resolved(c.Validate != "false" && c.Validate != "abort_false")
			}
		},
	}
	var typing TypingDelay
	if c.Typing != "" {
		typing = func(Segment, *Signal) *Future[struct{}] {
			state.call("typing")
			state.change(c.TypingChange)
			if c.Typing == "abort_reject" {
				signal.Abort(cancellation("error"))
			}
			if c.Typing == "throw" {
				panic(errors.New("fixture_typing"))
			}
			if strings.Contains(c.Typing, "reject") {
				return Rejected[struct{}](errors.New("fixture_typing"))
			}
			return Resolved(struct{}{})
		}
	}
	type outcome struct {
		receipt Receipt
		failure *Failure
	}
	done := make(chan outcome, 1)
	total := c.Total
	if total == 0 {
		total = 1
	}
	go func() {
		r, f := DispatchSegment(services, context, segment, "operation", guard, typing, c.Index, total)
		done <- outcome{r, f}
	}()
	if strings.HasPrefix(c.Validate, "pending_") || c.Validate == "later_self_abort" {
		awaitObserved(validationPending)
		kind := "error"
		if c.Validate == "pending_string_abort" || c.Validate == "later_self_abort" {
			kind = "string"
		}
		signal.Abort(cancellation(kind))
		if c.Validate == "later_self_abort" {
			validationPending.Resolve(false)
		}
	} else if c.Send == "microtask_abort_receipt" {
		awaitObserved(sendPending)
		signal.Abort(cancellation("error"))
		sendPending.Resolve(receipt())
	} else if c.Send == "pending" {
		awaitObserved(sendPending)
		signal.Abort(cancellation(c.Cancel))
		if c.Contained {
			sendPending.Resolve(receipt())
		}
	} else if c.Send == "held" || c.Send == "held_throw" {
		send := <-held
		awaitObserved(sendPending)
		state.change(c.TransmitChange)
		state.call("transmit")
		if err := send.gate(); err != nil {
			if c.Send == "held_throw" {
				sendPending.Reject(err)
			} else {
				sendPending.Resolve(Receipt{Status: Failed, Detail: text("send_invalidated")})
			}
		} else {
			state.Lock()
			state.physical++
			state.Unlock()
			sendPending.Resolve(receipt())
		}
	}
	result := <-done
	validationPending.Resolve(true)
	sendPending.Resolve(Receipt{Status: Sent, MessageID: text("late")})
	state.Lock()
	actual := map[string]any{"sends": state.sends, "physical": state.physical, "calls": state.calls, "flows": state.flows, "listeners": listenerCount(signal)}
	state.Unlock()
	if result.failure != nil {
		actual["error"] = causeProjection(result.failure.Cause)
	} else {
		r := result.receipt
		if c.Mode == "simulation" && r.Status == Sent {
			if r.MessageID == nil || !regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`).MatchString(*r.MessageID) {
				t.Fatalf("invalid UUID: %+v", r)
			}
			r.MessageID = text("<uuid>")
		}
		actual["result"] = r
		if failure := OutcomeError(r); failure != nil {
			actual["outcomeError"] = causeProjection(failure.Cause)
			actual["outcomeContext"] = failure.Context
		}
	}
	return actual
}

func TestReceiptPrivateCauseAndNoRetry(t *testing.T) {
	cause := errors.New("secret fixture error")
	original := PreserveOutcomeError(Receipt{Status: Unknown, Detail: text("request_timeout")}, cause, ErrorContext{"inner", "inner_stage"})
	wrapped := PreserveOutcomeError(original, OutcomeError(original), ErrorContext{"outer", "outer_stage"})
	if OutcomeError(wrapped).Cause != cause || OutcomeError(wrapped).Context != (ErrorContext{"inner", "inner_stage"}) {
		t.Fatal("inner error identity/context lost")
	}
	raw, err := json.Marshal(wrapped)
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != `{"status":"unknown","detail":"request_timeout"}` {
		t.Fatalf("private cause escaped: %s", raw)
	}
	if strings.Contains(fmt.Sprint(OutcomeError(wrapped)), "secret") {
		t.Fatal("private cause escaped Error()")
	}
}
