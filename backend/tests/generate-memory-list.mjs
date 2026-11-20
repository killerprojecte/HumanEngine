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
const scoped = (personId) => ({ kind: 'scene', sceneId: 'scene', personId })
records.push(
  { key: 'dotless', value: { id: 'dotless', scope: scoped('dotless'), status: 'disputed', kind: 'claim', content: 'I', learnedAt: '2026-01-01T00:00:00.000Z', validFrom: '2090-01-01T00:00:00Z' } },
  { key: 'dotted', value: { id: 'dotted', scope: scoped('dotted'), status: 'active', kind: 'experience', content: 'İ', learnedAt: '2026-01-01T00:00:00.000Z' } },
  { key: 'numeric-10', value: { id: 'numeric-10', scope: { kind: 'scene', sceneId: 'numeric' }, status: 'active', kind: 'fact', content: 'Ten', learnedAt: '10' } },
  { key: 'numeric-2', value: { id: 'numeric-2', scope: { kind: 'scene', sceneId: 'numeric' }, status: 'active', kind: 'fact', content: 'Two', learnedAt: '2' } },
)
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
  const record = async (name, { method = 'GET', authorized = true, path = '/api/memories' } = {}) => {
    const headerID = '12345678-1234-4123-8123-123456789012'
    const headers = { 'X-Request-ID': headerID }
    headers.Authorization = authorized ? 'Bearer synthetic-token' : 'Bearer synthetic-invalid-bearer'
    const response = await app.request('http://synthetic.test' + path, { method, headers })
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
      name, method, path, authorized, status: response.status, body,
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
  const page = async (name, search, opts) => record(name, { ...opts, path: '/api/memories/page' + search })
  await page('stable-equal-dates', '?scene=scene:scene&limit=2')
  await page('page-two', '?scene=scene:scene&limit=2&page=0x2')
  await page('clamped-page', '?scene=scene:scene&limit=1&page=9007199254740991')
  await page('clamped-empty', '?q=absent&page=999')
  await page('all-inactive-facets', '?scene=scene:scene&status=invalidated')
  await page('public-person-empty', '?scene=public&person=dotted')
  await page('person-kind-status', '?scene=scene:scene&person=%EF%BB%BFdotless&kind=claim&status=disputed')
  await page('turkish', '?locale=tr&scene=scene:scene&q=%C4%B1')
  await page('azeri', '?locale=az-Latn-AZ&scene=scene:scene&q=%C4%B1')
  await page('locale-alias', '?locale=iw&scene=scene:scene')
  await page('case-fold-default', '?locale=EN-us&scene=scene:scene&q=i')
  await page('default-digit-sort', '?scene=scene:numeric')
  await page('locale-extension-numeric', '?scene=scene:numeric&locale=en-u-kn')
  await page('locale-extension-no-search', '?scene=scene:numeric&locale=en-u-co-search')
  await page('unsupported-locale', '?locale=zz')
  await page('invalid-locale', '?locale=not_a_locale')
  await page('invalid-extlang', '?locale=zh-yue')
  await page('invalid-duplicate-extension', '?locale=en-u-kn-u-co-emoji')
  await page('invalid-limit', '?limit=101')
  await page('invalid-scene', '?scene=scene:')
  await page('duplicate-query', '?limit=2&limit=101')
  await page('page-head', '?locale=en-US', { method: 'HEAD' })
  await page('page-unauthorized', '?locale=not_a_locale', { authorized: false })
  await record('head', { method: 'HEAD' })
  await record('unauthorized', { authorized: false })
  await record('unauthorized-head', { method: 'HEAD', authorized: false })
  write.run('malformed', 'scene:scene', null, 'invalid JSON', 'malformed', '{')
  await record('malformed-row')
  await record('malformed-head', { method: 'HEAD' })
  await page('off-page-corrupted-row', '?scene=scene:numeric&limit=1')
  await page('schema-invalid-before-rows', '?limit=101')
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
