import test from 'node:test'
import assert from 'node:assert/strict'
import {
  buildReplyPlan,
  validateReplyPlan,
  type ReplyPlan,
  type PlanEvidence,
} from '../src/runtime/reply-plan.ts'
import { buildReplyBrief } from '../src/runtime/reply-brief.ts'
import { PlannerEvidenceBudget } from '../src/runtime/planner-evidence-budget.ts'
import { packReplyEvidence } from '../src/runtime/reply-evidence-budget.ts'
import { validWorkspaceState } from '../src/runtime/workspace-state-evidence.ts'
const ref = { id: 'record', version: 2, deletionEpoch: 0 }
const record = {
  id: 'record',
  kind: 'experience',
  content: 'Reported trip; details unconfirmed',
  status: 'disputed',
  epistemicKind: 'reported',
  learnedAt: '2026-10-28T00:00:00.000Z',
  validFrom: '2026-10-01T00:00:00.000Z',
  validUntil: '2026-11-01T00:00:00.000Z',
  scope: { kind: 'scene', sceneId: 's', personId: 'p' },
  sources: [{ messageId: 'm', senderId: 'p' }],
}
const working = () => ({
  stateKind: 'working_set',
  page: {
    enabled: true,
    items: [
      {
        kind: 'assertion',
        snapshot: { ref, qualificationKey: 'PRIVATE_QUALIFICATION', record },
        accessibility: {
          state: 'hot',
          reviewDue: true,
          pinIntent: true,
          pinStatus: 'stale',
          policyCoverage: 'unknown',
          contextSelectedCount: 2,
          lastSelectedAt: null,
          dependencyKey: 'PRIVATE_DEPENDENCY',
        },
      },
    ],
    coverage: 'limited',
    omittedCold: 2,
    omittedEntries: 1,
    interpretation: 'accessibility_not_truth_or_absence',
  },
})
const episodes = () => ({
  stateKind: 'experience_view',
  page: {
    items: [
      {
        kind: 'reported_experience',
        assertionRef: ref,
        qualificationKey: 'PRIVATE_QUALIFICATION',
        record,
        time: { occurredAt: null, recordedAt: record.learnedAt },
      },
    ],
    coverage: { status: 'limited', reasons: ['upstream_limited'], returned: 1, omittedEntries: 0 },
    interpretation: 'independent_qualified_records_not_synthesized_episodes',
    sourceBodyCoverage: 'unavailable',
  },
})
const plan = (ids: string[]): ReplyPlan => ({
  action: 'reply',
  deliveryTool: 'messages.send',
  targetMessageIds: ['m'],
  contextMessageIds: ['m'],
  strategy: 'Use qualified evidence',
  constraints: [],
  facts: [],
  assets: [],
  emoji: [],
  expressionGoals: [],
  memorySelections: [],
  stateEvidenceIds: ids,
  actions: [],
})
const receipt = (toolName: string, value: unknown): PlanEvidence => ({
  id: 'e',
  toolName,
  value: [value],
})
const validate = (r: PlanEvidence) =>
  validateReplyPlan(plan(['e']), {
    targetMessageIds: ['m'],
    contextMessageIds: ['m'],
    evidence: [r],
    actionTools: [],
  })
test('two whole-page tools validate independently, including explicit empty coverage; mixed or oversized state rejects', () => {
  for (const [tool, make] of [
    ['memory.workingSet', working],
    ['memory.episodes', episodes],
  ] as const) {
    const page = make()
    assert.equal(validWorkspaceState(tool, page), true)
    validate(receipt(tool, page))
    const empty = structuredClone(page)
    empty.page.items = []
    if (typeof empty.page.coverage === 'object') empty.page.coverage.returned = 0
    validate(receipt(tool, empty))
    assert.throws(
      () => validate(receipt(tool, { ...page, stateKind: 'assertion_history' })),
      /unknown_state/,
    )
    assert.throws(() => validate({ ...receipt(tool, page), value: [page, page] }), /unknown_state/)
    const over = structuredClone(page)
    over.page.items = Array(13).fill(page.page.items[0])
    assert.throws(() => validate(receipt(tool, over)), /unknown_state/)
  }
})
test('brief retains exact selected references, full record qualifications and unknown event time, not internal proof hashes', () => {
  for (const [tool, value] of [
    ['memory.workingSet', working()],
    ['memory.episodes', episodes()],
  ] as const) {
    const brief = buildReplyBrief(validate(receipt(tool, value)), [receipt(tool, value)], [])
    const state = (brief.evidence[0] as any).states[0]
    assert.equal(validWorkspaceState(tool, state), true)
    assert.ok(JSON.stringify(brief).includes('Reported trip; details unconfirmed'))
    assert.ok(JSON.stringify(brief).includes('disputed'))
    assert.ok(JSON.stringify(brief).includes(record.validUntil))
    assert.ok(!JSON.stringify(brief).includes('PRIVATE_'))
    if (tool === 'memory.episodes') assert.equal(state.page.items[0].time.occurredAt, null)
    else assert.deepEqual(state.page.items[0].snapshot.ref, ref)
  }
})
test('planner byte admission omits complete page atomically and prevents selecting omitted state', () => {
  const value = episodes()
  value.page.items[0]!.record = { ...record, content: 'x'.repeat(4000) }
  const r = receipt('memory.episodes', value),
    budget = new PlannerEvidenceBudget(600)
  const result = budget.receipt([], r, 'call')
  assert.deepEqual(result.receipt.value, [])
  assert.equal(result.receipt.coverage?.omittedRecords, 1)
  assert.throws(() => validate(result.receipt), /unknown_state/)
  assert.ok(budget.measure([result.message]) <= 600)
})
test('reply packing drops whole oversized state plus dependent fact and draft, not half a page', () => {
  const value = episodes()
  value.page.items[0]!.record = { ...record, content: 'large qualified content '.repeat(400) }
  const p = {
    ...plan(['e']),
    facts: [{ text: 'qualified statement', evidenceIds: ['e'] }],
    replySuggestion: 'must disappear',
  }
  const brief = buildReplyBrief(p, [receipt('memory.episodes', value)], [])
  const result = packReplyEvidence({
    brief,
    envelope: {},
    serialize: JSON.stringify,
    budget: { mode: 'bytes', limit: 1800 },
  })
  assert.equal(result.status, 'selected')
  assert.ok(result.omitted.some((o) => o.evidenceId === 'e'))
  assert.deepEqual(result.brief!.plan.facts, [])
  assert.equal(result.brief!.plan.replySuggestion, undefined)
  assert.ok(!result.serialized!.includes('large qualified content'))
})

