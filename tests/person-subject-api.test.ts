import test from 'node:test'
import assert from 'node:assert/strict'
import { Store } from '../src/store.ts'
import { Engine } from '../src/engine.ts'
import { createApp } from '../src/api.ts'

test('person HTTP projection separates witnessed subject, speaker and server/event/learning times', async () => {
  const store = new Store(':memory:')
  const engine = new Engine(store)
  const app = createApp(store, { token: 'synthetic-subject-test', engine })
  const headers = { Authorization: 'Bearer synthetic-subject-test' }
  for (const id of ['scene', 'foreign'])
    store.saveScene({
      id,
      kind: 'group',
      adapterId: 'fixture',
      accountId: 'fixture',
      platformSceneId: id,
      name: 'Synthetic',
      learningEnabled: true,
      participationEnabled: false,
      proactiveEnabled: false,
    })
  store.set('identity:scene:subject', {
    senderDisplayName: 'Mentioned person',
    senderPlatformId: 'DO_NOT_EXPOSE',
  })
  store.set('identity:scene:speaker', { senderDisplayName: 'Reporting speaker' })
  const source = { sceneId: 'scene', messageId: 'source', senderId: 'speaker' }
  const context = {
    sceneId: 'scene',
    simulation: false,
    provenance: 'source-only' as const,
    botUserId: 'bot',
    sourceEvidence: [
      {
        ...source,
        available: true,
        isBot: false,
        mentions: [{ partIndex: 2, userId: 'subject' }],
        sentAt: '2026-10-01T12:00:00Z',
        receivedAt: '2026-10-01T12:00:02Z',
      },
    ],
  }
  try {
    const saved = engine.memory.propose(context, {
      content: 'Synthetic third-party preference',
      kind: 'fact',
      epistemicKind: 'explicit',
      subject: {
        kind: 'mentioned',
        personId: 'subject',
        sourceMessageId: 'source',
        mentionPartIndex: 2,
      },
      sources: [source],
      occurredAt: '2026-09-01T10:00:00Z',
      validFrom: '2026-09-01T10:00:00Z',
      validUntil: '2028-01-01T00:00:00Z',
    })
    assert.equal(saved.status, 'saved')
    if (saved.status !== 'saved') return
    engine.memory.create({
      scope: { kind: 'scene', sceneId: 'scene', personId: 'subject' },
      kind: 'claim',
      content: 'Legacy admin record without event or receipt clock',
    })
    store.saveScene({ ...store.scene('scene')!, learningEnabled: false })
    const url = '/api/memory-people/profile?sceneId=scene&personId=subject'
    assert.equal((await app.request(url)).status, 401)
    const originalBatch = engine.memory.subjectEvidenceBatch.bind(engine.memory)
    const originalList = engine.memory.list.bind(engine.memory)
    let batchCalls = 0,
      listCalls = 0
    engine.memory.subjectEvidenceBatch = (...args) => {
      batchCalls++
      return originalBatch(...args)
    }
    engine.memory.list = (...args) => {
      listCalls++
      return originalList(...args)
    }
    const response = await app.request(url, { headers })
    engine.memory.subjectEvidenceBatch = originalBatch
    engine.memory.list = originalList
    assert.equal(batchCalls, 1)
    assert.equal(
      listCalls,
      2,
      'one profile authorization and one bounded batch authorization, not one per record',
    )
    assert.equal(response.status, 200)
    const value = await response.json()
    assert.equal(value.subjectEvidenceCoverage, 'complete')
    const record = value.records.find((row: { id: string }) => row.id === saved.memory.id)
    assert.equal(record.scope.personId, 'subject')
    assert.equal(record.sources[0].senderId, 'speaker')
    assert.equal(record.kind, 'claim')
    assert.equal(record.epistemicKind, 'reported')
    assert.equal(record.observedAt, '2026-10-01T12:00:02Z')
    assert.equal(record.occurredAt, '2026-09-01T10:00:00Z')
    assert.equal(record.validFrom, '2026-09-01T10:00:00Z')
    assert.notEqual(record.learnedAt, record.observedAt)
    const witness = record.subjectEvidence.witnesses[0]
    assert.equal(witness.kind, 'mentioned')
    assert.equal(witness.personId, 'subject')
    assert.equal(witness.subjectDisplayName, 'Mentioned person')
    assert.equal(witness.sources[0].senderDisplayName, 'Reporting speaker')
    assert.equal(witness.observations[0].sentAt, '2026-10-01T12:00:00Z')
    assert.equal(witness.observations[0].receivedAt, record.observedAt)
    assert.doesNotMatch(
      JSON.stringify(value),
      /DO_NOT_EXPOSE|sourceEvidence|botUserId|dependencyParentReferences/,
    )
    const legacy = value.records.find((row: { id: string }) => row.id !== saved.memory.id)
    assert.equal(legacy.observedAt, undefined)
    assert.equal(legacy.occurredAt, undefined)
    assert.deepEqual(legacy.subjectEvidence.witnesses, [])
    const timeline = value.timeline.find(
      (row: { memoryId: string }) => row.memoryId === saved.memory.id,
    )
    assert.equal(timeline.observedAt, record.observedAt)
    assert.equal(timeline.recordedAt, record.learnedAt)
    assert.equal(timeline.validFrom, record.validFrom)
    const foreign = await (
      await app.request('/api/memory-people/profile?sceneId=foreign&personId=subject', { headers })
    ).json()
    assert.deepEqual(foreign.records, [])
    engine.memory.recall('scene', 'source')
    const refreshed = await (await app.request(url, { headers })).json()
    assert.ok(refreshed.records.every((row: { id: string }) => row.id !== saved.memory.id))
    assert.doesNotMatch(JSON.stringify(refreshed), /Reporting speaker|2026-10-01T12:00:02Z/)
    const before = store.db.prepare('SELECT total_changes() AS n').get()
    assert.equal(
      (
        await app.request('/api/memory-people/profile?sceneId=simulation&personId=subject', {
          headers,
        })
      ).status,
      404,
    )
    assert.deepEqual(store.db.prepare('SELECT total_changes() AS n').get(), before)
  } finally {
    engine.dispose()
    store.close()
  }
})

test('person evidence batch reports truncation instead of claiming remaining records have no evidence', async () => {
  const store = new Store(':memory:')
  const engine = new Engine(store)
  const app = createApp(store, { token: 'synthetic-subject-test', engine })
  store.saveScene({
    id: 'scene',
    kind: 'group',
    adapterId: 'fixture',
    accountId: 'fixture',
    platformSceneId: 'scene',
    name: 'Synthetic',
    learningEnabled: false,
    participationEnabled: false,
    proactiveEnabled: false,
  })
  try {
    for (let index = 0; index < 201; index++)
      engine.memory.create({
        scope: { kind: 'scene', sceneId: 'scene', personId: 'subject' },
        kind: 'claim',
        content: `Synthetic record ${index}`,
      })
    const response = await app.request(
      '/api/memory-people/profile?sceneId=scene&personId=subject',
      { headers: { Authorization: 'Bearer synthetic-subject-test' } },
    )
    assert.equal(response.status, 200)
    const value = await response.json()
    assert.equal(value.records.length, 201)
    assert.equal(value.subjectEvidenceCoverage, 'limited')
    assert.equal(
      value.records.filter((record: { subjectEvidence?: unknown }) => record.subjectEvidence)
        .length,
      200,
    )
  } finally {
    engine.dispose()
    store.close()
  }
})
