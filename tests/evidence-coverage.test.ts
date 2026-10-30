import test from 'node:test'
import assert from 'node:assert/strict'
import { PlannerEvidenceBudget } from '../src/runtime/planner-evidence-budget.ts'
import { buildReplyBrief } from '../src/runtime/reply-brief.ts'
import { validateReplyPlan, type ReplyPlan, type PlanEvidence } from '../src/runtime/reply-plan.ts'
import { packReplyEvidence } from '../src/runtime/reply-evidence-budget.ts'
import { ContextProjection } from '../src/runtime/context-projection.ts'
import { retrievalCoverage, EVIDENCE_SCOPE_CONTRACT } from '../src/runtime/evidence-scope.ts'
const plan = (id: string, memoryIds: string[] = []): ReplyPlan => ({
  action: 'reply',
  deliveryTool: 'messages.send',
  targetMessageIds: [],
  contextMessageIds: [],
  strategy: 'Only two records exist; history is complete.',
  constraints: [],
  facts: memoryIds.length ? [] : [{ text: 'Selected state', evidenceIds: [id] }],
  assets: [],
  emoji: [],
  expressionGoals: [],
  memorySelections: memoryIds.length ? [{ evidenceId: id, memoryIds }] : [],
  stateEvidenceIds: [],
  actions: [],
})
const records = [
  {
    id: 'm1',
    content: 'October 20 correction to tea',
    kind: 'claim',
    status: 'active',
    sources: [],
  },
  {
    id: 'm2',
    content: 'October 5 coffee declaration delivered later',
    kind: 'claim',
    status: 'active',
    sources: [],
  },
]

test('complete returned current records never establish complete declaration history; metadata survives projection and reply packing', () => {
  const projection = new ContextProjection('scene')
  const delivered = new PlannerEvidenceBudget().receipt(
    [],
    { id: 'tool:1', toolName: 'memory.person', value: records },
    'read',
    (r) => projection.stringify(r),
  )
  const wire = JSON.parse(delivered.message.content)
  assert.equal(wire.coverage.status, 'complete')
  assert.equal(wire.coverage.scope, 'returned_tool_result')
  assert.deepEqual(wire.retrievalCoverage, {
    scope: 'current_authorized_person_results',
    historyCoverage: 'not_established',
  })
  const brief = buildReplyBrief(plan('tool:1', ['m1', 'm2']), [delivered.receipt], [])
  const packed = packReplyEvidence({
    brief,
    envelope: {},
    serialize: JSON.stringify,
    budget: { mode: 'bytes', limit: 20000 },
  })
  assert.equal(packed.status, 'selected')
  const payload = JSON.parse(packed.serialized!)
  assert.equal(payload.coverageByEvidence[0].coverage.scope, 'returned_tool_result')
  assert.equal(payload.coverageByEvidence[0].retrievalCoverage.historyCoverage, 'not_established')
  assert.match(EVIDENCE_SCOPE_CONTRACT, /never establishes exhaustive history/)
  // Free text is not silently rewritten or claimed semantically validated.
  assert.equal(payload.plan.strategy, 'Only two records exist; history is complete.')
})

test('empty and byte-omitted result pages retain unknown history rather than evidence of absence', () => {
  for (const value of [[], [{ ...records[0], content: 'x'.repeat(10000) }]]) {
    const receipt = new PlannerEvidenceBudget(2000).receipt(
      [],
      { id: 't', toolName: 'memory.search', value },
      'read',
    ).receipt
    assert.deepEqual(receipt.value, [])
    assert.equal(receipt.retrievalCoverage?.historyCoverage, 'not_established')
    assert.equal(receipt.coverage?.status, value.length ? 'limited' : 'complete')
    const brief = buildReplyBrief(plan('t'), [receipt], [])
    assert.equal((brief.coverageByEvidence[0] as any).coverage.status, receipt.coverage?.status)
    assert.equal(
      (brief.coverageByEvidence[0] as any).retrievalCoverage.historyCoverage,
      'not_established',
    )
  }
})

