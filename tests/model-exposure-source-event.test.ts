import assert from 'node:assert/strict'
import test from 'node:test'
import { Store } from '../src/store.ts'
import { Engine } from '../src/engine.ts'
import type { MemoryRecord } from '../src/memory.ts'
import type { RuntimeExtension, TurnContext } from '../src/runtime.ts'

test('real source-event OR eligibility changes invalidate prepared and delivered batches without assertion revision drift', async () => {
  const store = new Store(':memory:')
  const scene = {
    id: 's',
    kind: 'group' as const,
    adapterId: 'offline',
    accountId: 'account',
    platformSceneId: 's',
  }
  store.saveScene({
    ...scene,
    name: 'Synthetic',
    learningEnabled: true,
    participationEnabled: false,
    proactiveEnabled: false,
  })
  const engine = new Engine(store)
  try {
    const sources = ['a', 'b'].map((messageId) => {
      const source = { sceneId: 's', messageId, senderId: 'person' }
      const eventRef = store.sourceEvents.observe({
        kind: 'ingress',
        identity: { ...source, adapterId: 'offline', accountId: 'account' },
        bodyHash: 'a'.repeat(64),
        occurredAt: null,
      }).ref!
      return { ...source, eventRef }
    })
    for (const source of sources) {
      const result = engine.memory.propose(
        {
          sceneId: 's',
          simulation: false,
          provenance: 'source-only',
          sourceEvidence: [
            {
              ...source,
              available: true,
              isBot: false,
              sourceText: 'Independent synthetic statement.',
            },
          ],
        },
        { content: 'Independent synthetic statement.', kind: 'claim', sources: [source] },
      )
      assert.equal(result.status, 'saved')
    }
    const record = engine.memory.list({ sceneId: 's', simulation: false })[0]!
    assert.equal(record.sources.length, 2)
    const before = engine.memory.revisionState(record.id)
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
    const preparedTurn = turn(),
      deliveredTurn = turn()
    const read = (ctx: TurnContext) =>
      extension.execute(
        'memory.search',
        { query: 'Independent synthetic statement', limit: 20 },
        ctx,
      ) as Promise<MemoryRecord[]>
    const pending = await read(preparedTurn),
      delivered = await read(deliveredTurn)
    assert.equal(
      (extension.delivered!('memory.search', delivered, deliveredTurn) as unknown[]).length,
      1,
    )
    await Promise.resolve()
    store.sourceEvents.observe({
      kind: 'edit',
      identity: {
        sceneId: 's',
        messageId: 'a',
        senderId: 'person',
        adapterId: 'offline',
        accountId: 'account',
      },
      bodyHash: 'b'.repeat(64),
      occurredAt: null,
      editId: 'edit-a',
      order: { kind: 'observed_cas', expectedBase: sources[0]!.eventRef },
    })
    assert.deepEqual(engine.memory.revisionState(record.id), before)
    assert.deepEqual(extension.delivered!('memory.search', pending, preparedTurn), [])
    assert.equal(preparedTurn.dependencies.size, 0)
    assert.equal(
      deliveredTurn.validations.every((valid) => valid()),
      false,
    )
    const freshTurn = turn(),
      fresh = await read(freshTurn)
    assert.equal(fresh.length, 1)
    assert.deepEqual(
      fresh[0]!.sources.map((s) => s.messageId),
      ['b'],
    )
    assert.equal((extension.delivered!('memory.search', fresh, freshTurn) as unknown[]).length, 1)
    assert.equal(
      freshTurn.validations.every((valid) => valid()),
      true,
    )
  } finally {
    await engine.dispose()
    store.close()
  }
})
