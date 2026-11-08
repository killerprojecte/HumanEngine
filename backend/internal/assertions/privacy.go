package assertions

import (
	"errors"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/recovery"
)

// Privacy intent and SQL stay on the exact live assertion capability. Durable
// revocations survive rollback; no independent writer or post-loss fallback.
func (s *Store) RecallSource(scene, message string) error {
	if e := s.requireTransaction(); e != nil {
		return e
	}
	if _, e := s.appendIntent(recovery.Source{SceneID: scene, MessageID: message}); e != nil {
		return e
	}
	return s.exec("INSERT OR IGNORE INTO recall_tombstones VALUES(?,?,?)", scene, message, s.now())
}
func (s *Store) RevokeTerminalAssertion(id string) error {
	if e := s.requireTransaction(); e != nil {
		return e
	}
	state, e := s.reader().State(id)
	if e != nil {
		return e
	}
	_, e = s.appendIntent(recovery.Assertion{ID: id, ThroughVersion: state.Version, Terminal: true})
	return e
}
func (s *Store) DeletePrivacyFingerprints(record *Object) error {
	fp, e := Fingerprint(record)
	if e != nil {
		return e
	}
	emit := func(v, at string) error {
		if _, e := s.appendIntent(recovery.Fingerprint{Value: v}); e != nil {
			return e
		}
		return s.exec("INSERT OR REPLACE INTO memory_deleted VALUES(?,?)", v, at)
	}
	if e = s.requireTransaction(); e != nil {
		return e
	}
	if e = emit(fp, s.now()); e != nil {
		return e
	}
	if get(record, "origin") == "learned" && get(get(record, "scope"), "kind") == "sharedPublicKnowledge" {
		at := s.now()
		seen := map[string]bool{}
		for _, src := range array(get(record, "sources")) {
			scene := str(get(src, "sceneId"))
			if seen[scene] {
				continue
			}
			seen[scene] = true
			copy := record.Clone()
			_ = copy.Set("scope", obj("kind", "scene", "sceneId", scene))
			fp, e := Fingerprint(copy)
			if e != nil {
				return e
			}
			if e = emit(fp, at); e != nil {
				return e
			}
		}
	}
	return nil
}
func (s *Store) RemovePrivacyRecord(id string) error {
	return s.exec("DELETE FROM memories WHERE id=?", id)
}
func (r *Reader) SourceRecords(scene, message string) (out []*Object, err error) {
	if err = r.ready(); err != nil {
		return
	}
	rows, err := r.query("SELECT m.value FROM memories m JOIN memory_sources s ON s.memory_id=m.id WHERE s.scene_id=? AND s.message_id=?", scene, message)
	if err != nil {
		return
	}
	defer func() { err = errors.Join(err, rows.Close()) }()
	out = []*Object{}
	for rows.Next() {
		var raw string
		if err = rows.Scan(&raw); err != nil {
			return
		}
		v, e := jsjson.Parse(raw)
		if e != nil {
			return nil, e
		}
		o, ok := v.(*Object)
		if !ok {
			return nil, errors.New("assertion_record_required")
		}
		out = append(out, o)
	}
	err = rows.Err()
	return
}
