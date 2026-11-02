import assert from 'node:assert/strict'
import test, { type TestContext } from 'node:test'
import { setImmediate as immediate } from 'node:timers/promises'
import { Store } from '../src/store.ts'
import { Runtime, ReplyGrounding, type TurnContext, type RuntimeMessage } from '../src/runtime.ts'
import type { SceneRef } from '../src/onebot.ts'
import type { Router, Completion } from '../src/models.ts'
import { TurnWaitCoordinator } from '../src/runtime/turn-wait.ts'

const epoch = Date.parse('2026-10-28T00:00:00Z')
function fixture(t: TestContext, cacheEnabled = true) {
  t.mock.timers.enable({ apis: ['Date'], now: epoch })
  const store = new Store(':memory:')
  store.set('settings', { ...store.settings, cacheEnabled, cacheHours: 0.1 })
  let received = 0,
    disposed = 0,
    modelCalls = 0
  const runtime = new Runtime(store, () => {
    modelCalls++
    throw new Error('unexpected_external_model')
  })
  runtime.addExtension({
    tools: [],
    execute: async () => undefined,
    received: () => {
      received++
    },
  })
  const adapter = {
    send: async () => {
      throw new Error('unexpected_external_send')
    },
    history: async () => ({ items: [], coverage: 'complete' as const }),
    getMessage: async () => ({ items: [], coverage: 'complete' as const }),
    dispose: () => {
      disposed++
    },
  }
  runtime.addAdapter('offline', adapter)
  const scope = (id: string, accountId = 'a'): SceneRef => ({
    id: `${accountId}:${id}`,
    adapterId: 'offline',
    accountId,
    kind: 'group',
    platformSceneId: id,
  })
  const message = (scene: SceneRef, id = 'input'): RuntimeMessage => ({
    id,
    sceneId: scene.id,
    senderId: 'person',
    sentAt: new Date().toISOString(),
    receivedAt: new Date().toISOString(),
    origin: 'live',
    status: 'available',
    parts: [{ type: 'text', text: 'SYNTHETIC_IDLE_SCENE' }],
  })
  const ingest = (scene: SceneRef, id = 'input') => runtime.ingest(message(scene, id), scene, false)
  const internal = runtime as any
  t.after(() => {
    runtime.dispose()
    store.close()
  })
  return {
    store,
    runtime,
    internal,
    scope,
    message,
    ingest,
    adapter,
    counts: () => ({ received, disposed, modelCalls }),
    clean: () => internal.cleanupIdleScenes(),
    at: (ms: number) => t.mock.timers.setTime(epoch + ms),
  }
}

for (const cacheEnabled of [false, true])
  test(`multi-scene churn reclaims cache-only runtime objects, cache=${cacheEnabled}`, (t) => {
    const f = fixture(t, cacheEnabled)
    const counts: number[] = []
    for (let wave = 0; wave < 10; wave++) {
      f.at(wave * 600000)
      for (let i = 0; i < 100; i++) f.ingest(f.scope(`${wave}-${i}`))
      assert.equal(f.internal.states.size, 100)
      assert.equal(f.internal.scenes.size, 100)
      f.at(wave * 600000 + 400000)
      f.clean()
      counts.push(f.internal.states.size)
      assert.equal(f.internal.scenes.size, 0)
    }
    assert.deepEqual(counts, Array(10).fill(0))
    assert.equal(
      (f.store.db.prepare('SELECT count(*) n FROM scenes').get() as { n: number }).n,
      1000,
    )
    assert.equal(f.internal.adapters.get('offline'), f.adapter)
    assert.deepEqual(f.counts(), { received: 1000, disposed: 0, modelCalls: 0 })
  })

test('the runtime minute timer performs idle cleanup without a manual sweep', (t) => {
  t.mock.timers.enable({ apis: ['Date', 'setInterval'], now: epoch })
  const store = new Store(':memory:')
  store.set('settings', { ...store.settings, cacheHours: 0.1 })
  const runtime = new Runtime(store)
  const scene: SceneRef = {
    id: 'timer',
    adapterId: 'offline',
    accountId: 'a',
    kind: 'group',
    platformSceneId: 'g',
  }
  runtime.ingest(
    {
      id: 'input',
      sceneId: scene.id,
      senderId: 'person',
      sentAt: new Date().toISOString(),
      receivedAt: new Date().toISOString(),
      origin: 'live',
      status: 'available',
      parts: [],
    },
    scene,
    false,
  )
  const internal = runtime as any
  t.mock.timers.tick(360000)
  assert.equal(internal.states.size, 1)
  t.mock.timers.tick(60000)
  assert.equal(internal.states.size, 0)
  assert.equal(internal.scenes.size, 0)
  runtime.dispose()
  store.close()
})

