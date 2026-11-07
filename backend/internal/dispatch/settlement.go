package dispatch

import (
	"context"
	"errors"
	"sync"
	"time"
)

// Settlement and cancellation share a linearization lock. Notifications only wake
// waiters; they never decide the winner. In particular, channel scheduling is not
// used as a replacement for the TypeScript promise/microtask ordering contract.
var settlement struct {
	sync.Mutex
	sequence uint64
}

type listener struct {
	active bool
	call   func()
}

// Signal is a context.Context with synchronous, ordered cancellation publication.
// The turn owner calls Abort, rather than racing an AfterFunc against receipts.
// Abort has no timeout policy. Adapters may pass the signal to ordinary Go IO.
type Signal struct {
	done      chan struct{}
	delivered chan struct{}
	sequence  uint64
	reason    any
	listeners []*listener
}

func NewSignal() *Signal { return &Signal{done: make(chan struct{}), delivered: make(chan struct{})} }

// Abort publishes its cause exactly once and invokes listeners in registration
// order. The first cause is retained, including non-error causes from V1 plugins.
func (s *Signal) Abort(reason any) {
	if reason == nil {
		reason = errors.New("request_cancelled")
	}
	settlement.Lock()
	if s.sequence != 0 {
		settlement.Unlock()
		return
	}
	settlement.sequence++
	s.sequence, s.reason = settlement.sequence, reason
	close(s.done)
	callbacks := append([]*listener(nil), s.listeners...)
	s.listeners = nil
	settlement.Unlock()
	for _, callback := range callbacks {
		settlement.Lock()
		active := callback.active
		callback.active = false
		settlement.Unlock()
		if active {
			callback.call()
		}
	}
	// Promise continuations run after synchronous abort listeners. A waiter may
	// observe the publication before its wake callback, but must honor this fence.
	close(s.delivered)
}

func (s *Signal) Aborted() bool {
	settlement.Lock()
	defer settlement.Unlock()
	return s.sequence != 0
}

func (s *Signal) Reason() any {
	settlement.Lock()
	defer settlement.Unlock()
	return s.reason
}

func (s *Signal) Done() <-chan struct{}       { return s.done }
func (s *Signal) Deadline() (time.Time, bool) { return time.Time{}, false }
func (s *Signal) Value(any) any               { return nil }
func (s *Signal) Err() error {
	if !s.Aborted() {
		return nil
	}
	if isTimeout(s.Reason()) {
		return context.DeadlineExceeded
	}
	return context.Canceled
}

func (s *Signal) subscribe(call func()) func() {
	settlement.Lock()
	if s.sequence != 0 {
		settlement.Unlock()
		call()
		return func() {}
	}
	l := &listener{active: true, call: call}
	s.listeners = append(s.listeners, l)
	settlement.Unlock()
	return func() {
		settlement.Lock()
		defer settlement.Unlock()
		l.active = false
		for i, existing := range s.listeners {
			if existing == l {
				s.listeners = append(s.listeners[:i], s.listeners[i+1:]...)
				break
			}
		}
	}
}

// CancellationReason preserves the DOM TimeoutError distinction without adding a
// timeout. A caller that already owns a deadline may also use DeadlineExceeded.
type CancellationReason struct{ Name, Message string }

func (r CancellationReason) Error() string { return r.Message }
func isTimeout(reason any) bool {
	switch r := reason.(type) {
	case CancellationReason:
		return r.Name == "TimeoutError"
	case *CancellationReason:
		return r != nil && r.Name == "TimeoutError"
	case error:
		return errors.Is(r, context.DeadlineExceeded)
	default:
		return false
	}
}

// Future separates invoking a callback from its asynchronous settlement.
// Resolved/Rejected explicitly denote an inline invocation return; NewFuture
// denotes deferred work. Do not resolve a NewFuture from the invocation itself
// to represent a synchronous result: return Resolved/Rejected instead. This
// distinction prevents a fast IO goroutine from accidentally obtaining inline
// receipt priority merely because it ran before the invocation returned.
// An invocation MUST return promptly. Blocking work belongs in the adapter's own
// IO goroutine. Dispatch never launches an unkillable callback goroutine or retries.
type Future[T any] struct {
	inline    bool
	sequence  uint64
	value     T
	failure   *Failure
	listeners []*listener
}

