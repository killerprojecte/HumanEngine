import assert from 'node:assert/strict'
import test from 'node:test'
import { MemoryService, type MemoryContext, type MemoryCandidate } from '../src/memory.ts'
import { Store } from '../src/store.ts'

function setup() {
  const store = new Store(':memory:')
  for (const id of ['group-a', 'group-b'])
    store.saveScene({
      id,
      kind: 'group',
      adapterId: 'snow',
      accountId: '42',
      platformSceneId: id,
      name: id,
      participationEnabled: true,
      learningEnabled: true,
      proactiveEnabled: false,
    })
  return { store, memory: new MemoryService(store) }
}
function evidence(sceneId: string, messageId = 'm1', senderId = 'person'): MemoryContext {
  return {
    sceneId,
    simulation: false,
    sourceEvidence: [{ sceneId, messageId, senderId, available: true, isBot: false }],
  }
}
function candidate(
  sceneId: string,
  content: string,
  messageId = 'm1',
  senderId = 'person',
): MemoryCandidate {
  return { content, kind: 'fact', personId: 'person', sources: [{ sceneId, messageId, senderId }] }
}
function snapshot(store: Store) {
  return JSON.stringify(
    [
      'memories',
      'memory_sources',
      'memory_vectors',
      'memory_deleted',
      'vector_spaces',
      'recall_tombstones',
      'scenes',
      'config',
    ].map((table) => [table, store.db.prepare(`SELECT * FROM ${table}`).all()]),
  )
}

test('Memory is scene/person scoped and simulation reads only sanitized explicit public knowledge without writes', () => {
  const { store, memory } = setup()
  try {
    memory.propose(evidence('group-a'), candidate('group-a', 'Person prefers tea'))
    memory.propose(evidence('group-b'), candidate('group-b', 'Person prefers coffee'))
    memory.create({
      scope: { kind: 'sharedPublicKnowledge' },
      content: 'Water freezes near zero Celsius',
      kind: 'fact',
      sources: [{ sceneId: 'group-a', messageId: 'public-source', senderId: 'private-identity' }],
    })
    assert.deepEqual(
      memory.list(evidence('group-a'), 'person').map((item) => item.content),
      ['Person prefers tea'],
    )
    assert.deepEqual(
      memory.list(evidence('group-b'), 'person').map((item) => item.content),
      ['Person prefers coffee'],
    )
    const before = snapshot(store)
    const simulation = { sceneId: 'simulation', simulation: true }
    assert.equal(memory.search(simulation, 'water').length, 1)
    assert.deepEqual(memory.search(simulation, 'water')[0]?.sources, [])
    assert.deepEqual(memory.list(simulation, 'person'), [])
    assert.equal(memory.search(simulation, 'tea').length, 0)
    assert.deepEqual(memory.propose(simulation, candidate('group-a', 'No writes')), {
      status: 'rejected',
      reason: 'simulation_does_not_learn',
    })
    assert.equal(snapshot(store), before)
    assert.throws(
      () =>
        memory.create({
          scope: { kind: 'sharedPublicKnowledge', personId: 'person' } as never,
          content: 'Forbidden person',
          kind: 'fact',
        }),
      /invalid_public_scope/,
    )
  } finally {
    store.close()
  }
})

test('Learning validates live nonbot evidence, current switch, lock, delete tombstones, and corrections', () => {
  const { store, memory } = setup()
  try {
    assert.equal(
      memory.propose(
        { sceneId: 'group-a', simulation: false },
        candidate('group-a', 'Missing evidence'),
      ).status,
      'rejected',
    )
    const bot = evidence('group-a')
    bot.sourceEvidence![0]!.isBot = true
    assert.equal(memory.propose(bot, candidate('group-a', 'Self evidence')).status, 'rejected')
    const result = memory.propose(evidence('group-a'), candidate('group-a', 'Tea preference'))
    assert.equal(result.status, 'saved')
    if (result.status !== 'saved') return
    memory.lock(result.memory.id, true)
    assert.equal(
      memory.propose(evidence('group-a'), candidate('group-a', 'Tea preference')).status,
      'rejected',
    )
    const unlocked = memory.lock(result.memory.id, false)
    assert.equal(unlocked.origin, 'learned')
    assert.equal(
      memory.propose(evidence('group-a'), candidate('group-a', 'Tea preference')).status,
      'saved',
    )
    memory.lock(result.memory.id, true)
    assert.equal(
      memory.propose(evidence('group-a'), {
        ...candidate('group-a', 'Coffee preference'),
        supersedesId: result.memory.id,
      }).status,
      'rejected',
    )
    memory.delete(result.memory.id)
    assert.deepEqual(memory.propose(evidence('group-a'), candidate('group-a', 'Tea preference')), {
      status: 'rejected',
      reason: 'manually_deleted',
    })
    store.saveScene({ ...store.scene('group-a')!, learningEnabled: false })
    assert.deepEqual(memory.propose(evidence('group-a'), candidate('group-a', 'Disabled')), {
      status: 'rejected',
      reason: 'learning_disabled',
    })
    store.saveScene({ ...store.scene('group-a')!, learningEnabled: true })
    const old = memory.propose(
      evidence('group-a', 'm2'),
      candidate('group-a', 'Original claim', 'm2'),
    )
    assert.equal(old.status, 'saved')
    if (old.status !== 'saved') return
    const correction = memory.propose(evidence('group-a', 'm3'), {
      ...candidate('group-a', 'Corrected claim', 'm3'),
      supersedesId: old.memory.id,
    })
    assert.equal(correction.status, 'saved')
    assert.equal(
      memory.list(evidence('group-a')).some((item) => item.content === 'Original claim'),
      false,
    )
  } finally {
    store.close()
  }
})

