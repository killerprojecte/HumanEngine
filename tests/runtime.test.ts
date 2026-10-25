import test from 'node:test'
import assert from 'node:assert/strict'
import { Store } from '../src/store.ts'
import { Runtime, type RuntimeAdapter, type RuntimeMessage } from '../src/runtime.ts'
import type { Router, Completion, ChatMessage } from '../src/models.ts'
import type { SceneRef, SendResult } from '../src/onebot.ts'

const result = (calls: Completion['toolCalls'], text = ''): Completion => ({
  text,
  toolCalls: calls,
  modelId: 'fixture',
  usage: { inputTokens: 0, outputTokens: 0, totalTokens: 0 },
  assistant: { role: 'assistant', content: text, toolCalls: calls },
})
const scene: SceneRef = {
  id: 'scene-a',
  kind: 'group',
  adapterId: 'fixture',
  accountId: 'bot',
  platformSceneId: 'group',
}
const message = (id: string): RuntimeMessage => ({
  id,
  sceneId: scene.id,
  senderId: 'person',
  sentAt: new Date().toISOString(),
  receivedAt: new Date().toISOString(),
  origin: 'live',
  status: 'available',
  parts: [{ type: 'text', text: 'hello' }],
})
const send = (operationId = 'send-1') => ({
  id: 'call-send',
  name: 'messages.send',
  arguments: {
    operationId,
    segments: [
      { parts: [{ type: 'text', text: 'Hello' }] },
      { parts: [{ type: 'text', text: 'Follow-up' }] },
    ],
  },
})
const delay = (ms: number) => new Promise((resolve) => setTimeout(resolve, ms))
function setup(complete: (role: string, messages: ChatMessage[]) => Promise<Completion>) {
  const store = new Store(':memory:')
  store.set('settings', { ...store.settings, debounceMs: 0, maxWaitMs: 100 })
  const runtime = new Runtime(store, () => ({ complete }) as Router)
  return { store, runtime }
}

test('simulation uses Planner and tool sends, ignores terminal text, de-duplicates operations, and never persists or sends externally', async () => {
  let thinker = 0,
    realSends = 0
  const { store, runtime } = setup(async (role) =>
    role === 'planner'
      ? result([
          {
            id: 'decision',
            name: 'participation.decide',
            arguments: { action: 'reply', reasonCode: 'addressed' },
          },
        ])
      : ++thinker <= 2
        ? result([send()])
        : result([], 'Do not send this final text'),
  )
  runtime.addAdapter('simulation', {
    send: async () => {
      realSends++
      return { status: 'sent' }
    },
    getMessage: async () => ({ items: [], coverage: 'complete' }),
    history: async () => ({ items: [], coverage: 'complete' }),
    dispose() {},
  })
  try {
    const before = store.db.prepare('SELECT count(*) AS n FROM config').get()
    const id = runtime.createSimulation(),
      messages = await runtime.simulate(id, 'Hi')
    assert.equal(messages.length, 3)
    assert.equal(messages.filter((m) => m.senderId === 'bot').length, 2)
    assert.equal(realSends, 0)
    assert.deepEqual(store.db.prepare('SELECT count(*) AS n FROM config').get(), before)
    runtime.recall(id, messages[0]!.id)
    assert.deepEqual(store.db.prepare('SELECT count(*) AS n FROM recall_tombstones').get(), {
      n: 0,
    })
    runtime.endSimulation(id)
    assert.equal(runtime.activeTasks, 0)
    await assert.rejects(runtime.simulate(id, 'Again'), /simulation_missing/)
  } finally {
    runtime.dispose()
    store.close()
  }
})

test('recall during generation prevents unsent replies and clears source content', async () => {
  let release: () => void = () => {},
    started: () => void = () => {}
  const waiting = new Promise<void>((resolve) => {
      started = resolve
    }),
    gate = new Promise<void>((resolve) => {
      release = resolve
    })
  let sends = 0
  const { store, runtime } = setup(async (role) => {
    if (role === 'planner')
      return result([
        {
          id: 'd',
          name: 'participation.decide',
          arguments: { action: 'reply', reasonCode: 'reply' },
        },
      ])
    started()
    await gate
    return result([send()])
  })
  store.saveScene({
    ...scene,
    name: 'Group',
    participationEnabled: true,
    learningEnabled: true,
    proactiveEnabled: false,
  })
  runtime.addAdapter('fixture', {
    send: async () => {
      sends++
      return { status: 'sent' }
    },
    getMessage: async () => ({
      items: [{ ...message('m1'), origin: 'history' }],
      coverage: 'complete',
    }),
    history: async () => ({ items: [], coverage: 'complete' }),
    dispose() {},
  })
  try {
    runtime.ingest(message('m1'), scene)
    await waiting
    runtime.recall(scene.id, 'm1')
    release()
    await delay(30)
    assert.equal(sends, 0)
    assert.equal(runtime.preview(scene.id)[0]?.status, 'recalled')
    assert.deepEqual(runtime.preview(scene.id)[0]?.parts, [])
    assert.equal(store.recalled(scene.id, 'm1'), true)
  } finally {
    runtime.dispose()
    store.close()
  }
})

