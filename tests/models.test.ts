import assert from 'node:assert/strict'
import { createServer, type IncomingMessage, type ServerResponse } from 'node:http'
import { once } from 'node:events'
import test from 'node:test'
import { ModelProtocolRegistry } from '../src/model-protocols.ts'
import {
  Router,
  type ChatMessage,
  type ModelLog,
  type Provider,
  type RouterConfig,
  type ToolDefinition,
} from '../src/models.ts'

const tool: ToolDefinition = {
  name: 'messages.send',
  description: 'Send an approved message',
  inputSchema: {
    type: 'object',
    properties: { text: { type: 'string' } },
    required: ['text'],
    additionalProperties: false,
  },
}
async function fixture(
  handler: (
    body: Record<string, unknown>,
    req: IncomingMessage,
    res: ServerResponse,
  ) => void | Promise<void>,
) {
  const server = createServer(async (req, res) => {
    const chunks: Buffer[] = []
    for await (const chunk of req) chunks.push(Buffer.from(chunk))
    const body = chunks.length ? JSON.parse(Buffer.concat(chunks).toString()) : {}
    res.setHeader('content-type', 'application/json')
    try {
      await handler(body, req, res)
    } catch {
      res.statusCode = 500
      res.end('{}')
    }
  })
  server.listen(0, '127.0.0.1')
  await once(server, 'listening')
  const address = server.address()
  if (!address || typeof address === 'string') throw new Error('No fixture address')
  return {
    url: `http://127.0.0.1:${address.port}/v1`,
    close: async () => {
      server.closeAllConnections()
      await new Promise<void>((resolve) => server.close(() => resolve()))
    },
  }
}
function config(baseUrl: string, protocol: Provider['protocol']): RouterConfig {
  return {
    providers: [
      {
        id: 'p',
        name: 'Fixture',
        protocol,
        baseUrl,
        credentialEnv: '',
        retryCount: 0,
        timeoutMs: 10000,
      },
    ],
    models: [
      {
        id: 'a',
        displayName: 'A',
        providerId: 'p',
        apiModelName: 'same-model',
        capabilities: ['text', 'tools', 'vision', 'embedding'],
      },
    ],
    // Successful local protocol fixtures have a realistic bounded startup budget.
    // Provider timeouts are independent from the role stage budget.
    routes: [{ role: 'thinker', orderedModelIds: ['a'], timeoutMs: 10000 }],
  }
}
const chatText = {
  choices: [{ finish_reason: 'stop', message: { content: 'Finished' } }],
  usage: { prompt_tokens: 8, completion_tokens: 3, total_tokens: 11 },
}

test('Chat Completions roundtrip keeps tool IDs, maps dotted names, and never sends final text automatically', async () => {
  const requests: Array<{ body: Record<string, unknown>; path: string | undefined }> = []
  const service = await fixture((body, req, res) => {
    requests.push({ body, path: req.url })
    res.end(
      JSON.stringify(
        requests.length === 1
          ? {
              choices: [
                {
                  finish_reason: 'tool_calls',
                  message: {
                    content: null,
                    tool_calls: [
                      {
                        id: 'call-1',
                        type: 'function',
                        function: { name: 'messages__send', arguments: '{"text":"Hello"}' },
                      },
                    ],
                  },
                },
              ],
            }
          : chatText,
      ),
    )
  })
  try {
    const router = new Router(config(service.url, 'openai-chat'))
    const messages: ChatMessage[] = [{ role: 'user', content: 'Hello' }]
    const first = await router.complete('thinker', messages, [tool])
    assert.deepEqual(first.toolCalls, [
      { id: 'call-1', name: 'messages.send', arguments: { text: 'Hello' } },
    ])
    assert.equal(requests[0]?.path, '/v1/chat/completions')
    assert.equal(requests[0]?.body.stream, false)
    messages.push(first.assistant, {
      role: 'tool',
      toolCallId: 'call-1',
      content: '{"status":"sent"}',
    })
    const second = await router.complete('thinker', messages, [tool])
    assert.equal(second.text, 'Finished')
    assert.deepEqual(second.usage, { inputTokens: 8, outputTokens: 3, totalTokens: 11 })
    const body = requests[1]?.body as { messages: Array<Record<string, unknown>> }
    assert.deepEqual(body.messages[2], {
      role: 'tool',
      tool_call_id: 'call-1',
      content: '{"status":"sent"}',
    })
    assert.equal(requests.length, 2)
  } finally {
    await service.close()
  }
})

