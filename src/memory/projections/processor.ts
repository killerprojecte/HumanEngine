import type Database from 'better-sqlite3'
import type { MemoryRecord } from '../domain/contracts.ts'
import { ProjectionOutbox } from '../storage/projection-outbox.ts'

type ProjectionDatabase = Pick<
  Database.Database,
  'exec' | 'prepare' | 'transaction' | 'inTransaction'
>
export interface ProjectionDrainResult {
  claimed: number
  completed: number
  invalid: number
  failed: number
}
class SourceUnavailable extends Error {}

/** Optional deterministic shadow indexes, not authoritative reads or learned facts.
 * readEligible must validate current support/scope/retraction status, but must not
 * exclude a future validFrom: a future fact can be indexed before it becomes usable.
 * Indexing does not grant access; all answer paths retain authoritative filtering.
 */
export class MemoryProjectionProcessor {
  constructor(
    private readonly db: ProjectionDatabase,
    private readonly outbox: ProjectionOutbox,
    private readonly readEligible: (id: string) => MemoryRecord | undefined,
    private readonly now = Date.now,
  ) {
    db.transaction(() =>
      db.exec(`
      CREATE VIRTUAL TABLE IF NOT EXISTS memory_fts USING fts5(
        assertion_id UNINDEXED, scope_key UNINDEXED, person_id UNINDEXED, content
      );
      CREATE TABLE IF NOT EXISTS memory_person_projection_state (
        assertion_id TEXT PRIMARY KEY, version INTEGER NOT NULL, deletion_epoch INTEGER NOT NULL,
        scope_key TEXT NOT NULL, person_id TEXT, refreshed_at INTEGER NOT NULL
      );
    `),
    )()
  }

  drain(maxJobs = 16): ProjectionDrainResult {
    if (!Number.isInteger(maxJobs) || maxJobs < 1 || maxJobs > 64)
      throw new Error('invalid_projection_drain_limit')
    const result: ProjectionDrainResult = { claimed: 0, completed: 0, invalid: 0, failed: 0 }
    while (result.claimed < maxJobs) {
      const leases = this.outbox.claim({
        leaseMs: 30_000,
        limit: Math.min(16, maxJobs - result.claimed),
      })
      if (!leases.length) break
      result.claimed += leases.length
      for (const lease of leases) {
        try {
          const completed = this.outbox.complete(lease, () => {
            const record = this.readEligible(lease.assertionId)
            if (
              !record ||
              record.id !== lease.assertionId ||
              record.status === 'invalidated' ||
              record.lineageStatus === 'quarantined'
            )
              throw new SourceUnavailable()
            const scope =
              record.scope.kind === 'scene' ? `scene:${record.scope.sceneId}` : 'shared:public'
            const person = record.scope.kind === 'scene' ? (record.scope.personId ?? null) : null
            if (lease.kind === 'lexical') {
              this.db.prepare('DELETE FROM memory_fts WHERE assertion_id=?').run(record.id)
              this.db
                .prepare(
                  'INSERT INTO memory_fts(assertion_id,scope_key,person_id,content) VALUES(?,?,?,?)',
                )
                .run(record.id, scope, person, record.content)
            } else {
              const now = this.now()
              if (!Number.isSafeInteger(now) || now < 0) throw new Error('invalid_projection_clock')
              this.db
                .prepare(
                  `INSERT INTO memory_person_projection_state VALUES(?,?,?,?,?,?)
                ON CONFLICT(assertion_id) DO UPDATE SET version=excluded.version,
                  deletion_epoch=excluded.deletion_epoch,scope_key=excluded.scope_key,
                  person_id=excluded.person_id,refreshed_at=excluded.refreshed_at`,
                )
                .run(record.id, lease.version, lease.deletionEpoch, scope, person, now)
            }
          })
          if (completed) result.completed++
          else result.invalid++
        } catch (error) {
          const unavailable = error instanceof SourceUnavailable
          this.outbox.fail(lease, unavailable ? 'source_unavailable' : 'projection_failed')
          if (unavailable) result.invalid++
          else result.failed++
        }
      }
    }
    this.outbox.pruneTerminal()
    return result
  }

  /** Call in the authoritative lifecycle transaction before deleted evidence can be read again. */
  remove(assertionId: string): void {
    if (!this.db.inTransaction) throw new Error('projection_transaction_required')
    this.db.prepare('DELETE FROM memory_fts WHERE assertion_id=?').run(assertionId)
    this.db
      .prepare('DELETE FROM memory_person_projection_state WHERE assertion_id=?')
      .run(assertionId)
    this.outbox.invalidate(assertionId)
  }
}
