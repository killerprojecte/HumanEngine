// Package dispatch implements one guarded send. It neither queues nor restores
// work and never retries an adapter, including after an uncertain receipt.
package dispatch

import (
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"regexp"
	"strings"
)

type Status string

const (
	Sent      Status = "sent"
	Failed    Status = "failed"
	Unknown   Status = "unknown"
	Cancelled Status = "cancelled"
)

type SceneRef struct {
	ID              string  `json:"id"`
	Kind            string  `json:"kind"`
	AdapterID       string  `json:"adapterId"`
	AccountID       string  `json:"accountId"`
	PlatformSceneID string  `json:"platformSceneId"`
	DisplayName     *string `json:"displayName,omitempty"`
	AvatarURL       *string `json:"avatarUrl,omitempty"`
}

// Parts pass through untouched. Dispatch does not decode/re-encode opaque media
// or message data, impose size limits, or replace a transport's schema validation.
type Segment struct {
	Parts     []json.RawMessage `json:"parts"`
	ReplyToID *string           `json:"replyToId,omitempty"`
}
type Receipt struct {
	Status    Status  `json:"status"`
	MessageID *string `json:"messageId,omitempty"`
	Detail    *string `json:"detail,omitempty"`
	failure   *Failure
}
type ErrorContext struct {
	Operation string `json:"operation,omitempty"`
	Stage     string `json:"stage,omitempty"`
}

// Failure is private outcome metadata. Error deliberately never exposes arbitrary
// adapter/provider messages. Cause retains the original error/value for trusted
// diagnostics; Receipt's JSON projection cannot include it.
type Failure struct {
	Cause   any          `json:"-"`
	Context ErrorContext `json:"-"`
}

func (f *Failure) Error() string { return "send_stage_failed" }
func (f *Failure) Unwrap() error { err, _ := f.Cause.(error); return err }
func failureOf(cause any) *Failure {
	if f, ok := cause.(*Failure); ok && f != nil {
		return f
	}
	return &Failure{Cause: cause}
}
func PreserveOutcomeError(receipt Receipt, cause any, context ErrorContext) Receipt {
	f := *failureOf(cause)
	// JavaScript WeakMap metadata only attaches to objects, not primitive abort
	// reasons. Retain this distinction at the V1 boundary.
	if carriesContext(f.Cause) {
		if f.Context.Operation == "" {
			f.Context.Operation = safeContextLabel(context.Operation)
		}
		if f.Context.Stage == "" {
			f.Context.Stage = safeContextLabel(context.Stage)
		}
	}
	receipt.failure = &f
	return receipt
}
func OutcomeError(receipt Receipt) *Failure { return receipt.failure }

var contextLabel = regexp.MustCompile(`^[A-Za-z0-9_.:@/ -]{1,128}$`)

func safeContextLabel(value string) string {
	if contextLabel.MatchString(value) && !strings.Contains(value, "://") {
		return value
	}
	return ""
}
func carriesContext(value any) bool {
	if value == nil {
		return false
	}
	switch reflect.TypeOf(value).Kind() {
	case reflect.Pointer, reflect.Struct, reflect.Map, reflect.Slice, reflect.Array:
		return true
	default:
		return false
	}
}

type TurnContext struct {
	Scene  SceneRef
	Mode   string
	TurnID string
	Signal *Signal
}
type ValidationRequest struct {
	OperationID string    `json:"operationId"`
	Segments    []Segment `json:"segments"`
}
type Guard struct {
	// CheckGrounding returns true when a source dependency is invalid.
	CheckGrounding       func([]Segment) bool
	CaptureTransmitGuard func() func() bool
	Validate             func(ValidationRequest, *Signal) *Future[bool]
}
type TypingDelay func(Segment, *Signal) *Future[struct{}]
type Send func(SceneRef, Segment, *Signal, func() error) *Future[Receipt]

// Adapter handles have reference identity. Replacing a handle, even with the same
// Send function, invalidates an in-flight final transmit gate. Publish immutable
// handles from the registry and synchronize registry/eligibility/grounding reads.
type Adapter struct {
	Send           Send
	abortContained bool
}

// AbortContainedAdapter is for core-installed wrappers that already arbitrate
// cancellation across their complete physical send scope. Mark before publishing.
func AbortContainedAdapter(adapter *Adapter) *Adapter {
	adapter.abortContained = true
	return adapter
}

