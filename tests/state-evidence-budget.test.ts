import assert from 'node:assert/strict'
import test from 'node:test'
import { ReplyPlanSchema, validateReplyPlan, type PlanEvidence } from '../src/runtime/reply-plan.ts'
import { buildReplyBrief } from '../src/runtime/reply-brief.ts'
import { packReplyEvidence } from '../src/runtime/reply-evidence-budget.ts'
import { PlannerEvidenceBudget } from '../src/runtime/planner-evidence-budget.ts'
const state = (tail = 'ONLY IF BOTH CONFIRM') => ({
  stateKind: 'commitment',
  view: {
    ref: { id: 'plan', version: 2, deletionEpoch: 0 },
    mutationVersion: 4,
    sceneId: 'scene',
    terms: {
      kind: 'plan',
      title: 'Synthetic meeting',
      participantIds: ['a', 'b'],
      polarity: 'positive',
      modality: 'possible',
      temporalScope: 'future',
      conditions: [tail],
      timeQualification: '19:00 date unknown',
    },
    confirmation: 'incomplete',
    lifecycle: 'needs_review',
    qualification: 'declared_not_verified',
    sourceBodyCoverage: 'unavailable',
    changed: true,
    confirmedParticipantIds: ['a'],
    missingParticipantIds: ['b'],
    completedParticipantIds: [],
    cancelledParticipantIds: [],
    evidence: [
      {
        kind: 'confirm',
        personId: 'a',
        backing: { id: 'assertion', version: 1, deletionEpoch: 0 },
        proofs: [
          {
            privateCanary: 'RAW_PROOF_SECRET',
            sources: [{ sceneId: 'scene', messageId: 'm', senderId: 'a' }],
          },
        ],
      },
    ],
  },
})
const plan = (stateEvidenceIds: string[] = []) =>
  ReplyPlanSchema.parse({
    action: 'reply',
    targetMessageIds: ['m'],
    contextMessageIds: ['m'],
    strategy: 'STRATEGY_CANARY',
    constraints: ['Never remove negative conditions'],
    facts: [
      { text: 'Qualified state fact', evidenceIds: ['s'] },
      { text: 'Web-only fact', evidenceIds: ['w'] },
    ],
    assets: [],
    emoji: [],
    expressionGoals: ['GOAL_CANARY'],
    replySuggestion: 'DRAFT_CANARY',
    actions: [],
    stateEvidenceIds,
  })
const evidence = (item = state()): PlanEvidence[] => [
  { id: 's', toolName: 'memory.commitments', value: [item] },
  {
    id: 'w',
    toolName: 'web-read.read',
    value: { title: 'Public citation', sourceUrl: 'https://example.invalid/', trust: 'untrusted' },
  },
]
test('explicit state selection validates actual receipt, defaults compatible, no empty or guessed state', () => {
  assert.deepEqual(
    ReplyPlanSchema.parse({ ...plan(), stateEvidenceIds: undefined }).stateEvidenceIds,
    [],
  )
  const input = {
    targetMessageIds: ['m'],
    contextMessageIds: ['m'],
    evidence: evidence(),
    actionTools: [],
    replySuggestionsEnabled: true,
  }
  assert.deepEqual(validateReplyPlan(plan(['s']), input).stateEvidenceIds, ['s'])
  for (const ids of [['s', 's'], ['w'], ['missing']])
    assert.throws(() => validateReplyPlan(plan(ids), input), /state_evidence/)
  assert.throws(
    () =>
      validateReplyPlan(plan(['s']), {
        ...input,
        evidence: [{ id: 's', toolName: 'memory.commitments', value: [] }, input.evidence[1]!],
      }),
    /state_evidence/,
  )
})
test('full state qualifiers survive projection; raw proofs are absent', () => {
  const brief = buildReplyBrief(plan(['s']), evidence(), [])
  const result = packReplyEvidence({
    brief,
    envelope: { now: '2026-10-29T00:00:00Z', timezone: 'UTC', messages: [] },
    serialize: JSON.stringify,
    budget: { mode: 'bytes', limit: 10000 },
  })
  assert.equal(result.status, 'selected')
  assert.match(result.serialized!, /ONLY IF BOTH CONFIRM/)
  assert.match(result.serialized!, /incomplete/)
  assert.match(result.serialized!, /needs_review/)
  assert.match(result.serialized!, /date unknown/)
  assert.doesNotMatch(result.serialized!, /RAW_PROOF_SECRET|"proofs"/)
  assert.equal(result.usage!.count, Buffer.byteLength(result.serialized!, 'utf8'))
})
test('oversized whole state and dependent facts omitted together, retaining unrelated web facts', () => {
  const brief = buildReplyBrief(
    plan(['s']),
    evidence(state('很长的条件'.repeat(5000) + '不可遗漏否定')),
    [],
  )
  const result = packReplyEvidence({
    brief,
    envelope: { messages: [] },
    serialize: JSON.stringify,
    budget: { mode: 'bytes', limit: 1800 },
  })
  assert.equal(result.status, 'selected')
  assert.ok(result.omitted.some((item) => item.evidenceId === 's'))
  assert.doesNotMatch(
    result.serialized!,
    /Qualified state fact|DRAFT_CANARY|STRATEGY_CANARY|GOAL_CANARY|不可遗漏否定/,
  )
  assert.match(result.serialized!, /Web-only fact/)
  assert.match(result.serialized!, /Never remove negative conditions/)
  assert.ok(Buffer.byteLength(result.serialized!, 'utf8') <= 1800)
  const missing = packReplyEvidence({
    brief: buildReplyBrief(plan(), evidence(), []),
    envelope: {},
    serialize: JSON.stringify,
    budget: { mode: 'bytes', limit: 10000 },
  })
  assert.equal(missing.omitted[0]!.reason, 'missing_state_selection')
  assert.doesNotMatch(missing.serialized!, /Qualified state fact/)
})
test('planner state receipts are all-or-empty, and upstream omission disables draft even unselected', () => {
  const receipt = {
    id: 's',
    toolName: 'memory.commitments',
    value: [state('x'.repeat(10000)), state('negative qualifier')],
  }
  const measured = new PlannerEvidenceBudget(1000).receipt([], receipt, 'call')
  assert.deepEqual(measured.receipt.value, [])
  assert.equal(measured.receipt.coverage!.omittedRecords, 2)
  const minimal = plan()
  minimal.facts = []
  const packed = packReplyEvidence({
    brief: buildReplyBrief(minimal, [], []),
    envelope: {},
    serialize: JSON.stringify,
    budget: { mode: 'bytes', limit: 2000 },
    upstreamOmissions: [{ evidenceId: 's', reason: 'planner_evidence_budget', omittedRecords: 2 }],
  })
  assert.equal(packed.status, 'selected')
  assert.doesNotMatch(packed.serialized!, /DRAFT_CANARY|STRATEGY_CANARY/)
  assert.match(packed.serialized!, /planner_evidence_budget/)
})
test('history preserves exact revision, old-time uncertainty and full assertion conditions, never source body', () => {
  const history = {
    stateKind: 'assertion_history',
    kind: 'assertion_history',
    interpretation: 'previously_recorded_not_current_truth',
    sourceBodyCoverage: 'unavailable',
    asOfRecorded: '2026-10-28T00:00:00Z',
    coverage: { status: 'limited', reasons: ['legacy_import'], returned: 1, hasMore: false },
    revisions: [
      {
        assertionId: 'old',
        version: 1,
        ref: { id: 'old', version: 1, deletionEpoch: 0 },
        recordedAt: null,
        validFrom: null,
        validTo: null,
        validityCoverage: 'unknown',
        record: {
          id: 'old',
          kind: 'claim',
          content: 'At that time 18:00, not a current agreement.',
          status: 'superseded',
          epistemicKind: 'reported',
          supports: 'RAW_HISTORY_SECRET',
        },
        supports: [{ raw: 'RAW_HISTORY_SECRET' }],
      },
    ],
  }
  const brief = buildReplyBrief(
    plan(['s']),
    [{ id: 's', toolName: 'memory.history', value: [history] }, evidence()[1]!],
    [],
  )
  const result = packReplyEvidence({
    brief,
    envelope: {},
    serialize: JSON.stringify,
    budget: { mode: 'bytes', limit: 10000 },
  })
  assert.equal(result.status, 'selected')
  assert.match(result.serialized!, /not a current agreement/)
  assert.match(result.serialized!, /previously_recorded_not_current_truth/)
  assert.match(result.serialized!, /legacy_import/)
  assert.doesNotMatch(result.serialized!, /RAW_HISTORY_SECRET/)
})

