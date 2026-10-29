import assert from 'node:assert/strict'
import test from 'node:test'
import { Engine } from '../src/engine.ts'
import { Store } from '../src/store.ts'
import type { MemoryRecord } from '../src/memory.ts'
import type { RuntimeExtension, TurnContext } from '../src/runtime.ts'

// Synthetic 1k integration; no provider or platform is contacted.
// Count scene authorization calls; elapsed time is not a production latency guarantee.
test('actual 1k Engine search has constant full-scene reads and fresh delivery guards', async () => {
  const store = new Store(':memory:')
  const scene = {
    id: 'batch-scene',
    kind: 'group' as const,
    adapterId: 'offline',
    accountId: 'synthetic',
    platformSceneId: 'synthetic',
  }
  store.saveScene({
    ...scene,
    name: 'Synthetic',
    participationEnabled: false,
    learningEnabled: false,
    proactiveEnabled: false,
  })
  const engine = new Engine(store)
  try {
    store.db.transaction(() => {
      for (let i = 0; i < 1000; i++)
        engine.memory.create({
          scope: { kind: 'scene', sceneId: scene.id, personId: 'person' },
          content: `Synthetic search needle ${i}; only under stated conditions.`,
          kind: 'claim',
          sources: [{ sceneId: scene.id, messageId: `source-${i}`, senderId: 'person' }],
        })
    })()
    const extension = (
      engine.runtime as unknown as { extensions: RuntimeExtension[] }
    ).extensions.find((e) => e.tools.some((t) => t.name === 'memory.search'))!
    const turn = (): TurnContext => ({
      scene,
      mode: 'real',
      turnId: crypto.randomUUID(),
      signal: new AbortController().signal,
      dependencies: new Set(),
      validations: [],
      deferMemoryExposure: true,
    })
    const original = engine.memory.list.bind(engine.memory)
    let lists = 0
    engine.memory.list = (...args: Parameters<typeof original>) => {
      lists++
      return original(...args)
    }
    const ctx = turn()
    const prepared = (await extension.execute(
      'memory.search',
      { query: 'Synthetic search needle', limit: 20 },
      ctx,
    )) as MemoryRecord[]
    assert.equal(prepared.length, 20)
    assert.ok(lists <= 3, `prepare performed ${lists} full-scene reads`)
    const beforeCommit = lists
    const delivered = extension.delivered!('memory.search', prepared, ctx) as MemoryRecord[]
    assert.equal(delivered.length, 20)
    assert.equal(lists - beforeCommit, 2)
    const beforeValidate = lists
    assert.equal(
      ctx.validations.every((valid) => valid()),
      true,
    )
    assert.equal(lists - beforeValidate, 1)
    // Existing person API is unpaginated: >40 must neither throw nor silently truncate.
    const personTurn = turn(),
      beforePerson = lists
    const personRows = (await extension.execute(
      'memory.person',
      { personId: 'person' },
      personTurn,
    )) as MemoryRecord[]
    assert.equal(personRows.length, 1000)
    assert.equal(lists - beforePerson, 2) // tool list + one atomic exposure capture
    await Promise.resolve()
    engine.memory.update(personRows.at(-1)!.id, { locked: true })
    engine.memory.update(personRows.at(-1)!.id, { locked: false })
    const beforePersonDelivery = lists
    assert.deepEqual(extension.delivered!('memory.person', personRows, personTurn), [])
    assert.equal(lists - beforePersonDelivery, 1)
    assert.equal(personTurn.dependencies.size, 0)
    // Actual same-body revision drift: locked true then false returns identical body,
    // but immutable version must invalidate the original prepared snapshot.
    const other = turn(),
      pending = (await extension.execute(
        'memory.search',
        { query: 'Synthetic search needle', limit: 20 },
        other,
      )) as MemoryRecord[]
    engine.memory.update(pending[0]!.id, { locked: true })
    engine.memory.update(pending[0]!.id, { locked: false })
    assert.deepEqual(extension.delivered!('memory.search', pending, other), [])
    assert.equal(other.dependencies.size, 0)
    // Recall occurs between synchronous prepare and delivery, never hidden by a TTL.
    const recalled = turn(),
      old = (await extension.execute(
        'memory.search',
        { query: 'Synthetic search needle', limit: 20 },
        recalled,
      )) as MemoryRecord[]
    const source = old[0]!.sources[0]!
    engine.memory.recall(source.sceneId, source.messageId)
    assert.deepEqual(extension.delivered!('memory.search', old, recalled), [])
    assert.equal(recalled.dependencies.size, 0)
  } finally {
    await engine.dispose()
    store.close()
  }
})
