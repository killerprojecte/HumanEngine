import assert from 'node:assert/strict'
import test from 'node:test'
import { MemoryService, type MemoryCandidate, type MemoryRecord } from '../src/memory.ts'
import { Store } from '../src/store.ts'

function setup() {
  const store = new Store(':memory:')
  for (const id of ['a', 'b'])
    store.saveScene({
      id,
      kind: 'group',
      adapterId: 'local',
      accountId: 'bot',
      platformSceneId: id,
      name: id,
      participationEnabled: true,
      learningEnabled: true,
      proactiveEnabled: false,
    })
  const memory = new MemoryService(store)
  const propose = (
    content: string,
    messageId: string,
    patch: Partial<MemoryCandidate> = {},
    sceneId = 'a',
  ) => {
    const source = { sceneId, messageId, senderId: 'person' }
    return memory.propose(
      {
        sceneId,
        simulation: false,
        provenance: 'source-only',
        sourceEvidence: [{ ...source, available: true, isBot: false }],
      },
      {
        content,
        kind: 'fact',
        personId: 'person',
        sources: [source],
        ...patch,
      },
    )
  }
  const saved = (result: ReturnType<typeof propose>): MemoryRecord => {
    assert.equal(result.status, 'saved', JSON.stringify(result))
    if (result.status !== 'saved') throw new Error('expected_saved')
    return result.memory
  }
  const a = saved(propose('Person prefers tea', 'm1'))
  const b = saved(propose('Person prefers coffee', 'm2', { supersedesId: a.id }))
  return { store, memory, propose, saved, a, b }
}

test('Repeated preference reversions create fresh acyclic versions and replace derived vectors', () => {
  const { store, memory, propose, saved, a, b } = setup()
  try {
    const space = memory.registerSpace({
      providerId: 'local',
      modelId: 'embedding',
      apiModelName: 'embedding',
      deployment: 'local',
      dimensions: 2,
      preprocessing: 'plain',
      normalized: true,
    })
    memory.index(b.id, space, [1, 0])
    const nextA = saved(propose(a.content, 'm3', { supersedesId: b.id }))
    assert.notEqual(nextA.id, a.id)
    assert.equal(nextA.supersedesId, b.id)
    assert.deepEqual(
      nextA.sources.map((s) => s.messageId),
      ['m3'],
    )
    assert.equal(memory.adminList().find((m) => m.id === a.id)?.status, 'superseded')
    assert.equal(memory.adminList().find((m) => m.id === b.id)?.status, 'superseded')
    assert.equal(
      (store.db.prepare('SELECT count(*) AS n FROM memory_vectors').get() as { n: number }).n,
      0,
    )
    assert.throws(() => memory.index(a.id, space, [1, 0]), /memory_not_indexable/)
    memory.index(nextA.id, space, [0, 1])
    assert.deepEqual(
      memory
        .search({ sceneId: 'a', simulation: false }, 'tea', 'person', {
          spaceId: space,
          vector: [0, 1],
        })
        .map((m) => m.id),
      [nextA.id],
    )
    assert.deepEqual(memory.search({ sceneId: 'b', simulation: false }, 'tea'), [])
    const nextB = saved(propose(b.content, 'm4', { supersedesId: nextA.id }))
    assert.notEqual(nextB.id, b.id)
    assert.equal(nextB.supersedesId, nextA.id)
    assert.equal(propose(a.content, 'm1', { supersedesId: nextB.id }).status, 'rejected')
    const lastA = saved(propose(a.content, 'm5', { supersedesId: nextB.id }))
    assert.notEqual(lastA.id, nextA.id)
    assert.equal(lastA.supersedesId, nextB.id)
    assert.equal(memory.adminList().length, 5)
    memory.lock(a.id, true)
    memory.lock(a.id, false)
    assert.throws(() => memory.update(a.id, { status: 'active' }), /UNIQUE constraint/)
    // Old source retraction must remain historical, never retract the new statement.
    memory.recall('a', 'm1')
    memory.recall('a', 'm3')
    assert.deepEqual(
      memory.list({ sceneId: 'a', simulation: false }).map((m) => m.id),
      [lastA.id],
    )
    memory.recall('a', 'm5')
    assert.deepEqual(memory.list({ sceneId: 'a', simulation: false }), [])
    assert.equal(propose(a.content, 'm6', { supersedesId: nextB.id }).status, 'rejected')
  } finally {
    store.close()
  }
})

