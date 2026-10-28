import { fixturePlanCompletion } from './helpers/reply-plan.ts'
import test from 'node:test'
import assert from 'node:assert/strict'
import { Store } from '../src/store.ts'
import { Runtime, DecisionSchema } from '../src/runtime.ts'
import { Router, type ChatMessage, type Completion } from '../src/models.ts'

const marker = 'planner_reason_omitted_over_limit'
const overflow = 'x'.repeat(212)
const result = (calls: Completion['toolCalls']): Completion => ({
  text: '',
  toolCalls: calls,
  modelId: 'fixture',
  usage: { inputTokens: 0, outputTokens: 0, totalTokens: 0 },
  assistant: { role: 'assistant', content: '', toolCalls: calls },
})
async function runDecision(
  makeDecision: (input: { messages: Array<{ id: string }> }) => Record<string, unknown>,
) {
  const store = new Store(':memory:')
  store.set('settings', { ...store.settings, debounceMs: 0, maxWaitMs: 100 })
  let thinkerCalls = 0
  const thinkerInputs: Array<Record<string, unknown>> = []
  const planningInputs: Array<Record<string, unknown>> = []
  const router = new Router(store.models)
  router.complete = async (role, messages: ChatMessage[]) => {
    const fixturePlan = fixturePlanCompletion(role, messages)
    if (fixturePlan) {
      planningInputs.push(JSON.parse(messages.find((m) => m.role === 'user')!.content))
      return fixturePlan
    }
    if (role === 'planner')
      return result([
        {
          id: 'decision',
          name: 'participation.decide',
          arguments: makeDecision(JSON.parse(messages.at(-1)!.content as string)),
        },
      ])
    thinkerCalls++
    thinkerInputs.push(JSON.parse(messages.find((m) => m.role === 'user')!.content as string))
    return result(
      thinkerCalls === 1
        ? [
            {
              id: 'send',
              name: 'messages.send',
              arguments: {
                operationId: 'send-once',
                segments: [{ parts: [{ type: 'text', text: 'Verified reply' }] }],
              },
            },
          ]
        : [],
    )
  }
  const runtime = new Runtime(store, () => router)
  try {
    const id = runtime.createSimulation()
    const messages = await runtime.simulate(id, 'Please answer the current question.')
    return {
      messages,
      thinkerCalls,
      thinkerInputs,
      planningInputs,
      logs: structuredClone(store.logs),
    }
  } finally {
    runtime.dispose()
    store.close()
  }
}

test('a sole 212-character planner explanation overflow is omitted without changing the decision', async () => {
  assert.equal(overflow.length, 212)
  assert.equal(DecisionSchema.safeParse({ action: 'reply', reasonCode: overflow }).success, false)
  const outcome = await runDecision((input) => ({
    action: 'reply',
    reasonCode: overflow,
    targetMessageIds: [input.messages[0]!.id],
    topicId: 'current',
    topics: [{ topicId: 'current', messageIds: [input.messages[0]!.id] }],
  }))
  assert.equal(outcome.messages.filter((m) => m.senderId === 'bot').length, 1)
  assert.equal(outcome.thinkerCalls, 1)
  const decision = outcome.planningInputs[0]!.decision as Record<string, unknown>
  assert.equal(decision.action, 'reply')
  assert.equal(decision.reasonCode, marker)
  const context = outcome.thinkerInputs[0]!.messages as Array<{ id: string }>
  assert.deepEqual(decision.targetMessageIds, [context[0]!.id])
  assert.notEqual(context[0]!.id, outcome.messages[0]!.id)
  assert.equal(
    (outcome.thinkerInputs[0]!.plan as { targetMessageIds: string[] }).targetMessageIds[0],
    context[0]!.id,
  )
  assert.equal(outcome.logs.filter((entry) => entry.code === marker).length, 1)
  assert.equal(JSON.stringify(outcome.logs).includes(overflow), false)
  assert.equal(JSON.stringify(outcome.thinkerInputs).includes(overflow), false)
})

test('bounded planner explanations remain unchanged and silent or wait cannot become replies', async () => {
  const reason = 'a'.repeat(200)
  const bounded = await runDecision(() => ({ action: 'reply', reasonCode: reason }))
  assert.equal((bounded.planningInputs[0]!.decision as Record<string, unknown>).reasonCode, reason)
  assert.equal(
    bounded.logs.some((entry) => entry.code === marker),
    false,
  )
  for (const action of ['silent', 'wait']) {
    const outcome = await runDecision(() => ({ action, reasonCode: overflow }))
    assert.equal(outcome.thinkerCalls, 0)
    assert.equal(outcome.messages.filter((m) => m.senderId === 'bot').length, 0)
    assert.equal(outcome.logs.filter((entry) => entry.code === marker).length, 1)
  }
})

test('reason normalization does not repair other malformed fields or bypass message scope checks', async () => {
  const invalid: Array<Record<string, unknown>> = [
    { action: 'send', reasonCode: overflow },
    { action: 'reply', reasonCode: 212 },
    { action: 'reply' },
    { action: 'reply', reasonCode: overflow, extra: true },
    { action: 'reply', reasonCode: overflow, targetMessageIds: Array(21).fill('outside') },
    { action: 'reply', reasonCode: overflow, targetMessageIds: 'outside' },
    {
      action: 'reply',
      reasonCode: overflow,
      topics: [{ topicId: 'x'.repeat(101), messageIds: [] }],
    },
    {
      action: 'reply',
      reasonCode: overflow,
      topics: [{ topicId: 'topic', messageIds: [], extra: true }],
    },
  ]
  for (const decision of invalid) {
    const outcome = await runDecision(() => decision)
    assert.equal(outcome.thinkerCalls, 0)
    assert.equal(outcome.messages.filter((m) => m.senderId === 'bot').length, 0)
    assert.equal(
      outcome.logs.some((entry) => entry.code === marker),
      false,
    )
    assert.equal(
      outcome.logs.some((entry) => entry.stage === 'turn' && entry.status === 'failed'),
      true,
    )
  }
  for (const fields of [
    { targetMessageIds: ['another-scene-message'] },
    { topics: [{ topicId: 'current', messageIds: ['another-scene-message'] }] },
    {
      topics: [
        { topicId: 'duplicate', messageIds: [] },
        { topicId: 'duplicate', messageIds: [] },
      ],
    },
  ]) {
    const outcome = await runDecision(() => ({ action: 'reply', reasonCode: overflow, ...fields }))
    assert.equal(outcome.thinkerCalls, 0)
    assert.equal(outcome.messages.filter((m) => m.senderId === 'bot').length, 0)
    assert.equal(
      outcome.logs.some((entry) => entry.stage === 'turn' && entry.status === 'failed'),
      true,
    )
  }
})
