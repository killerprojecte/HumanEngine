import assert from 'node:assert/strict'
import { mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { createApp } from '../../src/api.ts'
import { Engine } from '../../src/engine.ts'
import { Store } from '../../src/store.ts'

const scope = { kind: 'scene', sceneId: 'scene', personId: 'person' }
const records = [
  { key: 'legacy', value: { id: 'legacy', scope, status: 'active', kind: 'fact', content: 'Legacy', learnedAt: '2026-01-01T00:00:00.000Z' } },
  { key: 'invalidated', value: { id: 'raw-id', scope, status: 'invalidated', kind: 'fact', content: 'Revoked', learnedAt: '2025-01-01T00:00:00.000Z', revision: { version: 1 } } },
  { key: 'internal', value: { id: 'internal', scope, status: 'active', kind: 'summary', internalUse: 'synthetic-private', content: 'must not appear' } },
  { key: 'public', value: { id: 'public', scope: { kind: 'sharedPublicKnowledge' }, status: 'superseded', kind: 'term', content: 'Public', learnedAt: '2026-01-01T00:00:00.000Z' } },
]
const states = [{ id: 'raw-id', version: 7, deletionEpoch: 2, deleted: 1 }]
const dir = mkdtempSync(join(tmpdir(), 'he-memory-list-'))
const originalCwd = process.cwd()
const cases = []
let store, engine
try {
  process.chdir(dir)
  store = new Store(join(dir, 'live/main.sqlite'))
  engine = new Engine(store)
  const app = createApp(store, { engine, token: 'synthetic-token' })
  const record = async (name, { method = 'GET', authorized = true } = {}) => {
    const headerID = '12345678-1234-4123-8123-123456789012'
    const headers = { 'X-Request-ID': headerID }
    headers.Authorization = authorized ? 'Bearer synthetic-token' : 'Bearer synthetic-invalid-bearer'
    const response = await app.request('http://synthetic.test/api/memories', { method, headers })
    const bodyText = await response.text()
    const body = bodyText ? JSON.parse(bodyText) : null
    const requestId = response.headers.get('x-request-id')
    assert.match(requestId, /^[0-9a-f-]{36}$/i)
    assert.notEqual(requestId, headerID)
    if (body?.requestId) {
      assert.equal(body.requestId, requestId)
      body.requestId = '<request-id>'
    }
    cases.push({
      name, method, authorized, status: response.status, body,
      headers: Object.fromEntries(['cache-control', 'content-type', 'x-content-type-options'].map(
        (name) => [name, response.headers.get(name) ?? ''],
      )),
    })
  }
  await record('empty')
  const write = store.db.prepare('INSERT INTO memories(id,scope_key,person_id,content,fingerprint,value) VALUES (?, ?, ?, ?, ?, ?)')
  for (const { key, value } of records)
    write.run(key, 'scene:scene', null, value.content, key, JSON.stringify(value))
  const writeState = store.db.prepare('INSERT INTO memory_assertion_state(assertion_id,version,deletion_epoch,deleted) VALUES (?, ?, ?, ?)')
  for (const { id, version, deletionEpoch, deleted } of states)
    writeState.run(id, version, deletionEpoch, deleted)
  await record('all-statuses-storage-order')
  await record('head', { method: 'HEAD' })
  await record('unauthorized', { authorized: false })
  await record('unauthorized-head', { method: 'HEAD', authorized: false })
  write.run('malformed', 'scene:scene', null, 'invalid JSON', 'malformed', '{')
  await record('malformed-row')
  await record('malformed-head', { method: 'HEAD' })
} finally {
  await engine?.dispose()
  store?.close()
  process.chdir(originalCwd)
  rmSync(dir, { recursive: true, force: true })
}
const dest = new URL('../internal/app/testdata/memory-list-source.json', import.meta.url)
const out = JSON.stringify({ records, states, cases }, null, 2) + '\n'
if (process.argv.includes('--check')) assert.equal(readFileSync(dest, 'utf8'), out)
else writeFileSync(dest, out)
console.log(`PASS ${cases.length} actual-source memory list cases`)
