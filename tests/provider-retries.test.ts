import { mkdtemp, rm } from 'node:fs/promises'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import sharp from 'sharp'
import { Store } from '../src/store.ts'
import { Engine } from '../src/engine.ts'
import { MemeService } from '../src/memes.ts'
import type { RuntimeMessage } from '../src/runtime.ts'
import assert from 'node:assert/strict'
import { once } from 'node:events'
import { createServer, type IncomingMessage, type ServerResponse } from 'node:http'
import test from 'node:test'
import { ModelProtocolError, ModelProtocolRegistry } from '../src/model-protocols.ts'
import { Router, type Provider, type RouterConfig } from '../src/models.ts'
import { providerOperationBudget, providerPolicy } from '../src/models/request-policy.ts'

import type { FlowWriter } from '../src/diagnostics/flow.ts'
import { sanitizeError } from '../src/diagnostics/errors.ts'

const chat = { choices: [{ finish_reason: 'stop', message: { content: 'Fixture reply' } }] }
const responses = {
  status: 'completed',
  output: [
    {
      type: 'message',
      role: 'assistant',
      content: [{ type: 'output_text', text: 'Fixture reply' }],
    },
  ],
}
const usage = { inputTokens: 1, outputTokens: 1, totalTokens: 2 }
async function fixture(
  handler: (body: Record<string, unknown>, req: IncomingMessage, res: ServerResponse) => void,
) {
  const server = createServer(async (req, res) => {
    const chunks: Buffer[] = []
    for await (const chunk of req) chunks.push(Buffer.from(chunk))
    const body = chunks.length ? JSON.parse(Buffer.concat(chunks).toString()) : {}
    res.setHeader('content-type', 'application/json')
    handler(body, req, res)
  })
  server.listen(0, '127.0.0.1')
  await once(server, 'listening')
  const address = server.address()
  if (!address || typeof address === 'string') throw new Error('fixture_address_missing')
  return {
    url: `http://127.0.0.1:${address.port}/v1`,
    close: async () => {
      server.closeAllConnections()
      await new Promise<void>((resolve) => server.close(() => resolve()))
    },
  }
}
function config(
  baseUrl: string,
  protocol = 'openai-chat',
  policy: Partial<Provider> = {},
): RouterConfig {
  return {
    providers: [
      { id: 'fixture', name: 'Fixture', protocol, baseUrl, credentialEnv: '', ...policy },
    ],
    models: [
      {
        id: 'first',
        displayName: 'First',
        providerId: 'fixture',
        apiModelName: 'first',
        capabilities: ['text', 'embedding'],
      },
    ],
    routes: [{ role: 'thinker', orderedModelIds: ['first'], timeoutMs: 10000 }],
  }
}
const complete = (router: Router, signal?: AbortSignal) =>
  router.complete('thinker', [{ role: 'user', content: 'Synthetic fixture' }], [], signal)

test('provider defaults and probe budget include two extra attempts and each independent timeout', () => {
  assert.deepEqual(providerPolicy({}), { retryCount: 2, maxAttempts: 3, timeoutMs: 30000 })
  assert.equal(providerOperationBudget({}), 110000)
  assert.equal(providerOperationBudget({ retryCount: 0, timeoutMs: 100 }), 100)
})

for (const protocol of ['openai-chat', 'openai-responses']) {
  test(`${protocol} retries 429 then 503 on the same model before success`, async () => {
    const requests: string[] = []
    const service = await fixture((body, req, res) => {
      requests.push(`${req.url}:${body.model}`)
      if (requests.length < 3) {
        res.statusCode = requests.length === 1 ? 429 : 503
        res.setHeader('retry-after', '0')
        res.end('{}')
      } else res.end(JSON.stringify(protocol === 'openai-chat' ? chat : responses))
    })
    try {
      const rows: Array<Parameters<FlowWriter['log']>[3]> = []
      const writer: FlowWriter = {
        log(_level, _module, _code, context) {
          rows.push(context)
        },
      }
      const result = await complete(
        new Router(
          config(service.url, protocol, { timeoutMs: 1500 }),
          undefined,
          undefined,
          undefined,
          undefined,
          writer,
        ),
      )
      const attempts = rows.filter(
        (row) => row?.stage === 'provider_attempt' && row.status === 'started',
      )
      assert.deepEqual(
        attempts.map((row) => row?.counts),
        [1, 2, 3].map((attempt) => ({ attempt, maxAttempts: 3, timeoutMs: 1500 })),
      )
      assert.deepEqual(
        rows
          .filter((row) => row?.stage === 'provider_attempt' && row.status !== 'started')
          .map((row) => row?.status),
        ['failed', 'failed', 'completed'],
      )
      assert.equal(result.text, 'Fixture reply')
      const path = protocol === 'openai-chat' ? '/v1/chat/completions' : '/v1/responses'
      assert.deepEqual(requests, Array(3).fill(`${path}:first`))
    } finally {
      await service.close()
    }
  })
}

