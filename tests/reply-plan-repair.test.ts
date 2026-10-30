import test from 'node:test'
import assert from 'node:assert/strict'
import { buildReplyPlan, PLANNER_MAX_ROUNDS } from '../src/runtime/reply-plan.ts'
import { ContextProjection } from '../src/runtime/context-projection.ts'
import type { Completion } from '../src/models.ts'
const plan = (selection: unknown = []) => ({
  action: 'reply',
  deliveryTool: 'messages.send',
  targetMessageIds: [],
  contextMessageIds: [],
  strategy: 'Use selected memory',
  constraints: [],
  facts: [],
  assets: [],
  emoji: [],
  expressionGoals: [],
  memorySelections: selection,
  stateEvidenceIds: [],
  actions: [],
})
const result = (name: string, args: unknown): Completion => {
  const toolCalls = [{ id: 'call', name, arguments: args as Record<string, unknown> }]
  return {
    text: '',
    toolCalls,
    modelId: 'fixture',
    usage: { inputTokens: 0, outputTokens: 0, totalTokens: 0 },
    assistant: { role: 'assistant', content: '', toolCalls },
  }
}
function fixture() {
  const projection = new ContextProjection('scene')
  let calls = 0,
    reads = 0,
    deliveries = 0,
    eligible = true
  const input: Parameters<typeof buildReplyPlan>[0] = {
    messages: [{ role: 'user', content: 'Synthetic context' }],
    projection,
    readTools: [{ name: 'memory.search', description: 'Read', inputSchema: {} }],
    actionTools: [],
    evidence: [],
    targetMessageIds: [],
    contextMessageIds: [],
    signal: new AbortController().signal,
    eligible: () => eligible,
    execute: async () => {
      reads++
      return [
        {
          id: 'canonical-memory-record-with-long-uuid-001',
          content: 'SYNTHETIC_CONTENT_NOT_IN_REPAIR',
        },
      ]
    },
    deliver: (_name, value) => {
      deliveries++
      return value
    },
    complete: async (messages, tools) => {
      calls++
      if (calls === 1) return result('memory.search', {})
      const receipt = JSON.parse(messages.find((m) => m.role === 'tool')!.content)
      if (calls === 2)
        return result('reply.plan', plan([{ evidenceId: receipt.id, memoryIds: [receipt.id] }]))
      assert.deepEqual(
        tools.map((t) => t.name),
        ['reply.plan'],
      )
      const hint = JSON.parse(messages.at(-1)!.content)
      assert.equal(hint.error.code, 'planner_unknown_memory_evidence')
      assert.equal(JSON.stringify(hint).includes('SYNTHETIC_CONTENT'), false)
      assert.equal(hint.memorySelectionOptions[0].evidenceId, receipt.id)
      assert.deepEqual(hint.memorySelectionOptions[0].memoryIds, [receipt.value[0].id])
      return result(
        'reply.plan',
        plan([{ evidenceId: receipt.id, memoryIds: [receipt.value[0].id] }]),
      )
    },
  }
  return {
    input,
    counts: () => ({ calls, reads, deliveries }),
    revoke: () => {
      eligible = false
    },
  }
}
test('actual compact receipt ID confusion gets one terminal-only repair without new exposure', async () => {
  const f = fixture()
  const value = await buildReplyPlan(f.input)
  assert.equal(
    value.plan.memorySelections[0]!.memoryIds[0],
    'canonical-memory-record-with-long-uuid-001',
  )
  assert.deepEqual(f.counts(), { calls: 3, reads: 1, deliveries: 1 })
  assert.equal(value.rounds, 3)
})
test('second invalid plan fails, never automatically remaps a reference', async () => {
  const f = fixture(),
    complete = f.input.complete
  f.input.complete = async (...args) => {
    const value = await complete(...args)
    if (f.counts().calls === 3)
      return result('reply.plan', plan([{ evidenceId: 'unknown', memoryIds: ['unknown'] }]))
    return value
  }
  await assert.rejects(buildReplyPlan(f.input), /context_reference_unknown/)
  assert.deepEqual(f.counts(), { calls: 3, reads: 1, deliveries: 1 })
})
test('repair cannot execute read or action calls even if a model ignores terminal-only tool list', async () => {
  for (const name of ['memory.search', 'learning.propose']) {
    const f = fixture(),
      complete = f.input.complete
    f.input.complete = async (...args) => {
      const value = await complete(...args)
      return f.counts().calls === 3 ? result(name, {}) : value
    }
    await assert.rejects(buildReplyPlan(f.input), /planner_forbidden_tool/)
    assert.equal(f.counts().reads, 1)
  }
})
test('revocation while repair awaits rejects repaired output before return', async () => {
  const f = fixture(),
    complete = f.input.complete
  f.input.complete = async (...args) => {
    const value = await complete(...args)
    if (f.counts().calls === 3) f.revoke()
    return value
  }
  await assert.rejects(buildReplyPlan(f.input), /planner_context_ineligible/)
})
test('provider failure is not an implicit repair or retry', async () => {
  const f = fixture()
  let calls = 0
  f.input.complete = async () => {
    calls++
    throw Error('provider_failure')
  }
  await assert.rejects(buildReplyPlan(f.input), /provider_failure/)
  assert.equal(calls, 1)
})
test('repair uses original byte budget and original six-round maximum', async () => {
  const f = fixture()
  f.input.evidenceMaxBytes = 200
  await assert.rejects(buildReplyPlan(f.input), /planner_evidence_budget_exhausted/)
  const g = fixture()
  let calls = 0
  g.input.complete = async () => {
    calls++
    return calls < PLANNER_MAX_ROUNDS ? result('memory.search', {}) : result('reply.plan', {})
  }
  await assert.rejects(buildReplyPlan(g.input), /planner_invalid_plan/)
  assert.equal(calls, PLANNER_MAX_ROUNDS)
})
test('schema explicitly distinguishes outer receipt and inner record IDs', async () => {
  const f = fixture()
  f.input.complete = async (_messages, tools) => {
    const schema = JSON.stringify(tools.find((t) => t.name === 'reply.plan')!.inputSchema)
    assert.match(schema, /Outer delivered tool receipt.id/)
    assert.match(schema, /Inner record.id/)
    return result('reply.plan', plan())
  }
  await buildReplyPlan(f.input)
})

