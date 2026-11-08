package plugins

import (
	"context"
	"fmt"
)

// BeginManagedConfiguration owns an import-only isolate for an explicit operation.
// The control process is temporary until an activation is admitted. It is shared
// with existing enabled modules, never duplicated or migrated across processes.
// No setup, adapter or runtime capability is created by this operation.
func (h *V1Host) BeginManagedConfiguration(ctx context.Context, id string, input InspectionRequest) (*ConfigurationOperation, Inspection, error) {
	h.lifecycle.Lock()
	if h.closed {
		h.lifecycle.Unlock()
		return nil, Inspection{}, ErrNotLoaded
	}
	lease := h.modules[id]
	usable := lease != nil && !lease.physicalFinalizing && !lease.processDetached && protocolProcessLive(lease)
	if usable {
		h.lifecycle.Unlock()
		return h.BeginCurrentConfiguration(ctx, id, input)
	}
	var err error
	if h.process == nil {
		h.process, err = startNode(ctx, h.node, h.diagnostics, h.workerExited, h.moduleReady, h.adapterRPC, h.toolRPC, h.modelProtocolModuleURLs)
		if err != nil {
			h.lifecycle.Unlock()
			return nil, Inspection{}, err
		}
	}
	h.watchProcess(h.process)
	h.sequence++
	lease = &nodeLease{moduleLifetime: &moduleLifetime{done: make(chan struct{})}, moduleReady: make(chan struct{}), managementOnly: true, key: fmt.Sprintf("%s:%d", id, h.sequence), process: h.process, generation: h.sequence, closing: true, retired: true}
	lease.adapterContext, lease.adapterCancel = context.WithCancel(context.Background())
	h.modules[id] = lease
	h.retired[lease] = id
	pending, err := lease.process.begin(map[string]any{"op": "enable", "plugin": lease.key, "pluginId": id, "path": input.Path, "managementOnly": true, "deferAdapterStart": true})
	h.lifecycle.Unlock()
	if err == nil {
		_, err = pending.wait(ctx)
	}
	if err != nil {
		_ = h.finishRetiredLease(id, lease)
		return nil, Inspection{}, err
	}
	operation, result, err := h.BeginCurrentConfiguration(ctx, id, input)
	if err != nil {
		_ = h.finishRetiredLease(id, lease)
	}
	return operation, result, err
}

// DetachConfigurationModule establishes a true disable boundary. Already accepted
// tokens keep their own module; new explicit operations import a fresh isolate.
func (h *V1Host) DetachConfigurationModule(id string) {
	h.lifecycle.Lock()
	delete(h.modules, id)
	h.lifecycle.Unlock()
}
