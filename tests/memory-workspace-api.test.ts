import test from 'node:test'
import assert from 'node:assert/strict'
import { Store } from '../src/store.ts'
import { Engine } from '../src/engine.ts'
import { createApp } from '../src/api.ts'
test('workspace HTTP uses exact pin policy CAS, permits stale/OFF unpin, and preserves independent qualified episodes', async () => {
  const store = new Store(':memory:'),
    engine = new Engine(store),
    app = createApp(store, { token: 'synthetic-workspace', engine })
  const headers = {
    Authorization: 'Bearer synthetic-workspace',
    'Content-Type': 'application/json',
  }
  for (const id of ['scene', 'foreign'])
    store.saveScene({
      id,
      kind: 'group',
      adapterId: 'fixture',
      accountId: 'fixture',
      platformSceneId: id,
      name: 'Synthetic',
      learningEnabled: false,
      participationEnabled: false,
      proactiveEnabled: false,
    })
  const get = async (path: string) => app.request(path, { headers })
  const post = async (path: string, body: unknown) =>
    app.request(path, { method: 'POST', headers, body: JSON.stringify(body) })
  try {
    assert.equal((await app.request('/api/memory-working-set?sceneId=scene')).status, 401)
    assert.equal((await get('/api/memory-working-set?sceneId=simulation')).status, 404)
    assert.equal((await get('/api/memory-working-set?sceneId=scene&includeCold=guess')).status, 400)
    const record = engine.memory.create({
      scope: { kind: 'scene', sceneId: 'scene', personId: 'person' },
      kind: 'experience',
      content: 'Reported synthetic experience, not externally verified',
      epistemicKind: 'explicit',
    })
    assert.equal((await (await get('/api/memory-working-set?sceneId=scene')).json()).enabled, false)
    store.set('settings', {
      ...store.settings,
      memoryWorkingSetEnabled: true,
      memoryEpisodesEnabled: true,
    })
    const page = await (
      await get('/api/memory-working-set?sceneId=scene&personId=person&includeCold=true')
    ).json()
    assert.equal(page.items.length, 1)
    const item = page.items[0]
    assert.equal(item.record.id, record.id)
    assert.equal(item.accessibility.contextSelectedCount, 0)
    assert.equal(item.accessibility.pinRevision, 0)
    const selection = {
      ref: item.accessibility.ref,
      dependencyKey: item.accessibility.dependencyKey,
    }
    assert.equal(
      (
        await post('/api/memory-working-set/pin', {
          sceneId: 'foreign',
          selection,
          expectedRevision: 0,
          pinned: true,
        })
      ).status,
      409,
    )
    assert.equal(
      (
        await app.request('/api/memory-working-set/pin', {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({ sceneId: 'scene', selection, expectedRevision: 0, pinned: true }),
        })
      ).status,
      401,
    )
    const pinned = await post('/api/memory-working-set/pin', {
      sceneId: 'scene',
      selection,
      expectedRevision: 0,
      pinned: true,
    })
    assert.equal(pinned.status, 200)
    const policy = await pinned.json()
    assert.equal(policy.policyRevision, 1)
    assert.equal(
      (
        await post('/api/memory-working-set/pin', {
          sceneId: 'scene',
          selection,
          expectedRevision: 0,
          pinned: false,
        })
      ).status,
      409,
    )
    assert.equal(
      (
        await post('/api/memory-working-set/pin', {
          sceneId: 'scene',
          selection,
          expectedRevision: 1,
          pinned: false,
          passed: true,
        })
      ).status,
      400,
    )
    const ep = await (await get('/api/memory-episodes?sceneId=scene&personId=person')).json()
    assert.equal(ep.items.length, 1)
    assert.equal(ep.items[0].kind, 'reported_experience')
    assert.equal(ep.items[0].time.occurredAt, null)
    assert.equal(ep.items[0].record.epistemicKind, 'explicit')
    assert.equal(ep.sourceBodyCoverage, 'unavailable')
    assert.equal(ep.interpretation, 'independent_qualified_records_not_synthesized_episodes')
    assert.deepEqual((await (await get('/api/memory-episodes?sceneId=foreign')).json()).items, [])
    engine.memory.update(record.id, { content: 'Corrected independently qualified experience' })
    assert.equal(
      (
        await post('/api/memory-working-set/pin', {
          sceneId: 'scene',
          selection,
          expectedRevision: 1,
          pinned: true,
        })
      ).status,
      409,
    )
    store.set('settings', {
      ...store.settings,
      memoryWorkingSetEnabled: false,
      memoryEpisodesEnabled: false,
    })
    const offPolicies = await (await get('/api/memory-working-set?sceneId=scene')).json()
    assert.equal(offPolicies.policies.items.length, 1)
    assert.equal(offPolicies.policies.items[0].pinned, true)
    assert.equal(offPolicies.policies.items[0].policyRevision, 1)
    assert.doesNotMatch(JSON.stringify(offPolicies), /Corrected independently|Reported synthetic/)
    assert.deepEqual(
      (await (await get('/api/memory-working-set?sceneId=foreign')).json()).policies.items,
      [],
    )
    assert.equal(
      (
        await post('/api/memory-working-set/pin', {
          sceneId: 'scene',
          selection,
          expectedRevision: 1,
          pinned: false,
        })
      ).status,
      200,
    )
    assert.equal(
      (await (await get('/api/memory-working-set?sceneId=scene')).json()).items.length,
      0,
    )
    const before = store.db.prepare('SELECT total_changes() AS n').get()
    assert.equal(
      (await post('/api/memory-working-set/rebuild', { sceneId: 'simulation' })).status,
      404,
    )
    assert.deepEqual(store.db.prepare('SELECT total_changes() AS n').get(), before)
    store.set('settings', {
      ...store.settings,
      memoryWorkingSetEnabled: true,
      memoryEpisodesEnabled: true,
    })
    const refreshed = await post('/api/memory-working-set/rebuild', {
      sceneId: 'scene',
      personId: 'person',
    })
    assert.equal(refreshed.status, 200)
    const refreshedBody = await refreshed.json()
    assert.equal(
      refreshedBody.items[0].record.content,
      'Corrected independently qualified experience',
    )
    assert.equal(refreshedBody.items[0].accessibility.contextSelectedCount, 0)
    engine.memory.delete(record.id)
    assert.deepEqual((await (await get('/api/memory-working-set?sceneId=scene')).json()).items, [])
    assert.deepEqual((await (await get('/api/memory-episodes?sceneId=scene')).json()).items, [])
  } finally {
    engine.dispose()
    store.close()
  }
})
