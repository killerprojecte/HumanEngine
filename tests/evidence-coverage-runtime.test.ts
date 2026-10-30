import assert from 'node:assert/strict'
import test, { type TestContext } from 'node:test'
import { mkdtempSync, rmSync } from 'node:fs'
import { join } from 'node:path'
import { tmpdir } from 'node:os'
import { setTimeout as delay } from 'node:timers/promises'
import { Engine } from '../src/engine.ts'
import { Store } from '../src/store.ts'
import { installAdapter, type AdapterContext } from '../src/plugins/adapters.ts'
import type { Message, SceneRef } from '../src/onebot.ts'
import type { ChatMessage, ToolCall } from '../src/models.ts'
import { EVIDENCE_SCOPE_CONTRACT } from '../src/runtime/evidence-scope.ts'
import { fixtureReplyPlan } from './helpers/reply-plan.ts'

// Synthetic completeness claims retained as adversarial planner data, not an oracle of truth.
// The short constraints/goals reproduce the failure class; no host report or private reasoning is loaded.
const badPlan = {
  strategy: 'Only these two declarations exist in what is saved; present the records as complete.',
  constraints: [
    'Address Alice directly; no need to ask clarifying questions since the records are complete',
  ],
  expressionGoals: [
    'Be upfront that these are the only saved declarations and confidence is moderate, not certainty',
  ],
}
type Scope = { scope: string; historyCoverage: 'not_established' }
type Coverage = {
  scope: 'returned_tool_result'
  status: 'complete' | 'limited'
  returnedRecords: number
  omittedRecords: number
}
type Receipt = {
  id: string
  toolName: string
  value: unknown
  coverage?: Coverage
  retrievalCoverage: Scope
}
type NativeRequest = { messages: ChatMessage[]; tools: Array<{ function: { name: string } }> }
type Hint = {
  memorySelectionOptions: Array<{
    evidenceId: string
    memoryIds: string[]
    coverage: string
    retrievalCoverage: Scope
    receiptCoverage: Coverage
  }>
  coverage: string
}
type ReplyWire = {
  coverageByEvidence: Array<{
    id: string
    toolName?: string
    coverage?: Coverage
    retrievalCoverage: Scope
  }>
  evidence: Array<{
    id: string
    toolName: string
    resultCoverage?: string
    memories?: Array<{ id: string; content: string }>
    messageIds?: string[]
  }>
  plan: { strategy: string; constraints: string[]; expressionGoals: string[] }
  memoryEvidenceCoverage: { status: string; omitted: Array<{ evidenceId: string; reason: string }> }
}
const call = (name: string, arguments_: Record<string, unknown>): ToolCall => ({
  id: name,
  name,
  arguments: arguments_,
})
const response = (calls: ToolCall[]) =>
  new Response(
    JSON.stringify({
      choices: [
        {
          finish_reason: calls.length ? 'tool_calls' : 'stop',
          message: {
            role: 'assistant',
            content: '',
            tool_calls: calls.map((item, index) => ({
              id: `${item.id}-${index}`,
              type: 'function',
              function: {
                name: item.name.replaceAll('.', '__'),
                arguments: JSON.stringify(item.arguments),
              },
            })),
          },
        },
      ],
      usage: { prompt_tokens: 1, completion_tokens: 1, total_tokens: 2 },
    }),
    { headers: { 'content-type': 'application/json' } },
  )
