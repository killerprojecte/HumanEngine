import test from 'node:test'
import assert from 'node:assert/strict'
import { createServer } from 'node:http'
import { once } from 'node:events'
import { Store } from '../src/store.ts'
import { createApp } from '../src/api.ts'
import { Router, type ModelLog } from '../src/models.ts'
import type { ModelCallObservation } from '../src/model-calls.ts'

function observation(overrides: Partial<ModelCallObservation> = {}): ModelCallObservation {
  return {
    event: 'model.completed',
    modelId: 'm',
    providerId: 'p',
    role: 'planner',
    durationMs: 2000,
    at: new Date().toISOString(),
    attempt: 1,
    pricing: { apiModelName: 'api-model' },
    usage: {
      inputTokens: 100,
      outputTokens: 20,
      totalTokens: 120,
      cacheReadInputTokens: 60,
      cacheWriteInputTokens: 0,
    },
    ...overrides,
  }
}
test('call metadata preserves unknown versus zero, bounded keyset pages and privacy', async () => {
  const store = new Store(':memory:')
  try {
    store.recordModelCall(observation())
    store.recordModelCall(observation({ role: 'replyer', usage: undefined, event: 'model.failed' }))
    store.recordModelCall(
      observation({ usage: { inputTokens: 0, outputTokens: 0, totalTokens: 0 }, durationMs: 0 }),
    )
    const first = store.modelCalls({ limit: 2 })
    assert.equal(first.items.length, 2)
    assert.equal(first.items[0]!.outputTokens, 0)
    assert.equal(first.items[0]!.tokensPerSecond, null)
    assert.equal(first.items[0]!.cacheReadInputTokens, null)
    assert.equal(first.items[1]!.inputTokens, null)
    store.recordModelCall(observation())
    const next = store.modelCalls({ limit: 2, beforeId: first.nextCursor! })
    assert.equal(next.items.length, 1)
    assert.equal(next.items[0]!.tokensPerSecond, 10)
    assert.equal(next.items[0]!.cacheWriteInputTokens, 0)
    assert.equal(next.items[0]!.firstTokenMs, null)
    assert.equal(next.nextCursor, null)
    assert.equal(store.modelCalls({ limit: 50, role: 'replyer', status: 'failed' }).items.length, 1)
    store.recordModelCall(
      observation({
        usage: {
          inputTokens: 100,
          outputTokens: 20,
          totalTokens: 120,
          cacheReadInputTokens: 60,
          cacheKnown: false,
        },
      }),
    )
    const partial = store.modelCalls({ limit: 1 }).items[0]!
    assert.equal(partial.cacheReadInputTokens, 60, 'known read survives unknown write partition')
    assert.equal(partial.cacheWriteInputTokens, null)
    assert.equal(store.usage().length, 0, 'independent call logs do not double count billing')
    const app = createApp(store, { token: 'test' })
    assert.equal((await app.request('/api/model-calls')).status, 401)
    const headers = { Authorization: 'Bearer test' }
    for (const q of ['limit=101', 'limit=0', 'beforeId=-1', 'status=anything'])
      assert.equal((await app.request('/api/model-calls?' + q, { headers })).status, 400)
    const response = await app.request('/api/model-calls?modelId=m&limit=1', { headers })
    assert.equal(response.status, 200)
    assert.equal(response.headers.get('cache-control'), 'no-store')
    const body = (await response.json()) as Record<string, unknown>
    assert.equal(body.retentionDays, 30)
    assert.equal(body.maxRecords, 100000)
    const serialized = JSON.stringify(body)
    for (const key of ['messages', 'prompt', 'credential', 'error', 'sceneId'])
      assert.ok(!serialized.includes(`"${key}"`))
  } finally {
    store.close()
  }
})
test('log retention removes expired rows and enforces a bounded monotonically ordered history', () => {
  const store = new Store(':memory:')
  try {
    store.recordModelCall(observation({ at: '2000-01-01T00:00:00.000Z' }))
    assert.equal(store.modelCalls({ limit: 50 }).items.length, 0)
    store.recordModelCall(observation())
    store.db.prepare("UPDATE sqlite_sequence SET seq=200000 WHERE name='model_calls'").run()
    store.recordModelCall(observation())
    assert.equal(store.modelCalls({ limit: 50 }).items.length, 1)
    assert.equal(
      (store.db.prepare('SELECT COUNT(*) AS n FROM model_calls').get() as { n: number }).n,
      1,
    )
  } finally {
    store.close()
  }
})
test('router records each retry separately with no duplicate accounting or sensitive payload', async () => {
  let requests = 0
  const server = createServer(async (req, res) => {
    for await (const _ of req) {
      /* drain */
    }
    res.setHeader('Content-Type', 'application/json')
    if (++requests === 1) {
      res.statusCode = 503
      res.end('{"error":"private error text"}')
      return
    }
    res.end(
      JSON.stringify({
        choices: [
          { finish_reason: 'stop', message: { role: 'assistant', content: 'private reply' } },
        ],
        usage: { prompt_tokens: 20, completion_tokens: 10, total_tokens: 30 },
      }),
    )
  })
  server.listen(0, '127.0.0.1')
  await once(server, 'listening')
  const address = server.address()
  assert.ok(address && typeof address !== 'string')
  const calls: ModelCallObservation[] = [],
    accounting: ModelLog[] = []
  try {
    const router = new Router(
      {
        providers: [
          {
            id: 'p',
            name: 'P',
            protocol: 'openai-chat',
            baseUrl: `http://127.0.0.1:${address.port}/v1`,
            credentialEnv: '',
            retryCount: 1,
            timeoutMs: 10000,
          },
        ],
        models: [
          {
            id: 'm',
            displayName: 'M',
            providerId: 'p',
            apiModelName: 'model',
            capabilities: ['text'],
          },
        ],
        routes: [{ role: 'planner', orderedModelIds: ['m'], timeoutMs: 30000 }],
      },
      (entry) => accounting.push(entry),
      undefined,
      undefined,
      undefined,
      undefined,
      undefined,
      undefined,
      (entry) => {
        calls.push(entry)
        throw new Error('observer failure')
      },
    )
    await router.complete('planner', [{ role: 'user', content: 'private prompt' }])
    assert.equal(requests, 2)
    assert.deepEqual(
      calls.map((c) => [c.event, c.attempt]),
      [
        ['model.failed', 1],
        ['model.completed', 2],
      ],
    )
    assert.equal(calls[0]!.usage, undefined)
    assert.equal(calls[1]!.usage?.outputTokens, 10)
    assert.equal(accounting.length, 1)
    assert.ok(calls.every((c) => c.durationMs >= 0 && Number.isFinite(Date.parse(c.at))))
    assert.ok(!JSON.stringify(calls).includes('private'))
  } finally {
    server.closeAllConnections()
    await new Promise<void>((resolve) => server.close(() => resolve()))
  }
})
