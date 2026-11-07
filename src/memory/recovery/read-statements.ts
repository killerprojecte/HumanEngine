import type Database from 'better-sqlite3'

const queries = {
  dataVersion: { sql: 'PRAGMA main.data_version', all: false },
  totalChanges: { sql: 'SELECT total_changes() AS n', all: false },
  queryOnly: { sql: 'PRAGMA query_only', all: false },
  schemaVersion: { sql: 'PRAGMA main.schema_version', all: false },
  gateExists: {
    sql: "SELECT 1 FROM main.sqlite_master WHERE type='table' AND name='recovery_gate'",
    all: false,
  },
  gateValue: { sql: 'SELECT value FROM main.recovery_gate WHERE id=1', all: false },
  identities: { sql: 'SELECT id,format FROM main.identity', all: true },
  chain: { sql: 'SELECT * FROM main.revocations ORDER BY sequence', all: true },
  identity: { sql: 'SELECT id FROM main.identity', all: false },
  latest: {
    sql: 'SELECT sequence,hash FROM main.revocations ORDER BY sequence DESC LIMIT 1',
    all: false,
  },
} as const
export type RecoveryRead = keyof typeof queries
const statements = new WeakMap<
  Database.Database,
  Partial<Record<Exclude<RecoveryRead, 'queryOnly'>, Database.Statement>>
>()
const queryOnlyStatements = new WeakMap<Database.Database, Database.Statement>()

/** Fixed compiled handles only: this helper never caches rows or validation state.
 * Each call executes against the connection's current transaction/snapshot. Handles
 * never escape or accept bound arguments. Only the dedicated query-only handle uses
 * fixed scalar mode, avoiding row-factory regeneration when SQLite reparses this PRAGMA.
 * No caller can change a cached handle's result mode.
 * SQLite revalidates/recompiles on schema change; errors invalidate handles and fail closed.
 */
export function readRecoveryStatement(db: Database.Database, key: RecoveryRead): unknown {
  if (!db.open) {
    statements.delete(db)
    queryOnlyStatements.delete(db)
    throw new Error('recovery_connection_closed')
  }
  if (!Object.hasOwn(queries, key)) throw new Error('recovery_read_unknown')
  try {
    if (key === 'queryOnly') {
      let statement = queryOnlyStatements.get(db)
      if (!statement) {
        statement = db.prepare(queries.queryOnly.sql).pluck()
        queryOnlyStatements.set(db, statement)
      }
      // Execute the literal PRAGMA every time; only its compiled handle is reused.
      const value: unknown = statement.get()
      if (value !== 0 && value !== 1) throw new Error('recovery_query_only_invalid')
      return { query_only: value }
    }
    let cached = statements.get(db)
    if (!cached) {
      cached = {}
      statements.set(db, cached)
    }
    const query = queries[key]
    const statement = cached[key] ?? (cached[key] = db.prepare(query.sql))
    // Full audits stream history; only explicit callers collect parsed events.
    return key === 'chain' ? statement.iterate() : query.all ? statement.all() : statement.get()
  } catch (error) {
    statements.delete(db)
    queryOnlyStatements.delete(db)
    throw error
  }
}