test('captured e4/e7 receipts and e5/e6 records repair e5-as-receipt without guessing', async () => {
  const f = fixture()
  const projection = f.input.projection!
  for (let i = 0; i < 3; i++)
    projection.project({ evidenceId: `prior-canonical-evidence-identifier-long-${i}` })
  let calls = 0
  f.input.readTools.push({ name: 'memory.person', description: 'Read', inputSchema: {} })
  f.input.execute = async () => [
    { id: 'first-long-canonical-memory-record-000001' },
    { id: 'second-long-canonical-memory-record-000002' },
  ]
  f.input.complete = async (messages, tools) => {
    calls++
    if (calls === 1) return result('memory.search', {})
    if (calls === 2) return result('memory.person', {})
    const receipts = messages
      .filter((m) => m.role === 'tool')
      .slice(0, 2)
      .map((m) => JSON.parse(m.content))
    assert.deepEqual(
      receipts.map((r) => r.id),
      ['e4', 'e7'],
    )
    assert.deepEqual(
      receipts[0].value.map((r: { id: string }) => r.id),
      ['e5', 'e6'],
    )
    if (calls === 3) return result('reply.plan', plan([{ evidenceId: 'e5', memoryIds: ['e5'] }]))
    assert.deepEqual(
      tools.map((t) => t.name),
      ['reply.plan'],
    )
    const hint = JSON.parse(messages.at(-1)!.content)
    assert.deepEqual(
      hint.memorySelectionOptions.map((r: { evidenceId: string; memoryIds: string[] }) => [
        r.evidenceId,
        r.memoryIds,
      ]),
      [
        ['e4', ['e5', 'e6']],
        ['e7', ['e5', 'e6']],
      ],
    )
    return result('reply.plan', plan([{ evidenceId: 'e7', memoryIds: ['e5'] }]))
  }
  const output = await buildReplyPlan(f.input)
  assert.equal(output.plan.memorySelections[0]!.evidenceId, 'tool:2')
  assert.equal(
    output.plan.memorySelections[0]!.memoryIds[0],
    'first-long-canonical-memory-record-000001',
  )
  assert.equal(calls, 4)
})
test('OFF then ON revision ABA during repair cannot resurrect a captured read ticket', async () => {
  const f = fixture()
  let revision = 0
  const captured = revision
  f.input.eligible = () => revision === captured
  const complete = f.input.complete
  f.input.complete = async (...args) => {
    const value = await complete(...args)
    if (f.counts().calls === 3) {
      revision++
      revision++
    }
    return value
  }
  await assert.rejects(buildReplyPlan(f.input), /planner_context_ineligible/)
})
test('ambiguous finals and forbidden actions never gain a repair opportunity', async () => {
  for (const ambiguous of [false, true]) {
    const f = fixture()
    let calls = 0
    f.input.complete = async () => {
      calls++
      const value = result('reply.plan', {
        ...plan(),
        actions: ambiguous ? [] : [{ toolName: 'forbidden', arguments: {} }],
      })
      if (ambiguous) value.toolCalls.push({ id: 'other', name: 'memory.search', arguments: {} })
      return value
    }
    await assert.rejects(
      buildReplyPlan(f.input),
      ambiguous ? /planner_ambiguous_plan/ : /planner_forbidden_action/,
    )
    assert.equal(calls, 1)
  }
})

