import assert from 'node:assert/strict'
import { createServer, type IncomingMessage, type ServerResponse } from 'node:http'
import { once } from 'node:events'
import test from 'node:test'
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
    providers: [{ id: 'p', name: 'Fixture', protocol, baseUrl, credentialEnv: '' }],
    models: [
      {
        id: 'a',
        displayName: 'A',
        providerId: 'p',
        apiModelName: 'same-model',
        capabilities: ['text', 'tools', 'vision', 'embedding'],
      },
    ],
    routes: [{ role: 'thinker', orderedModelIds: ['a'], timeoutMs: 1000 }],
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
    setTimeout(() => controller.abort(), 40)
    await assert.rejects(pending, { message: 'request_cancelled' })
    assert.equal(paths.length, 1)
    cfg.routes[0]!.timeoutMs = 20
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
