import { randomUUID } from 'node:crypto'
import type Database from 'better-sqlite3'

export type ProjectionKind = 'lexical' | 'person'
export interface ProjectionSnapshot {
  assertionId: string
  version: number
  deletionEpoch: number
  kind: ProjectionKind
}
export interface ProjectionLease extends ProjectionSnapshot {
  id: number
  leaseToken: string
  attempts: number
}
type OutboxDatabase = Pick<Database.Database, 'exec' | 'prepare' | 'transaction' | 'inTransaction'>
type JobRow = {
  id: number
  assertion_id: string
  version: number
  deletion_epoch: number
  kind: ProjectionKind
  state: string
  lease_token: string | null
  lease_until: number | null
  attempts: number
}
const kinds: readonly string[] = ['lexical', 'person']
const safeInt = (value: number) => Number.isSafeInteger(value) && value >= 0

/** Durable metadata-only work for deterministic projections of committed assertions.
 * Never enqueue chat extraction, embeddings, model calls or sends here.
 * The caller still validates current source/scope/applicability before reading data
 * and within complete's synchronous callback. This queue is not read authority.
 */
export class ProjectionOutbox {
  constructor(
    private readonly db: OutboxDatabase,
    private readonly now = Date.now,
  ) {
    db.transaction(() =>
      db.exec(`
      CREATE TABLE IF NOT EXISTS memory_projection_outbox (
        id INTEGER PRIMARY KEY, assertion_id TEXT NOT NULL,
        version INTEGER NOT NULL, deletion_epoch INTEGER NOT NULL, kind TEXT NOT NULL,
        state TEXT NOT NULL, attempts INTEGER NOT NULL DEFAULT 0,
        available_at INTEGER NOT NULL, lease_token TEXT, lease_until INTEGER,
        updated_at INTEGER NOT NULL, reason TEXT,
        UNIQUE(assertion_id,version,deletion_epoch,kind),
        CHECK(kind IN ('lexical','person')),
        CHECK(state IN ('pending','leased','done','invalid','failed'))
      );
      CREATE INDEX IF NOT EXISTS memory_projection_outbox_ready
        ON memory_projection_outbox(state,available_at,id);
      CREATE INDEX IF NOT EXISTS memory_projection_outbox_assertion
        ON memory_projection_outbox(assertion_id,state);
    `),
    )()
  }

  enqueue(snapshot: ProjectionSnapshot): number {
    this.requireTransaction()
    if (
      !snapshot.assertionId ||
      !safeInt(snapshot.version) ||
      snapshot.version === 0 ||
      !safeInt(snapshot.deletionEpoch) ||
      !kinds.includes(snapshot.kind)
    )
      throw new Error('invalid_projection_snapshot')
    if (!this.current(snapshot)) throw new Error('projection_snapshot_stale')
    const existing = this.db
      .prepare(
        `SELECT id FROM memory_projection_outbox
      WHERE assertion_id=? AND version=? AND deletion_epoch=? AND kind=?`,
      )
      .get(snapshot.assertionId, snapshot.version, snapshot.deletionEpoch, snapshot.kind) as
      { id: number } | undefined
    if (existing) return existing.id
    const now = this.time()
    this.db
      .prepare(
        `UPDATE memory_projection_outbox SET state='invalid',lease_token=NULL,
      lease_until=NULL,reason='snapshot_stale',updated_at=?
      WHERE assertion_id=? AND kind=? AND state IN ('pending','leased')
        AND (version!=? OR deletion_epoch!=?)`,
      )
      .run(now, snapshot.assertionId, snapshot.kind, snapshot.version, snapshot.deletionEpoch)
    const result = this.db
      .prepare(
        `INSERT INTO memory_projection_outbox
      (assertion_id,version,deletion_epoch,kind,state,available_at,updated_at)
      VALUES(?,?,?,?,'pending',?,?)`,
      )
      .run(snapshot.assertionId, snapshot.version, snapshot.deletionEpoch, snapshot.kind, now, now)
    return Number(result.lastInsertRowid)
  }

  claim(options: { leaseMs: number; limit?: number }): ProjectionLease[] {
    if (
      !safeInt(options.leaseMs) ||
      options.leaseMs < 1 ||
      options.leaseMs > 300_000 ||
      !Number.isInteger(options.limit ?? 1) ||
      (options.limit ?? 1) < 1 ||
      (options.limit ?? 1) > 16
    )
      throw new Error('invalid_projection_lease')
    return this.db
      .transaction(() => {
        const now = this.time()
        this.db
          .prepare(
            `UPDATE memory_projection_outbox SET state='invalid',lease_token=NULL,
        lease_until=NULL,reason='snapshot_stale',updated_at=?
        WHERE state IN ('pending','leased') AND NOT EXISTS (
          SELECT 1 FROM memory_assertion_state AS current
          WHERE current.assertion_id=memory_projection_outbox.assertion_id AND current.deleted=0
            AND current.version=memory_projection_outbox.version
            AND current.deletion_epoch=memory_projection_outbox.deletion_epoch)`,
          )
          .run(now)
        this.db
          .prepare(
            `UPDATE memory_projection_outbox SET state=CASE WHEN attempts>=3 THEN 'failed' ELSE 'pending' END,
        available_at=?,lease_token=NULL,lease_until=NULL,reason='lease_expired',updated_at=?
        WHERE state='leased' AND lease_until<=?`,
          )
          .run(now, now, now)
        const rows = this.db
          .prepare(
            `SELECT * FROM memory_projection_outbox
        WHERE state='pending' AND available_at<=? ORDER BY available_at,id LIMIT ?`,
          )
          .all(now, options.limit ?? 1) as JobRow[]
        return rows.map((row) => {
          const leaseToken = randomUUID()
          this.db
            .prepare(
              `UPDATE memory_projection_outbox SET state='leased',attempts=attempts+1,
          lease_token=?,lease_until=?,updated_at=? WHERE id=? AND state='pending'`,
            )
            .run(leaseToken, now + options.leaseMs, now, row.id)
          return {
            id: row.id,
            assertionId: row.assertion_id,
            version: row.version,
            deletionEpoch: row.deletion_epoch,
            kind: row.kind,
            leaseToken,
            attempts: row.attempts + 1,
          }
        })
      })
      .immediate()
  }

