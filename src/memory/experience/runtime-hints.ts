import { isDeepStrictEqual } from 'node:util'
import { z } from '@hono/zod-openapi'
import type { RuntimeMessage, TurnContext } from '../../runtime.ts'
import type {
  ExperienceHint,
  ExperienceOutcome,
  ExperienceReference,
} from '../domain/experience.ts'
import type { ExperienceService } from './service.ts'
import { deriveExplicitPolicyFeedback } from './feedback-adapter.ts'
import { renderExperienceAction } from './policy-actions.ts'
import { appliedExperienceOperation } from './operation-eligibility.ts'

const reference = z
  .object({ id: z.string().min(1).max(512), version: z.number().int().positive() })
  .strict()
export const ExperienceProposalSchema = z
  .object({
    action: z.enum([
      'exact_person_before_semantic',
      'verify_plan_qualifiers',
      'conclusion_before_detail',
    ]),
    condition: z.discriminatedUnion('kind', [
      z
        .object({ kind: z.literal('person_question'), personId: z.string().min(1).max(512) })
        .strict(),
      z.object({ kind: z.literal('plan_question') }).strict(),
      z
        .object({ kind: z.literal('response_style'), personId: z.string().min(1).max(512) })
        .strict(),
    ]),
    outcomeRefs: z.array(reference).length(1),
  })
  .strict()
const scope = (context: TurnContext) => ({
  sceneId: context.scene.id,
  simulation: context.mode === 'simulation',
})
interface TurnHints {
  sceneId: string
  trialLearningEpoch: number
  selected: ExperienceHint[]
  semanticRead: boolean
  applied: Set<string>
  at: number
}
type OutcomeView = Pick<
  ExperienceOutcome,
  'id' | 'version' | 'source' | 'target' | 'code' | 'observedAt' | 'policyRef'
