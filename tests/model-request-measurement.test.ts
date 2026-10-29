import assert from 'node:assert/strict'
import test from 'node:test'
import { createServer } from 'node:http'
import { Router, type RouterConfig, type ModelLog, type ChatMessage } from '../src/models.ts'
import { ModelProtocolRegistry } from '../src/model-protocols.ts'
import { TargetTokenizerRegistry } from '../src/model-measurement/registry.ts'
import {
  measureSerializedRequest,
  withRequestMeasurements,
  recordRequestMeasurement,
  requestMeasurementSummary,
} from '../src/model-measurement/measurement.ts'
import { BudgetAdmissionError, type NativeBudgetGate } from '../src/budget/native-attempt.ts'
const target = {
  providerId: 'p',
  protocol: 'openai-chat',
  apiModelName: 'fixture',
  codecId: 'native-json:chat/completions:v1',
}
const config = (protocol: string, baseUrl: string): RouterConfig => ({
  providers: [
    {
      id: 'p',
      name: 'Synthetic',
      protocol,
      baseUrl,
      credentialEnv: '',
      retryCount: 1,
      timeoutMs: 2000,
    },
  ],
  models: [
    {
      id: 'm',
      displayName: 'Synthetic',
      providerId: 'p',
      apiModelName: 'fixture',
      capabilities: ['text', 'tools', 'embedding'],
    },
  ],
  routes: [{ role: 'replyer', orderedModelIds: ['m'], timeoutMs: 10000 }],
})
test('unregistered targets remain byte-only; trusted counters cannot claim provider calibration or hard caps', () => {
  const text = JSON.stringify({
    system: '完整条件😀',
    tools: [{ name: 'fixture', schema: { description: 'Keep negative constraint' } }],
  })
  const empty = measureSerializedRequest(text, target)
  assert.equal(empty.bodyBytes, Buffer.byteLength(text))
  assert.equal(empty.textTokens, undefined)
  const registry = new TargetTokenizerRegistry()
  let n = 7
  const remove = registry.register({
    target,
    id: 'fixture-counter',
    revision: 'v1',
    count: () => n,
  })
  const measured = measureSerializedRequest(text, target, registry)
  assert.equal(measured.textTokens, 7)
  assert.equal(measured.hardTokenCap, false)
  assert.equal(measured.providerTokenCount, 'unknown')
  assert.equal(
    measureSerializedRequest(text, { ...target, apiModelName: 'another' }, registry).textTokens,
    undefined,
  )
  for (const invalid of [NaN, -1, Infinity, 0.5]) {
    n = invalid
    assert.equal(measureSerializedRequest(text, target, registry).reason, 'invalid_counter')
  }
  remove()
  assert.equal(measureSerializedRequest(text, target, registry).reason, 'unregistered_tokenizer')
  registry.register({
    target,
    id: 'throws',
    revision: 'v1',
    count: () => {
      throw Error('PRIVATE_COUNTER_BODY')
    },
  })
  assert.equal(measureSerializedRequest(text, target, registry).reason, 'counter_failed')
  assert.equal(
    JSON.stringify(measureSerializedRequest(text, target, registry)).includes(
      'PRIVATE_COUNTER_BODY',
    ),
    false,
  )
  withRequestMeasurements(() => {
    recordRequestMeasurement('p', 'm', { ...measured, textTokens: Number.MAX_SAFE_INTEGER })
    recordRequestMeasurement('p', 'm', { ...measured, textTokens: 1 })
    assert.equal(requestMeasurementSummary('p', 'm')!.serializedTextTokens, null)
  })
})

