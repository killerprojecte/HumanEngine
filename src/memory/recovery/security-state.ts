import { randomUUID } from 'node:crypto'
import type Database from 'better-sqlite3'
import { revoke } from './ledger.ts'

export interface SecurityStateRevision {
  kind: 'security_state'
  revision: string
}

/** No values, endpoints, credential names or low-entropy value hashes enter authority. */
export function ensureSecurityState(db: Database.Database): void {
  db.exec(
    'CREATE TABLE IF NOT EXISTS recovery_security_state (id INTEGER PRIMARY KEY CHECK(id=1), revision TEXT NOT NULL)',
  )
}

export function advanceSecurityState(db: Database.Database): string {
  if (!db.inTransaction) throw new Error('recovery_transaction_required')
  ensureSecurityState(db)
  const revision = randomUUID()
  revoke(db, { kind: 'security_state', revision })
  db.prepare(
    'INSERT INTO recovery_security_state VALUES(1,?) ON CONFLICT(id) DO UPDATE SET revision=excluded.revision',
  ).run(revision)
  return revision
}

/** Exact latest trusted material is required; replay cannot reconstruct rotated credentials. */
export function assertSecurityState(
  db: Database.Database,
  latest: SecurityStateRevision | undefined,
): void {
  const object = db
    .prepare("SELECT type FROM sqlite_master WHERE name='recovery_security_state'")
    .get() as { type: string } | undefined
  let revision: string | undefined
  if (object) {
    if (object.type !== 'table') throw new Error('recovery_security_state_invalid')
    const columns = db.pragma('table_xinfo(recovery_security_state)') as { name: string }[]
    if (columns.length !== 2 || columns[0]?.name !== 'id' || columns[1]?.name !== 'revision')
      throw new Error('recovery_security_state_invalid')
    const rows = db.prepare('SELECT id,revision FROM recovery_security_state LIMIT 2').all() as {
      id: unknown
      revision: unknown
    }[]
    if (rows.length > 1) throw new Error('recovery_security_state_invalid')
    const row = rows[0]
    if (row) {
      if (
        row.id !== 1 ||
        typeof row.revision !== 'string' ||
        !/^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/i.test(row.revision)
      )
        throw new Error('recovery_security_state_invalid')
      revision = row.revision
    }
  }
  if (revision !== latest?.revision) throw new Error('recovery_security_state_unrecoverable')
}