>
const outcomeView = (outcome: ExperienceOutcome): OutcomeView => ({
  id: outcome.id,
  version: outcome.version,
  source: structuredClone(outcome.source),
  target: {
    messageId: outcome.target.messageId,
    ...(outcome.target.eventRef ? { eventRef: structuredClone(outcome.target.eventRef) } : {}),
  },
  code: outcome.code,
  observedAt: outcome.observedAt,
  ...(outcome.policyRef ? { policyRef: { ...outcome.policyRef } } : {}),
})
/** Trusted feedback and observed tool use remain separate from model self-assessment. */
export class ExperienceRuntime {
  private readonly turns = new Map<string, TurnHints>()
  private readonly trialLearningEpochs = new Map<string, number>()
  private readonly pending = new WeakMap<TurnContext, WeakMap<object, ExperienceOutcome>>()
  private readonly exposed = new WeakMap<TurnContext, ExperienceReference[]>()
  constructor(
    private readonly service: ExperienceService,
    private readonly messages: (sceneId: string) => RuntimeMessage[],
  ) {}
  /** Pause invalidates prior trial use, not the retained instruction or fresh resumed turns. */
  learningPaused(sceneId: string) {
    this.trialLearningEpochs.set(sceneId, (this.trialLearningEpochs.get(sceneId) ?? 0) + 1)
  }
  received(message: RuntimeMessage) {
    const ctx = { sceneId: message.sceneId, simulation: message.origin === 'simulation' }
    const target = this.messages(message.sceneId).find((row) => row.id === message.replyToId)
    if (!target) return
    const uses = this.service.targetUses(ctx, target.id)
    const selected = uses.filter((use) => use.kind === 'exposed')
    const applied = uses.filter(
      (use) =>
        use.kind === 'applied' &&
        (use.operation === 'memory_person' || use.operation === 'plan_qualifiers_checked'),
    )
    // Ambiguous target mappings are not a license to withdraw a guessed policy.
    const policy = selected.length === 1 ? selected[0]!.policyRef : undefined
    const feedback = deriveExplicitPolicyFeedback(
      message.sceneId,
      message,
      target,
      policy,
      applied.length === 1
        ? {
            policyRef: applied[0]!.policyRef,
            operation: applied[0]!.operation as 'memory_person' | 'plan_qualifiers_checked',
          }
        : undefined,
    )
    if (feedback) this.service.recordFeedback(ctx, feedback)
  }
  plannerContext(context: TurnContext, messages: readonly RuntimeMessage[]) {
    if (context.mode !== 'real' || context.signal.aborted) return undefined
    const latest = messages.findLast(
      (message) => message.status === 'available' && message.senderId !== 'bot',
    )
    if (!latest) return undefined
    const text = latest.parts
      .filter((part) => part.type === 'text')
      .map((part) => part.text)
      .join(' ')
    const mentions = latest.parts
      .filter((part) => part.type === 'mention')
      .map((part) => part.userId)
    const selfQuestion =
      mentions.length === 0 &&
      /[?？]/u.test(text) &&
      /(?:我|\b(?:I|my|me)\b)/iu.test(text) &&
      !/["“”‘’「」『』]|(?:他说|她说|引用|quote)/iu.test(text)
    const ctx = scope(context)
    const hints = this.service.hints(ctx, {
      requesterId: latest.senderId,
      personId: latest.senderId,
      personQuestion: selfQuestion,
      planQuestion: /计划|安排|活动|plan|schedule/iu.test(text),
    })
    if (mentions.length === 1 && /[?？]|谁|如何|怎样|what|who|how/iu.test(text))
      hints.push(
        ...this.service.hints(ctx, {
          requesterId: latest.senderId,
          personId: mentions[0],
          personQuestion: true,
        }),
      )
    const selected: ExperienceHint[] = []
    const actions = new Set<string>()
    for (const hint of hints) {
      const key = JSON.stringify([hint.action, hint.condition])
      if (actions.has(key)) continue
      actions.add(key)
      selected.push(hint)
      if (selected.length === 3) break
    }
    if (!selected.length) return undefined
    for (const [id, turn] of this.turns) if (Date.now() - turn.at > 3600000) this.turns.delete(id)
    while (this.turns.size >= 128) this.turns.delete(this.turns.keys().next().value!)
    const trialLearningEpoch = this.trialLearningEpochs.get(ctx.sceneId) ?? 0
    this.turns.set(context.turnId, {
      sceneId: ctx.sceneId,
      trialLearningEpoch,
      selected,
      semanticRead: false,
      applied: new Set(),
      at: Date.now(),
    })
    for (const hint of selected) {
      context.validations.push(
        () =>
          (!hint.eligibilityBasis ||
            trialLearningEpoch === (this.trialLearningEpochs.get(ctx.sceneId) ?? 0)) &&
          this.service.snapshotValid(ctx, hint),
      )
      this.service.recordUse(ctx, { turnId: context.turnId, policyRef: hint, kind: 'exposed' })
    }
    return {
      policy:
        'Experimental low-priority operation hints, not facts or permissions. Hard rules, source qualifications and budgets dominate. Mechanical checks do not establish effectiveness; decline inappropriate hints.',
      hints: selected.map((hint) => ({
        policyId: hint.id,
        version: hint.version,
        action: hint.action,
        condition: hint.condition,
        tier: hint.tier,
        ...(hint.eligibilityBasis
          ? {
              eligibilityBasis: hint.eligibilityBasis,
              trialExpiresAt: hint.trialExpiresAt,
              reportedHelpfulCount: hint.reportedHelpfulCount,
              interpretation:
                hint.eligibilityBasis === 'explicit_instruction_trial'
                  ? 'Bounded trial authorized by an explicit instruction; effectiveness is unproven.'
                  : 'Distinct source reports by the relevant user attributed to this operation; not independently verified task success.',
            }
          : {}),
        hint: renderExperienceAction(hint.action),
      })),
    }
  }
  outcomes(context: TurnContext) {
    if (context.signal.aborted) return []
    const pending = this.pending.get(context) ?? new WeakMap<object, ExperienceOutcome>()
    this.pending.set(context, pending)
    const values = this.service.listOutcomes(scope(context)).map((outcome) => {
      const value = outcomeView(outcome)
      pending.set(value, structuredClone(outcome))
      return value
    })
    return context.deferMemoryExposure ? values : this.deliverOutcomes(context, values)
  }
  deliverOutcomes(context: TurnContext, values: readonly unknown[]) {
    if (context.signal.aborted) return []
    const pending = this.pending.get(context)
    const current = new Map(
      this.service.listOutcomes(scope(context)).map((outcome) => [outcome.id, outcome]),
    )
    const accepted: OutcomeView[] = []
    for (const value of values) {
      if (!value || typeof value !== 'object') return []
      const prepared = pending?.get(value)
      if (
        !prepared ||
        !isDeepStrictEqual(value, outcomeView(prepared)) ||
        !isDeepStrictEqual(current.get(prepared.id), prepared)
      )
        return []
      accepted.push(value as OutcomeView)
    }
    const refs = this.exposed.get(context) ?? []
    for (const value of accepted) {
      if (refs.some((ref) => ref.id === value.id)) continue
      refs.push({ id: value.id, version: value.version })
      context.dependencies.add(value.source.messageId)
      context.dependencies.add(value.target.messageId)
      context.validations.push(() =>
        this.service
          .listOutcomes(scope(context))
          .some((row) => isDeepStrictEqual(outcomeView(row), value)),
      )
    }
    this.exposed.set(context, refs)
    return accepted
  }
  propose(context: TurnContext, input: unknown) {
    if (context.signal.aborted || context.validations.some((valid) => !valid()))
      throw new Error('experience_context_invalid')
    const result = this.service.proposePolicy(
      scope(context),
      ExperienceProposalSchema.parse(input),
      this.exposed.get(context) ?? [],
    )
    return {
      status: 'candidate',
      candidateId: result.candidate.id,
      duplicate: result.duplicate,
      active: result.candidate.status === 'active',
      validationTier: 'mechanical_only',
    }
  }
  deliveredTool(context: TurnContext, name: string, args: Record<string, unknown>, value: unknown) {
    const turn = this.turns.get(context.turnId)
    if (!turn || turn.sceneId !== context.scene.id) return
    if (context.signal.aborted || context.validations.some((valid) => !valid())) return
    if (name === 'memory.search') turn.semanticRead = true
    if (!Array.isArray(value) || !value.length) return
    for (const hint of turn.selected) {
      const operation = appliedExperienceOperation({
        action: hint.action,
        condition: hint.condition,
        tool: name,
        arguments: args,
        value,
        semanticRead: turn.semanticRead,
        turnValid: true,
      })
      if (!operation) continue
      if (
        this.service.recordUse(scope(context), {
          turnId: context.turnId,
          policyRef: hint,
          kind: 'applied',
          operation,
        })
      )
        turn.applied.add(hint.id)
    }
  }
  sent(sceneId: string, turnId: string, targetMessageId: string) {
    const turn = this.turns.get(turnId)
    if (!turn || turn.sceneId !== sceneId) return
    const ctx = { sceneId, simulation: false }
    for (const hint of turn.selected) {
      if (
        hint.eligibilityBasis &&
        turn.trialLearningEpoch !== (this.trialLearningEpochs.get(sceneId) ?? 0)
      )
        continue
      if (!this.service.snapshotValid(ctx, hint)) continue
      this.service.recordUse(ctx, { turnId, policyRef: hint, kind: 'exposed', targetMessageId })
      if (turn.applied.has(hint.id))
        this.service.recordUse(ctx, {
          turnId,
          policyRef: hint,
          kind: 'applied',
          operation:
            hint.action === 'exact_person_before_semantic'
              ? 'memory_person'
              : 'plan_qualifiers_checked',
          targetMessageId,
        })
    }
    // Expression ordering is not inferred from a transport acknowledgement.
  }
}
