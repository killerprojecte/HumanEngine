import test from 'node:test'
import assert from 'node:assert/strict'
import { Store } from '../src/store.ts'
import { Runtime, type RuntimeMessage } from '../src/runtime.ts'
import { PluginManager, type PluginToolContext, type LocalPlugin } from '../src/plugins.ts'
import { installAdapter, untilAbort } from '../src/plugin-adapters.ts'
import type { Router, Completion, ChatMessage } from '../src/models.ts'
import type { SceneRef, SendResult } from '../src/onebot.ts'

const scene: SceneRef = {
  id: 'fixture-scene',
  kind: 'group',
  adapterId: 'fixture',
  accountId: 'bot',
  platformSceneId: 'group',
}
const message = (id: string, text: string): RuntimeMessage => ({
  id,
  sceneId: scene.id,
  senderId: 'person',
  origin: 'live',
  status: 'available',
  sentAt: new Date().toISOString(),
  receivedAt: new Date().toISOString(),
  parts: [{ type: 'text', text }],
})
const result = (toolCalls: Completion['toolCalls']): Completion => ({
  text: '',
  toolCalls,
  modelId: 'fixture',
  usage: { inputTokens: 0, outputTokens: 0, totalTokens: 0 },
  assistant: { role: 'assistant', content: '', toolCalls },
})
const decision = (action = 'reply') =>
  result([
    {
      id: 'decision',
      name: 'participation.decide',
      arguments: { action, reasonCode: 'fixture', targetMessageIds: ['main'] },
    },
  ])
const request = {
  operationId: 'reply',
  segments: [{ parts: [{ type: 'text', text: 'old answer' }] }],
}
const manifest: LocalPlugin['manifest'] = {
  id: 'fixture',
  name: 'Fixture',
  version: '1.0.0',
  description: 'Local send boundary fixture',
  configSchema: { type: 'object' },
  capabilities: [],
}
const tick = () => new Promise<void>((resolve) => setImmediate(resolve))
function setup(complete: (role: string, messages: ChatMessage[]) => Promise<Completion>) {
  const store = new Store(':memory:')
  store.saveScene({
    ...scene,
    name: 'Group',
    participationEnabled: true,
    learningEnabled: false,
    proactiveEnabled: true,
  })
  const runtime = new Runtime(store, () => ({ complete }) as Router)
  const plugins = new PluginManager(store, runtime)
  return { store, runtime, plugins }
}

test('A plugin send delayed by a mutating hook validates the exact post-hook segment after a correction', async () => {
  let release!: () => void,
    entered!: () => void,
    plannerCalls = 0,
    sends = 0,
    hookSceneFrozen = false
  const gate = new Promise<void>((resolve) => {
      release = resolve
    }),
    waiting = new Promise<void>((resolve) => {
      entered = resolve
    })
  const validated: Array<{ proposed: typeof request; newMessages: RuntimeMessage[] }> = []
  const { store, runtime, plugins } = setup(async (role, messages) => {
    if (role === 'planner') {
      if (messages[0]!.content.includes('Validate the exact')) {
        const value = JSON.parse(messages[1]!.content) as {
          proposed: typeof request
          newMessages: RuntimeMessage[]
        }
        validated.push(value)
        return result([
          {
            id: 'validate',
            name: 'reply.validate',
            arguments: {
              valid: value.proposed.segments[0]?.parts[0]?.text === 'safe acknowledgment',
            },
          },
        ])
      }
      return decision(++plannerCalls === 1 ? 'reply' : 'silent')
    }
    return messages.some((message) => message.role === 'tool')
      ? result([])
      : result([{ id: 'deliver', name: 'fixture.deliver', arguments: {} }])
  })
  runtime.addAdapter('fixture', {
    send: async () => {
      sends++
      return { status: 'sent', messageId: 'receipt' }
    },
    history: async () => ({ items: [], coverage: 'complete' }),
    getMessage: async () => ({ items: [], coverage: 'complete' }),
    dispose() {},
  })
  try {
    await plugins.register({
      manifest,
      setup(context) {
        context.beforeSend({
          priority: 1,
          handler: async (segments, turn) => {
            hookSceneFrozen = Object.isFrozen(turn.scene)
            entered()
            await gate
            return {
              action: 'continue',
              segments: segments.map((segment, index) => ({
                ...segment,
                parts: [
                  {
                    type: 'text',
                    text: index === 0 ? 'safe acknowledgment' : 'hook-edited stale answer',
                  },
                ],
              })),
            }
          },
        })
        context.registerTool({
          name: 'fixture.deliver',
          description: 'Guarded send',
          effect: 'send',
          inputSchema: { type: 'object' },
          outputSchema: { type: 'array' },
          execute: async (_input, turn) =>
            await turn.send({ ...request, segments: [...request.segments, ...request.segments] }),
        })
      },
    })
    await plugins.enable('fixture')
    runtime.ingest(message('main', 'Original question'), scene, false)
    const running = runtime.proactive(scene.id)
    await Promise.race([
      waiting,
      running.then(() => {
        throw new Error('hook_not_started')
      }),
    ])
    runtime.ingest(
      { ...message('correction', 'Correction: do not use the old answer'), replyToId: 'main' },
      scene,
      false,
    )
    release()
    await running
    assert.equal(
      validated.length,
      2,
      'approval for one segment must not approve a different segment',
    )
    assert.deepEqual(
      validated.map((value) => value.proposed.segments),
      [
        [{ parts: [{ type: 'text', text: 'safe acknowledgment' }] }],
        [{ parts: [{ type: 'text', text: 'hook-edited stale answer' }] }],
      ],
    )
    assert.ok(
      validated.every((value) => value.newMessages.some((message) => message.id === 'correction')),
    )
    assert.equal(sends, 1, 'only the explicitly validated acknowledgment may be sent')
  } finally {
    release()
    await plugins.dispose()
    runtime.dispose()
    store.close()
  }
})

