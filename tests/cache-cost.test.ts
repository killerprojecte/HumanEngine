import test from 'node:test'
import assert from 'node:assert/strict'
import Database from 'better-sqlite3'
import { mkdtempSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { Store } from '../src/store.ts'
import { enroll } from '../src/memory/recovery/ledger.ts'
import {
  calculateModelCost,
  cachePartition,
  type CostUsage,
  type ModelCostPrices,
} from '../src/model-cost.ts'
import type { ModelLog, RouterConfig } from '../src/models.ts'
const prices: ModelCostPrices = {
  inputUsdPerMillion: 2,
  outputUsdPerMillion: 8,
  cacheReadUsdPerMillion: 0.5,
  cacheWriteUsdPerMillion: 4,
  cacheWrite5mUsdPerMillion: 2.5,
  cacheWrite1hUsdPerMillion: 4,
  cacheWrite30mUsdPerMillion: 3,
  cacheWrite12hUsdPerMillion: 5,
}
const usage: CostUsage = {
  known: true,
  cacheKnown: true,
  inputTokens: 1000,
  outputTokens: 200,
  ordinaryInputTokens: 400,
  cacheReadInputTokens: 300,
  cacheWriteInputTokens: 300,
  cacheWrite5mInputTokens: 100,
  cacheWrite1hInputTokens: 50,
  cacheWrite30mInputTokens: 25,
  cacheWrite12hInputTokens: 25,
}
const config: RouterConfig = {
  providers: [],
  routes: [],
  models: [
    {
      id: 'm',
      displayName: 'M',
      providerId: 'p',
      apiModelName: 'api-current',
      capabilities: ['text'],
      ...prices,
    },
  ],
}
const log = (patch: Partial<ModelLog> = {}): ModelLog => ({
  event: 'model.completed',
  modelId: 'm',
  providerId: 'p',
  role: 'replyer',
  durationMs: 10,
  usage: { ...usage, totalTokens: 1200 },
  ...patch,
})
const closeTo = (actual: number, expected: number) =>
  assert.ok(Math.abs(actual - expected) < 1e-12, `${actual} != ${expected}`)

test('inclusive input is partitioned once; all four write TTLs and generic residual are charged disjointly', () => {
  assert.equal(cachePartition(usage)!.unclassifiedWriteTokens, 100)
  const cost = calculateModelCost(usage, prices)!
  closeTo(cost.ordinaryInputUsd!, 0.0008)
  closeTo(cost.cacheReadUsd!, 0.00015)
  closeTo(cost.cacheWriteUsd!, 0.00105)
  closeTo(cost.outputUsd!, 0.0016)
  closeTo(cost.estimatedUsd, 0.0036)
  closeTo(
    cost.estimatedUsd,
    cost.ordinaryInputUsd! + cost.cacheReadUsd! + cost.cacheWriteUsd! + cost.outputUsd!,
  )
})

test('missing counters or prices keep affected components unknown without ordinary-price fallback', () => {
  for (const incomplete of [
    { ...usage, cacheReadInputTokens: undefined },
    { ...usage, known: false },
    { ...usage, cacheWriteInputTokens: 900 },
    { ...usage, ordinaryInputTokens: 399 },
    { ...usage, cacheWrite5mInputTokens: 400 },
    { ...usage, cacheReadInputTokens: NaN },
  ])
    assert.equal(calculateModelCost(incomplete, prices), undefined)
  for (const key of Object.keys(prices) as Array<keyof ModelCostPrices>)
    assert.equal(
      calculateModelCost(usage, { ...prices, [key]: undefined })?.fullyPriced,
      false,
      key,
    )
  assert.equal(
    calculateModelCost(usage, { ...prices, cacheReadUsdPerMillion: Infinity })?.cacheReadUsd,
    undefined,
  )
  assert.equal(
    calculateModelCost(usage, { ...prices, cacheReadUsdPerMillion: -1 })?.cacheReadUsd,
    undefined,
  )
  const noCache = {
    ...usage,
    inputTokens: 400,
    ordinaryInputTokens: 400,
    cacheReadInputTokens: 0,
    cacheWriteInputTokens: 0,
    cacheWrite5mInputTokens: 0,
    cacheWrite1hInputTokens: 0,
    cacheWrite30mInputTokens: 0,
    cacheWrite12hInputTokens: 0,
  }
  assert.ok(calculateModelCost(noCache, { inputUsdPerMillion: 2, outputUsdPerMillion: 8 }))
  assert.ok(calculateModelCost(usage, { ...prices, cacheReadUsdPerMillion: 0 }))
})

test('store distinguishes fully priced, known-breakdown unpriced, missing-breakdown and unknown calls', () => {
  const store = new Store(':memory:')
  try {
    store.recordModel(log(), config)
    const incomplete = structuredClone(config)
    delete (incomplete.models[0] as ModelCostPrices).cacheReadUsdPerMillion
    store.recordModel(log(), incomplete)
    store.recordModel(
      log({ usage: { inputTokens: 1000, outputTokens: 200, totalTokens: 1200 } }),
      config,
    )
    store.recordModel(log({ usage: undefined, event: 'model.failed' }), config)
    const row = store.usage()[0]!
    assert.equal(row.calls, 4)
    assert.equal(row.pricedCalls, 1)
    assert.equal(row.partialPricedCalls, 2)
    assert.equal(row.cacheReadKnownCalls, 2)
    assert.equal(row.cacheWriteKnownCalls, 2)
    assert.equal(row.cacheKnownCalls, 2)
    assert.equal(row.unknownUsageCalls, 1)
    assert.equal(row.legacyCalls, 0)
    assert.equal(row.inputTokens, 3000)
    assert.equal(row.ordinaryInputTokens, 800)
    assert.equal(row.cacheReadInputTokens, 600)
    assert.equal(row.cacheWriteInputTokens, 600)
    assert.equal(row.cacheWrite30mInputTokens, 50)
    assert.equal(row.cacheWrite12hInputTokens, 50)
    closeTo(row.estimatedUsd, 0.00865)
    closeTo(
      row.estimatedUsd,
      row.ordinaryInputUsd + row.outputUsd + row.cacheReadUsd + row.cacheWriteUsd,
    )
  } finally {
    store.close()
  }
})

test('request price snapshot remains authoritative after live edits, including explicitly absent prices', () => {
  const store = new Store(':memory:')
  try {
    const old = { apiModelName: 'api-at-request', ...prices }
    const snapshotLog = { ...log(), pricing: old }
    const changed = structuredClone(config)
    changed.models[0]!.inputUsdPerMillion = 999
    store.recordModel(snapshotLog, changed)
    store.recordModel({ ...log(), pricing: { apiModelName: 'api-at-request' } }, changed)
    const row = store.usage()[0]!
    assert.equal(row.apiModelName, 'api-at-request')
    assert.equal(row.pricedCalls, 1)
    closeTo(row.estimatedUsd, 0.0036)
  } finally {
    store.close()
  }
})

test('legacy database/backup migration preserves aggregate prices and marks cache categories unknown once across reopen', async () => {
  const root = mkdtempSync(join(tmpdir(), 'he-cache-accounting-'))
  const path = join(root, 'old.sqlite'),
    backup = join(root, 'restored.sqlite')
  const db = new Database(path)
  db.exec(
    'CREATE TABLE model_usage(day TEXT NOT NULL,model_id TEXT NOT NULL,provider_id TEXT NOT NULL,api_model TEXT NOT NULL,role TEXT NOT NULL,calls INTEGER NOT NULL,input_tokens INTEGER NOT NULL,output_tokens INTEGER NOT NULL,duration_ms REAL NOT NULL,priced_calls INTEGER NOT NULL,estimated_usd REAL NOT NULL,PRIMARY KEY(day,model_id,provider_id,api_model,role))',
  )
  db.prepare('INSERT INTO model_usage VALUES(?,?,?,?,?,?,?,?,?,?,?)').run(
    '2026-01-01',
    'm',
    'p',
    'old-api',
    'thinker',
    3,
    1000,
    200,
    40,
    2,
    0.25,
  )
  // Enroll this synthetic current legacy database before producing its trusted backup.
  assert.throws(() => new Store(path), /recovery_legacy_enrollment_required/)
  enroll(db, path, root + '-authority').close()
  await db.backup(backup)
  db.close()
  try {
    for (const file of [path, backup]) {
      for (let iteration = 0; iteration < 2; iteration++) {
        const store = new Store(file)
        try {
          const row = store.usage()[0]!
          assert.equal(row.role, 'replyer')
          assert.equal(row.calls, 3)
          assert.equal(row.pricedCalls, 2)
          assert.equal(row.estimatedUsd, 0.25)
          assert.equal(row.legacyCalls, 3)
          assert.equal(row.legacyEstimatedUsd, 0.25)
          assert.equal(row.cacheKnownCalls, 0)
          assert.equal(row.ordinaryInputTokens, 0)
          assert.equal(
            row.cacheReadUsd + row.cacheWriteUsd + row.ordinaryInputUsd + row.outputUsd,
            0,
          )
          assert.equal(
            (store.db.prepare('SELECT role FROM model_usage').get() as { role: string }).role,
            'thinker',
          )
        } finally {
          store.close()
        }
      }
    }
  } finally {
    rmSync(root, { recursive: true, force: true })
    rmSync(root + '-authority', { recursive: true, force: true })
  }
})

test('cached-read-only provider reports retain read/output subtotal while write and ordinary remain unknown', () => {
  const partial: CostUsage = {
    known: true,
    cacheKnown: false,
    inputTokens: 1000,
    outputTokens: 200,
    cacheReadInputTokens: 300,
  }
  const cost = calculateModelCost(partial, prices)!
  assert.equal(cost.fullyPriced, false)
  assert.equal(cost.ordinaryInputUsd, undefined)
  assert.equal(cost.cacheWriteUsd, undefined)
  closeTo(cost.estimatedUsd, 0.00175)
  const store = new Store(':memory:')
  try {
    store.recordModel(log({ usage: { ...partial, totalTokens: 1200 } }), config)
    const row = store.usage()[0]!
    assert.equal(row.cacheReadInputTokens, 300)
    assert.equal(row.ordinaryInputTokens, 0)
    assert.equal(row.cacheWriteInputTokens, 0)
    assert.equal(row.cacheKnownCalls, 0)
    assert.equal(row.cacheReadKnownCalls, 1)
    assert.equal(row.cacheWriteKnownCalls, 0)
    assert.equal(row.pricedCalls, 0)
    assert.equal(row.partialPricedCalls, 1)
    closeTo(row.estimatedUsd, 0.00175)
    assert.equal(row.ordinaryInputUsd, 0)
    assert.equal(row.cacheWriteUsd, 0)
  } finally {
    store.close()
  }
})

test('invalid usage cannot gain component costs; only known zero buckets do not make unknown usage free', () => {
  assert.equal(calculateModelCost({ ...usage, known: false }, prices), undefined)
  assert.equal(
    calculateModelCost({ ...usage, cacheKnown: false, cacheReadInputTokens: 1001 }, prices),
    undefined,
  )
  assert.equal(
    calculateModelCost(
      { inputTokens: 1000, outputTokens: 0, cacheKnown: false, cacheReadInputTokens: 0 },
      prices,
    ),
    undefined,
  )
  assert.equal(
    calculateModelCost({ inputTokens: 0, outputTokens: 0, known: false }, prices),
    undefined,
  )
  const actualZero = calculateModelCost(
    {
      inputTokens: 0,
      outputTokens: 0,
      cacheKnown: true,
      cacheReadInputTokens: 0,
      cacheWriteInputTokens: 0,
    },
    {},
  )!
  assert.equal(actualZero.fullyPriced, true)
  assert.equal(actualZero.estimatedUsd, 0)
})
