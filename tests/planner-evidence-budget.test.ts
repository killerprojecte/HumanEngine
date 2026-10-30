import test from 'node:test'
import assert from 'node:assert/strict'
import { PlannerEvidenceBudget } from '../src/runtime/planner-evidence-budget.ts'
import { buildReplyPlan, type ReplyPlan } from '../src/runtime/reply-plan.ts'
import { ContextProjection } from '../src/runtime/context-projection.ts'
import type { Completion, ChatMessage } from '../src/models.ts'
const call = (name: string, args: Record<string, unknown> = {}, id = name) => ({
  id,
  name,
  arguments: args,
})
const completion = (...toolCalls: Completion['toolCalls']): Completion => ({
  text: '',
  toolCalls,
  modelId: 'synthetic',
  usage: { inputTokens: 0, outputTokens: 0, totalTokens: 0 },
  assistant: { role: 'assistant', content: '', toolCalls },
})
const plan = (patch: Partial<ReplyPlan> = {}): ReplyPlan => ({
  action: 'reply',
  deliveryTool: 'messages.send',
  targetMessageIds: ['m1'],
  contextMessageIds: ['m1'],
  strategy: 'Use only delivered evidence',
  constraints: [],
  facts: [],
  assets: [],
  emoji: [],
  expressionGoals: [],
  memorySelections: [],
  actions: [],
  ...patch,
})
const records = [
  {
    id: 'memory-a',
    content: 'Positive claim "quoted" 中文',
    status: 'disputed',
    validFrom: '2026-10-01',
    validUntil: '2026-11-01',
    epistemicKind: 'reported',
    sources: [{ sceneId: 's', messageId: 'm1' }],
  },
  {
    id: 'memory-b',
    content: 'Conflicting correction. ' + 'x'.repeat(2000),
    status: 'active',
    recordedAt: '2026-10-28',
    epistemicKind: 'explicit',
  },
]
function input(
  patch: Partial<Parameters<typeof buildReplyPlan>[0]> = {},
): Parameters<typeof buildReplyPlan>[0] {
  return {
    messages: [{ role: 'user', content: 'synthetic query' }],
    readTools: [
      { name: 'memory.search', description: 'Read', inputSchema: {} },
      { name: 'web-read.read', description: 'Read', inputSchema: {}, effect: 'read' },
    ],
    actionTools: [],
    evidence: [],
    targetMessageIds: ['m1'],
    contextMessageIds: ['m1'],
    signal: new AbortController().signal,
    eligible: () => true,
    complete: async () => completion(call('reply.plan', plan())),
    execute: async () => records,
    ...patch,
  }
}
test('measurement is actual UTF8 serialized ChatMessage bytes including escaped receipt envelope', () => {
  const budget = new PlannerEvidenceBudget(10000)
  const messages: ChatMessage[] = [{ role: 'user', content: '中文 "\\\n' }]
  const delivered = budget.receipt(
    messages,
    { id: 't', toolName: 'memory.search', value: records },
    'c',
  )
  assert.equal(
    budget.measure([...messages, delivered.message]),
    Buffer.byteLength(JSON.stringify([...messages, delivered.message]), 'utf8'),
  )
  assert.deepEqual(delivered.receipt.value, records)
  assert.equal(delivered.receipt.coverage?.status, 'complete')
})
test('atomic conflicting record set is omitted explicitly, not positive-only or false no-results', async () => {
  let calls = 0
  const result = await buildReplyPlan(
    input({
      evidenceMaxBytes: 900,
      complete: async (messages) => {
        calls++
        if (calls === 1) return completion(call('memory.search'))
        const receipt = JSON.parse(messages.at(-1)!.content)
        assert.deepEqual(receipt.value, [])
        assert.deepEqual(receipt.coverage, {
          scope: 'returned_tool_result',
          status: 'limited',
          returnedRecords: 0,
          omittedRecords: 2,
          reason: 'planner_evidence_budget',
        })
        assert.ok(Buffer.byteLength(JSON.stringify(messages)) <= 900)
        return completion(call('reply.plan', plan()))
      },
    }),
  )
  assert.equal(result.evidence[0]!.coverage?.omittedRecords, 2)
  assert.deepEqual(result.evidence[0]!.value, [])
  assert.equal(calls, 2)
})
test('omitted member cannot be selected even if a projection registered its canonical ID', async () => {
  let calls = 0
  const projection = new ContextProjection('s')
  await assert.rejects(
    buildReplyPlan(
      input({
        projection,
        evidenceMaxBytes: 900,
        complete: async () =>
          ++calls === 1
            ? completion(call('memory.search'))
            : completion(
                call(
                  'reply.plan',
                  JSON.parse(
                    projection.stringify(
                      plan({
                        memorySelections: [{ evidenceId: 'tool:1', memoryIds: ['memory-a'] }],
                      }),
                    ),
                  ),
                ),
              ),
      }),
    ),
    /planner_evidence_budget_exhausted/,
  )
  assert.equal(calls, 2) // The byte cap refuses the correction before another model call.
})
test('full delivered records preserve times and qualifications and authorize exact membership', async () => {
  let calls = 0
  const result = await buildReplyPlan(
    input({
      evidenceMaxBytes: 10000,
      complete: async (messages) => {
        if (++calls === 1) return completion(call('memory.search'))
        assert.deepEqual(JSON.parse(messages.at(-1)!.content).value, records)
        return completion(
          call(
            'reply.plan',
            plan({ memorySelections: [{ evidenceId: 'tool:1', memoryIds: ['memory-a'] }] }),
          ),
        )
      },
    }),
  )
  assert.equal(result.plan.memorySelections[0]!.memoryIds[0], 'memory-a')
})
test('initial prompt too large fails before model and repeated receipts exhaust cumulative conversation bytes', async () => {
  let calls = 0
  await assert.rejects(
    buildReplyPlan(
      input({
        evidenceMaxBytes: 10,
        complete: async () => {
          calls++
          return completion()
        },
      }),
    ),
    /planner_evidence_budget_exhausted/,
  )
  assert.equal(calls, 0)
  await assert.rejects(
    buildReplyPlan(
      input({
        evidenceMaxBytes: 800,
        execute: async () => [{ id: 'tiny', content: 'fact' }],
        complete: async (messages) => {
          calls++
          assert.ok(Buffer.byteLength(JSON.stringify(messages)) <= 800)
          return completion(call('memory.search', {}, 'repeated-' + calls))
        },
      }),
    ),
    /planner_evidence_budget_exhausted/,
  )
  assert.ok(calls >= 2 && calls < 6)
})
test('non-memory tool output is never sliced; aggregate and existing raw cap refuse before next model', async () => {
  for (const [value, max, error] of [
    ['z'.repeat(1000), 900, /planner_evidence_budget_exhausted/],
    ['z'.repeat(65537), 200000, /planner_evidence_too_large/],
  ] as const) {
    let calls = 0
    await assert.rejects(
      buildReplyPlan(
        input({
          evidenceMaxBytes: max,
          execute: async () => ({ text: value }),
          complete: async () => {
            calls++
            return completion(call('web-read.read'))
          },
        }),
      ),
      error,
    )
    assert.equal(calls, 1)
  }
})
test('empty real search is complete and distinct from budget omission', () => {
  const receipt = new PlannerEvidenceBudget(1000).receipt(
    [],
    { id: 't', toolName: 'memory.person', value: [] },
    'c',
  ).receipt
  assert.deepEqual(receipt.coverage, {
    scope: 'returned_tool_result',
    status: 'complete',
    returnedRecords: 0,
    omittedRecords: 0,
  })
})

test('delivery commit receives only measured members, including empty omitted receipts', async () => {
  for (const evidenceMaxBytes of [900, 10000]) {
    let calls = 0,
      deliveries = 0
    await buildReplyPlan(
      input({
        evidenceMaxBytes,
        deliver(name, value) {
          deliveries++
          assert.equal(name, 'memory.search')
          assert.deepEqual(value, evidenceMaxBytes === 900 ? [] : records)
          return value
        },
        complete: async () =>
          ++calls === 1
            ? completion(call('memory.search'))
            : completion(call('reply.plan', plan())),
      }),
    )
    assert.equal(deliveries, 1)
    assert.equal(calls, 2)
  }
})
test('delivery commit changed result or in-place mutation fails before another model request', async () => {
  for (const mutate of [false, true]) {
    let calls = 0
    await assert.rejects(
      buildReplyPlan(
        input({
          execute: async () => structuredClone(records),
          deliver(_name, value) {
            if (mutate) {
              ;(value as typeof records)[0]!.content = 'changed after measurement'
              return value
            }
            return []
          },
          complete: async () => {
            calls++
            return completion(call('memory.search'))
          },
        }),
      ),
      /planner_context_ineligible/,
    )
    assert.equal(calls, 1)
  }
})
