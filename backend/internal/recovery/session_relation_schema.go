package recovery

const relationStorageDDL = `
 CREATE TABLE IF NOT EXISTS memory_relation_entities(id TEXT PRIMARY KEY,scene_id TEXT NOT NULL,assertion_id TEXT NOT NULL,value TEXT NOT NULL);
 CREATE INDEX IF NOT EXISTS memory_relation_entities_assertion ON memory_relation_entities(assertion_id);
 CREATE TABLE IF NOT EXISTS memory_relation_candidates(id TEXT PRIMARY KEY,scene_id TEXT NOT NULL,assertion_id TEXT NOT NULL,target_id TEXT NOT NULL,admission_key TEXT NOT NULL UNIQUE,version INTEGER NOT NULL,value TEXT NOT NULL);
 CREATE INDEX IF NOT EXISTS memory_relation_candidates_scene ON memory_relation_candidates(scene_id);
 CREATE INDEX IF NOT EXISTS memory_relation_candidates_assertion ON memory_relation_candidates(assertion_id);
 CREATE INDEX IF NOT EXISTS memory_relation_candidates_target ON memory_relation_candidates(target_id);
 CREATE TABLE IF NOT EXISTS memory_relation_tombstones(id TEXT PRIMARY KEY,version INTEGER NOT NULL);
 `

// EnsureRelationStorageSchema owns the source constructor transaction.
func (s *WriteSession) EnsureRelationStorageSchema() error {
	if e := s.AssertReady(); e != nil {
		return e
	}
	return s.WithSavepoint(func(child *WriteSession) error { return child.schema(relationStorageDDL) })
}
