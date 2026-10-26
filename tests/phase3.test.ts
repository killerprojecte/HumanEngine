import assert from 'node:assert/strict'
import test from 'node:test'
import { WorkScheduler } from '../src/scheduler.ts'
import { Store } from '../src/store.ts'
import { Runtime, type RuntimeEvent, type RuntimeMessage } from '../src/runtime.ts'
import type { ChatMessage, Completion, ModelDiagnostic, ModelLog, Router } from '../src/models.ts'
import type { ModelConfig } from '../src/schema.ts'
import type { SceneRef } from '../src/onebot.ts'

const tick = () => new Promise<void>((resolve) => setImmediate(resolve))
const scene: SceneRef = {
  id: 'scene',
  kind: 'group',
  adapterId: 'fixture',
  accountId: 'bot',
  platformSceneId: 'group',
}
const message = (id: string, senderId = 'person'): RuntimeMessage => ({
  id,
  sceneId: scene.id,
  senderId,
  sentAt: new Date().toISOString(),
  receivedAt: new Date().toISOString(),
  origin: 'live',
  status: 'available',
  parts: [{ type: 'text', text: 'private message' }],
})
const completion = (toolCalls: Completion['toolCalls']): Completion => ({
  text: '',
  toolCalls,
  modelId: 'fixture',
  usage: { inputTokens: 0, outputTokens: 0, totalTokens: 0 },
  assistant: { role: 'assistant', content: '', toolCalls },
})
const config = (): ModelConfig => ({
  providers: [
    {
      id: 'p',
      name: 'Fixture',
      protocol: 'openai-chat',
      baseUrl: 'https://example.invalid/v1',
      credentialEnv: 'HUMANENGINE_PHASE3_KEY',
    },
  ],
  models: [
    {
      id: 'm',
      displayName: 'Model',
      providerId: 'p',
      apiModelName: 'api-model',
      capabilities: ['text'],
      embeddingBatchSize: 4,
      inputUsdPerMillion: 2,
      outputUsdPerMillion: 4,
    },
  ],
  routes: [],
})

test('Scheduler prioritizes foreground queued work and never executes canceled queued work', async () => {
  const scheduler = new WorkScheduler(1, 8),
    order: string[] = []
  let release!: () => void
  const first = scheduler.run(
    1,
    undefined,
    () =>
      new Promise<void>((resolve) => {
        order.push('running')
        release = resolve
      }),
  )
  await tick()
  const background = scheduler.run(1, undefined, async () => {
    order.push('background')
  })
  const foreground = scheduler.run(0, undefined, async () => {
    order.push('foreground')
  })
  const controller = new AbortController()
  const canceled = scheduler.run(0, controller.signal, async () => {
    order.push('must-not-run')
  })
  const rejected = assert.rejects(canceled, /request_cancelled/)
  controller.abort()
  await rejected
  assert.equal(scheduler.status.queued, 2)
  release()
  await Promise.all([first, foreground, background])
  await tick()
  assert.deepEqual(order, ['running', 'foreground', 'background'])
  assert.deepEqual(scheduler.status, { running: 0, queued: 0, concurrency: 1 })
})

