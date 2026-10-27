import assert from 'node:assert/strict'
import test from 'node:test'
import { Store } from '../src/store.ts'
import { Engine } from '../src/engine.ts'
import { createApp } from '../src/api.ts'

const headers = {
  Authorization: 'Bearer settings-fixture-only',
  'Content-Type': 'application/json',
}
const limits = {
  learningQueueMaxMessages: [60, 20000, 2048],
  learningQueueMaxTotalMessages: [60, 100000, 8192],
  learningRetryAttempts: [1, 5, 3],
  learningRetryBaseMs: [100, 30000, 1000],
} as const

test('Learning settings defaults, numeric boundaries and rejected writes preserve stored configuration', async () => {
  const store = new Store(':memory:'),
    engine = new Engine(store)
  const app = createApp(store, { engine, token: 'settings-fixture-only' })
  const put = (value: unknown, auth = true) =>
    app.request('/api/settings', {
      method: 'PUT',
      headers: auth ? headers : { 'Content-Type': 'application/json' },
      body: JSON.stringify(value),
    })
  try {
    const initial = structuredClone(store.settings)
    assert.equal((await put({ ...initial, name: 'unauthorized' }, false)).status, 401)
    assert.deepEqual(store.settings, initial)
    for (const [key, [minimum, maximum, defaultValue]] of Object.entries(limits)) {
      assert.equal(Reflect.get(initial, key), defaultValue)
      for (const value of [minimum, maximum]) {
        assert.equal((await put({ ...store.settings, [key]: value })).status, 200)
        assert.equal(Reflect.get(store.settings, key), value)
      }
      for (const value of [minimum - 1, maximum + 1, minimum + 0.5, String(minimum), null]) {
        const before = structuredClone(store.settings)
        assert.equal((await put({ ...before, [key]: value })).status, 400)
        assert.deepEqual(store.settings, before)
      }
    }
    const before = structuredClone(store.settings)
    assert.equal((await put({ ...before, untrustedExtraField: true })).status, 400)
    assert.deepEqual(store.settings, before)
    const withoutDefaults: Record<string, unknown> = { ...initial }
    for (const key of Object.keys(limits)) delete withoutDefaults[key]
    assert.equal((await put(withoutDefaults)).status, 200)
    for (const [key, [, , value]] of Object.entries(limits))
      assert.equal(Reflect.get(store.settings, key), value)
  } finally {
    await engine.dispose()
    store.close()
  }
})

test('Learning queue metadata and explicit failure actions require authentication and do not replay automatically', async () => {
  const store = new Store(':memory:'),
    engine = new Engine(store)
  const app = createApp(store, { engine, token: 'settings-fixture-only' })
  const calls: Array<[string, string]> = []
  engine.retryLearning = (id) => {
    calls.push(['retry', id])
    return { ok: id === 'retryable' }
  }
  engine.discardLearning = (id) => {
    calls.push(['discard', id])
    return { ok: id === 'discardable' }
  }
  try {
    assert.equal((await app.request('/api/learning/status')).status, 401)
    const statusResponse = await app.request('/api/learning/status', { headers })
    assert.equal(statusResponse.status, 200)
    const status = await statusResponse.json()
    assert.deepEqual(status.failures, [])
    for (const key of [
      'accepted',
      'completed',
      'failed',
      'overflow',
      'retried',
      'paused',
      'queued',
      'retrying',
      'active',
    ])
      assert.equal(status[key], 0)
    for (const [method, path] of [
      ['POST', '/api/learning/failures/retryable/retry'],
      ['DELETE', '/api/learning/failures/discardable'],
    ] as const) {
      assert.equal((await app.request(path, { method })).status, 401)
    }
    assert.deepEqual(calls, [])
    assert.equal(
      (await app.request('/api/learning/failures/retryable/retry', { method: 'POST', headers }))
        .status,
      200,
    )
    assert.equal(
      (await app.request('/api/learning/failures/partial/retry', { method: 'POST', headers }))
        .status,
      400,
    )
    assert.equal(
      (await app.request('/api/learning/failures/discardable', { method: 'DELETE', headers }))
        .status,
      200,
    )
    assert.equal(
      (await app.request('/api/learning/failures/missing', { method: 'DELETE', headers })).status,
      400,
    )
    assert.deepEqual(calls, [
      ['retry', 'retryable'],
      ['retry', 'partial'],
      ['discard', 'discardable'],
      ['discard', 'missing'],
    ])
  } finally {
    await engine.dispose()
    store.close()
  }
})