test('actual Planner read loop receives whole qualified workspace receipt and selects it for Replyer', async () => {
  for (const [tool, value] of [
    ['memory.workingSet', working()],
    ['memory.episodes', episodes()],
  ] as const) {
    let calls = 0
    const completion = (name: string, args: Record<string, unknown>) => {
      const toolCalls = [{ id: 'call-' + calls, name, arguments: args }]
      return {
        text: '',
        modelId: 'synthetic',
        toolCalls,
        usage: { inputTokens: 0, outputTokens: 0, totalTokens: 0 },
        assistant: { role: 'assistant' as const, content: '', toolCalls },
      }
    }
    const result = await buildReplyPlan({
      messages: [{ role: 'user', content: 'synthetic qualified history question' }],
      readTools: [{ name: tool, description: 'Synthetic authorized read', inputSchema: {} }],
      actionTools: [],
      evidence: [],
      targetMessageIds: ['m'],
      contextMessageIds: ['m'],
      signal: new AbortController().signal,
      eligible: () => true,
      execute: async () => [value],
      complete: async (messages) => {
        calls++
        if (calls === 1) return completion(tool, {})
        const r = JSON.parse(messages.at(-1)!.content)
        assert.deepEqual(r.value, [value])
        return completion('reply.plan', plan([r.id]) as unknown as Record<string, unknown>)
      },
    })
    assert.equal(calls, 2)
    const brief = buildReplyBrief(result.plan, result.evidence, [])
    assert.equal((brief.evidence[0] as any).states[0].stateKind, value.stateKind)
  }
})

test('shared plan declaration preserves every condition and self-report completion caveat without becoming an observed episode', () => {
  const view = {
    ref,
    mutationVersion: 3,
    sceneId: 's',
    confirmation: 'incomplete',
    lifecycle: 'done',
    qualification: 'declared_not_verified',
    sourceBodyCoverage: 'unavailable',
    terms: {
      kind: 'plan',
      title: 'Conditional dinner',
      participantIds: ['p', 'q'],
      conditions: ['Only if both confirm', 'Unless cancelled'],
      polarity: 'positive',
      modality: 'possible',
      temporalScope: 'past',
      timeQualification: 'Date unknown',
    },
    evidence: [
      {
        kind: 'complete',
        personId: 'p',
        backing: ref,
        proofs: [{ id: 'PRIVATE_PROOF', sources: [{ messageId: 'm', senderId: 'p' }] }],
      },
    ],
    requiredProposalGroups: [],
  }
  const page = {
    items: [
      {
        kind: 'shared_plan_declaration',
        commitmentRef: { ...ref, mutationVersion: 3 },
        view,
        completionBasis: 'participant_self_report_not_observed_event',
      },
    ],
    coverage: { status: 'complete', reasons: [], returned: 1, omittedEntries: 0 },
    interpretation: 'independent_qualified_records_not_synthesized_episodes',
    sourceBodyCoverage: 'unavailable',
  }
  const r = receipt('memory.episodes', { stateKind: 'experience_view', page })
  const brief = buildReplyBrief(validate(r), [r], [])
  const item = (brief.evidence[0] as any).states[0].page.items[0]
  assert.deepEqual(item.view.terms, view.terms)
  assert.equal(item.completionBasis, 'participant_self_report_not_observed_event')
  assert.equal(item.view.confirmation, 'incomplete')
  assert.ok(!JSON.stringify(brief).includes('PRIVATE_PROOF'))
  assert.equal(item.view.terms.occurredAt, undefined)
})
