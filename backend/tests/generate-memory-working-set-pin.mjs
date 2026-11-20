import assert from 'node:assert/strict'
import { mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import Database from 'better-sqlite3'
import { createApp } from '../../src/api.ts'
import { Engine } from '../../src/engine.ts'
import { Store } from '../../src/store.ts'

assert.equal(process.version, 'v24.22.1')
const root = mkdtempSync(join(tmpdir(), 'he-pin-'))
const cwd = process.cwd()
const syntheticToken = 'synthetic-token'
const scenes = [
  { id: 'scene', kind: 'group' },
  { id: 'foreign', kind: 'private' },
  { id: 'simulation', kind: 'simulation' },
].map((s) => ({
  ...s, adapterId: 'fixture', accountId: 'fixture', name: 'Synthetic', platformSceneId: s.id,
  learningEnabled: false, participationEnabled: false, proactiveEnabled: false,
}))
const records = ['target', 'failed'].map((id) => ({
  id, scope: { kind: 'scene', sceneId: 'scene', personId: 'person' }, kind: 'claim',
  status: 'active', content: 'Synthetic ' + id, learnedAt: '2025-01-01T00:00:00.000Z', sources: [],
}))
const cases = []
let store, engine, authority
try {
  process.chdir(root)
  store = new Store(join(root, 'live/humanengine.sqlite'))
  engine = new Engine(store)
  const app = createApp(store, { engine, token: syntheticToken })
  engine.workspaceMaintenance.dispose()
  for (const scene of scenes) store.saveScene(scene)
  for (const record of records)
    store.db.prepare('INSERT INTO memories(id,scope_key,person_id,content,fingerprint,value) VALUES(?,?,?,?,?,?)')
      .run(record.id, 'scene:scene', record.scope.personId, record.content, record.id, JSON.stringify(record))
  const flags = (ws, ep) => store.set('settings', { ...store.settings, memoryWorkingSetEnabled: ws, memoryEpisodesEnabled: ep })
  flags(true, false)
  const gate = () => JSON.parse(store.db.prepare('SELECT value FROM recovery_gate WHERE id=1').get().value)
  authority = new Database(join(gate().authority, 'authority.sqlite'), { readonly: true })
  const page = await (await app.request('/api/memory-working-set?sceneId=scene&includeCold=true', {
    headers: { Authorization: 'Bearer ' + syntheticToken },
  })).json()
  const keys = new Map()
  for (const { record, accessibility } of page.items) {
    assert.match(accessibility.dependencyKey, /^[a-f0-9]{64}$/)
    keys.set(record.id, accessibility.dependencyKey)
  }
  assert.equal(keys.size, 2)
  const selection = (id = 'target', dependencyKey = keys.get(id)) => ({
    ref: { kind: 'assertion', id, version: 1, deletionEpoch: 0 }, dependencyKey,
  })
  const command = (extra = {}) => ({ sceneId: 'scene', selection: selection(), expectedRevision: 0, pinned: true, ...extra })
  const bad = '0'.repeat(64)
  const normalize = (raw) => {
    let result = raw
    for (const [id, key] of keys) result = result.replaceAll(key, '@' + id)
    return result
  }
  const max = () => authority.prepare('SELECT max(sequence) n FROM revocations').get().n ?? 0
  async function capture(name, input = command(), { authorized = true, contentType = 'application/json', absent = false } = {}) {
    const requestBody = absent ? '' : typeof input === 'string' ? input : JSON.stringify(input)
    const headers = { 'X-Request-ID': '12345678-1234-4123-8123-123456789012' }
    if (contentType) headers['Content-Type'] = contentType
    if (authorized) headers.Authorization = 'Bearer ' + syntheticToken
    const before = max(), gateBefore = gate().head.sequence
    const response = await app.request('/api/memory-working-set/pin', {
      method: 'POST', headers, ...(absent ? {} : { body: requestBody }),
    })
    const body = await response.json()
    if (body.requestId) body.requestId = '<request-id>'
    const events = authority.prepare('SELECT payload FROM revocations WHERE sequence>? ORDER BY sequence').all(before)
      .map((row) => JSON.parse(normalize(row.payload)))
    const rows = store.db.prepare('SELECT value FROM memory_working_set_pins ORDER BY scene_id,kind,entity_id').all()
      .map((row) => JSON.parse(normalize(row.value)))
    cases.push({ name, requestBody: normalize(requestBody), authorized, contentType, absent,
      status: response.status, body: JSON.parse(normalize(JSON.stringify(body))),
      headers: Object.fromEntries(['content-type', 'cache-control', 'x-content-type-options'].map((k) => [k, response.headers.get(k) ?? ''])),
      events, rows, gateAdvance: gate().head.sequence - gateBefore, reader: gate().minimumReader })
  }
  await capture('unauthorized-before-parser', '{broken', { authorized: false })
  await capture('malformed-json', '{broken')
  await capture('absent-body', null, { contentType: '', absent: true })
  await capture('different-content-type', command(), { contentType: 'text/plain' })
  await capture('extra-outer', command({ extra: true }))
  await capture('extra-selection', command({ selection: { ...selection(), extra: true } }))
  await capture('extra-reference', command({ selection: { ...selection(), ref: { ...selection().ref, mutationVersion: 1 } } }))
  await capture('missing-commitment-mutation', command({ selection: { ...selection(), ref: { ...selection().ref, kind: 'commitment' } } }))
  await capture('invalid-key', command({ selection: selection('target', 'F'.repeat(64)) }))
  await capture('unsafe-revision', command({ expectedRevision: 9007199254740992 }))
  await capture('string-revision', command({ expectedRevision: '0' }))
  await capture('astral-scene-max', command({ sceneId: '💡'.repeat(512) }))
  await capture('astral-scene-over', command({ sceneId: '💡'.repeat(513) }))
  await capture('missing-scene', command({ sceneId: 'missing' }))
  await capture('simulation', command({ sceneId: 'simulation' }))
  await capture('foreign', command({ sceneId: 'foreign' }))
  await capture('missing-unpin', command({ pinned: false }))
  await capture('stale-selection', command({ selection: selection('target', bad) }))
  await capture('pin')
  await capture('same-current-revision', command({ expectedRevision: 1 }))
  await capture('old-cas', command())
  flags(false, false)
  await capture('disabled-pin', command({ expectedRevision: 1 }))
  await capture('disabled-stale-unpin', command({ expectedRevision: 1, pinned: false, selection: selection('target', bad) }))
  await capture('repeat-disabled-unpin', command({ expectedRevision: 2, pinned: false }))
  flags(false, true)
  await capture('episodes-only-repin', command({ expectedRevision: 2 }))
  assert.equal(engine.memory.delete('target'), true)
  await capture('terminal-policy', command({ expectedRevision: 4, pinned: false }))
  store.db.exec("CREATE TRIGGER fixture_abort_pin BEFORE INSERT ON memory_working_set_pins WHEN new.entity_id='failed' BEGIN SELECT RAISE(ABORT,'fixture_pin_failure'); END")
  await capture('post-intent-failure', command({ selection: selection('failed') }))
  await capture('gap-before-parser', '{broken')
  const get = (name) => cases.find((x) => x.name === name)
  assert.equal(get('pin').events[0].kind, 'working_set_pin')
  assert.equal(get('pin').gateAdvance, 1)
  assert.equal(get('same-current-revision').events.length, 0)
  assert.equal(get('same-current-revision').gateAdvance, 0)
  assert.equal(get('old-cas').status, 409)
  assert.equal(get('disabled-pin').status, 409)
  assert.equal(get('disabled-stale-unpin').body.selection.dependencyKey, '@target')
  assert.equal(get('repeat-disabled-unpin').events.length, 0)
  assert.equal(get('episodes-only-repin').events.length, 1)
  assert.equal(get('terminal-policy').status, 409)
  assert.equal(get('post-intent-failure').events.length, 1)
  assert.equal(get('post-intent-failure').gateAdvance, 0)
  assert.equal(get('post-intent-failure').rows.length, 1)
  assert.equal(get('gap-before-parser').status, 503)
  const fixture = JSON.stringify({ node: process.version, scenes, records, cases }, null, 2) + '\n'
  const dest = new URL('../internal/app/testdata/memory-working-set-pin-source.json', import.meta.url)
  if (process.argv.includes('--check')) assert.equal(readFileSync(dest, 'utf8'), fixture)
  else writeFileSync(dest, fixture)
  console.log('PASS ' + cases.length + ' actual memory working-set pin oracle cases')
} finally {
  authority?.close()
  await engine?.dispose()
  store?.close()
  process.chdir(cwd)
  rmSync(root, { recursive: true, force: true })
}