test('native actual request body includes system, tools, retries and later rounds; the counted string is exactly sent', async () => {
  for (const protocol of ['openai-chat', 'openai-responses']) {
    const wires: string[] = [],
      counted: string[] = [],
      logs: ModelLog[] = []
    const server = createServer(async (req, res) => {
      let body = ''
      for await (const chunk of req) body += chunk.toString()
      wires.push(body)
      res.setHeader('Content-Type', 'application/json')
      if (wires.length === 1) {
        res.statusCode = 429
        res.setHeader('retry-after', '0')
        res.end('{}')
        return
      }
      const input = JSON.parse(body),
        name = protocol === 'openai-chat' ? input.tools[0].function.name : input.tools[0].name
      const first = wires.length === 2
      res.end(
        JSON.stringify(
          protocol === 'openai-chat'
            ? {
                choices: [
                  {
                    message: first
                      ? {
                          content: null,
                          tool_calls: [
                            { id: 'call', type: 'function', function: { name, arguments: '{}' } },
                          ],
                        }
                      : { content: 'Done' },
                    finish_reason: first ? 'tool_calls' : 'stop',
                  },
                ],
                usage: { prompt_tokens: 1, completion_tokens: 1, total_tokens: 2 },
              }
            : {
                status: 'completed',
                output: first
                  ? [{ type: 'function_call', call_id: 'call', name, arguments: '{}' }]
                  : [{ type: 'message', content: [{ type: 'output_text', text: 'Done' }] }],
                usage: { input_tokens: 1, output_tokens: 1, total_tokens: 2 },
              },
        ),
      )
    })
    await new Promise<void>((resolve) => server.listen(0, '127.0.0.1', resolve))
    const url = `http://127.0.0.1:${(server.address() as { port: number }).port}`
    const registry = new TargetTokenizerRegistry()
    registry.register({
      target: {
        ...target,
        protocol,
        codecId: `native-json:${protocol === 'openai-chat' ? 'chat/completions' : 'responses'}:v1`,
      },
      id: 'offline-fixture',
      revision: 'v1',
      count: (wire) => {
        counted.push(wire)
        return 17
      },
    })
    try {
      const router = new Router(
        config(protocol, url),
        (log) => logs.push(log),
        undefined,
        undefined,
        undefined,
        undefined,
        undefined,
        registry,
      )
      const messages: ChatMessage[] = [
        { role: 'system', content: 'SYSTEM_CANARY complete constraints' },
        { role: 'user', content: 'QUESTION_CANARY' },
      ]
      const tools = [
        {
          name: 'fixture.read',
          description: 'TOOLS_CANARY',
          inputSchema: { type: 'object', properties: {}, additionalProperties: false },
        },
      ]
      const first = await router.complete('replyer', messages, tools)
      await router.complete(
        'replyer',
        [
          ...messages,
          first.assistant,
          { role: 'tool', toolCallId: 'call', content: 'LATER_RESULT_CANARY' },
        ],
        tools,
      )
      assert.equal(wires.length, 3)
      assert.deepEqual(counted, wires)
      assert.ok(
        wires.every((wire) => wire.includes('SYSTEM_CANARY') && wire.includes('TOOLS_CANARY')),
      )
      assert.ok(wires[2]!.includes('LATER_RESULT_CANARY'))
      const measurements = logs
        .filter((log) => log.event === 'model.completed')
        .map((log) => log.requestMeasurement!)
      assert.deepEqual(
        measurements.map((m) => m.attempts),
        [2, 1],
      )
      assert.deepEqual(
        measurements.map((m) => m.bodyUtf8Bytes),
        [Buffer.byteLength(wires[0]!) + Buffer.byteLength(wires[1]!), Buffer.byteLength(wires[2]!)],
      )
      assert.deepEqual(
        measurements.map((m) => m.serializedTextTokens),
        [34, 17],
      )
      assert.equal(JSON.stringify(measurements).includes('CANARY'), false)
    } finally {
      await new Promise<void>((resolve) => server.close(() => resolve()))
    }
  }
})

test('custom adapters report unknown wire instead of counting invented native framing', async () => {
  const protocols = new ModelProtocolRegistry(),
    controller = new AbortController(),
    logs: ModelLog[] = []
  protocols.register(
    'fixture:custom',
    'Synthetic',
    {
      complete: async () => ({
        text: 'ok',
        toolCalls: [],
        usage: { inputTokens: 1, outputTokens: 1, totalTokens: 2 },
      }),
    },
    controller.signal,
  )
  try {
    const router = new Router(
      config('fixture:custom', 'https://example.invalid'),
      (log) => logs.push(log),
      undefined,
      protocols,
    )
    await router.complete('replyer', [{ role: 'user', content: 'PRIVATE_BODY' }])
    assert.equal(logs[0]!.requestMeasurement!.attempts, 1)
    assert.equal(logs[0]!.requestMeasurement!.bytesCoverage, 'unknown')
    assert.equal(logs[0]!.requestMeasurement!.textTokenCoverage, 'unknown')
  } finally {
    controller.abort()
  }
})

test('trusted counter runs before the final native HOLD guard; denial sends no request', async () => {
  let requests = 0,
    blocked = false
  const server = createServer((_req, res) => {
    requests++
    res.end('{}')
  })
  await new Promise<void>((resolve) => server.listen(0, '127.0.0.1', resolve))
  const registry = new TargetTokenizerRegistry()
  registry.register({
    target,
    id: 'hold-fixture',
    revision: 'v1',
    count: () => {
      blocked = true
      return 1
    },
  })
  const budget = {
    prepare: (_: unknown, body: unknown) => ({
      body,
      start: () => {
        if (blocked) throw new BudgetAdmissionError('budget_restore_accounting_hold')
      },
      fail: () => {},
      settle: () => {},
    }),
  } as unknown as NativeBudgetGate
  try {
    const router = new Router(
      config('openai-chat', `http://127.0.0.1:${(server.address() as { port: number }).port}`),
      undefined,
      undefined,
      undefined,
      undefined,
      undefined,
      budget,
      registry,
    )
    await assert.rejects(
      router.complete('replyer', [{ role: 'user', content: 'Synthetic' }]),
      (e: unknown) =>
        !!e && typeof e === 'object' && 'code' in e && e.code === 'budget_restore_accounting_hold',
    )
    assert.equal(requests, 0)
  } finally {
    await new Promise<void>((resolve) => server.close(() => resolve()))
  }
})

