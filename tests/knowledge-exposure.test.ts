import test from 'node:test'
import assert from 'node:assert/strict'
import { KnowledgeExposure } from '../src/memory/read/knowledge-exposure.ts'
import type {
  KnowledgeQuery,
  KnowledgeQueryResult,
  KnowledgeEdgeView,
} from '../src/memory/knowledge/contracts.ts'
import type { ModelMemoryContext } from '../src/memory/read/model-exposure.ts'
const context = (): ModelMemoryContext => ({
  scene: { id: 's' },
  mode: 'real',
  signal: new AbortController().signal,
  dependencies: new Set(),
  validations: [],
})
const source = (messageId: string) => ({ sceneId: 's', messageId, senderId: 'p' })
const ref = { id: 'assertion', version: 1, deletionEpoch: 0 }
const entity = {
  id: 'entity',
  sceneId: 's',
  kind: 'tool' as const,
  title: 'Synthetic tool',
  backing: ref,
  originEdgeId: 'edge',
}
const proof = (id: string) => ({ proofId: 'proof-' + id, sources: [source(id)], parents: [] })
function edge(): KnowledgeEdgeView {
  return {
    id: 'edge',
    version: 1,
    sceneId: 's',
    registryVersion: 1,
    predicate: 'uses_tool',
    subject: { kind: 'person', personId: 'p' },
    object: { kind: 'entity', entity },
    polarity: 'negative',
    modality: 'possible',
    temporalScope: 'past',
    conditions: ['Only with permission', 'Reported, not observed'],
    backing: ref,
    backingFingerprint: 'a'.repeat(64),
    proofs: [proof('direct')],
    spans: [
      {
        source: source('span'),
        rootId: 'root',
        partIndex: 0,
        start: 0,
        end: 4,
        spanHash: 'b'.repeat(64),
        stance: 'reported',
      },
    ],
    dependencies: [{ entity, backingFingerprint: 'c'.repeat(64), proofs: [proof('dependency')] }],
    readDependencies: [
      { backing: ref, backingFingerprint: 'd'.repeat(64), proofs: [proof('read-dependency')] },
    ],
    requiresPersonalization: true,
    status: 'candidate',
    admission: 'qualified_candidate',
    admissionPolicyVersion: 1,
    createdAt: '2026-10-29T00:00:00.000Z',
    backingStatus: 'disputed',
    dependencyStatuses: [{ entityId: 'entity', status: 'active' }],
    applicability: 'past',
  }
}
const input: KnowledgeQuery = {
  anchor: { kind: 'person', personId: 'p' },
  predicates: ['uses_tool'],
  depth: 2,
}
function fixture() {
  let allowed = true
  let page: KnowledgeQueryResult = {
    edges: [edge()],
    paths: [['edge']],
    coverage: 'complete',
    interpretation: 'attributed_claims_not_transitive_truth',
    supportClosure: { edges: [], records: [] },
  }
  const calls: unknown[] = []
  const exposure = new KnowledgeExposure({
    allowed: () => allowed,
    query: (ctx, q) => {
      calls.push({ ctx, q })
      return structuredClone(page)
    },
  })
  return {
    exposure,
    calls,
    page: () => page,
    set: (p: KnowledgeQueryResult) => {
      page = p
    },
    disable: () => {
      allowed = false
    },
  }
}
test('whole graph query retains negative/candidate qualifiers and all dependency source ids; only delivered entity authorizes another query', () => {
  const f = fixture(),
    ctx = context()
  const raw = f.exposure.query(ctx, input)
  assert.deepEqual(f.exposure.readEntities(ctx), [])
  const next: KnowledgeQuery = {
    ...input,
    anchor: { kind: 'entity', entityId: 'entity', backingRef: ref },
  }
  assert.deepEqual(f.exposure.query(ctx, next), [])
  assert.deepEqual(f.exposure.deliver(ctx, []), [])
  assert.deepEqual(f.exposure.deliver(ctx, structuredClone(raw)), [])
  assert.deepEqual(f.exposure.deliver(ctx, [...raw, ...raw]), [])
  assert.deepEqual(f.exposure.deliver(context(), raw), [])
  assert.equal(f.exposure.deliver(ctx, raw), raw)
  assert.deepEqual([...ctx.dependencies].sort(), [
    'dependency',
    'direct',
    'read-dependency',
    'span',
  ])
  assert.deepEqual(raw[0]!.page.edges[0]!.conditions, [
    'Only with permission',
    'Reported, not observed',
  ])
  assert.equal(raw[0]!.page.edges[0]!.polarity, 'negative')
  assert.deepEqual(f.exposure.readEntities(ctx), [{ entityId: 'entity', backingRef: ref }])
  const later = f.exposure.query(ctx, next)
  assert.equal(later.length, 1)
  assert.deepEqual(
    f.exposure.query(ctx, {
      ...next,
      anchor: { kind: 'entity', entityId: 'entity', backingRef: { ...ref, version: 2 } },
    }),
    [],
  )
  assert.throws(() => {
    raw[0]!.page.edges[0]!.conditions.push('mutate')
  }, TypeError)
})
test('stale whole page invalidates after prepare and delivery, and false detached authority never vanishes', () => {
  const f = fixture(),
    ctx = context()
  const first = f.exposure.query(ctx, input)
  f.set({ ...f.page(), edges: [{ ...edge(), conditions: ['Changed qualification'] }] })
  assert.deepEqual(f.exposure.deliver(ctx, first), [])
  const fresh = f.exposure.query(ctx, input)
  f.exposure.deliver(ctx, fresh)
  const guard = f.exposure.captureGuard(ctx)!
  assert.equal(guard(), true)
  f.set({ ...f.page(), edges: [], paths: [] })
  assert.equal(guard(), false)
  assert.deepEqual(f.exposure.readEntities(ctx), [])
  assert.equal(f.exposure.captureGuard(ctx)!(), false)
  assert.equal(
    ctx.validations.every((v) => v()),
    false,
  )
})
test('normal turn finish does not cancel queued authority but flag revocation still invalidates it', () => {
  const f = fixture(),
    abort = new AbortController(),
    ctx = { ...context(), signal: abort.signal }
  f.exposure.deliver(ctx, f.exposure.query(ctx, input))
  const guard = f.exposure.captureGuard(ctx)!
  abort.abort(new Error('turn_finished'))
  assert.equal(guard(), true)
  assert.deepEqual(f.exposure.readEntities(ctx), [])
  f.disable()
  assert.equal(guard(), false)
})
test('limited empty page remains explicit whole evidence and never yields entity authority', () => {
  const f = fixture(),
    ctx = context()
  f.set({
    edges: [],
    paths: [],
    coverage: 'limited',
    reason: 'dependency_limit',
    interpretation: 'attributed_claims_not_transitive_truth',
    supportClosure: { edges: [], records: [] },
  })
  const raw = f.exposure.query(ctx, input)
  assert.equal(raw.length, 1)
  f.exposure.deliver(ctx, raw)
  assert.equal(f.exposure.readDelivered(ctx)[0]!.page.reason, 'dependency_limit')
  assert.deepEqual(f.exposure.readEntities(ctx), [])
  assert.deepEqual([...ctx.dependencies], [])
})
test('foreign scope, orphan paths, and oversized core result are rejected rather than sliced', () => {
  for (const page of [
    { edges: [{ ...edge(), sceneId: 'foreign' }], paths: [] },
    { edges: [edge()], paths: [['missing']] },
    { edges: Array(41).fill(edge()), paths: [] },
  ]) {
    const f = fixture()
    f.set({
      ...page,
      coverage: 'complete',
      interpretation: 'attributed_claims_not_transitive_truth',
      supportClosure: { edges: [], records: [] },
    })
    assert.throws(() => f.exposure.query(context(), input), /knowledge_exposure_page_invalid/)
  }
  const f = fixture(),
    ctx = context()
  ctx.mode = 'simulation'
  assert.deepEqual(f.exposure.query(ctx, input), [])
  assert.equal(f.calls.length, 0)
})

