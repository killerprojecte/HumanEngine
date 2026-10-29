import { createHash } from 'node:crypto'
import { isDeepStrictEqual } from 'node:util'
import type { BudgetDatabase } from '../storage.ts'
import {
  ExternalCostReceiptSchema,
  EXTERNAL_COST_TABLE,
  EXTERNAL_COST_COLUMNS,
  type ExternalCostReceipt,
} from './contracts.ts'
const hash = (v: unknown) => createHash('sha256').update(JSON.stringify(v)).digest('hex')
export class ExternalCostStorage {
  constructor(readonly db: BudgetDatabase) {}
  assertSupported(): boolean {
    const objects = this.db
      .prepare(
        "SELECT name,type FROM sqlite_master WHERE name GLOB 'budget_external_cost_*' AND type IN ('table','view')",
      )
      .all() as { name: string; type: string }[]
    if (!objects.length) return false
    if (
      objects.length !== 1 ||
      objects[0]!.name !== EXTERNAL_COST_TABLE ||
      objects[0]!.type !== 'table'
    )
      throw new Error('external_cost_schema_unsupported')
    const columns = this.db.prepare(`PRAGMA table_xinfo(${EXTERNAL_COST_TABLE})`).all() as {
      name: string
    }[]
    if (
      !isDeepStrictEqual(
        columns.map((c) => c.name),
        [...EXTERNAL_COST_COLUMNS],
      )
    )
      throw new Error('external_cost_schema_unsupported')
    return true
  }
  ensure() {
    if (this.assertSupported()) return
    this.db.exec(
      `CREATE TABLE ${EXTERNAL_COST_TABLE}(id TEXT NOT NULL,revision INTEGER NOT NULL,payload TEXT NOT NULL,payload_hash TEXT NOT NULL,correction_id TEXT UNIQUE,recorded_at TEXT NOT NULL,PRIMARY KEY(id,revision))`,
    )
  }
  private decode(row: unknown): ExternalCostReceipt {
    const r = row as {
      id: unknown
      revision: unknown
      payload: unknown
      payload_hash: unknown
      correction_id: unknown
      recorded_at: unknown
    }
    if (typeof r.payload !== 'string') throw new Error('external_cost_storage_invalid')
    let value: ExternalCostReceipt
    try {
      value = ExternalCostReceiptSchema.parse(JSON.parse(r.payload))
    } catch {
      throw new Error('external_cost_storage_invalid')
    }
    if (
      r.id !== value.receipt.id ||
      r.revision !== value.revision ||
      r.correction_id !== value.correctionId ||
      r.recorded_at !== value.recordedAt ||
      r.payload_hash !== hash(value)
    )
      throw new Error('external_cost_storage_invalid')
    return value
  }
  latest(id: string) {
    if (!this.assertSupported()) return undefined
    const r = this.db
      .prepare(`SELECT * FROM ${EXTERNAL_COST_TABLE} WHERE id=? ORDER BY revision DESC LIMIT 1`)
      .get(id)
    return r ? this.decode(r) : undefined
  }
  correction(id: string) {
    if (!this.assertSupported()) return undefined
    const r = this.db.prepare(`SELECT * FROM ${EXTERNAL_COST_TABLE} WHERE correction_id=?`).get(id)
    return r ? this.decode(r) : undefined
  }
  current(): ExternalCostReceipt[] {
    if (!this.assertSupported()) return []
    const rows = this.db
      .prepare(
        `SELECT r.* FROM ${EXTERNAL_COST_TABLE} r JOIN (SELECT id,max(revision) revision FROM ${EXTERNAL_COST_TABLE} GROUP BY id) h ON r.id=h.id AND r.revision=h.revision LIMIT 10001`,
      )
      .all()
    if (rows.length > 10000) throw new Error('external_cost_capacity')
    return rows.map((r) => this.decode(r))
  }
  put(input: ExternalCostReceipt) {
    const value = ExternalCostReceiptSchema.parse(input)
    this.ensure()
    const existing = this.db
      .prepare(`SELECT * FROM ${EXTERNAL_COST_TABLE} WHERE id=? AND revision=?`)
      .get(value.receipt.id, value.revision)
    if (existing) {
      if (!isDeepStrictEqual(this.decode(existing), value))
        throw new Error('external_cost_conflict')
      return
    }
    this.db
      .prepare(`INSERT INTO ${EXTERNAL_COST_TABLE} VALUES(?,?,?,?,?,?)`)
      .run(
        value.receipt.id,
        value.revision,
        JSON.stringify(value),
        hash(value),
        value.correctionId,
        value.recordedAt,
      )
  }
}
