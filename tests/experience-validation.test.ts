import test from 'node:test'
import assert from 'node:assert/strict'
import {
  experienceCandidateHash,
  type ExperiencePolicyCandidate,
} from '../src/memory/domain/experience.ts'
import { validateExperiencePolicy } from '../src/memory/experience/validation.ts'
import {
  renderExperienceAction,
  isExperienceActionCondition,
} from '../src/memory/experience/policy-actions.ts'
import { deriveExplicitPolicyFeedback } from '../src/memory/experience/feedback-adapter.ts'
import type { RuntimeMessage } from '../src/runtime.ts'
function candidate(patch: Partial<ExperiencePolicyCandidate> = {}): ExperiencePolicyCandidate {
  const result: ExperiencePolicyCandidate = {
    id: 'policy',
    version: 1,
    hash: '',
    sceneId: 'scene',
    action: 'conclusion_before_detail',
    condition: { kind: 'response_style', personId: 'person' },
    status: 'candidate',
    supportIds: ['outcome'],
    counterexampleIds: [],
    generationRoots: ['real-source-root'],
    generationFamilies: ['instruction-code-family'],
    createdAt: '2026-10-28T00:00:00Z',
    expiresAt: '2026-11-28T00:00:00Z',
    ...patch,
  }
  result.hash = experienceCandidateHash(result)
  return result
}
const bot: RuntimeMessage = {
  id: 'bot-response',
  sceneId: 'scene',
  senderId: 'bot',
  origin: 'live',
  status: 'available',
  sentAt: '2026-10-28T00:00:00Z',
  receivedAt: '2026-10-28T00:00:00Z',
  parts: [{ type: 'text', text: 'Synthetic previous answer' }],
}
const feedback = (text: string): RuntimeMessage => ({
  id: 'user-feedback',
  sceneId: 'scene',
  senderId: 'person',
  origin: 'live',
  status: 'available',
  sentAt: '2026-10-28T00:01:00Z',
  receivedAt: '2026-10-28T00:01:01Z',
  replyToId: bot.id,
  parts: [{ type: 'text', text }],
})
test('fixed independent mechanical runner produces exact reproducible receipt, not an effectiveness claim', () => {
  const c = candidate(),
    receipt = validateExperiencePolicy(c)
  assert.equal(receipt.passed, true)
  assert.equal(receipt.tier, 'mechanical_only')
  assert.equal(receipt.candidateVersion, 1)
  assert.equal(receipt.candidateHash, c.hash)
  assert.match(receipt.ruleHash, /^[a-f0-9]{64}$/)
  assert.match(receipt.datasetHash, /^[a-f0-9]{64}$/)
  assert.deepEqual(validateExperiencePolicy(c), receipt)
  const changed = { ...c, version: 2 }
  assert.equal(validateExperiencePolicy(changed).candidateVersion, 2)
  assert.deepEqual(receipt.failures, [])
  assert.ok(receipt.checks.includes('fixed_protocol_holdouts'))
})
test('contaminated roots or families, forged pass, wrong hash and unsafe action/condition cannot validate', () => {
  const receipt = validateExperiencePolicy(candidate())
  for (const c of [
    candidate({ generationRoots: [receipt.heldoutRoots[0]!] }),
    candidate({ generationFamilies: [receipt.heldoutFamilies[0]!] }),
    { ...candidate(), hash: 'forged' },
    { ...candidate(), passed: true },
    candidate({ action: 'execute_arbitrary' as never }),
    candidate({
      condition: {
        kind: 'response_style',
        personId: 'person',
        template: 'PRIVATE_TEMPLATE_CANARY',
      } as never,
    }),
    candidate({ counterexampleIds: ['counterexample'] }),
  ]) {
    const rejected = validateExperiencePolicy(c)
    assert.equal(rejected.passed, false)
    assert.ok(rejected.failures.length > 0)
    assert.equal(JSON.stringify(rejected).includes('PRIVATE_TEMPLATE_CANARY'), false)
  }
})
test('action rendering is closed, fixed and does not interpolate identity or grant permission', () => {
  assert.equal(
    isExperienceActionCondition('verify_plan_qualifiers', { kind: 'plan_question' }),
    true,
  )
  assert.equal(
    isExperienceActionCondition('exact_person_before_semantic', {
      kind: 'response_style',
      personId: 'person',
    }),
    false,
  )
  assert.throws(
    () => renderExperienceAction('send_message' as never),
    /experience_action_unsupported/,
  )
  assert.match(
    renderExperienceAction('exact_person_before_semantic'),
    /authorization and evidence rules still apply/,
  )
  assert.match(
    renderExperienceAction('conclusion_before_detail'),
    /Do not omit essential uncertainty/,
  )
})
test('explicit short reply-targeted instructions retain provenance without copying body', () => {
  for (const text of ['请先说结论', '先给结论，再给细节', ' 请先给结论，再给细节！ ']) {
    const source = feedback(text),
      outcome = deriveExplicitPolicyFeedback('scene', source, bot)!
    assert.equal(outcome.code, 'request_conclusion_first')
    assert.deepEqual(outcome.source, { messageId: source.id, senderId: 'person' })
    assert.deepEqual(outcome.target, { messageId: bot.id })
    assert.equal(outcome.observedAt, source.receivedAt)
    assert.equal(JSON.stringify(outcome).includes(text.trim()), false)
    assert.deepEqual(deriveExplicitPolicyFeedback('scene', source, bot), outcome)
  }
})
test('withdrawal requires exact trusted exposed policy, while ACK, silence, quotes and unrelated targets do nothing', () => {
  const source = feedback('不要再先说结论')
  assert.equal(deriveExplicitPolicyFeedback('scene', source, bot), undefined)
  const ref = { id: 'policy', version: 3 }
  const outcome = deriveExplicitPolicyFeedback('scene', source, bot, ref)!
  assert.equal(outcome.code, 'withdraw_instruction')
  assert.deepEqual(outcome.policyRef, ref)
  ref.version = 99
  assert.equal(outcome.policyRef!.version, 3)
  for (const text of [
    '谢谢',
    '好的',
    'ACK',
    '',
    '他说“请先说结论”',
    '如果我说请先说结论呢',
    '不要这样',
    '请先查人物',
  ])
    assert.equal(deriveExplicitPolicyFeedback('scene', feedback(text), bot), undefined)
  for (const patch of [
    { origin: 'history' },
    { origin: 'simulation' },
    { senderId: 'bot' },
    { status: 'recalled' },
    { sceneId: 'other' },
    { replyToId: 'unrelated' },
    { receivedAt: '' },
  ] as const)
    assert.equal(
      deriveExplicitPolicyFeedback(
        'scene',
        { ...feedback('请先说结论'), ...patch } as RuntimeMessage,
        bot,
      ),
      undefined,
    )
  assert.equal(
    deriveExplicitPolicyFeedback('scene', feedback('请先说结论'), { ...bot, sceneId: 'other' }),
    undefined,
  )
  assert.equal(deriveExplicitPolicyFeedback('scene', feedback('请先说结论'), undefined), undefined)
})

