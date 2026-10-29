import { replayExternalCosts } from './external-cost.ts'
import { createRecoveryExperienceTrialsPort } from './experience-trials.ts'
import { createRecoverySemanticPort } from './semantic.ts'
import { createRecoveryKnowledgePort } from './knowledge.ts'
import { createRecoveryIdentityPort } from './identities.ts'
import { createRecoveryWorkingSetPort } from './working-set.ts'
import { createRecoveryCommitmentPort } from './commitments.ts'
import { createRecoverySourceEventPort } from './source-events.ts'
import { markRestoredModelHold } from './model-hold.ts'
import { assertSecurityState } from './security-state.ts'
import { sceneIdentityHash } from './configuration-contract.ts'
import { MemoryService } from '../../memory.ts'
import type { Store } from '../../store.ts'
import {
  metadata,
  saveGate,
  sameHead,
  captureRecoveryEffects,
  type RecoveryAuthority,
  type Revocation,
} from './ledger.ts'
import { createRecoveryDomainPorts } from './domain-ports.ts'

/** Offline-only, exact-source replay. The staging DB stays quarantined across filesystem awaits. */
export async function reconcileRecovery(
  store: Store,
  authority: RecoveryAuthority,
  mediaRoot: string,
) {
  if (!store.recoveryQuarantined) throw new Error('recovery_maintenance_required')
  const initial = authority.head(),
    gate = metadata(store.db)
  if (!gate || gate.head.instanceId !== initial.instanceId || gate.head.sequence > initial.sequence)
    throw new Error('recovery_instance_or_head_mismatch')
  if (!sameHead(gate.head, authority.prefix(gate.head.sequence)))
    throw new Error('recovery_prefix_invalid')
  const entries = authority.entries()
  assertSecurityState(
    store.db,
    entries.map((e) => e.revocation).findLast((e) => e.kind === 'security_state'),
  )
  replayExternalCosts(
    store.db,
    entries.map((entry) => entry.revocation),
  )
  const workingSet = createRecoveryWorkingSetPort(store, {
    resolvePinTarget: (policy) => memory.workspace.resolvePinTarget(policy),
  })
  workingSet.assertSupported()
  const sourceEvents = createRecoverySourceEventPort(store)
  sourceEvents.assertSupported()
  const commitments = createRecoveryCommitmentPort(store, () => memory.commitments)
  commitments.assertSupported()
  const trials = createRecoveryExperienceTrialsPort(store)
  trials.assertSupported()
  trials.prepare(entries.map((entry) => entry.revocation))
  const domains = createRecoveryDomainPorts(store, mediaRoot)
  domains.assertSupported()
  const identities = createRecoveryIdentityPort(store)
  identities.replay(entries, gate.head.sequence)
  const semantic = createRecoverySemanticPort(store, () => memory.semantic)
  semantic.assertSupported()
  semantic.prepare(entries.map((entry) => entry.revocation))
  const knowledge = createRecoveryKnowledgePort(store)
  knowledge.assertSupported()
  const memory = new MemoryService(store)
  const affected = new Set<string>()
  const redactionReceipts = new Map<string, number>()
  const receiptKey = (effect: Extract<Revocation, { kind: 'assertion' }>) =>
    JSON.stringify([effect.kind, effect.id, effect.throughVersion, effect.terminal])
  const replayMemory = (apply: () => unknown) => {
    for (const effect of captureRecoveryEffects(store.db, apply)) {
      if (effect.kind !== 'assertion' || effect.terminal) continue
      const key = receiptKey(effect)
      redactionReceipts.set(key, (redactionReceipts.get(key) ?? 0) + 1)
    }
  }

  const appliedDomains: Parameters<typeof domains.apply>[0][] = []
  for (const { sequence, revocation } of entries) {
    if (!sameHead(initial, authority.head())) throw new Error('recovery_authority_head_changed')
    if (
      revocation.kind === 'external_cost_receipt' ||
      revocation.kind === 'identity_mutation' ||
      revocation.kind === 'working_set_pin' ||
      revocation.kind === 'security_state' ||
      revocation.kind === 'source_edit_head'
    )
      continue
    if (revocation.kind === 'commitment_head') {
      if (sequence > gate.head.sequence) commitments.replayMarker(revocation)
      continue
    }
    if (revocation.kind === 'experience_trial') {
      if (sequence > gate.head.sequence) trials.apply(revocation)
      continue
    }
    if (revocation.kind === 'semantic_mutation') {
      if (sequence > gate.head.sequence) semantic.apply(revocation)
      continue
    }
    if (revocation.kind === 'commitment_delete') {
      commitments.erase(revocation)
      continue
    }
    if (revocation.kind === 'configuration_flags') {
      if (sequence > gate.head.sequence)
        store.set('settings', { ...store.settings, ...revocation.flags })
    } else if (revocation.kind === 'scene_flags') {
      if (sequence > gate.head.sequence) {
        const scene = store.scene(revocation.sceneId)
        if (!scene || sceneIdentityHash(scene) !== revocation.identityHash)
          throw new Error('recovery_scene_identity_mismatch')
        store.saveScene({ ...scene, ...revocation.flags })
      }
    } else if (revocation.kind === 'source') {
      redactionReceipts.clear()
      replayMemory(() => memory.recall(revocation.sceneId, revocation.messageId))
      sourceEvents.recall(revocation.sceneId, revocation.messageId)
      commitments.recall(revocation.sceneId, revocation.messageId)
      await domains.apply(revocation)
      appliedDomains.push(revocation)
    } else if (revocation.kind === 'fingerprint') {
      replayMemory(() =>
        store.db
          .transaction(() => {
            const rows = store.db
              .prepare('SELECT id FROM memories WHERE fingerprint=?')
              .all(revocation.value) as { id: string }[]
            for (const row of rows) {
              memory.delete(row.id)
              commitments.purgeAssertion(row.id)
            }
            store.db
              .prepare('INSERT OR IGNORE INTO memory_deleted VALUES(?,?)')
              .run(revocation.value, new Date().toISOString())
          })
          .immediate(),
      )
    } else if (revocation.kind === 'assertion') {
      if (!revocation.terminal && sequence <= gate.head.sequence) continue
      if (revocation.terminal) {
        commitments.purgeAssertion(revocation.id)
        affected.add(revocation.id)
        replayMemory(() => memory.eraseRecoveredAssertion(revocation.id))
      } else {
        const key = receiptKey(revocation),
          count = redactionReceipts.get(key) ?? 0
        if (count > 0) {
          // Consume only this precise already-executed obligation, not a version-range waiver.
          if (count === 1) redactionReceipts.delete(key)
          else redactionReceipts.set(key, count - 1)
          continue
        }
        redactionReceipts.clear()
        replayMemory(() => memory.redactRecoveredHistory(revocation.id))
      }
    } else if (sequence > gate.head.sequence) {
      // Domain transitions may be followed by legitimate later reviews/re-admission.
      // Replay only the unapplied ordered suffix, never undo a verified applied prefix.
      await domains.apply(revocation)
      appliedDomains.push(revocation)
    }
  }
  // No local transaction spans a file unlink. Ready is written only after every effect and check.
  store.db
    .transaction(() => {
      for (const { revocation } of entries) {
        if (
          revocation.kind === 'assertion' &&
          affected.has(revocation.id) &&
          store.db.prepare('SELECT 1 FROM memories WHERE id=?').get(revocation.id)
        )
          throw new Error('recovery_purge_incomplete')
        if (
          revocation.kind === 'source' &&
          store.db
            .prepare('SELECT 1 FROM memory_sources WHERE scene_id=? AND message_id=?')
            .get(revocation.sceneId, revocation.messageId)
        )
          throw new Error('recovery_purge_incomplete')
      }
      trials.verify(entries.map((entry) => entry.revocation))
      semantic.verify(entries.map((entry) => entry.revocation))
      knowledge.verify(entries.map((entry) => entry.revocation))
      identities.verify(entries.map((entry) => entry.revocation))
      domains.verify(appliedDomains)
      sourceEvents.verify(entries.map((entry) => entry.revocation))
      commitments.verify(entries.map((entry) => entry.revocation))
      if (!sameHead(initial, authority.head())) throw new Error('recovery_authority_head_changed')
      workingSet.verifyPins(entries.map((entry) => entry.revocation))
      workingSet.reset()
      markRestoredModelHold(store.db)
      saveGate(store.db, {
        ...gate,
        minimumReader: Math.max(gate.minimumReader, metadata(store.db)!.minimumReader),
        authority: authority.path,
        head: initial,
        state: 'ready',
      })
    })
    .immediate()
  return initial
}