test('repair preserves upstream omitted coverage and never exposes omitted record IDs', async () => {
  const f = fixture()
  f.input.evidenceMaxBytes = 4000
  f.input.execute = async () => [{ id: 'never-delivered-memory', content: 'x'.repeat(10000) }]
  let calls = 0
  f.input.complete = async (messages) => {
    calls++
    if (calls === 1) return result('memory.search', {})
    const receipt = JSON.parse(messages.find((m) => m.role === 'tool')!.content)
    assert.deepEqual(receipt.value, [])
    if (calls === 2)
      return result('reply.plan', plan([{ evidenceId: receipt.id, memoryIds: [receipt.id] }]))
    const hint = JSON.parse(messages.at(-1)!.content)
    assert.equal(hint.coverage, 'limited')
    assert.equal(hint.memorySelectionOptions[0].coverage, 'limited')
    assert.equal(hint.memorySelectionOptions[0].receiptCoverage.omittedRecords, 1)
    assert.deepEqual(hint.memorySelectionOptions[0].memoryIds, [])
    assert.equal(JSON.stringify(hint).includes('never-delivered-memory'), false)
    return result('reply.plan', plan())
  }
  await buildReplyPlan(f.input)
  assert.equal(calls, 3)
})

test('repair cannot introduce an otherwise permitted action', async () => {
  const f = fixture(),
    complete = f.input.complete
  f.input.actionTools = [{ name: 'learning.propose', description: 'Candidate', inputSchema: {} }]
  f.input.complete = async (...args) => {
    const value = await complete(...args)
    if (f.counts().calls === 3)
      value.toolCalls[0]!.arguments.actions = [{ toolName: 'learning.propose', arguments: {} }]
    return value
  }
  await assert.rejects(buildReplyPlan(f.input), /planner_forbidden_action/)
  assert.equal(f.counts().reads, 1)
})
test('repair retains the exact original valid action plan for later ordinary permission checks', async () => {
  const f = fixture(),
    complete = f.input.complete
  const actions = [{ toolName: 'learning.propose', arguments: { content: 'Synthetic candidate' } }]
  f.input.actionTools = [{ name: 'learning.propose', description: 'Candidate', inputSchema: {} }]
  f.input.complete = async (...args) => {
    const value = await complete(...args)
    if (f.counts().calls >= 2) value.toolCalls[0]!.arguments.actions = structuredClone(actions)
    return value
  }
  const output = await buildReplyPlan(f.input)
  assert.deepEqual(output.plan.actions, actions)
  assert.equal(f.counts().reads, 1)
})