test('retryCount zero makes one failed request then advances to fallback', async () => {
  const requested: unknown[] = []
  const service = await fixture((body, _req, res) => {
    requested.push(body.model)
    if (body.model === 'first') {
      res.statusCode = 503
      res.end('{}')
    } else res.end(JSON.stringify(chat))
  })
  try {
    const settings = config(service.url, 'openai-chat', { retryCount: 0 })
    settings.models.push({ ...settings.models[0]!, id: 'fallback', apiModelName: 'fallback' })
    settings.routes[0]!.orderedModelIds.push('fallback')
    assert.equal((await complete(new Router(settings))).modelId, 'fallback')
    assert.deepEqual(requested, ['first', 'fallback'])
  } finally {
    await service.close()
  }
})

test('all same-model retry attempts finish before fallback starts', async () => {
  const requested: unknown[] = []
  const service = await fixture((body, _req, res) => {
    requested.push(body.model)
    if (body.model === 'first') {
      res.statusCode = 503
      res.end('{}')
    } else res.end(JSON.stringify(chat))
  })
  try {
    const settings = config(service.url)
    settings.models.push({ ...settings.models[0]!, id: 'fallback', apiModelName: 'fallback' })
    settings.routes[0]!.orderedModelIds.push('fallback')
    assert.equal((await complete(new Router(settings))).modelId, 'fallback')
    assert.deepEqual(requested, ['first', 'first', 'first', 'fallback'])
  } finally {
    await service.close()
  }
})

for (const failure of ['401', '403', 'invalid-json', 'invalid-schema']) {
  test(`${failure} is not retried even with the default retry allowance`, async () => {
    let count = 0
    const service = await fixture((_body, _req, res) => {
      count++
      if (failure === '401' || failure === '403') res.statusCode = Number(failure)
      res.end(failure === 'invalid-json' ? '{' : '{}')
    })
    try {
      await assert.rejects(complete(new Router(config(service.url))))
      assert.equal(count, 1)
    } finally {
      await service.close()
    }
  })
}

for (const operation of ['embedding', 'discovery'] as const) {
  test(`${operation} inherits provider retries`, async () => {
    let count = 0
    const service = await fixture((_body, req, res) => {
      count++
      assert.equal(req.url, operation === 'embedding' ? '/v1/embeddings' : '/v1/models')
      if (count < 3) {
        res.statusCode = 503
        res.end('{}')
      } else
        res.end(
          JSON.stringify(
            operation === 'embedding'
              ? { data: [{ index: 0, embedding: [0.2, 0.8] }] }
              : { data: [{ id: 'discovered' }] },
          ),
        )
    })
    try {
      const router = new Router(config(service.url))
      if (operation === 'embedding')
        assert.deepEqual((await router.embeddings('first', ['Synthetic fixture'])).vectors, [
          [0.2, 0.8],
        ])
      else assert.deepEqual(await router.listModels('fixture'), ['discovered'])
      assert.equal(count, 3)
    } finally {
      await service.close()
    }
  })
}

