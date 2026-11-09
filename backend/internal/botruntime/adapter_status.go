package botruntime

import (
	"context"
	"sync"
)

type adapterEntry struct {
	id    string
	value *Adapter
}

// adapterRegistry preserves JavaScript Map insertion order. Its lock protects
// only registration bookkeeping; neither disposal nor status getters run here.
type adapterRegistry struct {
	mu      sync.Mutex
	entries []adapterEntry
}

func (m *adapterRegistry) Load(key any) (any, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, entry := range m.entries {
		if entry.id == key {
			return entry.value, true
		}
	}
	return nil, false
}
func (m *adapterRegistry) Store(key, value any) {
	m.mu.Lock()
	defer m.mu.Unlock()
	id, adapter := key.(string), value.(*Adapter)
	for i := range m.entries {
		if m.entries[i].id == id {
			m.entries[i].value = adapter
			return
		}
	}
	m.entries = append(m.entries, adapterEntry{id, adapter})
}
func (m *adapterRegistry) LoadAndDelete(key any) (any, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i, entry := range m.entries {
		if entry.id == key {
			copy(m.entries[i:], m.entries[i+1:])
			m.entries[len(m.entries)-1] = adapterEntry{}
			m.entries = m.entries[:len(m.entries)-1]
			return entry.value, true
		}
	}
	return nil, false
}
func (m *adapterRegistry) Delete(key any) { m.LoadAndDelete(key) }
func (m *adapterRegistry) snapshot() []adapterEntry {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]adapterEntry(nil), m.entries...)
}
func (m *adapterRegistry) Range(f func(any, any) bool) {
	for _, entry := range m.snapshot() {
		if !f(entry.id, entry.value) {
			return
		}
	}
}

type AdapterStatus struct {
	ID     string `json:"id"`
	Status string `json:"status"`
}

// AdapterStatuses takes one ordered object snapshot, then evaluates getters
// sequentially outside all registry locks. Source checks the first read but
// returns a second, unnormalized read when the first value is recognized.
func (r *Runtime) AdapterStatuses(ctx context.Context) ([]AdapterStatus, error) {
	entries := r.adapters.snapshot()
	result := make([]AdapterStatus, 0, len(entries))
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		status := "unknown"
		if getter := entry.value.Status; getter != nil {
			first, err := getter(ctx)
			if err != nil {
				return nil, err
			}
			switch first {
			case "connecting", "connected", "disconnected", "disposed":
				status, err = getter(ctx)
				if err != nil {
					return nil, err
				}
			}
		}
		result = append(result, AdapterStatus{entry.id, status})
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return result, nil
}