func NewFuture[T any]() *Future[T] { return &Future[T]{} }
func Resolved[T any](value T) *Future[T] {
	f := &Future[T]{inline: true}
	f.Resolve(value)
	return f
}
func Rejected[T any](cause any) *Future[T] {
	f := &Future[T]{inline: true}
	f.Reject(cause)
	return f
}
func (f *Future[T]) Resolve(value T) bool { return f.complete(value, nil) }
func (f *Future[T]) Reject(cause any) bool {
	var zero T
	return f.complete(zero, failureOf(cause))
}
func (f *Future[T]) complete(value T, failure *Failure) bool {
	settlement.Lock()
	if f.sequence != 0 {
		settlement.Unlock()
		return false
	}
	settlement.sequence++
	f.sequence, f.value, f.failure = settlement.sequence, value, failure
	callbacks := append([]*listener(nil), f.listeners...)
	f.listeners = nil
	settlement.Unlock()
	for _, callback := range callbacks {
		settlement.Lock()
		active := callback.active
		callback.active = false
		settlement.Unlock()
		if active {
			callback.call()
		}
	}
	return true
}

// AwaitSendStage is the invocation/settlement protocol from awaitSendStage:
//  1. Already cancelled: never invoke.
//  2. Invoke exactly once. Synchronous failure, an inline return, or a future
//     already settled at admission is authoritative even if the invocation aborts.
//  3. Deferred work uses publication order, even when a fast worker settles before
//     the invocation returns. Late settlement cannot replace the result.
//
// A nil signal means the caller already owns cancellation (typing/core wrapper).
func AwaitSendStage[T any](action func() *Future[T], signal *Signal) (T, *Failure) {
	var zero T
	wake := make(chan struct{}, 1)
	notify := func() {
		select {
		case wake <- struct{}{}:
		default:
		}
	}
	if signal != nil {
		remove := signal.subscribe(notify)
		defer remove()
	}
	settlement.Lock()
	admittedAt := settlement.sequence
	if signal != nil && signal.sequence != 0 {
		reason := signal.reason
		settlement.Unlock()
		<-signal.delivered
		return zero, failureOf(reason)
	}
	// Admission is the invocation's linearization point. An abort following
	// this point belongs to this invocation, rather than proving no call.
	settlement.Unlock()
	var pending *Future[T]
	var thrown *Failure
	func() {
		defer func() {
			if reason := recover(); reason != nil {
				thrown = failureOf(reason)
			}
		}()
		pending = action()
		if pending == nil {
			thrown = failureOf(errors.New("missing_stage_future"))
		}
	}()
	if thrown != nil {
		if signal != nil && signal.Aborted() {
			<-signal.delivered
		}
		return zero, thrown
	}
	settlement.Lock()
	// Closing invocation is explicit and atomic relative to both publications.
	if pending.sequence != 0 && (pending.inline || pending.sequence <= admittedAt) {
		value, failure := pending.value, pending.failure
		aborted := signal != nil && signal.sequence != 0
		settlement.Unlock()
		if aborted {
			<-signal.delivered
		}
		return value, failure
	}
	l := &listener{active: true, call: notify}
	pending.listeners = append(pending.listeners, l)
	settlement.Unlock()
	defer func() {
		settlement.Lock()
		defer settlement.Unlock()
		l.active = false
		for i, existing := range pending.listeners {
			if existing == l {
				pending.listeners = append(pending.listeners[:i], pending.listeners[i+1:]...)
				break
			}
		}
	}()
	for {
		settlement.Lock()
		if pending.sequence != 0 && (signal == nil || signal.sequence == 0 || pending.sequence < signal.sequence) {
			value, failure := pending.value, pending.failure
			aborted := signal != nil && signal.sequence != 0
			settlement.Unlock()
			if aborted {
				<-signal.delivered
			}
			return value, failure
		}
		if signal != nil && signal.sequence != 0 {
			reason := signal.reason
			settlement.Unlock()
			<-signal.delivered
			return zero, failureOf(reason)
		}
		settlement.Unlock()
		<-wake
	}
}
