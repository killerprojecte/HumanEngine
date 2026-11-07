package recovery

// These statements are the fixed schema from memory/storage/assertion-history.ts.
// Keeping them here allows domain initialization without giving caller SQL DDL
// or transaction-control authority. Read-only history construction never calls
// this capability.
const assertionHistoryDDL = `CREATE TABLE IF NOT EXISTS memory_assertion_state (
 assertion_id TEXT PRIMARY KEY, version INTEGER NOT NULL,
 deletion_epoch INTEGER NOT NULL, deleted INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS memory_assertion_revisions (
 assertion_id TEXT NOT NULL REFERENCES memory_assertion_state(assertion_id),
 version INTEGER NOT NULL, scope_key TEXT NOT NULL, recorded_at TEXT,
 idempotency_key TEXT, payload_hash TEXT, value TEXT,
 PRIMARY KEY(assertion_id,version), UNIQUE(assertion_id,idempotency_key)
);
CREATE TABLE IF NOT EXISTS memory_assertion_revision_sources (
 assertion_id TEXT NOT NULL, version INTEGER NOT NULL,
 scene_id TEXT NOT NULL, message_id TEXT NOT NULL,
 PRIMARY KEY(assertion_id,version,scene_id,message_id),
 FOREIGN KEY(assertion_id,version) REFERENCES memory_assertion_revisions(assertion_id,version) ON DELETE CASCADE
);
CREATE TABLE IF NOT EXISTS memory_assertion_revision_parents (
 assertion_id TEXT NOT NULL, version INTEGER NOT NULL, parent_id TEXT NOT NULL,
 PRIMARY KEY(assertion_id,version,parent_id),
 FOREIGN KEY(assertion_id,version) REFERENCES memory_assertion_revisions(assertion_id,version) ON DELETE CASCADE
);
CREATE INDEX IF NOT EXISTS memory_assertion_revision_parent_lookup
 ON memory_assertion_revision_parents(parent_id,assertion_id);
CREATE INDEX IF NOT EXISTS memory_assertion_revision_source_lookup
 ON memory_assertion_revision_sources(scene_id,message_id);
CREATE INDEX IF NOT EXISTS memory_assertion_revision_scope
 ON memory_assertion_revisions(scope_key,assertion_id);`

const assertionParentBackfill = `INSERT OR IGNORE INTO memory_assertion_revision_parents(assertion_id,version,parent_id)
 SELECT revision.assertion_id,revision.version,parent.value
 FROM memory_assertion_revisions AS revision,
 json_each(revision.value,'$.supports') AS support,
 json_each(support.value,'$.parents') AS parent
 WHERE revision.value IS NOT NULL AND parent.type='text'`

// EnsureAssertionHistorySchema initializes or migrates history atomically in
// the caller's transaction. Parent backfill runs only if the parent table was
// absent before this call, matching the source constructor's migration marker.
func (s *WriteSession) EnsureAssertionHistorySchema() error {
	if err := s.AssertReady(); err != nil {
		return err
	}
	return s.WithSavepoint(func(child *WriteSession) error {
		child.mu.Lock()
		defer child.mu.Unlock()
		if err := child.check(); err != nil {
			return err
		}
		var exists bool
		err := child.conn.QueryRowContext(child.ctx, "SELECT EXISTS(SELECT 1 FROM sqlite_master WHERE type='table' AND name='memory_assertion_revision_parents')").Scan(&exists)
		if err != nil {
			return child.after(err)
		}
		if err = child.schemaLocked(assertionHistoryDDL); err != nil || exists {
			return err
		}
		_, err = child.conn.ExecContext(child.ctx, assertionParentBackfill)
		return child.after(err)
	})
}

const memoryStorageDDL = `CREATE TABLE IF NOT EXISTS memories (
 id TEXT PRIMARY KEY, scope_key TEXT NOT NULL, person_id TEXT,
 content TEXT NOT NULL, fingerprint TEXT NOT NULL UNIQUE, value TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS memories_scope ON memories(scope_key,person_id);
CREATE TABLE IF NOT EXISTS memory_sources (
 memory_id TEXT NOT NULL REFERENCES memories(id) ON DELETE CASCADE,
 scene_id TEXT NOT NULL, message_id TEXT NOT NULL, sender_id TEXT NOT NULL,
 PRIMARY KEY(memory_id,scene_id,message_id)
);
CREATE INDEX IF NOT EXISTS memory_source_lookup ON memory_sources(scene_id,message_id);
CREATE TABLE IF NOT EXISTS memory_deleted (fingerprint TEXT PRIMARY KEY, deleted_at TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS vector_spaces (id TEXT PRIMARY KEY, value TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS memory_vectors (
 memory_id TEXT NOT NULL REFERENCES memories(id) ON DELETE CASCADE,
 space_id TEXT NOT NULL REFERENCES vector_spaces(id) ON DELETE CASCADE,
 embedding BLOB NOT NULL, PRIMARY KEY(memory_id,space_id)
);`

const memorySupportsDDL = `CREATE TABLE IF NOT EXISTS memory_supports (
 memory_id TEXT PRIMARY KEY REFERENCES memories(id) ON DELETE CASCADE,
 value TEXT NOT NULL
);`

// EnsureMemoryStorageSchema preserves initializeMemoryStorage's boundary: base
// tables are initialized first, then supports and the learned-lineage migration
// are savepoint-atomic. Catching a migration error retains the base tables in the
// enclosing transaction; rolling back the outer session removes all its writes.
// This is an owned live-transaction capability, not an autocommit initializer.
func (s *WriteSession) EnsureMemoryStorageSchema() error {
	if err := s.AssertReady(); err != nil {
		return err
	}
	if err := s.schema(memoryStorageDDL); err != nil {
		return err
	}
	return s.WithSavepoint(func(child *WriteSession) error {
		if err := child.schema(memorySupportsDDL); err != nil {
			return err
		}
		_, err := child.Exec(`UPDATE memories SET value=json_set(value,'$.lineageStatus','unverified')
 WHERE json_extract(value,'$.origin')='learned'
 AND json_extract(value,'$.lineageStatus') IS NULL`)
		return err
	})
}
