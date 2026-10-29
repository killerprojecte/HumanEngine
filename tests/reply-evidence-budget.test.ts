import test from 'node:test'
import assert from 'node:assert/strict'
import { packReplyEvidence, type ReplyBrief } from '../src/runtime/reply-evidence-budget.ts'
const bytes = (value: string) => new TextEncoder().encode(value).length
const fixture = (): ReplyBrief =>
  ({
    plan: {
      action: 'reply',
      deliveryTool: 'messages.send',
      targetMessageIds: ['message'],
      contextMessageIds: ['message'],
      strategy: 'Explain selected evidence',
      constraints: ['Never invent confirmation'],
      facts: [
        { text: 'Memory fact', evidenceIds: ['m1'] },
        { text: 'Web fact', evidenceIds: ['web'] },
      ],
      assets: [],
      emoji: [],
      expressionGoals: [],
      replySuggestion: 'Potential memory-based draft',
    },
    evidence: [
      {
        id: 'm1',
        toolName: 'memory.search',
        memories: [
          {
            id: 'r1',
            content: '中文'.repeat(800) + '但尚未确认，不是永久习惯',
            status: 'disputed',
            validFrom: '2026-10-01',
            sources: [{ messageId: 'a' }, { messageId: 'b' }],
          },
        ],
      },
      { id: 'web', toolName: 'web-read.read', snippet: 'Public source' },
    ],
    imageContext: [
      {
        trust: 'untrusted',
        images: [{ messageId: 'message', status: 'unavailable', reason: 'budget_exhausted' }],
        omitted: [],
      },
    ],
    actionReceipts: [{ id: 'action', toolName: 'test', status: 'failed' }],
  }) as unknown as ReplyBrief
const envelope = {
  now: '2026-10-28T00:00:00Z',
  timezone: 'UTC',
  messages: [{ text: 'Question', sentAt: '2026-10-28T00:00:00Z' }],
}
const run = (brief: ReplyBrief, limit = 100000, extra = {}) =>
  packReplyEvidence({
    brief,
    envelope,
    serialize: JSON.stringify,
    budget: { mode: 'bytes', limit },
    ...extra,
  })
test('full memory and AND sources retain tail qualification and full envelope is exactly counted', () => {
  const brief = fixture(),
    before = JSON.stringify(brief),
    result = run(brief)
  assert.equal(result.status, 'selected')
  assert.ok(result.serialized!.includes('但尚未确认'))
  assert.equal(result.usage?.count, bytes(result.serialized!))
  assert.equal(JSON.stringify(brief), before)
  assert.deepEqual(result.brief?.evidence, brief.evidence)
  assert.equal(Object.isFrozen(result.brief?.plan), true)
})
test('optional oversized group drops dependent fact and draft, not fixed web facts or images/results/constraints', () => {
  const brief = fixture()
  const result = run(brief, 1800)
  assert.equal(result.status, 'selected')
  assert.equal(result.omitted.length, 1)
  assert.deepEqual(result.brief!.plan.facts, [brief.plan.facts[1]])
  assert.equal(result.brief!.plan.replySuggestion, undefined)
  assert.deepEqual(result.brief!.imageContext, brief.imageContext)
  assert.deepEqual(result.brief!.actionReceipts, brief.actionReceipts)
  assert.deepEqual(result.brief!.plan.constraints, brief.plan.constraints)
  assert.ok(result.serialized!.includes('omitted'))
  assert.ok(bytes(result.serialized!) <= 1800)
  assert.equal(run(brief, 10).status, 'insufficient')
  assert.equal(run(brief, 1800, { mandatoryEvidenceIds: ['m1'] }).status, 'insufficient')
})
test('cross-memory and memory+web fact closures are indivisible', () => {
  const brief = fixture()
  brief.evidence.push({
    id: 'm2',
    toolName: 'memory.person',
    memories: [{ id: 'r2', content: 'Counterclaim' }],
  } as never)
  brief.plan.facts = [
    { text: 'Both required with public context', evidenceIds: ['m1', 'm2', 'web'] },
  ]
  const limited = run(brief, 1800)
  assert.equal(limited.status, 'selected')
  assert.equal(limited.brief!.plan.facts.length, 0)
  assert.equal(limited.omitted.length, 2)
  assert.deepEqual(
    limited.brief!.evidence.map((e) => e.id),
    ['web'],
  )
  const full = run(brief)
  assert.equal(full.brief!.plan.facts.length, 1)
  assert.equal(full.omitted.length, 0)
})
test('memory-not-selected or empty receipts drop dependent fact and flag missing selection', () => {
  for (const change of [{ contentOmitted: 'memory_not_selected' }, { memories: [] }]) {
    const brief = fixture()
    brief.evidence[0] = { id: 'm1', toolName: 'memory.search', ...change } as never
    const result = run(brief)
    assert.equal(result.status, 'selected')
    assert.deepEqual(result.omitted, [{ evidenceId: 'm1', reason: 'missing_memory_selection' }])
    assert.deepEqual(result.brief!.plan.facts, [brief.plan.facts[1]])
  }
})
test('record count cannot hide behind one receipt or one linked group', () => {
  const brief = fixture()
  brief.plan.facts = []
  brief.evidence = [
    {
      id: 'm1',
      toolName: 'memory.search',
      memories: Array.from({ length: 13 }, (_, i) => ({ id: String(i), content: 'Complete ' + i })),
    },
  ] as never
  const result = run(brief)
  assert.equal(result.status, 'selected')
  assert.equal(result.omitted.length, 1)
  assert.equal(result.omitted[0]!.reason, 'record_limit')
  assert.equal(result.brief!.evidence.length, 0)
  assert.equal(
    run(brief, 100000, { mandatoryEvidenceIds: ['m1'] }).reason,
    'mandatory_record_limit',
  )
  ;(brief.evidence[0] as unknown as { memories: unknown[] }).memories = Array.from(
    { length: 41 },
    () => ({ content: 'x' }),
  )
  assert.equal(run(brief).reason, 'candidate_record_limit')
})
test('missing mandatory and throwing or nondeterministic serializer fail closed without body echoes', () => {
  assert.equal(run(fixture(), 100000, { mandatoryEvidenceIds: ['absent'] }).status, 'invalid')
  assert.equal(
    run(fixture(), 100000, {
      serialize: () => {
        throw new Error('private body')
      },
    }).status,
    'invalid',
  )
  let count = 0
  assert.equal(
    run(fixture(), 100000, {
      serialize: (payload: unknown) => JSON.stringify({ payload, count: count++ }),
    }).status,
    'invalid',
  )
})