test('all live work, replay admissions and uncertain sends remain pinned after body expiry', (t) => {
  const f = fixture(t)
  const blockers: Array<(s: any) => void> = [
    (s) =>
      s.tasks.set('task', {
        controller: new AbortController(),
        dependencies: new Set(),
        validations: [],
      }),
    (s) => s.timers.set('timer', setTimeout(() => {}, 3600000).unref()),
    (s) => s.firstPending.set('pending', epoch),
    (s) => s.pendingTurns.add('pending'),
    (s) => s.flowQueue.set('flow', { span: { finish() {} }, count: 1 }),
    (s) => s.routing.deferred.set('deferred', { schedule: false }),
    (s) => s.routing.turns.set('turn', 'topic'),
    (s) => s.routing.protectedSends.add('unknown-or-partial'),
    (s) => s.recalled.add('fail-closed-negative-state'),
    (s) => f.internal.sendGuards.set('guard', { context: { scene: { id: s.sceneId } } }),
    ...(['pending', 'active', 'waiting'] as const).map((phase) => (s: any) => {
      const admission = new TurnWaitCoordinator(
        { quietMs: 0, maxWaitMs: 0, addressedQuietMs: 0, maxModelWaitMs: 60000, maxPending: 60 },
        Date.now,
      )
      admission.observe({ id: phase, meaningful: true })
      if (phase !== 'pending') {
        const wake = admission.takeReady()!
        if (phase === 'waiting') admission.finish(wake, { waitUntil: Date.now() + 60000 })
      }
      s.admissions.set(phase, admission)
    }),
    (s) => {
      const admission = new TurnWaitCoordinator(
        { quietMs: 0, maxWaitMs: 0, addressedQuietMs: 0, maxModelWaitMs: 60000, maxPending: 60 },
        Date.now,
      )
      admission.observe({ id: 'seen', meaningful: true })
      const wake = admission.takeReady()!
      admission.finish(wake)
      assert.equal(admission.observe({ id: 'seen', meaningful: true }), 'duplicate')
      s.admissions.set('finished-replay-state', admission)
    },
  ]
  const states = blockers.map((block, i) => {
    const scene = f.scope(String(i))
    f.ingest(scene)
    const state = f.internal.states.get(scene.id)
    block(state)
    return state
  })
  f.at(400000)
  f.clean()
  assert.equal(f.internal.states.size, blockers.length)
  for (const state of states) {
    assert.equal(f.internal.states.get(state.sceneId), state)
    assert.equal(state.messages.length, 0)
  }
  const admission = states.at(-1).admissions.get('finished-replay-state')
  assert.equal(admission.observe({ id: 'seen', meaningful: true }), 'duplicate')
})

test('backward clocks, cooldowns and proactive horizon never expire early', (t) => {
  const f = fixture(t, false),
    scene = f.scope('clock')
  f.ingest(scene)
  const state = f.internal.states.get(scene.id)
  f.at(-100000)
  f.clean()
  assert.equal(f.internal.states.get(scene.id), state)
  state.lastAutomaticTurn.group = epoch + 120000
  f.at(61000)
  f.clean()
  assert.equal(f.internal.states.get(scene.id), state)
  state.lastAutomaticTurn = {}
  f.store.saveScene({ ...f.store.scene(scene.id)!, proactiveEnabled: true })
  f.at(400000)
  f.clean()
  assert.equal(f.internal.states.get(scene.id), state)
  f.at(1800001)
  f.clean()
  assert.equal(f.internal.states.size, 0)
})

test('cache TTL still protects retained bodies and rejoin builds fresh runtime state', (t) => {
  const f = fixture(t),
    scene = f.scope('future-body')
  const message = f.message(scene)
  message.receivedAt = new Date(epoch + 600000).toISOString()
  f.runtime.ingest(message, scene, false)
  const state = f.internal.states.get(scene.id)
  f.at(400000)
  f.clean()
  assert.equal(f.internal.states.get(scene.id), state)
  f.at(1000000)
  f.clean()
  assert.equal(f.internal.states.size, 0)
  f.ingest(scene, 'fresh')
  assert.notEqual(f.internal.states.get(scene.id), state)
  assert.equal(state.closed, true)
  assert.equal(f.runtime.preview(scene.id)[0]?.id, 'fresh')
})

test('rejoin preserves durable recall/source authority, receipt identity and account isolation', (t) => {
  const f = fixture(t)
  f.store.set('settings', { ...f.store.settings, sourceEventRevisionsEnabled: true })
  const a = f.scope('same', 'a'),
    b = f.scope('same', 'b')
  f.ingest(a, 'recalled')
  f.ingest(a, 'registered')
  f.runtime.recall(a.id, 'recalled')
  const receipt = [{ status: 'unknown' }]
  f.internal.operations.set(`${a.id}|old-turn|operation`, receipt)
  f.at(400000)
  f.clean()
  assert.equal(f.internal.states.size, 0)
  assert.equal(f.internal.operations.get(`${a.id}|old-turn|operation`), receipt)
  f.ingest(a, 'recalled')
  f.ingest(a, 'registered')
  assert.equal(f.counts().received, 2)
  f.ingest(a, 'fresh')
  f.ingest(b, 'registered')
  assert.equal(f.counts().received, 4)
  assert.equal(f.store.recalled(a.id, 'recalled'), true)
  assert.equal(f.store.recalled(b.id, 'recalled'), false)
  f.runtime.removeAdapter('offline')
  assert.equal(f.counts().disposed, 1)
})

