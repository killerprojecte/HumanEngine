import assert from 'node:assert/strict'
import Database from 'better-sqlite3'
import { createHash } from 'node:crypto'
import { readFileSync } from 'node:fs'
import { ExternalCostStorage } from '../../../../src/budget/external-cost/storage.ts'
import { ExternalCostReceiptSchema } from '../../../../src/budget/external-cost/contracts.ts'
import { replayExternalCosts } from '../../../../src/memory/recovery/external-cost.ts'
assert.equal(process.version, 'v24.22.1', 'Use pinned Node 24.22.1')
// Deliberately reverse insertion order; Zod's parse order is the hash contract.
const base = {
  recordedAt: '2026-11-07T00:00:00.123456789Z',
  correctionId: null,
  receipt: {
    amountNanoUsd: 9007199254740991,
    status: 'known',
    sourceId: 'invoice',
    periodEnd: '2026-11-07',
    periodStart: '2026-11-01',
    billingDate: '2026-11-07',
    currency: 'USD',
    category: 'compute',
    id: 'cost',
  },
  revision: 1,
  schemaVersion: 1,
}
const second = structuredClone(base)
second.revision = 2
second.correctionId = 'fix'
second.receipt.amountNanoUsd = 7
const third = structuredClone(second)
third.revision = 3
third.correctionId = 'fix3'
const conflict = structuredClone(base)
conflict.receipt.amountNanoUsd = 1
const event = (r) => ({ kind: 'external_cost_receipt', receipt: r })
const cases = {
  success: [base, base, second, second],
  gap: [base, third],
  oldRepeat: [base, second, base],
  conflict: [base, conflict],
  correction: [base, second, { ...third, correctionId: 'fix' }],
}
const result = { raw: JSON.stringify(base), sources: {}, cases: {}, storage: {} }
for (const p of [
  'src/budget/external-cost/storage.ts',
  'src/budget/external-cost/contracts.ts',
  'src/memory/recovery/external-cost.ts',
  'src/memory/recovery/external-cost-contract.ts',
  'src/memory/recovery/ledger.ts',
])
  result.sources[p] = createHash('sha256').update(readFileSync(p)).digest('hex')
for (const [name, receipts] of Object.entries(cases)) {
  const db = new Database(':memory:')
  let error = ''
  try {
    replayExternalCosts(db, receipts.map(event))
  } catch (e) {
    error = e.message
  }
  const storage = new ExternalCostStorage(db)
  result.cases[name] = {
    events: receipts.map((r) => JSON.stringify(event(r))),
    error,
    heads: storage.current(),
    rows: db.prepare('SELECT * FROM budget_external_cost_revisions ORDER BY revision').all(),
  }
  db.close()
}
const db = new Database(':memory:')
const s = new ExternalCostStorage(db)
s.put(base)
result.storage.row = db.prepare('SELECT * FROM budget_external_cost_revisions').get()
result.storage.parsed = ExternalCostReceiptSchema.parse(base)
// Persist noncanonical raw history but canonical parsed hash: must decode.
db.prepare('UPDATE budget_external_cost_revisions SET payload=?').run(JSON.stringify(base))
result.storage.reordered = s.latest('cost')
for (const [name, sql] of Object.entries({
  hash: "UPDATE budget_external_cost_revisions SET payload_hash='bad'",
  material: "UPDATE budget_external_cost_revisions SET recorded_at='bad'",
  schema: 'ALTER TABLE budget_external_cost_revisions ADD COLUMN surprise TEXT',
})) {
  const copy = new Database(':memory:')
  const st = new ExternalCostStorage(copy)
  st.put(base)
  copy.exec(sql)
  try {
    st.current()
    result.storage[name] = 'ok'
  } catch (e) {
    result.storage[name] = e.message
  }
  copy.close()
}
console.log(JSON.stringify(result))
