package recovery

// EnsureManualCommandSchema grants only the fixed source metadata schema, not
// arbitrary DDL authority to command callers.
func (s *WriteSession) EnsureManualCommandSchema() error {
	return s.schema(`CREATE TABLE IF NOT EXISTS memory_command_receipts (command_key TEXT PRIMARY KEY, assertion_id TEXT NOT NULL, payload_hash TEXT NOT NULL, deletion_epoch INTEGER NOT NULL, result_version INTEGER NOT NULL, created_at TEXT NOT NULL)`)
}
