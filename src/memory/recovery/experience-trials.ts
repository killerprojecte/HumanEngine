import { ExperienceStore } from '../experience/store.ts'
import { sourceKey } from '../experience/outcomes.ts'
import { ExperienceTrials } from '../experience/trials.ts'
import type Database from 'better-sqlite3'
import type { Store } from '../../store.ts'
import { TrialReceiptSchema, trialHash, type TrialMutation } from '../experience/trial-contracts.ts'
import { revoke, requireExperienceTrialReader, type Revocation } from './ledger.ts'
import {
  parseExperienceTrialRecoveryEvent,
  type ExperienceTrialRecoveryEvent,
} from './experience-trial-contract.ts'

export function recordExperienceTrialMutation(db: Database.Database, change: TrialMutation): void {
  requireExperienceTrialReader(db)
  revoke(
    db,
    parseExperienceTrialRecoveryEvent({ kind: 'experience_trial', schemaVersion: 1, ...change }),
  )
}
/** External witnesses contain no positive-feedback or instruction text. Missing evidence is not synthesized. */
export function createRecoveryExperienceTrialsPort(
  store: Pick<Store, 'db' | 'assertRecoveryReady' | 'recalled'>,
) {
  function rows() {
    store.assertRecoveryReady()
    const table = store.db
      .prepare("SELECT type FROM sqlite_master WHERE name='memory_experience_trials'")
      .get() as { type: string } | undefined
    if (!table) return []
    if (table.type !== 'table') throw new Error('recovery_unknown_experience_trial_schema')
    const columns = (
      store.db.prepare('PRAGMA table_xinfo(memory_experience_trials)').all() as { name: string }[]
    )
      .map((r) => r.name)
      .sort()
    if (JSON.stringify(columns) !== JSON.stringify(['policy', 'scene', 'version', 'value'].sort()))
      throw new Error('recovery_unknown_experience_trial_schema')
    const values = store.db
      .prepare('SELECT policy,scene,version,value FROM memory_experience_trials')
      .all() as { policy: string; scene: string; version: number; value: unknown }[]
    if (values.length > 4096) throw new Error('recovery_experience_trial_material_invalid')
    return values.map((r) => {
      if (
        typeof r.policy !== 'string' ||
        !r.policy ||
        r.policy.length > 512 ||
        typeof r.scene !== 'string' ||
        !r.scene ||
        r.scene.length > 512 ||
        !Number.isSafeInteger(r.version) ||
        r.version < 1
      )
        throw new Error('recovery_experience_trial_material_invalid')
      if (r.value === null) return { ...r, receipt: undefined }
      if (typeof r.value !== 'string') throw new Error('recovery_experience_trial_material_invalid')
      const receipt = TrialReceiptSchema.parse(JSON.parse(r.value))
      if (
        receipt.policyId !== r.policy ||
        receipt.sceneId !== r.scene ||
        receipt.version !== r.version
      )
        throw new Error('recovery_experience_trial_material_invalid')
      return { ...r, receipt }
    })
  }
  const alreadyLatest = new Set<string>()
  function prepare(events: readonly Revocation[]) {
    const values = rows(),
      latest = new Map<string, ExperienceTrialRecoveryEvent>()
    for (const e of events) if (e.kind === 'experience_trial') latest.set(e.policyId, e)
    for (const e of latest.values()) {
      const row = values.find((r) => r.policy === e.policyId && r.scene === e.sceneId)
      if (
        row &&
        (e.afterHash === null
          ? !row.receipt && row.version === e.expectedReceiptVersion! + 1
          : row.receipt &&
            row.version === e.nextReceiptVersion &&
            trialHash(row.receipt) === e.afterHash)
      )
        alreadyLatest.add(e.policyId)
    }
  }
  function apply(event: ExperienceTrialRecoveryEvent) {
    if (alreadyLatest.has(event.policyId)) return
    const row = rows().find((r) => r.policy === event.policyId && r.scene === event.sceneId)
    if (
      row &&
      (event.afterHash === null
        ? !row.receipt && row.version === event.expectedReceiptVersion! + 1
        : row.receipt &&
          row.version === event.nextReceiptVersion &&
          trialHash(row.receipt) === event.afterHash)
    )
      return
    store.db
      .transaction(() => {
        new ExperienceTrials(store.db).recoverMutation(event)
        // Live eraseTrial removes its positive observations before the policy deletion.
        // Retain the exact pre-erase receipt here: the tombstone no longer carries these IDs.
        if (event.operation === 'erase' && row?.receipt) {
          const material = new ExperienceStore(store.db)
          for (const item of row.receipt.positives) {
            const outcome = material.outcome(item.outcomeId)
            if (!outcome) continue
            if (
              outcome.sceneId !== event.sceneId ||
              outcome.policyRef?.id !== event.policyId ||
              outcome.version !== item.outcomeVersion
            )
              throw new Error('recovery_experience_trial_evidence_missing')
            material.tombstone(outcome.rootId)
            material.eraseOutcome(outcome.id)
          }
        }
      })
      .immediate()
  }
  function verifyMaterial() {
    const values = rows()
    if (!values.some((r) => r.receipt)) return
    const material = new ExperienceStore(store.db)
    for (const row of values) {
      const t = row.receipt
      if (!t) continue
      const policy = material.policy(t.policyId),
        instruction = material.outcome(t.instruction.id),
        validation = material.receipt(t.validationId)
      const active = t.phase === 'trial' || t.phase === 'promoted'
      if (
        !policy ||
        policy.sceneId !== t.sceneId ||
        policy.version < t.policyVersion ||
        (active &&
          (policy.version !== t.policyVersion ||
            policy.hash !== t.policyHash ||
            policy.status !== 'validated')) ||
        !instruction ||
        instruction.version !== t.instruction.version ||
        instruction.sceneId !== t.sceneId ||
        instruction.source.senderId !== t.requesterId ||
        !instruction.snapshotBound ||
        store.recalled(t.sceneId, instruction.source.messageId) ||
        store.recalled(t.sceneId, instruction.target.messageId) ||
        instruction.code !==
          (policy.action === 'exact_person_before_semantic'
            ? 'request_exact_person_lookup'
            : 'request_verify_plan_qualifiers') ||
        !policy.supportIds.includes(instruction.id) ||
        !validation ||
        !validation.passed ||
        validation.tier !== 'mechanical_only' ||
        validation.candidateId !== policy.id ||
        validation.candidateHash !== t.policyHash ||
        validation.candidateVersion !== t.evidencePolicyVersion - 1 ||
        validation.ruleVersion !== 'experience-mechanical-v2' ||
        validation.datasetVersion !== 'experience-fixed-behavior-holdout-v2'
      )
        throw new Error('recovery_experience_trial_evidence_missing')
      const operation =
        policy.action === 'exact_person_before_semantic'
          ? 'memory_person'
          : policy.action === 'verify_plan_qualifiers'
            ? 'plan_qualifiers_checked'
            : undefined
      if (!operation) throw new Error('recovery_experience_trial_evidence_missing')
      for (const item of t.positives) {
        const o = material.outcome(item.outcomeId)
        if (
          !o ||
          o.version !== item.outcomeVersion ||
          o.sceneId !== t.sceneId ||
          o.code !== 'user_reported_helpful' ||
          !o.snapshotBound ||
          store.recalled(t.sceneId, o.source.messageId) ||
          store.recalled(t.sceneId, o.target.messageId) ||
          o.source.senderId !== t.requesterId ||
          o.policyRef?.id !== t.policyId ||
          o.policyRef.version !== t.evidencePolicyVersion ||
          sourceKey(o.sceneId, o.source.messageId) !== item.sourceRoot ||
          sourceKey(o.sceneId, o.target.messageId) !== item.targetRoot ||
          !material
            .uses(t.sceneId, o.target.messageId)
            .some(
              (u) =>
                u.kind === 'applied' &&
                u.operation === operation &&
                u.policyRef.id === t.policyId &&
                u.policyRef.version === t.evidencePolicyVersion &&
                u.turnId === item.turnId,
            )
        )
          throw new Error('recovery_experience_trial_evidence_missing')
      }
    }
  }
  function verify(events: readonly Revocation[]) {
    verifyMaterial()
    const values = rows(),
      latest = new Map<string, ExperienceTrialRecoveryEvent>()
    for (const e of events) if (e.kind === 'experience_trial') latest.set(e.policyId, e)
    for (const e of latest.values()) {
      const row = values.find((r) => r.policy === e.policyId && r.scene === e.sceneId)
      if (!row) throw new Error('recovery_experience_trial_material_missing')
      if (e.afterHash === null) {
        if (row.receipt || row.version !== e.expectedReceiptVersion! + 1)
          throw new Error('recovery_experience_trial_delete_incomplete')
      } else if (
        !row.receipt ||
        row.version !== e.nextReceiptVersion ||
        trialHash(row.receipt) !== e.afterHash
      )
        throw new Error('recovery_experience_trial_head_mismatch')
    }
  }
  return { assertSupported: rows, prepare, apply, verify }
}
