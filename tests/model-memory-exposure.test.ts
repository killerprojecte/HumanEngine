import test from 'node:test'
import assert from 'node:assert/strict'
import { Engine } from '../src/engine.ts'
import { Store } from '../src/store.ts'
import type { MemoryRecord } from '../src/memory.ts'
import type { RuntimeExtension, TurnContext } from '../src/runtime.ts'
import type { ExposedMemorySnapshot } from '../src/memory/learning/contextual-proposals.ts'

function fixture() {
  const store = new Store(':memory:')
  const scene = {
    id: 'exposure-scene',
    kind: 'group' as const,
    adapterId: 'offline',
    accountId: 'synthetic',
    platformSceneId: 'synthetic-scene',
  }
  store.saveScene({
    ...scene,
    name: 'Synthetic exposure fixture',
    participationEnabled: false,
    learningEnabled: false,
    proactiveEnabled: false,
  })
  const engine = new Engine(store)
  const extension = (
    engine.runtime as unknown as { extensions: RuntimeExtension[] }
  ).extensions.find((item) => item.tools.some((tool) => tool.name === 'memory.person'))!
  const context = (
    deferMemoryExposure = true,
    mode: 'real' | 'simulation' = 'real',
  ): TurnContext => ({
    scene,
    mode,
    turnId: 'synthetic-turn',
    signal: new AbortController().signal,
    dependencies: new Set(),
    validations: [],
    deferMemoryExposure,
  })
  const create = (content: string, source: string) =>
    engine.memory.create({
      scope: { kind: 'scene', sceneId: scene.id, personId: 'person' },
      content,
      kind: 'claim',
      sources: [{ sceneId: scene.id, messageId: source, senderId: 'speaker' }],
    })
  const read = (ctx: TurnContext) =>
    extension.execute('memory.person', { personId: 'person' }, ctx) as Promise<MemoryRecord[]>
  let lastExposed: readonly ExposedMemorySnapshot[] = []
  engine.contextualLearning.submit = (_ctx, _input, exposed) => {
    lastExposed = exposed
    return { status: 'rejected', reason: 'offline_fixture_capture' }
  }
  const exposed = async (ctx: TurnContext) => {
    await extension.execute('learning.propose', {}, ctx)
    return lastExposed
  }
  return {
    store,
    scene,
    engine,
    extension,
    context,
    create,
    read,
    exposed,
    close: async () => {
      await engine.dispose()
      store.close()
    },
  }
}

test('only selected model-delivered records become dependencies and contextual learning parents', async () => {
  const h = fixture()
  try {
    const included = h.create('Included record', 'included-source')
    const omitted = h.create('Omitted record', 'omitted-source')
    const ctx = h.context()
    const result = await h.read(ctx)
    assert.equal(result.length, 2)
    assert.equal(ctx.dependencies.size, 0)
    assert.equal(
      ctx.validations.length,
      1,
      'only the scene read-policy guard exists, not a memory proof',
    )
    assert.deepEqual(await h.exposed(ctx), [])
    const selected = result.filter((record) => record.id === included.id)
    assert.deepEqual(h.extension.delivered!('memory.person', selected, ctx), selected)
    assert.deepEqual([...ctx.dependencies], ['included-source'])
    assert.deepEqual(
      (await h.exposed(ctx)).map((item) => item.id),
      [included.id],
    )
    assert.ok(ctx.validations.every((valid) => valid()))
    h.engine.memory.recall(h.scene.id, 'omitted-source')
    assert.ok(
      ctx.validations.every((valid) => valid()),
      'an omitted source cannot cancel this turn',
    )
    assert.equal(
      (await h.exposed(ctx)).some((item) => item.id === omitted.id),
      false,
    )
    h.engine.memory.recall(h.scene.id, 'included-source')
    assert.ok(
      ctx.validations.some((valid) => !valid()),
      'delivered evidence still invalidates on recall',
    )
  } finally {
    await h.close()
  }
})

