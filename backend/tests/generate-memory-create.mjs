import assert from 'node:assert/strict'
import { readFileSync, writeFileSync } from 'node:fs'
import { createApp } from '../../src/api.ts'
import { Engine } from '../../src/engine.ts'
import { Store } from '../../src/store.ts'

const store = new Store(':memory:')
const engine = new Engine(store)
const chosen = { scope: { kind: 'scene', sceneId: 'synthetic-a', personId: 'fixture-person' }, content: ' A synthetic fact. ', kind: 'fact' }
const title = 'synthetic-token'
const app = createApp(store, { engine, token: title })
const indexed = []
const index = engine.indexMemory.bind(engine)
engine.indexMemory = (id, content) => {
  indexed.push({ id, content })
  return index(id, content)
}
const cases = []
try {
  for (const [name, input, contentType = 'application/json', authorized = true] of [
    ['scene-person', chosen],
    ['other-scope', { ...chosen, scope: { kind: 'scene', sceneId: 'synthetic-b' }, importance: 'high', locked: true, confidence: 0.75 }],
    ['public', { scope: { kind: 'sharedPublicKnowledge' }, content: 'Synthetic public claim.', kind: 'claim', validFrom: '2024-02-29T12:32:11.123+02:30', validUntil: '2099-01-01T00:00:00Z', occurredAt: '2024-01-01T00:00:00Z', epistemicKind: 'reported' }],
    ['duplicate-scope', chosen],
    ['unauthorized', chosen, 'application/json', false],
    ['missing-field', { scope: chosen.scope, kind: chosen.kind }],
    ['invalid-scope', { ...chosen, scope: { ...chosen.scope, arbitrary: 'invalid' } }],
    ['invalid-date', { ...chosen, validFrom: '2026-02-29T00:00:00Z' }],
    ['invalid-offset', { ...chosen, occurredAt: '2024-01-01T00:00:00+24:00' }],
    ['bad-content-type', chosen, 'text/plain'],
    ['malformed-json', '{'],
    ['blank-business-content', { ...chosen, content: '\t \uFEFF' }],
    ['private-source-fields', { ...chosen, sources: [] }],
  ]) {
    const raw = typeof input === 'string' ? input : JSON.stringify(input)
    const response = await app.request('http://fixture.invalid/api/memories', {
      method: 'POST', headers: { 'Content-Type': contentType, ...(authorized ? { Authorization: `Bearer ${title}` } : {}) }, body: raw,
    })
    const body = await response.json()
    delete body.requestId
    if (response.ok) {
      assert.match(body.id, /^[0-9a-f-]{36}$/)
      assert.match(body.learnedAt, /^\d{4}-/)
      assert.deepEqual(indexed.at(-1), { id: body.id, content: body.content })
      const { id, learnedAt } = body
      assert.deepEqual(store.db.prepare('SELECT version, deletion_epoch FROM memory_assertion_state WHERE assertion_id=?').get(id), { version: body.revision.version, deletion_epoch: body.revision.deletionEpoch })
      assert.equal(JSON.parse(store.db.prepare('SELECT value FROM memories WHERE id=?').get(id).value).learnedAt, learnedAt)
      delete body.id
      delete body.learnedAt
    }
    cases.push({ name, raw, contentType, authorized, status: response.status, body })
  }
  assert.equal(indexed.length, 3)
  const target = new URL('../internal/app/testdata/memory-create-source.json', import.meta.url)
  const output = JSON.stringify({ node: process.version, cases }, null, 2) + '\n'
  if (process.argv.includes('--check')) assert.equal(readFileSync(target, 'utf8'), output)
  else writeFileSync(target, output)
} finally {
  await engine.dispose()
  store.close()
}
