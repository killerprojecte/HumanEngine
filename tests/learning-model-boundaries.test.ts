import assert from 'node:assert/strict'
import { once } from 'node:events'
import { createServer } from 'node:http'
import test from 'node:test'
import { Router, type ModelDiagnostic, type RouterConfig } from '../src/models.ts'
import { ModelProtocolRegistry, ModelProtocolError } from '../src/model-protocols.ts'
import { modelFailureReason } from '../src/diagnostics/model-flow.ts'
import { sanitizeError } from '../src/diagnostics/errors.ts'
import type { FlowWriter } from '../src/diagnostics/flow.ts'

async function fixture(payload: unknown, delayMs = 0) {
  let requests = 0
  const timers = new Set<ReturnType<typeof setTimeout>>()
  const server = createServer(async (req, res) => {
    for await (const _chunk of req) {
      /* drain the synthetic request */
    }
    requests++
    const timer = setTimeout(() => {
      timers.delete(timer)
      res.setHeader('content-type', 'application/json')
      res.end(JSON.stringify(payload))
    }, delayMs)
    timers.add(timer)
  })
  server.listen(0, '127.0.0.1')
  await once(server, 'listening')
  const address = server.address()
  assert.ok(address && typeof address !== 'string')
  return {
    config(protocol = 'openai-chat', providerMs = 1000, stageMs = 2000): RouterConfig {
      return {
        providers: [
          {
            id: 'p',
            name: 'Fixture',
            protocol,
            baseUrl: `http://127.0.0.1:${address.port}`,
            credentialEnv: '',
            timeoutMs: providerMs,
            retryCount: 0,
          },
        ],
        models: [
          {
            id: 'm',
            displayName: 'Fixture',
            providerId: 'p',
            apiModelName: 'fixture',
            capabilities: ['text', 'tools'],
          },
        ],
        routes: [{ role: 'learning', orderedModelIds: ['m'], timeoutMs: stageMs }],
      }
    },
    get requests() {
      return requests
    },
    async close() {
      for (const timer of timers) clearTimeout(timer)
      server.closeAllConnections()
      await new Promise<void>((resolve) => server.close(() => resolve()))
    },
  }
}
const chat = (message: unknown, finish_reason = 'stop') => ({
  choices: [{ finish_reason, message }],
})
const complete = (router: Router, signal?: AbortSignal) =>
  router.complete('learning', [{ role: 'user', content: 'Synthetic casual greeting' }], [], signal)

for (const [name, protocol, payload, expected] of [
  ['explicit silence', 'openai-chat', chat({ content: '' }), 'empty_model_output'],
  ['null silence', 'openai-chat', chat({ content: null }), 'empty_model_output'],
  [
    'chat reasoning only',
    'openai-chat',
    chat({ content: '', reasoning_content: 'private synthetic reasoning' }),
    'reasoning_only_response',
  ],
  [
    'responses silence',
    'openai-responses',
    { status: 'completed', output: [] },
    'empty_model_output',
  ],
  [
    'responses reasoning only',
    'openai-responses',
    {
      status: 'completed',
      output: [{ type: 'reasoning', encrypted_content: 'private synthetic reasoning' }],
    },
    'reasoning_only_response',
  ],
] as const) {
  test(`learning preserves ${name} and exposes bounded completion evidence`, async () => {
    const service = await fixture(payload)
    const diagnostics: ModelDiagnostic[] = []
    const rows: Array<Parameters<FlowWriter['log']>[3]> = []
    const writer: FlowWriter = {
      log(_level, _module, _code, context) {
        rows.push(context)
      },
    }
    try {
      const router = new Router(
        service.config(protocol),
        undefined,
        (entry) => diagnostics.push(entry),
        undefined,
        undefined,
        writer,
      )
      const result = await complete(router)
      assert.equal(result.text, '')
      assert.deepEqual(result.toolCalls, [])
      assert.equal(service.requests, 1)
      assert.ok(
        rows.some(
          (row) =>
            row?.stage === 'model_request' && row.status === 'completed' && row.reason === expected,
        ),
      )
      assert.ok(
        rows.some(
          (row) =>
            row?.stage === 'model_request' &&
            row.status === 'started' &&
            row.counts?.stageBudgetMs === 2000,
        ),
      )
      assert.equal(diagnostics[0]?.output.responseSummary?.protocol, protocol)
      assert.equal(diagnostics[0]?.output.usage?.known, false)
      assert.equal(JSON.stringify(diagnostics).includes('private synthetic reasoning'), false)
    } finally {
      await service.close()
    }
  })
}

