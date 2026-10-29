import { isDeepStrictEqual } from 'node:util'
import { assertDomainReady, isRecoveryMaintenance } from '../recovery/ledger.ts'
import type { ExperienceDatabase } from './store.ts'
import {
  TrialReceiptSchema,
  trialHash,
  type TrialReceipt,
  type TrialMutation,
} from './trial-contracts.ts'
/** Persistent authorization receipts, never outcome scores or an executable policy language. */
export class ExperienceTrials {
  constructor(
    private readonly db: ExperienceDatabase,
    private readonly beforeWrite?: (mutation: TrialMutation) => void,
  ) {}
  private exists() {
    assertDomainReady(this.db)
    return !!this.db
      .prepare("SELECT 1 FROM sqlite_master WHERE type='table' AND name='memory_experience_trials'")
      .get()
  }
  private decode(row: unknown): TrialReceipt | undefined {
    if (!row) return undefined
    const r = row as { policy: string; scene: string; version: number; value: unknown }
    if (
      typeof r.policy !== 'string' ||
      !r.policy ||
      typeof r.scene !== 'string' ||
      !r.scene ||
      !Number.isSafeInteger(r.version) ||
      r.version < 1
    )
      throw new Error('experience_trial_corrupt')
    if (r.value === null) return undefined
    if (typeof r.value !== 'string') throw new Error('experience_trial_corrupt')
    const value = TrialReceiptSchema.parse(JSON.parse(r.value))
    if (value.policyId !== r.policy || value.sceneId !== r.scene || value.version !== r.version)
      throw new Error('experience_trial_corrupt')
    return value
  }
  get(policy: string) {
    return this.exists()
      ? this.decode(
          this.db.prepare('SELECT * FROM memory_experience_trials WHERE policy=?').get(policy),
        )
      : undefined
  }
  rows(scene?: string): TrialReceipt[] {
    if (!this.exists()) return []
    const rows = scene
      ? this.db.prepare('SELECT * FROM memory_experience_trials WHERE scene=?').all(scene)
      : this.db.prepare('SELECT * FROM memory_experience_trials').all()
    if (rows.length > 4096) throw new Error('experience_trial_capacity')
    return rows.map((row) => this.decode(row)).filter((row): row is TrialReceipt => !!row)
  }
  seen(policy: string) {
    return (
      this.exists() &&
      !!this.db.prepare('SELECT 1 FROM memory_experience_trials WHERE policy=?').get(policy)
    )
  }
  recoverMutation(change: TrialMutation) {
    if (!isRecoveryMaintenance(this.db)) throw new Error('recovery_maintenance_required')
    const previous = this.get(change.policyId)
    if (previous && trialHash(previous) === change.afterHash) return
    if (!previous && change.afterHash === null) return
    if (
      !previous ||
      previous.sceneId !== change.sceneId ||
      previous.version !== change.expectedReceiptVersion ||
      trialHash(previous) !== change.beforeHash
    )
      throw new Error('recovery_experience_trial_material_missing')
    if (change.operation === 'erase') {
      this.write(undefined, previous, 'erase')
      return
    }
    if (change.operation !== 'suspend' && change.operation !== 'expire')
      throw new Error('recovery_experience_trial_material_missing')
    const next = TrialReceiptSchema.parse({
      ...previous,
      version: change.nextReceiptVersion,
      phase: change.nextPhase,
      reason: change.nextReason,
    })
    if (trialHash(next) !== change.afterHash)
      throw new Error('recovery_experience_trial_material_missing')
    this.write(next, previous, change.operation)
  }
  write(
    next: TrialReceipt | undefined,
    previous: TrialReceipt | undefined,
    operation: TrialMutation['operation'],
    applyPolicy?: () => void,
  ) {
    assertDomainReady(this.db)
    if (!this.db.inTransaction) throw new Error('experience_transaction_required')
    if (next) next = TrialReceiptSchema.parse(next)
    const identity = next ?? previous
    if (!identity) throw new Error('experience_trial_missing')
    const stored = this.get(identity.policyId)
    if ((stored ? trialHash(stored) : null) !== (previous ? trialHash(previous) : null))
      throw new Error('experience_trial_version_conflict')
    if (
      next &&
      previous &&
      (next.version !== previous.version + 1 ||
        next.sceneId !== previous.sceneId ||
        next.policyId !== previous.policyId)
    )
      throw new Error('experience_trial_version_conflict')
    if (next && previous) {
      for (const key of [
        'instruction',
        'validationId',
        'policyHash',
        'requesterId',
        'settingsRevision',
        'evidencePolicyVersion',
        'createdAt',
        'expiresAt',
      ] as const)
        if (!isDeepStrictEqual(next[key], previous[key]))
          throw new Error('experience_trial_immutable')
      if (next.policyVersion !== previous.policyVersion + (operation === 'promote' ? 1 : 0))
        throw new Error('experience_trial_version_conflict')
    }
    if (
      (operation === 'start') !== !previous ||
      (operation === 'erase') !== !next ||
      (next &&
        next.phase !==
          (
            {
              start: 'trial',
              collect: 'trial',
              promote: 'promoted',
              suspend: 'suspended',
              expire: 'expired',
            } as const
          )[operation as Exclude<TrialMutation['operation'], 'erase'>])
    )
      throw new Error('experience_trial_transition_invalid')
    if (next && !previous && next.version !== 1)
      throw new Error('experience_trial_version_conflict')
    if (next && !previous) {
      if (this.seen(next.policyId)) throw new Error('experience_trial_consumed')
      const count = this.exists()
        ? (
            this.db.prepare('SELECT count(*) AS n FROM memory_experience_trials').get() as {
              n: number
            }
          ).n
        : 0
      if (count >= 4096) throw new Error('experience_trial_capacity')
    }
    const result: unknown = this.beforeWrite?.({
      operation,
      policyId: identity.policyId,
      sceneId: identity.sceneId,
      expectedPolicyVersion: previous?.policyVersion ?? identity.policyVersion,
      expectedReceiptVersion: previous?.version ?? null,
      nextReceiptVersion: next?.version ?? null,
      nextPolicyVersion: next?.policyVersion ?? identity.policyVersion,
      beforeHash: previous ? trialHash(previous) : null,
      afterHash: next ? trialHash(next) : null,
      ...(next
        ? { nextPhase: next.phase, ...(next.reason ? { nextReason: next.reason } : {}) }
        : {}),
    })
    if (result && typeof (result as PromiseLike<unknown>).then === 'function')
      throw new Error('experience_trial_async_hook')
    this.db.exec(
      'CREATE TABLE IF NOT EXISTS memory_experience_trials(policy TEXT PRIMARY KEY,scene TEXT NOT NULL,version INTEGER NOT NULL,value TEXT)',
    )
    const applied: unknown = applyPolicy?.()
    if (applied && typeof (applied as PromiseLike<unknown>).then === 'function')
      throw new Error('experience_trial_async_write')
    if (previous)
      this.db
        .prepare('UPDATE memory_experience_trials SET version=?,value=? WHERE policy=?')
        .run(
          next?.version ?? previous.version + 1,
          next ? JSON.stringify(next) : null,
          identity.policyId,
        )
    else
      this.db
        .prepare('INSERT INTO memory_experience_trials VALUES(?,?,?,?)')
        .run(identity.policyId, identity.sceneId, identity.version, JSON.stringify(next))
  }
}
