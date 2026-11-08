package plugins

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"io"
	"os/exec"
)

//go:embed v1-management.mjs
var managementSource string

//go:embed v1-inspect.mjs
var inspectSource string

// InspectionRequest is an explicit user management operation, not a startup
// probe or background poll. Config and Previous remain private native pipe data.
type InspectionRequest struct {
	Operation     string          `json:"operation"`
	Token         string          `json:"token,omitempty"`
	CurrentModule bool            `json:"currentModule,omitempty"`
	ID            string          `json:"id,omitempty"`
	Path          string          `json:"path"`
	Config        json.RawMessage `json:"config"`
	Previous      json.RawMessage `json:"previous"`
}
type InspectionIssue struct {
	Field  string `json:"field"`
	Reason string `json:"reason"`
}
type Inspection struct {
	Issues            []InspectionIssue `json:"issues"`
	Manifest          json.RawMessage   `json:"manifest"`
	Config            json.RawMessage   `json:"config"`
	Redacted          json.RawMessage   `json:"redacted"`
	ConfiguredSecrets json.RawMessage   `json:"configuredSecrets"`
	Failed            bool              `json:"failed"`
	Configuration     bool              `json:"configuration"`
	Code              string            `json:"code"`
	SystemCode        string            `json:"systemCode"`
	HTTPStatus        any               `json:"httpStatus"`
}

// InspectV1 runs the exact embedded source schema/secret functions with the
// plugin's own synchronous configuration hooks in a one-shot Node process.
// No setup, shared-host mutation or idle process is created. A cancelled caller
// kills the process, then waits for actual exit before releasing ownership.
// InspectExplicitV1 uses the same exact SDK resolution map as this host, while
// remaining a one-shot metadata/secret operation with no runtime registrations.
func (h *V1Host) InspectExplicitV1(ctx context.Context, input InspectionRequest) (Inspection, error) {
	h.lifecycle.Lock()
	if h.closed {
		h.lifecycle.Unlock()
		return Inspection{}, ErrNotLoaded
	}
	aliases := append([]string(nil), h.modelProtocolModuleURLs...)
	node, diagnostics := h.node, h.diagnostics
	h.lifecycle.Unlock()
	return inspectV1(ctx, node, diagnostics, input, aliases)
}

func InspectV1(ctx context.Context, node string, diagnostics io.Writer, input InspectionRequest) (Inspection, error) {
	return inspectV1(ctx, node, diagnostics, input, nil)
}
func inspectV1(ctx context.Context, node string, diagnostics io.Writer, input InspectionRequest, aliases []string) (Inspection, error) {
	if input.Operation != "manifest" && input.Operation != "validate" && input.Operation != "prepare" && input.Operation != "secrets" {
		return Inspection{}, errors.New("invalid_plugin_inspection_operation")
	}
	if len(input.Config) == 0 {
		input.Config = json.RawMessage(`{}`)
	}
	if len(input.Previous) == 0 {
		input.Previous = json.RawMessage(`{}`)
	}
	raw, err := json.Marshal(input)
	if err != nil {
		return Inspection{}, err
	}
	resolution, _ := json.Marshal(map[string]any{"modelProtocolSDK": nodeModelProtocolSDK, "modelProtocolModuleURLs": aliases})
	command := exec.CommandContext(ctx, node, "--input-type=module", "--eval", nodeConfigSource+"\n"+managementSource+"\nconst inspectionSDK="+string(resolution)+";\n"+inspectSource)
	command.Stdin = bytes.NewReader(raw)
	command.Stderr = diagnostics
	var output bytes.Buffer
	command.Stdout = &output
	if err = command.Run(); err != nil {
		if ctx.Err() != nil {
			return Inspection{}, ctx.Err()
		}
		return Inspection{}, errors.New("plugin_inspection_failed")
	}
	var result Inspection
	if err = json.Unmarshal(output.Bytes(), &result); err != nil {
		return Inspection{}, errors.New("plugin_inspection_invalid_response")
	}
	return result, nil
}