test('Recall atomically invalidates single-source learning, reduces independent multisource confidence, and blocks late commits', () => {
  const { store, memory } = setup()
  try {
    const single = memory.propose(evidence('group-a'), candidate('group-a', 'Single source'))
    const multi = memory.propose(evidence('group-a'), candidate('group-a', 'Multiple sources'))
    memory.propose(
      evidence('group-a', 'm2', 'another-person'),
      candidate('group-a', 'Multiple sources', 'm2', 'another-person'),
    )
    assert.equal(single.status, 'saved')
    assert.equal(multi.status, 'saved')
    if (single.status !== 'saved' || multi.status !== 'saved') return
    const before = memory.adminList().find((item) => item.id === multi.memory.id)!.confidence
    assert.equal(memory.recall('group-a', 'm1').length, 2)
    assert.equal(store.recalled('group-a', 'm1'), true)
    assert.equal(
      memory.adminList().find((item) => item.id === single.memory.id)?.status,
      'invalidated',
    )
    const remaining = memory.list(evidence('group-a')).find((item) => item.id === multi.memory.id)!
    assert.equal(remaining.sources.length, 1)
    assert.equal(remaining.status, 'disputed')
    assert.ok(remaining.confidence < before)
    assert.deepEqual(
      memory.propose(evidence('group-a'), candidate('group-a', 'Late recalled proposal')),
      { status: 'rejected', reason: 'invalid_source_evidence' },
    )
    assert.equal(memory.recall('group-a', 'm1').length, 0)
    memory.recall('group-a', 'm2')
    assert.equal(memory.list(evidence('group-a')).length, 0)
  } finally {
    store.close()
  }
})

test('sqlite-vec exact search filters scope before distance, preserves space identity, and removes stale vectors', () => {
  const { store, memory } = setup()
  try {
    const a = memory.create({
      scope: { kind: 'scene', sceneId: 'group-a' },
      content: 'Apples are fruit',
      kind: 'fact',
    })
    const b = memory.create({
      scope: { kind: 'scene', sceneId: 'group-b' },
      content: 'Secret project',
      kind: 'fact',
    })
    const publicRecord = memory.create({
      scope: { kind: 'sharedPublicKnowledge' },
      content: 'Bananas are fruit',
      kind: 'fact',
    })
    const definition = {
      providerId: 'provider',
      modelId: 'instance-a',
      apiModelName: 'embedding-v1',
      deployment: 'https://provider.test/v1',
      dimensions: 2,
      normalized: true,
      preprocessing: 'plain-v1',
    }
    const space = memory.registerSpace(definition)
    const other = memory.registerSpace({ ...definition, apiModelName: 'embedding-v2' })
    assert.notEqual(space, other)
    memory.index(a.id, space, [0.8, 0.2])
    memory.index(b.id, space, [1, 0])
    memory.index(publicRecord.id, space, [0, 1])
    const results = memory.search(evidence('group-a'), 'unmatched semantic query', undefined, {
      spaceId: space,
      vector: [1, 0],
    })
    assert.equal(results[0]?.id, a.id)
    assert.equal(
      results.some((item) => item.id === b.id),
      false,
    )
    assert.equal(
      memory.search(evidence('group-a'), 'unmatched semantic query', undefined, {
        spaceId: other,
        vector: [1, 0],
      }).length,
      0,
    )
    assert.throws(() => memory.index(a.id, space, [1, 0, 0]), /invalid_vector/)
    memory.update(a.id, { content: 'Apples can be green' })
    assert.equal(
      (
        store.db
          .prepare('SELECT count(*) AS n FROM memory_vectors WHERE memory_id=?')
          .get(a.id) as { n: number }
      ).n,
      0,
    )
    memory.delete(publicRecord.id)
    assert.equal(
      (
        store.db
          .prepare('SELECT count(*) AS n FROM memory_vectors WHERE memory_id=?')
          .get(publicRecord.id) as { n: number }
      ).n,
      0,
    )
    assert.match(store.vectorVersion, /^v?0\.1\.9/)
  } finally {
    store.close()
  }
})
