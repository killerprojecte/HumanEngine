import assert from 'node:assert/strict'
import test from 'node:test'
import { Router, type RouterConfig, type ModelLog } from '../src/models.ts'
import { ModelProtocolRegistry } from '../src/model-protocols.ts'
import { currentFlowContext, withFlowContext, type FlowWriter } from '../src/diagnostics/flow.ts'
import { modelScheduler } from '../src/scheduler.ts'

type Row = Record<string, unknown> & { counts?: Record<string, number> }
function capture() {
  const rows: Row[] = []
  const writer: FlowWriter = {
    log(level, module, code, context) {
      rows.push({ level, module, code, ...context })
    },
  }
  return { rows, writer }
}
function config(): RouterConfig {
  return {
    providers: [
      {
        id: 'provider',
        name: 'Fixture',
        protocol: 'openai-chat',
        baseUrl: 'http://fixture.invalid/v1',
        credentialEnv: '',
      },
    ],
    models: [
      {
        id: 'model',
        displayName: 'Fixture',
        apiModelName: 'fixture-model',
        providerId: 'provider',
        capabilities: ['text', 'tools', 'embedding'],
      },
    ],
    routes: [{ role: 'thinker', orderedModelIds: ['model'], timeoutMs: 1000 }],
  }
}
const completion = () =>
  Response.json({
    choices: [{ finish_reason: 'stop', message: { content: 'PRIVATE_COMPLETION_CANARY' } }],
    usage: { prompt_tokens: 8, completion_tokens: 3, total_tokens: 11 },
  })
function assertClosed(rows: Row[]) {
  const started = rows.filter((row) => row.status === 'started')
  assert.ok(started.length > 0)
  for (const row of started) {
    const pair = rows.filter(
      (other) => other.spanId === row.spanId && other.status !== 'cancel_requested',
    )
    assert.equal(pair.length, 2, `${row.stage} must have exactly one terminal event`)
    assert.notEqual(pair[1]!.status, 'started')
    assert.equal(pair[1]!.traceId, row.traceId)
    assert.equal(typeof pair[1]!.durationMs, 'number')
  }
}

test('model fallback logs safe attempt lifecycle, usage and result category without changing accounting', async () => {
  const { rows, writer } = capture(),
    accounting: ModelLog[] = []
  const cfg = config()
  cfg.models.push({ ...cfg.models[0]!, id: 'fallback', apiModelName: 'fallback-model' })
  cfg.routes[0]!.orderedModelIds = ['absent', 'model', 'fallback']
  const original = globalThis.fetch
  globalThis.fetch = async (_url, init) =>
    JSON.parse(String(init?.body)).model === 'fixture-model'
      ? new Response('PRIVATE_ERROR_CANARY', { status: 503 })
      : completion()
  try {
    const router = new Router(
      cfg,
      (row) => accounting.push(row),
      undefined,
      undefined,
      undefined,
      writer,
    )
    await withFlowContext(
      { traceId: 'trace-fallback', turnId: 'turn-fallback' },
      () => router.complete('thinker', [{ role: 'user', content: 'PRIVATE_INPUT_CANARY' }]),
      writer,
    )
    assertClosed(rows)
    const attempts = rows.filter(
      (row) =>
        row.stage === 'model_attempt' &&
        row.status !== 'started' &&
        row.status !== 'cancel_requested',
    )
    assert.deepEqual(
      attempts.map((row) => row.status),
      ['skipped', 'failed', 'completed'],
    )
    assert.equal(attempts[0]!.reason, 'model_not_configured')
    assert.equal(attempts[1]!.reason, 'provider_http_503')
    assert.equal(attempts[1]!.nextStep, 'select_next_model')
    assert.equal(attempts[2]!.outputKind, 'text')
    assert.equal(attempts[2]!.counts?.totalTokens, 11)
    assert.equal(attempts[2]!.apiModel, 'fallback-model')
    assert.equal(rows.at(-1)!.stage, 'model_request')
    assert.equal(rows.at(-1)!.status, 'completed')
    assert.deepEqual(
      accounting.map((row) => row.event),
      ['model.failed', 'model.completed'],
    )
    assert.ok(
      rows.every((row) => row.traceId === 'trace-fallback' && row.turnId === 'turn-fallback'),
    )
    assert.doesNotMatch(JSON.stringify(rows), /PRIVATE_.*_CANARY/)
  } finally {
    globalThis.fetch = original
  }
})

