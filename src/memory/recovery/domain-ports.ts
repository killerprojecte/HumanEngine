import { HabitService } from '../../habits.ts'
import { MemeService } from '../../memes.ts'
import type { Store } from '../../store.ts'
import { ExperienceService } from '../experience/service.ts'
import { isRecoveryMaintenance, type Revocation } from './ledger.ts'
import { parseDomainRecoveryEvent, qualificationHash } from './domain-contracts.ts'

/** Offline maintenance only. Replays existing authoritative lifecycle methods, never learning. */
export function createRecoveryDomainPorts(store: Store, mediaRoot: string) {
  const assertSupported = () => {
    if (!store.recoveryQuarantined || !isRecoveryMaintenance(store.db))
      throw new Error('recovery_maintenance_required')
    const known: Record<string, readonly string[]> = {
      habits: ['id', 'fingerprint', 'value'],
      habit_deleted: ['fingerprint'],
      meme_assets: ['id', 'value'],
      meme_reference_denials: ['key'],
      meme_sources: ['asset_id', 'scene_id', 'message_id', 'sender_id'],
      meme_media_references: [
        'scene_id',
        'reference_hash',
        'asset_id',
        'message_id',
        'sender_id',
        'content_hash',
        'updated_at',
      ],
      meme_feedback: ['asset_id', 'scene_id', 'message_id', 'positive'],
      meme_search_concepts: ['asset_id', 'concept'],
      memory_experience_outcomes: ['id', 'root', 'scene', 'source', 'value'],
      memory_experience_policies: ['id', 'scene', 'version', 'value'],
      memory_experience_validations: ['id', 'policy', 'value'],
      memory_experience_uses: ['root', 'scene', 'policy', 'target', 'value'],
      memory_experience_tombstones: ['key'],
      memory_experience_trials: ['policy', 'scene', 'version', 'value'],
    }
    const tables = store.db.prepare("SELECT name FROM sqlite_master WHERE type='table'").all() as {
      name: string
    }[]
    for (const { name } of tables) {
      if (!(
        name === 'habits' ||
        name.startsWith('habit_') ||
        name.startsWith('meme_') ||
        name.startsWith('experience_') ||
        name.startsWith('memory_experience_')
      ))
        continue
      const expected = known[name]
      if (!expected) throw new Error('recovery_unknown_domain_schema')
      const columns = (store.db.prepare(`PRAGMA table_info("${name}")`).all() as { name: string }[])
        .map((column) => column.name)
        .sort()
      if (JSON.stringify(columns) !== JSON.stringify([...expected].sort()))
        throw new Error('recovery_unknown_domain_schema')
    }
  }
  assertSupported()
  const habits = new HabitService(store),
    memes = new MemeService(store, mediaRoot)
  const experience = new ExperienceService(store.db, {
    settings: () => ({ enabled: false, autoActivate: false, hints: false }),
    learningEnabled: () => false,
    sceneAvailable: (scene) => !!store.scene(scene),
    admissionSourceAvailable: () => false,
    targetAvailable: () => false,
    sourceRevoked: (scene, message) => store.recalled(scene, message),
    validate: () => {
      throw new Error('recovery_does_not_validate')
    },
  })
  const applied: Revocation[] = []
  const removedPolicies = new Set<string>()
  const expectedMedia = new Map<string, string>()
  const policyKey = (scene: string, id: string) => JSON.stringify([scene, id])
  const captureMedia = (id: string) =>
    expectedMedia.set(id, qualificationHash(memes.adminGet(id) ?? null))
  const apply = async (event: Revocation) => {
    assertSupported()
    if (event.kind === 'source') {
      const affectedMedia = (
        store.db
          .prepare(
            'SELECT asset_id FROM meme_sources WHERE scene_id=? AND message_id=? UNION SELECT asset_id FROM meme_feedback WHERE scene_id=? AND message_id=?',
          )
          .all(event.sceneId, event.messageId, event.sceneId, event.messageId) as {
          asset_id: string
        }[]
      ).map((row) => row.asset_id)
      for (const outcome of experience.store.sourceOutcomes(event.sceneId, event.messageId))
        for (const policy of experience.store.policiesForOutcome(event.sceneId, outcome.id))
          removedPolicies.add(policyKey(policy.sceneId, policy.id))
      store.db.transaction(() => {
        habits.recall(event)
        memes.recall(event.sceneId, event.messageId)
        experience.revokeSource(event.sceneId, event.messageId)
      })()
      for (const id of affectedMedia) captureMedia(id)
      applied.push(event)
      return
    }
    if (event.kind === 'assertion' || event.kind === 'fingerprint') return
    const value = parseDomainRecoveryEvent(event)
    switch (value.kind) {
      case 'habit_delete':
        store.db.transaction(() => habits.recoverDelete(value.id, value.fingerprint))()
        break
      case 'meme_delete':
        await memes.recoverDelete(value)
        break
      case 'meme_state':
        store.db.transaction(() => memes.recoverState(value))()
        break
      case 'experience_delete':
        store.db.transaction(() => experience.recoverDelete(value.sceneId, value.id))()
        break
      case 'experience_state':
        store.db.transaction(() => experience.recoverState(value))()
        break
    }
    if (value.kind === 'meme_state' || value.kind === 'meme_delete') captureMedia(value.id)
    applied.push(value)
  }
  const verify = (_events: readonly Revocation[]) => {
    assertSupported()
    // Verify only the final operation for a subject: later authorized state supersedes earlier state.
    const last = new Map<string, Revocation>()
    for (const event of applied) {
      if (event.kind === 'source') {
        for (const table of ['meme_sources', 'meme_feedback', 'meme_media_references'])
          if (
            store.db
              .prepare(`SELECT 1 FROM ${table} WHERE scene_id=? AND message_id=? LIMIT 1`)
              .get(event.sceneId, event.messageId)
          )
            throw new Error('recovery_domain_source_remaining')
        if (experience.store.sourceOutcomes(event.sceneId, event.messageId).length)
          throw new Error('recovery_domain_source_remaining')
        if (
          habits
            .adminList()
            .some((habit) =>
              habit.sources.some(
                (source) =>
                  source.sceneId === event.sceneId && source.messageId === event.messageId,
              ),
            )
        )
          throw new Error('recovery_domain_source_remaining')
      } else if (
        event.kind.startsWith('meme_') ||
        event.kind.startsWith('experience_') ||
        event.kind === 'habit_delete'
      )
        last.set(
          JSON.stringify([
            event.kind.split('_')[0],
            'sceneId' in event ? event.sceneId : null,
            'id' in event ? event.id : null,
          ]),
          event,
        )
    }
    for (const event of last.values()) {
      if (
        event.kind === 'habit_delete' &&
        habits.adminList().some((habit) => habit.id === event.id)
      )
        throw new Error('recovery_domain_delete_incomplete')
      if (event.kind === 'meme_delete' && memes.adminGet(event.id))
        throw new Error('recovery_domain_delete_incomplete')
      if (event.kind === 'experience_delete' && experience.store.policy(event.id))
        throw new Error('recovery_domain_delete_incomplete')
      if (
        event.kind === 'experience_state' &&
        !removedPolicies.has(policyKey(event.sceneId, event.id))
      ) {
        const policy = experience.store.policy(event.id)
        if (!policy || policy.version !== event.version || policy.status !== event.status)
          throw new Error('recovery_domain_state_incomplete')
      }
      if (
        event.kind === 'meme_state' &&
        qualificationHash(memes.adminGet(event.id) ?? null) !== expectedMedia.get(event.id)
      )
        throw new Error('recovery_domain_state_incomplete')
    }
  }
  return { assertSupported, apply, verify }
}
