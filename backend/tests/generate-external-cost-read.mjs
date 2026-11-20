import assert from 'node:assert/strict'
import { readFileSync, writeFileSync } from 'node:fs'
import { createApp } from '../../src/api.ts'
import { Engine } from '../../src/engine.ts'
import { Store } from '../../src/store.ts'
import { BudgetService } from '../../src/budget/service.ts'

assert.equal(process.version, 'v24.22.1')
const store = new Store(':memory:')
const engine = new Engine(store)
let now = Date.parse('2031-03-15T12:00:00Z')
engine.budget = new BudgetService(store, { now: () => now, bootId: 'fixture' })
const app = createApp(store, { engine, token: 'synthetic-token' })
const path = '/api/budget/external-costs'
const cases = []
try {
  const capture = async (name, query = '', method = 'GET', authorized = true) => {
    const headers = { 'X-Request-ID': '12345678-1234-4123-8123-123456789012' }
    if (authorized) headers.Authorization = 'Bearer synthetic-token'
    const res = await app.request(path + query, { method, headers })
    const raw = await res.text()
    const body = raw && JSON.parse(raw) || null
    if (body?.requestId) body.requestId = '<request-id>'
    cases.push({ name, query, method, authorized, status: res.status, body,
      headers: Object.fromEntries(['cache-control', 'x-content-type-options', 'content-type'].map(k => [k, res.headers.get(k) ?? ''])) })
  }
  await capture('absent-table')
  await capture('auth', '?unsupported=1', 'GET', false)
  await capture('head-empty', '', 'HEAD')

  const receipts = []
  for (const id of ['aa', 'a_a', 'a-A', 'a-a', 'AB']) {
    const input = { id, category:'tool', currency:'USD', billingDate:'2031-03-15', periodStart:'2031-03-14',
      periodEnd:'2031-03-15', sourceId:'fixture', status:'known', amountNanoUsd:100 }
    receipts.push(engine.budget.externalCosts.import(input))
  }
  now += 1000
  receipts.push(engine.budget.externalCosts.correct({ id:'AB', expectedRevision:1, correctionId:'correct-AB',
    replacement:{ ...receipts.at(-1).receipt, amountNanoUsd:321 } }))
  await capture('sort-all')
  await capture('page-two', '?page=2&pageSize=2')
  await capture('coerce', '?page=0x02&pageSize=%20%2B2e0%20')
  await capture('maximum-empty', '?page=10000&pageSize=50')
  await capture('invalid-page', '?page=10001')
  await capture('invalid-size', '?pageSize=51')
  await capture('duplicate', '?page=1&page=1')
  await capture('unknown', '?unknown=1')
  await capture('empty-value', '?page=')
  await capture('invalid-js-number', '?page=0x_1')
  await capture('invalid-head', '?extra=1', 'HEAD')
  store.db.exec("UPDATE budget_external_cost_revisions SET payload_hash='corrupt' WHERE id='aa'")
  await capture('eager-corruption', '?pageSize=1')
  const fixture = JSON.stringify({ receipts, cases }, null, 2) + String.fromCharCode(10)
  const dest = new URL('../internal/app/testdata/external-cost-read-source.json', import.meta.url)
  if (process.argv.includes('--check')) assert.equal(readFileSync(dest, 'utf8'), fixture)
  else writeFileSync(dest, fixture)
  console.log('PASS ' + cases.length + ' external-cost read oracle cases')
} finally {
  await engine.dispose()
  store.close()
}
