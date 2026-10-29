import test from 'node:test'
import assert from 'node:assert/strict'
import { CommitmentTimelineExposure } from '../src/memory/read/commitment-timeline-exposure.ts'
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
import type { TemporalHistoryResult } from '../src/memory/commitments/temporal-read.ts'
import { validWorkspaceState } from '../src/runtime/workspace-state-evidence.ts'
function fixture() {
  const anchor = { id: 'plan', version: 2, deletionEpoch: 0, mutationVersion: 4 }
  const state = {
    enabled: true,
    ready: true,
    exposed: [anchor] as CommitmentExposure[],
    current: view(2),
    reads: 0,
    history: {
      recordedStates: [
        {
          recordedAt: '2026-10-29T10:00:00.000Z',
          mutationVersion: 3,
          reason: 'confirm',
          activeVersion: 1,
          latestVersion: 1,
          status: 'available',
          states: [view(1)],
        },
        {
          recordedAt: '2026-10-29T11:00:00.000Z',
          mutationVersion: 4,
          reason: 'confirm',
          activeVersion: 2,
          latestVersion: 2,
          status: 'available',
          states: [view(2)],
        },
      ],
      coverage: 'limited',
      reasons: [],
      interpretation: 'previously_recorded_declaration_state_not_current_truth',
      sourceBodyCoverage: 'unavailable',
      stateTime: 'server_recorded_not_occurred',
      window: {
        maxTransitions: 64,
        firstRecordedAt: '2026-10-29T10:00:00.000Z',
        firstMutationVersion: 3,
        prunedThroughMutationVersion: 0,
        throughMutationVersion: 4,
      },
    } as TemporalHistoryResult,
  }
  const exposure = new CommitmentTimelineExposure({
    enabled: () => state.enabled,
    ready: () => state.ready,
    readCurrentExposedRefs: () => state.exposed,
    commitments: {
      current: () => state.current,
      temporalHistory: (ctx, ref) => {
        state.reads++
        assert.equal(ctx.exposedRefs?.[0]?.mutationVersion, 4)
        assert.equal(ref.mutationVersion, 4)
        return structuredClone(state.history)
      },
    },
  })
  return { state, exposure, anchor }
}
test('recorded timeline requires actual first anchor and delivers an entire exact packet', () => {
  const { state, exposure, anchor } = fixture(),
    ctx = context()
  assert.deepEqual(exposure.query(ctx, { ...anchor, id: 'guessed' }), [])
  assert.equal(state.reads, 0)
  const page = exposure.query(ctx, anchor, { asOfRecorded: '2026-10-29T11:00:00.000Z' })
  assert.equal(validWorkspaceState('memory.commitmentTimeline', page[0]), true)
  assert.equal(
    page[0]!.recordedStates[0]!.states[0]!.terms!.timeQualification,
    '18:00; date unknown',
  )
  assert.equal(page[0]!.stateTime, 'server_recorded_not_occurred')
  assert.deepEqual(exposure.deliver(ctx, structuredClone(page)), [])
  assert.deepEqual(exposure.deliver(context(), page), [])
  assert.equal(exposure.captureGuard(ctx), undefined)
  assert.deepEqual(exposure.deliver(ctx, page), page)
  assert.deepEqual([...ctx.dependencies], ['source-1', 'source-2'])
  assert.equal(exposure.captureGuard(ctx)!(), true)
})
test('old mutation and privacy projection changes reject prepared or already delivered timeline', () => {
  const { state, exposure, anchor } = fixture(),
    ctx = context(),
    page = exposure.query(ctx, anchor)
  state.current.mutationVersion++
  assert.deepEqual(exposure.deliver(ctx, page), [])
  state.current.mutationVersion--
  exposure.deliver(ctx, page)
  state.history.recordedStates[0]!.states = []
  state.history.recordedStates[0]!.status = 'redacted'
  state.history.reasons.push('privacy_or_proof_unavailable')
  assert.equal(exposure.captureGuard(ctx)!(), false)
  assert.equal(
    ctx.validations.every((v) => v()),
    false,
  )
})
test('empty limited legacy/redacted timeline is a meaningful whole receipt, not no history', () => {
  const { state, exposure, anchor } = fixture(),
    ctx = context()
  state.history.recordedStates = []
  state.history.reasons = ['legacy_history_unknown', 'no_recorded_state']
  const page = exposure.query(ctx, anchor)
  assert.equal(page.length, 1)
  assert.equal(validWorkspaceState('memory.commitmentTimeline', page[0]), true)
  exposure.deliver(ctx, page)
  assert.equal(exposure.captureGuard(ctx)!(), true)
  assert.equal(ctx.dependencies.size, 0)
  state.ready = false
  assert.equal(exposure.captureGuard(ctx)!(), false)
})
test('prepared but omitted timeline registers no authority, no dependencies and no guard', () => {
  const { state, exposure, anchor } = fixture(),
    ctx = context()
  exposure.query(ctx, anchor)
  assert.deepEqual(exposure.deliver(ctx, []), [])
  state.history.recordedStates = []
  assert.equal(exposure.captureGuard(ctx), undefined)
  assert.equal(ctx.dependencies.size, 0)
})
