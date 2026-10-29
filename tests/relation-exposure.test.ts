import test from 'node:test'
import assert from 'node:assert/strict'
import { ModelMemoryExposure, type ModelMemoryContext } from '../src/memory/read/model-exposure.ts'
import { RelationExposure } from '../src/memory/read/relation-exposure.ts'
import type { MemoryRecord } from '../src/memory/domain/contracts.ts'
import type { RelationRecord } from '../src/memory/domain/relations.ts'
const record = (id: string): MemoryRecord => ({
  id,
  scope: { kind: 'scene', sceneId: 's', personId: 'p' },
  kind: 'claim',
  content: 'Complete synthetic ' + id,
  sources: [{ sceneId: 's', messageId: 'source-' + id, senderId: 'p' }],
  importance: 'low',
  confidence: 0.5,
  learnedAt: '2026-01-01T00:00:00Z',
  status: 'active',
  locked: false,
  origin: 'learned',
  lineageStatus: 'verified',
})
const edge = (
  id: string,
  backing = 'a',
  target = 'target',
  polarity: 'positive' | 'negative' = 'positive',
): RelationRecord => ({
  id,
  version: 1,
  sceneId: 's',
  predicate: 'participates_in',
  modality: 'asserted',
  temporalScope: 'current',
  personId: 'p',
  target: {
    id: 'entity',
    sceneId: 's',
    kind: 'plan',
    title: 'Synthetic plan',
    backing: { id: target, version: 1, deletionEpoch: 0 },
  },
  polarity,
  conditions: ['Only if approved; not unconditional'],
  backing: { id: backing, version: 1, deletionEpoch: 0 },
  epistemicKind: 'reported',
  proofs: [{ proofId: 'proof', sources: record(backing).sources, parents: [] }],
  status: polarity === 'positive' ? 'accepted' : 'candidate',
  admission: 'reviewed',
  createdAt: '2026-01-01T00:00:00Z',
})
const context = (): ModelMemoryContext => ({
  scene: { id: 's' },
  mode: 'real',
  signal: new AbortController().signal,
  dependencies: new Set(),
  validations: [],
  deferMemoryExposure: true,
})
function fixture() {
  let enabled = true,
    limited = false
  const records = new Map(['a', 'target', 'b'].map((id) => [id, record(id)]))
  const edges = [edge('positive')]
  const states = new Map(
    ['a', 'target', 'b'].map((id) => [id, { version: 1, deletionEpoch: 0, deleted: false }]),
  )
  const memory = {
    list: (ctx: { sceneId: string; simulation: boolean }) =>
      ctx.sceneId === 's' && !ctx.simulation
        ? [...records.values()].map((r) => structuredClone(r))
        : [],
    captureParentReference: (_ctx: unknown, r: MemoryRecord) => ({
      id: r.id,
      ...states.get(r.id)!,
    }),
    revisionState: (id: string) => states.get(id)!,
    parentBinding: () => 'bound' as const,
    relations: {
      list: () => ({
        items: structuredClone(edges),
        coverage: limited ? ('limited' as const) : ('complete' as const),
      }),
    },
  }
  const exposure = new ModelMemoryExposure(memory)
  const service = new RelationExposure(
    memory,
    exposure,
    () => enabled,
    () => Date.parse('2026-10-28T00:00:00Z'),
  )
  return {
    records,
    edges,
    states,
    exposure,
    service,
    off: () => {
      enabled = false
    },
    limit: () => {
      limited = true
    },
  }
}
test('query prepares without exposure; complete delivery commits exact underlying originals and entity reference', () => {
  const h = fixture(),
    ctx = context()
  const result = h.service.query(ctx, 'p')
  assert.equal(result.length, 2)
  assert.equal(h.exposure.readExposed(ctx).length, 0)
  assert.equal(h.service.readExposedEntities(ctx).length, 0)
  assert.equal(ctx.dependencies.size, 0)
  assert.equal(h.service.deliver(ctx, result).length, 2)
  assert.equal(h.exposure.readExposed(ctx).length, 2)
  assert.deepEqual([...ctx.dependencies].sort(), ['source-a', 'source-target'])
  assert.deepEqual(h.service.readExposedEntities(ctx), [
    { entityId: 'entity', backingRef: { id: 'target', version: 1, deletionEpoch: 0 } },
  ])
  assert.ok(ctx.validations.every((valid) => valid()))
  assert.equal(result[0]!.relationEvidence[0]!.conditions[0], 'Only if approved; not unconditional')
  assert.equal(Reflect.set(result[0]!.relationEvidence[0]!, 'polarity', 'negative'), false)
})
test('omitted target sibling, forged clone, stale epoch or flags off commit no partial exposure', () => {
  for (const mode of ['subset', 'clone', 'stale', 'off'] as const) {
    const h = fixture(),
      ctx = context(),
      result = h.service.query(ctx)
    if (mode === 'stale') h.states.get('target')!.deletionEpoch++
    if (mode === 'off') h.off()
    const selected =
      mode === 'subset' ? result.slice(0, 1) : mode === 'clone' ? structuredClone(result) : result
    assert.deepEqual(h.service.deliver(ctx, selected), [])
    assert.equal(h.exposure.readExposed(ctx).length, 0)
    assert.equal(h.service.readExposedEntities(ctx).length, 0)
    assert.equal(ctx.dependencies.size, 0)
  }
})
test('candidate negative conflicts are included as full group and later conflicts invalidate prepared positive delivery', () => {
  const h = fixture(),
    ctx = context()
  h.edges.push(edge('negative', 'b', 'target', 'negative'))
  const result = h.service.query(ctx)
  assert.equal(result.length, 3)
  assert.ok(
    result
      .flatMap((item) => item.relationEvidence)
      .some((item) => item.status === 'candidate' && item.polarity === 'negative'),
  )
  assert.equal(
    h.service.deliver(
      ctx,
      result.filter((item) => item.id !== 'b'),
    ).length,
    0,
  )
  assert.equal(h.service.deliver(ctx, result).length, 3)
  h.edges.push({
    ...edge('new-conflict', 'b'),
    conditions: ['Different condition'],
    status: 'candidate',
  })
  assert.ok(ctx.validations.some((valid) => !valid()))
  const fresh = fixture(),
    freshCtx = context(),
    prepared = fresh.service.query(freshCtx)
  fresh.edges.push(edge('new-negative', 'b', 'target', 'negative'))
  assert.equal(fresh.service.deliver(freshCtx, prepared).length, 0)
  assert.equal(fresh.exposure.readExposed(freshCtx).length, 0)
})
test('limited queries fail explicitly; simulation yields no relation exposure; times retain qualification', () => {
  const h = fixture()
  h.limit()
  assert.throws(() => h.service.query(context()), /relation_query_limited/)
  const fresh = fixture()
  fresh.edges[0]!.validFrom = '2027-01-01T00:00:00Z'
  const ctx = context()
  assert.equal(fresh.service.query(ctx)[0]!.relationEvidence[0]!.applicability, 'future')
  assert.deepEqual(fresh.service.query({ ...ctx, mode: 'simulation' }), [])
  fresh.off()
  assert.deepEqual(fresh.service.query(ctx), [])
})

