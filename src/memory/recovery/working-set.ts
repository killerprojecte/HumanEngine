import type { Store } from '../../store.ts'
import {
  WORKING_SET_TABLES,
  WorkingSetItemSchema,
  WorkingSetJobSchema,
  WorkingSetReceiptSchema,
  WorkingSetPinPolicySchema,
  type WorkingSetPinPolicy,
} from '../working-set/contracts.ts'
import {
  isRecoveryMaintenance,
  requireWorkingSetReader,
  revoke,
  type Revocation,
} from './ledger.ts'
import { parseWorkingSetPinRecoveryEvent } from './working-set-contract.ts'
import { WorkingSetStorage } from '../working-set/storage.ts'

export function recordWorkingSetPin(db: Store['db'], policy: WorkingSetPinPolicy): void {
  requireWorkingSetReader(db)
  revoke(db, parseWorkingSetPinRecoveryEvent({ kind: 'working_set_pin', schemaVersion: 1, policy }))
}
export interface RecoveryWorkingSetPorts {
  resolvePinTarget(policy: WorkingSetPinPolicy): 'current' | 'stale' | 'missing'
}

/** Soft attention metadata is discarded, never reconstructed as authority or access history. */
export function createRecoveryWorkingSetPort(store: Store, ports?: RecoveryWorkingSetPorts) {
  function assertSupported(): boolean {
    if (!store.recoveryQuarantined || !isRecoveryMaintenance(store.db))
      throw new Error('recovery_maintenance_required')
    const tables = store.db
      .prepare(
        "SELECT name,type FROM sqlite_master WHERE (type='table' OR type='view') AND name GLOB 'memory_working_set_*'",
      )
      .all() as { name: string; type: string }[]
    for (const row of tables) {
      const columns = WORKING_SET_TABLES[row.name as keyof typeof WORKING_SET_TABLES]
      if (row.type !== 'table' || !columns) throw new Error('recovery_unknown_working_set_schema')
      const actual = (
        store.db.prepare(`PRAGMA table_xinfo("${row.name}")`).all() as { name: string }[]
      )
        .map((c) => c.name)
        .sort()
      if (JSON.stringify(actual) !== JSON.stringify([...columns].sort()))
        throw new Error('recovery_unknown_working_set_schema')
      const schema =
        row.name === 'memory_working_set_items'
          ? WorkingSetItemSchema
          : row.name === 'memory_working_set_jobs'
            ? WorkingSetJobSchema
            : row.name === 'memory_working_set_pins'
              ? WorkingSetPinPolicySchema
              : WorkingSetReceiptSchema
      for (const entry of store.db
        .prepare(`SELECT value FROM "${row.name}"`)
        .iterate() as Iterable<{ value: unknown }>) {
        if (typeof entry.value !== 'string') throw new Error('recovery_unknown_working_set_schema')
        let value: unknown
        try {
          value = JSON.parse(entry.value)
        } catch {
          throw new Error('recovery_unknown_working_set_schema')
        }
        if (!schema.safeParse(value).success) throw new Error('recovery_unknown_working_set_schema')
      }
      if (row.name === 'memory_working_set_pins') {
        for (const entry of store.db
          .prepare('SELECT scene_id,kind,entity_id,value FROM memory_working_set_pins')
          .iterate() as Iterable<{
          scene_id: unknown
          kind: unknown
          entity_id: unknown
          value: string
        }>) {
          const policy = WorkingSetPinPolicySchema.parse(JSON.parse(entry.value))
          if (
            entry.scene_id !== policy.sceneId ||
            entry.kind !== policy.selection.ref.kind ||
            entry.entity_id !== policy.selection.ref.id
          )
            throw new Error('recovery_unknown_working_set_schema')
        }
      }
    }
    if (tables.length && tables.length !== Object.keys(WORKING_SET_TABLES).length)
      throw new Error('recovery_unknown_working_set_schema')
    return tables.length > 0
  }
  function reset(): void {
    if (!store.db.inTransaction) throw new Error('recovery_transaction_required')
    if (!assertSupported()) return
    for (const table of Object.keys(WORKING_SET_TABLES).filter(
      (name) => name !== 'memory_working_set_pins',
    ))
      store.db.prepare(`DELETE FROM "${table}"`).run()
    for (const table of Object.keys(WORKING_SET_TABLES).filter(
      (name) => name !== 'memory_working_set_pins',
    ))
      if (store.db.prepare(`SELECT 1 FROM "${table}" LIMIT 1`).get())
        throw new Error('recovery_working_set_reset_incomplete')
  }
  function verifyPins(entries: readonly Revocation[]): void {
    if (!store.db.inTransaction) throw new Error('recovery_transaction_required')
    assertSupported()
    const policies = new Map<string, WorkingSetPinPolicy>()
    const key = (p: WorkingSetPinPolicy) =>
      JSON.stringify([p.sceneId, p.selection.ref.kind, p.selection.ref.id])
    const deletedAssertions = new Set(
      entries
        .filter((e) => e.kind === 'assertion' && e.terminal)
        .map((e) => (e as Extract<Revocation, { kind: 'assertion' }>).id),
    )
    const deletedCommitments = new Set(
      entries
        .filter((e) => e.kind === 'commitment_delete')
        .map((e) =>
          JSON.stringify([
            (e as Extract<Revocation, { kind: 'commitment_delete' }>).sceneId,
            (e as Extract<Revocation, { kind: 'commitment_delete' }>).id,
          ]),
        ),
    )
    for (const entry of entries)
      if (entry.kind === 'working_set_pin') policies.set(key(entry.policy), entry.policy)
    const storage = new WorkingSetStorage(store.db)
    // No snapshot-only policy is accepted without the external precommit authority.
    if (assertSupported()) {
      for (const row of store.db
        .prepare('SELECT value FROM memory_working_set_pins')
        .iterate() as Iterable<{ value: string }>) {
        const p = WorkingSetPinPolicySchema.parse(JSON.parse(row.value))
        if (!policies.has(key(p))) throw new Error('recovery_working_set_pin_authority_missing')
      }
    }
    for (const policy of policies.values()) {
      const ref = policy.selection.ref
      if (
        ref.kind === 'assertion'
          ? deletedAssertions.has(ref.id)
          : deletedCommitments.has(JSON.stringify([policy.sceneId, ref.id]))
      ) {
        if (assertSupported())
          store.db
            .prepare(
              'DELETE FROM memory_working_set_pins WHERE scene_id=? AND kind=? AND entity_id=?',
            )
            .run(policy.sceneId, ref.kind, ref.id)
        continue
      }
      if (policy.pinned) {
        const target = ports?.resolvePinTarget(policy)
        if (target !== 'current' && target !== 'stale')
          throw new Error('recovery_working_set_pin_material_missing')
      }
      storage.ensure()
      storage.putPin(policy)
    }
  }
  return { assertSupported, reset, verifyPins }
}
