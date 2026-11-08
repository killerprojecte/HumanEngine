package recovery

// EnsureHabitStorageSchema enrolls only the two source HabitService tables.
// It is a fixed writer capability, never caller SQL or a read-side migration.
func (s *WriteSession) EnsureHabitStorageSchema() error {
	if err := s.AssertReady(); err != nil {
		return err
	}
	return s.schema(`CREATE TABLE IF NOT EXISTS habits (id TEXT PRIMARY KEY, fingerprint TEXT UNIQUE NOT NULL, value TEXT NOT NULL); CREATE TABLE IF NOT EXISTS habit_deleted (fingerprint TEXT PRIMARY KEY);`)
}