test('Responses roundtrip preserves opaque same-instance context and function_call_output', async () => {
  const requests: Record<string, unknown>[] = []
  const reasoning = {
    type: 'reasoning',
    id: 'rs-1',
    summary: [],
    encrypted_content: 'opaque-fixture',
  }
  const functionCall = {
    type: 'function_call',
    id: 'fc-1',
    call_id: 'call-2',
    name: 'messages__send',
    arguments: '{"text":"Hello"}',
    status: 'completed',
  }
  const service = await fixture((body, _req, res) => {
    requests.push(body)
    res.end(
      JSON.stringify({
        status: 'completed',
        output:
          requests.length === 1
            ? [reasoning, functionCall]
            : [
                {
                  type: 'message',
                  status: 'completed',
                  content: [{ type: 'output_text', text: 'Done' }],
                },
              ],
        usage: { input_tokens: 4, output_tokens: 2 },
      }),
    )
  })
  try {
    const router = new Router(config(service.url, 'openai-responses'))
    const messages: ChatMessage[] = [
      { role: 'user', content: 'Hello', images: ['data:image/png;base64,AA=='] },
    ]
    const first = await router.complete('thinker', messages, [tool])
    assert.equal(JSON.stringify(first).includes('opaque-fixture'), false)
    messages.push(first.assistant, {
      role: 'tool',
      toolCallId: 'call-2',
      content: '{"status":"sent"}',
    })
    assert.equal((await router.complete('thinker', messages, [tool])).text, 'Done')
    assert.equal(requests[0]?.store, false)
    assert.deepEqual((requests[1]?.input as unknown[]).slice(1), [
      reasoning,
      functionCall,
      { type: 'function_call_output', call_id: 'call-2', output: '{"status":"sent"}' },
    ])
  } finally {
    await service.close()
  }
})

test('Ordered independent instances fallback safely without leaking credentials or accepting partial calls', async () => {
  const paths: Array<string | undefined> = []
  const logs: ModelLog[] = []
  const service = await fixture((_body, req, res) => {
    paths.push(req.url)
    if (req.url?.includes('/bad/')) {
      res.statusCode = 429
      res.end('{"error":"secret-fixture-key"}')
      return
    }
    if (req.url?.includes('/partial/')) {
      res.end(
        JSON.stringify({
          choices: [
            {
              finish_reason: 'length',
              message: {
                tool_calls: [{ id: 'bad', function: { name: 'messages__send', arguments: '{' } }],
              },
            },
          ],
        }),
      )
      return
    }
    res.end(JSON.stringify(chatText))
  })
  process.env.HUMANENGINE_FIXTURE_KEY = 'secret-fixture-key'
  try {
    const cfg = config(`${service.url}/bad`, 'openai-chat')
    cfg.providers[0]!.credentialEnv = 'HUMANENGINE_FIXTURE_KEY'
    cfg.providers.push(
      { ...cfg.providers[0]!, id: 'partial', baseUrl: `${service.url}/partial` },
      { ...cfg.providers[0]!, id: 'good', baseUrl: `${service.url}/good` },
    )
    cfg.models.push(
      { ...cfg.models[0]!, id: 'b', providerId: 'partial' },
      { ...cfg.models[0]!, id: 'c', providerId: 'good' },
    )
    cfg.routes[0]!.orderedModelIds = ['a', 'b', 'c']
    const router = new Router(cfg, (entry) => logs.push(entry))
    const result = await router.complete('thinker', [{ role: 'user', content: 'Hi' }], [tool])
    assert.equal(result.modelId, 'c')
    assert.deepEqual(paths, [
      '/v1/bad/chat/completions',
      '/v1/partial/chat/completions',
      '/v1/good/chat/completions',
    ])
    assert.deepEqual(logs.map((log) => log.code).filter(Boolean), [
      'provider_http_429',
      'incomplete_model_response',
    ])
    assert.equal(JSON.stringify(logs).includes('secret-fixture-key'), false)
    assert.deepEqual(result.toolCalls, [])
  } finally {
    delete process.env.HUMANENGINE_FIXTURE_KEY
    await service.close()
  }
})

