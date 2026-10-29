import type { SemanticService } from '../semantic/candidates.ts'
import type Database from 'better-sqlite3'
import type { Store } from '../../store.ts'
import { SemanticStorage } from '../semantic/storage.ts'
import { semanticHash, type SemanticMutation } from '../semantic/contracts.ts'
import { revoke, requireSemanticReader, type Revocation } from './ledger.ts'
import { parseSemanticRecoveryEvent, type SemanticRecoveryEvent } from './semantic-contract.ts'

export function recordSemanticMutation(db: Database.Database, change: SemanticMutation): void {
  requireSemanticReader(db)
  revoke(db, parseSemanticRecoveryEvent({ kind: 'semantic_mutation', schemaVersion: 1, ...change }))
}
/** Review/attempt witnesses qualify trusted snapshots; hashes cannot reconstruct missing material. */
export function createRecoverySemanticPort(
  store: Store,
  getService: () => Pick<SemanticService, 'review' | 'lifecycle'>,
) {
  const storage = new SemanticStorage(store.db, () => store.assertRecoveryReady())
  const known: Record<string, readonly string[]> = {
    memory_semantic_candidates: ['id', 'scene_id', 'input_key', 'version', 'value'],
    memory_semantic_attempts: ['scene_id', 'input_key', 'value'],
  }
  function assertSupported() {
    const tables = store.db
      .prepare(
        "SELECT name,type FROM sqlite_master WHERE (type='table' OR type='view') AND name LIKE 'memory_semantic_%'",
      )
      .all() as { name: string; type: string }[]
    for (const table of tables) {
      const expected = known[table.name]
      if (table.type !== 'table' || !expected) throw new Error('recovery_unknown_semantic_schema')
      const columns = (
        store.db.prepare(`PRAGMA table_xinfo("${table.name}")`).all() as { name: string }[]
      )
        .map((c) => c.name)
        .sort()
      if (JSON.stringify(columns) !== JSON.stringify([...expected].sort()))
        throw new Error('recovery_unknown_semantic_schema')
    }
    try {
      storage.rows()
      storage.attempts()
    } catch {
      throw new Error('recovery_semantic_material_invalid')
    }
  }
  const alreadyLatest = new Set<string>()
  function prepare(events: readonly Revocation[]) {
    const latest = new Map<string, SemanticRecoveryEvent>()
    for (const event of events)
      if (event.kind === 'semantic_mutation' && event.operation !== 'attempt')
        latest.set(event.id, event)
    for (const event of latest.values()) {
      const current = storage.get(event.id)
      if (
        current &&
        current.sceneId === event.sceneId &&
        current.version === event.nextVersion &&
        semanticHash(current) === event.afterHash
      )
        alreadyLatest.add(event.id)
    }
  }
  function apply(event: SemanticRecoveryEvent) {
    if (alreadyLatest.has(event.id)) return
    if (event.operation === 'privacy' && event.afterHash === null) {
      const current = storage.get(event.id)
      if (!current) return // Final tombstone verification still requires exact metadata.
      if (
        current.sceneId !== event.sceneId ||
        current.version !== event.previousVersion ||
        semanticHash(current) !== event.beforeHash
      )
        throw new Error('recovery_semantic_delete_material_missing')
      store.db
        .transaction(() =>
          getService().lifecycle.erase({ sceneId: event.sceneId, simulation: false }, event.id),
        )
        .immediate()
      return
    }
    if (event.operation !== 'review') return
    const current = storage.get(event.id)
    if (
      !current ||
      current.sceneId !== event.sceneId ||
      current.version !== event.previousVersion ||
      semanticHash(current) !== event.beforeHash
    )
      throw new Error('recovery_semantic_review_material_missing')
    store.db
      .transaction(() => {
        const status = event.nextState?.status
        if (status !== 'admitted' && status !== 'rejected')
          throw new Error('recovery_semantic_event_invalid')
        const next = getService().review(
          { sceneId: event.sceneId, simulation: false },
          event.id,
          event.previousVersion!,
          status,
        )
        if (next.version !== event.nextVersion || semanticHash(next) !== event.afterHash)
          throw new Error('recovery_semantic_head_mismatch')
      })
      .immediate()
  }
  function verify(events: readonly Revocation[]) {
    assertSupported()
    const latest = new Map<string, SemanticRecoveryEvent>()
    for (const e of events)
      if (e.kind === 'semantic_mutation')
        latest.set(
          JSON.stringify([e.operation === 'attempt' ? 'attempt' : 'candidate', e.sceneId, e.id]),
          e,
        )
    const attempts = storage.attempts()
    for (const event of latest.values()) {
      if (event.operation === 'attempt') {
        const attempt = attempts.find((a) => a.sceneId === event.sceneId && a.inputKey === event.id)
        if (!attempt) throw new Error('recovery_semantic_attempt_material_missing')
        if (attempt.version !== event.nextVersion || semanticHash(attempt) !== event.afterHash)
          throw new Error('recovery_semantic_attempt_mismatch')
      } else if (event.afterHash === null) {
        const exists = store.db
          .prepare(
            "SELECT 1 FROM sqlite_master WHERE type='table' AND name='memory_semantic_candidates'",
          )
          .get()
        const row = exists
          ? (store.db
              .prepare('SELECT scene_id,version,value FROM memory_semantic_candidates WHERE id=?')
              .get(event.id) as { scene_id: string; version: number; value: unknown } | undefined)
          : undefined
        if (
          !row ||
          row.scene_id !== event.sceneId ||
          row.version !== event.previousVersion! + 1 ||
          row.value !== null
        )
          throw new Error('recovery_semantic_delete_incomplete')
      } else {
        const candidate = storage.get(event.id)
        if (!candidate) throw new Error('recovery_semantic_material_missing')
        if (
          candidate.sceneId !== event.sceneId ||
          candidate.version !== event.nextVersion ||
          semanticHash(candidate) !== event.afterHash
        )
          throw new Error('recovery_semantic_head_mismatch')
      }
    }
    const deleted = new Set(
      events
        .filter(
          (e): e is Extract<Revocation, { kind: 'assertion' }> =>
            e.kind === 'assertion' && e.terminal,
        )
        .map((e) => e.id),
    )
    for (const candidate of storage.rows())
      for (const parent of [...candidate.parents, ...candidate.anchorDependencies]) {
        if (
          deleted.has(parent.ref.id) ||
          parent.proofs.some(
            (p) =>
              p.parents.some((id) => deleted.has(id)) ||
              p.sources.some((s) => store.recalled(s.sceneId, s.messageId)),
          )
        )
          throw new Error('recovery_semantic_privacy_incomplete')
      }
  }
  return { assertSupported, prepare, apply, verify }
}
