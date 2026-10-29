import assert from 'node:assert/strict'
import test from 'node:test'
import {
  WorkspaceExposure,
  type WorkspaceExposurePort,
  type WorkingSetPage,
} from '../src/memory/read/workspace-exposure.ts'
import type { ModelMemoryContext } from '../src/memory/read/model-exposure.ts'
import type { ExperienceAssertionSnapshot } from '../src/memory/domain/experience-view.ts'
import type { CommitmentView } from '../src/memory/domain/commitments.ts'
import type { WorkingSetViewItem } from '../src/memory/working-set/contracts.ts'
import { projectExperienceView } from '../src/memory/projections/experience-view.ts'
const context = (): ModelMemoryContext => ({
  scene: { id: 's' },
  mode: 'real',
  signal: new AbortController().signal,
  dependencies: new Set(),
  validations: [],
})
const assertion = (id: string): ExperienceAssertionSnapshot => ({
  ref: { id, version: 1, deletionEpoch: 0 },
  qualificationKey: 'a'.repeat(64),
  record: {
    id,
    scope: { kind: 'scene', sceneId: 's', personId: 'p' },
    kind: 'experience',
    content: 'Synthetic reported experience, NOT confirmed; only if conditions hold.',
    sources: [{ sceneId: 's', messageId: 'source-' + id, senderId: 'p' }],
    importance: 'low',
    confidence: 0.4,
    learnedAt: '2026-10-28T00:00:00.000Z',
    epistemicKind: 'reported',
    status: 'disputed',
    locked: false,
    origin: 'learned',
  },
})
const plan = (): CommitmentView => ({
  ref: { id: 'plan', version: 2, deletionEpoch: 0 },
  mutationVersion: 3,
  sceneId: 's',
  terms: {
    kind: 'plan',
    title: 'Synthetic plan',
    participantIds: ['a', 'b'],
    temporalScope: 'unspecified',
    polarity: 'negative',
    modality: 'possible',
    conditions: ['Only if both explicitly agree'],
    timeQualification: 'Unknown date',
  },
  confirmation: 'incomplete',
  lifecycle: 'needs_review',
  qualification: 'declared_not_verified',
  sourceBodyCoverage: 'unavailable',
  changed: true,
  confirmedParticipantIds: ['a'],
  missingParticipantIds: ['b'],
  completedParticipantIds: ['a'],
  cancelledParticipantIds: [],
  requiredProposalGroups: [],
  evidence: [
    {
      backing: { id: 'backing', version: 1, deletionEpoch: 0 },
      backingFingerprint: 'b'.repeat(64),
      kind: 'proposal',
      proofs: [
        {
          proofId: 'proof',
          sources: [{ sceneId: 's', messageId: 'plan-source', senderId: 'a' }],
          parents: [],
        },
      ],
    },
  ],
})
function fixture() {
  const state = {
    enabled: true,
    ready: true,
    limited: true,
    delivered: 0,
    assertions: [assertion('a')],
    plans: [plan()],
    metadata: new Map<string, WorkingSetViewItem>(),
    extra: assertion('unselected'),
    empty: false,
    oversize: false,
  }
  const metadata = (kind: 'assertion' | 'commitment', id: string): WorkingSetViewItem => ({
    ref:
      kind === 'assertion'
        ? { kind, id, version: 1, deletionEpoch: 0 }
        : { kind, id, version: 2, deletionEpoch: 0, mutationVersion: 3 },
    dependencyKey: 'a'.repeat(64),
    state: 'hot',
    reviewDue: false,
    pinned: false,
    pinIntent: false,
    pinRevision: 0,
    pinStatus: 'none',
    policyCoverage: 'complete',
    metadataRevision: 1,
    contextSelectedCount: 0,
    lastSelectedAt: null,
    protection: null,
  })
  state.metadata.set('a', metadata('assertion', 'a'))
  state.metadata.set('plan', metadata('commitment', 'plan'))
  const ports: WorkspaceExposurePort = {
    enabled: () => state.enabled,
    ready: () => state.ready,
    onDelivered: () => {
      state.delivered++
    },
    workspace: {
      workingSet: () => {
        const items: Extract<WorkingSetPage, { interpretation: string }>['items'] = state.empty
          ? []
          : [
              ...state.assertions.map((snapshot) => ({
                kind: 'assertion' as const,
                snapshot,
                accessibility: state.metadata.get(snapshot.ref.id)!,
              })),
              ...state.plans.map((view) => ({
                kind: 'commitment' as const,
                view,
                accessibility: state.metadata.get(view.ref.id)!,
              })),
            ]
        return {
          enabled: true,
          items: state.oversize ? Array(13).fill(items[0]) : items,
          coverage: state.limited ? 'limited' : 'complete',
          omittedCold: state.limited ? 2 : 0,
          omittedEntries: 0,
          interpretation: 'accessibility_not_truth_or_absence',
        }
      },
      episodes: () =>
        projectExperienceView({
          sceneId: 's',
          simulation: false,
          assertions: state.empty ? [] : state.assertions,
          commitments: state.empty ? [] : state.plans,
          upstreamCoverage: {
            assertions: state.limited ? 'limited' : 'complete',
            commitments: 'complete',
          },
        }),
      access: {
        view: (_ctx, selections) => {
          const items = selections.flatMap((selection) => {
            const value = state.metadata.get(selection.ref.id)
            return value &&
              value.dependencyKey === selection.dependencyKey &&
              JSON.stringify(value.ref) === JSON.stringify(selection.ref)
              ? [structuredClone(value)]
              : []
          })
          return {
            enabled: state.enabled,
            items,
            coverage: items.length === selections.length ? 'complete' : 'limited',
            omittedCold: 0,
            unavailable: selections.length - items.length,
          }
        },
      },
    },
    authority: {
      resolveCurrentBatch: (sceneId, refs) =>
        sceneId === 's'
          ? {
              assertions: refs.assertions.flatMap((ref) =>
                state.assertions.filter((item) => JSON.stringify(item.ref) === JSON.stringify(ref)),
              ),
              commitments: refs.commitments.flatMap((ref) =>
                state.plans.filter(
                  (view) =>
                    view.ref.id === ref.id &&
                    view.ref.version === ref.version &&
                    view.ref.deletionEpoch === ref.deletionEpoch &&
                    view.mutationVersion === ref.mutationVersion,
                ),
              ),
            }
          : undefined,
    },
  }
  return { state, ports, exposure: new WorkspaceExposure(ports) }
}

