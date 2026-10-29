import assert from 'node:assert/strict'
import test from 'node:test'
import { deriveExplicitPolicyFeedback } from '../src/memory/experience/feedback-adapter.ts'
import { ExperienceRuntime } from '../src/memory/experience/runtime-hints.ts'
import type { ExperienceService } from '../src/memory/experience/service.ts'
import type {
  ExperienceHint,
  TrustedFeedback,
  TrustedPolicyUse,
} from '../src/memory/domain/experience.ts'
import type { RuntimeMessage, TurnContext } from '../src/runtime.ts'
const source = (id: string, text: string, senderId = 'person'): RuntimeMessage => ({
  id,
  sceneId: 'scene',
  senderId,
  origin: 'live',
  status: 'available',
  receivedAt: '2026-10-29T00:00:00Z',
  sentAt: '2026-10-29T00:00:00Z',
  parts: [{ type: 'text', text }],
  sourceEventRef: { eventId: id, revision: 1, deletionEpoch: 0 },
})
const ref = { id: 'policy', version: 3 }
test('explicit positive report requires exact matching applied operation and preserves first source/target snapshot', () => {
  const target = source('bot-target', 'Synthetic delivery.', 'bot')
  const message = { ...source('report', '这次先查我的记忆有帮助'), replyToId: target.id }
  assert.equal(deriveExplicitPolicyFeedback('scene', message, target, ref), undefined)
  assert.equal(
    deriveExplicitPolicyFeedback('scene', message, target, ref, {
      policyRef: ref,
      operation: 'plan_qualifiers_checked',
    }),
    undefined,
  )
  const result = deriveExplicitPolicyFeedback('scene', message, target, ref, {
    policyRef: ref,
    operation: 'memory_person',
  })!
  assert.equal(result.code, 'user_reported_helpful')
  assert.deepEqual(result.policyRef, ref)
  assert.deepEqual(result.source.eventRef, message.sourceEventRef)
  assert.deepEqual(result.target.eventRef, target.sourceEventRef)
  message.sourceEventRef!.revision++
  assert.equal(result.source.eventRef!.revision, 1)
})
test('quoted, generic, ambiguous, foreign and limited-event reports never become positive evidence', () => {
  const target = source('bot-target', 'Synthetic delivery.', 'bot')
  for (const text of [
    '谢谢',
    '好',
    '这次很好',
    '“这次先查我的记忆有帮助”',
    '这次先查我的记忆有帮助但也没帮助',
    '这次先查我的记忆有帮助？',
    '这次先查我的记忆有帮助?',
    '这次先查我的记忆不合适？',
    '这次先查我的记忆不合适?',
  ]) {
    assert.equal(
      deriveExplicitPolicyFeedback(
        'scene',
        { ...source('report', text), replyToId: target.id },
        target,
        ref,
        { policyRef: ref, operation: 'memory_person' },
      ),
      undefined,
    )
  }
  const message = { ...source('report', '这次先查我的记忆有帮助'), replyToId: target.id }
  assert.equal(
    deriveExplicitPolicyFeedback('foreign', message, target, ref, {
      policyRef: ref,
      operation: 'memory_person',
    }),
    undefined,
  )
  assert.equal(
    deriveExplicitPolicyFeedback(
      'scene',
      { ...message, sourceEventRegistrationLimited: true },
      target,
      ref,
      { policyRef: ref, operation: 'memory_person' },
    ),
    undefined,
  )
})
function harness(action: ExperienceHint['action'], hintPatch: Partial<ExperienceHint> = {}) {
  const target = source('bot-target', 'Synthetic delivery.', 'bot')
  const uses: TrustedPolicyUse[] = [],
    feedback: TrustedFeedback[] = []
  const hint: ExperienceHint = {
    ...ref,
    hash: 'synthetic',
    action,
    condition:
      action === 'verify_plan_qualifiers'
        ? { kind: 'plan_question' }
        : action === 'conclusion_before_detail'
          ? { kind: 'response_style', personId: 'person' }
          : { kind: 'person_question', personId: 'person' },
    expiresAt: '2999-01-01T00:00:00Z',
    tier: 'mechanical_only',
    ...hintPatch,
  }
  const service = {
    hints: () => [hint],
    snapshotValid: () => true,
    recordUse: (_ctx: unknown, use: TrustedPolicyUse) => {
      uses.push(structuredClone(use))
      return true
    },
    targetUses: () => uses.filter((use) => use.targetMessageId === target.id),
    recordFeedback: (_ctx: unknown, value: TrustedFeedback) => feedback.push(value),
  } as unknown as ExperienceService
  const runtime = new ExperienceRuntime(service, () => [target])
  const context = {
    scene: { id: 'scene' },
    mode: 'real',
    turnId: 'turn',
    signal: new AbortController().signal,
    validations: [],
    dependencies: new Set(),
  } as unknown as TurnContext
  const planner = runtime.plannerContext(context, [source('question', '计划如何？')])
  return { runtime, context, target, uses, feedback, planner }
}
test('actual hint/use hooks bind positive feedback only after qualified read and delivered target; style ACK stays exposure', () => {
  for (const variant of ['empty', 'unqualified', 'qualified', 'mixed', 'style'] as const) {
    const h = harness(variant === 'style' ? 'conclusion_before_detail' : 'verify_plan_qualifiers')
    const value =
      variant === 'empty'
        ? []
        : variant === 'qualified'
          ? [
              {
                relationCoverage: 'complete',
                relationEvidence: [{ polarity: 'negative', conditions: ['if rain'] }],
              },
            ]
          : [{ content: 'ordinary record' }]
    if (variant === 'mixed')
      value.push({
        relationCoverage: 'complete',
        relationEvidence: [{ polarity: 'negative', conditions: ['if rain'] }],
      } as never)
    h.runtime.deliveredTool(h.context, 'memory.relations', {}, value)
    h.runtime.sent('scene', 'turn', h.target.id)
    h.runtime.received({
      ...source('positive', '这次先核对计划条件有帮助'),
      replyToId: h.target.id,
    })
    assert.equal(h.feedback.length, variant === 'qualified' ? 1 : 0)
    assert.equal(
      h.uses.filter((use) => use.kind === 'applied' && use.targetMessageId).length,
      variant === 'qualified' ? 1 : 0,
    )
  }
})
test('search before exact person, aborted read, and ambiguous applied mappings do not authorize positive reports', () => {
  for (const variant of ['search-first', 'aborted', 'ambiguous'] as const) {
    const h = harness('exact_person_before_semantic')
    if (variant === 'search-first') h.runtime.deliveredTool(h.context, 'memory.search', {}, [])
    if (variant === 'aborted') h.context.validations.push(() => false)
    h.runtime.deliveredTool(h.context, 'memory.person', { personId: 'person' }, [
      { content: 'scoped record' },
    ])
    h.runtime.sent('scene', 'turn', h.target.id)
    if (variant === 'ambiguous')
      h.uses.push({
        turnId: 'other',
        policyRef: { id: 'other', version: 1 },
        kind: 'applied',
        operation: 'memory_person',
        targetMessageId: h.target.id,
      })
    h.runtime.received({ ...source('positive', '这次先查我的记忆有帮助'), replyToId: h.target.id })
    assert.equal(h.feedback.length, 0)
  }
})

