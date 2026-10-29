import test from 'node:test'
import assert from 'node:assert/strict'
import { Store } from '../src/store.ts'
import { Engine } from '../src/engine.ts'
import { Runtime, type RuntimeMessage, type TurnContext } from '../src/runtime.ts'
import {
  ModelError,
  type ChatMessage,
  type Completion,
  type Router,
  type ToolDefinition,
} from '../src/models.ts'
import sharp from 'sharp'
import { OneBotError, type SceneRef, type SendResult } from '../src/onebot.ts'
import { preserveOutcomeError } from '../src/diagnostics/error-context.ts'
import { describeCurrentImages } from '../src/vision-context.ts'
import { fixtureReplyPlan } from './helpers/reply-plan.ts'

const completion = (toolCalls: Completion['toolCalls'] = []): Completion => ({
  text: '',
  toolCalls,
  modelId: 'fixture',
  usage: { inputTokens: 0, outputTokens: 0, totalTokens: 0 },
  assistant: { role: 'assistant', content: '', toolCalls },
})
const scene: SceneRef = {
  id: 'error-outcomes',
  kind: 'group',
  adapterId: 'fixture',
  accountId: 'bot',
  platformSceneId: 'group',
}
const source = (): RuntimeMessage => ({
  id: 'source',
  sceneId: scene.id,
  senderId: 'person',
  origin: 'live',
  status: 'available',
  sentAt: new Date().toISOString(),
  receivedAt: new Date().toISOString(),
  parts: [{ type: 'text', text: 'Private fixture body: outcome-body-canary' }],
})
const sqliteError = () =>
  Object.assign(new Error('attempt to write a readonly database'), {
    name: 'SqliteError',
    code: 'SQLITE_READONLY',
  })

for (const scenario of ['hook', 'plugin', 'adapter'] as const) {
  test(`final turn retains ${scenario} send failure even when dialogue continues after tool error`, async () => {
    const store = new Store(':memory:')
    store.saveScene({
      ...scene,
      name: 'Fixture',
      participationEnabled: true,
      proactiveEnabled: true,
      learningEnabled: false,
    })
    let dialogueCalls = 0,
      adapterCalls = 0
    const deliveryTool = scenario === 'plugin' ? 'fixture.deliver' : 'messages.send'
    const complete = async (role: string, messages: ChatMessage[], tools: ToolDefinition[]) => {
      if (tools.some((t) => t.name === 'participation.decide'))
        return completion([
          {
            id: 'decision',
            name: 'participation.decide',
            arguments: { action: 'reply', reasonCode: 'fixture', targetMessageIds: ['source'] },
          },
        ])
      if (role === 'planner')
        return completion([
          {
            id: 'plan',
            name: 'reply.plan',
            arguments: fixtureReplyPlan(messages, { deliveryTool })!,
          },
        ])
      if (++dialogueCalls > 1) return completion()
      return completion([
        {
          id: 'send',
          name: deliveryTool,
          arguments: {
            operationId: 'once',
            segments: [
              { replyToId: 'source', parts: [{ type: 'text', text: 'outcome-response-canary' }] },
            ],
          },
        },
      ])
    }
    const runtime = new Runtime(store, () => ({ complete }) as Router)
    runtime.addAdapter('fixture', {
      send: async (): Promise<SendResult> => {
        adapterCalls++
        return preserveOutcomeError(
          { status: 'unknown', detail: 'http_action_failed' },
          new OneBotError('http_action_failed', true, { httpStatus: 429 }),
          { operation: 'adapter.send', stage: 'send_segment' },
        )
      },
      getMessage: async () => ({ items: [], coverage: 'complete' }),
      history: async () => ({ items: [], coverage: 'complete' }),
      dispose() {},
    })
    if (scenario === 'hook')
      runtime.beforeSend({
        id: 'failing-hook',
        priority: 0,
        handler: async () => {
          throw new TypeError(
            "Cannot read properties of undefined (reading 'outcome-field-canary')",
          )
        },
      })
    if (scenario === 'plugin')
      runtime.addExtension({
        tools: [
          {
            name: deliveryTool,
            effect: 'send',
            description: 'Fixture delivery',
            inputSchema: { type: 'object' },
          },
        ],
        execute: async () => {
          throw sqliteError()
        },
      })
    try {
      runtime.ingest(source(), scene, false)
      await runtime.proactive(scene.id)
      const turn = store.logs.find(
        (row) => row.stage === 'turn' && ['failed', 'partial'].includes(row.status ?? ''),
      )
      assert.ok(turn, 'final turn must not report all work as completed')
      assert.ok(turn.rootCause, 'final turn must preserve the actual failed operation root cause')
      if (scenario === 'hook') {
        assert.equal(turn.rootCause.errorType, 'TypeError')
        assert.match(turn.rootCause.errorMessage!, /Cannot read a property/)
        assert.equal(adapterCalls, 0)
      } else if (scenario === 'plugin') {
        assert.equal(turn.rootCause.errorCode, 'database_readonly')
        assert.equal(turn.rootCause.systemCode, 'SQLITE_READONLY')
        assert.equal(adapterCalls, 0)
      } else {
        assert.equal(turn.rootCause.httpStatus, 429)
        assert.equal(turn.rootCause.errorCode, 'http_action_failed')
        assert.equal(turn.counts?.unknownSegments, 1)
        assert.equal(adapterCalls, 1)
      }
      assert.ok(turn.rootCause.errorContext?.operation)
      assert.ok(turn.rootCause.adviceCode)
      assert.doesNotMatch(JSON.stringify(store.logs), /outcome-(?:body|response|field)-canary/)
    } finally {
      runtime.dispose()
      store.close()
    }
  })
}

