package botruntime

import "errors"

var ErrAdapterIDInUse = errors.New("adapter_id_in_use")

// AdapterOwner identifies one installation origin. It is allocated by the host,
// not supplied by a plugin or derived from a package/manifest identifier.
type AdapterOwner struct{ runtime *Runtime }

func (r *Runtime) NewAdapterOwner() *AdapterOwner { return &AdapterOwner{runtime: r} }

// AdapterRegistration owns the actual dispatch ID, including the gap between
// withdrawing dispatch and draining transport IO. A stale generation cannot
// publish over, detach, or release its replacement.
type AdapterRegistration struct {
	owner     *AdapterOwner
	id        string
	value     *Adapter // protected by the registry mutex
	withdrawn bool
}

// Claim reserves an ID without making it dispatchable. An external adapter
// claims before starting physical IO and releases only after it has drained.
func (o *AdapterOwner) Claim(id string) (*AdapterRegistration, error) {
	return o.claim(id, false)
}

func (o *AdapterOwner) claim(id string, replace bool) (*AdapterRegistration, error) {
	r := o.runtime
	m := &r.adapters
	m.mu.Lock()
	if r.closed.Load() {
		m.mu.Unlock()
		return nil, code("runtime_closed")
	}
	prior := m.claims[id]
	if prior != nil && (!replace || prior.owner != o || prior.value == nil) {
		m.mu.Unlock()
		return nil, ErrAdapterIDInUse
	}
	next := &AdapterRegistration{owner: o, id: id}
	if m.claims == nil {
		m.claims = make(map[string]*AdapterRegistration)
	}
	m.claims[id] = next
	var old *Adapter
	if prior != nil {
		old, prior.value = prior.value, nil
		m.storeLocked(id, nil)
	}
	m.mu.Unlock()
	// Legacy same-owner replacement keeps its existing insertion position.
	// Between generations the entry is withdrawn from dispatch and snapshots.
	if old != nil {
		if r.config().TopicAdmissionEnabled {
			r.cancelAdapter(id)
		}
		disposeAdapter(old)
	}
	return next, nil
}

// Install is the synchronous bridge for established same-origin replacement
// semantics. New external packages use Claim, Publish, Detach, then Release.
func (o *AdapterOwner) Install(id string, value *Adapter) (*AdapterRegistration, error) {
	next, err := o.claim(id, true)
	if err != nil {
		return nil, err
	}
	if err := next.Publish(value); err != nil {
		next.Release()
		return nil, err
	}
	return next, nil
}

func (a *AdapterRegistration) Publish(value *Adapter) error {
	m := &a.owner.runtime.adapters
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.claims[a.id] != a || a.withdrawn || a.value != nil || a.owner.runtime.closed.Load() {
		return code("adapter_registration_not_current")
	}
	a.value = value
	m.storeLocked(a.id, value)
	return nil
}

// Detach withdraws routing and cancels local scene work, but leaves the
// registration occupied while its owner finishes physical shutdown.
func (a *AdapterRegistration) Detach() {
	r := a.owner.runtime
	m := &r.adapters
	m.mu.Lock()
	var old *Adapter
	if m.claims[a.id] == a {
		old, a.value = a.value, nil
		a.withdrawn = true
		m.deleteLocked(a.id)
	}
	m.mu.Unlock()
	if old != nil {
		r.cancelAdapter(a.id)
		disposeAdapter(old)
	}
}

// Release is called only after Detach and the owning transport has drained.
func (a *AdapterRegistration) Release() {
	m := &a.owner.runtime.adapters
	m.mu.Lock()
	if m.claims[a.id] == a && a.value == nil {
		m.deleteLocked(a.id) // also removes an unpublished replacement slot
		delete(m.claims, a.id)
	}
	m.mu.Unlock()
}

func disposeAdapter(value *Adapter) {
	if value.Dispose != nil {
		value.Dispose()
	}
}

func (r *Runtime) closeAdapters() {
	m := &r.adapters
	m.mu.Lock()
	entries := m.entries
	m.entries, m.claims = nil, nil
	m.mu.Unlock()
	for _, entry := range entries {
		if entry.value != nil {
			disposeAdapter(entry.value)
		}
	}
}
