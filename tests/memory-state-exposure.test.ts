import test from 'node:test'
import assert from 'node:assert/strict'
import { StateExposure } from '../src/memory/read/state-exposure.ts'
import type { ModelMemoryContext } from '../src/memory/read/model-exposure.ts'
const context = (): ModelMemoryContext => ({
  scene: { id: 'scene' },
  mode: 'real',
  signal: new AbortController().signal,
  dependencies: new Set(),
  validations: [],
})
function fixture() {
  let version = 1,
    calls = 0
  const originals = [
    { id: 'a', version: 1, deletionEpoch: 0, conditions: ['only when explicit'] },
    { id: 'b', version: 1, deletionEpoch: 0, conditions: ['also this condition'] },
  ]
  const exposure = new StateExposure({
    query: (_ctx: ModelMemoryContext, _input: string) => originals,
    valid: (ctx, values) => ctx.scene.id === 'scene' && values.every((v) => v.version === version),
    sourceIds: (values) => values.map((v) => `source:${v.id}`),
    onDelivered: () => {
      calls++
    },
  })
  return {
    exposure,
    originals,
    change() {
      version++
    },
    calls: () => calls,
  }
}

test('prepared snapshots require original whole group and exact context; omissions confer no authority', () => {
  const f = fixture(),
    ctx = context(),
    values = f.exposure.query(ctx, 'query')
  assert.ok(Object.isFrozen(values) && Object.isFrozen(values[0]!.conditions))
  f.originals[0]!.conditions[0] = 'external mutation'
  assert.equal(values[0]!.conditions[0], 'only when explicit')
  for (const candidate of [
    [],
    [values[0]],
    structuredClone(values),
    [values[1], values[0]],
    [values[0], values[0]],
  ])
    assert.deepEqual(f.exposure.deliver(ctx, candidate), [])
  assert.deepEqual(f.exposure.deliver(context(), values), [])
  assert.equal(f.exposure.captureGuard(ctx), undefined)
  assert.equal(ctx.dependencies.size, 0)
  assert.equal(f.calls(), 0)
  assert.equal(f.exposure.deliver(ctx, values), values)
  assert.equal(f.exposure.deliver(ctx, values), values)
  assert.equal(f.calls(), 1)
  assert.deepEqual([...ctx.dependencies], ['source:a', 'source:b'])
  assert.equal(ctx.validations.length, 1)
  assert.equal(ctx.validations[0]!(), true)
  assert.deepEqual(f.exposure.readDelivered(ctx), values)
})

test('stale state before/after delivery never becomes a latest snapshot and queued guard survives turn finish', () => {
  const f = fixture(),
    ctx = context(),
    controller = new AbortController()
  ctx.signal = controller.signal
  const values = f.exposure.query(ctx, 'query')
  f.exposure.deliver(ctx, values)
  const guard = f.exposure.captureGuard(ctx)!
  controller.abort('turn_finished')
  assert.equal(guard(), true)
  assert.deepEqual(f.exposure.readDelivered(ctx), [])
  f.change()
  assert.equal(guard(), false)
  const after = f.exposure.captureGuard(ctx)
  assert.equal(typeof after, 'function')
  assert.equal(after!(), false)
  assert.equal(ctx.validations[0]!(), false)
  const other = fixture(),
    fresh = context(),
    prepared = other.exposure.query(fresh, 'query')
  other.change()
  assert.deepEqual(other.exposure.deliver(fresh, prepared), [])
  assert.equal(other.calls(), 0)
  assert.equal(other.exposure.captureGuard(fresh), undefined)
})

test('callback authority change or exception poisons the turn without registering delivered snapshots', () => {
  for (const throws of [false, true]) {
    const ctx = context()
    let valid = true
    const exposure = new StateExposure({
      query: () => [{ id: 'one', mutationVersion: 9 }],
      valid: () => valid,
      sourceIds: () => ['source'],
      onDelivered: () => {
        valid = false
        if (throws) throw new Error('recovery_authority_head_changed')
      },
    })
    const values = exposure.query(ctx, undefined)
    if (throws)
      assert.throws(() => exposure.deliver(ctx, values), /recovery_authority_head_changed/)
    else assert.deepEqual(exposure.deliver(ctx, values), [])
    assert.equal(ctx.validations.at(-1)!(), false)
    assert.equal(ctx.dependencies.size, 0)
    assert.equal(exposure.captureGuard(ctx), undefined)
  }
})

test('groups from separate queries cannot be spliced and simulation/aborted queries do not read storage', () => {
  const f = fixture(),
    ctx = context(),
    first = f.exposure.query(ctx, 'a'),
    second = f.exposure.query(ctx, 'b')
  assert.deepEqual(f.exposure.deliver(ctx, [first[0], second[1]]), [])
  let calls = 0
  const exposure = new StateExposure({
    query: () => {
      calls++
      return [{ id: 'a' }]
    },
    valid: () => true,
    sourceIds: () => [],
  })
  assert.deepEqual(exposure.query({ ...context(), mode: 'simulation' }, undefined), [])
  assert.deepEqual(exposure.query({ ...context(), signal: AbortSignal.abort() }, undefined), [])
  assert.equal(calls, 0)
})
