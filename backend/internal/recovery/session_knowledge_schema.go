package recovery

const knowledgeStorageDDL = `CREATE TABLE IF NOT EXISTS memory_knowledge_edges(id TEXT PRIMARY KEY,scene_id TEXT NOT NULL,assertion_id TEXT NOT NULL,admission_key TEXT NOT NULL UNIQUE,value TEXT NOT NULL);
CREATE INDEX IF NOT EXISTS memory_knowledge_scene ON memory_knowledge_edges(scene_id);
CREATE INDEX IF NOT EXISTS memory_knowledge_assertion ON memory_knowledge_edges(assertion_id);`

// EnsureKnowledgeStorageSchema enrolls only the source-owned optional schema.
// No caller callback or caller SQL runs with schema privileges.
func (s *WriteSession) EnsureKnowledgeStorageSchema() error {
	if e := s.AssertReady(); e != nil {
		return e
	}
	return s.WithSavepoint(func(child *WriteSession) error { return child.schema(knowledgeStorageDDL) })
}
