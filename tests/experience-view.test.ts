import assert from 'node:assert/strict'
import test from 'node:test'
import {
  projectExperienceView,
  verifyExperienceView,
} from '../src/memory/projections/experience-view.ts'
import type {
  ExperienceAssertionSnapshot,
  ExperienceProjectionInput,
  ExperienceRefreshPort,
} from '../src/memory/domain/experience-view.ts'
import type { CommitmentView } from '../src/memory/domain/commitments.ts'
const ref = (id: string) => ({ id, version: 1, deletionEpoch: 0 })
const assertion = (id: string): ExperienceAssertionSnapshot => ({
  ref: ref(id),
  qualificationKey: 'a'.repeat(64),
  record: {
    id,
    scope: { kind: 'scene', sceneId: 's', personId: 'subject' },
    kind: 'experience',
    content: '很长的经历条件。'.repeat(1500) + '但没有参加活动，不可省略否定。',
    sources: [
      {
        sceneId: 's',
        messageId: 'source',
        senderId: 'reporter',
        eventRef: { eventId: 'event', revision: 2, deletionEpoch: 0 },
      },
    ],
    importance: 'low',
    confidence: 0.4,
    learnedAt: '2026-10-28T18:00:00Z',
    observedAt: '2026-10-28T17:50:00Z',
    validFrom: '2026-10-28T17:00:00Z',
    validUntil: '2026-12-01T00:00:00Z',
    epistemicKind: 'reported',
    status: 'disputed',
    locked: false,
    origin: 'learned',
    lineageStatus: 'unverified',
    parentBinding: 'legacy_unknown',
  },
})
const plan = (id = 'plan'): CommitmentView => ({
  ref: ref(id),
  mutationVersion: 5,
  sceneId: 's',
  recordedAt: '2026-10-28T18:00:00Z',
  terms: {
    kind: 'plan',
    title: 'Same title',
    participantIds: ['alice', 'bob'],
    polarity: 'negative',
    modality: 'possible',
    temporalScope: 'unspecified',
    conditions: ['除非双方重新确认，否则不能说活动已发生。'],
    timeQualification: '日期未知',
  },
  confirmation: 'incomplete',
  lifecycle: 'needs_review',
  qualification: 'declared_not_verified',
  sourceBodyCoverage: 'unavailable',
  changed: true,
  confirmedParticipantIds: ['alice'],
  missingParticipantIds: ['bob'],
  completedParticipantIds: ['alice'],
  cancelledParticipantIds: [],
  evidence: [
    {
      backing: ref('self-statement'),
      backingFingerprint: 'b'.repeat(64),
      personId: 'alice',
      kind: 'complete',
      proofs: [
        {
          proofId: 'proof',
          sources: [{ sceneId: 's', messageId: 'self', senderId: 'alice' }],
          parents: [],
          sourceObservations: [
            {
              sceneId: 's',
              messageId: 'self',
              senderId: 'alice',
              receivedAt: '2026-10-28T18:00:00Z',
            },
          ],
        },
      ],
    },
  ],
  requiredProposalGroups: [],
})
const input = (
  assertions = [assertion('one')],
  commitments = [plan()],
): ExperienceProjectionInput => ({
  sceneId: 's',
  simulation: false,
  assertions,
  commitments,
  upstreamCoverage: { assertions: 'complete', commitments: 'complete' },
})
test('independent experience and shared declaration preserve complete qualifiers, subjects and unknown occurrence times', () => {
  const original = input([assertion('one'), assertion('two')], [plan(), plan('other-plan')])
  const snapshot = structuredClone(original)
  const output = projectExperienceView(original)
  assert.equal(output.items.length, 4)
  assert.deepEqual(original, snapshot)
  const first = output.items[0]!
  assert.equal(first.kind, 'reported_experience')
  if (first.kind !== 'reported_experience') throw Error('fixture')
  assert.deepEqual(first.record, original.assertions[0]!.record)
  assert.equal(first.time.occurredAt, null)
  assert.equal(first.time.recordedAt, original.assertions[0]!.record.learnedAt)
  assert.match(first.record.content, /不可省略否定。$/)
  assert.equal(first.record.scope.kind === 'scene' && first.record.scope.personId, 'subject')
  assert.equal(first.record.sources[0]!.senderId, 'reporter')
  const shared = output.items[2]!
  assert.equal(shared.kind, 'shared_plan_declaration')
  if (shared.kind !== 'shared_plan_declaration') throw Error('fixture')
  assert.deepEqual(shared.view, original.commitments[0])
  assert.deepEqual(shared.view.completedParticipantIds, ['alice'])
  assert.deepEqual(shared.view.missingParticipantIds, ['bob'])
  assert.equal(shared.completionBasis, 'participant_self_report_not_observed_event')
  assert.equal(shared.view.terms!.occurredAt, undefined)
  assert.ok(Object.isFrozen(first.record.sources))
  original.assertions[0]!.record.content = 'mutated caller'
  original.commitments[0]!.terms!.conditions[0] = 'mutated caller'
  assert.match(first.record.content, /不可省略否定。$/)
  assert.match(shared.view.terms!.conditions[0]!, /不能说活动已发生/)
})
test('scene-only filtered entries, simulation exclusion and bounded whole-entry coverage are explicit', () => {
  const shared = assertion('shared')
  shared.record.scope = { kind: 'sharedPublicKnowledge' }
  const internal = assertion('internal')
  internal.record.internalUse = 'commitment_declaration'
  const fact = assertion('fact')
  fact.record.kind = 'fact'
  const invalidated = assertion('invalidated')
  invalidated.record.status = 'invalidated'
  assert.equal(
    projectExperienceView(input([shared, internal, fact, invalidated], [])).items.length,
    0,
  )
  assert.equal(projectExperienceView({ ...input(), simulation: true }).items.length, 0)
  const bounded = projectExperienceView({
    ...input(
      Array.from({ length: 13 }, (_, i) => assertion(`a${i}`)),
      [],
    ),
    upstreamCoverage: { assertions: 'limited', commitments: 'complete' },
  })
  assert.equal(bounded.items.length, 12)
  assert.deepEqual(bounded.coverage, {
    status: 'limited',
    reasons: ['upstream_limited', 'entry_limit'],
    returned: 12,
    omittedEntries: 1,
  })
  assert.equal(
    projectExperienceView({
      ...input([], []),
      upstreamCoverage: { assertions: 'limited', commitments: 'complete' },
    }).coverage.status,
    'limited',
  )
  assert.throws(
    () =>
      projectExperienceView(
        input(
          Array.from({ length: 41 }, (_, i) => assertion(`a${i}`)),
          [],
        ),
      ),
    /invalid_input/,
  )
  const foreign = assertion('foreign')
  foreign.record.scope = { kind: 'scene', sceneId: 'other' }
  assert.throws(() => projectExperienceView(input([foreign], [])), /invalid_input/)
  for (const limit of [0, -1, 13, NaN, Infinity])
    assert.throws(() => projectExperienceView({ ...input(), limit }), /invalid_input/)
  assert.throws(
    () => projectExperienceView(input([assertion('same'), assertion('same')], [])),
    /invalid_input/,
  )
  const mismatch = assertion('one')
  mismatch.ref.id = 'two'
  assert.throws(() => projectExperienceView(input([mismatch], [])), /invalid_input/)
})
test('one current-resolution batch checks included exact qualification only; omissions never become dependencies', () => {
  const source = input([assertion('one'), assertion('two')], [plan()])
  const page = projectExperienceView({ ...source, limit: 1 })
  let calls = 0
  const resolver: ExperienceRefreshPort = {
    resolveCurrentBatch: (sceneId, refs) => {
      calls++
      assert.equal(sceneId, 's')
      assert.deepEqual(refs, { assertions: [ref('one')], commitments: [] })
      return { assertions: [source.assertions[0]!], commitments: [] }
    },
  }
  assert.equal(verifyExperienceView('s', page, resolver), true)
  assert.equal(calls, 1)
  source.assertions[1]!.record.content = 'omitted does not register'
  assert.equal(verifyExperienceView('s', page, resolver), true)
  source.assertions[0]!.qualificationKey = 'c'.repeat(64)
  assert.equal(verifyExperienceView('s', page, resolver), false)
  source.assertions[0]!.qualificationKey = 'a'.repeat(64)
  const savedBody = source.assertions[0]!.record.content
  source.assertions[0]!.record.content = 'changed body with unchanged ref must fail'
  assert.equal(verifyExperienceView('s', page, resolver), false)
  source.assertions[0]!.record.content = savedBody
  source.assertions[0]!.ref.deletionEpoch++
  assert.equal(verifyExperienceView('s', page, resolver), false)
  assert.equal(verifyExperienceView('s', page, { resolveCurrentBatch: () => undefined }), false)
  assert.equal(
    verifyExperienceView('s', page, {
      resolveCurrentBatch: () => {
        throw Error('private detail')
      },
    }),
    false,
  )
  const empty = projectExperienceView(input([], []))
  assert.equal(
    verifyExperienceView('s', empty, {
      resolveCurrentBatch: () => {
        throw Error('must not resolve empty')
      },
    }),
    true,
  )
})
test('plan mutation, inherited proof clock, body and current scope drift invalidate a delivered view', () => {
  const original = input([], [plan()])
  const originalPlan = original.commitments[0]!
  originalPlan.requiredProposalGroups = [[structuredClone(originalPlan.evidence[0]!)]]
  originalPlan.requiredProposalGroups[0]![0]!.kind = 'proposal'
  const page = projectExperienceView(original)
  const verify = (current: CommitmentView) =>
    verifyExperienceView('s', page, {
      resolveCurrentBatch: () => ({ assertions: [], commitments: [current] }),
    })
  assert.equal(verify(structuredClone(originalPlan)), true)
  for (const change of [
    (v: CommitmentView) => {
      v.mutationVersion++
    },
    (v: CommitmentView) => {
      v.ref.version++
    },
    (v: CommitmentView) => {
      v.sceneId = 'other'
    },
    (v: CommitmentView) => {
      v.terms!.conditions = []
    },
    (v: CommitmentView) => {
      v.requiredProposalGroups[0]![0]!.proofs[0]!.sourceObservations![0]!.receivedAt =
        '2026-10-28T19:00:00Z'
    },
  ]) {
    const changed = structuredClone(originalPlan)
    change(changed)
    assert.equal(verify(changed), false)
  }
})