function returned(body: NativeRequest): Receipt[] {
  return body.messages
    .filter((message) => message.role === 'tool')
    .map((message) => JSON.parse(message.content))
    .filter((item) => item.toolName)
}
async function until(check: () => boolean) {
  const deadline = Date.now() + 6000
  while (!check()) {
    if (Date.now() > deadline) throw Error('synthetic_runtime_did_not_finish')
    await delay(10)
  }
}
async function run(t: TestContext, mode: 'complete' | 'empty' | 'omitted') {
  const root = mkdtempSync(join(tmpdir(), 'he-evidence-coverage-'))
  const store = new Store(join(root, 'fixture.sqlite'))
  store.set('settings', {
    ...store.settings,
    debounceMs: 0,
    maxWaitMs: 100,
    ...(mode === 'omitted' ? { plannerEvidenceMaxBytes: 16384 } : {}),
  })
  store.saveModels({
    providers: [
      {
        id: 'synthetic',
        name: 'In-process native mock',
        protocol: 'openai-chat',
        baseUrl: 'https://coverage-fixture.invalid/v1',
        credentialEnv: '',
        retryCount: 0,
        timeoutMs: 2000,
      },
    ],
    models: [
      {
        id: 'synthetic',
        displayName: 'Synthetic',
        providerId: 'synthetic',
        apiModelName: 'synthetic',
        capabilities: ['text', 'tools'],
      },
    ],
    routes: (['planner', 'replyer'] as const).map((role) => ({
      role,
      orderedModelIds: ['synthetic'],
      timeoutMs: 3000,
    })),
  })
  const log = store.log.bind(store)
  let finished = false
  store.log = (level, module, code, detail = {}) => {
    const result = log(level, module, code, detail)
    if (detail.stage === 'turn_cleanup' && detail.status === 'completed') finished = true
    return result
  }
  const engine = new Engine(store)
  let adapter!: AdapterContext,
    scene!: SceneRef,
    sends = 0,
    plannerCalls = 0,
    replyerCalls = 0
  const registration = installAdapter(
    'coverage-fixture',
    (context) => {
      adapter = context
      return {
        async send(_scene, segment) {
          sends++
          // Deliberately independent of the bad plan: this is a transport ACK, not semantic scoring.
          assert.deepEqual(segment.parts, [
            { type: 'text', text: 'Synthetic coverage fixture reply.' },
          ])
          return { status: 'sent' as const, messageId: context.messageId(scene, 'local-ack') }
        },
        async history() {
          return { items: [], coverage: 'complete' as const }
        },
        async getMessage() {
          return { items: [], coverage: 'complete' as const }
        },
        dispose() {},
      }
    },
    engine.runtime,
    store,
    new AbortController().signal,
    {},
  )
  scene = adapter.scene('synthetic-account', 'group', 'synthetic-group')
  store.saveScene({
    ...scene,
    name: 'Synthetic coverage group',
    participationEnabled: true,
    learningEnabled: false,
    proactiveEnabled: false,
  })
  const historicalCanary = 'HISTORICAL_UNRETURNED_CANARY prefers black coffee.'
  const history = engine.memory.create({
    scope: { kind: 'scene', sceneId: scene.id, personId: 'alice' },
    content: historicalCanary,
    kind: 'fact',
  })
  engine.memory.update(history.id, { status: 'superseded' })
  if (mode !== 'empty') {
    for (const content of ['Alice prefers jasmine tea.', 'Alice requests no sugar.'])
      engine.memory.create({
        scope: { kind: 'scene', sceneId: scene.id, personId: 'alice' },
        content:
          content + (mode === 'omitted' ? ' Long qualified synthetic detail.'.repeat(350) : ''),
        kind: 'fact',
      })
  }
  const errors: unknown[] = [],
    plannerReceipts: Receipt[] = []
  let replyWire: ReplyWire | undefined, repairHint: Hint | undefined
  let questionRef = ''
  let replySystem = ''
  const initialEvidenceIds: string[] = []
  t.mock.method(globalThis, 'fetch', async (url: unknown, init?: RequestInit) => {
    try {
      assert.equal(String(url), 'https://coverage-fixture.invalid/v1/chat/completions')
      const body = JSON.parse(String(init?.body)) as NativeRequest
      const names = body.tools.map((tool) => tool.function.name)
      const input = JSON.parse(body.messages.find((message) => message.role === 'user')!.content)
      if (names.includes('participation__decide'))
        return response([
          call('participation.decide', {
            action: 'reply',
            reasonCode: 'synthetic_coverage',
            targetMessageIds: [input.messages.at(-1).id],
          }),
        ])
      if (names.includes('reply__plan')) {
        plannerCalls++
        const system = body.messages
          .filter((message) => message.role === 'system')
          .map((message) => message.content)
          .join('\n')
        assert.equal(system.split(EVIDENCE_SCOPE_CONTRACT).length - 1, 1)
        if (plannerCalls === 1) {
          questionRef = input.planContract.targetMessageIds[0]
          initialEvidenceIds.push(...input.evidence.map((item: { id: string }) => item.id))
          return response([
            call('memory.search', { query: 'Alice jasmine tea sugar' }),
            call('memory.person', { personId: 'alice' }),
            call('messages.get', { ids: [questionRef] }),
          ])
        }
        const receipts = returned(body)
        if (!plannerReceipts.length) plannerReceipts.push(...structuredClone(receipts))
        const person = receipts.find((receipt) => receipt.toolName === 'memory.person')!
        const message = receipts.find((receipt) => receipt.toolName === 'messages.get')!
        const records = person.value as Array<{ id: string; content: string }>
        const selected = records.find((record) =>
          record.content.startsWith('Alice prefers jasmine tea.'),
        )
        if (mode === 'complete') assert.ok(selected)
        const last = body.messages.at(-1)
        if (last?.role === 'tool') {
          const value = JSON.parse(last.content)
          if (value.error?.remainingRepairs === 1) {
            repairHint = value
            assert.deepEqual(names, ['reply__plan'])
          }
        }
        const plan = fixtureReplyPlan(body.messages, {
          ...badPlan,
          facts: [
            {
              text: 'The current question requests remembered declarations.',
              evidenceIds: [message.id],
            },
          ],
          memorySelections:
            mode === 'complete' ? [{ evidenceId: person.id, memoryIds: [selected!.id] }] : [],
        })!
        if (mode === 'complete')
          plan.facts.push({
            text: 'The selected declaration mentions jasmine tea.',
            evidenceIds: [
              receipts.find((receipt) => receipt.toolName === 'memory.search')!.id,
              person.id,
            ],
          })
        if (mode === 'complete')
          plan.memorySelections.push({
            evidenceId: receipts.find((receipt) => receipt.toolName === 'memory.search')!.id,
            memoryIds: [selected!.id],
          })
        if (mode === 'omitted' && !repairHint)
          plan.memorySelections = [{ evidenceId: person.id, memoryIds: ['never-delivered-record'] }]
        return response([call('reply.plan', plan as unknown as Record<string, unknown>)])
      }
      assert.deepEqual(names, ['messages__send'])
      replyerCalls++
      replyWire = input as ReplyWire
      replySystem = body.messages
        .filter((message) => message.role === 'system')
        .map((message) => message.content)
        .join('\n')
      return response([
        call('messages.send', {
          operationId: 'synthetic-coverage-answer',
          segments: [{ parts: [{ type: 'text', text: 'Synthetic coverage fixture reply.' }] }],
        }),
      ])
    } catch (error) {
      errors.push(error)
      throw error
    }
  })
  try {
    const now = new Date().toISOString()
    adapter.receive(
      {
        id: adapter.messageId(scene, 'question'),
        sceneId: scene.id,
        senderId: 'alice',
        senderDisplayName: 'Alice',
        origin: 'live',
        status: 'available',
        sentAt: now,
        receivedAt: now,
        parts: [{ type: 'text', text: 'What declarations are available from the saved records?' }],
      } as Message,
      scene,
      false,
    )
    await until(() => finished)
    assert.deepEqual(errors, [])
    assert.equal(replyerCalls, 1)
    assert.equal(sends, 1)
    assert.equal(plannerCalls, mode === 'omitted' ? 3 : 2)
    assert.ok(replyWire)
    assert.ok(
      engine.memory
        .adminList()
        .some((record) => record.id === history.id && record.status === 'superseded'),
      'unreturned history still exists; a current result is not the whole library',
    )
    assert.equal(JSON.stringify(replyWire).includes(historicalCanary), false)
    return {
      wire: replyWire,
      receipts: plannerReceipts,
      repairHint,
      questionRef,
      replySystem,
      initialEvidenceIds,
    }
  } finally {
    registration.dispose()
    try {
      await engine.dispose()
    } finally {
      store.close()
      rmSync(root, { recursive: true, force: true })
    }
  }
}
function assertScope(actual: Scope | undefined, expected: string) {
  assert.deepEqual(actual, { scope: expected, historyCoverage: 'not_established' })
}
function assertSummary(wire: ReplyWire, receipts: Receipt[], initialEvidenceIds: string[]) {
  assert.deepEqual(
    wire.coverageByEvidence.map((item) => item.id).sort(),
    [...initialEvidenceIds, ...receipts.map((item) => item.id)].sort(),
  )
  for (const item of wire.coverageByEvidence) {
    assert.deepEqual(
      Object.keys(item).sort(),
      [
        'id',
        ...(item.toolName ? ['toolName'] : []),
        ...(item.coverage ? ['coverage'] : []),
        'retrievalCoverage',
      ].sort(),
      'metadata index must not leak record IDs, bodies or raw sources',
    )
    assert.equal(item.retrievalCoverage.historyCoverage, 'not_established')
  }
  for (const receipt of receipts) {
    const summary = wire.coverageByEvidence.find((item) => item.id === receipt.id)!
    assert.ok(summary)
    assert.equal(summary.toolName, receipt.toolName)
    assert.deepEqual(summary.retrievalCoverage, receipt.retrievalCoverage)
    if (receipt.coverage) assert.deepEqual(summary.coverage, receipt.coverage)
  }
}
test('actual native Replyer receives server-owned query/person/requested-ID scope despite a planner claiming complete history', async (t) => {
  const { wire, receipts, questionRef, replySystem, initialEvidenceIds } = await run(t, 'complete')
  assertSummary(wire, receipts, initialEvidenceIds)
  for (const [tool, scope] of [
    ['memory.search', 'current_authorized_query_results'],
    ['memory.person', 'current_authorized_person_results'],
    ['messages.get', 'requested_message_ids'],
  ] as const) {
    const receipt = receipts.find((item) => item.toolName === tool)!
    const delivered = wire.evidence.find((item) => item.toolName === tool)!
    assertScope(receipt.retrievalCoverage, scope)
    assert.equal(
      Object.hasOwn(delivered, 'retrievalCoverage'),
      false,
      'the whole receipt scope lives in the server summary, not selected-record coverage',
    )
    if (tool !== 'messages.get') {
      assert.deepEqual(receipt.coverage, {
        scope: 'returned_tool_result',
        status: 'complete',
        returnedRecords: 2,
        omittedRecords: 0,
      })
      assert.equal(
        delivered.memories!.length,
        1,
        'the metadata counts the original two-record tool receipt, not the one selected record',
      )
      assert.equal(delivered.memories![0]!.content, 'Alice prefers jasmine tea.')
    } else {
      assert.equal((receipt.value as { coverage: string }).coverage, 'complete')
      assert.equal(delivered.resultCoverage, 'complete')
      assert.deepEqual(delivered.messageIds, [questionRef])
    }
  }
  assert.equal(replySystem.split(EVIDENCE_SCOPE_CONTRACT).length - 1, 1)
  // No keyword filter or semantic rejection is claimed: bad planner prose still is lower-priority data.
  assert.equal(wire.plan.strategy, badPlan.strategy)
  assert.deepEqual(wire.plan.constraints, badPlan.constraints)
  assert.deepEqual(wire.plan.expressionGoals, badPlan.expressionGoals)
})
test('empty current query results remain scoped metadata in the actual Replyer even when no memory was selected', async (t) => {
  const { wire, receipts, initialEvidenceIds } = await run(t, 'empty')
  assertSummary(wire, receipts, initialEvidenceIds)
  for (const receipt of receipts.filter((item) => item.toolName.startsWith('memory.'))) {
    assert.deepEqual(receipt.value, [])
    assert.deepEqual(receipt.coverage, {
      scope: 'returned_tool_result',
      status: 'complete',
      returnedRecords: 0,
      omittedRecords: 0,
    })
    assert.equal(receipt.retrievalCoverage.historyCoverage, 'not_established')
  }
  assert.equal(
    wire.evidence.some((item) => item.toolName.startsWith('memory.')),
    false,
  )
  assert.deepEqual(wire.plan.constraints, badPlan.constraints)
})
test('whole-page omission and one reference repair preserve both coverage axes through the actual final native request', async (t) => {
  const { wire, receipts, repairHint, initialEvidenceIds } = await run(t, 'omitted')
  assertSummary(wire, receipts, initialEvidenceIds)
  assert.ok(repairHint)
  assert.equal(repairHint.coverage, 'limited')
  assert.equal(repairHint.memorySelectionOptions.length, 2)
  for (const option of repairHint.memorySelectionOptions) {
    const receipt = receipts.find((item) => item.id === option.evidenceId)!
    assert.deepEqual(option.memoryIds, [])
    assert.equal(option.coverage, 'limited')
    assert.deepEqual(option.retrievalCoverage, receipt.retrievalCoverage)
    assert.deepEqual(option.receiptCoverage, {
      scope: 'returned_tool_result',
      status: 'limited',
      returnedRecords: 0,
      omittedRecords: 2,
      reason: 'planner_evidence_budget',
    })
  }
  assert.equal(wire.memoryEvidenceCoverage.status, 'omitted')
  assert.equal(wire.memoryEvidenceCoverage.omitted.length, 2)
  assert.equal(
    wire.evidence.some((item) => item.toolName.startsWith('memory.')),
    false,
  )
  assert.deepEqual(wire.plan.constraints, badPlan.constraints)
})