type Services struct {
	Writer     FlowWriter
	Eligible   func(*TurnContext) bool
	IsRecalled func(sceneID, messageID string) bool
	// Checked ports preserve errors from real authority reads. When present they
	// replace, rather than supplement, the compatibility boolean callbacks.
	EligibleChecked   func(*TurnContext) (bool, error)
	IsRecalledChecked func(sceneID, messageID string) (bool, error)
	Adapter           func(id string) *Adapter
}

func text(value string) *string { return &value }
func invalidated() Receipt      { return Receipt{Status: Cancelled, Detail: text("send_invalidated")} }
func terminal(status Status) string {
	switch status {
	case Sent:
		return "completed"
	case Unknown:
		return "partial"
	case Cancelled:
		return "cancelled"
	default:
		return "failed"
	}
}
func deliveryReason(status Status) string {
	switch status {
	case Sent:
		return "segment_delivered"
	case Unknown:
		return "delivery_unknown"
	case Cancelled:
		return "send_invalidated"
	default:
		return "delivery_failed"
	}
}
func nextStep(receipt Receipt, index, total int) string {
	if receipt.Status == Sent && index+1 < total {
		return "send_segment"
	}
	return "replyer"
}
func (s Services) eligible(context *TurnContext) (bool, error) {
	if s.EligibleChecked != nil {
		return s.EligibleChecked(context)
	}
	return s.Eligible(context), nil
}
func recalled(s Services, context *TurnContext, segment Segment) (bool, error) {
	if segment.ReplyToID == nil || *segment.ReplyToID == "" {
		return false, nil
	}
	if s.IsRecalledChecked != nil {
		return s.IsRecalledChecked(context.Scene.ID, *segment.ReplyToID)
	}
	return s.IsRecalled(context.Scene.ID, *segment.ReplyToID), nil
}
func admissible(s Services, context *TurnContext, segment Segment) (bool, error) {
	eligible, err := s.eligible(context)
	if err != nil || !eligible {
		return false, err
	}
	revoked, err := recalled(s, context, segment)
	return !revoked, err
}

// DispatchSegment sends at most once. A non-cancellation validation failure is
// returned as an error; adapter failures become receipts. The final transmit gate
// MUST be called by the adapter immediately before the physical effect, after any
// asynchronous encoding. An authoritative adapter receipt is never downgraded by
// a later eligibility/cancellation check after transmission.
func DispatchSegment(services Services, context *TurnContext, segment Segment, operationID string, guard Guard, typingDelay TypingDelay, index, total int) (receipt Receipt, failure *Failure) {
	dispatch := startFlow(services.Writer, "send_segment", nil, context.Signal, FlowDetails{Counts: map[string]int{"segmentIndex": index + 1, "segmentCount": total, "parts": len(segment.Parts)}, NextStep: "typing_delay"})
	defer func() {
		if cause := recover(); cause != nil {
			failure = failureOf(cause)
		}
		status, reason := terminal(receipt.Status), deliveryReason(receipt.Status)
		if failure != nil {
			status, reason = "failed", "segment_send_failed"
			if context.Signal.Aborted() {
				status = "cancelled"
			}
		} else if receipt.Status == Sent && context.Mode == "simulation" {
			reason = "simulated_delivery"
		}
		state := receipt.Status
		if state == "" {
			state = Failed
		}
		cause := failure
		if cause == nil {
			cause = OutcomeError(receipt)
		}
		dispatch.finish(status, FlowDetails{ReceiptState: state, Reason: reason, Counts: map[string]int{"segmentIndex": index + 1, "segmentCount": total}, NextStep: nextStep(receipt, index, total)}, cause)
	}()
	if valid, err := admissible(services, context, segment); err != nil {
		return Receipt{}, failureOf(err)
	} else if !valid {
		return invalidated(), nil
	}
	typing := startFlow(services.Writer, "typing_delay", dispatch, context.Signal, FlowDetails{NextStep: "send_validation"})
	if typingDelay != nil {
		_, err := AwaitSendStage(func() *Future[struct{}] { return typingDelay(segment, context.Signal) }, nil)
		if err != nil {
			typing.finish("cancelled", FlowDetails{Reason: "typing_delay_cancelled", NextStep: "none"}, err)
			return Receipt{Status: Cancelled, Detail: text("typing_delay_cancelled")}, nil
		}
		typing.finish("completed", FlowDetails{Reason: "typing_wait_finished", NextStep: "send_validation"}, nil)
	} else {
		reason := "typing_delay_disabled"
		if context.Mode == "simulation" {
			reason = "simulation_mode"
		}
		typing.finish("skipped", FlowDetails{Reason: reason, NextStep: "send_validation"}, nil)
	}
	if valid, err := admissible(services, context, segment); err != nil {
		return Receipt{}, failureOf(err)
	} else if !valid || guard.CheckGrounding([]Segment{segment}) {
		return invalidated(), nil
	}
	validation := startFlow(services.Writer, "send_validation", dispatch, context.Signal, FlowDetails{NextStep: "adapter_send"})
	valid, err := AwaitSendStage(func() *Future[bool] {
		return guard.Validate(ValidationRequest{OperationID: operationID, Segments: []Segment{segment}}, context.Signal)
	}, context.Signal)
	if err != nil {
		status := "failed"
		if context.Signal.Aborted() {
			status = "cancelled"
		}
		validation.finish(status, FlowDetails{Reason: "send_validation_failed", NextStep: "none"}, err)
		if context.Signal.Aborted() {
			return PreserveOutcomeError(invalidated(), err, ErrorContext{"send.validate", "send_validation"}), nil
		}
		return Receipt{}, err
	}
	if valid {
		validation.finish("completed", FlowDetails{Reason: "send_validated", NextStep: "adapter_send"}, nil)
	} else {
		validation.finish("cancelled", FlowDetails{Reason: "send_invalidated", NextStep: "none"}, nil)
	}
	if !valid {
		return invalidated(), nil
	}
	if allowed, err := admissible(services, context, segment); err != nil {
		return Receipt{}, failureOf(err)
	} else if !allowed {
		return invalidated(), nil
	}
	if context.Mode == "simulation" {
		id, err := uuid()
		if err != nil {
			return Receipt{}, failureOf(err)
		}
		return Receipt{Status: Sent, MessageID: &id}, nil
	}
	var transmitGuard func() bool
	if guard.CaptureTransmitGuard != nil {
		transmitGuard = guard.CaptureTransmitGuard()
	}
	sending := startFlow(services.Writer, "adapter_send", dispatch, context.Signal, FlowDetails{NextStep: "replyer"})
	receipt = sendOnce(services, context, segment, guard, transmitGuard)
	reason := deliveryReason(receipt.Status)
	if receipt.Detail != nil && knownDiagnosticCode(*receipt.Detail) {
		reason = *receipt.Detail
	}
	sending.finish(terminal(receipt.Status), FlowDetails{ReceiptState: receipt.Status, Reason: reason, NextStep: nextStep(receipt, index, total)}, OutcomeError(receipt))
	return receipt, nil
}