test('Usage retains request-time prices and diagnostics are opt-in, redacted, transient, and cleared by recall/off', () => {
  const store = new Store(':memory:'),
    runtime = new Runtime(store)
  const previous = process.env.HUMANENGINE_PHASE3_KEY
  process.env.HUMANENGINE_PHASE3_KEY = 'fixture-"escaped\\secret'
  try {
    const priced = config()
    store.set('models', priced)
    const log: ModelLog = {
      event: 'model.completed',
      modelId: 'm',
      providerId: 'p',
      role: 'thinker',
      durationMs: 12,
      usage: { inputTokens: 1000, outputTokens: 500, totalTokens: 1500 },
    }
    store.recordModel(log, priced)
    const unknown = config()
    delete unknown.models[0]!.inputUsdPerMillion
    delete unknown.models[0]!.outputUsdPerMillion
    store.recordModel(log, unknown)
    const changed = config()
    changed.models[0]!.apiModelName = 'changed-later'
    changed.models[0]!.inputUsdPerMillion = 100
    store.set('models', changed)
    store.recordModel(log, priced)
    store.recordModel(
      { ...log, usage: { inputTokens: 0, outputTokens: 0, totalTokens: 0, known: false } },
      priced,
    )
    store.recordModel(
      {
        event: 'model.failed',
        modelId: 'm',
        providerId: 'p',
        role: 'thinker',
        durationMs: 4,
        code: 'provider_http_400',
      },
      priced,
    )
    const row = store.usage()[0]!
    assert.equal(row.apiModelName, 'api-model')
    assert.equal(row.calls, 5)
    assert.equal(row.pricedCalls, 2)
    assert.equal(row.estimatedUsd, 0.008)
    assert.equal(row.inputTokens, 3000)
    assert.equal(row.outputTokens, 1500)
    const diagnostic: ModelDiagnostic = {
      modelId: 'm',
      role: 'thinker',
      messages: [
        {
          role: 'user',
          content: `private text ${process.env.HUMANENGINE_PHASE3_KEY}`,
          images: ['data:image/png;base64,sensitive-image'],
          ...{ encrypted_content: 'hidden-state-sentinel' },
        },
      ],
      output: { text: `visible ${process.env.HUMANENGINE_PHASE3_KEY}`, toolCalls: [] },
    }
    store.captureDiagnostic(diagnostic)
    assert.equal(store.diagnostics().length, 0)
    store.set('settings', { ...store.settings, debugModelIO: true })
    const persistedBefore = JSON.stringify(store.db.prepare('SELECT * FROM config').all())
    store.captureDiagnostic(diagnostic)
    let entries = store.diagnostics()
    assert.equal(entries.length, 1)
    assert.ok(!JSON.stringify(entries).includes('hidden-state-sentinel'))
    assert.ok(!JSON.stringify(entries).includes('sensitive-image'))
    assert.ok(!entries[0]!.input.includes('escaped'))
    assert.ok(!entries[0]!.output.includes('escaped'))
    assert.ok(entries[0]!.input.includes('[redacted]'))
    assert.equal(JSON.stringify(store.db.prepare('SELECT * FROM config').all()), persistedBefore)
    const requestSecret = process.env.HUMANENGINE_PHASE3_KEY!
    process.env.HUMANENGINE_PHASE3_KEY = 'rotated-fixture-key'
    store.set('models', { providers: [], models: [], routes: [] })
    store.captureDiagnostic(
      {
        ...diagnostic,
        messages: [{ role: 'user', content: JSON.stringify({ content: requestSecret }) }],
        output: { text: JSON.stringify(JSON.stringify({ content: requestSecret })), toolCalls: [] },
      },
      [requestSecret],
    )
    const rotated = store.diagnostics().at(-1)!
    assert.ok(rotated.input.includes('[redacted]'))
    assert.ok(rotated.output.includes('[redacted]'))
    assert.ok(!rotated.input.includes('escaped'))
    assert.ok(!rotated.output.includes('escaped'))
    process.env.HUMANENGINE_PHASE3_KEY = requestSecret
    store.set('models', changed)
    runtime.recall(scene.id, 'recalled-message')
    assert.equal(store.diagnostics().length, 0)
    for (let i = 0; i < 25; i++) store.captureDiagnostic(diagnostic)
    assert.equal(store.diagnostics().length, 20)
    store.set('settings', { ...store.settings, debugModelIO: false })
    assert.equal(store.diagnostics().length, 0)
    store.set('settings', { ...store.settings, debugModelIO: true })
    entries = store.diagnostics()
    assert.equal(entries.length, 0)
  } finally {
    if (previous === undefined) delete process.env.HUMANENGINE_PHASE3_KEY
    else process.env.HUMANENGINE_PHASE3_KEY = previous
    runtime.dispose()
    store.close()
  }
})