test('embedding batches emit disjoint wire totals and default unregistered token coverage', async () => {
  const wires: string[] = [],
    logs: ModelLog[] = []
  const server = createServer(async (req, res) => {
    let body = ''
    for await (const chunk of req) body += chunk.toString()
    wires.push(body)
    const input = JSON.parse(body)
    res.setHeader('Content-Type', 'application/json')
    res.end(
      JSON.stringify({
        data: input.input.map((_: string, index: number) => ({ index, embedding: [1, 2] })),
        usage: { prompt_tokens: 1, total_tokens: 1 },
      }),
    )
  })
  await new Promise<void>((resolve) => server.listen(0, '127.0.0.1', resolve))
  try {
    const cfg = config(
      'openai-chat',
      `http://127.0.0.1:${(server.address() as { port: number }).port}`,
    )
    cfg.models[0]!.embeddingBatchSize = 1
    const router = new Router(cfg, (log) => logs.push(log))
    await router.embeddings('m', ['First synthetic', 'Second synthetic'])
    assert.equal(logs.length, 2)
    assert.deepEqual(
      logs.map((log) => log.requestMeasurement!.attempts),
      [1, 1],
    )
    assert.deepEqual(
      logs.map((log) => log.requestMeasurement!.bodyUtf8Bytes),
      wires.map((w) => Buffer.byteLength(w)),
    )
    assert.ok(logs.every((log) => log.requestMeasurement!.textTokenCoverage === 'unknown'))
  } finally {
    await new Promise<void>((resolve) => server.close(() => resolve()))
  }
})

test('budget OFF still rechecks restoration HOLD and refuses newly enabled unreserved budgeting after counting', async () => {
  let requests = 0
  const server = createServer((_req, res) => {
    requests++
    res.end('{}')
  })
  await new Promise<void>((resolve) => server.listen(0, '127.0.0.1', resolve))
  try {
    for (const change of ['hold', 'enable']) {
      let changed = false
      const registry = new TargetTokenizerRegistry()
      registry.register({
        target,
        id: 'off-guard-fixture',
        revision: 'v1',
        count: () => {
          changed = true
          return 1
        },
      })
      const supplier = () => {
        if (!changed) return undefined
        if (change === 'hold') throw new BudgetAdmissionError('budget_restore_accounting_hold')
        return {} as NativeBudgetGate
      }
      const router = new Router(
        config('openai-chat', `http://127.0.0.1:${(server.address() as { port: number }).port}`),
        undefined,
        undefined,
        undefined,
        undefined,
        undefined,
        supplier,
        registry,
      )
      const code =
        change === 'hold' ? 'budget_restore_accounting_hold' : 'budget_configuration_changed'
      await assert.rejects(
        router.complete('replyer', [{ role: 'user', content: 'Synthetic' }]),
        (e: unknown) => !!e && typeof e === 'object' && 'code' in e && e.code === code,
      )
    }
    assert.equal(requests, 0)
  } finally {
    await new Promise<void>((resolve) => server.close(() => resolve()))
  }
})

test('read admission codes survive final supplier checks without transport, retry, or model fallback', async () => {
  let requests = 0
  const server = createServer((_req, res) => {
    requests++
    res.end('{}')
  })
  await new Promise<void>((resolve) => server.listen(0, '127.0.0.1', resolve))
  try {
    for (const code of [
      'memory_read_disabled',
      'memory_read_revoked',
      'personalization_disabled',
    ]) {
      let revoked = false
      const registry = new TargetTokenizerRegistry()
      registry.register({
        target,
        id: 'read-admission-fixture',
        revision: 'v1',
        count: () => {
          revoked = true
          return 1
        },
      })
      const cfg = config(
        'openai-chat',
        `http://127.0.0.1:${(server.address() as { port: number }).port}`,
      )
      cfg.models.push({ ...cfg.models[0]!, id: 'fallback' })
      cfg.routes[0]!.orderedModelIds.push('fallback')
      const logs: ModelLog[] = []
      const router = new Router(
        cfg,
        (log) => logs.push(log),
        undefined,
        undefined,
        undefined,
        undefined,
        () => {
          if (revoked) throw new Error(code)
          return undefined
        },
        registry,
      )
      await assert.rejects(
        router.complete('replyer', [{ role: 'user', content: 'Synthetic' }]),
        (e: unknown) =>
          e instanceof Error &&
          'code' in e &&
          e.code === code &&
          'retryable' in e &&
          e.retryable === false,
      )
      assert.deepEqual(
        logs.filter((log) => log.event === 'model.failed').map((log) => [log.modelId, log.code]),
        [['m', code]],
      )
    }
    assert.equal(requests, 0)
  } finally {
    await new Promise<void>((resolve) => server.close(() => resolve()))
  }
})