test('review rejection and disabling answering revoke delivered relation and entity authority without assertion edits', () => {
  for (const mode of ['review', 'off'] as const) {
    const h = fixture(),
      ctx = context(),
      prepared = h.service.query(ctx)
    assert.equal(h.service.deliver(ctx, prepared).length, 2)
    if (mode === 'review') {
      h.edges[0]!.status = 'rejected'
      h.edges[0]!.version++
    } else h.off()
    assert.ok(ctx.validations.some((valid) => !valid()))
    assert.deepEqual(h.service.readExposedEntities(ctx), [])
  }
})

test('possible and past unknown-date participation keeps explicit qualifiers rather than fabricating currentness', () => {
  const h = fixture()
  h.edges[0]!.modality = 'possible'
  h.edges[0]!.temporalScope = 'past'
  const result = h.service.query(context())
  assert.equal(result[0]!.relationEvidence[0]!.modality, 'possible')
  assert.equal(result[0]!.relationEvidence[0]!.temporalScope, 'past')
  assert.equal(result[0]!.relationEvidence[0]!.applicability, 'past')
  assert.equal(result[0]!.relationEvidence[0]!.occurredAt, undefined)
})

test('queued learning guard survives ordinary turn completion but rejects relation review or flag changes', () => {
  for (const mode of ['review', 'off', 'backing'] as const) {
    const h = fixture(),
      controller = new AbortController(),
      ctx = { ...context(), signal: controller.signal }
    assert.equal(h.service.captureLearningGuard(ctx), undefined)
    const prepared = h.service.query(ctx)
    assert.equal(h.service.captureLearningGuard(ctx), undefined)
    h.service.deliver(ctx, prepared)
    const guard = h.service.captureLearningGuard(ctx)!
    assert.equal(guard(), true)
    controller.abort('turn_finished')
    assert.equal(guard(), true)
    if (mode === 'review') {
      h.edges[0]!.status = 'rejected'
      h.edges[0]!.version++
    } else if (mode === 'off') h.off()
    else h.edges[0]!.target.backing.deletionEpoch++
    assert.equal(guard(), false)
  }
})
