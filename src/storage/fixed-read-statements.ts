import type Database from 'better-sqlite3'

type ReadQueries = Record<string, { readonly sql: string; readonly all: boolean }>
type Handle = { statement: Database.Statement; executing: boolean }

/** Private compiled reads only. Every call executes fresh SQL; no rows, parsed
 * objects, permissions or transaction state are retained. SQLite recompiles on
 * schema changes. A failed read drops every handle, and reentrant reads use the
 * original prepare path rather than a statement that is already executing.
 */
export class FixedReadStatements<Queries extends ReadQueries> {
  private readonly handles = new Map<keyof Queries, Handle>()
  constructor(
    private readonly db: Pick<Database.Database, 'prepare'>,
    private readonly queries: Queries,
  ) {}

  read(key: keyof Queries, ...parameters: unknown[]): unknown {
    if (!Object.hasOwn(this.queries, key)) throw new Error('fixed_read_unknown')
    const query = this.queries[key]!
    let handle = this.handles.get(key)
    try {
      if (!handle || handle.executing) {
        handle = { statement: this.db.prepare(query.sql), executing: false }
        if (!this.handles.get(key)?.executing) this.handles.set(key, handle)
      }
      handle.executing = true
      return query.all ? handle.statement.all(...parameters) : handle.statement.get(...parameters)
    } catch (error) {
      this.handles.clear()
      throw error
    } finally {
      if (handle) handle.executing = false
    }
  }
}