test('delivery rejects forged clones, other contexts, mutated input and same-body revision races', async () => {
  const h = fixture()
  try {
    const record = h.create('Unchanged text does not imply unchanged revision', 'source')
    const ctx = h.context()
    const result = await h.read(ctx)
    assert.throws(() => {
      result[0]!.content = 'forged'
    }, TypeError)
    assert.throws(() => {
      result[0]!.sources.push({ sceneId: 'x', messageId: 'x', senderId: 'x' })
    }, TypeError)
    assert.deepEqual(h.extension.delivered!('memory.person', structuredClone(result), ctx), [])
    assert.deepEqual(
      h.extension.delivered!('memory.person', [result[0], ...structuredClone(result)], ctx),
      [],
    )
    assert.equal(
      ctx.dependencies.size,
      0,
      'a rejected receipt cannot partially commit valid siblings',
    )
    assert.deepEqual(h.extension.delivered!('memory.person', result, h.context()), [])
    h.engine.memory.lock(record.id, true)
    h.engine.memory.lock(record.id, false)
    assert.deepEqual(h.engine.memory.list({ sceneId: h.scene.id, simulation: false })[0], result[0])
    assert.deepEqual(
      h.extension.delivered!('memory.person', result, ctx),
      [],
      'same body with new revision cannot bind old prepared evidence',
    )
    assert.equal(ctx.dependencies.size, 0)
    assert.equal(
      ctx.validations.length,
      1,
      'only the scene read-policy guard exists, not a memory proof',
    )
    assert.deepEqual(await h.exposed(ctx), [])
  } finally {
    await h.close()
  }
})

test('immediate callers retain first-exposure immutability and deferred duplicate reads do not overwrite it', async () => {
  const h = fixture()
  try {
    const record = h.create('First immutable snapshot', 'source')
    const ctx = h.context(false)
    assert.equal((await h.read(ctx)).length, 1)
    const first = await h.exposed(ctx)
    assert.equal(first.length, 1)
    ctx.deferMemoryExposure = true
    const repeat = await h.read(ctx)
    assert.equal((h.extension.delivered!('memory.person', repeat, ctx) as unknown[]).length, 1)
    assert.deepEqual(await h.exposed(ctx), first)
    h.engine.memory.lock(record.id, true)
    const changed = await h.read(ctx)
    assert.deepEqual(h.extension.delivered!('memory.person', changed, ctx), [])
    assert.deepEqual(await h.exposed(ctx), first)
    assert.ok(ctx.validations.some((valid) => !valid()))
  } finally {
    await h.close()
  }
})

test('simulation delivery of public knowledge never binds real sources or migrates revisions', async () => {
  const h = fixture()
  try {
    h.engine.memory.create({
      scope: { kind: 'sharedPublicKnowledge' },
      kind: 'claim',
      content: 'Synthetic public lexical token',
    })
    const ctx = h.context(true, 'simulation')
    const changes = h.store.db.prepare('SELECT total_changes() AS n').get()
    const records = (await h.extension.execute(
      'memory.search',
      { query: 'lexical token' },
      ctx,
    )) as MemoryRecord[]
    assert.equal(records.length, 1)
    assert.deepEqual(h.extension.delivered!('memory.search', records, ctx), records)
    assert.deepEqual(h.store.db.prepare('SELECT total_changes() AS n').get(), changes)
    assert.equal(ctx.dependencies.size, 0)
    assert.equal(ctx.validations.length, 0)
    assert.deepEqual(await h.exposed(ctx), [])
  } finally {
    await h.close()
  }
})

test('derived parent-binding decoration is checked separately from the authoritative record body', async () => {
  const h = fixture()
  try {
    h.store.saveScene({ ...h.store.scene(h.scene.id)!, learningEnabled: true })
    const parent = h.create('Parent used by the child', 'parent-source')
    const source = { sceneId: h.scene.id, messageId: 'child-source', senderId: 'person' }
    const reference = h.engine.memory.captureParentReference(
      { sceneId: h.scene.id, simulation: false },
      parent,
    )!
    const child = h.engine.memory.propose(
      {
        sceneId: h.scene.id,
        simulation: false,
        provenance: 'contextual',
        dependencyMemoryIds: [parent.id],
        dependencyParentReferences: [reference],
        sourceEvidence: [{ ...source, available: true, isBot: false }],
      },
      {
        content: 'A derived child statement',
        kind: 'claim',
        personId: 'person',
        sources: [source],
      },
    )
    assert.equal(child.status, 'saved')
    if (child.status !== 'saved') throw new Error('synthetic_child_not_saved')
    const ctx = h.context()
    const result = (await h.read(ctx)).filter((record) => record.id === child.memory.id)
    assert.equal(result.length, 1)
    assert.equal(result[0]!.parentBinding, 'bound')
    assert.deepEqual(h.extension.delivered!('memory.person', result, ctx), result)
    assert.ok(ctx.validations.every((valid) => valid()))
    assert.deepEqual(
      (await h.exposed(ctx)).map((item) => item.id),
      [child.memory.id],
    )
  } finally {
    await h.close()
  }
})
