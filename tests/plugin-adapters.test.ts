import test from 'node:test'
import assert from 'node:assert/strict'
import { Store } from '../src/store.ts'
import { Runtime } from '../src/runtime.ts'
import { PluginManager } from '../src/plugins.ts'
import { ModelProtocolRegistry } from '../src/model-protocols.ts'
import { Router, type Completion } from '../src/models.ts'
import type { AdapterContext } from '../src/plugin-adapters.ts'
import type { Message, SceneRef } from '../src/onebot.ts'

const completion = (toolCalls: Completion['toolCalls']): Completion => ({
  modelId: 'fixture',
  text: '',
  toolCalls,
  usage: { inputTokens: 0, outputTokens: 0, totalTokens: 0 },
  assistant: { role: 'assistant', content: '', toolCalls },
})
test('Declared plugin adapters and protocols are scoped, core-owned, simulation isolated, and terminal on disable', async () => {
  const previousKey = process.env.HUMANENGINE_ADAPTER_FIXTURE_KEY
  process.env.HUMANENGINE_ADAPTER_FIXTURE_KEY = 'fixture'
  const store = new Store(':memory:'),
    registry = new ModelProtocolRegistry()
  const runtime = new Runtime(
    store,
    () =>
      ({
        complete: async (role, messages) =>
          completion(
            role === 'planner'
              ? [
                  {
                    id: 'd',
                    name: 'participation.decide',
                    arguments: { action: 'reply', reasonCode: 'fixture' },
                  },
                ]
              : messages.some((m) => m.role === 'tool')
                ? []
                : [
                    {
                      id: 's',
                      name: 'fixture.deliver',
                      arguments: {},
                    },
                  ],
          ),
      }) as Router,
  )
  let stopped = 0,
    disposed = 0,
    sends = 0,
    source!: Message,
    scope!: SceneRef,
    ctx!: AdapterContext
  let release!: () => void, started!: () => void
  const gate = new Promise<void>((resolve) => (release = resolve)),
    sending = new Promise<void>((resolve) => (started = resolve))
  const plugins = new PluginManager(store, runtime, registry, { adapterStopped: () => stopped++ })
  store.set('settings', { ...store.settings, debounceMs: 0, maxWaitMs: 100 })
  try {
    await plugins.register({
      manifest: {
        id: 'fixture',
        name: 'Fixture',
        version: '1.0.0',
        description: 'Local transport fixture',
        configSchema: { type: 'object' },
        capabilities: ['adapter', 'model-protocol', 'tools'],
      },
      setup(context) {
        context.registerTool({
          name: 'fixture.deliver',
          description: 'Use the guarded core send service',
          effect: 'send',
          inputSchema: { type: 'object', additionalProperties: false },
          outputSchema: { type: 'array' },
          execute: async (_input, turn) =>
            turn.send({
              operationId: 'once',
              segments: [{ parts: [{ type: 'text', text: 'reply' }] }],
            }),
        })

        context.registerModelProtocol('echo', 'Fixture echo', {
          complete: async () => ({
            text: 'fixture',
            toolCalls: [],
            usage: { inputTokens: 1, outputTokens: 1, totalTokens: 2 },
          }),
        })
        context.registerAdapter('chat', (adapter) => {
          ctx = adapter
          scope = ctx.scene('account', 'group', 'group')
          const now = new Date().toISOString()
          source = {
            id: ctx.messageId(scope, '1'),
            sceneId: scope.id,
            senderId: 'person',
            sentAt: now,
            receivedAt: now,
            origin: 'live',
            status: 'available',
            parts: [{ type: 'text', text: 'private fixture' }],
          }
          return {
            start: async () => ctx.status('connected'),
            send: async () => {
              sends++
              started()
              await gate
              return { status: 'sent', messageId: ctx.messageId(scope, 'bot-result') }
            },
            getMessage: async () => ({ items: [source], coverage: 'complete' }),
            history: async () => ({ items: [source], coverage: 'complete' }),
            dispose: () => {
              disposed++
            },
          }
        })
      },
    })
    await plugins.enable('fixture')
    assert.ok(registry.list().some((p) => p.id === 'fixture:echo'))
    assert.deepEqual(runtime.adapterStatuses, [{ id: 'fixture:chat', status: 'connected' }])
    const router = new Router(
      {
        providers: [
          {
            id: 'p',
            name: 'Fixture',
            protocol: 'fixture:echo',
            baseUrl: 'https://example.invalid',
            credentialEnv: 'HUMANENGINE_ADAPTER_FIXTURE_KEY',
          },
        ],
        models: [
          {
            id: 'm',
            displayName: 'Fixture',
            providerId: 'p',
            apiModelName: 'same',
            capabilities: ['text'],
          },
        ],
        routes: [{ role: 'thinker', orderedModelIds: ['m'], timeoutMs: 1000 }],
      },
      undefined,
      undefined,
      registry,
    )
    assert.equal(
      (await router.complete('thinker', [{ role: 'user', content: 'fixture' }])).text,
      'fixture',
    )
    const simulation = runtime.createSimulation()
    assert.equal((await runtime.simulate(simulation, 'Hello')).length, 2)
    assert.equal(sends, 0)
    ctx.receive(source, scope, false)
    assert.equal(store.scene(scope.id)?.participationEnabled, false)
    assert.equal((await runtime.previewHistory(scope.id)).items.length, 1)
    assert.throws(
      () => ctx.receive({ ...source, sceneId: 'other' }, scope, false),
      /invalid_adapter_message/,
    )
    assert.throws(
      () => ctx.recall({ ...scope, accountId: 'other' }, source.id),
      /adapter_scene_mismatch/,
    )
    ctx.recall(scope, source.id)
    assert.deepEqual(runtime.preview(scope.id).find((m) => m.id === source.id)?.parts, [])
    assert.equal((await runtime.previewHistory(scope.id)).items.length, 0)
    ctx.receive(source, scope, false)
    assert.ok(
      !runtime
        .preview(scope.id)
        .some((m) => m.parts.some((p) => p.type === 'text' && p.text === 'private fixture')),
    )
    const enabled = { ...store.scene(scope.id)!, participationEnabled: true }
    store.saveScene(enabled)
    runtime.sceneChanged(enabled)
    ctx.receive({ ...source, id: ctx.messageId(scope, '2') }, scope, false)
    await Promise.race([
      sending,
      new Promise((_, reject) => {
        const timer = setTimeout(() => reject(Error('send_not_started')), 3000)
        timer.unref()
      }),
    ])
    await plugins.disable('fixture')
    release()
    await new Promise((resolve) => setTimeout(resolve, 20))
    assert.equal(sends, 1)
    assert.equal(disposed, 1)
    assert.equal(stopped, 1)
    assert.equal(runtime.activeTasks, 0)
    assert.equal(runtime.preview(scope.id).filter((m) => m.senderId === 'bot').length, 0)
    assert.deepEqual(runtime.adapterStatuses, [])
    assert.ok(!registry.list().some((p) => p.id === 'fixture:echo'))
    assert.equal(runtime.preview(scope.id).length, 2)
    ctx.receive({ ...source, id: ctx.messageId(scope, 'late') }, scope, false)
    assert.equal(runtime.preview(scope.id).length, 2)
  } finally {
    release()
    await plugins.dispose()
    runtime.dispose()
    store.close()
    if (previousKey === undefined) delete process.env.HUMANENGINE_ADAPTER_FIXTURE_KEY
    else process.env.HUMANENGINE_ADAPTER_FIXTURE_KEY = previousKey
  }
})
