import assert from 'node:assert/strict'
import { readFileSync, writeFileSync } from 'node:fs'
import { createApp } from '../../src/api.ts'
import { Engine } from '../../src/engine.ts'
import { Store } from '../../src/store.ts'
import { RESTORATION_REVIEW_ACK } from '../../src/memory/recovery/model-hold.ts'

assert.equal(process.version, 'v24.22.1')
const store = new Store(':memory:')
const engine = new Engine(store)
const app = createApp(store, { engine, token: 'synthetic-token' })
const path = '/api/budget/recovery/acknowledge'
const restoreId = '12345678-1234-4123-8123-1234567890ab'
const nextId = 'abcd5678-1234-4123-8123-1234567890ef'
const acknowledgment = RESTORATION_REVIEW_ACK
const valid = { restoreId, acknowledgment }
const cases = []
try {
  async function capture(name, data = valid, options = {}) {
    const { method = 'POST', authorized = true, contentType = 'application/json' } = options
    const headers = { 'X-Request-ID': '12345678-1234-4123-8123-123456789012' }
    if (authorized) headers.Authorization = 'Bearer synthetic-token'
    if (contentType !== '') headers['Content-Type'] = contentType
    const requestBody = ['GET','HEAD'].includes(method) ? '' : typeof data === 'string' ? data : JSON.stringify(data)
    const response = await app.request(path, { method, headers, ...(requestBody ? { body: requestBody } : {}) })
    const raw = await response.text()
    const body = raw.startsWith('{') ? JSON.parse(raw) : raw || null
    if (body?.requestId) body.requestId = '<request-id>'
    cases.push({ name, requestBody, method, authorized, contentType, status: response.status, body,
      headers: Object.fromEntries(['cache-control','x-content-type-options','content-type'].map(k => [k, response.headers.get(k) ?? ''])) })
  }
  await capture('unauthorized', '{broken', { authorized: false })
  await capture('absent')
  await capture('wrong-method', valid, { method: 'GET' })
  await capture('wrong-head', valid, { method: 'HEAD' })
  await capture('invalid-token', { restoreId, acknowledgment: 'wrong' })
  await capture('invalid-id', { restoreId: 'not-a-uuid', acknowledgment })
  await capture('extra', { ...valid, extra: true })
  await capture('malformed-json', '{broken')
  await capture('wrong-content-type', valid, { contentType: 'text/plain' })
  await capture('missing-content-type', valid, { contentType: '' })
  store.db.exec('CREATE TABLE recovery_model_hold(id INTEGER PRIMARY KEY CHECK(id=1),restore_id TEXT NOT NULL,status TEXT NOT NULL)')
  store.db.prepare("INSERT INTO recovery_model_hold VALUES(1,?,'held')").run(restoreId)
  await capture('stale', { restoreId: nextId, acknowledgment })
  await capture('same-id-case-sensitive', { restoreId: restoreId.toUpperCase(), acknowledgment })
  await capture('valid-v7-stale', { restoreId: 'abcd5678-1234-7123-8123-1234567890ef', acknowledgment })
  await capture('valid-nil-stale', { restoreId: '00000000-0000-0000-0000-000000000000', acknowledgment })
  await capture('acknowledged')
  await capture('idempotent')
  store.db.prepare("UPDATE recovery_model_hold SET status='held',restore_id=?").run(nextId)
  await capture('fresh-hold-rejects-old-id')
  await capture('fresh-hold-acknowledged', { restoreId: nextId, acknowledgment })
  store.db.exec("UPDATE recovery_model_hold SET status='not-canonical'")
  await capture('malformed-state', { restoreId: nextId, acknowledgment })
  store.db.exec("UPDATE recovery_model_hold SET status='held'; ALTER TABLE recovery_model_hold ADD COLUMN extra GENERATED ALWAYS AS ('bad') VIRTUAL")
  await capture('malformed-schema', { restoreId: nextId, acknowledgment })
  const fixture = JSON.stringify({ restoreId, nextId, cases }, null, 2) + '\n'
  const dest = new URL('../internal/app/testdata/budget-ack-source.json', import.meta.url)
  if (process.argv.includes('--check')) assert.equal(readFileSync(dest, 'utf8'), fixture)
  else writeFileSync(dest, fixture)
  console.log('PASS ' + cases.length + ' budget acknowledgement oracle cases')
} finally {
  await engine.dispose()
  store.close()
}
