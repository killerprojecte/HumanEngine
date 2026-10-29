import test from 'node:test'
import assert from 'node:assert/strict'
import { Store } from '../src/store.ts'
import { Engine } from '../src/engine.ts'
import { createApp } from '../src/api.ts'

test('relationship administration preserves qualified sources and exact revision review behind authentication', async () => {
  const store = new Store(':memory:')
  const engine = new Engine(store)
  const app = createApp(store, { token: 'synthetic-relations', engine })
  const headers = {
    Authorization: 'Bearer synthetic-relations',
    'Content-Type': 'application/json',
  }
  for (const sceneId of ['scene', 'foreign'])
    store.saveScene({
      id: sceneId,
      kind: 'group',
      adapterId: 'fixture',
      accountId: 'fixture',
      platformSceneId: sceneId,
      name: 'Synthetic',
      learningEnabled: true,
      participationEnabled: false,
      proactiveEnabled: false,
    })
  store.set('settings', { ...store.settings, memoryGraphEnabled: true })
  store.set('identity:scene:person', {
    senderDisplayName: 'Synthetic person',
    senderPlatformId: 'DO_NOT_EXPOSE',
  })
  const source = { sceneId: 'scene', messageId: 'source', senderId: 'person' }
  try {
    const result = engine.memory.propose(
      {
        sceneId: 'scene',
        simulation: false,
        provenance: 'source-only',
        sourceEvidence: [{ ...source, available: true, isBot: false }],
      },
      {
        kind: 'claim',
        content: 'I will not participate unless the date changes.',
        epistemicKind: 'explicit',
        subject: { kind: 'self', sourceMessageId: 'source' },
        sources: [source],
        relations: [
          {
            predicate: 'participates_in',
            modality: 'asserted',
            temporalScope: 'current',
            personId: 'person',
            target: { kind: 'new', entityKind: 'plan', title: 'Synthetic plan' },
            polarity: 'negative',
            conditions: ['Only if the date changes; otherwise not.'],
          },
        ],
      },
    )
    assert.equal(result.status, 'saved')
    const url = '/api/memory-relations?sceneId=scene'
    assert.equal((await app.request(url)).status, 401)
    const get = async (path = url) => (await app.request(path, { headers })).json()
    const body = await get()
    assert.equal(body.items.length, 1)
    const row = body.items[0]
    assert.equal(row.status, 'candidate')
    assert.equal(row.polarity, 'negative')
    assert.equal(row.modality, 'asserted')
    assert.equal(row.temporalScope, 'current')
    assert.deepEqual(row.conditions, ['Only if the date changes; otherwise not.'])
    assert.equal(row.personName, 'Synthetic person')
    assert.equal(row.assertionContent, 'I will not participate unless the date changes.')
    assert.equal(row.sourceGroups[0].witnessKind, 'self')
    assert.equal(row.sourceGroups[0].sources[0].messageId, 'source')
    assert.doesNotMatch(JSON.stringify(body), /DO_NOT_EXPOSE|deletionEpoch|proofId|actorId/)
    const review = (version: number, status = 'accepted', sceneId = 'scene', extra = {}) =>
      app.request(`/api/memory-relations/${row.id}/review`, {
        method: 'POST',
        headers,
        body: JSON.stringify({ sceneId, expectedVersion: version, status, ...extra }),
      })
    assert.equal(
      (
        await app.request(`/api/memory-relations/${row.id}/review`, {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({
            sceneId: 'scene',
            expectedVersion: row.version,
            status: 'accepted',
          }),
        })
      ).status,
      401,
    )
    assert.equal((await review(row.version, 'accepted', 'foreign')).status, 404)
    assert.equal((await review(row.version, 'accepted', 'scene', { actorId: 'spoof' })).status, 400)
    const accepted = await review(row.version)
    assert.equal(accepted.status, 200)
    const updated = await accepted.json()
    assert.equal(updated.status, 'accepted')
    assert.equal(updated.admission, 'reviewed')
    assert.deepEqual(updated.conditions, row.conditions)
    assert.equal((await review(row.version, 'rejected')).status, 409)
    assert.equal((await review(updated.version, 'rejected')).status, 200)
    assert.equal((await get()).items[0].status, 'rejected')
    assert.deepEqual((await get('/api/memory-relations?sceneId=foreign')).items, [])
    assert.equal((await app.request(url + '&limit=41', { headers })).status, 400)
    assert.equal((await app.request(url + '&unexpected=1', { headers })).status, 400)
    store.set('settings', { ...store.settings, memoryGraphEnabled: false })
    assert.deepEqual((await get()).items, [])
    assert.equal((await get()).enabled, false)
    assert.equal((await review(updated.version + 1)).status, 404)
    store.set('settings', { ...store.settings, memoryGraphEnabled: true })
    engine.memory.recall('scene', 'source')
    assert.deepEqual((await get()).items, [])
    assert.equal((await review(updated.version + 1)).status, 404)
    const before = store.db.prepare('SELECT total_changes() AS n').get()
    assert.equal(
      (await app.request('/api/memory-relations?sceneId=simulation', { headers })).status,
      404,
    )
    assert.deepEqual(store.db.prepare('SELECT total_changes() AS n').get(), before)
  } finally {
    engine.dispose()
    store.close()
  }
})