test('participation disable between segments preserves delivered receipt and cancels remaining segments; hook cancellation is terminal', async () => {
  let turns = 0,
    sends = 0
  const { store, runtime } = setup(async (role) =>
    role === 'planner'
      ? result([
          {
            id: 'd',
            name: 'participation.decide',
            arguments: { action: 'reply', reasonCode: 'reply' },
          },
        ])
      : ++turns === 1
        ? result([send()])
        : result([]),
  )
  const config = {
    ...scene,
    name: 'Group',
    participationEnabled: true,
    learningEnabled: true,
    proactiveEnabled: false,
  }
  store.saveScene(config)
  const adapter: RuntimeAdapter = {
    send: async () => {
      sends++
      store.saveScene({ ...config, participationEnabled: false })
      runtime.sceneChanged({ ...config, participationEnabled: false })
      return { status: 'sent', messageId: 'sent-1' }
    },
    getMessage: async () => ({ items: [], coverage: 'complete' }),
    history: async () => ({ items: [], coverage: 'complete' }),
    dispose() {},
  }
  runtime.addAdapter('fixture', adapter)
  try {
    runtime.ingest(message('m1'), scene)
    await delay(40)
    assert.equal(sends, 1)
    assert.equal(runtime.preview(scene.id).filter((m) => m.senderId === 'bot').length, 1)
    let laterHook = 0
    runtime.beforeSend({
      id: 'cancel',
      priority: 0,
      handler: async () => ({ action: 'cancel', reason: 'blocked' }),
    })
    runtime.beforeSend({
      id: 'later',
      priority: 1,
      handler: async (segments) => {
        laterHook++
        return { action: 'continue', segments }
      },
    })
    const simulation = runtime.createSimulation()
    turns = 0
    const messages = await runtime.simulate(simulation, 'Hi')
    assert.equal(laterHook, 0)
    assert.equal(messages.filter((m) => m.senderId === 'bot').length, 0)
  } finally {
    runtime.dispose()
    store.close()
  }
})

test('quiet scene cache expires on reads and bot-origin ingress never schedules a reply', async () => {
  let calls = 0
  const { store, runtime } = setup(async () => {
    calls++
    return result([])
  })
  store.saveScene({
    ...scene,
    name: 'Group',
    participationEnabled: true,
    learningEnabled: true,
    proactiveEnabled: false,
  })
  const originalNow = Date.now
  try {
    runtime.ingest({ ...message('self'), senderId: 'bot' }, scene)
    await delay(20)
    assert.equal(calls, 0)
    Date.now = () => originalNow() + 25 * 3600000
    assert.deepEqual(runtime.preview(scene.id), [])
  } finally {
    Date.now = originalNow
    runtime.dispose()
    store.close()
  }
})

test('new correction validates the exact stale segments and replans rather than sending the old answer', async () => {
  let release: () => void = () => {},
    started: () => void = () => {},
    thinker = 0,
    sends = 0
  const gate = new Promise<void>((resolve) => (release = resolve)),
    waiting = new Promise<void>((resolve) => (started = resolve))
  const { store, runtime } = setup(async (role, messages) => {
    if (role === 'planner')
      return messages[0]?.content.includes('Validate the exact')
        ? result([{ id: 'v', name: 'reply.validate', arguments: { valid: false } }])
        : result([
            {
              id: 'd',
              name: 'participation.decide',
              arguments: { action: 'reply', reasonCode: 'reply' },
            },
          ])
    if (++thinker === 1) {
      started()
      await gate
      return result([send()])
    }
    return result([])
  })
  store.saveScene({
    ...scene,
    name: 'Group',
    participationEnabled: true,
    learningEnabled: true,
    proactiveEnabled: false,
  })
  runtime.addAdapter('fixture', {
    send: async () => {
      sends++
      return { status: 'sent' }
    },
    getMessage: async () => ({ items: [], coverage: 'complete' }),
    history: async () => ({ items: [], coverage: 'complete' }),
    dispose() {},
  })
  try {
    runtime.ingest(message('m1'), scene)
    await waiting
    runtime.ingest(
      {
        ...message('m2'),
        replyToId: 'm1',
        parts: [{ type: 'text', text: 'Correction: that is no longer true.' }],
      },
      scene,
    )
    release()
    await delay(40)
    assert.equal(sends, 0)
    assert.ok(thinker >= 2)
  } finally {
    runtime.dispose()
    store.close()
  }
})

