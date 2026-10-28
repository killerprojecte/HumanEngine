import test from 'node:test'
import assert from 'node:assert/strict'
import { createServer } from 'node:http'
import {
  Router,
  normalizeProviderUsage,
  type Usage,
  type RouterConfig,
  type ModelLog,
} from '../src/models.ts'
import { ModelProtocolRegistry } from '../src/model-protocols.ts'

const ordinary = { prompt_tokens: 100, completion_tokens: 20, total_tokens: 120 }
const partition = (usage: Usage) => [
  usage.inputTokens,
  usage.ordinaryInputTokens,
  usage.cacheReadInputTokens,
  usage.cacheWriteInputTokens,
  usage.outputTokens,
  usage.totalTokens,
]

test('OpenAI Chat and Responses counts include cached read and write once with distinct TTLs', () => {
  const chat = normalizeProviderUsage({
    ...ordinary,
    prompt_tokens_details: { cached_tokens: 60, cache_write_tokens: 30 },
  })
  assert.deepEqual(partition(chat), [100, 10, 60, 30, 20, 120])
  assert.equal(chat.cacheKnown, true)
  const response = normalizeProviderUsage({
    input_tokens: 100,
    output_tokens: 20,
    total_tokens: 120,
    input_tokens_details: {
      cached_tokens: 60,
      cache_write_tokens: 30,
      cache_write_tokens_details: { ttl_30m_tokens: 10, ttl_12h_tokens: 20 },
    },
  })
  assert.deepEqual(partition(response), partition(chat))
  assert.equal(response.cacheWrite30mInputTokens, 10)
  assert.equal(response.cacheWrite12hInputTokens, 20)
  assert.equal(response.cacheWrite5mInputTokens, undefined)
  assert.equal(
    response.rawTokenCounters?.['input_tokens_details.cache_write_tokens_details.ttl_12h_tokens'],
    20,
  )
})

test('Anthropic exclusive ordinary input is explicitly re-totalized, creation details are not added twice', () => {
  const raw = {
    input_tokens: 10,
    output_tokens: 20,
    cache_read_input_tokens: 60,
    cache_creation_input_tokens: 30,
    cache_creation: { ephemeral_5m_input_tokens: 10, ephemeral_1h_input_tokens: 20 },
  }
  const normalized = normalizeProviderUsage(raw, 'anthropic')
  assert.deepEqual(partition(normalized), [100, 10, 60, 30, 20, 120])
  assert.equal(normalized.cacheKnown, true)
  assert.equal(normalized.cacheWrite5mInputTokens, 10)
  assert.equal(normalized.cacheWrite1hInputTokens, 20)
  assert.equal(normalized.rawTokenCounters?.input_tokens, 10)
  assert.equal(
    normalizeProviderUsage(raw).known,
    false,
    'never guess exclusive convention on a compatible endpoint',
  )
})

test('DeepSeek hits are read input and misses ordinary input, not cache writes', () => {
  const normalized = normalizeProviderUsage({
    ...ordinary,
    prompt_cache_hit_tokens: 60,
    prompt_cache_miss_tokens: 40,
    prompt_tokens_details: { cached_tokens: 60 },
  })
  assert.deepEqual(partition(normalized), [100, 40, 60, 0, 20, 120])
  assert.equal(normalized.cacheKnown, true)
})

test('unknown and explicit-zero cache counters remain different, and raw metadata is numeric-only', () => {
  const absent = normalizeProviderUsage(ordinary)
  assert.equal(absent.cacheKnown, undefined)
  assert.equal(absent.cacheReadInputTokens, undefined)
  assert.equal(absent.ordinaryInputTokens, undefined)
  const partial = normalizeProviderUsage({
    ...ordinary,
    prompt_tokens_details: { cached_tokens: 60 },
  })
  assert.equal(partial.cacheKnown, false)
  assert.equal(partial.cacheWriteInputTokens, undefined)
  const zero = normalizeProviderUsage({
    ...ordinary,
    prompt_tokens_details: {
      cached_tokens: 0,
      cache_write_tokens: 0,
      private: 'PRIVATE_RAW_CANARY',
    },
    secret: 'PRIVATE_RAW_CANARY',
  })
  assert.equal(zero.cacheKnown, true)
  assert.deepEqual(partition(zero), [100, 100, 0, 0, 20, 120])
  assert.equal(JSON.stringify(zero).includes('PRIVATE_RAW_CANARY'), false)
  assert.ok(Object.keys(zero.rawTokenCounters!).length <= 19)
})

