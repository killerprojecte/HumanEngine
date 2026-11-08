package recovery

const semanticCandidatesDDL = `CREATE TABLE IF NOT EXISTS memory_semantic_candidates(id TEXT PRIMARY KEY,scene_id TEXT NOT NULL,input_key TEXT NOT NULL UNIQUE,version INTEGER NOT NULL,value TEXT); CREATE INDEX IF NOT EXISTS memory_semantic_scene ON memory_semantic_candidates(scene_id)`
const semanticAttemptsDDL = `CREATE TABLE IF NOT EXISTS memory_semantic_attempts(scene_id TEXT NOT NULL,input_key TEXT PRIMARY KEY,value TEXT NOT NULL)`

// These fixed capabilities enroll only the source-owned optional table. They
// do not run caller SQL, open another connection, or initialize on reads.
func (s *WriteSession) EnsureSemanticCandidatesSchema() error {
	if e := s.AssertReady(); e != nil {
		return e
	}
	return s.schema(semanticCandidatesDDL)
}
func (s *WriteSession) EnsureSemanticAttemptsSchema() error {
	if e := s.AssertReady(); e != nil {
		return e
	}
	return s.schema(semanticAttemptsDDL)
}