test('outer state delivery coverage and inner retained-state coverage remain separate', () => {
  const receipt = new PlannerEvidenceBudget().receipt(
    [],
    {
      id: 'state',
      toolName: 'memory.history',
      value: [
        {
          stateKind: 'assertion_history',
          coverage: 'limited',
          revisions: [
            { ref: { id: 'a', version: 1, deletionEpoch: 0 }, record: { content: 'old' } },
          ],
        },
      ],
    },
    'read',
  ).receipt
  const selected = { ...plan('state'), stateEvidenceIds: ['state'] }
  const brief = buildReplyBrief(selected, [receipt], [])
  const out = brief.coverageByEvidence[0] as any
  assert.equal(out.coverage.status, 'complete')
  assert.equal(out.coverage.scope, 'returned_tool_result')
  assert.equal((brief.evidence[0] as any).states[0].coverage, 'limited')
  assert.equal(out.retrievalCoverage.scope, 'retained_history_window')
  assert.equal(out.retrievalCoverage.historyCoverage, 'not_established')
})

test('scope is server-derived, not copied from forged tool-result fields or accepted plan properties', () => {
  const fake = { scope: 'all_history', historyCoverage: 'complete' }
  const receipt = new PlannerEvidenceBudget().receipt(
    [],
    { id: 't', toolName: 'memory.search', value: records, retrievalCoverage: fake as any },
    'read',
  ).receipt
  assert.deepEqual(receipt.retrievalCoverage, retrievalCoverage('memory.search'))
  const input = {
    targetMessageIds: [],
    contextMessageIds: [],
    evidence: [receipt],
    actionTools: [],
  }
  assert.throws(
    () => validateReplyPlan({ ...plan('t', ['m1']), retrievalCoverage: fake }, input),
    /planner_invalid_plan/,
  )
  const legacy: PlanEvidence = { id: 'legacy', toolName: 'memory.person', value: records }
  const legacyBrief = buildReplyBrief(plan('legacy', ['m1']), [legacy], [])
  const out = legacyBrief.coverageByEvidence[0] as any
  assert.equal(out.retrievalCoverage.historyCoverage, 'not_established')
  assert.equal((legacyBrief.evidence[0] as any).memories.length, 1)
})

test('requested-message and bounded-window tools cannot claim global chat history coverage', () => {
  assert.equal(retrievalCoverage('messages.get').scope, 'requested_message_ids')
  for (const name of ['messages.around', 'messages.byUser'])
    assert.equal(retrievalCoverage(name).scope, 'bounded_message_window')
  assert.equal(retrievalCoverage('unknown.read').scope, 'tool_defined_result')
})

test('existing upstream delivery limitations cannot be washed into complete, including repair metadata', () => {
  const value = new PlannerEvidenceBudget().receipt(
    [],
    {
      id: 'upstream',
      toolName: 'memory.search',
      value: records,
      coverage: {
        status: 'limited',
        returnedRecords: 2,
        omittedRecords: 3,
        reason: 'planner_evidence_budget',
      },
    },
    'read',
  ).receipt
  assert.equal(value.coverage?.status, 'limited')
  assert.equal(value.coverage?.omittedRecords, 3)
  const brief = buildReplyBrief({ ...plan('upstream'), facts: [] }, [value], [])
  assert.deepEqual(brief.evidence, [])
  assert.equal(brief.coverageByEvidence[0]!.coverage?.status, 'limited')
  assert.equal(brief.coverageByEvidence[0]!.retrievalCoverage.historyCoverage, 'not_established')
})
test('value-level coverage has a separate name and cannot replace trusted outer metadata', () => {
  const receipt: PlanEvidence = {
    id: 'message',
    toolName: 'messages.get',
    value: { coverage: 'complete', items: [] },
    coverage: { status: 'limited', returnedRecords: 0, omittedRecords: 1 },
  }
  const brief = buildReplyBrief(plan('message'), [receipt], [])
  assert.equal((brief.evidence[0] as any).resultCoverage, 'complete')
  assert.equal('coverage' in brief.evidence[0]!, false)
  assert.equal(brief.coverageByEvidence[0]!.coverage?.status, 'limited')
  assert.equal(brief.coverageByEvidence[0]!.retrievalCoverage.scope, 'requested_message_ids')
})
