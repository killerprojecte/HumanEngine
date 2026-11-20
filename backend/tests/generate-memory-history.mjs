import assert from 'node:assert/strict'
import { readFileSync, writeFileSync } from 'node:fs'
import { createApp } from '../../src/api.ts'
import { Engine } from '../../src/engine.ts'
import { Store } from '../../src/store.ts'

assert.equal(process.version, 'v24.22.1')
const store = new Store(':memory:')
const engine = new Engine(store)
const token = 'synthetic-token'
const app = createApp(store, { engine, token })
const base = { adapterId: 'fixture', accountId: 'fixture', name: 'Synthetic', learningEnabled: false, participationEnabled: false, proactiveEnabled: false }
const scenes = [['scene','group'], ['private','private'], ['simulation','simulation']].map(([id, kind]) => ({ ...base, id, kind, platformSceneId: id }))
const scene = { kind: 'scene', sceneId: 'scene', personId: 'subject' }
const source = { sceneId: 'scene', messageId: 'old-message', senderId: 'synthetic-witness' }
const legacy = (id, scope = scene, extra = {}) => ({
  id, scope, content: 'Synthetic ' + id, kind: 'fact', status: 'active', origin: 'manual',
  importance: 'medium', confidence: 0.8, locked: false, sources: [],
  learnedAt: '2024-01-01T00:00:00.000Z', ...extra,
})
const records = [
  legacy('ordinary', scene, { kind: 'claim', epistemicKind: 'reported', occurredAt: '2023-10-03T00:00:00.000Z', validFrom: '2023-11-03T00:00:00.000Z' }),
  legacy('head-first', scene, { learnedAt: 'not-a-clock' }),
  legacy('foreign', { kind: 'scene', sceneId: 'private' }),
  legacy('simulation', { kind: 'scene', sceneId: 'simulation' }),
  legacy('shared', { kind: 'sharedPublicKnowledge' }),
  legacy('window'),
]
const sourcedInput = { scope: scene, kind: 'fact', content: 'Synthetic sourced version', sources: [source] }
const update = { content: 'Synthetic updated version', status: 'disputed' }
const path = '/api/memory-history'
const cases = []
try {
  for (const row of scenes) store.saveScene(row)
  const insert = store.db.prepare('INSERT INTO memories(id,scope_key,person_id,content,fingerprint,value) VALUES(?,?,?,?,?,?)')
  for (const r of records) {
    insert.run(r.id, r.scope.kind === 'scene' ? 'scene:' + r.scope.sceneId : 'sharedPublicKnowledge', r.scope.personId ?? null, r.content, r.id, JSON.stringify(r))
  }
  const sourced = engine.memory.create(sourcedInput)
  async function capture(name, target, { query, method = 'GET', authorized = true, action = '' } = {}) {
    const id = target === 'sourced' ? sourced.id : target
    const make = () => store.db.prepare('SELECT version FROM memory_assertion_state WHERE assertion_id=?').get(id)?.version ?? 0
    if (action === 'update') engine.memory.update(id, update)
    if (action === 'recall') engine.memory.recall(source.sceneId, source.messageId)
    if (action === 'delete') engine.memory.delete(id)
    if (action === 'fill-window')
      for (let n = 2; n <= 201; n++) engine.memory.update(id, { content: 'Window ' + n })
    const before = make()
    const supplied = '12345678-1234-4123-8123-123456789012'
    const response = await app.request('http://fixture.invalid' + path + (query ?? '?sceneId=scene&id=') + id, {
      method, headers: { Authorization: 'Bearer ' + (authorized ? token : 'wrong'), 'X-Request-ID': supplied },
    })
    const requestID = response.headers.get('x-request-id')
    assert.match(requestID, /^[0-9a-f-]{36}$/)
    assert.notEqual(requestID, supplied)
    const raw = await response.text()
    const body = raw ? JSON.parse(raw) : null
    for (const r of body?.revisions ?? []) {
      if (r.recordedAt && r.recordedAt > '2024-01-01T00:00:00.000Z') r.recordedAt = '<recorded-at>'
      for (const field of ['sources','supports','actor','scope','event','deletionEpoch','assertionId']) assert.equal(field in r, false)
    }
    if (action === 'fill-window' && body?.revisions) {
      const rows = body.revisions
      body.revisionCount = rows.length
      body.revisions = [rows[0], rows.at(-1)]
    }
    if (body?.requestId) { assert.equal(body.requestId, requestID); body.requestId = '<request-id>' }
    cases.push({ name, target, query: query ?? '?sceneId=scene&id=', method, authorized, action, status: response.status, body, before, after: make(), privateHeaders: response.headers.get('cache-control') === 'no-store' })
  }
  await capture('unauthorized', 'ordinary', { authorized: false })
  await capture('required-id', 'ordinary', { query: '?sceneId=scene&ignored=' })
  await capture('duplicate', 'ordinary', { query: '?sceneId=scene&id=other&id=' })
  await capture('strict', 'ordinary', { query: '?sceneId=scene&extra=&id=' })
  await capture('absent-scene', 'ordinary', { query: '?sceneId=missing&id=' })
  await capture('absent-memory', 'missing')
  await capture('foreign-scene', 'foreign')
  await capture('correct-private-scene', 'foreign', { query: '?sceneId=private&id=' })
  await capture('simulation', 'simulation', { query: '?sceneId=simulation&id=' })
  await capture('shared', 'shared')
  await capture('legacy-first', 'ordinary')
  await capture('legacy-repeat', 'ordinary')
  await capture('head-enrolls', 'head-first', { method: 'HEAD' })
  await capture('null-recorded-clock', 'head-first')
  await capture('ordered-revisions', 'ordinary', { action: 'update' })
  await capture('before-recall', 'sourced')
  await capture('after-recall', 'sourced', { action: 'recall' })
  await capture('after-delete', 'ordinary', { action: 'delete' })
  await capture('limited-retained-window', 'window', { action: 'fill-window' })
  const pick = (name) => cases.find((r) => r.name === name)
  assert.equal(pick('legacy-first').status, 200)
  assert.equal(pick('legacy-first').before, 0)
  assert.equal(pick('legacy-first').after, 1)
  assert.equal(pick('head-enrolls').after, 1)
  assert.equal(pick('null-recorded-clock').body.revisions[0].recordedAt, null)
  assert.deepEqual(pick('ordered-revisions').body.revisions.map((r) => r.version), [1, 2])
  assert.equal(pick('before-recall').status, 200)
  assert.ok(!JSON.stringify(pick('after-recall').body).includes('Synthetic sourced version'))
  assert.equal(pick('after-delete').status, 404)
  assert.equal(pick('limited-retained-window').body.coverage, 'limited')
  assert.equal(pick('limited-retained-window').body.revisionCount, 200)
  assert.deepEqual(pick('limited-retained-window').body.revisions.map((r) => r.version), [2, 201])
  const dest = new URL('../internal/app/testdata/memory-history-source.json', import.meta.url)
  const output = JSON.stringify({ node: process.version, scenes, records, source, sourcedInput, update, path, cases }, null, 2) + '\n'
  if (process.argv.includes('--check')) assert.equal(readFileSync(dest, 'utf8'), output)
  else writeFileSync(dest, output)
  console.log(`PASS ${cases.length} actual-source basic memory history cases`)
} finally {
  await engine.dispose()
  store.close()
}
