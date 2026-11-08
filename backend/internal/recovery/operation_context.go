package recovery

import (
	"context"
	"database/sql"
	"errors"
	"sync"
)

type operationContextKey struct{}

// operationContext belongs to one root invocation, not a goroutine or the whole
// database. Separate requests sharing a parent context get separate operations.
// Sharing one operation concurrently is a caller ownership error: use a distinct
// root operation for independent work, and borrowed WriteSession for nested work.
type operationContext struct {
	mu             sync.Mutex
	authority      *Authority
	main           *sql.DB
	active, closed bool
}

// OperationContext is a ROOT COMPOSITION API. HTTP/engine root adapters create
// it once and defer release, then preserve the returned context through every
// layer. Service methods must never silently create replacements. An inherited
// operation is checked and reused; release is then a no-op, not ownership transfer.
// This does not detect arbitrary Go closures that discard an owned context and
// deliberately mint from context.Background inside an unmarked writer callback.
func (a *Authority) OperationContext(ctx context.Context, main *sql.DB) (context.Context, func(), error) {
	if ctx == nil || a == nil || main == nil {
		return nil, nil, errors.New("recovery_operation_context_required")
	}
	if existing, _ := ctx.Value(operationContextKey{}).(*operationContext); existing != nil {
		existing.mu.Lock()
		defer existing.mu.Unlock()
		if e := existing.binding(a, main); e != nil {
			return nil, nil, e
		}
		if existing.active {
			return nil, nil, errors.New("recovery_async_transaction_not_allowed")
		}
		return ctx, func() {}, nil
	}
	operation := &operationContext{authority: a, main: main}
	var once sync.Once
	release := func() { once.Do(func() { operation.mu.Lock(); operation.closed = true; operation.mu.Unlock() }) }
	return context.WithValue(ctx, operationContextKey{}, operation), release, nil
}
func (o *operationContext) binding(a *Authority, main *sql.DB) error {
	if o.authority != a || o.main != main {
		return errors.New("recovery_operation_binding")
	}
	if o.closed {
		return errors.New("recovery_operation_expired")
	}
	return nil
}

// RequireAsyncRoot rejects before acquiring a pool connection. Missing context
// is also rejected: an unmarked owner cannot accidentally deadlock async delete.
func (a *Authority) RequireAsyncRoot(ctx context.Context, main *sql.DB) error {
	if ctx == nil {
		return errors.New("recovery_operation_context_required")
	}
	operation, _ := ctx.Value(operationContextKey{}).(*operationContext)
	if operation == nil {
		return errors.New("recovery_operation_context_required")
	}
	operation.mu.Lock()
	defer operation.mu.Unlock()
	if e := operation.binding(a, main); e != nil {
		return e
	}
	if operation.active {
		return errors.New("recovery_async_transaction_not_allowed")
	}
	return nil
}
func (a *Authority) enterOperation(ctx context.Context, main *sql.DB) (func(), error) {
	operation, _ := ctx.Value(operationContextKey{}).(*operationContext)
	if operation == nil {
		return func() {}, nil
	} // Existing synchronous callers remain compatible.
	operation.mu.Lock()
	defer operation.mu.Unlock()
	if e := operation.binding(a, main); e != nil {
		return nil, e
	}
	if operation.active {
		return nil, errors.New("recovery_transaction_reentry")
	}
	operation.active = true
	return func() { operation.mu.Lock(); operation.active = false; operation.mu.Unlock() }, nil
}
func (a *Authority) checkOperation(ctx context.Context, main *sql.DB) error {
	operation, _ := ctx.Value(operationContextKey{}).(*operationContext)
	if operation == nil {
		return nil
	}
	operation.mu.Lock()
	defer operation.mu.Unlock()
	return operation.binding(a, main)
}

// ForkTaskOperation explicitly transfers a validated root binding into a new
// supervised task lifetime. The caller owns cancellation and must release only
// after all task work (including cleanup writes) has finished. It never repairs
// a missing, foreign, expired, or active owner and is not a transaction retry API.
func (a *Authority) ForkTaskOperation(ctx context.Context, main *sql.DB) (context.Context, func(), error) {
	if ctx == nil || a == nil || main == nil {
		return nil, nil, errors.New("recovery_operation_context_required")
	}
	parent, _ := ctx.Value(operationContextKey{}).(*operationContext)
	if parent == nil {
		return nil, nil, errors.New("recovery_operation_context_required")
	}
	parent.mu.Lock()
	defer parent.mu.Unlock()
	if err := parent.binding(a, main); err != nil {
		return nil, nil, err
	}
	if parent.active {
		return nil, nil, errors.New("recovery_async_transaction_not_allowed")
	}
	task := &operationContext{authority: a, main: main}
	var once sync.Once
	release := func() { once.Do(func() { task.mu.Lock(); task.closed = true; task.mu.Unlock() }) }
	return context.WithValue(context.WithoutCancel(ctx), operationContextKey{}, task), release, nil
}
