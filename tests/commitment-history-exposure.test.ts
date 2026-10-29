import test from 'node:test'
import assert from 'node:assert/strict'
import { CommitmentHistoryExposure } from '../src/memory/read/commitment-history-exposure.ts'
import type { CommitmentView, CommitmentExposure } from '../src/memory/domain/commitments.ts'
import type { ModelMemoryContext } from '../src/memory/read/model-exposure.ts'
const context = (): ModelMemoryContext => ({
  scene: { id: 's' },
  mode: 'real',
  signal: new AbortController().signal,
  dependencies: new Set(),
  validations: [],
})
const view = (version: number): CommitmentView => ({
  ref: { id: 'plan', version, deletionEpoch: 0 },
  mutationVersion: 4,
  sceneId: 's',
  terms: {
    kind: 'plan',
    title: 'Synthetic meeting',
    participantIds: ['a', 'b'],
    temporalScope: 'future',
    polarity: 'positive',
    modality: 'asserted',
    conditions: ['Only if both are available'],
    timeQualification: version === 1 ? '18:00; date unknown' : '19:00; date unknown',
  },
  confirmation: 'confirmed',
  lifecycle: 'pending',
  qualification: 'declared_not_verified',
  sourceBodyCoverage: 'unavailable',
  changed: true,
  confirmedParticipantIds: ['a', 'b'],
  missingParticipantIds: [],
  completedParticipantIds: [],
  cancelledParticipantIds: [],
  requiredProposalGroups: [],
  evidence: [
    {
      backing: { id: `assertion-${version}`, version: 1, deletionEpoch: 0 },
      backingFingerprint: 'synthetic',
      kind: 'proposal',
      proofs: [
        {
          proofId: `proof-${version}`,
          sources: [{ sceneId: 's', messageId: `source-${version}`, senderId: 'a' }],
          parents: [],
          parentReferences: [],
        },
      ],
    },
  ],
})
function fixture() {
  const state = {
    enabled: true,
    ready: true,
    exposed: [
      { id: 'plan', version: 2, deletionEpoch: 0, mutationVersion: 4 },
    ] as CommitmentExposure[],
    current: view(2),
    history: [view(1), view(2)],
    reads: 0,
  }
  const exposure = new CommitmentHistoryExposure({
    enabled: () => state.enabled,
    ready: () => state.ready,
    readCurrentExposedRefs: () => state.exposed,
    commitments: {
      current: () => state.current,
      history: (ctx) => {
        state.reads++
        assert.equal(ctx.provenance, 'contextual')
        assert.equal(ctx.exposedRefs?.[0]?.mutationVersion, 4)
        return {
          revisions: state.history,
          coverage: 'limited',
          interpretation: 'previously_recorded_not_current_truth',
        }
      },
    },
  })
  return { state, exposure }
}

test('actual exposed current anchor authorizes whole historical receipt, never latest-fact substitution', () => {
  const { state, exposure } = fixture(),
    ctx = context()
  assert.deepEqual(exposure.query(ctx, { id: 'guessed', version: 2, deletionEpoch: 0 }), [])
  assert.equal(state.reads, 0)
  const values = exposure.query(ctx, { id: 'plan', version: 2, deletionEpoch: 0 })
  assert.equal(values[0]!.stateKind, 'commitment_history')
  assert.equal(values[0]!.revisions[0]!.terms!.timeQualification, '18:00; date unknown')
  assert.equal(values[0]!.revisions[1]!.terms!.timeQualification, '19:00; date unknown')
  assert.equal(values[0]!.interpretation, 'previously_recorded_not_current_truth')
  assert.equal(values[0]!.coverage, 'limited')
  assert.equal(values[0]!.sourceBodyCoverage, 'unavailable')
  assert.deepEqual(exposure.deliver(ctx, []), [])
  assert.deepEqual(exposure.deliver(ctx, structuredClone(values)), [])
  assert.deepEqual(exposure.deliver(context(), values), [])
  assert.equal(exposure.captureGuard(ctx), undefined)
  exposure.deliver(ctx, values)
  assert.deepEqual([...ctx.dependencies], ['source-1', 'source-2'])
  assert.equal(ctx.validations[0]!(), true)
  assert.deepEqual(state.exposed, [
    { id: 'plan', version: 2, deletionEpoch: 0, mutationVersion: 4 },
  ])
  assert.equal(exposure.readDelivered(ctx)[0]!.revisions[0]!.ref.version, 1)
})

test('exact mutation/current body and history authorization are rechecked before and after delivery', () => {
  const { state, exposure } = fixture(),
    ctx = context(),
    values = exposure.query(ctx, state.exposed[0]!)
  state.current.mutationVersion++
  assert.deepEqual(exposure.deliver(ctx, values), [])
  state.current.mutationVersion--
  exposure.deliver(ctx, values)
  const guard = exposure.captureGuard(ctx)!
  state.history[0]!.evidence = []
  assert.equal(guard(), false)
  assert.equal(ctx.validations[0]!(), false)
  assert.deepEqual(exposure.readDelivered(ctx), [])
  assert.equal(typeof exposure.captureGuard(ctx), 'function')
})

test('queued history guard detaches only turn signal, not OFF/privacy/scope authority', () => {
  const { state, exposure } = fixture(),
    ctx = context(),
    controller = new AbortController()
  ctx.signal = controller.signal
  exposure.deliver(ctx, exposure.query(ctx, state.exposed[0]!))
  const guard = exposure.captureGuard(ctx)!
  controller.abort('turn_finished')
  assert.equal(guard(), true)
  state.ready = false
  assert.equal(guard(), false)
  state.ready = true
  state.enabled = false
  assert.equal(guard(), false)
})

test('history bounds whole views to the exposed version and reports omissions rather than completeness', () => {
  const { state, exposure } = fixture(),
    ctx = context()
  state.current = view(25)
  state.exposed = [{ id: 'plan', version: 25, deletionEpoch: 0, mutationVersion: 4 }]
  state.history = Array.from({ length: 30 }, (_, index) => view(index + 1))
  const values = exposure.query(ctx, state.exposed[0]!)
  assert.equal(values[0]!.revisions.length, 20)
  assert.equal(values[0]!.revisions[0]!.ref.version, 6)
  assert.equal(values[0]!.revisions.at(-1)!.ref.version, 25)
  assert.deepEqual(values[0]!.historyWindow, {
    maxRevisions: 20,
    omittedRevisions: 5,
    throughVersion: 25,
  })
  assert.equal(values[0]!.coverage, 'limited')
  assert.ok(Object.isFrozen(values[0]!.revisions[0]!.terms!.conditions))
  const group = structuredClone(state.history[24]!.evidence[0]!)
  group.proofs[0]!.sources[0]!.messageId = 'required-source'
  state.history[24]!.requiredProposalGroups = [[group]]
  const updated = exposure.query(ctx, state.exposed[0]!)
  exposure.deliver(ctx, updated)
  assert.ok(ctx.dependencies.has('required-source'))
})