for (const [name, protocol, payload, code] of [
  ['missing content', 'openai-chat', chat({}), 'invalid_provider_response'],
  [
    'missing promised tools',
    'openai-chat',
    chat({ content: null, tool_calls: [] }, 'tool_calls'),
    'incomplete_tool_call',
  ],
  [
    'chat refusal',
    'openai-chat',
    chat({ content: null, refusal: 'private refusal' }),
    'model_response_refused',
  ],
  [
    'responses refusal',
    'openai-responses',
    {
      status: 'completed',
      output: [
        {
          type: 'message',
          content: [{ type: 'refusal', refusal: 'private refusal' }],
        },
      ],
    },
    'model_response_refused',
  ],
  [
    'missing responses output',
    'openai-responses',
    { status: 'completed' },
    'invalid_provider_response',
  ],
  [
    'unknown responses content',
    'openai-responses',
    {
      status: 'completed',
      output: [{ type: 'message', content: [{ type: 'unknown' }] }],
    },
    'unsupported_response_item',
  ],
] as const) {
  test(`learning rejects ${name} instead of silently accepting a no-op`, async () => {
    const service = await fixture(payload)
    try {
      await assert.rejects(complete(new Router(service.config(protocol))), (error) => {
        assert.equal(sanitizeError(error).rootCause?.errorCode, code)
        return true
      })
      assert.equal(service.requests, 1)
    } finally {
      await service.close()
    }
  })
}

test(
  'learning provider can complete after 15 seconds when both configured deadlines permit it',
  { timeout: 25000 },
  async () => {
    const service = await fixture(chat({ content: '' }), 15_100)
    try {
      const result = await complete(new Router(service.config('openai-chat', 20000, 22000)))
      assert.equal(result.text, '')
      assert.equal(service.requests, 1)
    } finally {
      await service.close()
    }
  },
)

test('learning retains separate route budget, provider deadline and caller cancellation', async () => {
  for (const kind of ['stage', 'provider', 'caller'] as const) {
    const service = await fixture(chat({ content: '' }), 1000)
    const controller = new AbortController()
    const timer = kind === 'caller' ? setTimeout(() => controller.abort(), 100) : undefined
    try {
      const settings = service.config(
        'openai-chat',
        kind === 'provider' ? 100 : 2000,
        kind === 'stage' ? 100 : 3000,
      )
      settings.providers[0]!.retryCount = 2
      const router = new Router(settings)
      await assert.rejects(complete(router, controller.signal), (error) => {
        const expected =
          kind === 'stage'
            ? 'model_stage_timeout'
            : kind === 'provider'
              ? 'provider_timeout'
              : 'request_cancelled'
        assert.equal(sanitizeError(error).rootCause?.errorCode, expected)
        if (kind === 'stage') assert.equal(modelFailureReason(error), expected)
        return true
      })
      assert.equal(service.requests, 1)
    } finally {
      if (timer) clearTimeout(timer)
      await service.close()
    }
  }
})

for (const mode of ['deadline', 'reported_timeout'] as const) {
  test(`learning custom protocol ${mode} does not repeat uncertain inference`, async () => {
    const service = await fixture({})
    const registry = new ModelProtocolRegistry()
    const lifecycle = new AbortController()
    let calls = 0
    registry.register(
      'fixture:timeout',
      'Fixture',
      {
        async complete() {
          calls++
          if (mode === 'reported_timeout') throw new ModelProtocolError('timeout')
          return new Promise<never>(() => {})
        },
      },
      lifecycle.signal,
    )
    const settings = service.config('fixture:timeout', 100, 2000)
    settings.providers[0]!.retryCount = 2
    try {
      await assert.rejects(
        complete(new Router(settings, undefined, undefined, registry)),
        (error) => {
          assert.equal(sanitizeError(error).rootCause?.errorCode, 'provider_timeout')
          return true
        },
      )
      assert.equal(calls, 1)
    } finally {
      lifecycle.abort()
      await service.close()
    }
  })
}
