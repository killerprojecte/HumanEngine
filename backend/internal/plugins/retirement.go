package plugins

import (
	"context"
	"errors"
)

// attachedProcessLeases is called only under lifecycle. Each owner detaches once
// before deciding last-process shutdown; concurrent retirements cannot both
// observe the other's already-ending lease as a reason to leave Node alive.
func (h *V1Host) attachedProcessLeases(process *nodeProcess) int {
	count := 0
	for _, lease := range h.enabled {
		if lease.process == process && !lease.processDetached {
			count++
		}
	}
	for lease := range h.retired {
		if lease.process == process && !lease.processDetached {
			count++
		}
	}
	return count
}

// RetireManagedRuntime removes all ordinary runtime capability but does not
// deadlock behind accepted configuration operations queued after this cleanup.
// Only their exact existing tokens may use the retired isolate.
func (h *V1Host) RetireManagedRuntime(ctx context.Context, id string) error {
	return h.retireManagedLease(ctx, id, nil)
}
func (h *V1Host) retireManagedLease(ctx context.Context, id string, expected *nodeLease) error {
	h.lifecycle.Lock()
	lease := h.enabled[id]
	if lease == nil || expected != nil && lease != expected {
		h.lifecycle.Unlock()
		return nil
	}
	if len(lease.configurationTokens) == 0 || lease.closing {
		h.lifecycle.Unlock()
		return h.disableLease(ctx, id, lease)
	}
	lease.retiring = true
	lease.closing = true
	lease.ready = false
	lease.runtimeDone = make(chan struct{})
	lease.toolWork.Seal()
	lease.adapterWork.Seal()
	lease.adapterCancel()
	for _, stop := range lease.turnStops {
		stop()
	}
	lease.turnStops = map[string]func() bool{}
	h.lifecycle.Unlock()
	_, err := lease.process.call(ctx, map[string]any{"op": "retire", "plugin": lease.key})
	if ctx.Err() != nil {
		_, e := lease.process.call(context.Background(), map[string]any{"op": "terminate", "plugin": lease.key})
		err = errors.Join(err, e)
	}
	h.stopAdapters(lease)
	h.lifecycle.Lock()
	if h.enabled[id] == lease {
		delete(h.enabled, id)
	}
	lease.retired = true
	h.retired[lease] = id
	finish := err != nil || lease.dead || len(lease.configurationTokens) == 0 || h.closed
	h.lifecycle.Unlock()
	if finish {
		err = errors.Join(err, h.finishRetiredLease(id, lease))
	} else {
		err = errors.Join(err, lease.toolWork.Wait(context.Background()), lease.adapterWork.Wait(context.Background()))
	}
	h.lifecycle.Lock()
	close(lease.runtimeDone)
	h.lifecycle.Unlock()
	return err
}

func (h *V1Host) releaseConfiguration(id string, lease *nodeLease, token string) {
	h.lifecycle.Lock()
	delete(lease.configurationTokens, token)
	finish := lease.retired && len(lease.configurationTokens) == 0
	h.lifecycle.Unlock()
	if finish {
		_ = h.finishRetiredLease(id, lease)
	}
}
func (h *V1Host) finishRetiredLease(id string, lease *nodeLease) error {
	h.lifecycle.Lock()
	if lease.physicalFinalizing {
		h.lifecycle.Unlock()
		<-lease.done
		return lease.err
	}
	// A normal physical disable may already have completed while the operation's
	// finalizer was pending. It is never converted into a second retirement.
	if !lease.retired {
		h.lifecycle.Unlock()
		select {
		case <-lease.done:
			return lease.err
		default:
			return nil
		}
	}
	lease.physicalFinalizing = true
	lease.processDetached = true
	if h.modules[id] == lease {
		delete(h.modules, id)
	}
	last := h.attachedProcessLeases(lease.process) == 0 && h.process == lease.process
	if last {
		h.process = nil
	}
	h.lifecycle.Unlock()
	_, err := lease.process.call(context.Background(), map[string]any{"op": "terminate", "plugin": lease.key})
	if last {
		err = errors.Join(err, stopNode(lease.process))
	}
	err = errors.Join(err, lease.toolWork.Wait(context.Background()), lease.adapterWork.Wait(context.Background()))
	h.lifecycle.Lock()
	delete(h.retired, lease)
	h.completeModuleLease(lease, err)
	h.lifecycle.Unlock()
	return err
}

// All activations share one physical completion owner. Retaining an old lease
// never retains the history of other activations, and transfer does not signal exit.
func (h *V1Host) completeModuleLease(lease *nodeLease, err error) {
	lease.completion.Do(func() { lease.err = err; close(lease.done) })
}
func (h *V1Host) HasCurrentModule(id string) bool {
	h.lifecycle.Lock()
	defer h.lifecycle.Unlock()
	lease := h.modules[id]
	return !h.closed && lease != nil && lease.moduleLoaded && !lease.physicalFinalizing && !lease.processDetached && protocolProcessLive(lease) && (!lease.closing || lease.retired && (lease.managementOnly || len(lease.configurationTokens) > 0))
}

func (h *V1Host) failedActivation(ctx context.Context, id string, lease *nodeLease, preserve func() bool) {
	if preserve != nil && preserve() {
		h.abortPluginLease(id, lease)
		_ = h.retireManagedLease(context.Background(), id, lease)
	} else {
		_ = h.disableLease(ctx, id, lease)
	}
}
