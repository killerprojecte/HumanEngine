package recovery

const embeddingRebuildStorageDDL = `CREATE TABLE IF NOT EXISTS embedding_rebuild_items (
      job_id TEXT NOT NULL, memory_id TEXT NOT NULL REFERENCES memories(id) ON DELETE CASCADE,
      content_hash TEXT NOT NULL, PRIMARY KEY(job_id,memory_id)
    )`

// EnsureEmbeddingRebuildSchema owns the source's derived checkpoint table.
func (s *WriteSession) EnsureEmbeddingRebuildSchema() error {
	if err := s.AssertReady(); err != nil {
		return err
	}
	return s.WithSavepoint(func(child *WriteSession) error { return child.schema(embeddingRebuildStorageDDL) })
}