test('cancelled queued calls close their spans and scheduler callbacks keep distinct originating traces', async () => {
  const { rows, writer } = capture(),
    original = globalThis.fetch
  let release!: () => void, entered!: () => void
  const started = new Promise<void>((resolve) => {
    entered = resolve
  })
  const gate = new Promise<void>((resolve) => {
    release = resolve
  })
  const observed: string[] = []
  let calls = 0
  globalThis.fetch = async () => {
    observed.push(currentFlowContext().traceId!)
    if (++calls === 1) {
      entered()
      await gate
    }
    return completion()
  }
  modelScheduler.configure(1)
  try {
    const router = new Router(config(), undefined, undefined, undefined, undefined, writer)
    const run = (traceId: string, signal?: AbortSignal) =>
      withFlowContext(
        { traceId, turnId: `${traceId}-turn` },
        () => router.complete('thinker', [], [], signal),
        writer,
      )
    const a = run('trace-a')
    await started
    const abort = new AbortController()
    const b = run('trace-b', abort.signal)
    const rejected = assert.rejects(b, /request_cancelled/)
    const c = run('trace-c')
    abort.abort()
    await rejected
    release()
    await Promise.all([a, c])
    assertClosed(rows)
    assert.deepEqual(observed, ['trace-a', 'trace-c'])
    const cancelled = rows.filter(
      (row) =>
        row.traceId === 'trace-b' && row.status !== 'started' && row.status !== 'cancel_requested',
    )
    assert.deepEqual(
      cancelled.map((row) => row.status),
      ['cancelled', 'cancelled', 'cancelled'],
    )
    assert.equal(
      rows.some((row) => row.traceId === 'trace-b' && row.status === 'completed'),
      false,
    )
  } finally {
    release()
    globalThis.fetch = original
    modelScheduler.configure(4)
  }
})

test('custom protocols, embeddings and builtin discovery emit summaries without extra accounting records', async () => {
  const { rows, writer } = capture(),
    accounting: ModelLog[] = []
  const cfg = config(),
    registry = new ModelProtocolRegistry(),
    controller = new AbortController()
  cfg.providers[0]!.protocol = 'fixture:protocol'
  registry.register(
    'fixture:protocol',
    'Fixture',
    {
      async complete() {
        return {
          text: '',
          toolCalls: [],
          usage: { known: false, inputTokens: 0, outputTokens: 0, totalTokens: 0 },
        }
      },
      async embeddings(_context, inputs) {
        return {
          vectors: inputs.map(() => [1, 2]),
          usage: { inputTokens: 2, outputTokens: 0, totalTokens: 2 },
        }
      },
      async listModels() {
        return ['PRIVATE_MODEL_LIST_ENTRY']
      },
    },
    controller.signal,
  )
  const router = new Router(
    cfg,
    (row) => accounting.push(row),
    undefined,
    registry,
    undefined,
    writer,
  )
  await router.complete('thinker', [])
  await router.embeddings('model', ['PRIVATE_EMBEDDING_CANARY'])
  await router.listModels('provider')
  assertClosed(rows)
  assert.deepEqual(
    accounting.map((row) => row.event),
    ['model.completed', 'model.completed', 'model.completed'],
  )
  assert.ok(
    rows.some(
      (row) =>
        row.outputKind === 'empty' &&
        row.counts?.usageKnown === 0 &&
        row.counts.inputTokens === undefined,
    ),
  )
  assert.ok(
    rows.some(
      (row) =>
        row.outputKind === 'embeddings' && row.counts?.vectors === 1 && row.counts.dimensions === 2,
    ),
  )
  assert.ok(rows.some((row) => row.outputKind === 'model_list' && row.counts?.models === 1))
  assert.doesNotMatch(JSON.stringify(rows), /PRIVATE_/)
  controller.abort()
  const original = globalThis.fetch
  globalThis.fetch = async () => Response.json({ data: [{ id: 'PRIVATE_DISCOVERY_CANARY' }] })
  try {
    const plain = new Router(config(), undefined, undefined, undefined, undefined, writer)
    await plain.listModels('provider')
    assert.equal(rows.at(-1)!.outputKind, 'model_list')
    assertClosed(rows)
    assert.doesNotMatch(JSON.stringify(rows), /PRIVATE_/)
  } finally {
    globalThis.fetch = original
  }
})

test('preflight failures have terminal operation diagnostics without invented network attempts', async () => {
  const { rows, writer } = capture(),
    cfg = config()
  cfg.routes = []
  await assert.rejects(
    new Router(cfg, undefined, undefined, undefined, undefined, writer).complete('thinker', []),
    /route_not_configured/,
  )
  assertClosed(rows)
  assert.equal(rows.length, 2)
  assert.equal(rows[1]!.reason, 'route_not_configured')
  assert.equal(rows[1]!.status, 'failed')
})
