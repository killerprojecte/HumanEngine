package pluginmanager

import "github.com/killerprojecte/HumanEngine/backend/internal/plugins"

// This observer revokes native registrations only. Host itself owns physical
// unload; re-entering it from the reader's exit notification would self-wait.
func (m *Manager) runtimeStopped(stop plugins.RuntimeStop) {
	m.mu.Lock()
	i := m.items[stop.ID]
	if i == nil || i.hostGeneration != stop.Generation || stop.Generation == 0 {
		m.mu.Unlock()
		return
	}
	i.hostGeneration = 0
	i.enabled = false
	i.cancel()
	cleanups := i.cleanups
	i.cleanups = nil
	i.pages = []any{}
	i.actions = map[string]string{}
	m.cache.InvalidatePlugin(stop.ID)
	m.mu.Unlock()
	for n := len(cleanups) - 1; n >= 0; n-- {
		cleanups[n]()
	}
}
func (m *Manager) reconcileRuntime(id string, i *item) {
	m.mu.Lock()
	generation, enabled := i.hostGeneration, i.enabled
	m.mu.Unlock()
	if !enabled || generation == 0 {
		return
	}
	current, live := m.host.RuntimeGeneration(id)
	if !live || current != generation {
		m.runtimeStopped(plugins.RuntimeStop{ID: id, Generation: generation})
	}
}

// RegisteredIDs snapshots registration order without invoking plugin hooks.
func (m *Manager) RegisteredIDs() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]string(nil), m.order...)
}
