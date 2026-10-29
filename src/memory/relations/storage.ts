import type Database from 'better-sqlite3'
import type { RelationEntity, RelationRecord } from '../domain/relations.ts'
export type RelationDatabase = Pick<
  Database.Database,
  'exec' | 'prepare' | 'transaction' | 'inTransaction'
>
/** Transaction-local persistence; no lookup here grants authority. */
export class RelationStorage {
  constructor(readonly db: RelationDatabase) {
    db.transaction(() =>
      db.exec(`
 CREATE TABLE IF NOT EXISTS memory_relation_entities(id TEXT PRIMARY KEY,scene_id TEXT NOT NULL,assertion_id TEXT NOT NULL,value TEXT NOT NULL);
 CREATE INDEX IF NOT EXISTS memory_relation_entities_assertion ON memory_relation_entities(assertion_id);
 CREATE TABLE IF NOT EXISTS memory_relation_candidates(id TEXT PRIMARY KEY,scene_id TEXT NOT NULL,assertion_id TEXT NOT NULL,target_id TEXT NOT NULL,admission_key TEXT NOT NULL UNIQUE,version INTEGER NOT NULL,value TEXT NOT NULL);
 CREATE INDEX IF NOT EXISTS memory_relation_candidates_scene ON memory_relation_candidates(scene_id);
 CREATE INDEX IF NOT EXISTS memory_relation_candidates_assertion ON memory_relation_candidates(assertion_id);
 CREATE INDEX IF NOT EXISTS memory_relation_candidates_target ON memory_relation_candidates(target_id);
 CREATE TABLE IF NOT EXISTS memory_relation_tombstones(id TEXT PRIMARY KEY,version INTEGER NOT NULL);
 `),
    )()
  }
  requireTransaction() {
    if (!this.db.inTransaction) throw new Error('relation_transaction_required')
  }
  entity(id: string): RelationEntity | undefined {
    const row = this.db.prepare('SELECT value FROM memory_relation_entities WHERE id=?').get(id) as
      { value: string } | undefined
    return row ? JSON.parse(row.value) : undefined
  }
  get(id: string): RelationRecord | undefined {
    const row = this.db
      .prepare('SELECT value FROM memory_relation_candidates WHERE id=?')
      .get(id) as { value: string } | undefined
    return row ? JSON.parse(row.value) : undefined
  }
  byKey(key: string): RelationRecord | undefined {
    const row = this.db
      .prepare('SELECT value FROM memory_relation_candidates WHERE admission_key=?')
      .get(key) as { value: string } | undefined
    return row ? JSON.parse(row.value) : undefined
  }
  put(record: RelationRecord, key: string, entity?: RelationEntity) {
    this.requireTransaction()
    if (this.db.prepare('SELECT 1 FROM memory_relation_tombstones WHERE id=?').get(record.id))
      throw new Error('relation_deleted')
    if (entity)
      this.db
        .prepare('INSERT INTO memory_relation_entities VALUES(?,?,?,?)')
        .run(entity.id, entity.sceneId, entity.backing.id, JSON.stringify(entity))
    this.db
      .prepare('INSERT INTO memory_relation_candidates VALUES(?,?,?,?,?,?,?)')
      .run(
        record.id,
        record.sceneId,
        record.backing.id,
        record.target.id,
        key,
        record.version,
        JSON.stringify(record),
      )
  }
  update(record: RelationRecord, expectedVersion: number) {
    this.requireTransaction()
    const result = this.db
      .prepare('UPDATE memory_relation_candidates SET version=?,value=? WHERE id=? AND version=?')
      .run(record.version, JSON.stringify(record), record.id, expectedVersion)
    if (result.changes !== 1) throw new Error('relation_version_conflict')
  }
  rows(sceneId: string, limit = 201): RelationRecord[] {
    return (
      this.db
        .prepare(
          'SELECT value FROM memory_relation_candidates WHERE scene_id=? ORDER BY id LIMIT ?',
        )
        .all(sceneId, limit) as { value: string }[]
    ).map((row) => JSON.parse(row.value))
  }
  purgeAssertion(id: string): string[] {
    this.requireTransaction()
    const rows = this.db
      .prepare(
        `SELECT id,version FROM memory_relation_candidates WHERE assertion_id=? OR target_id IN(SELECT id FROM memory_relation_entities WHERE assertion_id=?)`,
      )
      .all(id, id) as { id: string; version: number }[]
    for (const row of rows) {
      this.db
        .prepare(
          'INSERT INTO memory_relation_tombstones VALUES(?,?) ON CONFLICT(id) DO UPDATE SET version=MAX(version,excluded.version)',
        )
        .run(row.id, row.version + 1)
      this.db.prepare('DELETE FROM memory_relation_candidates WHERE id=?').run(row.id)
    }
    this.db.prepare('DELETE FROM memory_relation_entities WHERE assertion_id=?').run(id)
    return rows.map((row) => row.id)
  }
}
