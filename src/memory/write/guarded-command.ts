import { createHash } from 'node:crypto'
import type Database from 'better-sqlite3'
import type { AssertionHistoryState } from '../storage/assertion-history.ts'

export interface MemoryCommandGuard {
  expectedVersion: number
  expectedDeletionEpoch: number
  idempotencyKey: string
}

/** Metadata-only command receipts. Never retain deleted content in the retry log. */
export class GuardedMemoryCommands {
  constructor(private readonly db: Database.Database) {
    db.exec(`CREATE TABLE IF NOT EXISTS memory_command_receipts (
      command_key TEXT PRIMARY KEY, assertion_id TEXT NOT NULL,
      payload_hash TEXT NOT NULL, deletion_epoch INTEGER NOT NULL,
      result_version INTEGER NOT NULL, created_at TEXT NOT NULL
    )`)
  }

  run<T>(
    id: string,
    payload: unknown,
    guard: MemoryCommandGuard,
    state: () => AssertionHistoryState,
    apply: () => T,
    replay: () => T,
    checkpoint: () => void,
  ): T {
    if (!this.db.inTransaction) throw new Error('memory_command_requires_transaction')
    if (
      !guard.idempotencyKey ||
      guard.idempotencyKey.length > 160 ||
      !Number.isSafeInteger(guard.expectedVersion) ||
      guard.expectedVersion < 0 ||
      !Number.isSafeInteger(guard.expectedDeletionEpoch) ||
      guard.expectedDeletionEpoch < 0
    )
      throw new Error('invalid_memory_command')
    const hash = createHash('sha256')
      .update(JSON.stringify([id, payload, guard.expectedVersion, guard.expectedDeletionEpoch]))
      .digest('hex')
    const existing = this.db
      .prepare('SELECT * FROM memory_command_receipts WHERE command_key=?')
      .get(guard.idempotencyKey) as
      | {
          assertion_id: string
          payload_hash: string
          deletion_epoch: number
          result_version: number
        }
      | undefined
    const before = state()
    if (existing) {
      if (existing.assertion_id !== id || existing.payload_hash !== hash)
        throw new Error('memory_command_conflict')
      if (
        before.deleted ||
        before.deletionEpoch !== existing.deletion_epoch ||
        before.version !== existing.result_version
      )
        throw new Error('memory_command_superseded')
      return replay()
    }
    if (before.deleted || before.deletionEpoch !== guard.expectedDeletionEpoch)
      throw new Error('assertion_deleted_or_stale_epoch')
    if (before.version !== guard.expectedVersion) throw new Error('assertion_version_conflict')
    const result = apply()
    checkpoint()
    const after = state()
    this.db
      .prepare('INSERT INTO memory_command_receipts VALUES(?,?,?,?,?,?)')
      .run(
        guard.idempotencyKey,
        id,
        hash,
        after.deletionEpoch,
        after.version,
        new Date().toISOString(),
      )
    // Bounded retry metadata; evicted old retries still fail the version precondition.
    this.db
      .prepare(
        'DELETE FROM memory_command_receipts WHERE rowid NOT IN (SELECT rowid FROM memory_command_receipts ORDER BY rowid DESC LIMIT 4096)',
      )
      .run()
    return result
  }
}
