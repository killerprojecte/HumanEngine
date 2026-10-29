import { randomUUID } from 'node:crypto'
import type Database from 'better-sqlite3'

type HoldDatabase = Pick<Database.Database, 'prepare' | 'exec' | 'transaction'>

export const RESTORATION_REVIEW_ACK = 'I_reviewed_accounting_and_accept_incomplete_history' as const
export interface RestoredModelHold {
  restoreId: string
  status: 'held' | 'manually_acknowledged'
}
export function readRestoredModelHold(db: HoldDatabase): RestoredModelHold | undefined {
  const object = db
    .prepare("SELECT type FROM sqlite_master WHERE name='recovery_model_hold'")
    .get() as { type: string } | undefined
  if (!object) return undefined
  if (object.type !== 'table') throw new Error('recovery_model_hold_invalid')
  const columns = db.prepare('PRAGMA table_xinfo(recovery_model_hold)').all() as { name: string }[]
  if (columns.map((c) => c.name).join(',') !== 'id,restore_id,status')
    throw new Error('recovery_model_hold_invalid')
  const rows = db.prepare('SELECT * FROM recovery_model_hold LIMIT 2').all() as {
    id: unknown
    restore_id: unknown
    status: unknown
  }[]
  if (rows.length !== 1) throw new Error('recovery_model_hold_invalid')
  const row = rows[0]!
  if (
    row.id !== 1 ||
    typeof row.restore_id !== 'string' ||
    !/^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/i.test(
      row.restore_id,
    ) ||
    typeof row.status !== 'string' ||
    !['held', 'manually_acknowledged'].includes(row.status)
  )
    throw new Error('recovery_model_hold_invalid')
  return { restoreId: row.restore_id, status: row.status as RestoredModelHold['status'] }
}
/** Every verified restore starts a fresh hold, even if the copied snapshot was acknowledged. */
export function markRestoredModelHold(db: Database.Database): RestoredModelHold {
  if (!db.inTransaction) throw new Error('recovery_transaction_required')
  readRestoredModelHold(db)
  db.exec(
    'CREATE TABLE IF NOT EXISTS recovery_model_hold(id INTEGER PRIMARY KEY CHECK(id=1),restore_id TEXT NOT NULL,status TEXT NOT NULL)',
  )
  const restoreId = randomUUID()
  db.prepare(
    "INSERT INTO recovery_model_hold VALUES(1,?,'held') ON CONFLICT(id) DO UPDATE SET restore_id=excluded.restore_id,status='held'",
  ).run(restoreId)
  return { restoreId, status: 'held' }
}
/** Acknowledgement permits admission; it never reconciles usage or claims accounting completeness. */
export function acknowledgeRestoredModelHold(
  db: HoldDatabase,
  restoreId: string,
  acknowledgement: string,
): void {
  if (acknowledgement !== RESTORATION_REVIEW_ACK)
    throw new Error('recovery_model_review_ack_required')
  db.transaction(() => {
    const current = readRestoredModelHold(db)
    if (!current || current.restoreId !== restoreId)
      throw new Error('recovery_model_review_conflict')
    db.prepare(
      "UPDATE recovery_model_hold SET status='manually_acknowledged' WHERE id=1 AND restore_id=? AND status='held'",
    ).run(restoreId)
  }).immediate()
}
