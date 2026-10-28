import assert from 'node:assert/strict'
import test from 'node:test'
import {
  providerPolicy,
  providerOperationBudget,
  parseRetryAfter,
  retryDelay,
  waitRetry,
  runProviderAttempts,
  type ProviderRetryMetadata,
  type ProviderAttemptMetadata,
} from '../src/models/request-policy.ts'

test('provider policy defaults, strict bounds and probe budget are deterministic', () => {
  assert.deepEqual(providerPolicy({}), { retryCount: 2, maxAttempts: 3, timeoutMs: 30000 })
  assert.deepEqual(providerPolicy({ retryCount: 0, timeoutMs: 100 }), {
    retryCount: 0,
    maxAttempts: 1,
    timeoutMs: 100,
  })
  assert.deepEqual(providerPolicy({ retryCount: 5, timeoutMs: 300000 }), {
    retryCount: 5,
    maxAttempts: 6,
    timeoutMs: 300000,
  })
  for (const input of [null, 2, 'invalid', []])
    assert.throws(() => providerPolicy(input as never), /^Error: invalid_provider_request_policy$/)
  assert.equal(providerOperationBudget({}), 110000)
  assert.equal(providerOperationBudget({ retryCount: 0, timeoutMs: 100 }), 100)
  assert.equal(providerOperationBudget({ retryCount: 5, timeoutMs: 300000 }), 1850000)
  for (const retryCount of [-1, 6, 1.5, NaN, Infinity, null, '2'])
    assert.throws(
      () => providerPolicy({ retryCount: retryCount as number }),
      /^Error: invalid_provider_request_policy$/,
    )
  for (const timeoutMs of [99, 300001, 100.5, NaN, Infinity, null, '30000'])
    assert.throws(
      () => providerPolicy({ timeoutMs: timeoutMs as number }),
      /^Error: invalid_provider_request_policy$/,
    )
})

test('default policy makes exactly three retryable calls; retryCount zero makes one', async () => {
  for (const [policy, expected] of [
    [{}, 3],
    [{ retryCount: 0 }, 1],
  ] as const) {
    const attempts: ProviderAttemptMetadata[] = [],
      retries: ProviderRetryMetadata[] = [],
      delays: number[] = []
    const failure = new Error('fixture_failure')
    let calls = 0
    await assert.rejects(
      runProviderAttempts(
        policy,
        undefined,
        async () => {
          calls++
          throw failure
        },
        {
          classify: () => ({ retryable: true }),
          onAttempt: (row) => attempts.push(row),
          onRetry: (row) => retries.push(row),
          sleep: async (delay) => {
            delays.push(delay)
          },
        },
      ),
      (error) => error === failure,
    )
    assert.equal(calls, expected)
    assert.deepEqual(
      attempts.map((row) => row.attempt),
      Array.from({ length: expected }, (_, i) => i + 1),
    )
    assert.ok(attempts.every((row) => row.maxAttempts === expected && row.timeoutMs === 30000))
    assert.equal(retries.length, expected - 1)
    assert.deepEqual(delays, expected === 3 ? [250, 500] : [])
  }
})

test('two transient failures then success preserve result and configured timeout', async () => {
  const result = { fixture: true },
    retries: ProviderRetryMetadata[] = []
  let calls = 0
  assert.equal(
    await runProviderAttempts(
      { timeoutMs: 1250 },
      undefined,
      async (attempt, timeout) => {
        calls++
        assert.equal(attempt, calls)
        assert.equal(timeout, 1250)
        if (attempt < 3) throw new Error('transient')
        return result
      },
      {
        classify: () => ({ retryable: true, retryAfterMs: 700 }),
        onRetry: (row) => retries.push(row),
        sleep: async () => {},
      },
    ),
    result,
  )
  assert.equal(calls, 3)
  assert.deepEqual(
    retries.map((row) => row.delayMs),
    [700, 700],
  )
})

test('nonretryable errors retain identity and never sleep or emit a retry', async () => {
  let calls = 0
  const failure = new Error('permanent')
  await assert.rejects(
    runProviderAttempts(
      {},
      undefined,
      async () => {
        calls++
        throw failure
      },
      {
        classify: () => ({ retryable: false }),
        onRetry: () => assert.fail('unexpected retry'),
        sleep: async () => assert.fail('unexpected sleep'),
      },
    ),
    (error) => error === failure,
  )
  assert.equal(calls, 1)
})

