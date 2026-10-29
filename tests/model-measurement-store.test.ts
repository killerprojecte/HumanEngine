import assert from 'node:assert/strict'
import test from 'node:test'
import { createServer } from 'node:http'
import { Engine } from '../src/engine.ts'
import { Store } from '../src/store.ts'
import { createApp } from '../src/api.ts'
import { withFlowContext } from '../src/diagnostics/flow.ts'
import { TargetTokenizerRegistry } from '../src/model-measurement/registry.ts'
import { RequestMeasurementSummarySchema } from '../src/model-measurement/schema.ts'
import type { RouterConfig } from '../src/models.ts'

test('actual Engine wire measurement reaches safe Store/API logs in flows without altering usage', async () => {
  const wires: string[] = []
  const server = createServer(async (req, res) => {
    let body = ''
    for await (const chunk of req) body += chunk.toString()
    wires.push(body)
    res.setHeader('Content-Type', 'application/json')
    res.end(
      JSON.stringify({
        choices: [{ message: { content: 'ok' }, finish_reason: 'stop' }],
        usage: { prompt_tokens: 7, completion_tokens: 2, total_tokens: 9 },
      }),
    )
  })
  await new Promise<void>((resolve) => server.listen(0, '127.0.0.1', resolve))
  const store = new Store(':memory:')
  const registry = new TargetTokenizerRegistry()
  const engine = new Engine(store, { tokenizerRegistry: registry })
  const controller = new AbortController()
  try {
    const config: RouterConfig = {
      providers: [
        {
          id: 'p',
          name: 'Synthetic',
          protocol: 'openai-chat',
          baseUrl: `http://127.0.0.1:${(server.address() as { port: number }).port}`,
          credentialEnv: '',
          retryCount: 0,
        },
      ],
      models: [
        {
          id: 'm',
          providerId: 'p',
          displayName: 'Synthetic',
          apiModelName: 'fixture',
          capabilities: ['text', 'tools'],
        },
      ],
      routes: [{ role: 'replyer', orderedModelIds: ['m'], timeoutMs: 10000 }],
    }
    const counted: string[] = []
    const invoke = () =>
      withFlowContext({ spanId: 'fixture-span', traceId: 'fixture-trace' }, () =>
        engine.router(config).complete(
          'replyer',
          [
            { role: 'system', content: 'PRIVATE_SYSTEM_CANARY' },
            { role: 'user', content: 'Synthetic' },
          ],
          [
            {
              name: 'fixture',
              description: 'PRIVATE_TOOL_CANARY',
              inputSchema: { type: 'object', properties: {} },
            },
          ],
        ),
      )
    await invoke()
    registry.register({
      target: {
        providerId: 'p',
        protocol: 'openai-chat',
        apiModelName: 'fixture',
        codecId: 'native-json:chat/completions:v1',
      },
      id: 'offline-counter',
      revision: 'v1',
      count: (text) => {
        counted.push(text)
        return 23
      },
    })
    await invoke()
    assert.deepEqual(counted, [wires[1]])
    assert.ok(
      wires.every((w) => w.includes('PRIVATE_SYSTEM_CANARY') && w.includes('PRIVATE_TOOL_CANARY')),
    )
    engine.protocols.register(
      'fixture:custom',
      'Synthetic',
      {
        complete: async () => ({
          text: 'ok',
          toolCalls: [],
          usage: { inputTokens: 7, outputTokens: 2, totalTokens: 9 },
        }),
      },
      controller.signal,
    )
    config.providers[0]!.protocol = 'fixture:custom'
    await invoke()
    const rows = store.logs.filter((row) => row.code === 'model_request_measured')
    assert.equal(rows.length, 3)
    assert.ok(rows.every((row) => !!row.spanId && row.traceId === 'fixture-trace'))
    const [unknown, known, custom] = rows.map((row) => row.requestMeasurement!)
    assert.equal(unknown!.textTokenCoverage, 'unknown')
    assert.equal(unknown!.bodyUtf8Bytes, Buffer.byteLength(wires[0]!))
    assert.equal(known!.serializedTextTokens, 23)
    assert.equal(known!.bodyUtf8Bytes, Buffer.byteLength(wires[1]!))
    assert.equal(custom!.bytesCoverage, 'unknown')
    assert.equal(custom!.measuredBodyAttempts, 0)
    assert.equal(JSON.stringify(rows).includes('CANARY'), false)
    const usage = store.usage()
    assert.equal(
      usage.reduce((sum, row) => sum + row.calls, 0),
      3,
    )
    assert.equal(
      usage.reduce((sum, row) => sum + row.inputTokens, 0),
      21,
    )
    assert.equal(
      usage.reduce((sum, row) => sum + row.outputTokens, 0),
      6,
    )
    const app = createApp(store, { token: 'fixture-measurement' })
    assert.equal((await app.request('/api/logs')).status, 401)
    const response = await app.request('/api/logs', {
      headers: { Authorization: 'Bearer fixture-measurement' },
    })
    assert.equal(response.status, 200)
    const apiRows = await response.json()
    assert.deepEqual(
      apiRows
        .filter((row: { code: string }) => row.code === 'model_request_measured')
        .map((row: { requestMeasurement: unknown }) => row.requestMeasurement),
      rows.toReversed().map((row) => row.requestMeasurement),
    )
    const nullable = {
      ...known!,
      bodyUtf8Bytes: null,
      serializedTextTokens: null,
      bytesCoverage: 'unknown',
      textTokenCoverage: 'unknown',
    }
    assert.equal(RequestMeasurementSummarySchema.parse(nullable).bodyUtf8Bytes, null)
    assert.equal(
      RequestMeasurementSummarySchema.safeParse({ ...known, body: 'PRIVATE_CANARY' }).success,
      false,
    )
  } finally {
    controller.abort()
    await engine.dispose()
    store.close()
    await new Promise<void>((resolve) => server.close(() => resolve()))
  }
})
