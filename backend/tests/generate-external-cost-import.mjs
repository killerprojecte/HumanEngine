import assert from 'node:assert/strict'
import { mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import Database from 'better-sqlite3'
import { createApp } from '../../src/api.ts'
import { Engine } from '../../src/engine.ts'
import { Store } from '../../src/store.ts'
import { BudgetService } from '../../src/budget/service.ts'

assert.equal(process.version, 'v24.22.1')
const dir = mkdtempSync(join(tmpdir(), 'he-cost-import-'))
const cwd = process.cwd()
const stamp = '2031-03-15T12:00:00.123Z'
const base = { id:'first', category:'tool', currency:'USD', billingDate:'2031-03-15',
  periodStart:'2031-03-14', periodEnd:'2031-03-15', sourceId:'fixture', status:'known', amountNanoUsd:123 }
const gateQuery = 'SELECT value FROM recovery_gate WHERE id=1'
const cases = []
let store, engine, authority
try {
  process.chdir(dir)
  store = new Store(join(dir, 'live/humanengine.sqlite'))
  engine = new Engine(store)
  engine.budget = new BudgetService(store, { now:() => Date.parse(stamp), bootId:'fixture' })
  const app = createApp(store, { engine, token:'synthetic-token' })
  const gate = () => JSON.parse(store.db.prepare(gateQuery).get().value)
  authority = new Database(join(gate().authority, 'authority.sqlite'), { readonly:true })
  const start = gate().head.sequence
  async function capture(name, input = base, { authorized = true, contentType = 'application/json' } = {}) {
    const requestBody = typeof input === 'string' ? input : JSON.stringify(input)
    const headers = { 'Content-Type': contentType, 'X-Request-ID':'12345678-1234-4123-8123-123456789012' }
    if (authorized) headers.Authorization = 'Bearer synthetic-token'
    const res = await app.request('/api/budget/external-costs', { method:'POST', headers, body:requestBody })
    const body = await res.json()
    if (body.requestId) body.requestId = '<request-id>'
    const existing = store.db.prepare("SELECT count(*) n FROM sqlite_master WHERE name='budget_external_cost_revisions'").get().n
    const rows = existing ? store.db.prepare('SELECT count(*) n FROM budget_external_cost_revisions').get().n : 0
    const events = authority.prepare('SELECT payload FROM revocations WHERE sequence>? ORDER BY sequence').all(start).map(r => JSON.parse(r.payload))
    cases.push({ name, requestBody, authorized, contentType, status:res.status, body,
      headers:Object.fromEntries(['cache-control','x-content-type-options','content-type'].map(k => [k, res.headers.get(k) ?? ''])),
      existing:Boolean(existing), rows, authority:events.length, gate:gate().head.sequence-start, reader:gate().minimumReader })
  }
  await capture('auth', '{broken', { authorized:false })
  await capture('malformed-json', '{broken')
  await capture('content-type', base, { contentType:'text/plain' })
  await capture('additional-field', { ...base, extra:1 })
  await capture('known-without-amount', { ...base, amountNanoUsd:undefined })
  await capture('unknown-with-amount', { ...base, status:'unknown' })
  await capture('invalid-day', { ...base, billingDate:'2031-02-29' })
  await capture('invalid-period', { ...base, periodStart:'2031-03-16' })
  await capture('unsafe-amount', { ...base, amountNanoUsd:9007199254740992 })
  await capture('first')
  await capture('repeat', Object.fromEntries(Object.entries(base).reverse()))
  await capture('conflict', { ...base, amountNanoUsd:124 })
  await capture('minus-zero-first', JSON.stringify({ ...base, id:'signed', amountNanoUsd:0 }).replace('"amountNanoUsd":0','"amountNanoUsd":-0'))
  await capture('minus-zero-repeat', JSON.stringify({ ...base, id:'signed', amountNanoUsd:0 }).replace('"amountNanoUsd":0','"amountNanoUsd":-0'))
  await capture('positive-zero-repeat', { ...base, id:'signed', amountNanoUsd:0 })
  await capture('unknown', { ...base, id:'unknown', status:'unknown', amountNanoUsd:undefined })
  store.db.exec("CREATE VIEW budget_external_cost_extra AS SELECT 1")
  await capture('unsupported-object')
  store.db.exec('DROP VIEW budget_external_cost_extra')
  const originalHash = store.db.prepare("SELECT payload_hash FROM budget_external_cost_revisions WHERE id='unknown'").get().payload_hash
  store.db.exec("UPDATE budget_external_cost_revisions SET payload_hash='invalid' WHERE id='unknown'")
  await capture('unrelated-corruption-idempotent')
  await capture('eager-corruption-new-id', { ...base, id:'new' })
  await capture('same-id-corruption', { ...base, id:'unknown', status:'unknown', amountNanoUsd:undefined })
  store.db.prepare("UPDATE budget_external_cost_revisions SET payload_hash=? WHERE id='unknown'").run(originalHash)
  store.db.exec("CREATE TRIGGER fixture_abort_cost BEFORE INSERT ON budget_external_cost_revisions WHEN new.id='failed' BEGIN SELECT RAISE(ABORT,'fixture_sql_failure'); END")
  await capture('post-intent-failure', { ...base, id:'failed' })
  await capture('gap-fails-closed')
  const events = authority.prepare('SELECT payload FROM revocations WHERE sequence>? ORDER BY sequence').all(start).map(r => JSON.parse(r.payload))
  const fixture = JSON.stringify({ stamp, initialReader:cases[0].reader, cases, events }, null, 2) + '\n'
  const dest = new URL('../internal/app/testdata/external-cost-import-source.json', import.meta.url)
  if (process.argv.includes('--check')) assert.equal(readFileSync(dest, 'utf8'), fixture)
  else writeFileSync(dest, fixture)
  console.log('PASS ' + cases.length + ' external-cost import oracle cases')
} finally {
  authority?.close()
  await engine?.dispose()
  store?.close()
  process.chdir(cwd)
  rmSync(dir, { recursive:true, force:true })
}