test('A detached plugin send waiting in a hook is canceled when its tool invocation returns', async () => {
  let release!: () => void,
    entered!: () => void,
    sends = 0
  const gate = new Promise<void>((resolve) => {
      release = resolve
    }),
    waiting = new Promise<void>((resolve) => {
      entered = resolve
    })
  let detached: Promise<SendResult[]> | undefined,
    lateSend: PluginToolContext['send'] | undefined,
    hookSignal: AbortSignal | undefined
  const { store, runtime, plugins } = setup(async (role, messages) =>
    role === 'planner'
      ? decision()
      : messages.some((message) => message.role === 'tool')
        ? result([])
        : result([{ id: 'deliver', name: 'fixture.detached', arguments: {} }]),
  )
  runtime.addAdapter('fixture', {
    send: async () => {
      sends++
      return { status: 'sent' }
    },
    history: async () => ({ items: [], coverage: 'complete' }),
    getMessage: async () => ({ items: [], coverage: 'complete' }),
    dispose() {},
  })
  try {
    await plugins.register({
      manifest,
      setup(context) {
        context.beforeSend({
          priority: 1,
          handler: async (segments, turn) => {
            hookSignal = turn.signal
            entered()
            await gate
            return { action: 'continue', segments }
          },
        })
        context.registerTool({
          name: 'fixture.detached',
          description: 'Deliberately incorrect detached use',
          effect: 'send',
          inputSchema: { type: 'object' },
          outputSchema: { type: 'object' },
          execute: async (_input, turn) => {
            lateSend = turn.send
            detached = turn.send(request)
            return { started: true }
          },
        })
      },
    })
    await plugins.enable('fixture')
    runtime.ingest(message('main', 'Question'), scene, false)
    const running = runtime.proactive(scene.id)
    await waiting
    await running
    assert.equal(hookSignal?.aborted, true)
    assert.equal(runtime.activeTasks, 0)
    assert.throws(() => lateSend!(request), /plugin_send_not_allowed/)
    release()
    assert.deepEqual(
      (await detached)!.map((result) => result.status),
      ['cancelled'],
    )
    assert.equal(sends, 0)
  } finally {
    release()
    await detached?.catch(() => {})
    await plugins.dispose()
    runtime.dispose()
    store.close()
  }
})

test('Disabled startup never invokes the transport and throwing disposal still removes registry state and notifies core', async () => {
  const store = new Store(':memory:'),
    runtime = new Runtime(store),
    controller = new AbortController()
  let started = 0,
    disposed = 0,
    stopped = 0
  const adapter = installAdapter(
    'fixture:transport',
    () => ({
      start: async () => {
        started++
      },
      send: async () => ({ status: 'failed' }),
      history: async () => ({ items: [], coverage: 'unavailable' }),
      getMessage: async () => ({ items: [], coverage: 'unavailable' }),
      dispose: () => {
        disposed++
        throw new Error('fixture_cleanup_failed')
      },
    }),
    runtime,
    store,
    controller.signal,
    {
      adapterStopped: () => {
        stopped++
      },
    },
  )
  try {
    const starting = adapter.start(),
      rejected = assert.rejects(starting, /plugin_disabled/)
    controller.abort()
    await rejected
    await tick()
    assert.equal(started, 0)
    await assert.rejects(
      untilAbort(Promise.reject(new Error('already_rejected_fixture')), controller.signal),
      /plugin_disabled/,
    )
    await tick()
    assert.throws(() => adapter.dispose(), /fixture_cleanup_failed/)
    assert.equal(disposed, 1)
    assert.equal(stopped, 1)
    assert.deepEqual(runtime.adapterStatuses, [])
  } finally {
    runtime.dispose()
    store.close()
  }
})