test('commitment history retains complete prior terms and inherited AND/OR declarations; omission closes dependent facts', () => {
  const old = state('18:00 only if both agree').view
  const view = {
    ...old,
    requiredProposalGroups: [[old.evidence[0]], [old.evidence[0], old.evidence[0]]],
  }
  const receipt: PlanEvidence = {
    id: 's',
    toolName: 'memory.commitmentHistory',
    value: [
      {
        stateKind: 'commitment_history',
        anchor: { ...view.ref, mutationVersion: 4 },
        revisions: [view],
        coverage: 'limited',
        interpretation: 'previously_recorded_not_current_truth',
        sourceBodyCoverage: 'unavailable',
        historyWindow: { maxRevisions: 20, omittedRevisions: 0, throughVersion: 2 },
      },
    ],
  }
  const receipts = [receipt, evidence()[1]!]
  assert.doesNotThrow(() =>
    validateReplyPlan(plan(['s']), {
      targetMessageIds: ['m'],
      contextMessageIds: ['m'],
      evidence: receipts,
      actionTools: [],
      replySuggestionsEnabled: true,
    }),
  )
  const brief = buildReplyBrief(plan(['s']), receipts, [])
  const full = packReplyEvidence({
    brief,
    envelope: {},
    serialize: JSON.stringify,
    budget: { mode: 'bytes', limit: 20000 },
  })
  assert.equal(full.status, 'selected')
  const serialized = full.serialized!
  assert.match(serialized, /18:00 only if both agree/)
  assert.match(serialized, /inheritedProposalGroups/)
  assert.match(serialized, /All groups required/)
  assert.match(serialized, /previously_recorded_not_current_truth/)
  assert.doesNotMatch(serialized, /RAW_PROOF_SECRET|"proofs"/)
  const parsed = JSON.parse(serialized)
  assert.deepEqual(
    parsed.evidence[0].states[0].revisions[0].inheritedProposalGroups.map(
      (g: unknown[]) => g.length,
    ),
    [1, 2],
  )
  const omitted = packReplyEvidence({
    brief,
    envelope: {},
    serialize: JSON.stringify,
    budget: { mode: 'bytes', limit: 1800 },
  })
  assert.equal(omitted.status, 'selected')
  assert.ok(omitted.omitted.some((o) => o.evidenceId === 's'))
  assert.doesNotMatch(
    omitted.serialized!,
    /Qualified state fact|DRAFT_CANARY|18:00 only if both agree/,
  )
  assert.match(omitted.serialized!, /Web-only fact/)
  assert.deepEqual(new PlannerEvidenceBudget(1000).receipt([], receipt, 'call').receipt.value, [])
})
