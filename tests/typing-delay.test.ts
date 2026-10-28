import { fixturePlanCompletion } from './helpers/reply-plan.ts'
import test from 'node:test'
import assert from 'node:assert/strict'
import { setTimeout as sleep } from 'node:timers/promises'
import { Store } from '../src/store.ts'
import { Runtime } from '../src/runtime.ts'
import type { Router, Completion, ChatMessage } from '../src/models.ts'
import type { SceneRef, SendSegment } from '../src/onebot.ts'
import {
  TypingDelayConfigSchema,
  createTypingDelay,
  typingDelayMs,
  MAX_TYPING_DELAY_MS,
} from '../src/typing-delay.ts'

const segment = (text: string): SendSegment => ({ parts: [{ type: 'text', text }] })
const scene: SceneRef = {
  id: 'typing-scene',
  adapterId: 'typing-fixture',
  accountId: 'bot',
  kind: 'group',
  platformSceneId: 'test',
}
const completion = (toolCalls: Completion['toolCalls']): Completion => ({
  text: '',
  toolCalls,
  modelId: 'fixture',
  usage: { inputTokens: 0, outputTokens: 0, totalTokens: 0 },
  assistant: { role: 'assistant', content: '', toolCalls },
})
function harness(segments: SendSegment[], speedFactor?: number, pluginSend = false) {
  const store = new Store(':memory:')
  store.set('settings', { ...store.settings, debounceMs: 0, maxWaitMs: 100 })
  store.saveScene({
    ...scene,
    name: 'Fixture',
    participationEnabled: true,
    learningEnabled: false,
    proactiveEnabled: false,
  })
  let calls = 0
  let revalidations = 0
  const runtime = new Runtime(
    store,
    () =>
      ({
        complete: async (role: string, messages: ChatMessage[]) => {
          if (role === 'planner' && messages[0]?.content.includes('Validate the exact proposed')) {
            revalidations++
            return completion([
              { id: 'validate', name: 'reply.validate', arguments: { valid: false } },
            ])
          }
          const fixturePlan = fixturePlanCompletion(role, messages, {
            deliveryTool: pluginSend ? 'fixture.send' : 'messages.send',
          })
          if (fixturePlan) return fixturePlan
          if (role === 'planner')
            return completion([
              {
                id: 'decision',
                name: 'participation.decide',
                arguments: { action: 'reply', reasonCode: 'fixture' },
              },
            ])
          return completion(
            ++calls === 1
              ? [
                  {
                    id: 'send',
                    name: pluginSend ? 'fixture.send' : 'messages.send',
                    arguments: {
                      operationId: 'fixture-operation',
                      segments,
                    },
                  },
                ]
              : [],
          )
        },
      }) as Router,
  )
  if (pluginSend)
    runtime.addExtension({
      tools: [
        {
          name: 'fixture.send',
          effect: 'send',
          description: 'Fixture core-routed send',
          inputSchema: { type: 'object' },
        },
      ],
      execute: async (_name, input, context) =>
        runtime.sendFromPlugin(context, input, context.signal),
    })
  const sent: Array<{ segment: SendSegment; time: number }> = []
  runtime.addAdapter(scene.adapterId, {
    send: async (_scene, outgoing) => {
      sent.push({ segment: outgoing, time: Date.now() })
      return { status: 'sent', messageId: `sent-${sent.length}` }
    },
    getMessage: async () => ({ items: [], coverage: 'complete' }),
    history: async () => ({ items: [], coverage: 'complete' }),
    dispose() {},
  })
  const plugin = new AbortController()
  const cleanup =
    speedFactor === undefined
      ? () => {}
      : runtime.installTypingDelay({ speedFactor }, plugin.signal)
  let entered!: () => void
  const prepared = new Promise<void>((resolve) => {
    entered = resolve
  })
  runtime.beforeSend({
    id: 'prepared',
    priority: 0,
    handler: async (parts) => {
      entered()
      return { action: 'continue', segments: parts }
    },
  })
  return {
    store,
    get revalidations() {
      return revalidations
    },
    runtime,
    sent,
    plugin,
    prepared,
    start() {
      const now = new Date().toISOString()
      runtime.ingest(
        {
          id: 'source',
          sceneId: scene.id,
          senderId: 'tester',
          sentAt: now,
          receivedAt: now,
          origin: 'live',
          status: 'available',
          parts: segment('test').parts,
        },
        scene,
      )
    },
    async settle() {
      const deadline = Date.now() + 2500
      while (runtime.activeTasks && Date.now() < deadline) await sleep(5)
      assert.equal(runtime.activeTasks, 0)
    },
    close() {
      cleanup()
      runtime.dispose()
      store.close()
    },
  }
}