test('omission removes freeworded strategy/goals/draft canaries while retaining safety constraints', () => {
  const brief = fixture()
  brief.plan.strategy = 'STRATEGY_MEMORY_CANARY'
  brief.plan.expressionGoals = ['GOAL_MEMORY_CANARY']
  brief.plan.replySuggestion = 'DRAFT_MEMORY_CANARY'
  const result = run(brief, 1800)
  assert.equal(result.status, 'selected')
  assert.ok(!result.serialized!.includes('MEMORY_CANARY'))
  assert.deepEqual(result.brief!.plan.expressionGoals, [])
  assert.deepEqual(result.brief!.plan.constraints, brief.plan.constraints)
  assert.deepEqual(result.brief!.plan.facts, [brief.plan.facts[1]])
})

test('upstream limited receipt remains disclosed with no selected memories or facts', () => {
  const brief = fixture()
  brief.evidence = []
  brief.plan.facts = []
  brief.plan.replySuggestion = 'UNSELECTED_UPSTREAM_DRAFT'
  const upstreamOmissions = [
    { evidenceId: 'upstream', reason: 'planner_evidence_budget' as const, omittedRecords: 3 },
  ]
  const result = run(brief, 3000, { upstreamOmissions })
  assert.equal(result.status, 'selected')
  assert.deepEqual(result.omitted, upstreamOmissions)
  assert.equal(result.brief!.plan.replySuggestion, undefined)
  assert.deepEqual(result.brief!.plan.expressionGoals, [])
  const payload = JSON.parse(result.serialized!)
  assert.equal(payload.memoryEvidenceCoverage.status, 'omitted')
  assert.deepEqual(payload.memoryEvidenceCoverage.omitted, upstreamOmissions)
  assert.equal(result.usage?.count, bytes(result.serialized!))
  assert.ok(!result.serialized!.includes('UNSELECTED_UPSTREAM_DRAFT'))
})
test('upstream omission wins duplicate receipt attribution and malformed counts fail closed', () => {
  const brief = fixture()
  brief.evidence[0] = {
    id: 'm1',
    toolName: 'memory.search',
    contentOmitted: 'memory_not_selected',
  } as never
  const upstreamOmissions = [
    { evidenceId: 'm1', reason: 'planner_evidence_budget' as const, omittedRecords: 2 },
  ]
  assert.deepEqual(run(brief, 3000, { upstreamOmissions }).omitted, upstreamOmissions)
  for (const omittedRecords of [NaN, -1, 0, 1.5])
    assert.equal(
      run(brief, 3000, { upstreamOmissions: [{ ...upstreamOmissions[0], omittedRecords }] }).status,
      'invalid',
    )
})
