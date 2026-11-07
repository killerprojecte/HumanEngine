package recovery

import "context"

// The base storage constructor owns a transaction. Temporal replacement does
// not: its lazy DDL and subsequent deletes belong to the caller's write scope.
const commitmentStorageDDL = `CREATE TABLE IF NOT EXISTS memory_commitment_heads(id TEXT PRIMARY KEY,scene_id TEXT NOT NULL,value TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS memory_commitment_revisions(id TEXT NOT NULL,version INTEGER NOT NULL,value TEXT NOT NULL,PRIMARY KEY(id,version));
CREATE TABLE IF NOT EXISTS memory_commitment_commands(key TEXT PRIMARY KEY,id TEXT NOT NULL,version INTEGER NOT NULL);
CREATE INDEX IF NOT EXISTS memory_commitment_scene ON memory_commitment_heads(scene_id);`
const commitmentTemporalDDL = `CREATE TABLE IF NOT EXISTS memory_commitment_transitions(id TEXT NOT NULL,mutation_version INTEGER NOT NULL,scene_id TEXT NOT NULL,value TEXT NOT NULL,PRIMARY KEY(id,mutation_version));
CREATE TABLE IF NOT EXISTS memory_commitment_temporal_windows(id TEXT PRIMARY KEY,scene_id TEXT NOT NULL,value TEXT NOT NULL);`

func (s *WriteSession) EnsureCommitmentStorageSchema() error {
	if e := s.AssertReady(); e != nil {
		return e
	}
	return s.WithSavepoint(func(child *WriteSession) error { return child.schema(commitmentStorageDDL) })
}
func (s *WriteSession) EnsureCommitmentTemporalSchema() error {
	if e := s.AssertReady(); e != nil {
		return e
	}
	return s.schema(commitmentTemporalDDL)
}

// EnsureCommitmentTemporalSchema uses only the guarded admitted transaction;
// marker replay may introduce forward-only temporal history on existing bases.
func (t *MaintenanceTx) EnsureCommitmentTemporalSchema(ctx context.Context) error {
	if e := t.CheckScope(ctx); e != nil {
		return e
	}
	_, e := t.ExecContext(ctx, commitmentTemporalDDL)
	return e
}

// EnsureCommitmentStorageSchema reproduces the base constructor's owned
// transaction. Callers of offline recovery validate existing material first;
// this explicit writer capability is never invoked by a reader constructor.
func (t *MaintenanceTx) EnsureCommitmentStorageSchema(ctx context.Context) error {
	if e := t.CheckScope(ctx); e != nil {
		return e
	}
	return t.Apply(ctx, 1, func(child *MaintenanceTx) error { _, e := child.ExecContext(ctx, commitmentStorageDDL); return e })
}