test('self lookup selection requires unquoted first-person question and no ambiguous mentions', () => {
  const queries: Array<{ personId?: string; personQuestion?: boolean }> = []
  const runtime = new ExperienceRuntime(
    {
      hints: (_ctx: unknown, query: { personId?: string; personQuestion?: boolean }) => {
        queries.push(query)
        return []
      },
    } as unknown as ExperienceService,
    () => [],
  )
  const context = {
    scene: { id: 'scene' },
    mode: 'real',
    turnId: 'turn',
    signal: new AbortController().signal,
    validations: [],
    dependencies: new Set(),
  } as unknown as TurnContext
  for (const [text, expected] of [
    ['我之前说的偏好是什么？', true],
    ['What is my preference?', true],
    ['我喜欢茶', false],
    ['“我喜欢什么？”', false],
    ['他说我喜欢什么？', false],
    ['他喜欢什么？', false],
  ] as const) {
    queries.length = 0
    runtime.plannerContext(context, [source('question', text)])
    assert.equal(queries[0]?.personQuestion, expected, text)
    assert.equal(queries[0]?.personId, 'person')
  }
  queries.length = 0
  const message = source('question', '我的偏好是什么？')
  message.parts.push({ type: 'mention', userId: 'a' }, { type: 'mention', userId: 'b' })
  runtime.plannerContext(context, [message])
  assert.equal(
    queries.some((q) => q.personQuestion),
    false,
  )
})

test('Planner receives trial versus user-reported eligibility without an effectiveness verdict', () => {
  for (const eligibilityBasis of ['explicit_instruction_trial', 'user_reported_support'] as const) {
    const h = harness('verify_plan_qualifiers', {
      eligibilityBasis,
      trialExpiresAt: '2026-10-30T00:00:00Z',
      reportedHelpfulCount: eligibilityBasis === 'user_reported_support' ? 2 : 0,
    })
    const hint = h.planner!.hints[0]!
    assert.equal(hint.eligibilityBasis, eligibilityBasis)
    assert.equal(hint.trialExpiresAt, '2026-10-30T00:00:00Z')
    assert.equal(hint.tier, 'mechanical_only')
    assert.match(
      hint.interpretation!,
      eligibilityBasis === 'explicit_instruction_trial' ? /unproven/ : /not independently verified/,
    )
  }
})
