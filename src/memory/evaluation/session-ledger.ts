import { createHash } from 'node:crypto'
import { performance } from 'node:perf_hooks'
import type Database from 'better-sqlite3'

/** Separate, host-owned evaluation ledger; never credential storage or the production backup DB. */
export class EvaluationSessionLedger {
  constructor(private readonly db: Database.Database) {
    db.exec(`CREATE TABLE IF NOT EXISTS evaluation_runs(
      run_id TEXT PRIMARY KEY, approval_hash TEXT NOT NULL,
      deadline_ms REAL NOT NULL, max_calls INTEGER NOT NULL,
      calls INTEGER NOT NULL, known_usd REAL NOT NULL,
      state TEXT NOT NULL CHECK(state IN ('ready','in_flight','unknown','stopped'))
    )`)
  }
  private now() {
    return performance.timeOrigin + performance.now()
  }
  open(runId: string, approval: unknown, durationMs: number, maxCalls: number) {
    const hash = createHash('sha256').update(JSON.stringify(approval)).digest('hex')
    this.db
      .transaction(() => {
        const existing = this.read(runId)
        if (existing) {
          if (existing.approval_hash !== hash) throw new Error('evaluation_run_approval_conflict')
          return
        }
        this.db
          .prepare('INSERT INTO evaluation_runs VALUES(?,?,?,?,0,0,?)')
          .run(runId, hash, this.now() + durationMs, maxCalls, 'ready')
      })
      .immediate()
  }
  private read(runId: string) {
    return this.db.prepare('SELECT * FROM evaluation_runs WHERE run_id=?').get(runId) as
      | {
          approval_hash: string
          deadline_ms: number
          max_calls: number
          calls: number
          known_usd: number
          state: string
        }
      | undefined
  }
  assertReady(runId: string) {
    const row = this.read(runId)
    if (
      !row ||
      row.state !== 'ready' ||
      !Number.isSafeInteger(row.calls) ||
      !Number.isSafeInteger(row.max_calls) ||
      row.calls < 0 ||
      row.calls >= row.max_calls ||
      !Number.isFinite(row.deadline_ms) ||
      this.now() >= row.deadline_ms
    )
      throw new Error('evaluation_run_not_ready')
  }
  assertInFlight(runId: string) {
    const row = this.read(runId)
    if (
      !row ||
      row.state !== 'in_flight' ||
      !Number.isFinite(row.deadline_ms) ||
      this.now() >= row.deadline_ms
    )
      throw new Error('evaluation_run_not_active')
  }
  start(runId: string) {
    this.db
      .transaction(() => {
        this.assertReady(runId)
        this.db
          .prepare("UPDATE evaluation_runs SET calls=calls+1,state='in_flight' WHERE run_id=?")
          .run(runId)
      })
      .immediate()
  }
  finish(runId: string, cost?: number, stopped = false) {
    this.db
      .transaction(() => {
        const row = this.read(runId)
        if (!row || row.state !== 'in_flight') throw new Error('evaluation_run_state_conflict')
        const known =
          cost !== undefined &&
          Number.isFinite(cost) &&
          cost >= 0 &&
          Number.isFinite(row.known_usd + cost)
        this.db
          .prepare('UPDATE evaluation_runs SET state=?,known_usd=? WHERE run_id=?')
          .run(
            known ? (stopped ? 'stopped' : 'ready') : 'unknown',
            known ? row.known_usd + cost : row.known_usd,
            runId,
          )
      })
      .immediate()
  }
  /** Crash-left in_flight is deliberately never automatically released or retried. */
  status(runId: string) {
    const row = this.read(runId)
    return (
      row && {
        calls: row.calls,
        knownUsd: row.known_usd,
        state: row.state,
        deadlineMs: row.deadline_ms,
        monetaryCoverage:
          row.state === 'unknown' || row.state === 'in_flight' ? 'unknown' : 'reported_only',
        hardUsdCap: false,
      }
    )
  }
}