test('learning persistence database failure reaches final batch with partial-write semantics and root cause', async () => {
  const store = new Store(':memory:')
  store.saveScene({
    ...scene,
    name: 'Fixture',
    participationEnabled: false,
    proactiveEnabled: false,
    learningEnabled: true,
  })
  store.set('settings', { ...store.settings, learningBatchMs: 300000 })
  const engine = new Engine(store)
  engine.router = () =>
    ({
      complete: async () =>
        completion([
          {
            id: 'learn',
            name: 'learning.propose',
            arguments: {
              content: 'A participant prefers brief replies.',
              kind: 'fact',
              sourceMessageIds: ['source'],
            },
          },
        ]),
    }) as unknown as Router
  engine.indexMemory = async () => {
    throw sqliteError()
  }
  const queue = engine.learning as unknown as {
    enqueueLearning(message: RuntimeMessage): void
    learn(id: string): Promise<void>
    learningRetries: Map<string, unknown>
  }
  try {
    queue.enqueueLearning(source())
    await queue.learn(scene.id)
    const batch = store.logs.find(
      (row) => row.stage === 'learning.batch' && row.status === 'partial',
    )
    const persist = store.logs.find(
      (row) => row.stage === 'learning.persist' && row.status === 'partial',
    )
    assert.ok(batch?.rootCause)
    assert.ok(persist?.rootCause)
    assert.equal(batch.rootCause.errorCode, 'database_readonly')
    assert.equal(batch.rootCause.systemCode, 'SQLITE_READONLY')
    assert.equal(persist.rootCause.errorCode, 'database_readonly')
    assert.equal(
      engine.memory.adminList().length,
      1,
      'confirmed memory write must not be called fully cancelled',
    )
    assert.equal(queue.learningRetries.size, 0, 'uncertain partial persistence must not replay')
    assert.doesNotMatch(JSON.stringify(store.logs), /outcome-body-canary/)
  } finally {
    await engine.dispose()
    store.close()
  }
})

for (const phase of ['download', 'vision'] as const)
  test(`image ${phase} failure stays explicit and recoverable with image-phase root cause`, async () => {
    const store = new Store(':memory:')
    store.saveScene({
      ...scene,
      name: 'Fixture',
      participationEnabled: true,
      proactiveEnabled: false,
      learningEnabled: false,
    })
    store.set('models', {
      providers: [
        {
          id: 'p',
          name: 'Fixture',
          protocol: 'openai-chat',
          baseUrl: 'https://fixture.invalid',
          credentialEnv: '',
        },
      ],
      models: [
        {
          id: 'v',
          displayName: 'Vision',
          providerId: 'p',
          apiModelName: 'vision',
          capabilities: ['vision'],
        },
      ],
      routes: [{ role: 'vision', orderedModelIds: ['v'], timeoutMs: 1000 }],
    })
    const message = source()
    message.parts = [
      { type: 'media', assetId: 'outcome-private-image-canary', mediaType: 'image/unknown' },
    ]
    const context: TurnContext = {
      scene,
      mode: 'real',
      turnId: 'vision-turn',
      signal: new AbortController().signal,
      dependencies: new Set(),
      validations: [],
    }
    let calls = 0
    const png = await sharp({ create: { width: 2, height: 2, channels: 3, background: '#00f' } })
      .png()
      .toBuffer()
    const runtime = {
      preview: () => [message],
      imageSourceAvailable: () => true,
      readImage: async () => {
        if (phase === 'vision') return png
        throw Object.assign(
          new Error('read ECONNRESET https://fixture.invalid/?token=outcome-url-canary'),
          { code: 'ECONNRESET' },
        )
      },
    } as unknown as Runtime
    try {
      const described = await describeCurrentImages(
        store,
        runtime,
        () =>
          ({
            complete: async () => {
              calls++
              throw new ModelError('provider_http_503', 503)
            },
          }) as unknown as Router,
        context,
        [message],
      )
      assert.equal(described.images[0]?.status, 'unavailable')
      assert.equal(calls, phase === 'download' ? 0 : 1)
      const image = store.logs.find(
        (row) => row.stage === 'vision.image' && row.status === 'failed',
      )
      assert.equal(
        image?.rootCause?.errorCode,
        phase === 'download' ? 'ECONNRESET' : 'provider_http_503',
      )
      assert.equal(
        image?.rootCause?.errorContext?.stage,
        phase === 'download' ? 'media.fetch' : 'vision.image',
      )
      const summary = store.logs.find(
        (row) => row.stage === 'vision.describe' && row.status === 'failed',
      )
      assert.equal(
        summary?.rootCause?.errorCode,
        phase === 'download' ? 'ECONNRESET' : 'provider_http_503',
      )
      assert.doesNotMatch(JSON.stringify(store.logs), /outcome-(?:private-image|url)-canary/)
    } finally {
      store.close()
    }
  })
