import type Database from 'better-sqlite3'
import type { Store } from '../../store.ts'
import type { MemoryRecord } from '../domain/contracts.ts'
import type { SupportProof } from '../domain/proofs.ts'
import { canonicalProofKey } from '../domain/proofs.ts'
import { fingerprint, historicalFingerprint, scopeKey } from '../domain/record-values.ts'
export type AssertionRow = {
  id: string
  scope_key: string
  person_id: string | null
  content: string
  value: string
  fingerprint: string
  distance?: number | null
}
export const readAssertionRow = (row: AssertionRow): MemoryRecord =>
  JSON.parse(row.value) as MemoryRecord
const supportReadSql = {
  record: 'SELECT * FROM memories WHERE id=?',
  supports: 'SELECT value FROM memory_supports WHERE memory_id=?',
} as const
type SupportRead = keyof typeof supportReadSql
type ReadHandle = { statement: Database.Statement; executing: boolean }
type ReadOperation = Partial<Record<SupportRead, ReadHandle>>
/** Internal persistence only; model-facing reads use the separate authority layer. */
export class AssertionRepository {
  private supportReads: ReadOperation | undefined

  /** Compiled handles only, scoped to one synchronous refresh. Never retains a
   * row, parsed object or authority result. Nested scopes get independent handles
   * and restore the outer scope; finally drops all handles on success or failure.
   */
  withSupportReadStatements<T>(operation: () => T): T {
    const previous = this.supportReads
    this.supportReads = {}
    try {
      return operation()
    } finally {
      this.supportReads = previous
    }
  }

  private readSupportRow(key: SupportRead, id: string): unknown {
    const operation = this.supportReads
    if (!operation) return this.store.db.prepare(supportReadSql[key]).get(id)
    let handle = operation[key]
    try {
      // Lazy preparation keeps first-read errors at their original call site.
      // Reentry follows the original prepare path, including its busy-connection error.
      if (!handle || handle.executing) {
        handle = { statement: this.store.db.prepare(supportReadSql[key]), executing: false }
        if (operation && !operation[key]?.executing) operation[key] = handle
      }
      handle.executing = true
      return handle.statement.get(id)
    } catch (error) {
      if (operation) {
        delete operation.record
        delete operation.supports
      }
      throw error
    } finally {
      if (handle) handle.executing = false
    }
  }

  constructor(
    private readonly store: Pick<Store, 'db' | 'assertRecoveryReady'>,
    private readonly mutations: {
      beforeWrite(id: string): void
      afterWrite?(record: MemoryRecord): void
      removeProjection(id: string): void
    },
  ) {}
  get(id: string): MemoryRecord | undefined {
    this.store.assertRecoveryReady()
    const row = this.readSupportRow('record', id) as AssertionRow | undefined
    return row ? readAssertionRow(row) : undefined
  }
  supports(id: string): SupportProof[] {
    const row = this.readSupportRow('supports', id) as { value: string } | undefined
    return row ? (JSON.parse(row.value) as SupportProof[]) : []
  }
  saveSupports(id: string, supports: SupportProof[]) {
    this.mutations.beforeWrite(id)
    // Replaying identical evidence must neither inflate confidence nor append
    // unbounded duplicate proofs. Keep genuinely distinct alternatives/history.
    const unique = new Map<string, SupportProof>()
    for (const proof of supports) {
      const key = canonicalProofKey(proof)
      const prior = unique.get(key)
      unique.set(
        key,
        prior
          ? {
              ...prior,
              sourceObservations: [
                ...new Map(
                  [...(proof.sourceObservations ?? []), ...(prior.sourceObservations ?? [])].map(
                    (source) => [
                      JSON.stringify([source.sceneId, source.messageId, source.senderId]),
                      source,
                    ],
                  ),
                ).values(),
              ],
            }
          : proof,
      )
    }
    this.store.db
      .prepare(
        'INSERT INTO memory_supports VALUES(?,?) ON CONFLICT(memory_id) DO UPDATE SET value=excluded.value',
      )
      .run(id, JSON.stringify([...unique.values()]))
  }
  write(memory: MemoryRecord) {
    this.mutations.beforeWrite(memory.id)
    const stored = this.store.db
      .prepare('SELECT fingerprint,value FROM memories WHERE id=?')
      .get(memory.id) as { fingerprint: string; value: string } | undefined
    if (stored && stored.value !== JSON.stringify(memory))
      this.mutations.removeProjection(memory.id)
    // Historical source retractions and lock changes must not reclaim the current
    // version's unique content key. Explicit manual reactivation still checks it.
    const fp =
      ['superseded', 'invalidated'].includes(memory.status) &&
      stored?.fingerprint === historicalFingerprint(memory)
        ? stored.fingerprint
        : fingerprint(memory.scope, memory.content)
    this.store.db
      .prepare(
        'INSERT INTO memories VALUES(?,?,?,?,?,?) ON CONFLICT(id) DO UPDATE SET scope_key=excluded.scope_key,person_id=excluded.person_id,content=excluded.content,fingerprint=excluded.fingerprint,value=excluded.value',
      )
      .run(
        memory.id,
        scopeKey(memory.scope),
        memory.scope.kind === 'scene' ? (memory.scope.personId ?? null) : null,
        memory.content,
        fp,
        JSON.stringify(memory),
      )
    this.store.db.prepare('DELETE FROM memory_sources WHERE memory_id=?').run(memory.id)
    // This reverse index locates affected messages; exact revisions remain in proof JSON.
    const insert = this.store.db.prepare('INSERT OR IGNORE INTO memory_sources VALUES(?,?,?,?)')
    for (const source of memory.sources)
      insert.run(memory.id, source.sceneId, source.messageId, source.senderId)
    this.mutations.afterWrite?.(memory)
  }
  all(): MemoryRecord[] {
    this.store.assertRecoveryReady()
    return (
      this.store.db.prepare('SELECT * FROM memories ORDER BY rowid DESC').all() as AssertionRow[]
    ).map(readAssertionRow)
  }
}
