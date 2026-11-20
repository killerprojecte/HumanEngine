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
const dir = mkdtempSync(join(tmpdir(), 'he-cost-correction-'))
const cwd = process.cwd()
const initialStamp = '2031-03-15T12:00:00.123Z'
let now = Date.parse(initialStamp)
const receipt = { id:'first', category:'tool', currency:'USD', billingDate:'2031-03-15',
  periodStart:'2031-03-14', periodEnd:'2031-03-15', sourceId:'fixture', status:'known', amountNanoUsd:11 }
const amend = (id = 'first', expectedRevision = 1, correctionId = 'amend1', amountNanoUsd = 22) =>
  ({ id, expectedRevision, correctionId, replacement:{ ...receipt, id, amountNanoUsd } })
const gateQuery = 'SELECT value FROM recovery_gate WHERE id=1'
const cases = []
let store, engine, authority
try {
  process.chdir(dir)
  store = new Store(join(dir, 'live/humanengine.sqlite'))
  engine = new Engine(store)
  engine.budget = new BudgetService(store, { now:() => now, bootId:'fixture' })
  const app = createApp(store, { engine, token:'synthetic-token' })
  const gate = () => JSON.parse(store.db.prepare(gateQuery).get().value)
  authority = new Database(join(gate().authority, 'authority.sqlite'), { readonly:true })
  let start = gate().head.sequence
  async function capture(name, input = amend(), authorized = true) {
    const requestBody = typeof input === 'string' ? input : JSON.stringify(input)
    const headers = { 'Content-Type':'application/json', 'X-Request-ID':'12345678-1234-4123-8123-123456789012' }
    if (authorized) headers.Authorization = 'Bearer synthetic-token'
    const res = await app.request('/api/budget/external-costs/corrections', { method:'POST', headers, body:requestBody })
    const body = await res.json()
    if (body.requestId) body.requestId = '<request-id>'
    const existing = Boolean(store.db.prepare("SELECT count(*) n FROM sqlite_master WHERE name='budget_external_cost_revisions'").get().n)
    const rows = existing ? store.db.prepare('SELECT count(*) n FROM budget_external_cost_revisions').get().n : 0
    const events = authority.prepare('SELECT count(*) n FROM revocations WHERE sequence>?').get(start).n
    cases.push({ name, requestBody, authorized, status:res.status, body, existing, rows, authority:events,
      gate:gate().head.sequence-start, reader:gate().minimumReader,
      headers:Object.fromEntries(['cache-control','x-content-type-options','content-type'].map(k => [k, res.headers.get(k) ?? ''])) })
  }
  await capture('auth', '{broken', false)
  await capture('malformed-json', '{broken')
  await capture('unknown-field', {...amend(), extra:true})
  await capture('mismatched-id', {...amend(), id:'different'})
  await capture('non-integer-revision', {...amend(), expectedRevision:1.5})
  await capture('unsafe-revision', {...amend(), expectedRevision:9007199254740992})
  await capture('invalid-correction-id', {...amend(), correctionId:'slash/'})
  await capture('missing-table')
  const seeded = engine.budget.externalCosts.import(receipt)
  start = gate().head.sequence
  await capture('missing-receipt', amend('other'))
  await capture('stale', amend('first', 2))
  now += 1000
  await capture('amend-first')
  await capture('repeat-reordered', {...amend(), replacement:Object.fromEntries(Object.entries(amend().replacement).reverse())})
  await capture('same-correction-id-other-revision', amend('first', 2))
  await capture('same-correction-id-other-replacement', amend('first', 1, 'amend1', 24))
  await capture('same-correction-id-other-entity', amend('other'))
  await capture('stale-new-correction-id', amend('first', 1, 'new'))
  now += 1000
  await capture('amend-second', amend('first', 2, 'amend2', 33))
  await capture('repeat-old-after-new')
  const atCap = { ...seeded, revision:1000, correctionId:'cap-seed', receipt:{...receipt,id:'upper'} }
  engine.budget.externalCosts.storage.put(atCap)
  await capture('source-revision-cap', amend('upper', 1000, 'over-cap'))
  await capture('safe-but-stale-revision', amend('first', 9007199254740991, 'stale-big'))
  const good = store.db.prepare("SELECT payload_hash FROM budget_external_cost_revisions WHERE correction_id='amend1'").get().payload_hash
  store.db.exec("UPDATE budget_external_cost_revisions SET payload_hash='invalid' WHERE correction_id='amend1'")
  await capture('corrupt-named-correction')
  await capture('unrelated-older-corruption', amend('first', 3, 'amend3', 44))
  store.db.prepare("UPDATE budget_external_cost_revisions SET payload_hash=? WHERE correction_id='amend1'").run(good)
  store.db.exec("CREATE TRIGGER fixture_abort_amend BEFORE INSERT ON budget_external_cost_revisions WHEN new.correction_id='fail' BEGIN SELECT RAISE(ABORT,'fixture_sql_failure'); END")
  now += 1000
  await capture('post-intent-failure', amend('first', 4, 'fail', 55))
  await capture('gap-fails-closed')
  const events = authority.prepare('SELECT payload FROM revocations WHERE sequence>? ORDER BY sequence').all(start).map(r => JSON.parse(r.payload))
  const fixture = JSON.stringify({ initialStamp, seeded, atCap, cases, events }, null, 2) + '\n'
  const dest = new URL('../internal/app/testdata/external-cost-correction-source.json', import.meta.url)
  if (process.argv.includes('--check')) assert.equal(readFileSync(dest, 'utf8'), fixture)
  else writeFileSync(dest, fixture)
  console.log('PASS ' + cases.length + ' external-cost correction oracle cases')
} finally {
  authority?.close()
  await engine?.dispose()
  store?.close()
  process.chdir(cwd)
  rmSync(dir, { recursive:true, force:true })
}
