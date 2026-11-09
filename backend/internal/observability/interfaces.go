package observability

import "context"

// Emitter is the stable producer boundary. Sink ownership and retention are
// deliberately absent from components which only produce diagnostics.
type Emitter interface{ Emit(Event) error }

// RetainedLogger is the pure-read logging dependency used by HTTP. Source
// LoggingStatus is a separate contract and is not implied by this interface.
type RetainedLogger interface {
	Emitter
	RetainedJSON() []byte
	Retention() RetentionStatus
}

// LogOwner is the explicit finalization dependency of an owning composition.
// A caller borrowing one must not close it as a side effect of domain cleanup.
type LogOwner interface {
	RetainedLogger
	Close(context.Context) error
	// Physical ownership is observed separately from logical health/errors.
	WaitPhysicalClose(context.Context) error
}
