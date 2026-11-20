import assert from 'node:assert/strict'
import { readFileSync, writeFileSync } from 'node:fs'
import { createApp } from '../../src/api.ts'
import { Engine } from '../../src/engine.ts'
import { Store } from '../../src/store.ts'

assert.equal(process.version, 'v24.22.1')
const store = new Store(':memory:')
const engine = new Engine(store)
const app = createApp(store, { engine, token: 'synthetic-config-token' })
const rule = { id: 'rule', scope: 'scene', sceneId: 'scene', content: 'Synthetic rule', nonOverridable: false, enabled: true }
const cases = []
try {
  async function capture(name, { seeded, method = 'GET', path = '/api/rules', key = 'rules', authorized = true, body, type = 'application/json' } = {}) {
    store.db.prepare('DELETE FROM config WHERE key=?').run(key)
    if (seeded !== undefined) store.db.prepare('INSERT INTO config VALUES(?,?)').run(key, seeded)
    const headers = { Authorization: 'Bearer ' + (authorized ? 'synthetic-config-token' : 'wrong'), 'Content-Type': type, 'X-Request-ID': '12345678-1234-4123-8123-123456789012' }
    const response = await app.request(path, { method, headers, ...(body === undefined ? {} : { body }) })
    const id = response.headers.get('x-request-id')
    assert.match(id, /^[0-9a-f-]{36}$/)
    assert.notEqual(id, headers['X-Request-ID'])
    const text = (await response.text()).replaceAll(id, '<request-id>')
    const saved = store.db.prepare("SELECT value FROM config WHERE key=?").get(key)?.value ?? null
    cases.push({ name, path, key, method, seeded: seeded ?? null, saved, headers, body: body ?? '', status: response.status, text, responseHeaders: Object.fromEntries(['content-type','cache-control','x-content-type-options'].map(k => [k, response.headers.get(k) ?? ''])) })
  }
  await capture('missing')
  await capture('present', { seeded: JSON.stringify([rule]) })
  await capture('head', { seeded: JSON.stringify([rule]), method: 'HEAD' })
  await capture('auth', { authorized: false })
  await capture('null-stays-null', { seeded: 'null' })
  await capture('invalid-storage', { seeded: '{' })
  await capture('unsupported-method', { method: 'POST' })
  const put = { method: 'PUT' }
  await capture('replace', { ...put, seeded: JSON.stringify([rule]), body: JSON.stringify([{ enabled: false, content: 'New synthetic rule', scope: 'global', nonOverridable: true, id: '' }]) })
  await capture('clear', { ...put, seeded: JSON.stringify([rule]), body: '[]' })
  await capture('duplicate', { ...put, body: JSON.stringify([rule, rule]) })
  await capture('invalid-before-duplicates', { ...put, body: JSON.stringify([rule, rule, { ...rule, ignored: true }]) })
  await capture('missing-scene', { ...put, body: JSON.stringify([{ ...rule, sceneId: '' }]) })
  await capture('global-scene-text', { ...put, body: JSON.stringify([{ ...rule, scope: 'global', sceneId: '' }]) })
  await capture('malformed-input', { ...put, body: '{' })
  await capture('wrong-content-type', { ...put, body: '[]', type: 'text/plain' })
  await capture('bad-storage-write', { ...put, seeded: '{', body: '[]' })
  await capture('no-auth-write', { ...put, authorized: false, body: '[]' })
  // The source's length checks count Unicode code points, not UTF-16 units.
  for (const n of [5000, 5001]) {
    const response = await app.request('/api/rules', {
      method: 'PUT',
      headers: { Authorization: 'Bearer synthetic-config-token', 'Content-Type': 'application/json' },
      body: JSON.stringify([{ ...rule, content: '😀'.repeat(n) }]),
    })
    assert.equal(response.status, n === 5000 ? 200 : 400)
  }
  const persona = { path: '/api/persona/versions', key: 'personaVersions' }
  const versions = JSON.stringify([
    { id: 'new', createdAt: '2026-01-02T00:00:00.000Z', content: 'Current synthetic persona' },
    { id: 'old', createdAt: '2026-01-01T00:00:00.000Z', content: 'Earlier synthetic persona' },
  ])
  await capture('persona-missing', persona)
  await capture('persona-order', { ...persona, seeded: versions })
  await capture('persona-head', { ...persona, seeded: versions, method: 'HEAD' })
  await capture('persona-auth', { ...persona, authorized: false })
  await capture('persona-invalid-storage', { ...persona, seeded: '{' })
  await capture('persona-wrong-method', { ...persona, seeded: versions, method: 'PUT', body: '[]' })
  const fixture = JSON.stringify({ cases }, null, 2) + '\n'
  const dest = new URL('../internal/httpapi/testdata/rules-http-source.json', import.meta.url)
  if (process.argv.includes('--check')) assert.equal(readFileSync(dest, 'utf8'), fixture)
  else writeFileSync(dest, fixture)
  console.log('PASS ' + cases.length + ' rules oracle cases')
} finally {
  await engine.dispose()
  store.close()
}
