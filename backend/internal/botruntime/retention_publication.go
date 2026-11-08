package botruntime

import "sync"

// sceneOwnerMutex reconciles reader-published retention before a later actor
// operation observes mutable indexes. Readers never acquire this mutex.
// before performs only in-memory bookkeeping; no SQL or extension callbacks.
type sceneOwnerMutex struct {
	sync.Mutex
	before func()
}

func (m *sceneOwnerMutex) Lock() {
	m.Mutex.Lock()
	if m.before != nil {
		m.before()
	}
}
func (m *sceneOwnerMutex) TryLock() bool {
	if !m.Mutex.TryLock() {
		return false
	}
	if m.before != nil {
		m.before()
	}
	return true
}

// trimPublished is the reader-side, irreversible source trim linearization.
// publicationMu covers only immutable-reference filtering and publication. It
// never covers display/SQL, model or transport work, and never takes scene.mu.
func (r *Runtime) trimPublished(s *sceneState) *sceneView {
	s.publicationMu.Lock()
	defer s.publicationMu.Unlock()
	v := s.view.Load()
	if v.scene.Kind == "simulation" {
		return v
	}
	cfg := r.config()
	clear := !cfg.CacheEnabled && v.active == 0 && v.timers == 0
	cutoff := r.services.Clock.Now() - r.lifetime(cfg)
	records := bodyRecords(v.history)
	retained := make([]*messageRecord, 0, len(records))
	for _, record := range records {
		if !record.retention.discarded.Load() && !clear && record.receivedAt >= cutoff {
			retained = append(retained, record)
		} else {
			record.retention.discarded.Store(true)
		}
	}
	if len(retained) > 500 {
		for _, record := range retained[:len(retained)-500] {
			record.retention.discarded.Store(true)
		}
		retained = retained[len(retained)-500:]
	}
	if len(retained) == len(records) {
		return v
	}
	next := *v
	next.history = nil
	for _, record := range retained {
		next.history = appendRecord(next.history, record)
	}
	s.retentionDirty.Store(true)
	s.view.Store(&next)
	return &next
}
func (r *Runtime) reconcilePublishedRetentionLocked(s *sceneState) {
	if !s.retentionDirty.Load() {
		return
	}
	s.publicationMu.Lock()
	defer s.publicationMu.Unlock()
	r.reconcilePublishedRetentionUnderPublication(s)
}
func (r *Runtime) reconcilePublishedRetentionUnderPublication(s *sceneState) {
	if !s.retentionDirty.Swap(false) {
		return
	}
	rows := make([]*Object, 0, len(s.messages))
	changed := false
	for _, body := range s.messages {
		record := s.byBody[body]
		if record.retention.discarded.Load() {
			changed = true
			continue
		}
		rows = append(rows, body)
	}
	if !changed {
		return
	}
	s.messages = rows
	r.rebuildMessagesLocked(s)
	s.historyDirty = true
	// Match body-dependent routing cleanup without deleting independent protected
	// topic history. The usual owner trim continues to own historical pruning.
	for _, entry := range s.routing.Metadata.Entries() {
		if entry.Value.CueSourceID != nil && !s.available[*entry.Value.CueSourceID] {
			entry.Value.Cue = nil
			entry.Value.CueSourceID = nil
		}
	}
	for _, entry := range s.routing.Acknowledged.Entries() {
		if !s.available[entry.Key] {
			s.routing.Acknowledged.Delete(entry.Key)
		}
	}
}
