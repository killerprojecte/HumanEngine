import test from 'node:test'
import assert from 'node:assert/strict'
import { Store } from '../src/store.ts'
import { Engine } from '../src/engine.ts'
import { createApp } from '../src/api.ts'

function fixture() {
  const store = new Store(':memory:')
  const engine = new Engine(store)
  const app = createApp(store, { token: 'synthetic-history-test', engine })
  for (const id of ['one', 'other'])
    store.saveScene({
      id,
      kind: 'group',
      name: 'Synthetic scene',
      adapterId: 'fixture',
      accountId: 'synthetic',
      platformSceneId: id,
      learningEnabled: false,
      participationEnabled: false,
      proactiveEnabled: false,
    })
  const url = (id: string, sceneId = 'one') =>
    `/api/memory-history?${new URLSearchParams({ sceneId, id })}`
  const get = (id: string, sceneId = 'one') =>
    app.request(url(id, sceneId), { headers: { Authorization: 'Bearer synthetic-history-test' } })
  return {
    store,
    engine,
    app,
    url,
    get,
    close: () => {
      engine.dispose()
      store.close()
    },
  }
}

test('retained history API preserves independent times and uncertainty without exposing internal provenance', async () => {
  const h = fixture()
  try {
    const memory = h.engine.memory.create({
      scope: { kind: 'scene', sceneId: 'one', personId: 'person' },
      kind: 'claim',
      content: 'A conditional statement',
      epistemicKind: 'reported',
      occurredAt: '2026-01-01T12:00:00.000Z',
      validFrom: '2026-02-01T00:00:00.000Z',
      validUntil: '2035-03-01T00:00:00.000Z',
      sources: [
        { sceneId: 'one', messageId: 'PRIVATE_SOURCE_MARKER', senderId: 'PRIVATE_SPEAKER_MARKER' },
      ],
    })
    h.engine.memory.lock(memory.id, true)
    assert.equal((await h.app.request(h.url(memory.id))).status, 401)
    const response = await h.get(memory.id)
    assert.equal(response.status, 200)
    const body = await response.json()
    assert.equal(body.revisions.length, 2)
    assert.deepEqual(Object.keys(body), ['revisions', 'coverage'])
    assert.equal(body.coverage, 'complete')
    assert.equal(body.revisions[0].parentBinding, 'none')
    assert.equal(body.revisions[0].validFrom, '2026-02-01T00:00:00.000Z')
    assert.equal(body.revisions[0].validTo, '2035-03-01T00:00:00.000Z')
    assert.equal(body.revisions[0].occurredAt, '2026-01-01T12:00:00.000Z')
    assert.notEqual(body.revisions[0].recordedAt, body.revisions[0].occurredAt)
    assert.equal(body.revisions[0].epistemicKind, 'reported')
    const wire = JSON.stringify(body)
    for (const forbidden of [
      'PRIVATE_SOURCE_MARKER',
      'PRIVATE_SPEAKER_MARKER',
      'deletionEpoch',
      'assertionId',
      'supports',
      'actorId',
      'legacyImport',
    ])
      assert.equal(wire.includes(forbidden), false)
    assert.equal((await h.get(memory.id, 'other')).status, 404)
    assert.equal((await h.get('unknown')).status, 404)
    const changes = h.store.db.prepare('SELECT total_changes() AS n').get()
    assert.equal((await h.get(memory.id, 'simulation')).status, 404)
    assert.deepEqual(h.store.db.prepare('SELECT total_changes() AS n').get(), changes)
    for (const path of [
      '/api/memory-history?sceneId=one',
      '/api/memory-history?sceneId=one&id=x&extra=1',
    ])
      assert.equal(
        (await h.app.request(path, { headers: { Authorization: 'Bearer synthetic-history-test' } }))
          .status,
        400,
      )
    h.engine.memory.recall('one', 'PRIVATE_SOURCE_MARKER')
    assert.equal((await h.get(memory.id)).status, 404)
  } finally {
    h.close()
  }
})

