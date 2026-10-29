import { CommitmentTemporalStore } from '../commitments/temporal-store.ts'
import type Database from 'better-sqlite3'
import type { Store } from '../../store.ts'
import type { CommitmentService } from '../commitments/service.ts'
import type { CommitmentMutation } from '../domain/commitments.ts'
import {
  isRecoveryMaintenance,
  requireCommitmentReader,
  requireOperationalReader,
  revoke,
  type Revocation,
} from './ledger.ts'
import {
  parseCommitmentRecoveryEvent,
  type CommitmentRecoveryEvent,
} from './commitment-contract.ts'

/** Called synchronously by the trusted service before any snapshot mutation. */
export function recordCommitmentMutation(
  db: Database.Database,
  change: Readonly<CommitmentMutation>,
): void {
  requireCommitmentReader(db)
  if (change.temporalVersion === 1) requireOperationalReader(db)
  const head = change.head
  revoke(
    db,
    parseCommitmentRecoveryEvent(
      head.deleted || change.kind === 'delete'
        ? {
            kind: 'commitment_delete',
            schemaVersion: 1,
            sceneId: head.sceneId,
            id: head.id,
            deletionEpoch: head.deletionEpoch,
          }
        : {
            kind: 'commitment_head',
            schemaVersion: change.temporalVersion === 1 ? 2 : 1,
            ...(change.temporalVersion === 1 ? { temporalVersion: 1 } : {}),
            ...(change.privacyMarker ? { privacyMarker: change.privacyMarker } : {}),
            sceneId: head.sceneId,
            id: head.id,
            termsVersion: head.latestVersion,
            mutationVersion: head.mutationVersion,
            deletionEpoch: head.deletionEpoch,
            qualificationHash: change.qualificationHash,
          },
    ),
  )
}

/** Offline lifecycle replay followed by exact snapshot qualification; no reconstructed commitments. */
type RecoveryCommitments = Pick<
  CommitmentService,
  | 'snapshot'
  | 'purgeSource'
  | 'purgeAssertion'
  | 'recoverErase'
  | 'requalify'
  | 'recoverTemporalMarker'
>
export function createRecoveryCommitmentPort(store: Store, getService: () => RecoveryCommitments) {
  const known: Record<string, readonly string[]> = {
    memory_commitment_heads: ['id', 'scene_id', 'value'],
    memory_commitment_revisions: ['id', 'version', 'value'],
    memory_commitment_commands: ['key', 'id', 'version'],
    memory_commitment_transitions: ['id', 'mutation_version', 'scene_id', 'value'],
    memory_commitment_temporal_windows: ['id', 'scene_id', 'value'],
  }
  function assertSupported() {
    if (!store.recoveryQuarantined || !isRecoveryMaintenance(store.db))
      throw new Error('recovery_maintenance_required')
    const tables = store.db
      .prepare(
        "SELECT name,type FROM sqlite_master WHERE (type='table' OR type='view') AND name LIKE 'memory_commitment_%'",
      )
      .all() as { name: string; type: string }[]
    for (const row of tables) {
      const expected = known[row.name]
      if (row.type !== 'table' || !expected) throw new Error('recovery_unknown_commitment_schema')
      const columns = (
        store.db.prepare(`PRAGMA table_xinfo("${row.name}")`).all() as { name: string }[]
      )
        .map((c) => c.name)
        .sort()
      if (JSON.stringify(columns) !== JSON.stringify([...expected].sort()))
        throw new Error('recovery_unknown_commitment_schema')
    }
    if (
      tables.length &&
      ((tables.length !== 3 && tables.length !== 5) ||
        ![
          'memory_commitment_heads',
          'memory_commitment_revisions',
          'memory_commitment_commands',
        ].every((name) => tables.some((t) => t.name === name)))
    )
      throw new Error('recovery_unknown_commitment_schema')
    if (tables.length === 5) {
      const temporal = new CommitmentTemporalStore(store.db)
      for (const id of temporal.allIds()) temporal.read(id)
    }
    return tables.length > 0
  }
  let service: RecoveryCommitments | undefined
  function existing() {
    if (!assertSupported()) return undefined
    return (service ??= getService())
  }
  function recall(sceneId: string, messageId: string) {
    const core = existing()
    if (core) store.db.transaction(() => core.purgeSource(sceneId, messageId)).immediate()
  }
  function purgeAssertion(id: string) {
    const core = existing()
    if (core) store.db.transaction(() => core.purgeAssertion(id)).immediate()
  }
  function erase(event: Extract<CommitmentRecoveryEvent, { kind: 'commitment_delete' }>) {
    const core = existing()
    if (core)
      store.db
        .transaction(() => core.recoverErase(event.id, event.sceneId, event.deletionEpoch))
        .immediate()
  }
  function replayMarker(event: Extract<CommitmentRecoveryEvent, { kind: 'commitment_head' }>) {
    if (!event.privacyMarker) return
    const core = existing()
    if (!core) throw new Error('recovery_commitment_material_missing')
    try {
      store.db.transaction(() => core.recoverTemporalMarker(event.privacyMarker!)).immediate()
    } catch (error) {
      if (error instanceof Error && error.message === 'commitment_temporal_marker_unavailable')
        throw new Error('recovery_commitment_material_missing')
      throw error
    }
  }
  function verify(entries: readonly Revocation[]) {
    const core = existing()
    const latest = new Map<string, Extract<CommitmentRecoveryEvent, { kind: 'commitment_head' }>>()
    const deleted = new Map<
      string,
      Extract<CommitmentRecoveryEvent, { kind: 'commitment_delete' }>
    >()
    for (const event of entries) {
      if (event.kind === 'commitment_head') latest.set(event.id, event)
      else if (event.kind === 'commitment_delete') deleted.set(event.id, event)
    }
    for (const event of deleted.values()) {
      const snapshot = core?.snapshot(event.id)
      if (
        snapshot &&
        (snapshot.head.sceneId !== event.sceneId ||
          snapshot.head.deletionEpoch < event.deletionEpoch ||
          !snapshot.head.deleted ||
          snapshot.revisions.length ||
          snapshot.timeline)
      )
        throw new Error('recovery_commitment_purge_incomplete')
      if (
        core &&
        store.db.prepare('SELECT 1 FROM memory_commitment_commands WHERE id=?').get(event.id)
      )
        throw new Error('recovery_commitment_purge_incomplete')
    }
    for (const event of latest.values()) {
      if (deleted.has(event.id)) continue
      const snapshot = core?.snapshot(event.id)
      if (!snapshot) throw new Error('recovery_commitment_material_missing')
      const head = snapshot.head
      if (
        head.deleted ||
        head.sceneId !== event.sceneId ||
        head.latestVersion !== event.termsVersion ||
        head.mutationVersion !== event.mutationVersion ||
        head.deletionEpoch !== event.deletionEpoch ||
        (event.schemaVersion === 2 && !snapshot.timeline) ||
        snapshot.qualificationHash !== event.qualificationHash
      )
        throw new Error('recovery_commitment_head_mismatch')
    }
  }
  return { assertSupported, recall, purgeAssertion, erase, replayMarker, verify }
}