test('Readonly runtime events are deeply frozen, listener failures isolated, and queued recalls suppress old bodies', async () => {
  const store = new Store(':memory:'),
    runtime = new Runtime(store)
  const seen: Readonly<RuntimeEvent>[] = []
  let frozen = false
  runtime.on('message.received', (event) => {
    if (event.type === 'message.received') {
      frozen =
        Object.isFrozen(event) &&
        Object.isFrozen(event.message.parts) &&
        Object.isFrozen(event.message.parts[0])
      throw new Error('listener-private-body')
    }
  })
  const unsubscribe = runtime.on('message.received', (event) => {
    seen.push(event)
  })
  try {
    runtime.ingest(message('one'), scene, false)
    await tick()
    assert.equal(seen.length, 1)
    assert.equal(frozen, true)
    assert.equal(runtime.preview(scene.id)[0]!.parts[0]!.type, 'text')
    assert.ok(store.logs.some((log) => log.code === 'listener_failed'))
    assert.ok(!JSON.stringify(store.logs).includes('listener-private-body'))
    runtime.ingest(message('recalled-before-dispatch'), scene, false)
    runtime.recall(scene.id, 'recalled-before-dispatch')
    await tick()
    const recalled = seen.filter(
      (event) =>
        event.type === 'message.received' && event.message.id === 'recalled-before-dispatch',
    )
    assert.ok(
      recalled.every(
        (event) => event.type === 'message.received' && event.message.parts.length === 0,
      ),
      'queued event must not expose recalled body',
    )
    unsubscribe()
    runtime.ingest(message('unsubscribed'), scene, false)
    await tick()
    assert.ok(
      !seen.some(
        (event) => event.type === 'message.received' && event.message.id === 'unsubscribed',
      ),
    )
  } finally {
    runtime.dispose()
    store.close()
  }
})

test('Simulation never notifies real observers or changes real reply state; self events and send receipts deduplicate cache', async () => {
  const store = new Store(':memory:'),
    events: Readonly<RuntimeEvent>[] = [],
    plannerStates: unknown[] = []
  store.set('settings', { ...store.settings, debounceMs: 0, maxWaitMs: 100 })
  store.saveScene({
    ...scene,
    name: 'Fixture',
    participationEnabled: true,
    learningEnabled: false,
    proactiveEnabled: false,
  })
  const runtime = new Runtime(
    store,
    () =>
      ({
        complete: async (role: string, messages: ChatMessage[]) => {
          if (role === 'planner') {
            plannerStates.push(JSON.parse(messages[1]!.content).globalState)
            return completion([
              {
                id: 'decide',
                name: 'participation.decide',
                arguments: { action: 'reply', reasonCode: 'fixture' },
              },
            ])
          }
          if (messages.some((m) => m.role === 'tool')) return completion([])
          return completion([
            {
              id: 'send',
              name: 'messages.send',
              arguments: {
                operationId: 'one',
                segments: [{ parts: [{ type: 'text', text: 'hello' }] }],
              },
            },
          ])
        },
      }) as Router,
  )
  let sends = 0,
    resolveSent!: () => void
  const sent = new Promise<void>((resolve) => {
    resolveSent = resolve
  })
  for (const type of ['message.received', 'message.sent', 'runtime.error'] as const)
    runtime.on(type, (event) => {
      events.push(event)
      if (event.type === 'message.sent') resolveSent()
    })
  runtime.addAdapter('fixture', {
    send: async () => {
      sends++
      runtime.ingest(message('bot-receipt', 'bot'), scene, false)
      return { status: 'sent', messageId: 'bot-receipt' }
    },
    getMessage: async () => ({ items: [], coverage: 'complete' }),
    history: async () => ({ items: [], coverage: 'complete' }),
    dispose() {},
  })
  try {
    await runtime.simulate(runtime.createSimulation(), 'simulation request')
    await tick()
    assert.equal(events.length, 0)
    assert.equal(sends, 0)
    runtime.ingest(message('real-request'), scene)
    await Promise.race([
      sent,
      new Promise<never>((_, reject) => {
        const timer = setTimeout(() => reject(new Error('send_timeout')), 5000)
        timer.unref()
      }),
    ])
    await tick()
    assert.deepEqual(plannerStates[1], { secondsSinceBotSpoke: null })
    runtime.ingest(message('bot-receipt', 'bot'), scene, false)
    await tick()
    assert.equal(sends, 1)
    assert.equal(runtime.preview(scene.id).filter((m) => m.id === 'bot-receipt').length, 1)
    assert.equal(events.filter((e) => e.type === 'message.sent').length, 1)
    assert.equal(
      events.filter((e) => e.type === 'message.received' && e.message.id === 'bot-receipt').length,
      1,
    )
  } finally {
    runtime.dispose()
    store.close()
  }
})