test('backing discovery requires a trusted live exposure ticket; prepare or budget omission alone grants no entities', () => {
  const page: KnowledgeQueryResult = {
    edges: [edge()],
    paths: [['edge']],
    coverage: 'complete',
    interpretation: 'attributed_claims_not_transitive_truth',
    supportClosure: { edges: [], records: [] },
  }
  let valid = false,
    calls = 0
  const exposure = new KnowledgeExposure({
    allowed: () => true,
    query: () => page,
    discover: (_ctx, anchor) => {
      calls++
      assert.deepEqual(anchor, ref)
      return structuredClone(page)
    },
  })
  const ctx = context(),
    options = { predicates: input.predicates, depth: input.depth }
  assert.deepEqual(
    exposure.queryFromBacking(ctx, ref, options, () => valid),
    [],
  )
  assert.equal(calls, 0)
  assert.deepEqual(
    exposure.queryFromBacking(ctx, ref, options, undefined as unknown as () => boolean),
    [],
  )
  assert.equal(calls, 0)
  valid = true
  const raw = exposure.queryFromBacking(ctx, ref, options, () => valid)
  assert.equal(raw.length, 1)
  assert.deepEqual(exposure.readEntities(ctx), [])
  exposure.deliver(ctx, [])
  assert.deepEqual(exposure.readEntities(ctx), [])
  exposure.deliver(ctx, raw)
  assert.deepEqual(exposure.readEntities(ctx), [{ entityId: 'entity', backingRef: ref }])
})
test('backing ticket exact generation invalidates even if graph content returns to the same bytes', () => {
  let generation = 1
  const captured = generation,
    ctx = context(),
    page: KnowledgeQueryResult = {
      edges: [edge()],
      paths: [['edge']],
      coverage: 'complete',
      interpretation: 'attributed_claims_not_transitive_truth',
      supportClosure: { edges: [], records: [] },
    }
  const exposure = new KnowledgeExposure({
    allowed: () => true,
    query: () => structuredClone(page),
    discover: () => structuredClone(page),
  })
  const raw = exposure.queryFromBacking(ctx, ref, input, () => generation === captured)
  exposure.deliver(ctx, raw)
  const guard = exposure.captureGuard(ctx)!
  assert.equal(guard(), true)
  generation = 3 // edit then restore identical text does not restore the original ticket generation
  assert.equal(guard(), false)
  assert.deepEqual(exposure.readEntities(ctx), [])
  assert.equal(exposure.captureGuard(ctx)!(), false)
  assert.deepEqual(
    exposure.queryFromBacking(ctx, ref, input, () => true),
    [],
  ) // prior invalid graph cannot downgrade
})
test('backing guard survives normal turn finish but errors and revocation reject pending delivery', () => {
  const ctx = context(),
    abort = new AbortController()
  ctx.signal = abort.signal
  let valid = true
  const page: KnowledgeQueryResult = {
    edges: [edge()],
    paths: [['edge']],
    coverage: 'complete',
    interpretation: 'attributed_claims_not_transitive_truth',
    supportClosure: { edges: [], records: [] },
  }
  const exposure = new KnowledgeExposure({
    allowed: () => true,
    query: () => page,
    discover: () => page,
  })
  const raw = exposure.queryFromBacking(ctx, ref, input, () => valid)
  exposure.deliver(ctx, raw)
  const guard = exposure.captureGuard(ctx)!
  abort.abort(new Error('turn_finished'))
  assert.equal(guard(), true)
  valid = false
  assert.equal(guard(), false)
  const other = context()
  let throws = false
  const pending = exposure.queryFromBacking(other, ref, input, () => {
    if (throws) throw new Error('stale ticket')
    return true
  })
  throws = true
  assert.deepEqual(exposure.deliver(other, pending), [])
})