test('legitimate activated and suspended policy shapes retain strict activation basis validation', () => {
  for (const activationBasis of ['explicit_preference', 'administrator_review'] as const) {
    const active = candidate({
      status: 'active',
      version: 3,
      activationBasis,
      validationId: 'trusted-validation',
    })
    assert.equal(validateExperiencePolicy(active).passed, true)
    const suspended = {
      ...active,
      status: 'suspended' as const,
      version: 4,
      reason: 'administrator_suspended',
    }
    const receipt = validateExperiencePolicy(suspended)
    assert.equal(receipt.passed, true)
    assert.equal(receipt.candidateVersion, 4)
    assert.equal(receipt.candidateHash, active.hash)
  }
  for (const activationBasis of ['model_says_passed', true, null, { arbitrary: 'payload' }]) {
    const invalid = { ...candidate(), activationBasis } as unknown as ExperiencePolicyCandidate
    assert.ok(validateExperiencePolicy(invalid).failures.includes('candidate_shape'))
  }
  assert.ok(
    validateExperiencePolicy({
      ...candidate(),
      arbitraryKey: true,
    } as ExperiencePolicyCandidate).failures.includes('candidate_shape'),
  )
})

test('all three whitelisted instruction actions have exact native feedback entry without semantic guessing', () => {
  for (const [text, code] of [
    ['请先说结论', 'request_conclusion_first'],
    ['涉及我的问题先查我的记忆', 'request_exact_person_lookup'],
    ['回答计划问题前先核对条件', 'request_verify_plan_qualifiers'],
  ] as const) {
    const result = deriveExplicitPolicyFeedback('scene', feedback(text), bot)!
    assert.equal(result.code, code)
    assert.equal(result.source.senderId, 'person')
    assert.equal(result.policyRef, undefined)
    assert.equal(Object.hasOwn(result, 'passed'), false)
    assert.equal(Object.hasOwn(result, 'status'), false)
    assert.equal(
      deriveExplicitPolicyFeedback('scene', feedback('他说“' + text + '”'), bot),
      undefined,
    )
    assert.equal(
      deriveExplicitPolicyFeedback('scene', { ...feedback(text), replyToId: undefined }, bot),
      undefined,
    )
  }
  for (const text of [
    '帮我看看',
    '请查他的记忆',
    '如果涉及我的问题先查我的记忆',
    '回答计划问题前先核对条件好吗',
  ])
    assert.equal(deriveExplicitPolicyFeedback('scene', feedback(text), bot), undefined)
})

test('exact negative action feedback requires the matching actual applied policy, not merely exposure', () => {
  for (const [text, operation] of [
    ['这次先查我的记忆不合适', 'memory_person'],
    ['这次先核对计划条件不合适', 'plan_qualifiers_checked'],
  ] as const) {
    const source = feedback(text),
      policyRef = { id: 'verified-applied-policy', version: 4 }
    assert.equal(deriveExplicitPolicyFeedback('scene', source, bot), undefined)
    assert.equal(deriveExplicitPolicyFeedback('scene', source, bot, policyRef), undefined)
    assert.equal(
      deriveExplicitPolicyFeedback('scene', source, bot, undefined, {
        policyRef,
        operation: operation === 'memory_person' ? 'plan_qualifiers_checked' : 'memory_person',
      }),
      undefined,
    )
    const result = deriveExplicitPolicyFeedback('scene', source, bot, undefined, {
      policyRef,
      operation,
    })!
    assert.equal(result.code, 'counterexample')
    assert.deepEqual(result.policyRef, policyRef)
    policyRef.version = 9
    assert.equal(result.policyRef!.version, 4)
    assert.equal(
      deriveExplicitPolicyFeedback('scene', feedback('他说“' + text + '”'), bot, undefined, {
        policyRef,
        operation,
      }),
      undefined,
    )
  }
  for (const text of ['答错了', '这次先说结论不合适', '谢谢', 'ACK'])
    assert.equal(
      deriveExplicitPolicyFeedback('scene', feedback(text), bot, undefined, {
        policyRef: { id: 'policy', version: 1 },
        operation: 'memory_person',
      }),
      undefined,
    )
})