test('Deleting an archived version keeps the current record but blocks automatic content reuse', () => {
  const { store, memory, propose, saved, a, b } = setup()
  try {
    const nextA = saved(propose(a.content, 'm3', { supersedesId: b.id }))
    assert.equal(memory.delete(a.id), true)
    assert.deepEqual(
      memory.list({ sceneId: 'a', simulation: false }).map((m) => m.id),
      [nextA.id],
    )
    assert.deepEqual(propose(a.content, 'm4'), { status: 'rejected', reason: 'manually_deleted' })
  } finally {
    store.close()
  }
})

test('Reversions require a current same-scope correction and fresh evidence, not historical replay', () => {
  const { store, memory, propose, saved, a, b } = setup()
  try {
    const before = () => JSON.stringify(memory.adminList())
    const initial = before()
    assert.equal(propose(a.content, 'm3').status, 'rejected')
    assert.equal(propose(a.content, 'm1', { supersedesId: b.id }).status, 'rejected')
    assert.equal(propose(a.content, 'm2', { supersedesId: b.id }).status, 'rejected')
    assert.equal(
      memory.propose(
        {
          sceneId: 'a',
          simulation: false,
          provenance: 'source-only',
          sourceEvidence: ['m1', 'm3'].map((messageId) => ({
            sceneId: 'a',
            messageId,
            senderId: 'person',
            available: true,
            isBot: false,
          })),
        },
        {
          content: a.content,
          kind: 'fact',
          personId: 'person',
          supersedesId: b.id,
          sources: ['m1', 'm3'].map((messageId) => ({
            sceneId: 'a',
            messageId,
            senderId: 'person',
          })),
        },
      ).status,
      'rejected',
    )
    assert.equal(propose(a.content, 'm3', { supersedesId: b.id }, 'b').status, 'rejected')
    assert.equal(
      propose(a.content, 'm3', { personId: 'other', supersedesId: b.id }).status,
      'rejected',
    )
    assert.equal(propose(a.content, 'm3', { supersedesId: a.id }).status, 'rejected')
    const unrelated = saved(propose('Person prefers quiet rooms', 'm4'))
    assert.equal(propose(a.content, 'm3', { supersedesId: unrelated.id }).status, 'rejected')
    memory.delete(unrelated.id)
    assert.equal(before(), initial)
    const corrected = saved(propose(a.content, 'm5', { supersedesId: b.id }))
    const otherSource = saved(propose(a.content, 'm6'))
    assert.equal(otherSource.id, corrected.id)
    assert.equal(otherSource.sources.length, 2)
    memory.recall('a', 'm5')
    const disputed = memory.list({ sceneId: 'a', simulation: false })[0]!
    assert.equal(disputed.status, 'disputed')
    assert.deepEqual(
      disputed.sources.map((s) => s.messageId),
      ['m6'],
    )
  } finally {
    store.close()
  }
})