test('Cancellation stops fallback, timeout is bounded, embedding space stays on the selected instance', async () => {
  const paths: Array<string | undefined> = []
  let requestSeen!: () => void
  const started = new Promise<void>((resolve) => {
    requestSeen = resolve
  })
  const service = await fixture((body, req, res) => {
    paths.push(req.url)
    if (req.url?.endsWith('/embeddings')) {
      res.end(
        JSON.stringify({
          data: [
            { index: 1, embedding: [0, 1] },
            { index: 0, embedding: [1, 0] },
          ],
          usage: { prompt_tokens: 2 },
        }),
      )
      return
    }
    if (req.url?.endsWith('/models')) {
      res.end('{"data":[{"id":"z"},{"id":"a"},{"id":"a"}]}')
      return
    }
    assert.equal(body.stream, false)
    requestSeen()
  })
  try {
    const cfg = config(service.url, 'openai-chat')
    cfg.models.push({ ...cfg.models[0]!, id: 'b' })
    cfg.routes[0]!.orderedModelIds = ['a', 'b']
    const router = new Router(cfg)
    const controller = new AbortController()
    const pending = router.complete(
      'thinker',
      [{ role: 'user', content: 'Hi' }],
      [],
      controller.signal,
    )
    // Abort only after the actual server received the first request, never a sleep race.
    await Promise.race([started, pending])
    controller.abort()
    await assert.rejects(pending, { message: 'request_cancelled' })
    assert.equal(paths.length, 1)
    cfg.providers[0]!.timeoutMs = 100
    cfg.routes[0]!.orderedModelIds = ['a']
    const logs: ModelLog[] = []
    const short = new Router(cfg, (entry) => logs.push(entry))
    await assert.rejects(short.complete('thinker', [{ role: 'user', content: 'Hi' }]), {
      message: 'all_models_failed',
    })
    assert.equal(logs[0]?.code, 'provider_timeout')
    const embedded = await router.embeddings('b', ['one', 'two'])
    assert.equal(embedded.modelId, 'b')
    assert.deepEqual(embedded.vectors, [
      [1, 0],
      [0, 1],
    ])
    assert.deepEqual(await router.listModels('p'), ['a', 'z'])
  } finally {
    await service.close()
  }
})

test('Embedding batches default to four, preserve ordering, sum usage, and honor configuration', async () => {
  const batches: string[][] = []
  const service = await fixture((body, _req, res) => {
    const input = body.input as string[]
    batches.push(input)
    res.end(
      JSON.stringify({
        data: input.map((text, index) => ({ index, embedding: [Number(text), 1] })).reverse(),
        usage: { prompt_tokens: input.length, total_tokens: input.length },
      }),
    )
  })
  try {
    const cfg = config(service.url, 'openai-chat')
    const input = Array.from({ length: 9 }, (_, index) => String(index))
    const result = await new Router(cfg).embeddings('a', input)
    assert.deepEqual(
      batches.map((batch) => batch.length),
      [4, 4, 1],
    )
    assert.deepEqual(
      result.vectors.map((vector) => vector[0]),
      input.map(Number),
    )
    assert.deepEqual(result.usage, { inputTokens: 9, outputTokens: 0, totalTokens: 9 })
    batches.length = 0
    cfg.models[0]!.embeddingBatchSize = 2
    await new Router(cfg).embeddings('a', input.slice(0, 5))
    assert.deepEqual(
      batches.map((batch) => batch.length),
      [2, 2, 1],
    )
    batches.length = 0
    await new Router(cfg).embeddings('a', [])
    assert.equal(batches.length, 0)
    cfg.models[0]!.embeddingBatchSize = 0
    await assert.rejects(new Router(cfg).embeddings('a', ['one']), {
      message: 'invalid_embedding_batch_size',
    })
    assert.equal(batches.length, 0)
  } finally {
    await service.close()
  }
})

test('Embedding batches stop after failure or cancellation and reject cross-batch dimension drift', async () => {
  let count = 0
  let mode = 'failure'
  const controller = new AbortController()
  const service = await fixture((body, _req, res) => {
    count++
    if (mode === 'failure' && count === 2) {
      res.statusCode = 503
      res.end('{}')
      return
    }
    if (mode === 'cancel') controller.abort()
    const input = body.input as string[]
    res.end(
      JSON.stringify({
        data: input.map((_, index) => ({
          index,
          embedding: mode === 'dimensions' && count === 2 ? [1, 2, 3] : [1, 2],
        })),
      }),
    )
  })
  try {
    const cfg = config(service.url, 'openai-chat')
    const inputs = Array.from({ length: 10 }, () => 'synthetic')
    await assert.rejects(new Router(cfg).embeddings('a', inputs), { message: 'provider_http_503' })
    assert.equal(count, 2)
    count = 0
    mode = 'dimensions'
    await assert.rejects(new Router(cfg).embeddings('a', inputs), {
      message: 'invalid_embedding_dimensions',
    })
    assert.equal(count, 2)
    count = 0
    mode = 'cancel'
    await assert.rejects(new Router(cfg).embeddings('a', inputs, controller.signal), {
      message: 'request_cancelled',
    })
    assert.equal(count, 1)
    count = 0
    mode = 'unknown-usage'
    const result = await new Router(cfg).embeddings('a', inputs)
    assert.equal(result.usage.known, false)
  } finally {
    await service.close()
  }
})