test('malformed, overlapping, conflicting and overflowing native cache counts are unpriceable', () => {
  for (const details of [
    { cached_tokens: -1, cache_write_tokens: 0 },
    { cached_tokens: 1.2, cache_write_tokens: 0 },
    { cached_tokens: '20', cache_write_tokens: 0 },
    { cached_tokens: 90, cache_write_tokens: 20 },
    {
      cached_tokens: 20,
      cache_write_tokens: 10,
      cache_write_tokens_details: { ttl_30m_tokens: 15 },
    },
  ]) {
    const usage = normalizeProviderUsage({ ...ordinary, prompt_tokens_details: details })
    assert.equal(usage.known, false)
    assert.equal(usage.cacheKnown, false)
    assert.equal(usage.ordinaryInputTokens, undefined)
  }
  assert.equal(
    normalizeProviderUsage({
      ...ordinary,
      prompt_cache_hit_tokens: 30,
      prompt_cache_miss_tokens: 80,
    }).known,
    false,
  )
  assert.equal(
    normalizeProviderUsage({
      ...ordinary,
      prompt_tokens_details: { cached_tokens: 20, cache_write_tokens: 0 },
      prompt_cache_hit_tokens: 40,
    }).known,
    false,
  )
  assert.equal(
    normalizeProviderUsage({ input_tokens: Number.MAX_SAFE_INTEGER, output_tokens: 1 }).known,
    false,
  )
  assert.equal(
    normalizeProviderUsage(
      {
        input_tokens: 10,
        output_tokens: 1,
        cache_read_input_tokens: 10,
        cache_creation_input_tokens: 5,
        cache_creation: { ephemeral_5m_input_tokens: 3, ephemeral_1h_input_tokens: 3 },
      },
      'anthropic',
    ).known,
    false,
  )
})

const config = (protocol: string, baseUrl = 'https://example.invalid'): RouterConfig => ({
  providers: [
    {
      id: 'p',
      name: 'Fixture',
      protocol,
      baseUrl,
      credentialEnv: '',
      retryCount: 0,
      timeoutMs: 1000,
    },
  ],
  models: [
    {
      id: 'm',
      displayName: 'Fixture',
      providerId: 'p',
      apiModelName: 'original-model',
      capabilities: ['text', 'tools', 'embedding'],
      embeddingBatchSize: 1,
      inputUsdPerMillion: 2,
      outputUsdPerMillion: 3,
      cacheReadUsdPerMillion: 0.2,
      cacheWriteUsdPerMillion: 4,
    },
  ],
  routes: [{ role: 'replyer', orderedModelIds: ['m'], timeoutMs: 2000 }],
})

test('actual Chat/Responses HTTP parsing preserves cache counters and frozen request prices', async () => {
  let release!: () => void, entered!: () => void
  const pending = new Promise<void>((resolve) => {
      release = resolve
    }),
    started = new Promise<void>((resolve) => {
      entered = resolve
    })
  const server = createServer(async (req, res) => {
    for await (const _chunk of req) {
      /* Drain fixed local request. */
    }
    entered()
    await pending
    res.setHeader('Content-Type', 'application/json')
    res.end(
      JSON.stringify(
        req.url?.endsWith('/responses')
          ? {
              status: 'completed',
              output: [
                {
                  type: 'message',
                  status: 'completed',
                  content: [{ type: 'output_text', text: 'Fixture' }],
                },
              ],
              usage: {
                input_tokens: 100,
                output_tokens: 20,
                input_tokens_details: { cached_tokens: 60, cache_write_tokens: 30 },
              },
            }
          : {
              choices: [
                { finish_reason: 'stop', message: { role: 'assistant', content: 'Fixture' } },
              ],
              usage: {
                ...ordinary,
                prompt_tokens_details: { cached_tokens: 60, cache_write_tokens: 30 },
              },
            },
      ),
    )
  })
  await new Promise<void>((resolve) => server.listen(0, '127.0.0.1', resolve))
  const url = `http://127.0.0.1:${(server.address() as { port: number }).port}`
  try {
    for (const protocol of ['openai-chat', 'openai-responses']) {
      const original = config(protocol, url),
        logs: ModelLog[] = []
      const router = new Router(
        original,
        (entry) => logs.push(entry),
        undefined,
        undefined,
        () => 'fixture-only',
      )
      const running = router.complete('replyer', [{ role: 'user', content: 'Fixture request' }])
      await started
      original.models[0]!.cacheReadUsdPerMillion = 999
      original.models[0]!.apiModelName = 'changed-during-request'
      release()
      const completed = await running
      assert.deepEqual(partition(completed.usage), [100, 10, 60, 30, 20, 120])
      assert.equal(logs[0]!.pricing?.cacheReadUsdPerMillion, 0.2)
      assert.equal(logs[0]!.pricing?.apiModelName, 'original-model')
      assert.equal(logs[0]!.pricing?.cacheWrite5mUsdPerMillion, undefined)
    }
  } finally {
    release()
    server.closeAllConnections()
    await new Promise<void>((resolve) => server.close(() => resolve()))
  }
})