test('unknown historical times stay unknown and deleted record history is inaccessible', async () => {
  const h = fixture()
  try {
    const record = h.engine.memory.create({
      scope: { kind: 'scene', sceneId: 'one', personId: 'person' },
      kind: 'claim',
      content: 'An administrator note with unknown time',
    })
    const body = await (await h.get(record.id)).json()
    assert.equal(body.revisions[0].validFrom, null)
    assert.equal(body.revisions[0].validTo, null)
    assert.equal(body.revisions[0].occurredAt, undefined)
    assert.equal(body.revisions[0].epistemicKind, undefined)
    h.engine.memory.delete(record.id)
    assert.equal((await h.get(record.id)).status, 404)
  } finally {
    h.close()
  }
})

test('history API inspects exact retained bound premises after version drift without making them current answers', async () => {
  const h = fixture()
  try {
    h.store.saveScene({ ...h.store.scene('one')!, learningEnabled: true })
    const parent = h.engine.memory.create({
      scope: { kind: 'scene', sceneId: 'one' },
      kind: 'fact',
      content: 'Synthetic previous premise',
      sources: [{ sceneId: 'one', messageId: 'parent-source', senderId: 'fixture-person' }],
    })
    const reference = h.engine.memory.captureParentReference(
      { sceneId: 'one', simulation: false },
      parent,
    )!
    const source = { sceneId: 'one', messageId: 'child-source', senderId: 'fixture-person' }
    const result = h.engine.memory.propose(
      {
        sceneId: 'one',
        simulation: false,
        provenance: 'contextual',
        dependencyMemoryIds: [parent.id],
        dependencyParentReferences: [reference],
        sourceEvidence: [{ ...source, available: true, isBot: false }],
      },
      { content: 'Synthetic dependent statement', kind: 'claim', sources: [source] },
    )
    assert.equal(result.status, 'saved')
    if (result.status !== 'saved') throw new Error('fixture_not_saved')
    h.engine.memory.update(parent.id, { content: 'Synthetic revised premise' })
    assert.equal(
      h.engine.memory
        .list({ sceneId: 'one', simulation: false })
        .some((m) => m.id === result.memory.id),
      false,
    )
    const response = await h.get(result.memory.id)
    assert.equal(response.status, 200)
    const body = await response.json()
    assert.equal(body.revisions[0].content, 'Synthetic dependent statement')
    assert.equal(body.revisions[0].parentBinding, 'bound')
    assert.equal(JSON.stringify(body).includes(parent.id), false)
    assert.equal((await h.get(result.memory.id, 'other')).status, 404)
    h.engine.memory.recall('one', 'parent-source')
    assert.equal((await h.get(result.memory.id)).status, 404)
  } finally {
    h.close()
  }
})

test('history API explicitly reports bounded latest coverage and a traversal failure is not an empty success', async () => {
  const h = fixture()
  try {
    const record = h.engine.memory.create({
      scope: { kind: 'scene', sceneId: 'one' },
      kind: 'fact',
      content: 'Synthetic retained timeline',
    })
    for (let index = 0; index < 205; index++) h.engine.memory.lock(record.id, index % 2 === 0)
    const response = await h.get(record.id)
    assert.equal(response.status, 200)
    const body = await response.json()
    assert.equal(body.coverage, 'limited')
    assert.equal(body.revisions.length, 200)
    assert.ok(body.revisions[0].version > 1)
    assert.equal(body.revisions.at(-1).version, h.engine.memory.revisionState(record.id).version)
    const { ProofTraversalLimit } = await import('../src/memory/read/provenance.ts')
    h.engine.memory.historyFor = () => {
      throw new ProofTraversalLimit()
    }
    const limited = await h.get(record.id)
    assert.equal(limited.status, 503)
    assert.equal(
      (await limited.json()).error,
      'Memory history exceeded its safe evidence traversal limit',
    )
  } finally {
    h.close()
  }
})
