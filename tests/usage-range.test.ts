import test from 'node:test'
import assert from 'node:assert/strict'
import { spawnSync } from 'node:child_process'
import { mkdtempSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { Store } from '../src/store.ts'
import { createApp } from '../src/api.ts'
import { usageRange } from '../app/utils/usage-range.ts'

const log = {
  event: 'model.completed',
  modelId: 'm',
  providerId: 'p',
  role: 'replyer',
  durationMs: 2,
  usage: {
    inputTokens: 100,
    outputTokens: 20,
    totalTokens: 120,
    cacheReadInputTokens: 60,
    cacheWriteInputTokens: 0,
    ordinaryInputTokens: 40,
  },
} as const
const range = { start: '2026-11-03T12:00:00.000Z', end: '2026-11-04T12:00:00.000Z' }

test('exact ranges are half-open; hourly and daily token sums match without double counting', (t) => {
  t.mock.timers.enable({ apis: ['Date'], now: Date.parse(range.start) })
  const store = new Store(':memory:')
  try {
    store.recordModel(log)
    t.mock.timers.setTime(Date.parse(range.end) - 1)
    store.recordModel(log)
    t.mock.timers.setTime(Date.parse(range.end))
    store.recordModel(log)
    assert.equal(
      store.usage().reduce((n, r) => n + r.calls, 0),
      3,
    )
    for (const bucket of ['hour', 'day'] as const) {
      const rows = store.usage({ ...range, bucket })
      assert.equal(
        rows.reduce((n, r) => n + r.calls, 0),
        2,
      )
      assert.equal(
        rows.reduce((n, r) => n + r.inputTokens, 0),
        200,
      )
      assert.equal(
        rows.reduce((n, r) => n + r.cacheReadInputTokens, 0),
        120,
      )
      assert.equal(
        rows.reduce((n, r) => n + r.cacheReadKnownCalls, 0),
        2,
      )
      assert.equal(store.usageIncomplete({ ...range, bucket }), false)
    }
    assert.equal(
      store
        .usage({ start: '2026-11-03T07:00:00-05:00', end: '2026-11-04T07:00:00-05:00' })
        .reduce((n, r) => n + r.calls, 0),
      2,
    )
    assert.throws(() => store.usage({ start: range.end, end: range.start }))
  } finally {
    store.close()
  }
})

test('old daily records migrate once and partial boundaries are explicitly incomplete', () => {
  const dir = mkdtempSync(join(tmpdir(), 'usage-history-')),
    path = join(dir, 'test.sqlite')
  let store = new Store(path)
  store.recordModel(log)
  // Recreate pre-upgrade storage without a timestamp ledger.
  store.db.exec('DROP TABLE model_usage_events; DROP TABLE model_usage_history;')
  store.db.prepare('UPDATE model_usage SET day=?').run('2026-11-03')
  store.close()
  try {
    store = new Store(path)
    const full = { start: '2026-11-03T00:00:00Z', end: '2026-11-04T00:00:00Z' }
    assert.equal(store.usage(full)[0]!.calls, 1)
    assert.equal(store.usageIncomplete(full), false)
    assert.deepEqual(store.usage(range), [])
    assert.equal(store.usageIncomplete(range), true)
    assert.deepEqual(store.usage({ ...full, bucket: 'hour' }), [])
    assert.equal(store.usageIncomplete({ ...full, bucket: 'hour' }), true)
    store.close()
    store = new Store(path)
    assert.equal(store.usage(full)[0]!.calls, 1)
  } finally {
    store.close()
    rmSync(dir, { recursive: true, force: true })
  }
})

test('retention compacts details into daily history atomically and preserves all-time totals', (t) => {
  t.mock.timers.enable({ apis: ['Date'], now: Date.parse('2026-01-01T12:00:00Z') })
  const store = new Store(':memory:')
  try {
    store.recordModel(log)
    t.mock.timers.setTime(Date.parse('2026-05-01T12:00:00Z'))
    store.recordModel(log)
    assert.equal(
      (store.db.prepare('SELECT count(*) n FROM model_usage_events').get() as { n: number }).n,
      1,
    )
    const old = { start: '2026-01-01T00:00:00Z', end: '2026-01-02T00:00:00Z' }
    assert.equal(store.usage(old)[0]!.calls, 1)
    assert.equal(
      store.usage().reduce((n, r) => n + r.calls, 0),
      2,
    )
    assert.equal(
      store
        .usage({ start: old.start, end: '2026-05-02T00:00:00Z' })
        .reduce((n, r) => n + r.calls, 0),
      2,
    )
    const plan = store.db
      .prepare('EXPLAIN QUERY PLAN SELECT * FROM model_usage_events WHERE at>=? AND at<?')
      .all(old.start, old.end)
    assert.match(JSON.stringify(plan), /model_usage_events_at/)
  } finally {
    store.close()
  }
})

test('usage API validates range pairs and reports legacy coverage', async () => {
  const store = new Store(':memory:')
  try {
    const app = createApp(store, { token: 'test-token' }),
      headers = { Authorization: 'Bearer test-token' }
    for (const query of [
      'start=bad&end=bad',
      'start=2026-01-01T00:00:00Z',
      'start=2026-02-01T00:00:00Z&end=2026-01-01T00:00:00Z',
      'bucket=invalid',
      'timeZone=Asia%2FTaipei',
      'start=2026-01-01T00:00:00Z&end=2026-01-02T00:00:00Z&timeZone=Not%2FAZone',
    ]) {
      assert.equal((await app.request(`/api/usage?${query}`, { headers })).status, 400)
    }
    assert.equal((await app.request('/api/usage')).status, 401)
    const response = await app.request(
      `/api/usage?${new URLSearchParams({ ...range, bucket: 'hour' })}`,
      { headers },
    )
    assert.equal(response.status, 200)
    assert.equal(response.headers.get('X-Usage-Incomplete'), 'false')
    assert.deepEqual(await response.json(), [])
    store.recordModel(log)
    store.db.exec(
      "INSERT INTO model_usage_history SELECT * FROM model_usage; UPDATE model_usage_history SET day='2026-11-03'",
    )
    const scoped = await app.request(
      `/api/usage?${new URLSearchParams({ ...range, bucket: 'hour', timeZone: 'Asia/Taipei' })}`,
      { headers },
    )
    assert.equal(scoped.headers.get('X-Usage-Incomplete'), 'true')
    assert.equal(scoped.headers.get('X-Usage-Planner-Incomplete'), 'false')
  } finally {
    store.close()
  }
})

test('presets use calendar DST boundaries, rolling 24h and clamped calendar months', () => {
  const script = `import { usageRange } from './app/utils/usage-range.ts'; console.log(JSON.stringify([
    usageRange('yesterday',new Date('2026-03-09T12:00:00-04:00')),
    usageRange('yesterday',new Date('2026-11-02T12:00:00-05:00')),
    usageRange('24h',new Date('2026-03-09T12:00:00-04:00')),
    usageRange('month',new Date('2026-03-31T12:00:00-04:00')),
    usageRange('custom',new Date(),'2026-03-08T02:30','2026-03-08T04:00')
  ]))`
  const result = spawnSync(
    process.execPath,
    ['--import', 'tsx', '--input-type=module', '-e', script],
    { env: { ...process.env, TZ: 'America/New_York' }, encoding: 'utf8' },
  )
  assert.equal(result.status, 0, result.stderr)
  const ranges = JSON.parse(result.stdout) as Array<{ start: string; end: string }>
  assert.equal(Date.parse(ranges[0]!.end) - Date.parse(ranges[0]!.start), 23 * 3600000)
  assert.equal(Date.parse(ranges[1]!.end) - Date.parse(ranges[1]!.start), 25 * 3600000)
  assert.equal(Date.parse(ranges[2]!.end) - Date.parse(ranges[2]!.start), 24 * 3600000)
  assert.equal(ranges[3]!.start, '2026-02-28T17:00:00.000Z')
  assert.equal(ranges[4], null)
  assert.equal(usageRange('custom', new Date(), 'bad', 'bad'), undefined)
  assert.equal(usageRange('custom', new Date(), range.end, range.start), undefined)
})

test('event write failure rolls back both precise and all-time accounting', () => {
  const store = new Store(':memory:')
  try {
    store.db.exec(
      `CREATE TRIGGER reject_usage BEFORE INSERT ON model_usage_events BEGIN SELECT RAISE(ABORT,'synthetic failure'); END;`,
    )
    assert.throws(() => store.recordModel(log), /synthetic failure/)
    assert.deepEqual(store.usage(), [])
    assert.equal(
      (store.db.prepare('SELECT count(*) n FROM model_usage_events').get() as { n: number }).n,
      0,
    )
  } finally {
    store.close()
  }
})

test('missing cache counters remain unknown and explicit zero remains known in temporal buckets', () => {
  const store = new Store(':memory:')
  try {
    store.recordModel({ ...log, usage: { inputTokens: 100, outputTokens: 20, totalTokens: 120 } })
    store.recordModel({ ...log, usage: { ...log.usage, cacheReadInputTokens: 0 } })
    const rows = store.usage({
      start: '2000-01-01T00:00:00Z',
      end: '2100-01-01T00:00:00Z',
      bucket: 'hour',
    })
    assert.equal(rows.length, 1)
    assert.equal(rows[0]!.calls, 2)
    assert.equal(rows[0]!.cacheReadKnownCalls, 1)
    assert.equal(rows[0]!.cacheReadInputTokens, 0)
  } finally {
    store.close()
  }
})

test('Planner range coverage ignores unrelated role history without claiming missing Planner hours', () => {
  const store = new Store(':memory:')
  try {
    store.recordModel(log)
    store.db.exec(
      "INSERT INTO model_usage_history SELECT * FROM model_usage; UPDATE model_usage_history SET day='2026-11-03'",
    )
    assert.equal(store.usageIncomplete(range), true)
    assert.equal(store.usageIncomplete(range, 'planner'), false)
    store.db.exec("UPDATE model_usage_history SET role='planner'")
    assert.equal(store.usageIncomplete(range, 'planner'), true)
  } finally {
    store.close()
  }
})

test('browser timezone buckets preserve local days, fractional offsets and distinct DST hours', () => {
  const store = new Store(':memory:')
  try {
    store.recordModel(log)
    store.db
      .prepare('UPDATE model_usage_events SET at=?,day=?')
      .run('2026-11-03T18:45:00.000Z', '2026-11-03')
    const selection = {
      start: '2026-11-03T00:00:00Z',
      end: '2026-11-05T00:00:00Z',
      timeZone: 'Asia/Kathmandu',
    }
    assert.equal(store.usage({ ...selection, bucket: 'day' })[0]!.day, '2026-11-04')
    assert.equal(store.usage({ ...selection, bucket: 'hour' })[0]!.day, '2026-11-03T18:15:00.000Z')
    store.db.exec('INSERT INTO model_usage_history SELECT * FROM model_usage')
    store.db.exec("UPDATE model_usage_history SET day='2026-11-03'")
    assert.equal(store.usageIncomplete({ ...selection, bucket: 'day' }), true)
    assert.equal(
      store.usage({ ...selection, bucket: 'day' }).reduce((n, r) => n + r.calls, 0),
      1,
      'local day chart must not duplicate or fabricate historical UTC days',
    )
    assert.throws(() => store.usage({ ...selection, timeZone: 'Not/AZone' }))
  } finally {
    store.close()
  }
})