test('parent abort during backoff stops retries and waitRetry removes its listener', async () => {
  const controller = new AbortController()
  let calls = 0,
    adds = 0,
    removes = 0
  const add = controller.signal.addEventListener.bind(controller.signal),
    remove = controller.signal.removeEventListener.bind(controller.signal)
  controller.signal.addEventListener = ((...args: Parameters<typeof add>) => {
    adds++
    return add(...args)
  }) as typeof add
  controller.signal.removeEventListener = ((...args: Parameters<typeof remove>) => {
    removes++
    return remove(...args)
  }) as typeof remove
  const pending = runProviderAttempts(
    {},
    controller.signal,
    async () => {
      calls++
      throw new Error('transient')
    },
    {
      classify: () => ({ retryable: true }),
      sleep: async (ms, signal) => {
        const waiting = waitRetry(ms, signal)
        controller.abort()
        return waiting
      },
    },
  )
  await assert.rejects(pending, /^Error: request_cancelled$/)
  assert.equal(calls, 1)
  assert.equal(adds, 1)
  assert.equal(removes, 1)
  await assert.rejects(waitRetry(0, controller.signal), /^Error: request_cancelled$/)
})

test('abort before a call and after a task result cannot trigger retries or success', async () => {
  const controller = new AbortController()
  controller.abort()
  await assert.rejects(
    runProviderAttempts({}, controller.signal, async () => assert.fail('unexpected task'), {
      classify: () => assert.fail('unexpected classification'),
      onAttempt: () => assert.fail('unexpected attempt'),
    }),
    /^Error: request_cancelled$/,
  )
  const late = new AbortController()
  await assert.rejects(
    runProviderAttempts(
      {},
      late.signal,
      async () => {
        late.abort()
        return 'not_delivered'
      },
      {
        classify: () => assert.fail('unexpected classification'),
      },
    ),
    /^Error: request_cancelled$/,
  )
  const normal = new AbortController()
  let removed = 0
  const original = normal.signal.removeEventListener.bind(normal.signal)
  normal.signal.removeEventListener = ((...args: Parameters<typeof original>) => {
    removed++
    return original(...args)
  }) as typeof original
  await waitRetry(0, normal.signal)
  assert.equal(removed, 1)
})

test('Retry-After supports seconds and dates with bounded exponential delays', () => {
  const now = Date.parse('2026-01-01T00:00:00Z')
  assert.equal(parseRetryAfter(null, now), undefined)
  assert.equal(parseRetryAfter(' ', now), undefined)
  assert.equal(parseRetryAfter('not-a-date', now), undefined)
  assert.equal(parseRetryAfter('2', now), 2000)
  assert.equal(parseRetryAfter('0.5', now), 500)
  assert.equal(parseRetryAfter('-1', now), 0)
  assert.equal(parseRetryAfter('999999999', now), 10000)
  assert.equal(parseRetryAfter('Thu, 01 Jan 2026 00:00:03 GMT', now), 3000)
  assert.equal(parseRetryAfter('Wed, 31 Dec 2025 23:59:59 GMT', now), 0)
  assert.equal(parseRetryAfter('Thu, 01 Jan 2026 00:01:00 GMT', now), 10000)
  assert.deepEqual(
    [1, 2, 3, 8, 1000].map((attempt) => retryDelay(attempt)),
    [250, 500, 1000, 10000, 10000],
  )
  assert.equal(retryDelay(2, 100), 500)
  assert.equal(retryDelay(2, 8000), 8000)
  assert.equal(retryDelay(2, 20000), 10000)
  assert.equal(retryDelay(2, NaN), 500)
})

test('cancellation from classification or backoff callbacks prevents the next attempt', async () => {
  for (const phase of ['classify', 'sleep'] as const) {
    const controller = new AbortController()
    let calls = 0,
      retries = 0
    await assert.rejects(
      runProviderAttempts(
        {},
        controller.signal,
        async () => {
          calls++
          throw new Error('transient')
        },
        {
          classify: () => {
            if (phase === 'classify') controller.abort()
            return { retryable: true }
          },
          onRetry: () => {
            retries++
          },
          sleep: async () => {
            controller.abort()
          },
        },
      ),
      /^Error: request_cancelled$/,
    )
    assert.equal(calls, 1)
    assert.equal(retries, phase === 'classify' ? 0 : 1)
  }
})
