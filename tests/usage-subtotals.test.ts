import test from 'node:test'
import assert from 'node:assert/strict'
import { mkdtempSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { Store } from '../src/store.ts'
const base = { modelId: 'm', providerId: 'p', role: 'planner', durationMs: 1 } as const
const good = { inputTokens: 1000, outputTokens: 100, totalTokens: 1100, cacheReadInputTokens: 600 }
const columns = [
  'reported_input_tokens',
  'reported_output_tokens',
  'reported_usage_calls',
  'cache_rate_input_tokens',
  'cache_rate_read_tokens',
  'cache_rate_known_calls',
]
function record(store: Store) {
  store.recordModel({ ...base, event: 'model.completed', usage: good })
  store.recordModel({
    ...base,
    event: 'model.completed',
    pricing: { apiModelName: 'unknown', outputUsdPerMillion: 1 },
    usage: { inputTokens: 500, outputTokens: 50, totalTokens: 550 },
  })
  store.recordModel({ ...base, event: 'model.failed' })
  store.recordModel({
    ...base,
    event: 'model.completed',
    usage: { inputTokens: 9000, outputTokens: 900, totalTokens: 9900, known: false },
  })
}
test('reported totals exclude invalid counts; cache rate uses matching input cohort', () => {
  const s = new Store(':memory:')
  try {
    record(s)
    const row = s.usage()[0]!
    assert.equal(row.calls, 4)
    assert.equal(row.reportedUsageCalls, 2)
    assert.equal(row.reportedInputTokens, 1500)
    assert.equal(row.reportedOutputTokens, 150)
    assert.equal(row.cacheRateKnownCalls, 1)
    assert.equal(row.cacheRateInputTokens, 1000)
    assert.equal(row.cacheRateReadTokens, 600)
    assert.equal(row.cacheRateReadTokens / row.cacheRateInputTokens, 0.6)
    assert.equal(row.inputTokens, 10500, 'legacy raw sum is not relabelled as a known subtotal')
  } finally {
    s.close()
  }
})
test('old event migration recovers known cohorts, mixed old daily history stays unclaimed, repeat open idempotent', () => {
  const dir = mkdtempSync(join(tmpdir(), 'usage-subtotals-'))
  const path = join(dir, 'db.sqlite')
  let s = new Store(path)
  try {
    record(s)
    // One old mixed day has no per-call timestamps, so none of its input subtotal is recoverable.
    s.db.exec(
      "INSERT INTO model_usage_history SELECT * FROM model_usage; UPDATE model_usage_history SET day='2026-01-01'",
    )
    for (const table of ['model_usage', 'model_usage_events', 'model_usage_history'])
      for (const column of columns) s.db.exec(`ALTER TABLE ${table} DROP COLUMN ${column}`)
    s.close()
    s = new Store(path)
    const events = s.usage({ start: '2026-01-02T00:00:00Z', end: '2030-01-01T00:00:00Z' })[0]!
    assert.equal(events.reportedInputTokens, 1500)
    assert.equal(events.cacheRateInputTokens, 1000)
    const history = s.usage({ start: '2026-01-01T00:00:00Z', end: '2026-01-02T00:00:00Z' })[0]!
    assert.equal(history.calls, 4)
    assert.equal(history.reportedUsageCalls, 0)
    assert.equal(history.reportedInputTokens, 0)
    assert.equal(history.cacheRateKnownCalls, 0)
    const before = s.usage()
    s.close()
    s = new Store(path)
    assert.deepEqual(s.usage(), before)
  } finally {
    s.close()
    rmSync(dir, { recursive: true, force: true })
  }
})
test('compaction preserves reported cohorts despite failed calls in the same day', (t) => {
  t.mock.timers.enable({ apis: ['Date'], now: Date.parse('2026-01-01T00:00:00Z') })
  const s = new Store(':memory:')
  try {
    record(s)
    t.mock.timers.setTime(Date.parse('2026-05-01T00:00:00Z'))
    s.recordModel({ ...base, event: 'model.failed' })
    const old = s.usage({ start: '2026-01-01T00:00:00Z', end: '2026-01-02T00:00:00Z' })[0]!
    assert.equal(old.reportedInputTokens, 1500)
    assert.equal(old.reportedUsageCalls, 2)
    assert.equal(old.cacheRateInputTokens, 1000)
    assert.equal(old.cacheRateReadTokens, 600)
    assert.equal(
      s.usageIncomplete({ start: '2026-01-01T08:00:00Z', end: '2026-01-02T00:00:00Z' }),
      true,
    )
  } finally {
    s.close()
  }
})

test('migration cannot upgrade cache-invalid legacy calls whose invalid counters were discarded', () => {
  const dir = mkdtempSync(join(tmpdir(), 'usage-invalid-'))
  const path = join(dir, 'db.sqlite')
  let s = new Store(path)
  try {
    s.recordModel({
      ...base,
      event: 'model.completed',
      usage: { inputTokens: 100, outputTokens: 10, totalTokens: 110, cacheReadInputTokens: 101 },
    })
    for (const table of ['model_usage', 'model_usage_events', 'model_usage_history'])
      for (const column of columns) s.db.exec(`ALTER TABLE ${table} DROP COLUMN ${column}`)
    s.close()
    s = new Store(path)
    const row = s.usage()[0]!
    assert.equal(row.reportedUsageCalls, 0)
    assert.equal(row.reportedInputTokens, 0)
    assert.equal(row.cacheRateKnownCalls, 0)
  } finally {
    s.close()
    rmSync(dir, { recursive: true, force: true })
  }
})

test('migration rebuilds all-time from history plus events once and never guesses mixed cache cohort', () => {
  const dir = mkdtempSync(join(tmpdir(), 'usage-cohorts-'))
  const path = join(dir, 'db.sqlite')
  let s = new Store(path)
  try {
    const pricing = { apiModelName: 'unknown', outputUsdPerMillion: 1 }
    s.recordModel({ ...base, event: 'model.completed', pricing, usage: good })
    s.recordModel({
      ...base,
      event: 'model.completed',
      pricing,
      usage: { inputTokens: 500, outputTokens: 50, totalTokens: 550 },
    })
    s.db.exec(
      'INSERT INTO model_usage_history SELECT * FROM model_usage; DELETE FROM model_usage_events',
    )
    // Add a fresh event alongside an old mixed daily aggregate with the same grouping key.
    s.recordModel({ ...base, event: 'model.completed', pricing, usage: good })
    for (const table of ['model_usage', 'model_usage_events', 'model_usage_history'])
      for (const column of columns) s.db.exec(`ALTER TABLE ${table} DROP COLUMN ${column}`)
    s.close()
    s = new Store(path)
    const row = s.usage()[0]!
    assert.equal(row.calls, 3)
    assert.equal(row.reportedUsageCalls, 3)
    assert.equal(row.reportedInputTokens, 2500)
    // Old mixed history's cache-read input cohort is unknowable; only the new event qualifies.
    assert.equal(row.cacheRateKnownCalls, 1)
    assert.equal(row.cacheRateInputTokens, 1000)
    assert.equal(row.cacheRateReadTokens, 600)
    const before = s.usage()
    s.close()
    s = new Store(path)
    assert.deepEqual(s.usage(), before)
  } finally {
    s.close()
    rmSync(dir, { recursive: true, force: true })
  }
})
