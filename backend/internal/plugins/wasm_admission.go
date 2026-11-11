package plugins

import (
	"context"
	"errors"
	"math"
)

// BindCallWithAdmission separates waiting for the captured generation's call
// slot from synchronous execution once admitted. Cancelling wait never closes
// an executing guest. Execution and free remain owned by life and instance
// teardown. Existing BindCall/Call implementations are deliberately unchanged.
func (h *WASMHost) BindCallWithAdmission(id string) (func(context.Context, context.Context, []byte) ([]byte, error), error) {
	h.mu.Lock()
	instance := h.instances[id]
	valid := !h.closed && instance != nil && instance.stopped.Err() == nil
	h.mu.Unlock()
	if !valid {
		return nil, ErrNotLoaded
	}
	return func(wait, life context.Context, input []byte) ([]byte, error) {
		return h.callInstanceAdmitted(wait, life, id, instance, input)
	}, nil
}
func (h *WASMHost) callInstanceAdmitted(wait, ctx context.Context, id string, expected *wasmInstance, input []byte) (output []byte, resultErr error) {
	if err := wait.Err(); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	h.mu.Lock()
	instance := h.instances[id]
	valid := !h.closed && instance != nil && (expected == nil || expected == instance)
	h.mu.Unlock()
	if !valid {
		return nil, ErrNotLoaded
	}
	select {
	case <-wait.Done():
		return nil, wait.Err()
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-instance.stopped.Done():
		return nil, ErrNotLoaded
	case <-instance.done:
	}
	if instance.err != nil {
		return nil, instance.err
	}
	select {
	case <-wait.Done():
		return nil, wait.Err()
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-instance.stopped.Done():
		return nil, ErrNotLoaded
	case <-instance.calls:
	}
	defer func() {
		// Release physical call ownership before joining retirement, which
		// also waits on this gate when cancellation closes the guest.
		instance.calls <- struct{}{}
		if instance.module.IsClosed() {
			resultErr = errors.Join(resultErr, h.disableInstance(context.Background(), id, instance))
		}
		if resultErr != nil {
			output = nil
		}
	}()
	if err := wait.Err(); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if instance.stopped.Err() != nil || instance.module.IsClosed() {
		return nil, ErrNotLoaded
	}
	// This is the wasm32 address width, not a configurable resource/admission limit.
	if uint64(len(input)) > math.MaxUint32 {
		return nil, errors.New("plugin_input_exceeds_wasm32_address_space")
	}
	callContext, cancel := context.WithCancel(ctx)
	detach := context.AfterFunc(instance.stopped, cancel)
	defer func() { detach(); cancel() }()
	var allocations []uint32
	defer func() {
		// free is guest code too. It must share caller/lifecycle cancellation,
		// and a failing cleanup must not turn a cancelled call into success.
		for i := len(allocations) - 1; i >= 0; i-- {
			_, err := instance.module.ExportedFunction("free").Call(callContext, uint64(allocations[i]))
			resultErr = errors.Join(resultErr, err)
		}
		resultErr = errors.Join(resultErr, callContext.Err())
		if instance.stopped.Err() != nil {
			resultErr = errors.Join(resultErr, ErrNotLoaded)
		}
		if resultErr != nil {
			output = nil
		}
	}()
	allocated, err := instance.module.ExportedFunction("alloc").Call(callContext, uint64(len(input)))
	if err != nil {
		return nil, err
	}
	ptr := uint32(allocated[0])
	allocations = append(allocations, ptr)
	if !instance.module.Memory().Write(ptr, input) {
		return nil, errors.New("plugin_input_memory_invalid")
	}
	response, err := instance.module.ExportedFunction("plugin_call").Call(callContext, uint64(ptr), uint64(len(input)))
	if err != nil {
		return nil, err
	}
	outPtr, outLen := uint32(response[0]>>32), uint32(response[0])
	allocations = append(allocations, outPtr)
	memory, ok := instance.module.Memory().Read(outPtr, outLen)
	if !ok {
		return nil, errors.New("plugin_output_memory_invalid")
	}
	return append([]byte(nil), memory...), nil
}