test('Reversion archival and supersession roll back together when inserting the new version fails', () => {
  const { store, memory, propose, saved, a, b } = setup()
  try {
    const space = memory.registerSpace({
      providerId: 'local',
      modelId: 'embedding',
      apiModelName: 'embedding',
      deployment: 'local',
      dimensions: 2,
      preprocessing: 'plain',
      normalized: true,
    })
    memory.index(b.id, space, [1, 0])
    const snapshot = () =>
      JSON.stringify(
        ['memories', 'memory_sources', 'memory_vectors', 'memory_deleted'].map((table) => [
          table,
          store.db.prepare(`SELECT * FROM ${table}`).all(),
        ]),
      )
    const before = snapshot()
    store.db.exec(`CREATE TEMP TRIGGER reject_reversion BEFORE INSERT ON memories
      WHEN NOT EXISTS (SELECT 1 FROM memories WHERE id=NEW.id)
      BEGIN SELECT RAISE(ABORT, 'injected_insert_failure'); END`)
    assert.throws(() => propose(a.content, 'm3', { supersedesId: b.id }), /injected_insert_failure/)
    assert.equal(snapshot(), before)
    store.db.exec('DROP TRIGGER reject_reversion')
    saved(propose(a.content, 'm3', { supersedesId: b.id }))
  } finally {
    store.close()
  }
})

test('Reversions never revive manually protected, deleted, or recalled historical records', () => {
  for (const protection of ['lock', 'manual', 'delete', 'recall', 'current-lock'] as const) {
    const { store, memory, propose, a, b } = setup()
    try {
      if (protection === 'lock') memory.lock(a.id, true)
      if (protection === 'manual') memory.update(a.id, { confidence: 0.9 })
      if (protection === 'delete') memory.delete(a.id)
      if (protection === 'recall') memory.recall('a', 'm1')
      if (protection === 'current-lock') memory.lock(b.id, true)
      const before = JSON.stringify(memory.adminList())
      assert.equal(propose(a.content, 'm3', { supersedesId: b.id }).status, 'rejected', protection)
      assert.equal(JSON.stringify(memory.adminList()), before, protection)
    } finally {
      store.close()
    }
  }
})

test('Reversion rejects incomplete or cyclic correction history without changing current memory', () => {
  for (const broken of ['missing', 'cycle'] as const) {
    const { store, memory, propose, saved, a, b } = setup()
    try {
      const nextA = saved(propose(a.content, 'm3', { supersedesId: b.id }))
      const nextB = saved(propose(b.content, 'm4', { supersedesId: nextA.id }))
      if (broken === 'missing') memory.delete(b.id)
      else
        store.db
          .prepare("UPDATE memories SET value=json_set(value,'$.supersedesId',?) WHERE id=?")
          .run(nextB.id, a.id)
      const before = JSON.stringify(memory.adminList())
      assert.equal(propose(a.content, 'm1', { supersedesId: nextB.id }).status, 'rejected')
      assert.equal(propose(a.content, 'm5', { supersedesId: nextB.id }).status, 'rejected')
      assert.equal(JSON.stringify(memory.adminList()), before)
    } finally {
      store.close()
    }
  }
})

test('Exact and semantic duplicates cannot reattach superseded source evidence to the current version', () => {
  const { store, memory, propose, saved, a, b } = setup()
  try {
    const nextA = saved(propose(a.content, 'm3', { supersedesId: b.id }))
    const before = JSON.stringify(memory.adminList())
    assert.equal(propose(a.content, 'm1').status, 'rejected')
    assert.equal(
      propose('Tea is still the preference', 'm1', { mergeIntoId: nextA.id }).status,
      'rejected',
    )
    assert.equal(JSON.stringify(memory.adminList()), before)
    assert.equal(saved(propose(a.content, 'm3')).id, nextA.id)
    assert.equal(
      propose('Tea is still the preference', 'm4', { mergeIntoId: nextA.id }).status,
      'rejected',
    )
    assert.equal(saved(propose(a.content, 'm4', { mergeIntoId: nextA.id })).id, nextA.id)
    memory.recall('a', 'm3')
    assert.deepEqual(
      memory.list({ sceneId: 'a', simulation: false })[0]?.sources.map((s) => s.messageId),
      ['m4'],
    )
    memory.recall('a', 'm4')
    assert.deepEqual(memory.list({ sceneId: 'a', simulation: false }), [])
  } finally {
    store.close()
  }
})
