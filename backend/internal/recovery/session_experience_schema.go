package recovery

// EnsureExperienceStorageSchema enrolls the source ExperienceStore tables under
// the writer's fixed-schema capability. Reads never create tables.
func (s *WriteSession) EnsureExperienceStorageSchema() error {
	if err := s.AssertReady(); err != nil {
		return err
	}
	return s.schema(`CREATE TABLE IF NOT EXISTS memory_experience_outcomes(id TEXT PRIMARY KEY,root TEXT UNIQUE NOT NULL,scene TEXT NOT NULL,source TEXT NOT NULL,value TEXT NOT NULL);
 CREATE TABLE IF NOT EXISTS memory_experience_policies(id TEXT PRIMARY KEY,scene TEXT NOT NULL,version INTEGER NOT NULL,value TEXT NOT NULL);
 CREATE TABLE IF NOT EXISTS memory_experience_validations(id TEXT PRIMARY KEY,policy TEXT NOT NULL,value TEXT NOT NULL);
 CREATE TABLE IF NOT EXISTS memory_experience_uses(root TEXT PRIMARY KEY,scene TEXT NOT NULL,policy TEXT NOT NULL,target TEXT,value TEXT NOT NULL);
 CREATE TABLE IF NOT EXISTS memory_experience_tombstones(key TEXT PRIMARY KEY);
 CREATE INDEX IF NOT EXISTS memory_experience_outcome_source ON memory_experience_outcomes(scene,source);
 CREATE INDEX IF NOT EXISTS memory_experience_policy_scene ON memory_experience_policies(scene);`)
}
func (s *WriteSession) EnsureExperienceTrialSchema() error {
	if err := s.AssertReady(); err != nil {
		return err
	}
	return s.schema(`CREATE TABLE IF NOT EXISTS memory_experience_trials(policy TEXT PRIMARY KEY,scene TEXT NOT NULL,version INTEGER NOT NULL,value TEXT)`)
}
