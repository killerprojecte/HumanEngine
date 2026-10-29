import assert from 'node:assert/strict'
import test from 'node:test'
import { ModelMemoryExposure, type ModelMemoryContext } from '../src/memory/read/model-exposure.ts'
import type { MemoryRecord } from '../src/memory.ts'

function fixture(size = 20) {
  const records: MemoryRecord[] = Array.from({ length: size }, (_, i) => ({
    id: `m${i}`,
    scope: { kind: 'scene', sceneId: 's' },
    content: `Synthetic ${i}`,
    kind: 'claim',
    epistemicKind: 'explicit',
    sources: [{ sceneId: 's', messageId: `source${i}`, senderId: 'p' }],
    importance: 'low',
    confidence: 0.5,
    learnedAt: '2026-10-29T00:00:00Z',
    status: 'active',
    locked: false,
    origin: 'manual',
    lineageStatus: 'unverified',
  }))
  let lists = 0,
    batches = 0,
    throwCallback = false
  let callback: (() => void) | undefined
  const states = new Map(
    records.map((r) => [r.id, { version: 1, deletionEpoch: 0, deleted: false }]),
  )
  const port = {
    list: () => {
      lists++
      return structuredClone(records)
    },
    captureParentReference: () => {
      throw Error('single capture forbidden')
    },
    captureExposedReferences: (_: unknown, input: readonly MemoryRecord[]) => {
      batches++
      const current = new Map(port.list().map((r) => [r.id, r]))
      return input.flatMap((r) => {
        const { score: _score, ...body } = r
        return JSON.stringify(current.get(r.id)) === JSON.stringify(body)
          ? [{ id: r.id, ...states.get(r.id)! }]
          : []
      })
    },
    revisionState: (id: string) => states.get(id)!,
    parentBinding: () => undefined,
  }
  const exposure = new ModelMemoryExposure(port, () => {
    if (throwCallback) throw Error('callback failed')
    callback?.()
  })
  const ctx = (): ModelMemoryContext => ({
    scene: { id: 's' },
    mode: 'real',
    signal: new AbortController().signal,
    dependencies: new Set(),
    validations: [],
  })
  return {
    records,
    states,
    exposure,
    ctx,
    counts: () => ({ lists, batches }),
    duringCallback: (fn: () => void) => {
      callback = fn
    },
    failCallback: () => {
      throwCallback = true
    },
  }
}
test('twenty records use one fresh authorization read per prepare, commit and ongoing validation phase', () => {
  const f = fixture(),
    ctx = f.ctx(),
    prepared = f.exposure.prepare(ctx, f.records)
  assert.equal(prepared.length, 20)
  assert.deepEqual(f.counts(), { lists: 1, batches: 1 })
  assert.equal(f.exposure.commit(ctx, prepared).length, 20)
  assert.deepEqual(f.counts(), { lists: 3, batches: 1 })
  assert.equal(ctx.validations.length, 1)
  assert.equal(ctx.validations[0]!(), true)
  assert.deepEqual(f.counts(), { lists: 4, batches: 1 })
  f.records.splice(5, 1) // Recall between stages, not a TTL-cached authorization.
  assert.equal(ctx.validations[0]!(), false)
  assert.equal(f.counts().lists, 5)
})
test('same-body newer revision, mixed duplicate bodies and failed callback never register exposure', () => {
  const f = fixture(),
    ctx = f.ctx(),
    prepared = f.exposure.prepare(ctx, f.records)
  f.states.get('m0')!.version++
  assert.deepEqual(f.exposure.commit(ctx, prepared), [])
  assert.deepEqual(f.exposure.readExposed(ctx), [])
  assert.equal(ctx.dependencies.size, 0)
  assert.deepEqual(
    f.exposure.prepare(f.ctx(), [
      f.records[1]!,
      { ...f.records[1]!, content: 'stale altered body' },
    ]),
    [],
  )
  const other = f.ctx(),
    fresh = f.exposure.prepare(other, f.records)
  f.failCallback()
  assert.throws(() => f.exposure.commit(other, fresh), /callback failed/)
  assert.deepEqual(f.exposure.readExposed(other), [])
  assert.equal(other.dependencies.size, 0)
})
test('simulation never invokes capture or writes dependencies', () => {
  const f = fixture(),
    ctx = { ...f.ctx(), mode: 'simulation' as const }
  const prepared = f.exposure.prepare(ctx, f.records)
  assert.equal(f.exposure.commit(ctx, prepared).length, 20)
  assert.deepEqual(f.counts(), { lists: 2, batches: 0 })
  assert.equal(ctx.dependencies.size, 0)
  assert.deepEqual(f.exposure.readExposed(ctx), [])
})

test('callback-era authority mutation rejects before any exposure registration', () => {
  const f = fixture(),
    ctx = f.ctx(),
    prepared = f.exposure.prepare(ctx, f.records)
  f.duringCallback(() => {
    f.states.get('m0')!.version++
  })
  assert.deepEqual(f.exposure.commit(ctx, prepared), [])
  assert.deepEqual(f.exposure.readExposed(ctx), [])
  assert.equal(ctx.dependencies.size, 0)
  assert.equal(
    ctx.validations.every((valid) => valid()),
    false,
  )
})

test('person batches above workspace forty keep all results and reauthorize after awaits', async () => {
  const f = fixture(120),
    ctx = f.ctx(),
    prepared = f.exposure.prepare(ctx, f.records)
  assert.equal(prepared.length, 120)
  assert.deepEqual(f.counts(), { lists: 1, batches: 1 })
  await Promise.resolve()
  f.states.get('m119')!.version++
  assert.deepEqual(f.exposure.commit(ctx, prepared), [])
  assert.equal(f.counts().lists, 2)
  assert.equal(ctx.dependencies.size, 0)
  const second = f.ctx(),
    fresh = f.exposure.prepare(second, f.records)
  await Promise.resolve()
  f.records.splice(119, 1)
  assert.deepEqual(f.exposure.commit(second, fresh), [])
  assert.equal(second.dependencies.size, 0)
})

test('same-version source qualification change rejects and a later failed delivery cannot register new siblings', async () => {
  const f = fixture(),
    ctx = f.ctx()
  const first = f.exposure.prepare(ctx, [f.records[0]!])
  assert.equal(f.exposure.commit(ctx, first).length, 1)
  const next = f.exposure.prepare(ctx, [f.records[1]!])
  f.failCallback()
  assert.throws(() => f.exposure.commit(ctx, next), /callback failed/)
  assert.deepEqual(
    f.exposure.readExposed(ctx).map((ref) => ref.id),
    ['m0'],
  )
  assert.equal(ctx.dependencies.has('source1'), false)
  assert.equal(
    ctx.validations.every((valid) => valid()),
    false,
  )
  const other = fixture(),
    pendingContext = other.ctx()
  const pending = other.exposure.prepare(pendingContext, [other.records[0]!])
  await Promise.resolve()
  // Authorized OR-source projection changes while the assertion revision is unchanged.
  other.records[0]!.sources = []
  assert.equal(other.states.get('m0')!.version, 1)
  assert.deepEqual(other.exposure.commit(pendingContext, pending), [])
  assert.equal(pendingContext.dependencies.size, 0)
})
