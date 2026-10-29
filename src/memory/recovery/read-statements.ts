import type Database from 'better-sqlite3'

const queries = {
  gateExists: {
    sql: "SELECT 1 FROM sqlite_master WHERE type='table' AND name='recovery_gate'",
    all: false,
  },
  gateValue: { sql: 'SELECT value FROM recovery_gate WHERE id=1', all: false },
  identities: { sql: 'SELECT id,format FROM identity', all: true },
  chain: { sql: 'SELECT * FROM revocations ORDER BY sequence', all: true },
  identity: { sql: 'SELECT id FROM identity', all: false },
  latest: {
    sql: 'SELECT sequence,hash FROM revocations ORDER BY sequence DESC LIMIT 1',
    all: false,
  },
} as const
export type RecoveryRead = keyof typeof queries
const statements = new WeakMap<
  Database.Database,
  Partial<Record<RecoveryRead, Database.Statement>>
>()

/** Six fixed compiled handles only: never caches rows, head, validity or permission.
 * Each call executes against the connection's current transaction/snapshot. Handles
 * never escape, accept no bound arguments and cannot be changed to raw/pluck/iterate.
 * SQLite revalidates/recompiles on schema change; errors invalidate handles and fail closed.
 */
export function readRecoveryStatement(db: Database.Database, key: RecoveryRead): unknown {
  if (!db.open) {
    statements.delete(db)
    throw new Error('recovery_connection_closed')
  }
  if (!Object.hasOwn(queries, key)) throw new Error('recovery_read_unknown')
  let cached = statements.get(db)
  if (!cached) {
    cached = {}
    statements.set(db, cached)
  }
  try {
    const query = queries[key]
    const statement = cached[key] ?? (cached[key] = db.prepare(query.sql))
    return query.all ? statement.all() : statement.get()
  } catch (error) {
    statements.delete(db)
    throw error
  }
}