test('policy identity OFF to ON transition cannot revive old prepared, delivered, or detached authority', () => {
  let enabled = true,
    identity = 1
  const page: KnowledgeQueryResult = {
    edges: [edge()],
    paths: [['edge']],
    coverage: 'complete',
    interpretation: 'attributed_claims_not_transitive_truth',
    supportClosure: { edges: [], records: [] },
  }
  const exposure = new KnowledgeExposure({
    allowed: () => enabled,
    policyIdentity: () => identity,
    query: () => structuredClone(page),
  })
  const pendingCtx = context(),
    deliveredCtx = context()
  const pending = exposure.query(pendingCtx, input)
  exposure.deliver(deliveredCtx, exposure.query(deliveredCtx, input))
  const detached = exposure.captureGuard(deliveredCtx)!
  assert.equal(detached(), true)
  enabled = false
  identity++
  assert.equal(detached(), false)
  enabled = true
  identity++
  assert.deepEqual(exposure.deliver(pendingCtx, pending), [])
  assert.deepEqual(exposure.readDelivered(deliveredCtx), [])
  assert.deepEqual(exposure.readEntities(deliveredCtx), [])
  assert.equal(detached(), false)
  assert.equal(exposure.captureGuard(deliveredCtx)!(), false)
  const fresh = context()
  assert.equal(exposure.deliver(fresh, exposure.query(fresh, input)).length, 1)
})