test('shutdown clears scene registry and late model completion cannot recreate state', async (t) => {
  t.mock.timers.enable({ apis: ['Date'], now: epoch })
  const store = new Store(':memory:')
  let release!: (value: Completion) => void
  const runtime = new Runtime(
    store,
    () =>
      ({
        complete: () =>
          new Promise((resolve) => {
            release = resolve
          }),
      }) as unknown as Router,
  )
  const scene: SceneRef = {
    id: 'shutdown',
    adapterId: 'offline',
    accountId: 'a',
    kind: 'group',
    platformSceneId: 'g',
  }
  store.saveScene({
    ...scene,
    name: 'offline',
    participationEnabled: true,
    learningEnabled: false,
    proactiveEnabled: false,
  })
  runtime.addAdapter('offline', {
    send: async () => {
      throw new Error('no_external_send')
    },
    getMessage: async () => ({ items: [], coverage: 'complete' }),
    history: async () => ({ items: [], coverage: 'complete' }),
    dispose() {},
  })
  runtime.ingest(
    {
      id: 'input',
      sceneId: scene.id,
      senderId: 'person',
      sentAt: new Date().toISOString(),
      receivedAt: new Date().toISOString(),
      origin: 'live',
      status: 'available',
      parts: [{ type: 'text', text: 'SYNTHETIC_SHUTDOWN' }],
    },
    scene,
    false,
  )
  const internal = runtime as any,
    state = internal.states.get(scene.id)
  const running = internal.run(scene, state.topics.keys().next().value, false)
  await immediate()
  runtime.dispose()
  assert.equal(state.closed, true)
  if (release)
    release({
      text: '',
      modelId: 'offline',
      toolCalls: [
        {
          id: 'silent',
          name: 'participation.decide',
          arguments: { action: 'silent', reasonCode: 'offline' },
        },
      ],
      assistant: { role: 'assistant', content: '' },
      usage: { inputTokens: 0, outputTokens: 0, totalTokens: 0 },
    })
  await running
  await immediate()
  assert.equal(internal.states.size, 0)
  assert.equal(internal.scenes.size, 0)
  store.close()
})

for (const outcome of ['late-unknown', 'same-call-sent'] as const)
  test(`adapter send ${outcome} after disposal cannot recreate runtime state`, async (t) => {
    const f = fixture(t)
    f.store.set('settings', { ...f.store.settings, topicAdmissionEnabled: true })
    const scene = f.scope('pending-send')
    f.ingest(scene)
    f.store.saveScene({ ...f.store.scene(scene.id)!, participationEnabled: true })
    const state = f.internal.states.get(scene.id)
    const controller = new AbortController()
    const context: TurnContext = {
      scene,
      mode: 'real',
      turnId: 'pending-turn',
      signal: controller.signal,
      dependencies: new Set(),
      validations: [],
    }
    state.tasks.set('topic', {
      controller,
      dependencies: context.dependencies,
      validations: [],
      context,
    })
    state.routing.turns.set(context.turnId, 'topic')
    f.internal.sendGuards.set(context.turnId, {
      context,
      grounding: new ReplyGrounding(),
      validate: async () => true,
    })
    let release!: (value: { status: 'unknown' }) => void
    let sends = 0
    f.runtime.addAdapter('offline', {
      ...f.adapter,
      send: async () => {
        sends++
        if (outcome === 'same-call-sent') {
          f.runtime.dispose()
          return { status: 'sent', messageId: 'authoritative-receipt' }
        }
        return new Promise<{ status: 'unknown' }>((resolve) => {
          release = resolve
        })
      },
    })
    // Adapter replacement above deliberately cancels existing work in topic mode. Bind fresh work.
    const fresh = new AbortController()
    context.signal = fresh.signal
    state.tasks.set('topic', {
      controller: fresh,
      dependencies: context.dependencies,
      validations: [],
      context,
    })
    const pending = f.internal.send(context, {
      operationId: 'one-attempt',
      segments: [{ parts: [{ type: 'text', text: 'SYNTHETIC_SEND' }] }],
    })
    await immediate()
    assert.equal(sends, 1)
    if (outcome === 'late-unknown') f.runtime.dispose()
    const receipts = await pending
    assert.equal(receipts[0]?.status, outcome === 'late-unknown' ? 'unknown' : 'sent')
    if (outcome === 'same-call-sent') assert.equal(receipts[0]?.messageId, 'authoritative-receipt')
    if (release) release({ status: 'unknown' })
    await immediate()
    assert.equal(f.internal.states.size, 0)
    assert.equal(f.internal.scenes.size, 0)
    assert.equal(f.internal.operations.size, 0)
    assert.equal(sends, 1)
  })