test('semantic topics narrow thinker context and unrelated secondary recall does not cancel the main reply', async () => {
  let release: () => void = () => {},
    started: () => void = () => {}
  const gate = new Promise<void>((resolve) => {
    release = resolve
  })
  const waiting = new Promise<void>((resolve) => {
    started = resolve
  })
  let thinkerSignal: AbortSignal | undefined,
    thinkerMessages: RuntimeMessage[] = [],
    thinkerCalls = 0,
    plannerCalls = 0,
    sends = 0
  const store = new Store(':memory:')
  store.saveScene({
    ...scene,
    name: 'Group',
    participationEnabled: true,
    learningEnabled: false,
    proactiveEnabled: true,
  })
  const runtime = new Runtime(
    store,
    () =>
      ({
        complete: async (
          role: string,
          messages: ChatMessage[],
          _tools: unknown,
          signal?: AbortSignal,
        ) => {
          if (role === 'planner') {
            plannerCalls++
            const plannerInput = JSON.parse(messages[1]!.content)
            const incoming = plannerInput.messages as RuntimeMessage[]
            const collidingSecondaryId = plannerInput.topics[0].id as string
            assert.notEqual(collidingSecondaryId, 'main')
            assert.deepEqual(
              incoming.map((m) => m.id),
              ['main-message', 'secondary-message'],
            )
            return result([
              {
                id: 'decision',
                name: 'participation.decide',
                arguments: {
                  action: 'reply',
                  reasonCode: 'main_topic_addressed',
                  topicId: 'main',
                  targetMessageIds: ['main-message'],
                  topics: [
                    { topicId: 'main', messageIds: ['main-message'] },
                    { topicId: collidingSecondaryId, messageIds: ['secondary-message'] },
                  ],
                },
              },
            ])
          }
          if (++thinkerCalls > 1) return result([])
          thinkerSignal = signal
          thinkerMessages = JSON.parse(messages[1]!.content).messages as RuntimeMessage[]
          started()
          await gate
          return result([
            {
              id: 'send',
              name: 'messages.send',
              arguments: {
                operationId: 'main-reply',
                segments: [
                  {
                    parts: [{ type: 'text', text: 'Main topic answer' }],
                    replyToId: 'main-message',
                  },
                ],
              },
            },
          ])
        },
      }) as Router,
  )
  runtime.addAdapter('fixture', {
    send: async () => {
      sends++
      return { status: 'sent', messageId: 'main-reply-receipt' }
    },
    getMessage: async () => ({ items: [], coverage: 'complete' }),
    history: async () => ({ items: [], coverage: 'complete' }),
    dispose() {},
  })
  try {
    runtime.ingest(
      { ...message('main-message'), parts: [{ type: 'text', text: 'Main question for the bot' }] },
      scene,
      false,
    )
    runtime.ingest(
      {
        ...message('secondary-message'),
        parts: [{ type: 'text', text: 'Unrelated secondary conversation' }],
      },
      scene,
      false,
    )
    const running = runtime.proactive(scene.id)
    await Promise.race([
      waiting,
      running.then(() => {
        throw new Error('thinker_not_started')
      }),
    ])
    assert.deepEqual(
      thinkerMessages.map((m) => m.id),
      ['main-message'],
    )
    assert.ok(thinkerSignal)
    runtime.recall(scene.id, 'secondary-message')
    assert.equal(thinkerSignal.aborted, false)
    release()
    await running
    assert.equal(sends, 1)
    assert.equal(plannerCalls, 1, 'secondary recall must not trigger main-topic stale validation')
    assert.equal(
      runtime.preview(scene.id).find((m) => m.id === 'secondary-message')?.status,
      'recalled',
    )
    assert.equal(runtime.preview(scene.id).filter((m) => m.id === 'main-reply-receipt').length, 1)
  } finally {
    release()
    runtime.dispose()
    store.close()
  }
})

test('a coalesced batch received during a busy silent decision is replanned instead of dropped', async () => {
  let release!: () => void,
    started!: () => void,
    done!: () => void,
    decisions = 0
  const gate = new Promise<void>((resolve) => (release = resolve)),
    waiting = new Promise<void>((resolve) => (started = resolve)),
    replanned = new Promise<void>((resolve) => (done = resolve))
  const { store, runtime } = setup(async (role, messages) => {
    assert.equal(role, 'planner')
    decisions++
    if (decisions === 1) {
      started()
      await gate
    } else {
      assert.ok(messages.some((m) => m.content.includes('new-addressed')))
      done()
    }
    return result([
      {
        id: 'd',
        name: 'participation.decide',
        arguments: { action: 'silent', reasonCode: 'fixture' },
      },
    ])
  })
  store.saveScene({
    ...scene,
    name: 'Fixture',
    participationEnabled: true,
    learningEnabled: false,
    proactiveEnabled: false,
  })
  runtime.addAdapter('fixture', {
    send: async () => ({ status: 'failed' }),
    getMessage: async () => ({ items: [], coverage: 'unavailable' }),
    history: async () => ({ items: [], coverage: 'unavailable' }),
    dispose() {},
  })
  try {
    runtime.ingest(message('first'), scene)
    await waiting
    runtime.ingest(message('new-addressed'), scene)
    await delay(20)
    release()
    await Promise.race([
      replanned,
      new Promise((_, reject) => {
        const timer = setTimeout(() => reject(Error('pending_batch_lost')), 3000)
        timer.unref()
      }),
    ])
    assert.equal(decisions, 2)
  } finally {
    release()
    runtime.dispose()
    store.close()
  }
})