test('aborting during HTTP retry backoff prevents another request', async () => {
  let count = 0
  const controller = new AbortController()
  const service = await fixture((_body, _req, res) => {
    count++
    res.statusCode = 503
    res.end('{}')
    setTimeout(() => controller.abort('fixture_cancelled'), 40)
  })
  try {
    await assert.rejects(complete(new Router(config(service.url)), controller.signal))
    await new Promise((resolve) => setTimeout(resolve, 300))
    assert.equal(count, 1)
  } finally {
    await service.close()
  }
})

test('provider timeout terminates a stalled HTTP attempt without retry when disabled', async () => {
  let count = 0
  const service = await fixture(() => {
    count++
  })
  try {
    const started = Date.now()
    await assert.rejects(
      complete(new Router(config(service.url, 'openai-chat', { retryCount: 0, timeoutMs: 100 }))),
    )
    assert.equal(count, 1)
    assert.ok(
      Date.now() - started < 3000,
      'provider timeout must not inherit the ten-second role budget',
    )
  } finally {
    await service.close()
  }
})

for (const operation of ['complete', 'embeddings', 'listModels'] as const) {
  for (const typed of [true, false]) {
    test(`plugin ${operation} ${typed ? 'retries typed HTTP503' : 'does not blindly retry generic errors'}`, async () => {
      let count = 0
      const attempt = () => {
        count++
        if (!typed || count < 3)
          throw typed
            ? new ModelProtocolError('http', { status: 503, retryAfterMs: 0 })
            : new Error('Synthetic plugin failure')
      }
      const registry = new ModelProtocolRegistry()
      const lifecycle = new AbortController()
      registry.register(
        'fixture:retry',
        'Retry fixture',
        {
          async complete() {
            attempt()
            return { text: 'Fixture reply', toolCalls: [], usage }
          },
          async embeddings() {
            attempt()
            return { vectors: [[0.2, 0.8]], usage }
          },
          async listModels() {
            attempt()
            return ['discovered']
          },
        },
        lifecycle.signal,
      )
      const router = new Router(
        config('http://unused.invalid/v1', 'fixture:retry'),
        undefined,
        undefined,
        registry,
      )
      const result =
        operation === 'complete'
          ? complete(router)
          : operation === 'embeddings'
            ? router.embeddings('first', ['Synthetic fixture'])
            : router.listModels('fixture')
      try {
        if (typed) {
          await result
          assert.equal(count, 3)
        } else {
          await assert.rejects(result)
          assert.equal(count, 1)
        }
      } finally {
        lifecycle.abort()
      }
    })
  }
}

test('exhausted HTTP429 root cause retains the last actual attempt and policy', async () => {
  let count = 0
  const service = await fixture((_body, _req, res) => {
    count++
    res.statusCode = 429
    res.end('{}')
  })
  try {
    await assert.rejects(
      complete(new Router(config(service.url, 'openai-chat', { timeoutMs: 1500 }))),
      (error: unknown) => {
        const diagnostic = sanitizeError(error)
        assert.equal(diagnostic.rootCause?.httpStatus, 429)
        assert.equal(diagnostic.rootCause?.errorContext?.attempt, 3)
        assert.equal(diagnostic.rootCause?.errorContext?.maxAttempts, 3)
        assert.equal(diagnostic.rootCause?.errorContext?.timeoutMs, 1500)
        return true
      },
    )
    assert.equal(count, 3)
  } finally {
    await service.close()
  }
})

for (const role of ['vision', 'learning', 'embedding'] as const) {
  test(`${role} stage budget caps provider attempts and preserves actual attempt context`, async () => {
    let count = 0
    const service = await fixture(() => {
      count++
    })
    try {
      const settings = config(service.url, 'openai-chat', { retryCount: 2, timeoutMs: 1000 })
      settings.models[0]!.capabilities.push('vision', 'tools')
      settings.routes = [{ role, orderedModelIds: ['first'], timeoutMs: 100 }]
      const router = new Router(settings)
      const result =
        role === 'embedding'
          ? router.embeddings('first', ['Synthetic fixture'])
          : router.complete(role, [{ role: 'user', content: 'Synthetic fixture' }])
      await assert.rejects(result, (error: unknown) => {
        const root = sanitizeError(error).rootCause
        assert.equal(root?.errorCode, 'model_stage_timeout')
        assert.equal(root?.errorContext?.modelId, 'first')
        assert.equal(root?.errorContext?.apiModel, 'first')
        assert.equal(root?.errorContext?.attempt, 1)
        assert.equal(root?.errorContext?.timeoutMs, 1000)
        assert.equal(root?.errorContext?.stageBudgetMs, 100)
        return true
      })
      assert.equal(count, 1)
    } finally {
      await service.close()
    }
  })
}

