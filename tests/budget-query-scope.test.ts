import test from 'node:test'
import assert from 'node:assert/strict'
import Database from 'better-sqlite3'
import { BudgetLedger } from '../src/budget/ledger.ts'
import type { BudgetDatabase } from '../src/budget/storage.ts'
import type { BudgetPolicy, BudgetRequest } from '../src/budget/contracts.ts'

const policy: BudgetPolicy = {
  dailyNanoUsd: Number.MAX_SAFE_INTEGER,
  maxUnresolvedNanoUsd: Number.MAX_SAFE_INTEGER,
}
const request = (id: string): BudgetRequest => ({
  id,
  requestId: `request-${id}`,
  attempt: 1,
  providerId: 'synthetic',
  modelId: 'model',
  operation: 'complete',
  lane: 'foreground',
  turnId: `turn-${id}`,
  estimate: {
    source: 'verified_contract',
    estimatorVersion: 'v1',
    contractVersion: 'v1',
    enforceable: true,
    inputUpperTokens: 1,
    outputUpperTokens: 0,
    inputCategories: ['inputUsdPerMillion'],
  },
  prices: { inputUsdPerMillion: 1, outputUsdPerMillion: 1 },
})
function fixture() {
  const db = new Database(':memory:')
  const reads: { sql: string; rows: number; params: unknown[] }[] = []
  const observed: BudgetDatabase = {
    exec: db.exec.bind(db),
    transaction: db.transaction.bind(db),
    prepare: ((sql: string) => {
      const statement = db.prepare(sql)
      return new Proxy(statement, {
        get(target, key) {
          if (key === 'all')
            return (...params: unknown[]) => {
              const result = target.all(...params)
              reads.push({ sql, rows: result.length, params })
              return result
            }
          const value = Reflect.get(target, key)
          return typeof value === 'function' ? value.bind(target) : value
        },
      })
    }) as BudgetDatabase['prepare'],
  }
  let now = Date.parse('2026-11-02T23:59:59Z')
  const ledger = new BudgetLedger(observed, { bootId: 'synthetic-boot', now: () => now })
  ledger.reserve(request('template'), policy)
  db.prepare(
    "UPDATE budget_attempts SET window_start='2020-01-01',state='complete',known_nano=1000,held_nano=0 WHERE id='template'",
  ).run()
  const insert = db.prepare(`INSERT INTO budget_attempts
    (id,payload_hash,payload,policy,boot_id,state,window_start,lane,turn_id,reserved_nano,known_nano,held_nano,created_at)
    SELECT ?,payload_hash,json_set(payload,'$.id',?,'$.requestId',?,'$.turnId',?),policy,boot_id,'complete','2020-01-01',lane,?,reserved_nano,1000,0,created_at
    FROM budget_attempts WHERE id='template'`)
  const add = (id: string) => insert.run(id, id, `request-${id}`, `turn-${id}`, `turn-${id}`)
  reads.length = 0
  return {
    db,
    ledger,
    reads,
    add,
    at: (date: string) => {
      now = Date.parse(date)
    },
  }
}

test('coverage and admission materialize relevant rows only among 20k historical settled attempts', () => {
  const f = fixture()
  try {
    f.db.transaction(() => {
      for (let i = 0; i < 20_000; i++) f.add(`old-${i}`)
    })()
    f.add('carry')
    f.add('old-overrun')
    f.add('old-unbounded')
    f.db.prepare("UPDATE budget_attempts SET state='unknown',held_nano=300 WHERE id='carry'").run()
    f.db
      .prepare(
        "UPDATE budget_attempts SET overrun=1,payload=json_set(payload,'$.modelId','other-model') WHERE id='old-overrun'",
      )
      .run()
    f.db.prepare("UPDATE budget_attempts SET unbounded_exposure=1 WHERE id='old-unbounded'").run()
    f.ledger.reserve(request('today'), { ...policy, mode: 'best_effort' })
    f.reads.length = 0
    const coverage = f.ledger.coverage(policy)
    assert.equal(coverage.knownNanoUsd, 0)
    assert.equal(coverage.heldNanoUsd, 1000)
    assert.equal(coverage.carriedNanoUsd, 300)
    assert.equal(coverage.unresolvedNanoUsd, 300)
    assert.equal(coverage.admissionExposureNanoUsd, 1300)
    assert.equal(coverage.laneExposureNanoUsd.foreground, 1300)
    assert.equal(coverage.overrunAttempts, 1)
    assert.equal(coverage.unboundedAttempts, 1)
    assert.equal(coverage.remainingNanoUsd, 0)
    assert.equal(f.reads.length, 1)
    // Today appears in window+held arms; deduplication must count it only once.
    assert.equal(f.reads[0]!.rows, 5)
    const coverageRead = f.reads[0]!
    const plan = f.db
      .prepare(`EXPLAIN QUERY PLAN ${coverageRead.sql}`)
      .all(...coverageRead.params) as { detail: string }[]
    for (const index of [
      'budget_window_idx',
      'budget_held_nonzero_idx',
      'budget_overrun_idx',
      'budget_unbounded_idx',
    ])
      assert.ok(
        plan.some(({ detail }) => detail.includes(index)),
        index,
      )
    assert.ok(
      !plan.some(({ detail }) =>
        /SCAN budget_attempts$|sqlite_autoindex_budget_attempts/.test(detail),
      ),
    )
    f.db.prepare("UPDATE budget_attempts SET unbounded_exposure=0 WHERE id='old-unbounded'").run()
    f.reads.length = 0
    f.ledger.reserve(request('next'), policy)
    assert.ok(f.reads.reduce((total, read) => total + read.rows, 0) <= 6)
    assert.ok(f.reads.every(({ sql }) => !/SELECT \* FROM budget_attempts\s*$/.test(sql)))
    const duplicatePlan = f.db
      .prepare(
        "EXPLAIN QUERY PLAN SELECT id FROM budget_attempts WHERE json_extract(payload,'$.requestId')=? AND json_extract(payload,'$.attempt')=?",
      )
      .all('request-old-1', 1) as { detail: string }[]
    assert.ok(duplicatePlan.some(({ detail }) => detail.includes('budget_request_attempt_idx')))
  } finally {
    f.db.close()
  }
})