test('custom protocol retains normalized cache usage, derives ordinary input, and rejects malformed partitions', async () => {
  const registry = new ModelProtocolRegistry(),
    lifecycle = new AbortController()
  let usage: Usage = {
    inputTokens: 100,
    outputTokens: 20,
    totalTokens: 120,
    cacheKnown: true,
    cacheReadInputTokens: 60,
    cacheWriteInputTokens: 30,
    cacheWrite5mInputTokens: 10,
    cacheWrite1hInputTokens: 20,
  }
  registry.register(
    'fixture:cache',
    'Cache fixture',
    { complete: async () => ({ text: 'Fixture', toolCalls: [], usage }) },
    lifecycle.signal,
  )
  const router = new Router(config('fixture:cache'), undefined, undefined, registry)
  try {
    const completed = await router.complete('replyer', [{ role: 'user', content: 'Fixture' }])
    assert.deepEqual(partition(completed.usage), [100, 10, 60, 30, 20, 120])
    for (const invalid of [
      { ...usage, cacheReadInputTokens: -1 },
      { ...usage, cacheWriteInputTokens: undefined },
      { ...usage, cacheWrite5mInputTokens: 40 },
      { ...usage, ordinaryInputTokens: 11 },
      { ...usage, rawTokenCounters: { PRIVATE_RAW_CANARY: 1 } },
    ]) {
      usage = invalid as Usage
      await assert.rejects(
        router.complete('replyer', [{ role: 'user', content: 'Fixture' }]),
        /all_models_failed/,
      )
    }
  } finally {
    lifecycle.abort()
  }
})

test('embedding batches sum cache partitions only when all batches report them, with per-batch accounting', async () => {
  const registry = new ModelProtocolRegistry(),
    lifecycle = new AbortController(),
    logs: ModelLog[] = []
  let calls = 0,
    omitSecond = false
  registry.register(
    'fixture:cache',
    'Cache fixture',
    {
      complete: async () => ({
        text: '',
        toolCalls: [],
        usage: { inputTokens: 0, outputTokens: 0, totalTokens: 0 },
      }),
      embeddings: async () => ({
        vectors: [[0.1, 0.9]],
        usage:
          ++calls === 2 && omitSecond
            ? { inputTokens: 10, outputTokens: 0, totalTokens: 10 }
            : {
                inputTokens: 10,
                outputTokens: 0,
                totalTokens: 10,
                cacheKnown: true,
                cacheReadInputTokens: 6,
                cacheWriteInputTokens: 2,
              },
      }),
    },
    lifecycle.signal,
  )
  const router = new Router(
    config('fixture:cache'),
    (entry) => logs.push(entry),
    undefined,
    registry,
  )
  try {
    const complete = await router.embeddings('m', ['one', 'two'])
    assert.deepEqual(partition(complete.usage), [20, 4, 12, 4, 0, 20])
    assert.equal(complete.usage.cacheKnown, true)
    assert.equal(logs.length, 2)
    calls = 0
    omitSecond = true
    const partial = await router.embeddings('m', ['one', 'two'])
    assert.equal(partial.usage.cacheKnown, false)
    assert.equal(partial.usage.cacheReadInputTokens, undefined)
    assert.equal(partial.usage.inputTokens, 20)
  } finally {
    lifecycle.abort()
  }
})

test('native embeddings documented prompt-only usage has an ordinary input partition, unlike unknown custom usages', () => {
  const native = normalizeProviderUsage({ prompt_tokens: 100, total_tokens: 100 }, 'openai', true)
  assert.deepEqual(partition(native), [100, 100, 0, 0, 0, 100])
  assert.equal(native.cacheKnown, true)
  assert.equal(normalizeProviderUsage({ prompt_tokens: 100 }, 'openai').cacheKnown, undefined)
  assert.equal(
    normalizeProviderUsage({ prompt_tokens: 100, total_tokens: 101 }, 'openai', true).known,
    false,
  )
})

test('both native OpenAI lifetime counters must exactly partition reported writes', () => {
  const value = normalizeProviderUsage({
    input_tokens: 12000,
    output_tokens: 1,
    input_tokens_details: {
      cached_tokens: 4000,
      cache_write_tokens: 7000,
      cache_write_tokens_details: { ttl_30m_tokens: 3000, ttl_12h_tokens: 3000 },
    },
  })
  assert.equal(value.known, false)
  assert.equal(value.cacheKnown, false)
  assert.equal(value.rawTokenCounters?.['input_tokens_details.cache_write_tokens'], 7000)
})
