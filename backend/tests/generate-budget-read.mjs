import assert from 'node:assert/strict'
import { readFileSync, writeFileSync } from 'node:fs'
import { createApp } from '../../src/api.ts'
import { Engine } from '../../src/engine.ts'
import { Store } from '../../src/store.ts'
import { BudgetService } from '../../src/budget/service.ts'

const today = '2031-03-15'
const older = '2031-03-14'
const store = new Store(':memory:')
const engine = new Engine(store)
engine.budget = new BudgetService(store, { now: () => Date.parse(today + 'T12:00:00Z'), bootId: 'fixture' })
const token = 'synthetic-token'
const app = createApp(store, { engine, token })
const routes = { coverage: '/api/budget', recovery: '/api/budget/recovery' }
const cases = []
const attempts = [
  { id:'settled', day:today, lane:'foreground', state:'settled', known:2011, held:0, overrun:false, unbounded:false },
  { id:'carry', day:older, lane:'maintenance', state:'unknown', known:7, held:4500, overrun:false, unbounded:false },
  { id:'partial', day:today, lane:'realtime', state:'partial', known:127, held:350, overrun:true, unbounded:false },
  { id:'reserved', day:today, lane:'foreground', state:'reserved', known:0, held:660, overrun:false, unbounded:false },
  { id:'started', day:older, lane:'foreground', state:'started', known:0, held:0, overrun:false, unbounded:false },
]
const restoreID = '12345678-1234-4234-8234-123456789012'
const settings = { budgetExperimentEnabled: true, budgetMode:'best_effort', budgetDailyUsd:2.0000000001 }
try {
  const capture = async (name, route, method = 'GET', authorized = true) => {
    const headers = { 'X-Request-ID': '12345678-1234-4123-8123-123456789012' }
    if (authorized) headers.Authorization = 'Bearer ' + token
    const response = await app.request(routes[route], { method, headers })
    const text = await response.text()
    const body = text ? JSON.parse(text) : null
    if (body?.requestId) body.requestId = '<request-id>'
    cases.push({ name, route, method, authorized, status: response.status, body, headers:
      Object.fromEntries(['cache-control', 'x-content-type-options', 'content-type'].map(k => [k, response.headers.get(k) ?? ''])) })
    return body
  }
  await capture('unauthenticated', 'coverage', 'GET', false)
  await capture('unauthenticated-head', 'recovery', 'HEAD', false)
  await capture('empty-disabled', 'coverage')
  await capture('no-restoration', 'recovery')
  await capture('head-coverage', 'coverage', 'HEAD')
  for (const a of attempts) {
    const payload = JSON.stringify({ id:a.id, requestId:a.id, attempt:1 })
    store.db.prepare(`INSERT INTO budget_attempts
      (id,payload_hash,payload,policy,boot_id,state,window_start,lane,reserved_nano,known_nano,held_nano,created_at,overrun,unbounded_exposure)
      VALUES(?, '', ?, '{}', 'fixture', ?, ?, ?, 0, ?, ?, ?, ?, ?)`).run(a.id, payload, a.state, a.day, a.lane, a.known, a.held, a.day + 'T00:00:00Z', Number(a.overrun), Number(a.unbounded))
  }
  store.db.exec("CREATE TABLE recovery_model_hold(id INTEGER PRIMARY KEY,restore_id TEXT,status TEXT)")
  store.db.prepare("INSERT INTO recovery_model_hold VALUES(1,?,'held')").run(restoreID)
  const held = await capture('spend-held-disabled', 'coverage')
  assert.equal(held.coverage, 'pending')
  assert.equal(held.pendingAttempts, 2)
  assert.equal(held.admissionHold, true)
  assert.equal(held.enabled, false)
  await capture('restoration-held', 'recovery')
  store.db.prepare("UPDATE recovery_model_hold SET status='manually_acknowledged'").run()
  await capture('restoration-acknowledged', 'recovery')
  await capture('head-recovery', 'recovery', 'HEAD')

  store.set('settings', { ...store.settings, ...settings })
  store.db.exec("UPDATE budget_attempts SET unbounded_exposure=1 WHERE id='carry'")
  const inputs = [
    { id:'current', billingDate:today, status:'known', amountNanoUsd:12345 },
    { id:'past', billingDate:older, status:'known', amountNanoUsd:300 },
    { id:'unknown', billingDate:today, status:'unknown' },
  ].map(a => ({ category:'tool', currency:'USD', periodStart:older, periodEnd:today, sourceId:'fixture-cost', ...a }))
  const receipts = []
  for (const input of inputs) {
    receipts.push(engine.budget.externalCosts.import(input))
    if (input.id === 'past') {
      const coverage = await capture('external-known-unbounded', 'coverage')
      assert.equal(coverage.externalCosts.knownAllTimeNanoUsd, 12645)
      assert.equal(coverage.coverage, 'unbounded')
    }
  }
  const incomplete = await capture('external-unknown-overshadows-unbounded', 'coverage')
  assert.equal(incomplete.coverage, 'external_incomplete')
  assert.equal(incomplete.remainingNanoUsd, 0)
  assert.equal(incomplete.enabled, true)
  store.db.exec("UPDATE recovery_model_hold SET status='malformed'")
  await capture('malformed-restoration', 'recovery')
  await capture('malformed-coverage', 'coverage')
  const fixture = JSON.stringify({ today, older, restoreID, settings, attempts, receipts, cases }, null, 2) + String.fromCharCode(10)
  const dest = new URL('../internal/app/testdata/budget-read-source.json', import.meta.url)
  if (process.argv.includes('--check')) assert.equal(readFileSync(dest, 'utf8'), fixture)
  else writeFileSync(dest, fixture)
  console.log(`PASS ${cases.length} actual-source budget read cases`)
} finally {
  await engine.dispose()
  store.close()
}
