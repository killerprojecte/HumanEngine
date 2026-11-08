package plugins

import (
	"bufio"
	"context"
	"crypto/rand"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os/exec"
	"sync"
)

//go:embed v1-configuration-operation.mjs
var configurationOperationSource string

// ConfigurationOperation pins one validated JS object and isolate generation.
// The owner serializes Prepare and Close and must close even after validation
// failure. Validation happens before any main writer transaction is acquired.
type ConfigurationOperation struct {
	prepare     func() (Inspection, error)
	validate    func(json.RawMessage) (Inspection, error)
	validated   bool
	transferred bool
	resume      func(context.Context, json.RawMessage, string, func(json.RawMessage) error, func(), func() bool) (json.RawMessage, error)
	close       func()
	once        sync.Once
	used        bool
}

func (o *ConfigurationOperation) Prepare() (Inspection, error) {
	if o.used || !o.validated {
		return Inspection{}, errors.New("plugin_configuration_operation_closed")
	}
	o.used = true
	return o.prepare()
}

// Validate repeats preservation/validation using the latest stored config, in
// the same module but with a newly constructed object, before writer admission.
func (o *ConfigurationOperation) Validate(previous json.RawMessage) (Inspection, error) {
	if o.used || o.validated {
		return Inspection{}, errors.New("plugin_configuration_operation_closed")
	}
	result, err := o.validate(previous)
	if err == nil && !result.Failed {
		o.validated = true
	}
	return result, err
}
func (o *ConfigurationOperation) CanResume() bool {
	return o != nil && o.resume != nil && !o.transferred
}
func (o *ConfigurationOperation) Resume(ctx context.Context, config json.RawMessage, requestID string, install func(json.RawMessage) error, admitted func(), preserve func() bool) (json.RawMessage, error) {
	if !o.used || !o.validated || o.transferred || o.resume == nil {
		return nil, errors.New("plugin_configuration_operation_closed")
	}
	return o.resume(ctx, config, requestID, install, admitted, preserve)
}
func (o *ConfigurationOperation) Close() {
	if o != nil {
		o.once.Do(func() { o.used = true; o.close() })
	}
}
func operationInput(input InspectionRequest) (InspectionRequest, error) {
	var token [16]byte
	if _, err := rand.Read(token[:]); err != nil {
		return input, err
	}
	input.Token = hex.EncodeToString(token[:])
	input.Operation = "configuration_validate_first"
	if len(input.Config) == 0 {
		input.Config = json.RawMessage(`{}`)
	}
	if len(input.Previous) == 0 {
		input.Previous = json.RawMessage(`{}`)
	}
	return input, nil
}

// BeginConfiguration runs only for an explicit disabled configuration operation.
// Close cancels and Waits for actual process exit, including a blocked JS hook.
func BeginConfiguration(ctx context.Context, node string, diagnostics io.Writer, input InspectionRequest) (*ConfigurationOperation, Inspection, error) {
	input, err := operationInput(input)
	if err != nil {
		return nil, Inspection{}, err
	}
	life, cancel := context.WithCancel(ctx)
	command := exec.CommandContext(life, node, "--input-type=module", "--eval", nodeConfigSource+"\n"+managementSource+"\n"+configurationOperationSource)
	command.Stderr = diagnostics
	stdin, err := command.StdinPipe()
	if err != nil {
		cancel()
		return nil, Inspection{}, err
	}
	stdout, err := command.StdoutPipe()
	if err != nil {
		cancel()
		_ = stdin.Close()
		return nil, Inspection{}, err
	}
	if err = command.Start(); err != nil {
		cancel()
		_ = stdin.Close()
		return nil, Inspection{}, err
	}
	reader := bufio.NewReader(stdout)
	call := func(input InspectionRequest) (Inspection, error) {
		raw, err := json.Marshal(input)
		if err == nil {
			_, err = stdin.Write(append(raw, '\n'))
		}
		if err == nil {
			raw, err = reader.ReadBytes('\n')
		}
		if err != nil {
			if ctx.Err() != nil {
				return Inspection{}, ctx.Err()
			}
			return Inspection{}, errors.New("plugin_inspection_failed")
		}
		var result Inspection
		if err = json.Unmarshal(raw, &result); err != nil {
			return Inspection{}, errors.New("plugin_inspection_invalid_response")
		}
		return result, nil
	}
	operation := &ConfigurationOperation{close: func() { cancel(); _ = stdin.Close(); _ = command.Wait() }}
	operation.validate = func(previous json.RawMessage) (Inspection, error) {
		input.Previous = previous
		input.Operation = "configuration_validate"
		return call(input)
	}
	operation.prepare = func() (Inspection, error) { input.Operation = "configuration_prepare"; return call(input) }
	result, err := call(input)
	if err != nil || result.Failed {
		operation.Close()
		return nil, result, err
	}
	return operation, result, nil
}

// BeginLoadedConfiguration never creates a second host. Its token cannot migrate
// to a replacement generation, even if that replacement uses the same plugin ID.
func (h *V1Host) BeginLoadedConfiguration(ctx context.Context, id string, input InspectionRequest) (*ConfigurationOperation, Inspection, error) {
	return h.beginLoadedConfiguration(ctx, id, input, false)
}
func (h *V1Host) BeginCurrentConfiguration(ctx context.Context, id string, input InspectionRequest) (*ConfigurationOperation, Inspection, error) {
	return h.beginLoadedConfiguration(ctx, id, input, true)
}
func (h *V1Host) beginLoadedConfiguration(ctx context.Context, id string, input InspectionRequest, currentModule bool) (*ConfigurationOperation, Inspection, error) {
	input, err := operationInput(input)
	if err != nil {
		return nil, Inspection{}, err
	}
	h.lifecycle.Lock()
	lease := h.enabled[id]
	if currentModule {
		lease = h.modules[id]
		input.CurrentModule = true
	}
	valid := !h.closed && lease != nil && !lease.physicalFinalizing && !lease.processDetached && protocolProcessLive(lease) && (!lease.closing || currentModule && lease.retired && (lease.managementOnly || len(lease.configurationTokens) > 0))
	h.lifecycle.Unlock()
	if !valid {
		return nil, Inspection{}, ErrNotLoaded
	}
	call := func(input InspectionRequest) (Inspection, error) {
		result, err := h.inspectLoadedLease(ctx, id, lease, input)
		if ctx.Err() != nil {
			_ = h.disableLease(ctx, id, lease)
			return Inspection{}, ctx.Err()
		}
		return result, err
	}
	operation := &ConfigurationOperation{}
	operation.close = func() {
		if operation.transferred {
			return
		}
		defer h.releaseConfiguration(id, lease, input.Token)
		if ctx.Err() != nil {
			_ = h.disableLease(ctx, id, lease)
			return
		}
		input.Operation = "configuration_release"
		_, _ = h.inspectLoadedLease(ctx, id, lease, input)
	}
	operation.resume = func(load context.Context, config json.RawMessage, requestID string, install func(json.RawMessage) error, admitted func(), preserve func() bool) (json.RawMessage, error) {
		return h.resumeModule(load, id, lease, input.Token, config, requestID, install, func() {
			operation.transferred = true
			if admitted != nil {
				admitted()
			}
		}, preserve)
	}
	operation.validate = func(previous json.RawMessage) (Inspection, error) {
		input.Previous = previous
		input.Operation = "configuration_validate"
		return call(input)
	}
	operation.prepare = func() (Inspection, error) { input.Operation = "configuration_prepare"; return call(input) }
	result, err := call(input)
	if err != nil || result.Failed {
		operation.Close()
		return nil, result, err
	}
	return operation, result, nil
}
