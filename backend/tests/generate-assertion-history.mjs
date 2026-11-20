import assert from 'node:assert/strict'
import { readFileSync, writeFileSync } from 'node:fs'
import { createApp } from '../../src/api.ts'
import { Engine } from '../../src/engine.ts'
import { Store } from '../../src/store.ts'

assert.equal(process.version, 'v24.22.1')
const store = new Store(':memory:')
const engine = new Engine(store)
const app = createApp(store, { engine, token: 'synthetic-token' })
const path = '/api/memory-assertion-history'
const scene = { adapterId: 'fixture', accountId: 'fixture', name: 'Synthetic', learningEnabled: false, participationEnabled: false, proactiveEnabled: false, id: 'scene', kind: 'group', platformSceneId: 'scene' }
const scope = { kind: 'scene', sceneId: 'scene' }
const record = {
  id: 'anchor', scope, content: 'Synthetic bounded legacy version', kind: 'claim', status: 'active', origin: 'manual',
  importance: 'medium', confidence: 0.8, locked: false, sources: [], epistemicKind: 'reported',
  learnedAt: '2024-01-01T00:00:00.000Z', validFrom: '2023-01-01T00:00:00.000Z', validUntil: '2025-01-01T00:00:00.000Z',
}
const source = { sceneId: 'scene', messageId: 'history-witness', senderId: 'synthetic-speaker' }
const sourcedInput = { scope, kind: 'fact', content: 'Synthetic old witness', sources: [source] }
const patch = { content: 'Synthetic updated version', status: 'disputed' }
const cases = []
try {
  store.saveScene(scene)
  store.db.prepare('INSERT INTO memories(id,scope_key,person_id,content,fingerprint,value) VALUES(?,?,?,?,?,?)')
    .run(record.id, 'scene:scene', null, record.content, record.id, JSON.stringify(record))
  const sourced = engine.memory.create(sourcedInput)
  const state = () => store.db.prepare('SELECT version FROM memory_assertion_state WHERE assertion_id=?').get(record.id)?.version ?? 0
  const flag = (enabled) => store.set('settings', { ...store.settings, memoryHistoricalReadEnabled: enabled })
  async function capture(name,{ url = '?sceneId=scene&memoryId=anchor', method = 'GET', authorized = true, action = '' } = {}) {
    if (action === 'enable') flag(true)
    if (action === 'update') engine.memory.update(record.id, patch)
    if (action === 'recall') engine.memory.recall(source.sceneId, source.messageId)
    if (action === 'delete') engine.memory.delete(record.id)
    const before = state()
    const supplied = '12345678-1234-4123-8123-123456789012'
    const response = await app.request('http://fixture.invalid' + path + url.replace('@sourced', sourced.id), {
      method, headers: { Authorization: 'Bearer ' + (authorized ? 'synthetic-token' : 'wrong'), 'X-Request-ID': supplied },
    })
    const id = response.headers.get('x-request-id')
    assert.match(id, /^[0-9a-f-]{36}$/)
    assert.notEqual(id, supplied)
    const raw = await response.text()
    const body = raw ? JSON.parse(raw) : null
    if (body?.requestId) { assert.equal(body.requestId, id); body.requestId = '<request-id>' }
    for (const r of body?.revisions ?? []) {
      if (r.recordedAt && r.recordedAt > record.learnedAt) r.recordedAt = '<recorded-at>'
      for (const field of ['sources','supports','actor','scope','event','deletionEpoch','assertionId','ref']) assert.equal(field in r, false)
    }
    assert.equal('anchor' in (body ?? {}), false)
    assert.equal('reasons' in (body?.coverage ?? {}), false)
    cases.push({ name, url, method, authorized, action, status: response.status, body, before, after: state(), privateHeaders: response.headers.get('cache-control') === 'no-store' })
  }
  const anchor = '?sceneId=scene&memoryId=anchor'
  await capture('feature-disabled')
  await capture('bad-auth',{ authorized:false, action:'enable' })
  await capture('missing-scene',{url:'?sceneId=missing&memoryId=anchor'})
  await capture('missing-anchor',{url:'?sceneId=scene&memoryId=missing'})
  await capture('invalid-time-no-millis',{url:anchor+'&asOfRecorded=2024-01-01T00:00:00Z'})
  await capture('invalid-time-offset',{url:anchor+'&validAt=2024-01-01T01:00:00.000%2B01:00'})
  await capture('invalid-date',{url:anchor+'&validAt=2024-02-31T00:00:00.000Z'})
  await capture('too-large-limit',{url:anchor+'&limit=21'})
  await capture('repeat-query',{url:anchor+'&limit=1&limit=2'})
  await capture('foreign-key',{url:anchor+'&anchor=x'})
  await capture('head-prepares-legacy-anchor',{method:'HEAD'})
  await capture('legacy-projection')
  await capture('current-two-versions',{action:'update'})
  await capture('limited-page',{url:anchor+'&limit=0x1'})
  await capture('recorded-cutoff',{url:anchor+'&asOfRecorded=2024-01-01T00:00:00.000Z'})
  await capture('recorded-before-first',{url:anchor+'&asOfRecorded=2023-12-31T00:00:00.000Z'})
  await capture('valid-within',{url:anchor+'&validAt=2024-01-01T00:00:00.000Z'})
  await capture('valid-end-exclusion',{url:anchor+'&validAt=2025-01-01T00:00:00.000Z'})
  await capture('before-recall',{url:'?sceneId=scene&memoryId=@sourced'})
  await capture('after-recall',{url:'?sceneId=scene&memoryId=@sourced',action:'recall'})
  await capture('deleted',{action:'delete'})
  const get = (name) => cases.find((row) => row.name === name)
  assert.equal(get('feature-disabled').after, 0)
  assert.equal(get('head-prepares-legacy-anchor').after, 1)
  assert.equal(get('legacy-projection').body.revisions[0].validityCoverage,'bounded')
  assert.equal(get('limited-page').body.coverage.hasMore, true)
  assert.deepEqual(get('recorded-cutoff').body.revisions.map((x) => x.version),[1])
  assert.deepEqual(get('recorded-before-first').body.revisions,[])
  assert.deepEqual(get('valid-end-exclusion').body.revisions,[])
  assert.equal(get('before-recall').status,200)
  assert.ok(!JSON.stringify(get('after-recall').body).includes('Synthetic old witness'))
  assert.equal(get('deleted').status,404)
  const dest = new URL('../internal/app/testdata/assertion-history-source.json', import.meta.url)
  const output = JSON.stringify({ node:process.version,scene,record,patch,source,sourcedInput,path,cases }, null, 2) + '\n'
  if (process.argv.includes('--check')) assert.equal(readFileSync(dest,'utf8'),output)
  else writeFileSync(dest,output)
  console.log(`PASS ${cases.length} actual-source assertion history cases`)
} finally {
  await engine.dispose()
  store.close()
}
