import assert from 'node:assert/strict'
import { mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { createApp } from '../../src/api.ts'
import { Engine } from '../../src/engine.ts'
import { Store } from '../../src/store.ts'
import { metadata } from '../../src/memory/recovery/ledger.ts'

const dir = mkdtempSync(join(tmpdir(), 'he-identity-write-oracle-'))
const cwd = process.cwd()
let store, engine
assert.equal(process.version, 'v24.22.1')
try {
  process.chdir(dir)
  store = new Store(join(dir, 'live', 'main.sqlite'))
  engine = new Engine(store)
  const path = '/api/memory-identities'
  const app = createApp(store, { engine, token: 'synthetic-token' })
  const base = { adapterId: 'fixture', accountId: 'local', name: 'Synthetic',
    learningEnabled: false, participationEnabled: false, proactiveEnabled: false }
  const scenes = [
    { ...base, kind: 'group', id: 'scene', platformSceneId: 'scene' },
    { ...base, kind: 'private', id: 'other', platformSceneId: 'other' },
    { ...base, kind: 'simulation', id: 'simulation', platformSceneId: 'simulation' },
  ]
  for (const scene of scenes) store.saveScene(scene)
  const endpoint = personId => ({ adapterId: 'fixture', accountId: 'local', personId })
  const a = endpoint('😀'.repeat(81)), b = endpoint('\ue000'), c = endpoint('a')
  const foreign = { adapterId: 'foreign', accountId: 'different', personId: 'f' }
  const link = (operationId, expectedRevision, left = a, right = b) =>
    ({ action: 'link', confirmed: true, operationId, expectedRevision, left, right })
  const pair = (action, ...args) => ({ ...link(...args), action })
  const split = (operationId, expectedRevision, partition) =>
    ({ action: 'split', operationId, expectedRevision, confirmed: true, partition })
  const put = (operationId, expectedRevision, changes = {}) => ({
    action: 'alias_put', operationId, expectedRevision, confirmed: true, sceneId: 'scene',
    aliasId: 'alias', endpoint: a, label: 'Synthetic alias body is not in journal',
    validFrom: '2001-02-01T00:00:00+01:00', validUntil: '2001-02-01T00:00:00Z', ...changes,
  })
  const remove = (operationId, expectedRevision, sceneId = 'scene') =>
    ({ action: 'alias_remove', operationId, expectedRevision, confirmed: true, sceneId, aliasId: 'alias' })
  const requests = [
    ['unauthorized-invalid', link('auth', 0), 401, { auth: false }],
    ['missing-type-empty', null, 400, { type: '', raw: '' }],
    ['wrong-type', link('wrong-type', 0), 503, { type: 'text/plain' }],
    ['wrong-type-empty', null, 503, { type: 'text/plain', raw: '' }],
    ['typed-empty', null, 503, { raw: '' }],
    ['utf8-bom-json', null, 409, { raw: '\ufeff' + JSON.stringify(link('bom-stale', 1)) }],
    ['missing-type', link('missing-type', 0), 503, { type: '' }],
    ['malformed', null, 503, { raw: '{' }],
    ['not-confirmed', { ...link('not-confirmed', 0), confirmed: false }, 400],
    ['strict-endpoint', link('strict', 0, { ...a, extra: 1 }), 400],
    ['stale-fresh', link('stale-fresh', 1), 409],
    ['operation-utf16', link('😀'.repeat(81), 0), 409],
    ['self-link', link('self', 0, a, a), 409],
    ['scene-unavailable-first', put('bad-scene', 0, { sceneId: 'simulation', endpoint: foreign }), 404],
    ['namespace-mismatch', put('bad-namespace', 0, { endpoint: foreign }), 400],
    ['datetime-format', put('bad-date', 0, { validFrom: 'not a date' }), 400],
    ['datetime-interval', put('bad-interval', 0, { validFrom: '2002-02-01T00:00:00Z' }), 409],
    ['link-first', link('first', 0, b, a), 200],
    ['link-cross-namespace', link('cross', 1, c, foreign), 200],
    ['duplicate-reversed-after-new-revision', link('first', 0, a, b), 200],
    ['operation-conflict', link('first', 2, a, b), 409],
    ['stale-link', link('stale', 1, a, c), 409],
    ['triangle-side-2', link('side2', 2, a, c), 200],
    ['triangle-side-3', link('side3', 3, b, c), 200],
    ['unlink-one-triangle-side', pair('unlink', 'unlink', 4), 200],
    ['split-duplicate-partition', split('bad-duplicate', 5, [a, a]), 409],
    ['split-foreign-component', split('bad-outside', 5, [a, endpoint('missing')]), 409],
    ['split-cycle-cut', split('split', 5, [a, b]), 200],
    ['split-replay-reordered', split('split', 5, [b, a]), 200],
    ['unlink-no-op-advances', pair('unlink', 'unlink-absent', 6), 200],
    ['alias-put', put('put', 7), 200],
    ['alias-replay', put('put', 7), 200],
    ['alias-immutable', put('immutable', 8, { label: 'different' }), 409],
    ['alias-remove-other-scene', remove('bad-remove', 8, 'other'), 404],
    ['alias-put-identical-new-key', put('put-again', 8), 200],
    ['alias-remove', remove('remove', 9), 200],
    ['alias-remove-replay-now-absent', remove('remove', 9, 'other'), 200],
    ['alias-remove-absent-advances', remove('remove-absent', 10), 200],
    ['durable-intent-main-rollback', put('fault', 11), 503, { rollback: true }],
  ]
  const cases = []
  const rows = () => store.db.prepare("SELECT 1 FROM sqlite_master WHERE type='table' AND name='memory_identity_registry'").get()
    ? store.db.prepare('SELECT value FROM memory_identity_registry WHERE id=1').get().value : null
  const startGate = metadata(store.db)
  for (const [name, request, status, options = {}] of requests) {
    if (options.rollback) store.db.exec("CREATE TRIGGER synthetic_registry_failure BEFORE UPDATE ON memory_identity_registry BEGIN SELECT RAISE(ABORT,'synthetic_registry_failure'); END")
    const before = store.recoveryAuthority.head().sequence
    const reply = await app.request('http://fixture.invalid' + path, {
      method: 'POST', headers: { Authorization: 'Bearer ' + (options.auth === false ? 'wrong-token' : 'synthetic-token'),
        'Content-Type': options.type ?? 'application/json', 'X-Request-ID': 'fixture-spoof' },
      body: options.raw ?? JSON.stringify(request),
    })
    const body = await reply.json()
    assert.equal(reply.status, status, name + ': ' + JSON.stringify(body))
    assert.match(reply.headers.get('x-request-id'), /^[0-9a-f-]{36}$/u)
    const requestId = body.requestId !== undefined
    if (requestId) assert.equal(body.requestId, reply.headers.get('x-request-id'))
    delete body.requestId
    const events = store.recoveryAuthority.db.prepare('SELECT payload FROM revocations WHERE sequence>? ORDER BY sequence').all(before).map(r => r.payload)
    for (const event of events) assert.doesNotMatch(event, /Synthetic alias body|administratorId|confirmed|sceneId.*scopeSceneId/u)
    const gate = metadata(store.db)
    const state = rows()
    cases.push({ name, request, ...options, status, body, requestId, events,
      cacheControl: reply.headers.get('cache-control') ?? '', nosniff: reply.headers.get('x-content-type-options') ?? '',
      state, headDelta: store.recoveryAuthority.head().sequence - startGate.head.sequence,
      gateDelta: gate.head.sequence - startGate.head.sequence, minimumReader: gate.minimumReader })
  }
  const byName = name => cases.find(c => c.name === name)
  assert.deepEqual(byName('duplicate-reversed-after-new-revision').body, { revision: 1, duplicate: true })
  assert.deepEqual(byName('alias-remove-replay-now-absent').body, { revision: 10, duplicate: true })
  assert.equal(byName('link-first').minimumReader, 6)
  assert.equal(byName('stale-fresh').state, null)
  const fault = cases.at(-1), previous = cases.at(-2)
  assert.equal(fault.state, previous.state)
  assert.equal(fault.gateDelta, previous.gateDelta)
  assert.equal(fault.headDelta, previous.headDelta + 1)
  const target = new URL('../internal/app/testdata/identity-admin-write-source.json', import.meta.url)
  const out = JSON.stringify({ node: process.version, scenes, minimumReader: startGate.minimumReader, cases }, null, 2) + '\n'
  if (process.argv.includes('--check')) assert.equal(readFileSync(target, 'utf8'), out)
  else writeFileSync(target, out)
  console.log('PASS', cases.length, 'actual-source identity administration writes with durable witnesses')
} finally {
  engine?.dispose()
  store?.close()
  process.chdir(cwd)
  rmSync(dir, { recursive: true, force: true })
}
