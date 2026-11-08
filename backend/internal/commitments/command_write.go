package commitments

import (
	"errors"
	"github.com/killerprojecte/HumanEngine/backend/internal/recovery"
)

// BindCommandSession prevents silently moving a bound store to a newer child.
func (s *Store) BindCommandSession(session *recovery.WriteSession) (*Store, error) {
	if s.bindingError != nil {
		return nil, s.bindingError
	}
	if s.maintenance != nil {
		return nil, errors.New("commitment_live_session_required")
	}
	if s.session != nil && s.session != session {
		return nil, errors.New("commitment_store_session_binding")
	}
	b := s.WithSession(session)
	if e := b.requireTransaction(); e != nil {
		return nil, e
	}
	return b, nil
}

// write is the normal command path: unlike privacy maintenance it records real
// active/latest projected states, then journals before touching main storage.
func (s *CommandService) write(kind string, head, revision *Object) error {
	if e := s.read.check(); e != nil {
		return e
	}
	if e := s.store.ready(); e != nil {
		return e
	}
	id := str(get(head, "id"))
	prior, e := s.store.reader().Revisions(id)
	if e != nil {
		return e
	}
	revisions := []any{}
	for _, r := range prior {
		if get(get(r, "ref"), "version") != get(get(revision, "ref"), "version") {
			revisions = append(revisions, r)
		}
	}
	revisions = append(revisions, revision)
	old, e := s.store.reader().Timeline(id)
	if e != nil {
		return e
	}
	first := get(head, "activeVersion")
	if first == nil {
		first = get(head, "latestVersion")
	}
	versions := []any{first}
	if first != get(head, "latestVersion") {
		versions = append(versions, get(head, "latestVersion"))
	}
	states := []any{}
	for _, version := range versions {
		var r *Object
		for _, v := range revisions {
			if get(get(v, "ref"), "version") == version {
				r = object(v)
				break
			}
		}
		item, e := ProjectCommitment(Context{SceneID: str(get(head, "sceneId"))}, head, r, s.read.current)
		if e != nil {
			return e
		}
		state := new(Object)
		for _, m := range item.Members() {
			if m.Key != "terms" && m.Key != "recordedAt" {
				_ = state.Set(m.Key, m.Value)
			}
		}
		if has(item, "terms") {
			hash, e := TemporalHash(get(item, "terms"))
			if e != nil {
				return e
			}
			_ = state.Set("termsHash", hash)
		}
		if has(item, "recordedAt") {
			_ = state.Set("termsRecordedAt", get(item, "recordedAt"))
		}
		states = append(states, state)
	}
	row := obj("schemaVersion", float64(1), "id", get(head, "id"), "sceneId", get(head, "sceneId"), "mutationVersion", get(head, "mutationVersion"), "recordedAt", s.store.now(), "reason", kind, "head", clone(head), "states", states)
	timeline, e := AppendTimeline(old, row, s.store.options.DateParser)
	if e != nil {
		return e
	}
	timeline, e = NormalizeTimeline(timeline)
	if e != nil {
		return e
	}
	hash, e := TemporalQualification(head, revisions, timeline, s.store.options.Compare)
	if e != nil {
		return e
	}
	// Reuse only the typed journal mechanism, never PrivacyService.write.
	journal := PrivacyService{Store: s.store}
	if e = journal.record(kind, head, hash, true, nil); e != nil {
		return e
	}
	if e = s.store.Put(head, revision); e != nil {
		return e
	}
	if e = s.store.ReplaceTimeline(id, timeline); e != nil {
		return e
	}
	e = s.owners.AfterWrite(s.store, clone(head).(*Object), clone(revisions).([]any), clone(prior).([]any))
	return errors.Join(e, s.store.requireTransaction(), s.read.check())
}
