import assert from 'node:assert/strict'
import { readFileSync, writeFileSync } from 'node:fs'
import { createApp } from '../../src/api.ts'
import { Engine } from '../../src/engine.ts'
import { IdentityRegistry } from '../../src/memory/identities/service.ts'
import { Store } from '../../src/store.ts'

const token = 'synthetic-token'
const headers = { Authorization: 'Bearer ' + token, 'Content-Type': 'application/json' }
const path = '/api/memory-identities'
const store = new Store(':memory:')
const engine = new Engine(store)
const app = createApp(store, { engine, token })
const fixture = []
const exists = () => Boolean(store.db.prepare("SELECT 1 FROM sqlite_master WHERE name='memory_identity_registry'").get())
async function capture(name, { route = path, method = 'GET', auth = true, action } = {}) {
  const reply = await app.request('http://fixture.invalid' + route, {
    method, headers: auth ? headers : { Authorization: 'Bearer wrong-token' },
  })
  const body = method === 'HEAD' ? null : await reply.json()
  if (body?.requestId !== undefined) {
    assert.equal(body.requestId, reply.headers.get('x-request-id'))
    delete body.requestId
  }
  fixture.push({ name, route, method, auth, action, status: reply.status, body,
    noStore: reply.headers.get('cache-control') === 'no-store', table: exists() })
}

try {
  assert.equal(exists(), false)
  await capture('unauthorized', { auth: false })
  await capture('fresh-empty')
  await capture('fresh-head', { method: 'HEAD' })
  assert.equal(exists(), false, 'the real route must not create a missing table')
  const a = { adapterId: 'fixture', accountId: 'local', personId: '😀'.repeat(81) }
  const b = { adapterId: 'other-adapter', accountId: 'foreign', personId: 'other-person' }
  store.saveScene({ id: 'scene', kind: 'group', adapterId: 'fixture', accountId: 'local',
    platformSceneId: 'scene', name: 'Synthetic', learningEnabled: false, participationEnabled: false,
    proactiveEnabled: false })
  async function post(action, expectedRevision, body) {
    const reply = await app.request('http://fixture.invalid' + path, {
      method: 'POST', headers, body: JSON.stringify({ action, expectedRevision, confirmed: true,
        operationId: 'do-not-expose-operation-' + expectedRevision, ...body }),
    })
    assert.equal(reply.status, 200, await (reply.clone()).text())
  }
  await post('link', 0, { left: a, right: b })
  await post('alias_put', 1, { aliasId: 'manual', sceneId: 'scene', endpoint: a, label: 'Expired manual',
    validFrom: '2001-01-01T00:00:00+01:00', validUntil: '2002-01-01T00:00:00Z' })

  const registry = new IdentityRegistry(store.db, { assertReady() {}, authorizeObservation: () => true,
    sourceAvailable: () => true })
  store.db.transaction(() => registry.putAlias({ operationId: 'do-not-expose-observation',
    expectedRevision: 2, adminConfirmed: true }, {
    id: 'observed', endpoint: a, label: 'Observed alias', scopeSceneId: 'scene', validFrom: null, validUntil: null,
    source: { kind: 'observation', sceneId: 'scene', messageId: 'source-message', senderId: 'synthetic-sender',
      eventRef: { eventId: 'synthetic-event', revision: 1, deletionEpoch: 0 } },
  }))()
  const data = JSON.parse(store.db.prepare('SELECT value FROM memory_identity_registry WHERE id=1').get().value)
  await capture('loaded-public-view', { action: 'seed' })
  await capture('no-scene-filter', { route: path + '?sceneId=foreign&extra=ignored' })
  await capture('loaded-head', { method: 'HEAD' })
  assert.deepEqual(fixture.at(-3).body.edges, data.edges)
  assert.deepEqual(fixture.at(-3).body.aliases, data.aliases)
  assert.doesNotMatch(JSON.stringify(fixture), /do-not-expose|operations/)
  store.db.exec('DELETE FROM memory_identity_registry')
  await capture('missing-singleton-is-corrupt', { action: 'delete' })
  assert.equal(fixture.at(-1).status, 503)
  const file = new URL('../internal/app/testdata/identity-admin-read-source.json', import.meta.url)
  const output = JSON.stringify({ node: process.version, data, cases: fixture }, null, 2) + '\n'
  if (process.argv.includes('--check')) assert.equal(readFileSync(file, 'utf8'), output)
  else writeFileSync(file, output)
  console.log(`PASS ${fixture.length} actual-source identity administration reads`)
} finally {
  engine.dispose()
  store.close()
}