test('UTC rollover keeps old active reservations, lifetime turn limits and global broken contracts', () => {
  const f = fixture()
  try {
    f.ledger.reserve(request('active'), policy)
    f.at('2026-11-03T00:00:00Z')
    const coverage = f.ledger.coverage(policy)
    assert.equal(coverage.windowStart, '2026-11-03')
    assert.equal(coverage.heldNanoUsd, 0)
    assert.equal(coverage.carriedNanoUsd, 1000)
    assert.throws(
      () =>
        f.ledger.reserve(
          { ...request('second'), turnId: 'turn-active' },
          { ...policy, perTurnCalls: 1 },
        ),
      /budget_turn_calls/,
    )
    assert.throws(
      () => f.ledger.reserve({ ...request('duplicate'), requestId: 'request-active' }, policy),
      /budget_attempt_identity_conflict/,
    )
    assert.equal(f.ledger.reserve(request('active'), policy).state, 'reserved')
    f.ledger.release('active')
    f.ledger.reserve(
      { ...request('second'), turnId: 'turn-active' },
      { ...policy, perTurnCalls: 1 },
    )
    f.db.prepare("UPDATE budget_attempts SET overrun=1 WHERE id='template'").run()
    assert.throws(() => f.ledger.reserve(request('blocked'), policy), /budget_contract_overrun/)
  } finally {
    f.db.close()
  }
})

test('exact coverage retains MAX_SAFE totals and rejects aggregates exceeding 2^53', () => {
  const f = fixture()
  try {
    f.add('exact')
    f.db
      .prepare("UPDATE budget_attempts SET window_start='2026-11-02',known_nano=? WHERE id='exact'")
      .run(Number.MAX_SAFE_INTEGER)
    assert.equal(f.ledger.coverage(policy).knownNanoUsd, Number.MAX_SAFE_INTEGER)
    f.add('overflow')
    f.db
      .prepare(
        "UPDATE budget_attempts SET window_start='2026-11-02',known_nano=2 WHERE id='overflow'",
      )
      .run()
    assert.throws(() => f.ledger.coverage(policy), /budget_amount_overflow/)
  } finally {
    f.db.close()
  }
})

test('malformed historical holds remain visible to baseline BigInt/safe-number validation', () => {
  for (const value of [-1, 0.5, 'not-an-integer', Buffer.from('bad')]) {
    const f = fixture()
    try {
      f.db.prepare("UPDATE budget_attempts SET held_nano=? WHERE id='template'").run(value)
      const stored = f.db
        .prepare("SELECT held_nano FROM budget_attempts WHERE id='template'")
        .get() as { held_nano: number }
      let expected: unknown
      try {
        const amount = BigInt(stored.held_nano)
        if (amount < 0n || amount > BigInt(Number.MAX_SAFE_INTEGER))
          throw new Error('budget_amount_overflow')
      } catch (error) {
        expected = error
      }
      assert.ok(expected instanceof Error)
      assert.throws(
        () => f.ledger.coverage(policy),
        (error: unknown) =>
          error instanceof Error &&
          error.constructor === (expected as Error).constructor &&
          error.message === (expected as Error).message,
      )
    } finally {
      f.db.close()
    }
  }
})

test('malformed legacy payload fails index migration closed and rolls back all new indexes', () => {
  const f = fixture()
  try {
    // Recreate the pre-optimization schema without new indexes. The JSON index
    // intentionally rejects corrupt legacy payloads earlier than admission did.
    for (const index of [
      'budget_held_nonzero_idx',
      'budget_overrun_idx',
      'budget_unbounded_idx',
      'budget_request_attempt_idx',
    ])
      f.db.exec(`DROP INDEX ${index}`)
    f.db.prepare("UPDATE budget_attempts SET payload='not-json' WHERE id='template'").run()
    const indexes = () =>
      f.db.prepare("SELECT name,sql FROM sqlite_master WHERE type='index' ORDER BY name").all()
    const before = indexes()
    assert.throws(() => new BudgetLedger(f.db, { bootId: 'migration' }), /malformed JSON/)
    assert.deepEqual(indexes(), before)
    assert.equal(f.db.inTransaction, false)
    assert.equal(
      (
        f.db.prepare("SELECT payload FROM budget_attempts WHERE id='template'").get() as {
          payload: string
        }
      ).payload,
      'not-json',
    )
  } finally {
    f.db.close()
  }
})