test('typing uses graphemes across a prepared segment, not sentences, with validated bounded speed', async () => {
  assert.deepEqual(TypingDelayConfigSchema.parse({}), { speedFactor: 1 })
  assert.equal(typingDelayMs(segment('👨‍👩‍👧‍👦')), 500)
  assert.equal(typingDelayMs(segment('e\u0301')), 500)
  assert.equal(typingDelayMs(segment('A. B.')), 1300)
  assert.equal(typingDelayMs(segment('x'.repeat(12000))), MAX_TYPING_DELAY_MS)
  assert.equal(typingDelayMs(segment('x'), { speedFactor: 10 }), 50)
  for (const speedFactor of [0, -1, Infinity, NaN, 11, 0.01])
    assert.throws(() => typingDelayMs(segment('x'), { speedFactor }))
  const policy = createTypingDelay({}, new AbortController().signal)
  const deadline = AbortSignal.timeout(5)
  await assert.rejects(policy.wait(segment('large pending segment'), deadline))
  policy.dispose()
})

test('disabled default sends immediately; enabled delay runs once per segment in order', async () => {
  const normal = harness([segment('a'), segment('b')])
  try {
    normal.start()
    await normal.prepared
    await normal.settle()
    assert.equal(normal.sent.length, 2)
  } finally {
    normal.close()
  }
  const h = harness([segment('a'), segment('b')], 10)
  try {
    const start = Date.now()
    h.start()
    await h.prepared
    assert.equal(h.sent.length, 0)
    await h.settle()
    assert.deepEqual(
      h.sent.map((s) => s.segment),
      [segment('a'), segment('b')],
    )
    assert.ok(h.sent[0]!.time - start >= 45)
    assert.ok(h.sent[1]!.time - h.sent[0]!.time >= 45)
  } finally {
    h.close()
  }
})

for (const reason of ['disable', 'recall', 'shutdown'] as const) {
  test(`${reason} during typing cancels pending transport and leaves no bot receipt`, async () => {
    const h = harness([{ ...segment('long enough to remain pending'), replyToId: 'source' }], 1)
    try {
      h.start()
      await h.prepared
      await sleep(10)
      if (reason === 'disable') h.plugin.abort('plugin_disabled')
      else if (reason === 'recall') h.runtime.recall(scene.id, 'source')
      else h.runtime.dispose()
      await h.settle()
      assert.equal(h.sent.length, 0)
      if (reason !== 'shutdown')
        assert.equal(h.runtime.preview(scene.id).filter((m) => m.senderId === 'bot').length, 0)
      if (reason === 'disable')
        assert.ok(
          h.store.logs.some((row) => row.stage === 'send_segment' && row.status === 'cancelled'),
        )
    } finally {
      h.close()
    }
  })
}

test('disabling after the first delivered segment preserves its receipt and cancels the rest', async () => {
  const h = harness([segment('a'), segment('b')], 10)
  h.runtime.on('message.sent', () => h.plugin.abort('plugin_disabled'))
  try {
    h.start()
    await h.prepared
    await h.settle()
    assert.equal(h.sent.length, 1)
    assert.equal(h.runtime.preview(scene.id).filter((m) => m.senderId === 'bot').length, 1)
    assert.ok(
      h.store.logs.some(
        (row) =>
          row.stage === 'turn' &&
          row.counts?.sentSegments === 1 &&
          row.counts?.cancelledSegments === 1,
      ),
    )
  } finally {
    h.close()
  }
})

test('simulation never waits on enabled typing or sends externally', async () => {
  const h = harness([segment('x'.repeat(12000))], 0.1)
  try {
    const id = h.runtime.createSimulation()
    const result = await Promise.race([
      h.runtime.simulate(id, 'test'),
      sleep(500).then(() => {
        throw new Error('simulation_delayed')
      }),
    ])
    assert.equal(result.filter((m) => m.senderId === 'bot').length, 1)
    assert.equal(h.sent.length, 0)
  } finally {
    h.close()
  }
})

test('plugin-routed sends use the same per-segment delay exactly once', async () => {
  const h = harness([segment('a'), segment('b')], 10, true)
  try {
    h.start()
    await h.prepared
    assert.equal(h.sent.length, 0)
    await h.settle()
    assert.equal(h.sent.length, 2)
    assert.ok(h.sent[1]!.time - h.sent[0]!.time >= 45)
  } finally {
    h.close()
  }
})

test('new correction during typing revalidates prepared content and never dispatches stale words', async () => {
  const h = harness([segment('a')], 10)
  try {
    h.start()
    await h.prepared
    await sleep(5)
    const now = new Date().toISOString()
    h.runtime.ingest(
      {
        id: 'correction',
        sceneId: scene.id,
        senderId: 'tester',
        sentAt: now,
        receivedAt: now,
        origin: 'live',
        status: 'available',
        replyToId: 'source',
        parts: segment('Correction: cancel the earlier request.').parts,
      },
      scene,
    )
    await h.settle()
    assert.equal(h.sent.length, 0)
    assert.equal(h.revalidations, 1)
  } finally {
    h.close()
  }
})