test('graph flags default off and automatic admission remains distinct from manual review', async () => {
  const store = new Store(':memory:')
  const engine = new Engine(store)
  const app = createApp(store, { token: 'synthetic-relations', engine })
  const headers = { Authorization: 'Bearer synthetic-relations' }
  store.saveScene({
    id: 'scene',
    kind: 'group',
    adapterId: 'fixture',
    accountId: 'fixture',
    platformSceneId: 'scene',
    name: 'Synthetic',
    learningEnabled: true,
    participationEnabled: false,
    proactiveEnabled: false,
  })
  try {
    for (const key of [
      'memoryGraphEnabled',
      'memoryGraphAutoAdmissionEnabled',
      'memoryGraphAnswerEnabled',
      'memoryGraphShadowEnabled',
    ] as const)
      assert.equal(store.settings[key], false)
    store.set('settings', {
      ...store.settings,
      memoryGraphEnabled: true,
      memoryGraphAutoAdmissionEnabled: true,
    })
    for (const personId of ['one', 'two']) {
      store.set(`identity:scene:${personId}`, { senderDisplayName: 'Same nickname' })
      const source = { sceneId: 'scene', messageId: `source-${personId}`, senderId: personId }
      const result = engine.memory.propose(
        {
          sceneId: 'scene',
          simulation: false,
          provenance: 'source-only',
          sourceEvidence: [{ ...source, available: true, isBot: false }],
        },
        {
          content: `I participate in synthetic event ${personId}.`,
          kind: 'fact',
          epistemicKind: 'explicit',
          subject: { kind: 'self', sourceMessageId: source.messageId },
          sources: [source],
          relations: [
            {
              predicate: 'participates_in',
              modality: 'asserted',
              temporalScope: 'current',
              personId,
              target: { kind: 'new', entityKind: 'event', title: 'Same title' },
              polarity: 'positive',
              conditions: [],
            },
          ],
        },
      )
      assert.equal(result.status, 'saved')
    }
    const get = async (query: string) =>
      (await app.request(`/api/memory-relations?sceneId=scene&${query}`, { headers })).json()
    const all = await get('status=accepted')
    assert.equal(all.items.length, 2)
    assert.ok(
      all.items.every(
        (row: { admission: string; autoReason: string }) =>
          row.admission === 'automatic' && row.autoReason === 'self_explicit_no_visible_conflict',
      ),
    )
    assert.notEqual(all.items[0].target.id, all.items[1].target.id)
    assert.notEqual(all.items[0].personId, all.items[1].personId)
    assert.equal((await get('personId=one')).items.length, 1)
    const limited = await get('limit=1')
    assert.equal(limited.items.length, 1)
    assert.equal(limited.coverage, 'limited')
    assert.deepEqual((await get('status=candidate')).items, [])
  } finally {
    engine.dispose()
    store.close()
  }
})

test('past and possible participation remains qualified and cannot automatically become current certainty', async () => {
  const store = new Store(':memory:')
  const engine = new Engine(store)
  const app = createApp(store, { token: 'synthetic-relations', engine })
  const headers = { Authorization: 'Bearer synthetic-relations' }
  store.saveScene({
    id: 'scene',
    kind: 'group',
    adapterId: 'fixture',
    accountId: 'fixture',
    platformSceneId: 'scene',
    name: 'Synthetic',
    learningEnabled: true,
    participationEnabled: false,
    proactiveEnabled: false,
  })
  store.set('settings', {
    ...store.settings,
    memoryGraphEnabled: true,
    memoryGraphAutoAdmissionEnabled: true,
  })
  try {
    for (const [modality, temporalScope, content] of [
      ['asserted', 'past', 'I used to participate; the date is unknown.'],
      ['possible', 'current', 'I may participate, but it is not confirmed.'],
    ] as const) {
      const source = { sceneId: 'scene', messageId: temporalScope, senderId: 'person' }
      const result = engine.memory.propose(
        {
          sceneId: 'scene',
          simulation: false,
          provenance: 'source-only',
          sourceEvidence: [{ ...source, available: true, isBot: false }],
        },
        {
          content,
          kind: 'claim',
          epistemicKind: 'explicit',
          subject: { kind: 'self', sourceMessageId: source.messageId },
          sources: [source],
          relations: [
            {
              predicate: 'participates_in',
              personId: 'person',
              target: { kind: 'new', entityKind: 'event', title: 'Synthetic event' },
              polarity: 'positive',
              modality,
              temporalScope,
              conditions: [],
            },
          ],
        },
      )
      assert.equal(result.status, 'saved')
    }
    const response = await app.request('/api/memory-relations?sceneId=scene', { headers })
    assert.equal(response.status, 200)
    const body = await response.json()
    assert.equal(body.items.length, 2)
    assert.ok(
      body.items.every(
        (row: { status: string; admission: string }) =>
          row.status === 'candidate' && row.admission === 'candidate',
      ),
    )
    const past = body.items.find((row: { temporalScope: string }) => row.temporalScope === 'past')
    const possible = body.items.find((row: { modality: string }) => row.modality === 'possible')
    assert.equal(past.modality, 'asserted')
    assert.equal(past.occurredAt, undefined)
    assert.equal(past.validFrom, undefined)
    assert.equal(possible.temporalScope, 'current')
    assert.equal(possible.occurredAt, undefined)
  } finally {
    engine.dispose()
    store.close()
  }
})
