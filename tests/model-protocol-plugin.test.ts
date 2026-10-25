import assert from 'node:assert/strict'
import test from 'node:test'
import { ModelProtocolRegistry, type ModelProtocol } from '../src/model-protocols.ts'
import {
  ModelError,
  Router,
  type ChatMessage,
  type ModelLog,
  type RouterConfig,
  type ToolDefinition,
} from '../src/models.ts'
import { modelScheduler } from '../src/scheduler.ts'

const tick = () => new Promise<void>((resolve) => setImmediate(resolve))
const tool: ToolDefinition = {
  name: 'probe.echo',
  description: 'Local test only',
  inputSchema: {
    type: 'object',
    properties: { value: { type: 'string' } },
    required: ['value'],
    additionalProperties: false,
  },
}
const usage = { inputTokens: 2, outputTokens: 1, totalTokens: 3 }

test('Engine-local protocol fixture isolates opaque state, validates results, and cancels registered work without fallback or secret exposure', async () => {
  const previousKey = process.env.HUMANENGINE_PROTOCOL_FIXTURE_KEY
  process.env.HUMANENGINE_PROTOCOL_FIXTURE_KEY = 'fixture-server-only-credential'
  const oldConcurrency = modelScheduler.status.concurrency
  const registry = new ModelProtocolRegistry(),
    lifecycle = new AbortController()
  const logs: ModelLog[] = [],
    diagnostics: unknown[] = [],
    seen: Array<{ id: string; continuation: unknown }> = []
  const opaque: { label: string; self?: unknown } = { label: 'opaque-hidden-state' }
  opaque.self = opaque
  let mode: 'normal' | 'invalid' | 'error' | 'pending' = 'normal',
    release: () => void = () => {},
    entered: () => void = () => {}
  const adapter: ModelProtocol = {
    complete: async (context, messages, tools, continuation) => {
      assert.equal(context.credential, 'fixture-server-only-credential')
      assert.equal(context.model?.apiModelName, 'same-api-name')
      assert.equal(tools[0]?.name, 'probe.echo')
      seen.push({ id: context.model!.id, continuation })
      if (mode === 'pending') {
        entered()
        await new Promise<void>((resolve) => {
          release = resolve
        })
      }
      if (mode === 'error') throw new ModelError('fixture-server-only-credential')
      const calls = [{ id: 'c', name: 'probe.echo', arguments: { value: 'ok' } }]
      if (mode === 'invalid')
        calls.push({ id: 'bad', name: 'unknown.tool', arguments: { value: 'must-not-execute' } })
      messages[0]!.content = 'mutated adapter copy'
      return { text: '', toolCalls: calls, usage, continuation: opaque }
    },
    embeddings: async (context, inputs) => {
      assert.equal(context.model?.id, 'a')
      return { vectors: inputs.map(() => [0.25, 0.75]), usage }
    },
    listModels: async (context) => {
      assert.equal(context.model, undefined)
      assert.equal(context.credential, 'fixture-server-only-credential')
      return ['same-api-name', 'same-api-name']
    },
  }
  let unregister = registry.register(
    'fixture:protocol',
    'Fixture protocol',
    adapter,
    lifecycle.signal,
  )
  const config: RouterConfig = {
    providers: [
      {
        id: 'p',
        name: 'Fixture',
        protocol: 'fixture:protocol',
        baseUrl: 'https://example.invalid/v1',
        credentialEnv: 'HUMANENGINE_PROTOCOL_FIXTURE_KEY',
      },
    ],
    models: ['a', 'b'].map((id) => ({
      id,
      displayName: id,
      providerId: 'p',
      apiModelName: 'same-api-name',
      capabilities: ['text', 'tools', 'embedding'],
    })),
    routes: [
      { role: 'planner', orderedModelIds: ['a'], timeoutMs: 1000 },
      { role: 'thinker', orderedModelIds: ['b'], timeoutMs: 1000 },
      { role: 'learning', orderedModelIds: ['a', 'b'], timeoutMs: 1000 },
    ],
  }
  const router = new Router(
    config,
    (entry) => logs.push(entry),
    (entry, credentials) => {
      assert.deepEqual(credentials, ['fixture-server-only-credential'])
      diagnostics.push(entry)
    },
    registry,
  )
  const messages: ChatMessage[] = [{ role: 'user', content: 'Run the local echo probe' }]
  let unblockScheduler: () => void = () => {}
  try {
    assert.deepEqual(
      new ModelProtocolRegistry().list().map((p) => p.id),
      ['openai-chat', 'openai-responses'],
    )
    assert.deepEqual(registry.list().at(-1)?.operations, ['complete', 'embeddings', 'listModels'])
    assert.throws(
      () => registry.register('openai-chat', 'Override', adapter, lifecycle.signal),
      /protocol_id/,
    )
    assert.throws(
      () => registry.register('fixture:protocol', 'Duplicate', adapter, lifecycle.signal),
      /protocol_id/,
    )
    const first = await router.complete('planner', messages, [tool])
    assert.equal(first.modelId, 'a')
    assert.equal(seen.at(-1)?.continuation, undefined)
    assert.equal(messages[0]?.content, 'Run the local echo probe')
    assert.equal(first.toolCalls[0]?.name, 'probe.echo')
    const continued = [
      ...messages,
      first.assistant,
      { role: 'tool' as const, toolCallId: 'c', content: 'ok' },
    ]
    await router.complete('planner', continued, [tool])
    assert.equal(seen.at(-1)?.continuation, opaque)
    await router.complete('thinker', continued, [tool])
    assert.equal(seen.at(-1)?.id, 'b')
    assert.equal(seen.at(-1)?.continuation, undefined)
    assert.deepEqual((await router.embeddings('a', ['one', 'two'])).vectors, [
      [0.25, 0.75],
      [0.25, 0.75],
    ])
    assert.deepEqual(await router.listModels('p'), ['same-api-name'])
    assert.ok(
      !JSON.stringify({ first, logs, diagnostics, metadata: registry.list() }).includes(
        'opaque-hidden-state',
      ),
    )
    assert.ok(
      !JSON.stringify({ first, logs, diagnostics, metadata: registry.list() }).includes(
        'fixture-server-only-credential',
      ),
    )
    const completedBefore = logs.filter((log) => log.event === 'model.completed').length
    mode = 'invalid'
    await assert.rejects(router.complete('planner', messages, [tool]), /all_models_failed/)
    assert.equal(logs.filter((log) => log.event === 'model.completed').length, completedBefore)
    mode = 'error'
    await assert.rejects(router.complete('planner', messages, [tool]), /all_models_failed/)
    assert.ok(!JSON.stringify(logs).includes('fixture-server-only-credential'))
    mode = 'pending'
    const started = new Promise<void>((resolve) => {
      entered = resolve
    })
    const pending = router.complete('learning', messages, [tool])
    const failed = assert.rejects(pending, /protocol_disabled/)
    await started
    const callsBefore = seen.length
    unregister()
    await failed
    release()
    await tick()
    assert.equal(
      seen.length,
      callsBefore,
      'disabling active protocol must not fall back to instance b',
    )
    assert.equal(logs.filter((log) => log.event === 'model.completed').length, completedBefore)
    assert.equal(registry.lookup('fixture:protocol'), undefined)
    mode = 'normal'
    unregister = registry.register('fixture:protocol', 'Reloaded', adapter, lifecycle.signal)
    await router.complete('planner', continued, [tool])
    assert.equal(
      seen.at(-1)?.continuation,
      undefined,
      'old generation cannot restore hidden continuation',
    )
    mode = 'pending'
    const timeoutLogs: ModelLog[] = []
    const timedRouter = new Router(
      { ...config, routes: [{ role: 'planner', orderedModelIds: ['a'], timeoutMs: 15 }] },
      (entry) => timeoutLogs.push(entry),
      undefined,
      registry,
    )
    const keepAlive = setTimeout(() => release(), 1000)
    try {
      await assert.rejects(timedRouter.complete('planner', messages, [tool]), /all_models_failed/)
      await tick()
      assert.equal(timeoutLogs.at(-1)?.code, 'provider_timeout')
      assert.equal(
        modelScheduler.status.running,
        0,
        'ignored adapter cancellation must not hold a scheduler slot',
      )
    } finally {
      clearTimeout(keepAlive)
      release()
      await tick()
    }
    const caller = new AbortController()
    const cancellationStarted = new Promise<void>((resolve) => {
      entered = resolve
    })
    const cancelling = router.complete('learning', messages, [tool], caller.signal)
    const cancelled = assert.rejects(cancelling, /request_cancelled/)
    await cancellationStarted
    const beforeCancellation = seen.length
    caller.abort()
    await cancelled
    release()
    await tick()
    assert.equal(seen.length, beforeCancellation, 'caller cancellation must not fall back')
    mode = 'normal'
    modelScheduler.configure(1)
    const blocker = modelScheduler.run(
      0,
      undefined,
      () =>
        new Promise<void>((resolve) => {
          unblockScheduler = resolve
        }),
    )
    await tick()
    const beforeQueued = seen.length
    const queued = router.complete('planner', messages, [tool])
    const queueFailed = assert.rejects(queued, /protocol_disabled/)
    lifecycle.abort()
    await queueFailed
    unblockScheduler()
    await blocker
    await tick()
    assert.equal(seen.length, beforeQueued, 'disabled queued work must never invoke adapter')
    assert.equal(modelScheduler.status.running, 0)
  } finally {
    release()
    unblockScheduler()
    unregister()
    lifecycle.abort()
    modelScheduler.configure(oldConcurrency)
    if (previousKey === undefined) delete process.env.HUMANENGINE_PROTOCOL_FIXTURE_KEY
    else process.env.HUMANENGINE_PROTOCOL_FIXTURE_KEY = previousKey
  }
})