test('whole working-set receipt preserves limited coverage and exact original-object delivery', () => {
  const { state, exposure } = fixture(),
    ctx = context(),
    prepared = exposure.workingSet(ctx)
  assert.equal(prepared.length, 1)
  assert.equal(prepared[0]!.stateKind, 'working_set')
  if (prepared[0]!.stateKind !== 'working_set') throw Error('fixture')
  assert.equal(prepared[0]!.page.coverage, 'limited')
  assert.equal(prepared[0]!.page.omittedCold, 2)
  assert.deepEqual(exposure.deliver(ctx, []), [])
  assert.deepEqual(exposure.deliver(ctx, structuredClone(prepared)), [])
  assert.deepEqual(exposure.deliver(context(), prepared), [])
  assert.equal(exposure.captureGuard(ctx), undefined)
  assert.equal(ctx.dependencies.size, 0)
  assert.deepEqual(exposure.deliver(ctx, prepared), prepared)
  assert.equal(state.delivered, 1)
  exposure.deliver(ctx, prepared)
  assert.equal(state.delivered, 1)
  assert.deepEqual([...ctx.dependencies].sort(), ['plan-source', 'source-a'])
  assert.equal(exposure.readDelivered(ctx).length, 1)
  assert.ok(Object.isFrozen(prepared[0]!.page.items))
})
test('statistics and unselected candidate changes do not cancel; source qualification and explicit pin policy changes do', () => {
  const { state, exposure } = fixture(),
    ctx = context(),
    prepared = exposure.workingSet(ctx)
  exposure.deliver(ctx, prepared)
  const guard = exposure.captureGuard(ctx)!
  const item = state.metadata.get('a')!
  item.contextSelectedCount++
  item.lastSelectedAt = 100
  item.metadataRevision++
  item.state = 'cold'
  item.reviewDue = true
  state.extra.record.content = 'Unselected content changed'
  assert.equal(guard(), true)
  item.pinRevision++
  item.pinIntent = true
  item.pinStatus = 'stale'
  item.policyCoverage = 'unknown'
  assert.equal(guard(), false)
  assert.equal(exposure.readDelivered(ctx).length, 0)
  const nextCtx = context()
  const next = exposure.workingSet(nextCtx)
  assert.equal(next.length, 1)
  item.dependencyKey = 'c'.repeat(64)
  assert.equal(exposure.deliver(nextCtx, next).length, 0)
})
test('actual included record body and plan mutation are rechecked, not merely candidate query order', () => {
  const f = fixture(),
    ctx = context(),
    prepared = f.exposure.workingSet(ctx)
  f.state.assertions[0]!.record.content = 'Changed current record with same synthetic test key'
  assert.deepEqual(f.exposure.deliver(ctx, prepared), [])
  const g = fixture(),
    other = context(),
    planPrepared = g.exposure.workingSet(other)
  g.state.plans[0]!.mutationVersion++
  assert.deepEqual(g.exposure.deliver(other, planPrepared), [])
  assert.equal(other.dependencies.size, 0)
})
test('experience receipt keeps participant qualifications and unknown time and verifies included entries only', () => {
  const { state, exposure } = fixture(),
    ctx = context(),
    prepared = exposure.episodes(ctx)
  assert.equal(prepared[0]!.stateKind, 'experience_view')
  if (prepared[0]!.stateKind !== 'experience_view') throw Error('fixture')
  const page = prepared[0]!.page
  assert.equal(page.coverage.status, 'limited')
  assert.equal(page.items[0]!.kind, 'reported_experience')
  if (page.items[0]!.kind === 'reported_experience')
    assert.equal(page.items[0]!.time.occurredAt, null)
  const shared = page.items.find((item) => item.kind === 'shared_plan_declaration')!
  if (shared.kind === 'shared_plan_declaration') {
    assert.equal(shared.completionBasis, 'participant_self_report_not_observed_event')
    assert.deepEqual(shared.view.missingParticipantIds, ['b'])
    assert.deepEqual(shared.view.terms!.conditions, ['Only if both explicitly agree'])
  }
  state.extra.record.content = 'Never included'
  assert.equal(exposure.deliver(ctx, prepared).length, 1)
  const guard = exposure.captureGuard(ctx)!
  state.assertions[0]!.qualificationKey = 'd'.repeat(64)
  assert.equal(guard(), false)
})
test('enabled empty pages retain coverage; OFF/simulation/cancelled never produce a state receipt', () => {
  const { state, exposure } = fixture(),
    ctx = context()
  state.empty = true
  const empty = exposure.episodes(ctx)
  assert.equal(empty.length, 1)
  if (empty[0]!.stateKind === 'experience_view') {
    assert.equal(empty[0]!.page.items.length, 0)
    assert.equal(empty[0]!.page.coverage.status, 'limited')
  }
  state.enabled = false
  assert.deepEqual(exposure.episodes(ctx), [])
  assert.deepEqual(exposure.deliver(ctx, empty), [])
  state.enabled = true
  assert.deepEqual(exposure.workingSet({ ...context(), mode: 'simulation' }), [])
  const abort = new AbortController()
  abort.abort()
  assert.deepEqual(exposure.episodes({ ...context(), signal: abort.signal }), [])
  state.empty = false
  state.oversize = true
  assert.throws(() => exposure.workingSet(ctx), /workspace_page_invalid/)
})
test('failed post-delivery hooks invalidate turn rather than granting partial state authority', () => {
  const { ports, state } = fixture()
  ports.onDelivered = () => {
    state.enabled = false
    throw Error('synthetic_delivery_failure')
  }
  const exposure = new WorkspaceExposure(ports),
    ctx = context(),
    prepared = exposure.workingSet(ctx)
  assert.throws(() => exposure.deliver(ctx, prepared), /synthetic_delivery_failure/)
  assert.equal(
    ctx.validations.some((check) => !check()),
    true,
  )
  assert.equal(exposure.readDelivered(ctx).length, 0)
})

test('working metadata key may wrap raw assertion qualification without weakening either independent witness', () => {
  const { state, exposure } = fixture(),
    ctx = context()
  state.metadata.get('a')!.dependencyKey = 'e'.repeat(64)
  const prepared = exposure.workingSet(ctx)
  assert.equal(prepared.length, 1)
  assert.equal(exposure.deliver(ctx, prepared).length, 1)
  assert.equal(exposure.captureGuard(ctx)!(), true)
  state.assertions[0]!.qualificationKey = 'f'.repeat(64)
  assert.equal(exposure.captureGuard(ctx)!(), false)
})
