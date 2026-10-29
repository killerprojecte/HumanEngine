import type Database from 'better-sqlite3'

type MemoryMigrationPort = { db: Pick<Database.Database, 'exec' | 'transaction' | 'prepare'> }

/** Preserve legacy IDs, proof alternatives and unverified provenance. */
export function initializeMemoryStorage(store: MemoryMigrationPort): void {
  store.db.exec(`
      CREATE TABLE IF NOT EXISTS memories (
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
      );
    `)
  // Atomic, idempotent migration: legacy content is retained but never silently
  // asserted to have independent provenance. No historical messages are replayed.
  store.db.transaction(() => {
    store.db.exec(`CREATE TABLE IF NOT EXISTS memory_supports (
        memory_id TEXT PRIMARY KEY REFERENCES memories(id) ON DELETE CASCADE,
        value TEXT NOT NULL
      );`)
    store.db
      .prepare(
        `UPDATE memories SET value=json_set(value,'$.lineageStatus','unverified')
        WHERE json_extract(value,'$.origin')='learned'
          AND json_extract(value,'$.lineageStatus') IS NULL`,
      )
      .run()
  })()
}
