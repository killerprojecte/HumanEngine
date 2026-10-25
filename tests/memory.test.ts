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

test('SQL top-k matches exact hybrid oracle for rare codes, Unicode, person scope, expiry, and vector spaces', () => {
  const { store, memory } = setup()
  try {
    const definition = {
      providerId: 'p',
      modelId: 'm',
      apiModelName: 'v1',
      deployment: 'local',
      dimensions: 2,
      preprocessing: 'plain',
      normalized: true,
    }
    const space = memory.registerSpace(definition)
    const other = memory.registerSpace({ ...definition, apiModelName: 'v2' })
    let excludedId = '',
      expiredId = ''
    for (let i = 0; i < 65; i++) {
      const record = memory.create({
        scope:
          i % 5 === 0
            ? { kind: 'sharedPublicKnowledge' }
            : {
                kind: 'scene',
                sceneId: i % 7 === 0 ? 'group-b' : 'group-a',
                ...(i % 3 === 0 ? { personId: 'person' } : {}),
              },
        content: `${i === 64 ? 'Rare-ZX:99 ÉCOLE İSTANBUL ΩMEGA' : `Sample ${i} ${i % 2 ? 'apple' : 'pear'}`}`,
        kind: 'fact',
        confidence: (i % 9) / 10,
        importance: (['low', 'medium', 'high'] as const)[i % 3],
        ...(i === 62 ? { validUntil: '2000-01-01T00:00:00Z' } : {}),
      })
      store.db
        .prepare("UPDATE memories SET value=json_set(value,'$.learnedAt',?) WHERE id=?")
        .run(new Date(Date.UTC(2026, 0, 1, 0, 0, i)).toISOString(), record.id)
      if (i % 4 !== 0) memory.index(record.id, space, [i % 2 ? 1 : 0, i % 2 ? 0 : 1])
      memory.index(record.id, other, [0, 1])
      if (i === 7) excludedId = record.id
      if (i === 62) expiredId = record.id
      if (i === 61) memory.update(record.id, { status: 'invalidated' })
    }
    const contexts = [
      evidence('group-a'),
      evidence('group-b'),
      { ...evidence('group-a'), simulation: true },
    ]
    for (const context of contexts)
      for (const personId of [undefined, 'person'])
        for (const query of [
          '',
          'apple pear',
          'Rare-ZX:99 école istanbul ωmega',
          'école',
          'İSTANBUL',
          '!!!',
        ])
          for (const selected of [undefined, space, other]) {
            const terms = [...new Set(query.toLowerCase().match(/[\p{L}\p{N}_:.+-]+/gu) ?? [])]
            const expected = memory
              .list(context, personId)
              .map((record) => {
                const lexical = terms.length
                  ? terms.filter((term) => record.content.toLowerCase().includes(term)).length /
                    terms.length
                  : 0
                const distance = selected
                  ? (store.db
                      .prepare(
                        'SELECT vec_distance_cosine(embedding,?) AS d FROM memory_vectors WHERE memory_id=? AND space_id=?',
                      )
                      .get(Buffer.from(new Float32Array([1, 0]).buffer), record.id, selected) as
                      { d: number } | undefined)
                  : undefined
                const relevance = Math.max(lexical, distance ? Math.max(0, 1 - distance.d) : 0)
                return {
                  ...record,
                  relevance,
                  score:
                    relevance * 0.8 +
                    record.confidence * 0.1 +
                    { low: 0.02, medium: 0.06, high: 0.1 }[record.importance],
                }
              })
              .filter((record) => !query.trim() || record.relevance > 0)
              .sort((a, b) => b.score - a.score || b.learnedAt.localeCompare(a.learnedAt))
              .slice(0, selected ? 7 : 20)
            const actual = memory.search(
              context,
              query,
              personId,
              selected ? { spaceId: selected, vector: [1, 0], limit: 7 } : undefined,
            )
            assert.deepEqual(
              actual.map((r) => r.id),
              expected.map((r) => r.id),
              JSON.stringify({ context, personId, query, selected }),
            )
            actual.forEach((r, i) => assert.ok(Math.abs(r.score! - expected[i]!.score) < 1e-12))
          }
    assert.equal(
      memory.search(evidence('group-a'), 'école')[0]?.content.includes('Rare-ZX:99'),
      true,
    )
    const tied = ['Tie first', 'Tie second'].map((content) =>
      memory.create({
        scope: { kind: 'scene', sceneId: 'group-a' },
        content,
        kind: 'fact',
        importance: 'high',
      }),
    )
    for (const item of tied)
      store.db
        .prepare("UPDATE memories SET value=json_set(value,'$.learnedAt',?) WHERE id=?")
        .run('2026-01-01T00:00:00.000Z', item.id)
    for (const selected of [undefined, space])
      assert.deepEqual(
        memory
          .search(
            evidence('group-a'),
            'tie',
            undefined,
            selected ? { spaceId: selected, vector: [1, 0], limit: 2 } : undefined,
          )
          .slice(0, 2)
          .map((item) => item.id),
        [tied[1]!.id, tied[0]!.id],
      )
    // Invalid vectors in inaccessible/expired records must never reach sqlite-vec.
    store.db
      .prepare('UPDATE memory_vectors SET embedding=? WHERE memory_id IN (?,?)')
      .run(Buffer.from([1]), excludedId, expiredId)
    assert.doesNotThrow(() =>
      memory.search(evidence('group-a'), '', undefined, {
        spaceId: space,
        vector: [1, 0],
        limit: 3,
      }),
    )
  } finally {
    store.close()
  }
})
