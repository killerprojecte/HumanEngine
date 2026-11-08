package plugins

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
)

// resumeModule consumes the sole transfer token before publishing a new,
// immutable runtime generation. The physical module/worker key stays stable.
// An old operation finalizer therefore cannot terminate the new activation.
func (h *V1Host) resumeModule(ctx context.Context, id string, old *nodeLease, token string, config json.RawMessage, requestID string, install func(json.RawMessage) error, admitted func(), preserve func() bool) (json.RawMessage, error) {
	if !json.Valid(config) {
		return nil, errors.New("invalid_plugin_configuration")
	}
	h.lifecycle.Lock()
	valid := ctx.Err() == nil && !h.closed && h.modules[id] == old && old.retired && !old.physicalFinalizing && protocolProcessLive(old) && len(old.configurationTokens) == 1 && old.configurationTokens[token] && h.enabled[id] == nil
	if !valid {
		h.lifecycle.Unlock()
		return nil, ErrNotLoaded
	}
	h.sequence++
	lease := &nodeLease{moduleReady: make(chan struct{}), moduleLoaded: true, key: old.key, process: old.process, generation: h.sequence, moduleLifetime: old.moduleLifetime, handles: map[string]string{}, modelCompletions: map[string]bool{}, turnStops: map[string]func() bool{}}
	lease.moduleReadyOnce.Do(func() { close(lease.moduleReady) })
	lease.adapterContext, lease.adapterCancel = context.WithCancel(context.Background())
	lease.adapterCalls = map[string]adapterCall{}
	delete(old.configurationTokens, token)
	old.retired = false
	old.processDetached = true
	delete(h.retired, old)
	h.enabled[id] = lease
	h.modules[id] = lease
	pending, err := lease.process.begin(map[string]any{"op": "reactivate", "activation": strconv.FormatUint(lease.generation, 10), "plugin": lease.key, "config": config, "requestId": requestID})
	h.lifecycle.Unlock()
	if admitted != nil {
		admitted()
	}
	var result json.RawMessage
	if err == nil {
		result, err = pending.wait(ctx)
	}
	if err != nil {
		h.failedActivation(ctx, id, lease, preserve)
		return nil, err
	}
	return h.finishEnable(ctx, id, lease, result, install, preserve)
}

func (h *V1Host) moduleReady(process *nodeProcess, key string) {
	h.lifecycle.Lock()
	defer h.lifecycle.Unlock()
	for _, lease := range h.modules {
		if lease.process == process && lease.key == key {
			lease.moduleLoaded = true
			lease.moduleReadyOnce.Do(func() { close(lease.moduleReady) })
			return
		}
	}
}
