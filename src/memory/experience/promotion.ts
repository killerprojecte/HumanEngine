import { validMechanicalReceipt } from './candidates.ts'
import type {
  ExperienceOutcome,
  ExperiencePolicyCandidate,
  TrustedPolicyUse,
  MechanicalReceipt,
} from '../domain/experience.ts'
import { experienceCandidateHash } from '../domain/experience.ts'
import { sourceKey } from './outcomes.ts'
import { ExperienceTrials } from './trials.ts'
import type { TrialReceipt } from './trial-contracts.ts'
export interface TrialPolicyPorts {
  settings(): { enabled: boolean; hints: boolean; feedbackTrials?: boolean; revision?: number }
  learningEnabled(scene: string): boolean
  now(): number
  outcome(id: string): ExperienceOutcome | undefined
  outcomeAvailable(outcome: ExperienceOutcome): boolean
  receipt(id: string): MechanicalReceipt | undefined
  uses(scene: string, target: string): TrustedPolicyUse[]
  putOutcome(outcome: ExperienceOutcome): void
  putPolicy(next: ExperiencePolicyCandidate, expected: number): void
}
/** Fixed low-risk trials. Two reports are an independence gate, not a benefit estimator. */
export class ExperiencePromotion {
  constructor(
    readonly trials: ExperienceTrials,
    private readonly ports: TrialPolicyPorts,
  ) {}
  private flags(scene: string) {
    const f = this.ports.settings()
    return (
      f.enabled &&
      f.hints &&
      f.feedbackTrials === true &&
      Number.isSafeInteger(f.revision) &&
      f.revision! >= 0 &&
      this.ports.learningEnabled(scene)
    )
  }
  current(policy: ExperiencePolicyCandidate): TrialReceipt | undefined {
    const trial = this.trials.get(policy.id)
    if (
      !trial ||
      !this.flags(policy.sceneId) ||
      !['trial', 'promoted'].includes(trial.phase) ||
      trial.sceneId !== policy.sceneId ||
      trial.policyVersion !== policy.version ||
      trial.policyHash !== policy.hash ||
      experienceCandidateHash(policy) !== policy.hash ||
      trial.settingsRevision !== this.ports.settings().revision ||
      Date.parse(trial.expiresAt) <= this.ports.now() ||
      Date.parse(policy.expiresAt) <= this.ports.now() ||
      policy.status !== 'validated' ||
      policy.counterexampleIds.length
    )
      return undefined
    const instruction = this.ports.outcome(trial.instruction.id),
      validation = this.ports.receipt(trial.validationId)
    if (
      !instruction ||
      instruction.snapshotBound !== true ||
      instruction.version !== trial.instruction.version ||
      instruction.source.senderId !== trial.requesterId ||
      instruction.sceneId !== policy.sceneId ||
      !this.ports.outcomeAvailable(instruction) ||
      !policy.supportIds.includes(instruction.id) ||
      !validation ||
      !validation.passed ||
      validation.ruleVersion !== 'experience-mechanical-v2' ||
      validation.datasetVersion !== 'experience-fixed-behavior-holdout-v2' ||
      !validMechanicalReceipt(
        { ...policy, version: trial.evidencePolicyVersion - 1 },
        validation,
      ) ||
      instruction.code !==
        (policy.action === 'exact_person_before_semantic'
          ? 'request_exact_person_lookup'
          : 'request_verify_plan_qualifiers') ||
      validation.candidateHash !== policy.hash ||
      validation.candidateVersion !== trial.evidencePolicyVersion - 1
    )
      return undefined
    for (const item of trial.positives) {
      const o = this.ports.outcome(item.outcomeId)
      if (
        !o ||
        o.version !== item.outcomeVersion ||
        o.code !== 'user_reported_helpful' ||
        o.snapshotBound !== true ||
        o.sceneId !== policy.sceneId ||
        o.source.senderId !== trial.requesterId ||
        o.policyRef?.id !== policy.id ||
        o.policyRef.version !== trial.evidencePolicyVersion ||
        !this.ports.outcomeAvailable(o) ||
        sourceKey(o.sceneId, o.source.messageId) !== item.sourceRoot ||
        sourceKey(o.sceneId, o.target.messageId) !== item.targetRoot ||
        !this.applied(policy, o, trial.evidencePolicyVersion, item.turnId)
      )
        return undefined
    }
    return trial
  }
  private applied(
    policy: ExperiencePolicyCandidate,
    outcome: ExperienceOutcome,
    version: number,
    turnId?: string,
  ) {
    const operation =
      policy.action === 'exact_person_before_semantic'
        ? 'memory_person'
        : policy.action === 'verify_plan_qualifiers'
          ? 'plan_qualifiers_checked'
          : undefined
    if (!operation) return undefined
    return this.ports
      .uses(policy.sceneId, outcome.target.messageId)
      .find(
        (u) =>
          u.kind === 'applied' &&
          u.operation === operation &&
          u.policyRef.id === policy.id &&
          u.policyRef.version === version &&
          (!turnId || u.turnId === turnId),
      )
  }
  start(policy: ExperiencePolicyCandidate, instruction: ExperienceOutcome) {
    if (
      !this.flags(policy.sceneId) ||
      policy.status !== 'validated' ||
      !policy.validationId ||
      policy.action === 'conclusion_before_detail'
    )
      return undefined
    const now = this.ports.now()
    const validation = this.ports.receipt(policy.validationId)
    if (
      !validation ||
      validation.ruleVersion !== 'experience-mechanical-v2' ||
      validation.datasetVersion !== 'experience-fixed-behavior-holdout-v2' ||
      !validMechanicalReceipt({ ...policy, version: policy.version - 1 }, validation)
    )
      throw new Error('experience_trial_validation_required')
    if (
      !this.ports.outcomeAvailable(instruction) ||
      instruction.snapshotBound !== true ||
      Date.parse(policy.expiresAt) <= now
    )
      throw new Error('experience_trial_ineligible')
    const next: TrialReceipt = {
      schemaVersion: 1,
      policyId: policy.id,
      sceneId: policy.sceneId,
      version: 1,
      policyVersion: policy.version,
      evidencePolicyVersion: policy.version,
      policyHash: policy.hash,
      requesterId: instruction.source.senderId,
      settingsRevision: this.ports.settings().revision!,
      phase: 'trial',
      createdAt: new Date(now).toISOString(),
      expiresAt: new Date(Math.min(now + 7 * 86400000, Date.parse(policy.expiresAt))).toISOString(),
      instruction: { id: instruction.id, version: instruction.version },
      validationId: policy.validationId,
      positives: [],
      reason: 'explicit_instruction_trial',
    }
    this.trials.write(next, undefined, 'start')
    return next
  }
  collect(policy: ExperiencePolicyCandidate, outcome: ExperienceOutcome) {
    const previous = this.current(policy)
    if (
      !previous ||
      previous.phase !== 'trial' ||
      outcome.policyRef?.id !== policy.id ||
      outcome.policyRef.version !== policy.version ||
      outcome.code !== 'user_reported_helpful' ||
      outcome.source.senderId !== previous.requesterId ||
      !this.ports.outcomeAvailable(outcome) ||
      outcome.snapshotBound !== true
    )
      throw new Error('experience_feedback_trial_ineligible')
    const use = this.applied(policy, outcome, policy.version)
    if (!use) throw new Error('experience_unverified_policy_use')
    const item = {
      outcomeId: outcome.id,
      outcomeVersion: outcome.version,
      sourceRoot: sourceKey(outcome.sceneId, outcome.source.messageId),
      targetRoot: sourceKey(outcome.sceneId, outcome.target.messageId),
      turnId: use.turnId,
    }
    if (
      item.sourceRoot ===
        sourceKey(outcome.sceneId, this.ports.outcome(previous.instruction.id)!.source.messageId) ||
      previous.positives.some(
        (p) =>
          p.sourceRoot === item.sourceRoot ||
          p.targetRoot === item.targetRoot ||
          p.turnId === item.turnId,
      )
    )
      throw new Error('experience_feedback_not_independent')
    const promote = previous.positives.length === 1
    const nextPolicy = promote ? { ...policy, version: policy.version + 1 } : policy
    const next: TrialReceipt = {
      ...previous,
      version: previous.version + 1,
      policyVersion: nextPolicy.version,
      phase: promote ? 'promoted' : 'trial',
      positives: [...previous.positives, item],
      reason: promote ? 'user_reported_helpfulness' : 'explicit_instruction_trial',
    }
    this.trials.write(next, previous, promote ? 'promote' : 'collect', () => {
      this.ports.putOutcome(outcome)
      if (promote) this.ports.putPolicy(nextPolicy, policy.version)
    })
    return nextPolicy
  }
  suspend(
    policy: ExperiencePolicyCandidate,
    reason:
      'negative_feedback' | 'instruction_withdrawn' | 'qualification_changed' = 'negative_feedback',
  ) {
    const previous = this.trials.get(policy.id)
    if (previous && ['trial', 'promoted'].includes(previous.phase))
      this.trials.write(
        {
          ...previous,
          version: previous.version + 1,
          phase: 'suspended',
          reason,
        },
        previous,
        'suspend',
      )
  }
  expire(policy: ExperiencePolicyCandidate) {
    const previous = this.trials.get(policy.id)
    if (
      previous &&
      ['trial', 'promoted'].includes(previous.phase) &&
      Date.parse(previous.expiresAt) <= this.ports.now()
    )
      this.trials.write(
        { ...previous, version: previous.version + 1, phase: 'expired', reason: 'expiry' },
        previous,
        'expire',
      )
  }
}
