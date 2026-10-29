import test from 'node:test'
import assert from 'node:assert/strict'
import { Store } from '../src/store.ts'
import { Engine } from '../src/engine.ts'
import { createApp } from '../src/api.ts'

test('person API uses authenticated exact-scene projections with no nickname identity merging', async () => {
  const store = new Store(':memory:')
  const engine = new Engine(store)
  const app = createApp(store, { token: 'synthetic-person-test', engine })
  const headers = { Authorization: 'Bearer synthetic-person-test' }
  for (const [id, kind] of [
    ['a', 'group'],
    ['b', 'private'],
  ] as const)
    store.saveScene({
      id,
      kind,
      adapterId: 'fixture',
      accountId: 'synthetic',
      platformSceneId: id,
      name: 'Synthetic scene',
      learningEnabled: false,
      participationEnabled: false,
      proactiveEnabled: false,
    })
  for (const personId of ['person-one', 'person-two']) {
    store.set(`identity:a:${personId}`, {
      senderDisplayName: 'Same nickname',
      senderPlatformId: 'DO_NOT_EXPOSE_PLATFORM_ID',
    })
    engine.memory.create({
      scope: { kind: 'scene', sceneId: 'a', personId },
      kind: 'claim',
      content: `Scoped ${personId}`,
    })
  }
  engine.memory.create({
    scope: { kind: 'scene', sceneId: 'b', personId: 'person-one' },
    kind: 'fact',
    content: 'PRIVATE_OTHER_SCENE',
  })
  engine.memory.create({
    scope: { kind: 'scene', sceneId: 'a' },
    kind: 'fact',
    content: 'UNRESOLVED_SUBJECT',
  })
  engine.memory.create({
    scope: { kind: 'sharedPublicKnowledge' },
    kind: 'fact',
    content: 'PUBLIC_NOT_PERSON',
  })
  const get = (url: string) => app.request(url, { headers })
  try {
    assert.equal((await app.request('/api/memory-people?sceneId=a')).status, 401)
    assert.equal(
      (await app.request('/api/memory-people/profile?sceneId=a&personId=person-one')).status,
      401,
    )
    const list = await (await get('/api/memory-people?sceneId=a')).json()
    assert.equal(list.people.length, 2)
    assert.deepEqual(
      list.people.map((p: { personId: string }) => p.personId),
      ['person-one', 'person-two'],
    )
    assert.ok(list.people.every((p: { displayName: string }) => p.displayName === 'Same nickname'))
    assert.doesNotMatch(
      JSON.stringify(list),
      /DO_NOT_EXPOSE_PLATFORM_ID|PRIVATE_OTHER_SCENE|UNRESOLVED_SUBJECT/,
    )
    const profile = await (
      await get('/api/memory-people/profile?sceneId=a&personId=person-one')
    ).json()
    assert.equal(profile.records.length, 1)
    assert.equal(profile.records[0].content, 'Scoped person-one')
    assert.equal(profile.records[0].occurredAt, undefined)
    assert.equal(profile.timeline[0].occurredAt, undefined)
    assert.equal(profile.timeline[0].recordedAt, profile.records[0].learnedAt)
    assert.doesNotMatch(
      JSON.stringify(profile),
      /PRIVATE_OTHER_SCENE|PUBLIC_NOT_PERSON|DO_NOT_EXPOSE_PLATFORM_ID/,
    )
    assert.deepEqual(
      (await (await get('/api/memory-people/profile?sceneId=a&personId=missing')).json()).records,
      [],
    )
    assert.equal((await get('/api/memory-people?sceneId=missing')).status, 404)
    for (const url of [
      '/api/memory-people',
      '/api/memory-people?sceneId=a&unexpected=yes',
      '/api/memory-people/profile?sceneId=a',
    ])
      assert.equal((await get(url)).status, 400)
    const before = store.db.prepare('SELECT total_changes() AS n').get()
    assert.equal((await get('/api/memory-people?sceneId=sim')).status, 404)
    assert.equal(
      (await get('/api/memory-people/profile?sceneId=sim&personId=person-one')).status,
      404,
    )
    assert.deepEqual(store.db.prepare('SELECT total_changes() AS n').get(), before)
  } finally {
    engine.dispose()
    store.close()
  }
})

test('person API refresh excludes deleted and recalled source-backed records without requiring learning', async () => {
  const store = new Store(':memory:')
  const engine = new Engine(store)
  const app = createApp(store, { token: 'synthetic-person-test', engine })
  store.saveScene({
    id: 'a',
    kind: 'group',
    adapterId: 'fixture',
    accountId: 'synthetic',
    platformSceneId: 'a',
    name: 'Synthetic scene',
    learningEnabled: false,
    participationEnabled: false,
    proactiveEnabled: false,
  })
  const first = engine.memory.create({
    scope: { kind: 'scene', sceneId: 'a', personId: 'person' },
    kind: 'claim',
    content: 'Source-backed statement',
    sources: [{ sceneId: 'a', messageId: 'source', senderId: 'speaker' }],
  })
  const second = engine.memory.create({
    scope: { kind: 'scene', sceneId: 'a', personId: 'person' },
    kind: 'claim',
    content: 'Administrator statement',
  })
  const get = async () =>
    (
      await app.request('/api/memory-people/profile?sceneId=a&personId=person', {
        headers: { Authorization: 'Bearer synthetic-person-test' },
      })
    ).json()
  try {
    assert.equal((await get()).records.length, 2)
    engine.memory.recall('a', 'source')
    assert.ok((await get()).records.every((record: { id: string }) => record.id !== first.id))
    engine.memory.delete(second.id)
    assert.deepEqual((await get()).records, [])
  } finally {
    engine.dispose()
    store.close()
  }
})

test('authenticated memory editing uses version/epoch guards while legacy clients remain compatible', async () => {
  const store = new Store(':memory:')
  const engine = new Engine(store)
  const app = createApp(store, { token: 'synthetic-person-test', engine })
  const headers = {
    Authorization: 'Bearer synthetic-person-test',
    'Content-Type': 'application/json',
  }
  try {
    const row = engine.memory.create({
      scope: { kind: 'scene', sceneId: 'a', personId: 'alice' },
      kind: 'claim',
      content: 'Original synthetic claim',
    })
    const all = (await (await app.request('/api/memories', { headers })).json()) as Array<{
      id: string
      revision: { version: number; deletionEpoch: number }
    }>
    const revision = all.find((value) => value.id === row.id)!.revision
    const guard = {
      expectedVersion: revision.version,
      expectedDeletionEpoch: revision.deletionEpoch,
      idempotencyKey: 'api-correction',
    }
    const patch = { content: 'Corrected synthetic claim', guard }
    const request = () =>
      app.request(`/api/memories/${row.id}`, {
        method: 'PATCH',
        headers,
        body: JSON.stringify(patch),
      })
    const first = await request()
    assert.equal(first.status, 200)
    const firstBody = await first.json()
    assert.deepEqual(await (await request()).json(), firstBody)
    const conflict = await app.request(`/api/memories/${row.id}`, {
      method: 'PATCH',
      headers,
      body: JSON.stringify({
        content: 'Stale edit',
        guard: { ...guard, idempotencyKey: 'another-command' },
      }),
    })
    assert.equal(conflict.status, 409)
    assert.equal(engine.memory.adminList()[0]!.content, 'Corrected synthetic claim')
    const legacy = await app.request(`/api/memories/${row.id}`, {
      method: 'PATCH',
      headers,
      body: JSON.stringify({ locked: true }),
    })
    assert.equal(legacy.status, 200)
  } finally {
    engine.dispose()
    store.close()
  }
})