// InspectLoadedV1 preserves the enabled module's closure state. The exact lease
// is checked both before admission and publication, never selected by handle.
func (h *V1Host) InspectLoadedV1(ctx context.Context, id string, input InspectionRequest) (Inspection, error) {
	return h.inspectLoadedLease(ctx, id, nil, input)
}
func (h *V1Host) inspectLoadedLease(ctx context.Context, id string, expected *nodeLease, input InspectionRequest) (Inspection, error) {
	if len(input.Config) == 0 {
		input.Config = json.RawMessage(`{}`)
	}
	if len(input.Previous) == 0 {
		input.Previous = json.RawMessage(`{}`)
	}
	if input.Operation != "manifest" && input.Operation != "validate" && input.Operation != "prepare" && input.Operation != "secrets" && input.Operation != "configuration_validate_first" && input.Operation != "configuration_validate" && input.Operation != "configuration_prepare" && input.Operation != "configuration_release" {
		return Inspection{}, errors.New("invalid_plugin_inspection_operation")
	}
	h.lifecycle.Lock()
	lease := h.enabled[id]
	if expected != nil {
		lease = expected
	}
	configuration := input.Operation == "configuration_validate_first" || input.Operation == "configuration_validate" || input.Operation == "configuration_prepare" || input.Operation == "configuration_release"
	accepted := lease != nil && lease.configurationTokens[input.Token]
	if input.Operation == "configuration_validate_first" && lease != nil && !h.closed && protocolProcessLive(lease) && !lease.physicalFinalizing && ((h.enabled[id] == lease && !lease.closing) || (input.CurrentModule && h.modules[id] == lease && lease.retired && (lease.managementOnly || len(lease.configurationTokens) > 0))) {
		if lease.configurationTokens == nil {
			lease.configurationTokens = map[string]bool{}
		}
		if accepted {
			h.lifecycle.Unlock()
			return Inspection{}, errors.New("plugin_configuration_operation_invalid")
		}
		lease.configurationTokens[input.Token] = true
		accepted = true
	}
	liveOwner := lease != nil && (h.enabled[id] == lease || lease.retired || lease.retiring)
	if h.closed || !liveOwner || lease.dead || !protocolProcessLive(lease) || lease.closing && !(configuration && accepted && (lease.retiring || lease.retired)) || configuration && !accepted {
		h.lifecycle.Unlock()
		return Inspection{}, ErrNotLoaded
	}
	pending, err := lease.process.begin(map[string]any{"op": "inspect_plugin", "plugin": lease.key, "input": input})
	h.lifecycle.Unlock()
	if err != nil {
		return Inspection{}, err
	}
	raw, err := pending.wait(ctx)
	if ctx.Err() != nil {
		// Synchronous custom hooks cannot cooperate with an abort message. Retire
		// only this exact isolate, never the other leases on the shared process.
		_ = h.disableLease(ctx, id, lease)
		return Inspection{}, ctx.Err()
	}
	if err != nil {
		return Inspection{}, err
	}
	h.lifecycle.Lock()
	current := !h.closed && !lease.dead && protocolProcessLive(lease) && ((h.enabled[id] == lease && !lease.closing) || (configuration && lease.configurationTokens[input.Token] && (lease.retiring || lease.retired)))
	h.lifecycle.Unlock()
	if !current {
		return Inspection{}, ErrNotLoaded
	}
	var result Inspection
	if err = json.Unmarshal(raw, &result); err != nil {
		return Inspection{}, errors.New("plugin_inspection_invalid_response")
	}
	return result, nil
}

// AbortPlugin revokes runtime work immediately while retaining the isolate for
// queued synchronous management hooks. Disable subsequently performs real unload.
func (h *V1Host) AbortPlugin(id string) { h.abortPluginLease(id, nil) }
func (h *V1Host) abortPluginLease(id string, expected *nodeLease) {
	h.lifecycle.Lock()
	lease := h.enabled[id]
	if lease == nil || lease.closing || expected != nil && expected != lease {
		h.lifecycle.Unlock()
		return
	}
	lease.ready = false
	lease.adapterCancel()
	pending, err := lease.process.begin(map[string]any{"op": "abort_plugin", "plugin": lease.key})
	h.lifecycle.Unlock()
	if err == nil {
		pending.remove()
	}
}
