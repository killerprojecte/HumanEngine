import test from 'node:test'
import assert from 'node:assert/strict'
import { createServer } from 'node:http'
import { Store } from '../src/store.ts'
import { Engine } from '../src/engine.ts'
import { sanitizeError } from '../src/diagnostics/errors.ts'
import { preserveErrorContext } from '../src/diagnostics/error-context.ts'

for (const scenario of [
  '401',
  '429',
  'invalid_json',
  'invalid_schema',
  'invalid_tool_arguments',
  'network',
  'timeout',
] as const) {
  test(`final turn retains actual provider ${scenario} root cause and actionable context`, async () => {
    const server = createServer(async (req, res) => {
      for await (const _ of req) {
        /* Drain the synthetic request. */
      }
      if (scenario === 'timeout') return
      res.setHeader('Content-Type', 'application/json')
      if (scenario === '401' || scenario === '429') {
        res.statusCode = Number(scenario)
        res.end('{"error":"private-response-canary"}')
        return
      }
      if (scenario === 'invalid_json') {
        res.end('private-invalid-json-canary')
        return
      }
      if (scenario === 'invalid_schema') {
        res.end('{"choices":123}')
        return
      }
      res.end(
        JSON.stringify({
          choices: [
            {
              finish_reason: 'tool_calls',
              message: {
                role: 'assistant',
                content: '',
                tool_calls: [
                  {
                    id: 'invalid',
                    type: 'function',
                    function: {
                      name: 'participation__decide',
                      arguments: 'private-invalid-arguments-canary',
                    },
                  },
                ],
              },
            },
          ],
        }),
      )
    })
    await new Promise<void>((r) => server.listen(0, '127.0.0.1', r))
    const port = (server.address() as { port: number }).port
    if (scenario === 'network') await new Promise<void>((r) => server.close(() => r()))
    const store = new Store(':memory:')
    const engine = new Engine(store)
    const scene = {
      id: 'error-fixture',
      kind: 'group' as const,
      adapterId: 'fixture',
      accountId: 'bot',
      platformSceneId: 'group',
    }
    store.saveScene({
      ...scene,
      name: 'Diagnostic fixture',
      participationEnabled: true,
      learningEnabled: false,
      proactiveEnabled: false,
    })
    store.set('settings', { ...store.settings, debounceMs: 0, maxWaitMs: 100 })
    store.saveModels(
      {
        providers: [
          {
            id: 'provider',
            name: 'Fixture',
            protocol: 'openai-chat',
            baseUrl: `http://127.0.0.1:${port}/v1`,
            credentialEnv: '',
          },
        ],
        models: [
          {
            id: 'model',
            displayName: 'Fixture',
            providerId: 'provider',
            apiModelName: 'fixture-planner',
            capabilities: ['text', 'tools'],
            embeddingBatchSize: 4,
          },
        ],
        routes: [
          { role: 'planner', orderedModelIds: ['model'], timeoutMs: 1000 },
          { role: 'thinker', orderedModelIds: ['model'], timeoutMs: 1000 },
        ],
      },
      { provider: { action: 'set', value: 'private-api-key-canary' } },
    )
    engine.runtime.addAdapter('fixture', {
      send: async () => {
        throw Error('must_not_send')
      },
      getMessage: async () => ({ items: [], coverage: 'complete' }),
      history: async () => ({ items: [], coverage: 'complete' }),
      dispose() {},
    })
    try {
      const now = new Date().toISOString()
      engine.runtime.ingest(
        {
          id: 'source',
          sceneId: scene.id,
          senderId: 'person',
          origin: 'live',
          status: 'available',
          sentAt: now,
          receivedAt: now,
          parts: [{ type: 'text', text: 'private-chat-canary' }],
        },
        scene,
      )
      for (
        let i = 0;
        i < 500 && !store.logs.some((row) => row.stage === 'turn' && row.status === 'failed');
        i++
      )
        await new Promise((r) => setTimeout(r, 5))
      const turn = store.logs.find((row) => row.stage === 'turn' && row.status === 'failed')
      assert.ok(turn, 'the actual turn must end in failure')
      assert.ok(turn.rootCause, 'final turn must include root cause, not merely turn_failed')
      const root = turn.rootCause
      if (scenario === '401' || scenario === '429') {
        assert.equal(root.httpStatus, Number(scenario))
        assert.equal(root.errorCode, `provider_http_${scenario}`)
      } else if (scenario === 'timeout')
        assert.ok(['planner_timeout', 'provider_timeout'].includes(root.errorCode ?? ''))
      else
        assert.equal(
          root.errorCode,
          {
            invalid_json: 'invalid_provider_json',
            invalid_schema: 'invalid_provider_response',
            invalid_tool_arguments: 'invalid_tool_arguments',
            network: 'ECONNREFUSED',
          }[scenario],
        )
      assert.equal(root.errorContext?.role, 'planner')
      assert.equal(root.errorContext?.apiModel, 'fixture-planner')
      assert.equal(root.errorContext?.providerId, 'provider')
      assert.equal(root.errorContext?.attempt, 1)
      assert.equal(root.errorContext?.timeoutMs, 1000)
      assert.ok(root.adviceCode)
      assert.ok(root.errorMessage)
      assert.doesNotMatch(
        JSON.stringify(store.logs),
        /private-(?:response|invalid-json|invalid-arguments|api-key|chat)-canary/,
      )
      assert.equal(
        store.logs.some((row) => row.code === 'turn_failed' && !row.rootCause),
        false,
        'no duplicate opaque turn error',
      )
    } finally {
      await engine.dispose()
      store.close()
      server.closeAllConnections()
      if (server.listening) await new Promise<void>((r) => server.close(() => r()))
    }
  })
}

test('serializer keeps safe technical messages, class, database code and context without evaluating accessors', () => {
  const error = Object.assign(new Error('database is locked'), {
    name: 'SqliteError',
    code: 'SQLITE_BUSY',
  })
  preserveErrorContext(error, { operation: 'learning.persist', stage: 'learning.persist' })
  const result = sanitizeError(error)
  assert.equal(result.rootCause?.errorCode, 'database_busy')
  assert.equal(result.rootCause?.errorType, 'SqliteError')
  assert.equal(result.rootCause?.systemCode, 'SQLITE_BUSY')
  assert.equal(result.rootCause?.errorContext?.operation, 'learning.persist')
  const suffix = sanitizeError(
    Object.assign(new Error('private-message-canary'), {
      code: 'SQLITE_CONSTRAINT_PRIVATE_SECRET_CANARY',
    }),
  )
  assert.equal(suffix.systemCode, 'SQLITE_CONSTRAINT')
  assert.doesNotMatch(JSON.stringify(suffix), /PRIVATE_SECRET|private-message/)
  const unsafe = new Error(
    'private-chat-canary https://fixture.invalid/?token=private-token-canary',
  )
  Object.defineProperty(unsafe, 'cause', {
    get() {
      throw Error('must_not_run')
    },
  })
  const safe = sanitizeError(unsafe)
  assert.equal(safe.errorCode, 'unknown_error')
  assert.doesNotMatch(JSON.stringify(safe), /private-|https:/)
  const technical = sanitizeError(
    new TypeError("Cannot read properties of undefined (reading 'private-field-canary')"),
  )
  assert.match(technical.errorMessage!, /Cannot read a property/)
  assert.doesNotMatch(JSON.stringify(technical), /private-field-canary/)
})
