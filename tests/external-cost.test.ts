import test from 'node:test'
import assert from 'node:assert/strict'
import Database from 'better-sqlite3'
import { mkdtempSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { ExternalCostService } from '../src/budget/external-cost/service.ts'
import type {
  ExternalCostInput,
  ExternalCostReceipt,
} from '../src/budget/external-cost/contracts.ts'
import { BudgetService } from '../src/budget/service.ts'
import { markRestoredModelHold } from '../src/memory/recovery/model-hold.ts'
import { BudgetLedger } from '../src/budget/ledger.ts'
import type { BudgetRequest } from '../src/budget/contracts.ts'
const input = (id = 'invoice_1'): ExternalCostInput => ({
  id,
  category: 'tool',
  currency: 'USD',
  billingDate: '2026-10-29',
  periodStart: '2026-10-01',
  periodEnd: '2026-10-31',
  sourceId: 'synthetic_vendor',
  status: 'known',
  amountNanoUsd: 8000,
})
function setup(db = new Database(':memory:')) {
  let now = Date.parse('2026-10-29T00:00:00Z')
  const writes: ExternalCostReceipt[] = []
  const service = new ExternalCostService(db, {
    assertReady() {},
    beforeWrite: (r) => writes.push(structuredClone(r)),
    now: () => now,
  })
  return {
    db,
    service,
    writes,
    now: () => now,
    advance: () => {
      now += 86400000
    },
  }
}
const request = (id: string): BudgetRequest => ({
  id,
  requestId: id,
  attempt: 1,
  providerId: 'provider',
  modelId: 'model',
  operation: 'complete',
  lane: 'foreground',
  turnId: id,
  estimate: {
    source: 'verified_contract',
    estimatorVersion: 'v1',
    contractVersion: 'v1',
    enforceable: true,
    inputUpperTokens: 2,
    outputUpperTokens: 0,
    inputCategories: ['inputUsdPerMillion'],
  },
  prices: { inputUsdPerMillion: 1 },
})
test('opaque receipt id is idempotent, explicit CAS corrections keep immutable revisions, invalid body never journals', () => {
  const f = setup()
  try {
    const first = f.service.import(input())
    assert.equal(first.revision, 1)
    assert.deepEqual(f.service.import(input()), first)
    assert.equal(f.writes.length, 1)
    assert.throws(
      () => f.service.import({ ...input(), status: 'known', amountNanoUsd: 9 }),
      /conflict/,
    )
    const correction = {
      id: 'invoice_1',
      expectedRevision: 1,
      correctionId: 'correction_1',
      replacement: { ...input(), status: 'known' as const, amountNanoUsd: 7 },
    }
    const revised = f.service.correct(correction)
    assert.equal(revised.revision, 2)
    assert.deepEqual(f.service.correct(correction), revised)
    assert.throws(
      () => f.service.correct({ ...correction, correctionId: 'correction_2' }),
      /revision_conflict/,
    )
    assert.equal(
      (f.db.prepare('SELECT count(*) n FROM budget_external_cost_revisions').get() as { n: number })
        .n,
      2,
    )
    assert.equal(f.service.coverage('2026-10-29').knownTodayNanoUsd, 7)
    assert.equal(f.writes.length, 2)
    assert.throws(() => f.service.import({ ...input('bad'), billingDate: '2026-02-30' }))
    assert.throws(() =>
      f.service.import({ ...input('bad'), invoiceBody: 'private' } as unknown as ExternalCostInput),
    )
    assert.throws(() =>
      f.service.import({ ...input('bad'), status: 'unknown' } as ExternalCostInput),
    )
    assert.equal(f.writes.length, 2)
    assert.throws(() => f.service.list({ pageSize: 51 }))
  } finally {
    f.db.close()
  }
})
test('known imported spend participates once in atomic admission; unknown receipts block both modes across actual date rollover', () => {
  const f = setup()
  try {
    const ledger = new BudgetLedger(f.db, {
      bootId: 'boot',
      now: f.now,
      externalCoverage: (day) => f.service.coverage(day),
    })
    f.service.import(input())
    const policy = { dailyNanoUsd: 9000, maxUnresolvedNanoUsd: 9000 }
    assert.equal(ledger.coverage(policy).knownNanoUsd, 8000)
    assert.equal(ledger.coverage(policy).remainingNanoUsd, 1000)
    assert.throws(() => ledger.reserve(request('blocked'), policy), /budget_daily_limit/)
    const { amountNanoUsd, ...unknown } = input('unknown') as Extract<
      ExternalCostInput,
      { status: 'known' }
    >
    f.service.import({ ...unknown, status: 'unknown' })
    for (const mode of ['strict', 'best_effort'] as const)
      assert.throws(
        () => ledger.reserve(request(mode), { ...policy, mode }),
        /budget_external_cost_unknown/,
      )
    f.advance()
    assert.equal(f.service.coverage('2026-10-30').knownAllTimeNanoUsd, 8000)
    assert.equal(ledger.coverage(policy).externalUnknownReceipts, 1)
    assert.equal(ledger.coverage(policy).remainingNanoUsd, 0)
    assert.throws(() => ledger.reserve(request('tomorrow'), policy), /budget_external_cost_unknown/)
    f.service.correct({
      id: 'unknown',
      expectedRevision: 1,
      correctionId: 'resolved',
      replacement: { ...unknown, status: 'known', amountNanoUsd: 0 },
    })
    assert.equal(ledger.reserve(request('allowed'), policy).reservedNanoUsd, 2000)
  } finally {
    f.db.close()
  }
})
test('persisted receipts retain billing date and unknown coverage after restart; storage malformed version fails closed', () => {
  const root = mkdtempSync(join(tmpdir(), 'he-external-cost-')),
    path = join(root, 'cost.sqlite')
  let db = new Database(path)
  try {
    let f = setup(db)
    f.service.import(input())
    const { amountNanoUsd, ...unknown } = input('pending') as Extract<
      ExternalCostInput,
      { status: 'known' }
    >
    f.service.import({ ...unknown, status: 'unknown' })
    db.close()
    db = new Database(path)
    f = setup(db)
    assert.equal(f.service.coverage('2030-01-01').unknownReceipts, 1)
    assert.equal(f.service.coverage('2030-01-01').knownAllTimeNanoUsd, 8000)
    assert.equal(f.service.coverage('2030-01-01').knownTodayNanoUsd, 0)
    db.exec(
      "UPDATE budget_external_cost_revisions SET payload=json_set(payload,'$.schemaVersion',2)",
    )
    assert.throws(() => f.service.coverage('2030-01-01'), /storage_invalid/)
  } finally {
    db.close()
    rmSync(root, { recursive: true, force: true })
  }
})

test('store-backed service OFF reports incomplete costs without enforcing experiment, while restore HOLD remains mandatory', () => {
  const db = new Database(':memory:')
  try {
    const service = new BudgetService(
      { db, settings: {} },
      { now: () => Date.parse('2026-10-29T00:00:00Z') },
    )
    const { amountNanoUsd, ...pending } = input('pending') as Extract<
      ExternalCostInput,
      { status: 'known' }
    >
    service.externalCosts.import({ ...pending, status: 'unknown' })
    assert.equal(service.gate(), undefined)
    assert.equal(service.coverage().coverage, 'external_incomplete')
    assert.equal(service.coverage().externalCosts.unknownReceipts, 1)
    assert.equal(service.coverage().externalCosts.coverage, 'incomplete')
    db.transaction(() => markRestoredModelHold(db)).immediate()
    assert.throws(() => service.gate(), /budget_restore_accounting_hold/)
    assert.equal(service.coverage().admissionHold, true)
  } finally {
    db.close()
  }
})

test('managed native completion and embedding admission report bounded unknown-cost reason before creating attempts', () => {
  const db = new Database(':memory:')
  try {
    const store = { db, settings: { budgetExperimentEnabled: true, budgetMode: 'strict' } }
    const service = new BudgetService(store, {
      contract: () => ({
        version: 'synthetic-v1',
        outputCapHonored: true,
        inputCategories: ['inputUsdPerMillion'],
      }),
      estimator: ({ outputCap }) => ({
        source: 'verified_contract',
        enforceable: true,
        estimatorVersion: 'synthetic-v1',
        contractVersion: 'synthetic-v1',
        inputUpperTokens: 1,
        outputUpperTokens: outputCap,
        inputCategories: ['inputUsdPerMillion'],
      }),
    })
    const { amountNanoUsd, ...pending } = input('pending') as Extract<
      ExternalCostInput,
      { status: 'known' }
    >
    service.externalCosts.import({ ...pending, status: 'unknown' })
    for (const operation of ['complete', 'embedding'] as const) {
      assert.throws(
        () =>
          service.gate()!.prepare(
            {
              providerId: 'synthetic',
              modelId: 'model',
              protocol: 'openai-chat',
              operation,
              role: operation === 'embedding' ? 'embedding' : 'replyer',
              prices: { inputUsdPerMillion: 1, outputUsdPerMillion: 1 },
            },
            {},
          ),
        /budget_external_cost_unknown/,
      )
    }
    assert.equal((db.prepare('SELECT count(*) n FROM budget_attempts').get() as { n: number }).n, 0)
  } finally {
    db.close()
  }
})
