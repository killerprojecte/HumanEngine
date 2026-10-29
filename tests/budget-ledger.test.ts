import test from 'node:test'
import assert from 'node:assert/strict'
import Database from 'better-sqlite3'
import { mkdtempSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { BudgetLedger } from '../src/budget/ledger.ts'
import { quoteEstimate, roundedNanoUsd } from '../src/budget/estimate.ts'
import type { BudgetPolicy, BudgetRequest } from '../src/budget/contracts.ts'
const policy: BudgetPolicy = { dailyNanoUsd: 10_000_000, maxUnresolvedNanoUsd: 10_000_000 }
function request(id = 'a'): BudgetRequest {
  return {
    id,
    requestId: `request-${id}`,
    attempt: 1,
    providerId: 'synthetic',
    modelId: 'model',
    operation: 'complete',
    lane: 'foreground',
    turnId: 'turn',
    estimate: {
      source: 'verified_contract',
      estimatorVersion: 'synthetic-v1',
      contractVersion: 'synthetic-v1',
      enforceable: true,
      inputUpperTokens: 1000,
      outputUpperTokens: 100,
      inputCategories: ['inputUsdPerMillion', 'cacheReadUsdPerMillion', 'cacheWriteUsdPerMillion'],
    },
    prices: {
      inputUsdPerMillion: 2,
      outputUsdPerMillion: 10,
      cacheReadUsdPerMillion: 1,
      cacheWriteUsdPerMillion: 3,
    },
  }
}
const usage = {
  inputTokens: 1000,
  outputTokens: 100,
  cacheKnown: true,
  ordinaryInputTokens: 400,
  cacheReadInputTokens: 300,
  cacheWriteInputTokens: 300,
}
function fixture() {
  const db = new Database(':memory:')
  let now = Date.parse('2026-10-28T23:59:50Z')
  const ledger = new BudgetLedger(db, { bootId: 'boot-a', now: () => now })
  return { db, ledger, at: (value: string) => (now = Date.parse(value)) }
}

test('quote uses mutually exclusive worst input rate, exact upward precision, and rejects unknown bounds/prices', () => {
  const r = request()
  assert.equal(quoteEstimate(r.estimate, r.prices), 4_000_000)
  assert.equal(roundedNanoUsd(0.00000000001), 1)
  assert.equal(
    quoteEstimate(
      {
        ...r.estimate,
        inputUpperTokens: 1,
        outputUpperTokens: 0,
        inputCategories: ['inputUsdPerMillion'],
      },
      { inputUsdPerMillion: 0.00001 },
    ),
    1,
  )
  assert.throws(
    () => quoteEstimate({ ...r.estimate, enforceable: false }, r.prices),
    /estimate_unknown/,
  )
  assert.throws(
    () => quoteEstimate(r.estimate, { inputUsdPerMillion: 2, outputUsdPerMillion: 10 }),
    /price_unknown/,
  )
  assert.throws(() => roundedNanoUsd(Number.MAX_VALUE), /overflow/)
})

test('atomic reservations consume headroom and immutable snapshots, exact retries cannot restart transport', () => {
  const f = fixture()
  try {
    const r = request()
    const initial = f.ledger.reserve(r, policy)
    r.prices.inputUsdPerMillion = 999
    assert.equal(initial.reservedNanoUsd, 4_000_000)
    assert.equal(f.ledger.get('a').request.prices.inputUsdPerMillion, 2)
    assert.equal(f.ledger.reserve(request(), policy).reservedNanoUsd, 4_000_000)
    assert.throws(
      () => f.ledger.reserve({ ...request(), lane: 'maintenance' }, policy),
      /idempotency_conflict/,
    )
    f.ledger.reserve(request('b'), policy)
    assert.throws(() => f.ledger.reserve(request('c'), policy), /daily_limit/)
    assert.equal(f.ledger.start('a'), true)
    assert.equal(f.ledger.start('a'), false)
    const settled = f.ledger.settle('a', usage)
    assert.equal(settled.state, 'complete')
    assert.equal(settled.knownNanoUsd, 3_000_000)
    assert.equal(settled.heldNanoUsd, 0)
    assert.deepEqual(f.ledger.settle('a', usage), settled)
    assert.throws(() => f.ledger.settle('a', { ...usage, outputTokens: 99 }), /receipt_conflict/)
    assert.equal(f.ledger.coverage(policy).admissionExposureNanoUsd, 7_000_000)
  } finally {
    f.db.close()
  }
})

test('lane/turn monetary, token and attempt ceilings are independent and atomic', () => {
  for (const [limits, reason] of [
    [{ laneDailyNanoUsd: { maintenance: 3_000_000 } }, 'lane_limit'],
    [{ perTurnNanoUsd: 3_000_000 }, 'turn_amount'],
    [{ perTurnTokens: 1099 }, 'turn_tokens'],
    [{ perTurnCalls: 0 }, 'turn_calls'],
  ] as const) {
    const f = fixture()
    try {
      assert.throws(
        () => f.ledger.reserve({ ...request(), lane: 'maintenance' }, { ...policy, ...limits }),
        new RegExp(reason),
      )
      assert.equal(f.ledger.coverage(policy).admissionExposureNanoUsd, 0)
    } finally {
      f.db.close()
    }
  }
  const f = fixture()
  try {
    f.ledger.reserve(request(), { ...policy, perTurnCalls: 1 })
    assert.throws(
      () => f.ledger.reserve(request('b'), { ...policy, perTurnCalls: 1 }),
      /turn_calls/,
    )
  } finally {
    f.db.close()
  }
})

test('cancel before start releases; cancel after start holds unknown exposure, never free', () => {
  const f = fixture()
  try {
    f.ledger.reserve(request(), policy)
    assert.equal(f.ledger.release('a').heldNanoUsd, 0)
    assert.equal(f.ledger.start('a'), false)
    f.ledger.reserve(request('b'), policy)
    f.ledger.start('b')
    assert.throws(() => f.ledger.release('b'), /release_started/)
    const unknown = f.ledger.unknown('b')
    assert.equal(unknown.heldNanoUsd, 4_000_000)
    assert.deepEqual(f.ledger.unknown('b'), unknown)
    assert.throws(
      () => f.ledger.reserve(request('c'), { ...policy, maxUnresolvedNanoUsd: 3_999_999 }),
      /unresolved_limit/,
    )
  } finally {
    f.db.close()
  }
})

test('partial cache counters preserve known subtotal and retain unresolved remainder', () => {
  const f = fixture()
  try {
    f.ledger.reserve(request(), policy)
    f.ledger.start('a')
    const value = f.ledger.settle('a', {
      inputTokens: 1000,
      outputTokens: 100,
      cacheReadInputTokens: 300,
    })
    assert.equal(value.state, 'partial')
    assert.equal(value.knownNanoUsd, 1_300_000)
    assert.equal(value.heldNanoUsd, 2_700_000)
    assert.equal(f.ledger.coverage(policy).admissionExposureNanoUsd, 4_000_000)
  } finally {
    f.db.close()
  }
})

test('actual overrun remains visible rather than truncating bill to reserved amount', () => {
  const f = fixture()
  try {
    f.ledger.reserve(request(), policy)
    f.ledger.start('a')
    const actual = f.ledger.settle('a', {
      inputTokens: 2000,
      outputTokens: 1000,
      cacheKnown: true,
      ordinaryInputTokens: 2000,
      cacheReadInputTokens: 0,
      cacheWriteInputTokens: 0,
    })
    assert.equal(actual.knownNanoUsd, 14_000_000)
    assert.equal(actual.overrun, true)
    assert.equal(f.ledger.coverage(policy).remainingNanoUsd, 0)
  } finally {
    f.db.close()
  }
})

test('dead-boot recovery is explicit and unknown holds survive UTC rollover and SQLite restart', () => {
  const dir = mkdtempSync(join(tmpdir(), 'he-budget-'))
  const path = join(dir, 'db.sqlite')
  let db = new Database(path)
  try {
    const a = new BudgetLedger(db, { bootId: 'a', now: () => Date.parse('2026-10-28T23:59:59Z') })
    a.reserve(request('reserved'), policy)
    a.reserve(request('started'), policy)
    a.start('started')
    db.close()
    db = new Database(path)
    const b = new BudgetLedger(db, { bootId: 'b', now: () => Date.parse('2026-10-29T00:00:01Z') })
    assert.equal(b.coverage(policy).carriedNanoUsd, 8_000_000)
    assert.throws(() => b.start('reserved'), /attempt_owner/)
    assert.throws(() => b.recoverDeadBoot('b'), /active_boot/)
    assert.equal(b.recoverDeadBoot('a'), 2)
    assert.equal(b.recoverDeadBoot('a'), 0)
    assert.equal(b.get('reserved').state, 'released')
    assert.equal(b.get('started').state, 'unknown')
    assert.equal(b.coverage(policy).carriedNanoUsd, 4_000_000)
    b.settle('started', usage)
    assert.equal(b.get('started').windowStart, '2026-10-28')
    assert.equal(b.coverage(policy).knownNanoUsd, 0)
    assert.equal(b.coverage(policy).carriedNanoUsd, 0)
  } finally {
    db.close()
    rmSync(dir, { recursive: true, force: true })
  }
})

test('embedding output zero needs no output rate; malformed usage is unknown and raw secrets are not retained', () => {
  const f = fixture()
  try {
    const r = request()
    r.operation = 'embedding'
    r.estimate.outputUpperTokens = 0
    r.estimate.inputCategories = ['inputUsdPerMillion']
    r.prices = { inputUsdPerMillion: 2 }
    f.ledger.reserve(r, policy)
    f.ledger.start('a')
    const value = f.ledger.settle('a', {
      inputTokens: 1000,
      outputTokens: 0,
      cacheKnown: true,
      cacheReadInputTokens: 0,
      cacheWriteInputTokens: 0,
    })
    assert.equal(value.state, 'complete')
    assert.equal(value.knownNanoUsd, 2_000_000)
    assert.throws(
      () =>
        f.ledger.reserve({ ...request('bad'), prompt: 'PRIVATE_CANARY' } as BudgetRequest, policy),
      /metadata/,
    )
    f.ledger.reserve(request('broken'), policy)
    f.ledger.start('broken')
    assert.equal(f.ledger.settle('broken', { inputTokens: -1, outputTokens: 0 }).state, 'unknown')
    const dump = JSON.stringify(f.db.prepare('SELECT * FROM budget_attempts').all())
    assert.ok(!dump.includes('PRIVATE_CANARY'))
  } finally {
    f.db.close()
  }
})

test('separate SQLite connections cannot both spend the same remaining allowance', () => {
  const dir = mkdtempSync(join(tmpdir(), 'he-budget-concurrent-'))
  const path = join(dir, 'db.sqlite')
  const one = new Database(path),
    two = new Database(path)
  try {
    const a = new BudgetLedger(one, { bootId: 'a' }),
      b = new BudgetLedger(two, { bootId: 'b' })
    a.reserve(request(), { ...policy, dailyNanoUsd: 4_000_000 })
    assert.throws(
      () => b.reserve(request('b'), { ...policy, dailyNanoUsd: 4_000_000 }),
      /daily_limit/,
    )
    assert.equal(b.coverage(policy).admissionExposureNanoUsd, 4_000_000)
  } finally {
    one.close()
    two.close()
    rmSync(dir, { recursive: true, force: true })
  }
})

test('verified reconciliation releases prior unknown once and retains immutable policy snapshots', () => {
  const f = fixture()
  try {
    const limits = { ...policy, laneDailyNanoUsd: { foreground: 8_000_000 } }
    f.ledger.reserve(request(), limits)
    limits.laneDailyNanoUsd.foreground = 0
    assert.equal(f.ledger.get('a').policy.laneDailyNanoUsd?.foreground, 8_000_000)
    f.ledger.start('a')
    f.ledger.settle('a', { inputTokens: 1000, outputTokens: 100, cacheReadInputTokens: 300 })
    assert.throws(
      () => f.ledger.reconcile('a', { inputTokens: 1000, outputTokens: 100 }, 'receipt'),
      /incomplete/,
    )
    const final = f.ledger.reconcile('a', usage, 'receipt')
    assert.equal(final.state, 'complete')
    assert.equal(final.heldNanoUsd, 0)
    assert.deepEqual(f.ledger.reconcile('a', usage, 'receipt'), final)
    assert.throws(
      () => f.ledger.reconcile('a', { ...usage, outputTokens: 50 }, 'receipt'),
      /conflict/,
    )
    assert.equal(f.ledger.coverage(policy).unresolvedNanoUsd, 0)
  } finally {
    f.db.close()
  }
})

test('logical attempt aliases, policy payload injection, and future calls after contract overrun are rejected', () => {
  const f = fixture()
  try {
    f.ledger.reserve(request(), policy)
    assert.throws(
      () => f.ledger.reserve({ ...request(), id: 'second-id' }, policy),
      /attempt_identity_conflict/,
    )
    assert.throws(
      () => f.ledger.reserve(request('x'), { ...policy, prompt: 'PRIVATE_CANARY' } as BudgetPolicy),
      /metadata/,
    )
    f.ledger.start('a')
    f.ledger.settle('a', {
      inputTokens: 1001,
      outputTokens: 0,
      cacheKnown: true,
      cacheReadInputTokens: 0,
      cacheWriteInputTokens: 0,
    })
    assert.throws(() => f.ledger.reserve(request('x'), policy), /contract_overrun/)
  } finally {
    f.db.close()
  }
})

test('unexpected positive cache category invalidates an ordinary-only contract and exposes unbounded remainder', () => {
  const f = fixture()
  try {
    const r = request()
    r.estimate.inputCategories = ['inputUsdPerMillion']
    r.estimate.outputUpperTokens = 1
    r.prices = { inputUsdPerMillion: 1, outputUsdPerMillion: 1 }
    f.ledger.reserve(r, policy)
    f.ledger.start('a')
    const row = f.ledger.settle('a', {
      inputTokens: 1000,
      outputTokens: 1,
      cacheKnown: true,
      cacheReadInputTokens: 0,
      cacheWriteInputTokens: 1000,
    })
    assert.equal(row.state, 'partial')
    assert.equal(row.knownNanoUsd, 1000)
    assert.equal(row.overrun, true)
    assert.equal(row.unboundedExposure, true)
    assert.equal(f.ledger.coverage(policy).unboundedAttempts, 1)
    assert.throws(() => f.ledger.reserve(request('same'), policy), /unbounded_exposure/)
    assert.throws(
      () => f.ledger.reserve({ ...request('other'), modelId: 'different' }, policy),
      /unbounded_exposure/,
    )
  } finally {
    f.db.close()
  }
})

test('priced unexpected TTL preserves actual truth but stops reuse of its false category contract', () => {
  const f = fixture()
  try {
    const r = request()
    r.estimate.inputCategories = ['inputUsdPerMillion']
    r.prices.cacheWrite1hUsdPerMillion = 0.1
    f.ledger.reserve(r, policy)
    f.ledger.start('a')
    const row = f.ledger.settle('a', {
      inputTokens: 1000,
      outputTokens: 0,
      cacheKnown: true,
      cacheReadInputTokens: 0,
      cacheWriteInputTokens: 1000,
      cacheWrite1hInputTokens: 1000,
    })
    assert.equal(row.state, 'complete')
    assert.equal(row.knownNanoUsd, 100_000)
    assert.equal(row.overrun, true)
    assert.equal(row.unboundedExposure, false)
    assert.throws(() => f.ledger.reserve(request('next'), policy), /contract_overrun/)
  } finally {
    f.db.close()
  }
})

test('decimal settlement matches quote and rounds disjoint fractional components only once', () => {
  const f = fixture()
  try {
    const r = request()
    r.estimate.inputCategories = ['inputUsdPerMillion']
    r.estimate.inputUpperTokens = 1
    r.estimate.outputUpperTokens = 0
    r.prices = { inputUsdPerMillion: 0.1 }
    assert.equal(f.ledger.reserve(r, policy).reservedNanoUsd, 100)
    f.ledger.start('a')
    const row = f.ledger.settle('a', {
      inputTokens: 1,
      outputTokens: 0,
      cacheKnown: true,
      cacheReadInputTokens: 0,
      cacheWriteInputTokens: 0,
    })
    assert.equal(row.knownNanoUsd, 100)
    assert.equal(row.overrun, false)
    const mixed = request('mixed')
    mixed.estimate.inputUpperTokens = 3
    mixed.estimate.outputUpperTokens = 1
    mixed.prices = {
      inputUsdPerMillion: 0.0001,
      cacheReadUsdPerMillion: 0.0001,
      cacheWriteUsdPerMillion: 0.0001,
      outputUsdPerMillion: 0.0001,
    }
    f.ledger.reserve(mixed, policy)
    f.ledger.start('mixed')
    assert.equal(
      f.ledger.settle('mixed', {
        inputTokens: 3,
        outputTokens: 1,
        ordinaryInputTokens: 1,
        cacheKnown: true,
        cacheReadInputTokens: 1,
        cacheWriteInputTokens: 1,
      }).knownNanoUsd,
      1,
    )
  } finally {
    f.db.close()
  }
})
