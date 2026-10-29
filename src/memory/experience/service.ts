import { recordExperienceTrialMutation } from '../recovery/experience-trials.ts'
import { ExperienceTrials } from './trials.ts'
import { ExperiencePromotion } from './promotion.ts'
import type { TrialMutation } from './trial-contracts.ts'
import type { SourceEventRef } from '../domain/events.ts'
import { SourceEventStore } from '../storage/event-revisions.ts'
import type Database from 'better-sqlite3'
import { assertDomainReady, revoke, isRecoveryMaintenance } from '../recovery/ledger.ts'
import { qualificationHash, type DomainRecoveryEvent } from '../recovery/domain-contracts.ts'
import { randomUUID } from 'node:crypto'
import type {
  ExperienceContext,
  ExperienceHint,
  ExperienceOutcome,
  ExperiencePolicyCandidate,
  ExperiencePolicyProposal,
  ExperienceReference,
  MechanicalReceipt,
  TrustedFeedback,
  TrustedPolicyUse,
} from '../domain/experience.ts'
import { experienceCandidateHash } from '../domain/experience.ts'
import { candidateFromOutcome, validMechanicalReceipt } from './candidates.ts'
import { createOutcome, experienceKey, sourceKey } from './outcomes.ts'
import { ExperienceStore, type ExperienceDatabase } from './store.ts'
export interface ExperiencePorts {
  settings(): {
    enabled: boolean
    autoActivate: boolean
    hints: boolean
    feedbackTrials?: boolean
    revision?: number
  }
  beforeTrialWrite?: (mutation: TrialMutation) => void
  learningEnabled(scene: string): boolean
  sceneAvailable(scene: string): boolean
  admissionSourceAvailable(scene: string, message: string, sender?: string): boolean
  targetAvailable(scene: string, message: string): boolean
  sourceRevoked(scene: string, message: string): boolean
  sourceCurrent?(scene: string, message: string, sender?: string): boolean
  validate(candidate: ExperiencePolicyCandidate): MechanicalReceipt
  now?: () => number
  id?: () => string
}
/** Trusted runtime adapter boundary, not an unconstrained model tool. */
export class ExperienceService {
  readonly store: ExperienceStore
  readonly trials: ExperienceTrials
  private readonly promotion: ExperiencePromotion
  private readonly now: () => number
  private readonly id: () => string
  private readonly sourceEvents: SourceEventStore
  constructor(
    db: ExperienceDatabase,
    private readonly ports: ExperiencePorts,
  ) {
    this.store = new ExperienceStore(db)
    this.sourceEvents = new SourceEventStore(db, { assertReady: () => assertDomainReady(db) })
    this.now = ports.now ?? Date.now
    this.id = ports.id ?? randomUUID
    this.trials = new ExperienceTrials(
      db,
      ports.beforeTrialWrite ??
        ((mutation) => recordExperienceTrialMutation(db as Database.Database, mutation)),
    )
    this.promotion = new ExperiencePromotion(this.trials, {
      settings: () => ports.settings(),
      learningEnabled: (s) => ports.learningEnabled(s),
      now: () => this.now(),
      outcome: (id) => this.store.outcome(id),
      outcomeAvailable: (o) => this.outcomeAvailable(o),
      receipt: (id) => this.store.receipt(id),
      uses: (s, t) => this.store.uses(s, t),
      putOutcome: (o) => this.store.putOutcome(o),
      putPolicy: (p, v) => this.store.putPolicy(p, v),
    })
  }
  private enabled(ctx: ExperienceContext) {
    assertDomainReady(this.store.db)
    return (
      !ctx.simulation && this.ports.sceneAvailable(ctx.sceneId) && this.ports.settings().enabled
    )
  }
  private learning(ctx: ExperienceContext) {
    return this.enabled(ctx) && this.ports.learningEnabled(ctx.sceneId)
  }
  private sourceCurrent(scene: string, message: string, sender?: string): boolean {
    if (
      this.ports.sourceRevoked(scene, message) ||
      this.ports.sourceCurrent?.(scene, message, sender) === false
    )
      return false
    const registered = this.sourceEvents.registered(scene, message)
    if (!registered) return true
    if (sender !== undefined && registered.identity.senderId !== sender) return false
    // This first domain integration has no revision-bound feedback protocol: edited
    // feedback/targets require a new independent message, never a latest-ref guess.
    return !this.sourceEvents.hasEdits(registered.identity)
  }
  private snapshotCurrent(
    scene: string,
    message: string,
    sender: string | undefined,
    ref: SourceEventRef | undefined,
  ): boolean {
    if (
      this.ports.sourceRevoked(scene, message) ||
      this.ports.sourceCurrent?.(scene, message, sender) === false
    )
      return false
    const registered = this.sourceEvents.registered(scene, message)
    if (!registered) return ref === undefined
    if (!ref || (sender !== undefined && registered.identity.senderId !== sender)) return false
    return this.sourceEvents.current(ref, registered.identity).eligible
  }
  private outcomeAvailable(outcome: ExperienceOutcome, current = true) {
    return (
      !this.store.tombstoned(sourceKey(outcome.sceneId, outcome.source.messageId)) &&
      !this.ports.sourceRevoked(outcome.sceneId, outcome.source.messageId) &&
      !this.ports.sourceRevoked(outcome.sceneId, outcome.target.messageId) &&
      (!current ||
        (outcome.snapshotBound
          ? this.snapshotCurrent(
              outcome.sceneId,
              outcome.source.messageId,
              outcome.source.senderId,
              outcome.source.eventRef,
            ) &&
            this.snapshotCurrent(
              outcome.sceneId,
              outcome.target.messageId,
              undefined,
              outcome.target.eventRef,
            )
          : this.sourceCurrent(
              outcome.sceneId,
              outcome.source.messageId,
              outcome.source.senderId,
            ) && this.sourceCurrent(outcome.sceneId, outcome.target.messageId)))
    )
  }
  private supported(policy: ExperiencePolicyCandidate, current = true) {
    return (
      policy.supportIds.length > 0 &&
      policy.supportIds.every((id) => {
        const outcome = this.store.outcome(id)
        return outcome?.sceneId === policy.sceneId && this.outcomeAvailable(outcome, current)
      })
    )
  }
  private get(ctx: ExperienceContext, id: string, version?: number) {
    const policy = this.store.policy(id)
    if (!this.enabled(ctx) || !policy || policy.sceneId !== ctx.sceneId)
      throw new Error('experience_unavailable')
    if (version !== undefined && (!Number.isSafeInteger(version) || policy.version !== version))
      throw new Error('experience_version_conflict')
    return policy
  }
  private save(policy: ExperiencePolicyCandidate, patch: Partial<ExperiencePolicyCandidate>) {
    const next = { ...policy, ...patch, version: policy.version + 1 }
    next.hash = experienceCandidateHash(next)
    if (['active', 'suspended', 'retired'].includes(next.status))
      revoke(this.store.db as Database.Database, this.recoveryState(next))
    this.store.putPolicy(next, policy.version)
    return next
  }
  recordFeedback(ctx: ExperienceContext, input: TrustedFeedback) {
    if (!this.learning(ctx)) return undefined
    return this.store.transaction(() => {
      if (
        !this.ports.admissionSourceAvailable(
          ctx.sceneId,
          input.source.messageId,
          input.source.senderId,
        ) ||
        !this.ports.targetAvailable(ctx.sceneId, input.target.messageId) ||
        !this.sourceCurrent(ctx.sceneId, input.source.messageId, input.source.senderId) ||
        !this.sourceCurrent(ctx.sceneId, input.target.messageId)
      )
        throw new Error('experience_feedback_not_authorized')
      const outcome = createOutcome(ctx, input, this.id())
      const operationTrial =
        input.code === 'user_reported_helpful' ||
        (this.ports.settings().feedbackTrials === true &&
          ['request_exact_person_lookup', 'request_verify_plan_qualifiers'].includes(input.code))
      if (operationTrial) {
        if (
          !this.ports.settings().feedbackTrials ||
          !this.snapshotCurrent(
            ctx.sceneId,
            input.source.messageId,
            input.source.senderId,
            input.source.eventRef,
          ) ||
          !this.snapshotCurrent(
            ctx.sceneId,
            input.target.messageId,
            undefined,
            input.target.eventRef,
          )
        )
          throw new Error('experience_feedback_snapshot_stale')
        outcome.snapshotBound = true
      }
      if (
        this.store.tombstoned(sourceKey(ctx.sceneId, input.source.messageId)) ||
        this.store.tombstoned(outcome.rootId)
      )
        throw new Error('experience_source_revoked')
      const previous = this.store.root(outcome.rootId)
      if (previous) {
        if (
          previous.code !== input.code ||
          JSON.stringify(previous.policyRef) !== JSON.stringify(input.policyRef)
        )
          throw new Error('experience_root_conflict')
        return { outcome: previous, candidate: this.findCandidate(previous), duplicate: true }
      }
      if (input.code === 'user_reported_helpful') {
        const policy = this.get(ctx, input.policyRef!.id, input.policyRef!.version)
        const candidate = this.promotion.collect(policy, outcome)
        return { outcome, candidate, duplicate: false }
      }
      const withdrawal = [
        'withdraw_instruction',
        'withdraw_person_instruction',
        'withdraw_plan_instruction',
      ].includes(input.code)
      if (input.code === 'counterexample' || withdrawal) {
        const ref = input.policyRef!
        const policy = this.get(ctx, ref.id, ref.version)
        const expectedWithdrawal =
          input.code === 'withdraw_person_instruction'
            ? 'exact_person_before_semantic'
            : input.code === 'withdraw_plan_instruction'
              ? 'verify_plan_qualifiers'
              : 'conclusion_before_detail'
        if (withdrawal && policy.action !== expectedWithdrawal)
          throw new Error('experience_instruction_mismatch')
        const use = this.store
          .uses(ctx.sceneId, input.target.messageId)
          .find(
            (use) =>
              (withdrawal ? use.kind === 'exposed' : use.kind === 'applied') &&
              use.policyRef.id === ref.id &&
              use.policyRef.version === ref.version,
          )
        if (!use) throw new Error('experience_unverified_policy_use')
        const trial = this.trials.get(policy.id)
        if (withdrawal && input.code !== 'withdraw_instruction' && !trial)
          throw new Error('experience_instruction_mismatch')
        if (trial) {
          if (
            !this.snapshotCurrent(
              ctx.sceneId,
              input.source.messageId,
              input.source.senderId,
              input.source.eventRef,
            ) ||
            !this.snapshotCurrent(
              ctx.sceneId,
              input.target.messageId,
              undefined,
              input.target.eventRef,
            )
          )
            throw new Error('experience_feedback_snapshot_stale')
          outcome.snapshotBound = true
        }
        if (
          (trial && trial.requesterId !== input.source.senderId) ||
          (policy.condition.kind !== 'plan_question' &&
            policy.condition.personId !== input.source.senderId)
        )
          throw new Error('experience_feedback_person_mismatch')
        this.promotion.suspend(policy, withdrawal ? 'instruction_withdrawn' : 'negative_feedback')
        this.store.putOutcome(outcome)
        const candidate = this.save(policy, {
          status: 'suspended',
          reason: withdrawal ? 'explicit_instruction_withdrawn' : 'explicit_counterexample',
          counterexampleIds: [...new Set([...policy.counterexampleIds, outcome.id])],
        })
        return { outcome, candidate, duplicate: false }
      }
      this.store.putOutcome(outcome)
      let candidate = candidateFromOutcome(outcome, this.id(), this.now())
      this.store.putPolicy(candidate)
      const flags = this.ports.settings()
      if (candidate.action === 'conclusion_before_detail' && flags.autoActivate && flags.hints) {
        candidate = this.validate(ctx, candidate.id, candidate.version)
        if (candidate.status === 'validated')
          candidate = this.review(
            ctx,
            candidate.id,
            candidate.version,
            'active',
            'explicit_preference',
          )
      }
      if (candidate.action !== 'conclusion_before_detail' && flags.feedbackTrials && flags.hints) {
        candidate = this.validate(ctx, candidate.id, candidate.version)
        this.promotion.start(candidate, outcome)
      }
      return { outcome, candidate, duplicate: false }
    })
  }
  private findCandidate(outcome: ExperienceOutcome) {
    return this.store.policiesForOutcome(outcome.sceneId, outcome.id)[0]
  }
  listOutcomes(ctx: ExperienceContext, limit = 20): ExperienceOutcome[] {
    if (!this.learning(ctx)) return []
    if (!Number.isSafeInteger(limit) || limit < 1 || limit > 20)
      throw new Error('experience_invalid_limit')
    return this.store
      .outcomes(ctx.sceneId, limit + 1)
      .filter((outcome) => this.outcomeAvailable(outcome))
      .slice(0, limit)
  }
  proposePolicy(
    ctx: ExperienceContext,
    proposal: ExperiencePolicyProposal,
    exposed: readonly ExperienceReference[],
  ) {
    if (!this.learning(ctx)) throw new Error('experience_learning_disabled')
    return this.store.transaction(() => {
      if (proposal.outcomeRefs.length !== 1) throw new Error('experience_invalid_support')
      const ref = proposal.outcomeRefs[0]!
      if (!exposed.some((item) => item.id === ref.id && item.version === ref.version))
        throw new Error('experience_outcome_not_exposed')
      const outcome = this.store.outcome(ref.id)
      if (
        !outcome ||
        outcome.version !== ref.version ||
        outcome.sceneId !== ctx.sceneId ||
        !this.outcomeAvailable(outcome)
      )
        throw new Error('experience_outcome_unavailable')
      const candidate = candidateFromOutcome(outcome, this.id(), this.now())
      if (
        candidate.action !== proposal.action ||
        JSON.stringify(candidate.condition) !== JSON.stringify(proposal.condition)
      )
        throw new Error('experience_instruction_mismatch')
      const previous = this.findCandidate(outcome)
      if (previous) return { candidate: previous, duplicate: true }
      this.store.putPolicy(candidate)
      return { candidate, duplicate: false }
    })
  }
  validate(ctx: ExperienceContext, id: string, expectedVersion: number) {
    return this.store.transaction(() => {
      const candidate = this.get(ctx, id, expectedVersion)
      if (
        !['candidate', 'suspended'].includes(candidate.status) ||
        !this.supported(candidate) ||
        Date.parse(candidate.expiresAt) <= this.now() ||
        candidate.counterexampleIds.length
      )
        throw new Error('experience_validation_ineligible')
      const receipt = this.ports.validate(structuredClone(candidate))
      if (!validMechanicalReceipt(candidate, receipt))
        throw new Error('experience_validation_failed')
      const validationId = this.id()
      this.store.putReceipt(validationId, receipt)
      return this.save(candidate, { status: 'validated', validationId, reason: 'mechanical_only' })
    })
  }
  review(
    ctx: ExperienceContext,
    id: string,
    expectedVersion: number,
    status: 'active' | 'suspended' | 'retired',
    basis: 'explicit_preference' | 'administrator_review' = 'administrator_review',
  ) {
    return this.store.transaction(() => {
      const candidate = this.get(ctx, id, expectedVersion)
      if (!['active', 'suspended', 'retired'].includes(status) || candidate.status === 'retired')
        throw new Error('experience_invalid_transition')
      if (status === 'active') {
        if (
          basis === 'explicit_preference' &&
          (!this.learning(ctx) ||
            !this.ports.settings().autoActivate ||
            !this.ports.settings().hints ||
            candidate.action !== 'conclusion_before_detail')
        )
          throw new Error('experience_activation_ineligible')
        const receipt = candidate.validationId
          ? this.store.receipt(candidate.validationId)
          : undefined
        if (
          candidate.status !== 'validated' ||
          !receipt ||
          receipt.candidateVersion !== candidate.version - 1 ||
          receipt.candidateHash !== candidate.hash ||
          !this.supported(candidate) ||
          Date.parse(candidate.expiresAt) <= this.now()
        )
          throw new Error('experience_activation_ineligible')
      }
      this.promotion.suspend(candidate, 'qualification_changed')
      return this.save(candidate, {
        status,
        ...(status === 'active' ? { activationBasis: basis } : {}),
        reason: status === 'active' ? 'experimental_mechanical_hint' : `review_${status}`,
      })
    })
  }
  list(
    ctx: ExperienceContext,
    options: { limit?: number; status?: ExperiencePolicyCandidate['status'] } = {},
  ) {
    const limit = options.limit ?? 40
    if (!Number.isSafeInteger(limit) || limit < 1 || limit > 40)
      throw new Error('experience_invalid_limit')
    if (!this.enabled(ctx)) return { items: [], coverage: 'complete' as const }
    return this.store.transaction(() => {
      const rows = this.store.policies(ctx.sceneId)
      for (const policy of rows.slice(0, 200)) this.promotion.expire(policy)
      const items = rows
        .slice(0, 200)
        .map((policy) =>
          policy.status === 'active' &&
          (!this.supported(policy, false) || Date.parse(policy.expiresAt) <= this.now())
            ? this.save(policy, { status: 'suspended', reason: 'support_or_expiry_invalid' })
            : policy,
        )
        .filter((policy) => !options.status || policy.status === options.status)
      return {
        items: items.slice(0, limit),
        coverage:
          rows.length > 200 || items.length > limit ? ('limited' as const) : ('complete' as const),
      }
    })
  }
  hints(
    ctx: ExperienceContext,
    query: {
      personId?: string
      requesterId?: string
      personQuestion?: boolean
      planQuestion?: boolean
    } = {},
  ): ExperienceHint[] {
    if (!this.enabled(ctx) || !this.ports.settings().hints) return []
    return this.list(ctx)
      .items.filter(
        (policy) =>
          this.supported(policy) &&
          (policy.status === 'active' || !!this.promotion.current(policy)),
      )
      .filter(
        (policy) =>
          policy.status === 'active' ||
          (query.requesterId !== undefined &&
            this.promotion.current(policy)?.requesterId === query.requesterId),
      )
      .filter((policy) =>
        policy.condition.kind === 'plan_question'
          ? query.planQuestion
          : policy.condition.personId === query.personId &&
            (policy.condition.kind === 'response_style' || query.personQuestion),
      )
      .slice(0, 3)
      .map((policy) => ({
        id: policy.id,
        version: policy.version,
        hash: policy.hash,
        action: policy.action,
        condition: policy.condition,
        expiresAt: policy.expiresAt,
        tier: 'mechanical_only' as const,
        ...this.trialHint(policy),
      }))
  }
  private trialHint(policy: ExperiencePolicyCandidate) {
    if (policy.status === 'active') return {}
    const trial = this.promotion.current(policy)
    return trial
      ? {
          eligibilityBasis:
            trial.phase === 'promoted'
              ? ('user_reported_support' as const)
              : ('explicit_instruction_trial' as const),
          feedbackRevision: trial.settingsRevision,
          trialVersion: trial.version,
          trialExpiresAt: trial.expiresAt,
          reportedHelpfulCount: trial.positives.length,
        }
      : {}
  }
  snapshotValid(
    ctx: ExperienceContext,
    ref: ExperienceReference & { hash: string; feedbackRevision?: number; trialVersion?: number },
  ) {
    if (!this.enabled(ctx) || !this.ports.settings().hints) return false
    const policy = this.store.policy(ref.id)
    return (
      !!policy &&
      policy.sceneId === ctx.sceneId &&
      (policy.status === 'active' || !!this.promotion.current(policy)) &&
      (ref.feedbackRevision === undefined ||
        (this.ports.settings().feedbackTrials === true &&
          ref.feedbackRevision === this.ports.settings().revision)) &&
      (ref.trialVersion === undefined ||
        this.promotion.current(policy)?.version === ref.trialVersion) &&
      policy.version === ref.version &&
      policy.hash === ref.hash &&
      this.supported(policy) &&
      Date.parse(policy.expiresAt) > this.now()
    )
  }
  recordUse(ctx: ExperienceContext, use: TrustedPolicyUse) {
    if (!this.enabled(ctx) || !this.ports.settings().hints) return false
    return this.store.transaction(() => {
      const policy = this.get(ctx, use.policyRef.id, use.policyRef.version)
      if (!this.snapshotValid(ctx, { ...use.policyRef, hash: policy.hash })) return false
      const expected =
        policy.action === 'exact_person_before_semantic'
          ? 'memory_person'
          : policy.action === 'verify_plan_qualifiers'
            ? 'plan_qualifiers_checked'
            : 'conclusion_first_response'
      if (
        !use.turnId ||
        !['exposed', 'applied', 'declined'].includes(use.kind) ||
        (use.kind === 'applied' && use.operation !== expected) ||
        (use.targetMessageId && !this.ports.targetAvailable(ctx.sceneId, use.targetMessageId))
      )
        throw new Error('experience_invalid_use')
      this.store.putUse(
        experienceKey(
          ctx.sceneId,
          use.turnId,
          policy.id,
          String(policy.version),
          use.kind,
          use.targetMessageId ?? '',
        ),
        ctx.sceneId,
        use,
      )
      return true
    })
  }
  targetUses(ctx: ExperienceContext, targetMessageId: string): TrustedPolicyUse[] {
    if (!this.enabled(ctx)) return []
    return this.store
      .uses(ctx.sceneId, targetMessageId)
      .filter((use) => {
        const policy = this.store.policy(use.policyRef.id)
        return (
          !!policy &&
          policy.sceneId === ctx.sceneId &&
          policy.status !== 'retired' &&
          this.supported(policy) &&
          Date.parse(policy.expiresAt) > this.now()
        )
      })
      .slice(0, 20)
  }
  adminList(ctx: ExperienceContext, options: Parameters<ExperienceService['list']>[1] = {}) {
    const result = this.list(ctx, options)
    return {
      ...result,
      items: result.items.map((policy) => ({
        ...policy,
        expired: Date.parse(policy.expiresAt) <= this.now(),
        trial: this.trials.get(policy.id),
        trialEligible: !!this.promotion.current(policy),
        sources: policy.supportIds
          .map((id) => this.store.outcome(id))
          .filter((item): item is ExperienceOutcome => !!item && item.sceneId === ctx.sceneId),
        validation: policy.validationId ? this.store.receipt(policy.validationId) : undefined,
        uses: this.store.useCounts(ctx.sceneId, policy.id),
      })),
    }
  }
  private recoveryQualification(policy: ExperiencePolicyCandidate): string {
    return qualificationHash({
      id: policy.id,
      sceneId: policy.sceneId,
      hash: policy.hash,
      supports: policy.supportIds.map((id) => this.store.outcome(id)),
      counterexamples: policy.counterexampleIds.map((id) => this.store.outcome(id)),
      validation: policy.validationId ? this.store.receipt(policy.validationId) : null,
    })
  }
  private recoveryState(
    policy: ExperiencePolicyCandidate,
  ): Extract<DomainRecoveryEvent, { kind: 'experience_state' }> {
    if (!['active', 'suspended', 'retired'].includes(policy.status))
      throw new Error('recovery_experience_state_invalid')
    return {
      schemaVersion: 1,
      kind: 'experience_state',
      sceneId: policy.sceneId,
      id: policy.id,
      qualificationHash: this.recoveryQualification(policy),
      version: policy.version,
      status: policy.status as 'active' | 'suspended' | 'retired',
      ...(policy.validationId ? { validationId: policy.validationId } : {}),
      ...(policy.activationBasis ? { activationBasis: policy.activationBasis } : {}),
      reason: policy.reason as Extract<DomainRecoveryEvent, { kind: 'experience_state' }>['reason'],
    }
  }
  recoverState(event: Extract<DomainRecoveryEvent, { kind: 'experience_state' }>): void {
    if (!isRecoveryMaintenance(this.store.db)) throw new Error('recovery_maintenance_required')
    const policy = this.store.policy(event.id)
    if (!policy || policy.sceneId !== event.sceneId || policy.version > event.version)
      throw new Error('recovery_experience_qualification_missing')
    const next = {
      ...policy,
      status: event.status,
      version: event.version,
      validationId: event.validationId,
      activationBasis: event.activationBasis,
      reason: event.reason,
    }
    if (this.recoveryQualification(next) !== event.qualificationHash)
      throw new Error('recovery_experience_qualification_missing')
    if (
      next.status === 'active' &&
      (!this.supported(next) || !next.validationId || !this.store.receipt(next.validationId))
    )
      throw new Error('recovery_experience_grant_invalid')
    this.store.putPolicy(next, policy.version)
  }
  recoverDelete(sceneId: string, id: string): void {
    if (!isRecoveryMaintenance(this.store.db)) throw new Error('recovery_maintenance_required')
    const policy = this.store.policy(id)
    if (policy && policy.sceneId !== sceneId) throw new Error('recovery_experience_scope_mismatch')
    if (policy) {
      for (const outcomeId of [...policy.supportIds, ...policy.counterexampleIds]) {
        const outcome = this.store.outcome(outcomeId)
        if (outcome) {
          this.store.tombstone(outcome.rootId)
          this.store.eraseOutcome(outcomeId)
        }
      }
      this.erase(policy)
    } else this.store.tombstone(experienceKey('policy', id))
  }
  revokeSource(scene: string, message: string) {
    this.store.transaction(() => {
      this.store.tombstone(sourceKey(scene, message))
      for (const outcome of this.store.sourceOutcomes(scene, message)) {
        for (const trial of this.trials.rows(scene))
          if (trial.positives.some((p) => p.outcomeId === outcome.id))
            this.eraseTrial(trial.policyId)
        for (const policy of this.store.policiesForOutcome(scene, outcome.id))
          if (
            policy.supportIds.includes(outcome.id) ||
            policy.counterexampleIds.includes(outcome.id)
          )
            this.erase(policy)
        this.store.tombstone(outcome.rootId)
        this.store.eraseOutcome(outcome.id)
      }
    })
  }
  private eraseTrial(id: string) {
    const trial = this.trials.get(id)
    if (!trial) return
    this.trials.write(undefined, trial, 'erase', () => {
      for (const item of trial.positives) {
        const o = this.store.outcome(item.outcomeId)
        if (o) {
          this.store.tombstone(o.rootId)
          this.store.eraseOutcome(o.id)
        }
      }
    })
  }
  private erase(policy: ExperiencePolicyCandidate) {
    this.eraseTrial(policy.id)
    revoke(this.store.db as Database.Database, {
      schemaVersion: 1,
      kind: 'experience_delete',
      sceneId: policy.sceneId,
      id: policy.id,
    })
    this.store.tombstone(experienceKey('policy', policy.id))
    this.store.erasePolicy(policy.id)
  }
  delete(ctx: ExperienceContext, id: string, expectedVersion: number) {
    this.store.transaction(() => {
      const policy = this.get(ctx, id, expectedVersion)
      for (const outcomeId of [...policy.supportIds, ...policy.counterexampleIds]) {
        const outcome = this.store.outcome(outcomeId)
        if (outcome) {
          this.store.tombstone(outcome.rootId)
          this.store.eraseOutcome(outcomeId)
        }
      }
      this.erase(policy)
    })
  }
}
