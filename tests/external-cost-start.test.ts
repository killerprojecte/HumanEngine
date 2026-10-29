import test from 'node:test'
import assert from 'node:assert/strict'
import Database from 'better-sqlite3'
import { BudgetLedger } from '../src/budget/ledger.ts'
import { ExternalCostService } from '../src/budget/external-cost/service.ts'
import type { BudgetRequest } from '../src/budget/contracts.ts'

for (const mode of ['strict', 'best_effort'] as const) {
  test(`${mode}: late external receipts are checked at start, without double reservation`, () => {
    const db = new Database(':memory:')
    let now = Date.parse('2026-10-29T12:00:00Z')
    const costs = new ExternalCostService(db, {
      assertReady() {},
      beforeWrite() {},
      now: () => now,
    })
    const ledger = new BudgetLedger(db, {
      bootId: 'test',
      now: () => now,
      externalCoverage: (day) => costs.coverage(day),
    })
    const request = (id: string): BudgetRequest => ({
      id,
      requestId: id,
      attempt: 1,
      providerId: 'p',
      modelId: 'm',
      operation: 'complete',
      lane: 'foreground',
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
    const policy = { mode, dailyNanoUsd: 2000, maxUnresolvedNanoUsd: 2000 }
    const receipt = {
      id: 'late',
      category: 'tool' as const,
      currency: 'USD' as const,
      billingDate: '2026-10-29',
      periodStart: '2026-10-29',
      periodEnd: '2026-10-29',
      sourceId: 'synthetic',
    }
    try {
      ledger.reserve(request('unknown'), policy)
      costs.import({ ...receipt, status: 'unknown' })
      now += 86400000 // Unknown debt still blocks across actual UTC rollover.
      assert.throws(() => ledger.start('unknown'), /budget_external_cost_unknown/)
      assert.equal(ledger.get('unknown').state, 'reserved')
      assert.equal(ledger.get('unknown').startedAt, undefined)
      ledger.release('unknown')
      assert.equal(ledger.get('unknown').heldNanoUsd, 0)
      costs.correct({
        id: receipt.id,
        correctionId: 'known',
        expectedRevision: 1,
        replacement: { ...receipt, status: 'known', amountNanoUsd: 0 },
      })
      ledger.reserve(request('known'), policy)
      costs.import({
        ...receipt,
        id: 'extra',
        billingDate: '2026-10-30',
        status: 'known',
        amountNanoUsd: 1,
      })
      assert.throws(() => ledger.start('known'), /budget_daily_limit/)
      ledger.release('known')
      costs.correct({
        id: 'extra',
        correctionId: 'zero',
        expectedRevision: 1,
        replacement: {
          ...receipt,
          id: 'extra',
          billingDate: '2026-10-30',
          status: 'known',
          amountNanoUsd: 0,
        },
      })
      ledger.reserve(request('exact'), policy)
      assert.equal(ledger.start('exact'), true) // Exact cap: do not add reservation twice.
      assert.equal(ledger.start('exact'), false)
      assert.equal(ledger.coverage(policy).admissionExposureNanoUsd, 2000)
    } finally {
      db.close()
    }
  })
}