  /** Trusted internal, synchronous database-only callback. No network or arbitrary plugin callback. */
  complete(lease: ProjectionLease, apply: () => void): boolean {
    if (apply.constructor.name === 'AsyncFunction')
      throw new Error('projection_callback_must_be_sync')
    return this.db
      .transaction(() => {
        if (!this.owned(lease)) return false
        const result: unknown = apply()
        if (
          result &&
          (typeof result === 'object' || typeof result === 'function') &&
          'then' in result &&
          typeof result.then === 'function'
        )
          throw new Error('projection_callback_must_be_sync')
        // A callback cannot silently change the version it is publishing.
        if (!this.owned(lease)) throw new Error('projection_changed_during_commit')
        this.db
          .prepare(
            `UPDATE memory_projection_outbox SET state='done',lease_token=NULL,
        lease_until=NULL,reason=NULL,updated_at=? WHERE id=? AND lease_token=?`,
          )
          .run(this.time(), lease.id, lease.leaseToken)
        return true
      })
      .immediate()
  }

  fail(lease: ProjectionLease, reason: 'projection_failed' | 'source_unavailable'): boolean {
    if (!['projection_failed', 'source_unavailable'].includes(reason))
      throw new Error('invalid_projection_reason')
    return this.db
      .transaction(() => {
        if (!this.owned(lease)) return false
        const terminal =
          reason === 'source_unavailable' ? 'invalid' : lease.attempts >= 3 ? 'failed' : 'pending'
        const now = this.time()
        this.db
          .prepare(
            `UPDATE memory_projection_outbox SET state=?,available_at=?,lease_token=NULL,
        lease_until=NULL,reason=?,updated_at=? WHERE id=? AND lease_token=?`,
          )
          .run(
            terminal,
            now + Math.min(30_000, 1000 * 2 ** (lease.attempts - 1)),
            reason,
            now,
            lease.id,
            lease.leaseToken,
          )
        return true
      })
      .immediate()
  }

  invalidate(assertionId: string): void {
    this.requireTransaction()
    this.db
      .prepare(
        `UPDATE memory_projection_outbox SET state='invalid',lease_token=NULL,
      lease_until=NULL,reason='snapshot_stale',updated_at=?
      WHERE assertion_id=? AND state IN ('pending','leased')`,
      )
      .run(this.time(), assertionId)
  }

  /** Retain current-snapshot idempotency forever and the newest 4096 obsolete terminal jobs.
   * This is not a total-table cap: live assertions and in-flight work are never pruned.
   */
  pruneTerminal(limit = 500): number {
    if (!Number.isInteger(limit) || limit < 1 || limit > 1000)
      throw new Error('invalid_projection_prune_limit')
    return this.db
      .transaction(() => {
        const result = this.db
          .prepare(
            `DELETE FROM memory_projection_outbox WHERE id IN (
        SELECT id FROM (
          SELECT job.id FROM memory_projection_outbox AS job
          WHERE job.state IN ('done','invalid','failed') AND NOT EXISTS (
            SELECT 1 FROM memory_assertion_state AS current
            WHERE current.assertion_id=job.assertion_id AND current.version=job.version
              AND current.deletion_epoch=job.deletion_epoch AND current.deleted=0
          ) ORDER BY job.id DESC LIMIT -1 OFFSET 4096
        ) ORDER BY id ASC LIMIT ?
      )`,
          )
          .run(limit)
        return result.changes
      })
      .immediate()
  }

  private current(snapshot: ProjectionSnapshot): boolean {
    return !!this.db
      .prepare(
        `SELECT 1 FROM memory_assertion_state
      WHERE assertion_id=? AND version=? AND deletion_epoch=? AND deleted=0`,
      )
      .get(snapshot.assertionId, snapshot.version, snapshot.deletionEpoch)
  }
  private owned(lease: ProjectionLease): boolean {
    const row = this.db
      .prepare(
        `SELECT * FROM memory_projection_outbox
      WHERE id=? AND state='leased' AND lease_token=? AND lease_until>?`,
      )
      .get(lease.id, lease.leaseToken, this.time()) as JobRow | undefined
    return (
      !!row &&
      row.assertion_id === lease.assertionId &&
      row.version === lease.version &&
      row.deletion_epoch === lease.deletionEpoch &&
      row.kind === lease.kind &&
      row.attempts === lease.attempts &&
      this.current(lease)
    )
  }
  private requireTransaction() {
    if (!this.db.inTransaction) throw new Error('projection_transaction_required')
  }
  private time() {
    const value = this.now()
    if (!safeInt(value)) throw new Error('invalid_projection_clock')
    return value
  }
}
