import type Database from 'better-sqlite3'
import {
  WORKING_SET_TABLES,
  WorkingSetItemSchema,
  WorkingSetJobSchema,
  WorkingSetReceiptSchema,
  WorkingSetPinPolicySchema,
  type WorkingSetPinPolicy,
  type WorkingSetItem,
  type WorkingSetJob,
  type WorkingSetReceipt,
  type WorkingSetReference,
} from './contracts.ts'

const decode = <T>(value: unknown, parse: (value: unknown) => T): T => {
  try {
    if (typeof value !== 'string') throw new Error('working_set_storage_invalid')
    return parse(JSON.parse(value))
  } catch {
    throw new Error('working_set_storage_invalid')
  }
}
/** Metadata persistence only. Caller owns readiness, transaction and exact authority. */
export class WorkingSetStorage {
  constructor(readonly db: Database.Database) {}
  assertSupported(): boolean {
    const rows = this.db
      .prepare(
        "SELECT name,type FROM sqlite_master WHERE name LIKE 'memory_working_set_%' AND type IN ('table','view')",
      )
      .all() as { name: string; type: string }[]
    if (!rows.length) return false
    if (
      rows.length !== 4 ||
      rows.some((row) => row.type !== 'table' || !Object.hasOwn(WORKING_SET_TABLES, row.name))
    )
      throw new Error('working_set_schema_unsupported')
    for (const [name, expected] of Object.entries(WORKING_SET_TABLES)) {
      const columns = this.db.prepare(`PRAGMA table_info(${name})`).all() as { name: string }[]
      if (JSON.stringify(columns.map((c) => c.name)) !== JSON.stringify(expected))
        throw new Error('working_set_schema_unsupported')
    }
    return true
  }
  exists(): boolean {
    return !!this.db
      .prepare("SELECT 1 FROM sqlite_master WHERE type='table' AND name='memory_working_set_items'")
      .get()
  }
  ensure(): void {
    if (this.assertSupported()) return
    if (!this.db.inTransaction) throw new Error('working_set_transaction_required')
    this.db.exec(`
      CREATE TABLE memory_working_set_pins(scene_id TEXT NOT NULL,kind TEXT NOT NULL,entity_id TEXT NOT NULL,value TEXT NOT NULL,PRIMARY KEY(scene_id,kind,entity_id));
      CREATE TABLE memory_working_set_items(scene_id TEXT NOT NULL,kind TEXT NOT NULL,entity_id TEXT NOT NULL,value TEXT NOT NULL,PRIMARY KEY(scene_id,kind,entity_id));
      CREATE TABLE memory_working_set_jobs(scene_id TEXT NOT NULL,kind TEXT NOT NULL,entity_id TEXT NOT NULL,value TEXT NOT NULL,PRIMARY KEY(scene_id,kind,entity_id));
      CREATE TABLE memory_working_set_receipts(receipt_key TEXT PRIMARY KEY,selected_at INTEGER NOT NULL,value TEXT NOT NULL);
    `)
  }
  pin(
    sceneId: string,
    ref: Pick<WorkingSetReference, 'kind' | 'id'>,
  ): WorkingSetPinPolicy | undefined {
    if (!this.exists()) return undefined
    const row = this.db
      .prepare(
        'SELECT value FROM memory_working_set_pins WHERE scene_id=? AND kind=? AND entity_id=?',
      )
      .get(sceneId, ref.kind, ref.id) as { value: unknown } | undefined
    const value = row ? decode(row.value, (v) => WorkingSetPinPolicySchema.parse(v)) : undefined
    if (
      value &&
      (value.sceneId !== sceneId ||
        value.selection.ref.kind !== ref.kind ||
        value.selection.ref.id !== ref.id)
    )
      throw new Error('working_set_storage_invalid')
    return value
  }
  listPins(sceneId: string, limit: number): WorkingSetPinPolicy[] {
    if (!this.exists()) return []
    const rows = this.db
      .prepare(
        "SELECT scene_id,kind,entity_id,value FROM memory_working_set_pins WHERE scene_id=? AND json_extract(value,'$.terminal')=0 ORDER BY rowid LIMIT ?",
      )
      .all(sceneId, limit) as {
      scene_id: string
      kind: string
      entity_id: string
      value: unknown
    }[]
    return rows.map((row) => {
      const value = decode(row.value, (v) => WorkingSetPinPolicySchema.parse(v))
      if (
        row.scene_id !== sceneId ||
        value.sceneId !== sceneId ||
        value.selection.ref.kind !== row.kind ||
        value.selection.ref.id !== row.entity_id
      )
        throw new Error('working_set_storage_invalid')
      return value
    })
  }
  putPin(policy: WorkingSetPinPolicy): void {
    const value = WorkingSetPinPolicySchema.parse(policy)
    this.db
      .prepare(
        'INSERT INTO memory_working_set_pins VALUES(?,?,?,?) ON CONFLICT(scene_id,kind,entity_id) DO UPDATE SET value=excluded.value',
      )
      .run(value.sceneId, value.selection.ref.kind, value.selection.ref.id, JSON.stringify(value))
  }
  item(sceneId: string, ref: WorkingSetReference): WorkingSetItem | undefined {
    if (!this.exists()) return undefined
    const row = this.db
      .prepare(
        'SELECT value FROM memory_working_set_items WHERE scene_id=? AND kind=? AND entity_id=?',
      )
      .get(sceneId, ref.kind, ref.id) as { value: string } | undefined
    const value = row ? decode(row.value, (v) => WorkingSetItemSchema.parse(v)) : undefined
    if (
      value &&
      (value.snapshot.sceneId !== sceneId ||
        value.snapshot.ref.kind !== ref.kind ||
        value.snapshot.ref.id !== ref.id)
    )
      throw new Error('working_set_storage_invalid')
    return value
  }
  putItem(item: WorkingSetItem): void {
    const value = WorkingSetItemSchema.parse(item)
    this.db
      .prepare(
        'INSERT INTO memory_working_set_items VALUES(?,?,?,?) ON CONFLICT(scene_id,kind,entity_id) DO UPDATE SET value=excluded.value',
      )
      .run(
        value.snapshot.sceneId,
        value.snapshot.ref.kind,
        value.snapshot.ref.id,
        JSON.stringify(value),
      )
  }
  job(sceneId: string, ref: WorkingSetReference): WorkingSetJob | undefined {
    if (!this.exists()) return undefined
    const row = this.db
      .prepare(
        'SELECT value FROM memory_working_set_jobs WHERE scene_id=? AND kind=? AND entity_id=?',
      )
      .get(sceneId, ref.kind, ref.id) as { value: string } | undefined
    const value = row ? decode(row.value, (v) => WorkingSetJobSchema.parse(v)) : undefined
    if (
      value &&
      (value.snapshot.sceneId !== sceneId ||
        value.snapshot.ref.kind !== ref.kind ||
        value.snapshot.ref.id !== ref.id)
    )
      throw new Error('working_set_storage_invalid')
    return value
  }
  putJob(job: WorkingSetJob): void {
    const value = WorkingSetJobSchema.parse(job)
    this.db
      .prepare(
        'INSERT INTO memory_working_set_jobs VALUES(?,?,?,?) ON CONFLICT(scene_id,kind,entity_id) DO UPDATE SET value=excluded.value',
      )
      .run(
        value.snapshot.sceneId,
        value.snapshot.ref.kind,
        value.snapshot.ref.id,
        JSON.stringify(value),
      )
  }
  jobs(sceneId: string, now: number, limit: number): WorkingSetJob[] {
    if (!this.exists()) return []
    return (
      this.db
        .prepare(
          "SELECT scene_id,kind,entity_id,value FROM memory_working_set_jobs WHERE scene_id=? AND (json_extract(value,'$.lease') IS NULL OR json_extract(value,'$.lease.expiresAt')<=?) ORDER BY rowid LIMIT ?",
        )
        .all(sceneId, now, limit) as {
        scene_id: string
        kind: string
        entity_id: string
        value: unknown
      }[]
    ).map((row) => {
      const value = decode(row.value, (v) => WorkingSetJobSchema.parse(v))
      if (
        row.scene_id !== sceneId ||
        value.snapshot.sceneId !== sceneId ||
        value.snapshot.ref.kind !== row.kind ||
        value.snapshot.ref.id !== row.entity_id
      )
        throw new Error('working_set_storage_invalid')
      return value
    })
  }
  removeJob(sceneId: string, ref: WorkingSetReference): void {
    this.db
      .prepare('DELETE FROM memory_working_set_jobs WHERE scene_id=? AND kind=? AND entity_id=?')
      .run(sceneId, ref.kind, ref.id)
  }
  pending(sceneId: string): number {
    return this.exists()
      ? (
          this.db
            .prepare('SELECT count(*) AS n FROM memory_working_set_jobs WHERE scene_id=?')
            .get(sceneId) as { n: number }
        ).n
      : 0
  }
  receipt(key: string): WorkingSetReceipt | undefined {
    if (!this.exists()) return undefined
    const row = this.db
      .prepare('SELECT value FROM memory_working_set_receipts WHERE receipt_key=?')
      .get(key) as { value: string } | undefined
    return row ? decode(row.value, (v) => WorkingSetReceiptSchema.parse(v)) : undefined
  }
  putReceipt(key: string, value: WorkingSetReceipt): void {
    this.db
      .prepare('INSERT INTO memory_working_set_receipts VALUES(?,?,?)')
      .run(key, value.selectedAt, JSON.stringify(WorkingSetReceiptSchema.parse(value)))
  }
  receiptCount(): number {
    return (
      this.db.prepare('SELECT count(*) AS n FROM memory_working_set_receipts').get() as {
        n: number
      }
    ).n
  }
  pruneReceipts(before: number): void {
    this.db.prepare('DELETE FROM memory_working_set_receipts WHERE selected_at<?').run(before)
  }
  invalidate(sceneId: string, refs: readonly Pick<WorkingSetReference, 'kind' | 'id'>[]): void {
    if (!this.exists()) return
    for (const ref of refs) {
      for (const table of ['memory_working_set_items', 'memory_working_set_jobs'])
        this.db
          .prepare(`DELETE FROM ${table} WHERE scene_id=? AND kind=? AND entity_id=?`)
          .run(sceneId, ref.kind, ref.id)
      this.db
        .prepare(
          "DELETE FROM memory_working_set_receipts WHERE json_extract(value,'$.sceneId')=? AND json_extract(value,'$.selection.ref.kind')=? AND json_extract(value,'$.selection.ref.id')=?",
        )
        .run(sceneId, ref.kind, ref.id)
    }
  }
  /** Recovery resets statistics/jobs only; explicit pin policy is separately journaled. */
  reset(): void {
    if (this.assertSupported())
      for (const table of Object.keys(WORKING_SET_TABLES).filter(
        (name) => name !== 'memory_working_set_pins',
      ))
        this.db.prepare(`DELETE FROM ${table}`).run()
  }
}