test('Explicit dimensions use the wire contract; rejected outputs retain exactly one paid usage event', async () => {
  const requests: Record<string, unknown>[] = [],
    logs: ModelLog[] = []
  let mode: 'normal' | 'ignored' | 'zero' | 'overflow' = 'normal'
  const service = await fixture((body, _req, res) => {
    requests.push(body)
    const width = mode === 'ignored' ? 1024 : Number(body.dimensions ?? 1024)
    res.end(
      JSON.stringify({
        data: (body.input as string[]).map((_, index) => ({
          index,
          embedding: Array.from({ length: width }, (_v, n) =>
            mode === 'zero' ? 0 : n === 0 ? (mode === 'overflow' ? 1e100 : 1) : 0,
          ),
        })),
        usage: { prompt_tokens: 7, total_tokens: 7 },
      }),
    )
  })
  try {
    const cfg = config(service.url, 'openai-chat')
    const run = () => new Router(cfg, (entry) => logs.push(entry)).embeddings('a', ['synthetic'])
    assert.equal((await run()).vectors[0]?.length, 1024)
    assert.equal(Object.hasOwn(requests[0]!, 'dimensions'), false)
    for (const width of [1024, 4096]) {
      cfg.models[0]!.embeddingDimensions = width
      assert.equal((await run()).vectors[0]?.length, width)
      assert.equal(requests.at(-1)?.dimensions, width)
    }
    for (const [nextMode, code] of [
      ['ignored', 'embedding_dimensions_mismatch'],
      ['zero', 'invalid_embedding_vector'],
      ['overflow', 'invalid_embedding_vector'],
    ] as const) {
      mode = nextMode
      const before = requests.length,
        eventsBefore = logs.length
      await assert.rejects(run(), { message: code })
      assert.equal(requests.length, before + 1)
      assert.equal(logs.length, eventsBefore + 1)
      assert.equal(logs.at(-1)?.event, 'model.completed')
      assert.equal(logs.at(-1)?.usage?.totalTokens, 7)
      assert.notEqual(logs.at(-1)?.usage?.known, false)
    }
    assert.equal(logs.filter((entry) => entry.event === 'model.failed').length, 0)
    assert.equal(
      logs.reduce((sum, entry) => sum + (entry.usage?.totalTokens ?? 0), 0),
      42,
    )
    for (const invalid of [0, -1, 1.5, 16385]) {
      cfg.models[0]!.embeddingDimensions = invalid
      await assert.rejects(run(), { message: 'invalid_embedding_dimensions_setting' })
    }
    assert.equal(requests.length, 6)
    assert.equal(logs.length, 6)
  } finally {
    await service.close()
  }
})

test('A later invalid embedding batch keeps earlier known usage and prevents the third request', async () => {
  let count = 0
  const logs: ModelLog[] = []
  const service = await fixture((_body, _req, res) => {
    count++
    res.end(
      JSON.stringify({
        data: [{ index: 0, embedding: count === 2 ? [0, 0] : [1, 0] }],
        usage: { prompt_tokens: count, total_tokens: count },
      }),
    )
  })
  try {
    const cfg = config(service.url, 'openai-chat')
    cfg.models[0]!.embeddingBatchSize = 1
    await assert.rejects(
      new Router(cfg, (entry) => logs.push(entry)).embeddings('a', ['one', 'two', 'three']),
      { message: 'invalid_embedding_vector' },
    )
    assert.equal(count, 2)
    assert.deepEqual(
      logs.map((entry) => [entry.event, entry.usage?.totalTokens]),
      [
        ['model.completed', 1],
        ['model.completed', 2],
      ],
    )
  } finally {
    await service.close()
  }
})

test('Custom embedding protocols preserve accounting when strict output validation rejects a response', async () => {
  const registry = new ModelProtocolRegistry(),
    controller = new AbortController(),
    logs: ModelLog[] = []
  let calls = 0,
    zero = false
  registry.register(
    'fixture:embedding',
    'Synthetic local protocol',
    {
      complete: async () => {
        throw new Error('not used')
      },
      embeddings: async (context) => {
        calls++
        assert.equal(context.model?.embeddingDimensions, 4096)
        return {
          vectors: [zero ? [0, 0] : [1, 0]],
          usage: { inputTokens: 3, outputTokens: 0, totalTokens: 3 },
        }
      },
    },
    controller.signal,
  )
  const cfg = config('https://example.invalid', 'fixture:embedding')
  cfg.models[0]!.embeddingDimensions = 4096
  try {
    const router = new Router(cfg, (entry) => logs.push(entry), undefined, registry)
    await assert.rejects(router.embeddings('a', ['synthetic']), {
      message: 'embedding_dimensions_mismatch',
    })
    zero = true
    await assert.rejects(router.embeddings('a', ['synthetic']), {
      message: 'invalid_embedding_vector',
    })
    assert.equal(calls, 2)
    assert.deepEqual(
      logs.map((entry) => [entry.event, entry.usage?.totalTokens]),
      [
        ['model.completed', 3],
        ['model.completed', 3],
      ],
    )
  } finally {
    controller.abort()
  }
})
