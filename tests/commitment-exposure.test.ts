import test from 'node:test'
import assert from 'node:assert/strict'
import { CommitmentExposure } from '../src/memory/read/commitment-exposure.ts'
import type { CommitmentView } from '../src/memory/domain/commitments.ts'
import type { ModelMemoryContext } from '../src/memory/read/model-exposure.ts'
const ctx = (): ModelMemoryContext => ({
  scene: { id: 's' },
  mode: 'real',
  signal: new AbortController().signal,
  dependencies: new Set(),
  validations: [],
})
const view = (id: string): CommitmentView => ({
  ref: { id, version: 1, deletionEpoch: 0 },
  mutationVersion: 3,
  sceneId: 's',
  terms: {
    kind: 'plan',
    title: 'Synthetic plan',
    participantIds: ['a', 'b'],
    temporalScope: 'future',
    polarity: 'positive',
    modality: 'possible',
    conditions: ['Only if both agree'],
    timeQualification: '18:00; date unknown',
  },
  recordedAt: '2026-10-28T00:00:00.000Z',
  confirmation: 'incomplete',
  lifecycle: 'needs_review',
  qualification: 'declared_not_verified',
  sourceBodyCoverage: 'unavailable',
  changed: true,
  confirmedParticipantIds: ['a'],
  missingParticipantIds: ['b'],
  completedParticipantIds: [],
  cancelledParticipantIds: [],
  requiredProposalGroups: [],
  evidence: [
    {
      backing: { id: 'assertion-' + id, version: 1, deletionEpoch: 0 },
      backingFingerprint: 'synthetic',
      kind: 'proposal',
      proofs: [
        {
          proofId: 'proof-' + id,
          sources: [{ sceneId: 's', messageId: 'source-' + id, senderId: 'a' }],
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
    limited: false,
    calls: 0,
    delivered: 0,
    items: [view('p'), view('q')],
  }
  const exposure = new CommitmentExposure({
    enabled: () => state.enabled,
    ready: () => state.ready,
    commitments: {
      list: (context, options) => {
        state.calls++
        assert.equal(context.provenance, 'contextual')
        assert.equal(options.limit, 12)
        return { items: state.items, coverage: state.limited ? 'limited' : 'complete' }
      },
      current: (_ctx, ref) => state.items.find((v) => v.ref.id === ref.id),
    },
    onDelivered: () => {
      state.delivered++
    },
  })
  return { state, exposure }
}

test('whole actual delivery exposes exact state refs and preserves all qualifications, not memory parents', () => {
  const { state, exposure } = fixture(),
    c = ctx(),
    values = exposure.query(c, { personId: 'a' })
  assert.deepEqual(values[0]!.view, state.items[0])
  assert.equal(values[0]!.stateKind, 'commitment')
  assert.ok(Object.isFrozen(values[0]!.view.terms!.conditions))
  assert.deepEqual(exposure.deliver(c, []), [])
  assert.deepEqual(exposure.deliver(c, [values[0]]), [])
  assert.deepEqual(exposure.deliver(c, structuredClone(values)), [])
  assert.deepEqual(exposure.deliver(ctx(), values), [])
  assert.deepEqual(exposure.readExposedRefs(c), [])
  assert.equal(exposure.captureGuard(c), undefined)
  exposure.deliver(c, values)
  assert.equal(state.delivered, 1)
  assert.deepEqual([...c.dependencies], ['source-p', 'source-q'])
  assert.equal(
    c.validations.every((check) => check()),
    true,
  )
  assert.deepEqual(exposure.readExposedRefs(c), [
    { id: 'p', version: 1, deletionEpoch: 0, mutationVersion: 3 },
    { id: 'q', version: 1, deletionEpoch: 0, mutationVersion: 3 },
  ])
  assert.deepEqual(exposure.readDelivered(c)[0]!.view.terms?.conditions, ['Only if both agree'])
  assert.equal(exposure.readDelivered(c)[0]!.view.terms?.timeQualification, '18:00; date unknown')
})

test('late state mutation and OFF invalidate exact exposure but never replace it with latest state', () => {
  const { state, exposure } = fixture(),
    c = ctx(),
    values = exposure.query(c)
  state.items[0]!.mutationVersion++
  assert.deepEqual(exposure.deliver(c, values), [])
  assert.equal(state.delivered, 0)
  const current = exposure.query(c)
  exposure.deliver(c, current)
  const guard = exposure.captureGuard(c)!
  state.items[0]!.confirmation = 'confirmed'
  assert.equal(guard(), false)
  assert.deepEqual(exposure.readExposedRefs(c), [])
  assert.equal(exposure.captureGuard(c)!(), false)
  state.enabled = false
  assert.deepEqual(exposure.query(c), [])
  assert.equal(guard(), false)
})

test('detached queued guard survives normal turn finish but not source/epoch or readiness revocation', () => {
  const { state, exposure } = fixture(),
    c = ctx(),
    controller = new AbortController()
  c.signal = controller.signal
  exposure.deliver(c, exposure.query(c))
  const guard = exposure.captureGuard(c)!
  controller.abort('turn_finished')
  assert.equal(guard(), true)
  assert.deepEqual(exposure.readDelivered(c), [])
  state.ready = false
  assert.equal(guard(), false)
  state.ready = true
  state.items[0]!.ref.deletionEpoch++
  assert.equal(guard(), false)
})

test('limited or oversized lists reject whole receipt and simulation never queries', () => {
  const { state, exposure } = fixture(),
    c = ctx()
  state.limited = true
  assert.throws(() => exposure.query(c), /commitment_query_limited/)
  assert.deepEqual(exposure.readExposedRefs(c), [])
  state.limited = false
  state.items = Array.from({ length: 13 }, (_, i) => view(String(i)))
  assert.throws(() => exposure.query(c), /commitment_query_limited/)
  const calls = state.calls
  assert.deepEqual(exposure.query({ ...ctx(), mode: 'simulation' }), [])
  assert.equal(state.calls, calls)
})

test('required proposal groups contribute all dependency sources and revoke delivered authority when they change', () => {
  const { state, exposure } = fixture(),
    c = ctx()
  const required = structuredClone(state.items[0]!.evidence[0]!)
  required.backing.id = 'required-proposal'
  required.proofs[0]!.sources = [
    { sceneId: 's', messageId: 'required-a', senderId: 'a' },
    { sceneId: 's', messageId: 'required-b', senderId: 'b' },
  ]
  const alternative = structuredClone(required)
  alternative.proofs[0]!.sources = [
    { sceneId: 's', messageId: 'alternative-source', senderId: 'a' },
  ]
  state.items[0]!.requiredProposalGroups = [[required, alternative]]
  const values = exposure.query(c)
  exposure.deliver(c, values)
  assert.deepEqual(
    [...c.dependencies].sort(),
    ['source-p', 'source-q', 'required-a', 'required-b', 'alternative-source'].sort(),
  )
  assert.deepEqual(values[0]!.view.requiredProposalGroups, [[required, alternative]])
  assert.equal(values[0]!.view.terms?.timeQualification, '18:00; date unknown')
  const guard = exposure.captureGuard(c)!
  assert.equal(guard(), true)
  // Simulate the authoritative current projection removing recalled backing evidence.
  state.items[0]!.requiredProposalGroups[0]!.splice(0, 1)
  assert.equal(guard(), false)
  assert.deepEqual(exposure.readExposedRefs(c), [])
  assert.equal(
    c.validations.every((check) => check()),
    false,
  )
})