func sendOnce(services Services, context *TurnContext, segment Segment, guard Guard, transmitGuard func() bool) (receipt Receipt) {
	invoked := false
	var failure *Failure
	defer func() {
		if cause := recover(); cause != nil {
			failure = failureOf(cause)
		}
		if failure == nil {
			return
		}
		status, detail := Cancelled, "delivery_unknown"
		if invoked {
			status = Unknown
		}
		if context.Signal.Aborted() {
			detail = "request_cancelled"
			if isTimeout(context.Signal.Reason()) {
				detail = "request_timeout"
			}
		}
		receipt = PreserveOutcomeError(Receipt{Status: status, Detail: &detail}, failure, ErrorContext{"adapter.send", "adapter_send"})
	}()
	adapter := services.Adapter(context.Scene.AdapterID)
	if adapter == nil {
		return PreserveOutcomeError(Receipt{Status: Failed, Detail: text("adapter_unavailable")}, errors.New("adapter_unavailable"), ErrorContext{"adapter.send", "adapter_send"})
	}
	signal := context.Signal
	if adapter.abortContained {
		signal = nil
	}
	receipt, failure = AwaitSendStage(func() *Future[Receipt] {
		invoked = true
		return adapter.Send(context.Scene, segment, context.Signal, func() error {
			eligible, err := services.eligible(context)
			if err != nil {
				return err
			}
			if !eligible || services.Adapter(context.Scene.AdapterID) != adapter {
				return errors.New("send_invalidated")
			}
			revoked, err := recalled(services, context, segment)
			if err != nil {
				return err
			}
			if revoked || (transmitGuard != nil && !transmitGuard()) || guard.CheckGrounding([]Segment{segment}) {
				return errors.New("send_invalidated")
			}
			return nil
		})
	}, signal)
	return receipt
}

func uuid() (string, error) {
	var data [16]byte
	if _, err := rand.Read(data[:]); err != nil {
		return "", err
	}
	data[6] = (data[6] & 15) | 64
	data[8] = (data[8] & 63) | 128
	return fmt.Sprintf("%x-%x-%x-%x-%x", data[:4], data[4:6], data[6:8], data[8:10], data[10:]), nil
}