test('complete support closure preserves negative qualifications and ordinary dependency bodies and registers every source', () => {
  const f = fixture(),
    ctx = context()
  const dependency = {
    ...edge(),
    id: 'support-edge',
    proofs: [proof('closure-direct')],
    spans: [{ ...edge().spans[0]!, source: source('closure-span') }],
    readDependencies: [
      { backing: ref, backingFingerprint: 'd'.repeat(64), proofs: [proof('closure-read')] },
    ],
    dependencies: [],
    conditions: ['Unless explicitly cancelled', 'Reported, not confirmed'],
    requiresPersonalization: true,
  }
  const record = {
    id: 'ordinary',
    scope: { kind: 'scene' as const, sceneId: 's', personId: 'p' },
    kind: 'claim' as const,
    content: 'Support is conditional; not an established preference',
    sources: [source('ordinary-source')],
    importance: 'medium' as const,
    confidence: 0.4,
    learnedAt: '2026-10-29T00:00:00.000Z',
    status: 'disputed' as const,
    locked: false,
    origin: 'learned' as const,
    epistemicKind: 'reported' as const,
    validUntil: '2026-11-01T00:00:00.000Z',
  }
  const supportClosure = {
    edges: [dependency],
    records: [{ ref: { ...ref, id: 'ordinary' }, record }],
  }
  f.set({ ...f.page(), supportClosure })
  const raw = f.exposure.query(ctx, input)
  f.exposure.deliver(ctx, raw)
  assert.deepEqual(raw[0]!.page.supportClosure, supportClosure)
  assert.deepEqual([...ctx.dependencies].sort(), [
    'closure-direct',
    'closure-read',
    'closure-span',
    'dependency',
    'direct',
    'ordinary-source',
    'read-dependency',
    'span',
  ])
  assert.deepEqual(
    f.exposure.readEdges(ctx).map((e) => e.id),
    ['edge', 'support-edge'],
  )
  const guard = f.exposure.captureGuard(ctx)!
  f.set({
    ...f.page(),
    supportClosure: {
      ...supportClosure,
      records: [
        {
          ref: { ...ref, id: 'ordinary' },
          record: { ...record, content: 'Later corrected support' },
        },
      ],
    },
  })
  assert.equal(guard(), false)
  assert.deepEqual(f.exposure.readDelivered(ctx), [])
})
