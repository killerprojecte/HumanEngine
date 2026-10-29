import type Database from 'better-sqlite3'
import type { Store } from '../../store.ts'
import type { IdentityMutation } from '../identities/contracts.ts'
import { RegistryStateSchema } from '../identities/contracts.ts'
import { IdentityRegistry } from '../identities/service.ts'
import { revoke, requireIdentityReader, isRecoveryMaintenance, type Revocation } from './ledger.ts'
import { parseIdentityRecoveryEvent, type IdentityRecoveryEvent } from './identity-contract.ts'

/** Only the trusted synchronous registry write hook may create these metadata obligations. */
export function recordIdentityMutation(db: Database.Database, mutation: IdentityMutation): void {
  // Recovery applies only exact journaled operations through its own checked port.
  // A root recall without its derived receipt cannot invent a new registry head.
  if (isRecoveryMaintenance(db))
    throw new Error(
      mutation.kind === 'source_recall'
        ? 'recovery_identity_source_cleanup_missing'
        : 'recovery_identity_unjournaled_mutation',
    )
  requireIdentityReader(db)
  const { kind: operation, ...rest } = mutation
  revoke(
    db,
    parseIdentityRecoveryEvent({ kind: 'identity_mutation', schemaVersion: 1, operation, ...rest }),
  )
}
export function createRecoveryIdentityPort(store: Store) {
  const registry = new IdentityRegistry(store.db, {
    assertReady: () => store.assertRecoveryReady(),
  })
  function assertSupported() {
    const tables = store.db
      .prepare("SELECT name,type FROM sqlite_master WHERE name LIKE 'memory_identity_%'")
      .all() as { name: string; type: string }[]
    if (!tables.length) return
    if (
      tables.length !== 1 ||
      tables[0]!.name !== 'memory_identity_registry' ||
      tables[0]!.type !== 'table'
    )
      throw new Error('recovery_unknown_identity_schema')
    const columns = (
      store.db.prepare('PRAGMA table_xinfo(memory_identity_registry)').all() as { name: string }[]
    )
      .map((c) => c.name)
      .sort()
    if (JSON.stringify(columns) !== JSON.stringify(['id', 'value']))
      throw new Error('recovery_unknown_identity_schema')
    const rows = store.db.prepare('SELECT id,value FROM memory_identity_registry').all() as {
      id: unknown
      value: unknown
    }[]
    if (rows.length !== 1 || rows[0]!.id !== 1 || typeof rows[0]!.value !== 'string')
      throw new Error('recovery_identity_material_invalid')
    try {
      RegistryStateSchema.parse(JSON.parse(rows[0]!.value))
    } catch {
      throw new Error('recovery_identity_material_invalid')
    }
    registry.witness()
  }
  function matches(event: IdentityRecoveryEvent) {
    const actual = registry.witness()
    return (
      actual.revision === event.nextRevision && actual.qualificationHash === event.qualificationHash
    )
  }
  function replay(
    entries: readonly { sequence: number; revocation: Revocation }[],
    appliedSequence: number,
  ) {
    assertSupported()
    const identity = entries.filter(
      (e): e is { sequence: number; revocation: IdentityRecoveryEvent } =>
        e.revocation.kind === 'identity_mutation',
    )
    const latest = identity.at(-1)?.revocation
    if (!latest) {
      if (registry.witness().revision !== 0) throw new Error('recovery_identity_witness_missing')
      return
    }
    if (matches(latest)) return
    const prefix = identity.findLast((e) => e.sequence <= appliedSequence)?.revocation
    if (prefix ? !matches(prefix) : registry.witness().revision !== 0)
      throw new Error('recovery_identity_head_mismatch')
    for (const { sequence, revocation: event } of identity) {
      if (sequence <= appliedSequence) continue
      const current = registry.witness()
      if (
        current.revision !== event.expectedRevision ||
        current.qualificationHash !== event.beforeHash
      )
        throw new Error('recovery_identity_head_mismatch')
      if (event.operation === 'link' || event.operation === 'alias_put')
        throw new Error('recovery_identity_material_missing')
      store.db
        .transaction(() => {
          requireIdentityReader(store.db)
          const command = {
            operationId: event.operationId,
            expectedRevision: event.expectedRevision,
            adminConfirmed: true as const,
          }
          if (event.operation === 'unlink')
            registry.unlink(command, event.targets.endpoints[0]!, event.targets.endpoints[1]!)
          else if (event.operation === 'split') registry.split(command, event.targets.endpoints)
          else if (event.operation === 'alias_remove')
            registry.removeAlias(command, event.targets.aliasIds[0]!)
          else
            registry.recallSource(
              command,
              event.targets.source!.sceneId,
              event.targets.source!.messageId,
            )
          if (!matches(event)) throw new Error('recovery_identity_head_mismatch')
        })
        .immediate()
    }
  }
  function verify(events: readonly Revocation[]) {
    assertSupported()
    const latest = events.findLast(
      (e): e is IdentityRecoveryEvent => e.kind === 'identity_mutation',
    )
    if (latest && !matches(latest)) throw new Error('recovery_identity_head_mismatch')
    for (const alias of registry.adminList(true).aliases) {
      if (
        alias.source.kind === 'observation' &&
        store.recalled(alias.source.sceneId, alias.source.messageId)
      )
        throw new Error('recovery_identity_source_cleanup_missing')
    }
  }
  return { assertSupported, replay, verify }
}
