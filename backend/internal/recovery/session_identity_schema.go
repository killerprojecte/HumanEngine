package recovery

// EnsureMemoryIdentitySchema materializes only the canonical registry table,
// after the live command has recorded its sealed identity intent.
func (s *WriteSession) EnsureMemoryIdentitySchema() error {
	return s.schema("CREATE TABLE IF NOT EXISTS memory_identity_registry(id INTEGER PRIMARY KEY CHECK(id=1),value TEXT NOT NULL)")
}