const learningScene = {
  id: 'retry-fixture',
  kind: 'group' as const,
  adapterId: 'fixture',
  accountId: 'bot',
  platformSceneId: 'fixture',
  name: 'Retry fixture',
  participationEnabled: false,
  proactiveEnabled: false,
  learningEnabled: true,
}
function learningConfig(url: string): RouterConfig {
  const settings = config(url)
  settings.models[0]!.capabilities.push('tools', 'vision')
  settings.routes = [
    { role: 'learning', orderedModelIds: ['first'], timeoutMs: 10000 },
    { role: 'vision', orderedModelIds: ['first'], timeoutMs: 10000 },
  ]
  return settings
}

test('Engine learning does not multiply exhausted Router retries into a second retry queue', async () => {
  let count = 0
  const service = await fixture((_body, _req, res) => {
    count++
    res.statusCode = 503
    res.end('{}')
  })
  const store = new Store(':memory:')
  store.saveScene(learningScene)
  store.set('settings', { ...store.settings, learningBatchMs: 300000 })
  const engine = new Engine(store)
  engine.router = () => new Router(learningConfig(service.url))
  const queue = engine.learning as unknown as {
    enqueueLearning(message: RuntimeMessage): void
    learn(sceneId: string): Promise<void>
    learningRetries: Map<string, unknown>
  }
  try {
    queue.enqueueLearning({
      id: 'source',
      sceneId: learningScene.id,
      senderId: 'person',
      origin: 'live',
      status: 'available',
      sentAt: new Date().toISOString(),
      receivedAt: new Date().toISOString(),
      parts: [{ type: 'text', text: 'Synthetic learning fixture' }],
    })
    await queue.learn(learningScene.id)
    assert.equal(count, 3)
    assert.equal(
      queue.learningRetries.size,
      0,
      'Router-owned attempts must not schedule another learning batch retry',
    )
    assert.ok(store.logs.some((row) => row.stage === 'learning.batch' && row.status === 'failed'))
  } finally {
    await engine.dispose()
    store.close()
    await service.close()
  }
})

test('MemeService does not multiply exhausted Router retries into nine requests', async () => {
  let count = 0
  const service = await fixture((_body, _req, res) => {
    count++
    res.statusCode = 503
    res.end('{}')
  })
  const directory = await mkdtemp(join(tmpdir(), 'he-retry-meme-'))
  const store = new Store(':memory:')
  store.saveScene(learningScene)
  const memes = new MemeService(store, directory)
  const source = { sceneId: learningScene.id, messageId: 'source', senderId: 'person' }
  const context = {
    sceneId: learningScene.id,
    simulation: false,
    sourceEvidence: [{ ...source, available: true, isBot: false }],
  }
  try {
    const bytes = await sharp({
      create: { width: 2, height: 2, channels: 3, background: '#ffffff' },
    })
      .png()
      .toBuffer()
    const asset = await memes.import(context, bytes, source)
    await assert.rejects(memes.analyze(context, asset.id, new Router(learningConfig(service.url))))
    assert.equal(count, 3)
    assert.equal(memes.adminGet(asset.id)?.analysisState, 'failed')
    assert.equal(
      memes.adminGet(asset.id)?.analysisAttempts,
      1,
      'MemeService executes one Router-owned attempt group',
    )
  } finally {
    store.close()
    await rm(directory, { recursive: true, force: true })
    await service.close()
  }
})

test('protocol retry contract rejects invalid runtime failure kinds and status', () => {
  assert.throws(
    () => new ModelProtocolError('invalid' as 'network'),
    /invalid_protocol_failure_kind/,
  )
  assert.throws(
    () => new ModelProtocolError('http', { status: 999 }),
    /invalid_protocol_failure_status/,
  )
})
