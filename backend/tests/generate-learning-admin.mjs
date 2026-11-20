import assert from 'node:assert/strict'
import { mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { createApp } from '../../src/api.ts'
import { Engine } from '../../src/engine.ts'
import { Store } from '../../src/store.ts'

const dir = mkdtempSync(join(tmpdir(), 'he-learning-admin-'))
const originalCwd = process.cwd()
const cases = []
let store, engine
try {
  process.chdir(dir)
  store = new Store(join(dir, 'live/main.sqlite'))
  engine = new Engine(store)
  const app = createApp(store, { engine, token: 'synthetic-token' })
  const record = async (name, method, path, { authorized = true, body } = {}) => {
    const headers = { 'X-Request-ID': '12345678-1234-4123-8123-123456789012' }
    if (authorized) headers.Authorization = 'Bearer synthetic-token'
    if (body !== undefined) headers['Content-Type'] = 'application/json'
    const response = await app.request('http://synthetic.test' + path, { method, body, headers })
    const requestId = response.headers.get('x-request-id')
    assert.match(requestId, /^[0-9a-f-]{36}$/i)
    assert.notEqual(requestId, headers['X-Request-ID'])
    const text = await response.text()
    cases.push({
      name, method, path, authorized, body, status: response.status,
      response: text === '' ? null : response.headers.get('content-type')?.includes('json') ? JSON.parse(text) : text,
      headers: Object.fromEntries(['cache-control', 'content-type', 'x-content-type-options'].map(
        (name) => [name, response.headers.get(name) ?? ''],
      )),
    })
  }
  const prefix = '/api/learning'
  await record('initial-status', 'GET', prefix + '/status')
  await record('status-head', 'HEAD', prefix + '/status')
  await record('unauthorized-status', 'GET', prefix + '/status', { authorized: false })
  await record('unauthorized-head', 'HEAD', prefix + '/status', { authorized: false })
  await record('missing-retry', 'POST', prefix + '/failures/missing%2Fid/retry', { body: '{' })
  await record('missing-discard', 'DELETE', prefix + '/failures/missing%2Fid')
  await record('unknown-method', 'GET', prefix + '/failures/missing%2Fid')
  await record('unknown-head', 'HEAD', prefix + '/failures/missing%2Fid')

  // Seed a synthetic paused result at its real owner's in-memory boundary.
  // The router, status projection, and retry/discard effects are all actual source.
  const scene = {
    id: 'learning-scene', adapterId: 'synthetic', accountId: 'a', platformSceneId: 's',
    kind: 'group', name: 'Synthetic', learningEnabled: true, participationEnabled: false,
    proactiveEnabled: false,
  }
  const setup = await app.request('http://synthetic.test/api/scenes/' + scene.id, {
    method: 'PUT', headers: { Authorization: 'Bearer synthetic-token', 'Content-Type': 'application/json' },
    body: JSON.stringify(scene),
  })
  assert.equal(setup.status, 200)
  const failure = {
    id: 'synthetic/failure', sceneId: scene.id, attempt: 1, messageCount: 1,
    partial: false, canRetry: true, createdAt: '2026-01-01T00:00:00.000Z',
    messages: [{
      id: 'message', sceneId: scene.id, senderId: 'synthetic-user', origin: 'live',
      status: 'available', parts: [{ type: 'text', text: 'synthetic body must not appear' }],
    }],
  }
  engine.learning.learningFailures.set(failure.id, failure)
  await record('paused-status', 'GET', prefix + '/status')
  await record('unauthorized-retry', 'POST', prefix + '/failures/synthetic%2Ffailure/retry', { authorized: false })
  await record('unauthorized-discard', 'DELETE', prefix + '/failures/synthetic%2Ffailure', { authorized: false })
  assert.equal(engine.learning.learningFailures.has(failure.id), true)
  await record('retry', 'POST', prefix + '/failures/synthetic%2Ffailure/retry', { body: '{' })
  assert.equal(engine.learning.learningFailures.has(failure.id), false)
  assert.equal(engine.learning.learningBatches.get(scene.id).length, 1)
  engine.learning.cancelLearning(scene.id)

  engine.learning.learningFailures.set(failure.id, { ...failure, partial: true, canRetry: false, messages: [] })
  await record('partial-retry', 'POST', prefix + '/failures/synthetic%2Ffailure/retry')
  assert.equal(engine.learning.learningFailures.has(failure.id), true)
  await record('discard', 'DELETE', prefix + '/failures/synthetic%2Ffailure')
  assert.equal(engine.learning.learningFailures.has(failure.id), false)
} finally {
  await engine?.dispose()
  store?.close()
  process.chdir(originalCwd)
  rmSync(dir, { recursive: true, force: true })
}
const dest = new URL('../internal/app/testdata/learning-admin-source.json', import.meta.url)
const out = JSON.stringify(cases, null, 2) + '\n'
if (process.argv.includes('--check')) assert.equal(readFileSync(dest, 'utf8'), out)
else writeFileSync(dest, out)
console.log(`PASS ${cases.length} actual-source learning administration cases`)
