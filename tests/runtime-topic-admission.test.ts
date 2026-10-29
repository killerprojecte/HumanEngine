import assert from 'node:assert/strict'
import test, { type TestContext } from 'node:test'
import { setImmediate as immediate } from 'node:timers/promises'
import { Engine } from '../src/engine.ts'
import { Store } from '../src/store.ts'
import type { RuntimeMessage } from '../src/runtime.ts'
import type { ChatMessage, Completion, Router } from '../src/models.ts'
import type { Message, SceneRef, SendResult, SendSegment } from '../src/onebot.ts'
import { installAdapter, type AdapterContext } from '../src/plugins/adapters.ts'
import { fixtureReplyPlan } from './helpers/reply-plan.ts'

// Deterministic synthetic completions establish routing/lifecycle contracts, not semantic quality,
// natural-language topic recognition, scene-wide fairness, or measured production call savings.
const completion = (toolCalls: Completion['toolCalls'] = []): Completion => ({
  text: '',
  toolCalls,
  modelId: 'synthetic-topic-script',
  usage: { inputTokens: 0, outputTokens: 0, totalTokens: 0 },
  assistant: { role: 'assistant', content: '', toolCalls },
})
const call = (name: string, args: Record<string, unknown>) => ({ id: name, name, arguments: args })
const decision = (
  action: 'silent' | 'wait' | 'reply' = 'silent',
  patch: Record<string, unknown> = {},
) =>
  completion([
    call('participation.decide', { action, reasonCode: 'synthetic_topic_contract', ...patch }),
  ])
const send = (segments = 1) =>
  completion([
    call('messages.send', {
      operationId: 'synthetic-single-attempt',
      segments: Array.from({ length: segments }, (_, index) => ({
        parts: [{ type: 'text', text: `Synthetic answer ${index + 1}.` }],
      })),
    }),
  ])
function gate() {
  let release!: () => void
  const promise = new Promise<void>((resolve) => {
    release = resolve
  })
  return { promise, release }
}
async function advance(t: TestContext, ms: number) {
  t.mock.timers.tick(ms)
  for (let i = 0; i < 8; i++) {
    await immediate()
    t.mock.timers.tick(0)
  }
}
type ReadonlyRouting = {
  topics: Map<string, { id: string; messageIds: string[]; version: number }>
  routing: {
    deferred: Map<string, { schedule: boolean }>
    acknowledged: Map<string, string>
    protectedSends: Set<string>
    metadata: Map<string, { provisional: boolean; cue?: { initiatorId: string; label: string } }>
  }
}
type DecisionInput = {
  messages: RuntimeMessage[]
  topics: Array<{ id: string; messageIds: string[] }>
}
function fixture(t: TestContext, enabled?: boolean) {
  t.mock.timers.enable({
    apis: ['Date', 'setTimeout', 'setInterval'],
    now: new Date('2026-10-29T02:00:00Z'),
  })
  let network = 0
  t.mock.method(globalThis, 'fetch', async () => {
    network++
    throw Error('Unexpected network')
  })
  const store = new Store(':memory:')
  store.set('settings', {
    ...store.settings,
    ...(enabled === undefined ? {} : { topicAdmissionEnabled: enabled }),
    sourceEventRevisionsEnabled: true,
    debounceMs: 100,
    maxWaitMs: 500,
  })
  const engine = new Engine(store),
    runtime = engine.runtime
  const received = new Map<string, number>(),
    edited: string[] = [],
    decisions: DecisionInput[] = [],
    inputs: ChatMessage[][] = [],
    sends: SendSegment[] = []
  const calls = { decision: 0, plan: 0, reply: 0, validation: 0 }
  let decide: (
    input: DecisionInput,
    index: number,
    signal?: AbortSignal,
  ) => Completion | Promise<Completion> = () => decision()
  let reply: (messages: ChatMessage[], index: number) => Completion | Promise<Completion> = () =>
    completion()
  let plan: (messages: ChatMessage[], index: number) => Completion | Promise<Completion> = (
    messages,
  ) => {
    const value = fixtureReplyPlan(messages)
    assert.ok(value)
    return completion([call('reply.plan', value)])
  }
  let validate: (messages: ChatMessage[]) => Completion | Promise<Completion> = () =>
    completion([call('reply.validate', { valid: true })])
  let historyItems: Message[] = []
  const historyReads: string[] = []
  let receipt: (scope: SceneRef, index: number) => SendResult | Promise<SendResult> = (
    scope,
    index,
  ) => ({ status: 'sent', messageId: adapter.messageId(scope, `bot-${index}`) })
  engine.router = () =>
    ({
      complete: async (role, messages, tools = [], signal) => {
        inputs.push(structuredClone(messages))
        if (tools.some((tool) => tool.name === 'participation.decide')) {
          const input = JSON.parse(messages[1]!.content) as DecisionInput
          decisions.push(structuredClone(input))
          return decide(input, ++calls.decision, signal)
        }
        if (tools.some((tool) => tool.name === 'reply.plan')) {
          return plan(messages, ++calls.plan)
        }
        if (tools.some((tool) => tool.name === 'reply.validate')) {
          calls.validation++
          return validate(messages)
        }
        assert.equal(
          role,
          'replyer',
          'routing must not introduce a topic classifier or learner request',
        )
        return reply(messages, ++calls.reply)
      },
    }) as Router
  runtime.addExtension({
    tools: [],
    execute: async () => {
      throw Error('Unexpected extension action')
    },
    received: (message) => received.set(message.id, (received.get(message.id) ?? 0) + 1),
    edited: (_old, message) => {
      edited.push(message.id)
    },
  })
  let adapter!: AdapterContext
  const controllers: AbortController[] = []
  const registrations: Array<ReturnType<typeof installAdapter>> = []
  const install = () => {
    const controller = new AbortController()
    controllers.push(controller)
    const registration = installAdapter(
      'synthetic-topic',
      (ctx) => {
        adapter = ctx
        return {
          send: async (scope, segment) => {
            sends.push(structuredClone(segment))
            return receipt(scope, sends.length)
          },
          history: async () => {
            historyReads.push('history')
            return { items: structuredClone(historyItems), coverage: 'complete' }
          },
          getMessage: async (_scope, id) => {
            historyReads.push('getMessage')
            return {
              items: structuredClone(historyItems.filter((item) => item.id === id)),
              coverage: 'complete',
            }
          },
          dispose() {},
        }
      },
      runtime,
      store,
      controller.signal,
      {},
    )
    registrations.push(registration)
  }
  install()
  const scene = adapter.scene('synthetic-account', 'group', 'main'),
    other = adapter.scene('synthetic-account', 'group', 'other')
  for (const scope of [scene, other])
    store.saveScene({
      ...scope,
      name: 'Synthetic topics',
      participationEnabled: true,
      learningEnabled: false,
      proactiveEnabled: false,
    })
  const state = (scope = scene) =>
    (runtime as unknown as { states: Map<string, ReadonlyRouting> }).states.get(scope.id)!
  const message = (id: string, text: string, senderId = 'person-a', scope = scene): Message => ({
    id: adapter.messageId(scope, id),
    sceneId: scope.id,
    senderId,
    origin: 'live',
    status: 'available',
    sentAt: new Date().toISOString(),
    receivedAt: new Date().toISOString(),
    parts: [{ type: 'text', text }],
  })
  return {
    store,
    engine,
    runtime,
    scene,
    other,
    calls,
    decisions,
    inputs,
    sends,
    received,
    edited,
    historyReads,
    state,
    message,
    decide(fn: typeof decide) {
      decide = fn
    },
    reply(fn: typeof reply) {
      reply = fn
    },
    plan(fn: typeof plan) {
      plan = fn
    },
    validate(fn: typeof validate) {
      validate = fn
    },
    history(items: Message[]) {
      historyItems = structuredClone(items)
    },
    receipt(fn: typeof receipt) {
      receipt = fn
    },
    receive(value: Message, scope = scene) {
      adapter.receive(value, scope, false)
    },
    ingest(id: string, text: string, senderId = 'person-a', scope = scene) {
      const value = message(id, text, senderId, scope)
      adapter.receive(value, scope, false)
      return value
    },
    owner(id: string, scope = scene) {
      return [...state(scope).topics.values()].find((topic) => topic.messageIds.includes(id))?.id
    },
    recall(value: Message, scope = scene) {
      adapter.recall(scope, value.id)
    },
    edit(value: Message, text: string, scope = scene) {
      const base = store.sourceEvents.registered(scope.id, value.id)!
      return adapter.edit({ ...value, parts: [{ type: 'text', text }] }, scope, {
        editId: 'synthetic-edit',
        order: { kind: 'observed_cas', expectedBase: base.ref },
      })
    },
    feature(value: boolean) {
      store.set('settings', { ...store.settings, topicAdmissionEnabled: value })
      engine.settingsChanged()
    },
    participation(value: boolean) {
      const changed = { ...store.scene(scene.id)!, participationEnabled: value }
      store.saveScene(changed)
      engine.sceneChanged(changed)
    },
    replaceAdapter() {
      install()
    },
    rechecks() {
      return store.logs.filter((row) => row.code === 'turn_wait_recheck')
    },
    async close() {
      try {
        registrations.at(-1)?.dispose()
        for (const controller of controllers) controller.abort()
        await engine.dispose()
      } finally {
        store.close()
      }
      assert.equal(network, 0)
    },
  }
}

for (const enabled of [undefined, true])
  test(`${enabled ? 'ON separates' : 'default OFF retains legacy coalescing for'} two explicit same-minute plans without a classifier call`, async (t) => {
    const h = fixture(t, enabled)
    try {
      assert.equal(h.store.settings.topicAdmissionEnabled, enabled ?? false)
      const a = h.ingest('plan-a', '[话题:野餐] 周六讨论计划 A。'),
        b = h.ingest('plan-b', '[topic:coffee] Discuss independent plan B.')
      assert.equal(h.runtime.sceneState(h.scene.id).topics.length, enabled ? 2 : 1)
      assert.equal(h.owner(a.id) === h.owner(b.id), !enabled)
      await advance(t, 100)
      assert.deepEqual(h.calls, { decision: enabled ? 2 : 1, plan: 0, reply: 0, validation: 0 })
      assert.equal(h.received.get(a.id), 1)
      assert.equal(h.received.get(b.id), 1)
      await advance(t, 2000)
      assert.equal(h.calls.decision, enabled ? 2 : 1)
      assert.equal(h.sends.length, 0)
    } finally {
      await h.close()
    }
  })

test('reply-to-other has exact topic priority over text cues; same labels and ambiguous sender continuity do not merge independent plans', async (t) => {
  const h = fixture(t, true)
  try {
    const a = h.ingest('a', '[topic:same] Alice plan.', 'canonical-a'),
      b = h.ingest('b', '[topic:same] Bob independent plan.', 'canonical-b')
    const aTopic = h.owner(a.id),
      bTopic = h.owner(b.id)
    assert.notEqual(aTopic, bTopic)
    const reply = {
      ...h.message('reply-other', '[topic:same] Replying specifically to Bob.', 'canonical-a'),
      replyToId: b.id,
    }
    h.receive(reply)
    assert.equal(h.owner(reply.id), bTopic)
    const ambiguous = h.ingest(
      'ambiguous',
      'Unanchored follow-up from a person active in both.',
      'canonical-a',
    )
    assert.notEqual(h.owner(ambiguous.id), aTopic)
    assert.notEqual(h.owner(ambiguous.id), bTopic)
    assert.equal(h.state().routing.metadata.get(h.owner(ambiguous.id)!)?.provisional, true)
    const scoped = h.ingest('same-local-label', '[topic:same] Other scene.', 'canonical-a', h.other)
    assert.notEqual(h.owner(scoped.id, h.other), aTopic)
    assert.equal(h.runtime.sceneState(h.scene.id).topics.length, 3)
    await advance(t, 100)
    assert.equal(h.calls.decision, 4)
    assert.equal(h.sends.length, 0)
  } finally {
    await h.close()
  }
})

for (const outcome of ['sent-id', 'sent-no-id', 'unknown'] as const)
  test(`actual ${outcome} receipt ${outcome === 'sent-id' ? 'grants' : 'does not grant'} a bot-reply topic anchor; no receipt creates an automatic retry`, async (t) => {
    const h = fixture(t, true)
    try {
      h.decide((_data, index) => decision(index === 1 ? 'reply' : 'silent'))
      h.reply((messages) =>
        messages.some((message) => message.role === 'tool') ? completion() : send(),
      )
      const syntheticAck = h.message('ack', '').id
      h.receipt(() =>
        outcome === 'sent-id'
          ? { status: 'sent', messageId: syntheticAck }
          : outcome === 'sent-no-id'
            ? { status: 'sent' }
            : { status: 'unknown', messageId: syntheticAck },
      )
      const first = h.ingest('original', '[topic:original] Original plan.')
      const original = h.owner(first.id)!
      await advance(t, 100)
      assert.equal(h.sends.length, 1)
      assert.equal(h.runtime.activeTasks, 0)
      assert.equal(h.state().routing.acknowledged.size, outcome === 'sent-id' ? 1 : 0)
      if (outcome === 'unknown') assert.ok(h.state().routing.protectedSends.has(original))
      const fallback = h.runtime
        .preview(h.scene.id)
        .find((message) => message.senderId === 'bot')?.id
      const target = outcome === 'sent-no-id' ? fallback! : syntheticAck
      assert.ok(target)
      const follow = {
        ...h.message('follow', '[topic:unrelated] Follow exact bot response.', 'new-person'),
        replyToId: target,
      }
      if (outcome === 'sent-no-id') {
        assert.throws(
          () => h.receive(follow),
          /invalid_adapter_message/,
          'display fallback UUID is not a canonical platform target',
        )
        assert.equal(h.received.get(follow.id), undefined)
        // Also check the direct Runtime compatibility boundary: a display-only reply ID
        // supplied by an internal caller is still not a platform ACK.
        h.runtime.ingest(follow, h.scene)
      } else h.receive(follow)
      if (outcome === 'sent-id') assert.equal(h.owner(follow.id), original)
      else {
        assert.notEqual(h.owner(follow.id), original)
        assert.equal(h.state().routing.metadata.get(h.owner(follow.id)!)?.provisional, true)
      }
      await advance(t, 100)
      await advance(t, 2000)
      assert.equal(h.calls.decision, 2)
      assert.equal(h.calls.plan, 1)
      assert.equal(h.sends.length, 1)
    } finally {
      await h.close()
    }
  })

test('four active slots defer a fifth input, then safely admit it once without repeating ingress/received or changing active identities', async (t) => {
  const h = fixture(t, true),
    held = Array.from({ length: 4 }, gate)
  try {
    h.decide(async (_data, index) => {
      if (index <= 4) await held[index - 1]!.promise
      return decision()
    })
    const first = Array.from({ length: 4 }, (_, index) =>
      h.ingest(`active-${index}`, `[topic:slot-${index}] Protected plan.`),
    )
    const initial = first.map((message) => h.owner(message.id)!)
    await advance(t, 100)
    assert.equal(h.runtime.activeTasks, 4)
    const fifth = h.ingest('deferred', '[topic:fifth] A fifth independent plan.')
    assert.equal(h.state().topics.size, 4)
    assert.ok(h.state().routing.deferred.has(fifth.id))
    assert.equal(h.owner(fifth.id), undefined)
    assert.equal(h.received.get(fifth.id), 1)
    await advance(t, 500)
    assert.equal(h.calls.decision, 4)
    held[0]!.release()
    await advance(t, 0)
    assert.equal(h.state().routing.deferred.size, 0)
    assert.ok(h.owner(fifth.id))
    assert.equal(h.state().topics.size, 4)
    assert.deepEqual(
      first.slice(1).map((message) => h.owner(message.id)),
      initial.slice(1),
    )
    await advance(t, 100)
    assert.equal(h.calls.decision, 5)
    assert.equal(h.received.get(fifth.id), 1)
    for (const barrier of held) barrier.release()
    await advance(t, 0)
    await advance(t, 2000)
    assert.equal(h.calls.decision, 5)
    assert.equal(h.runtime.activeTasks, 0)
    assert.equal(h.sends.length, 0)
  } finally {
    for (const barrier of held) barrier.release()
    await h.close()
  }
})

test('four WAIT slots remain protected until cheap expiry; deferred fresh input gets one turn, unchanged inputs never get model rechecks', async (t) => {
  const h = fixture(t, true)
  try {
    h.decide((_data, index) => (index <= 4 ? decision('wait', { waitForMs: 50 }) : decision()))
    for (let index = 0; index < 4; index++)
      h.ingest(`wait-${index}`, `[topic:wait-${index}] Wait plan.`)
    await advance(t, 100)
    const fifth = h.ingest('wait-fifth', '[topic:fifth] Fresh plan.')
    assert.equal(h.state().routing.deferred.size, 1)
    await advance(t, 49)
    assert.equal(h.calls.decision, 4)
    await advance(t, 1)
    assert.equal(h.rechecks().length, 4)
    assert.equal(h.state().routing.deferred.size, 0)
    assert.equal(h.calls.decision, 4)
    await advance(t, 100)
    assert.equal(h.calls.decision, 5)
    assert.equal(h.received.get(fifth.id), 1)
    await advance(t, 3000)
    assert.equal(h.rechecks().length, 4)
    assert.equal(h.calls.decision, 5)
  } finally {
    await h.close()
  }
})

test('fresh same-topic messages coalesce while active and cannot preserve a stale-generation WAIT or manufacture per-message model attempts', async (t) => {
  const h = fixture(t, true),
    held = gate()
  try {
    h.decide(async (_data, index) => {
      if (index === 1) {
        await held.promise
        return decision('wait', { waitForMs: 1000 })
      }
      return decision()
    })
    const first = h.ingest('active-one', '[topic:coalesce] First.')
    await advance(t, 100)
    const second = h.ingest('active-two', '[topic:coalesce] Second.'),
      third = h.ingest('active-three', '[topic:coalesce] Third.')
    assert.equal(h.runtime.activeTasks, 1)
    assert.equal(h.owner(second.id), h.owner(first.id))
    assert.equal(h.owner(third.id), h.owner(first.id))
    assert.equal(h.calls.decision, 1)
    held.release()
    await advance(t, 0)
    await advance(t, 100)
    assert.equal(h.calls.decision, 2)
    assert.match(JSON.stringify(h.decisions[1]), /Second\./)
    assert.match(JSON.stringify(h.decisions[1]), /Third\./)
    await advance(t, 2000)
    assert.equal(h.calls.decision, 2)
    assert.equal(h.rechecks().length, 0)
    assert.deepEqual([...h.received.values()], [1, 1, 1])
  } finally {
    held.release()
    await h.close()
  }
})

for (const lifecycle of ['recall', 'edit'] as const)
  test(`deferred ${lifecycle} uses current source qualification and cannot replay the old body or duplicate observed ingress`, async (t) => {
    const h = fixture(t, true),
      held = Array.from({ length: 4 }, gate)
    try {
      h.decide(async (_data, index) => {
        if (index <= 4) await held[index - 1]!.promise
        return decision()
      })
      for (let index = 0; index < 4; index++)
        h.ingest(`protected-${index}`, `[topic:protected-${index}] Protected.`)
      await advance(t, 100)
      const pending = h.ingest('pending-lifecycle', '[topic:old] OLD_DEFERRED_BODY_CANARY')
      assert.ok(h.state().routing.deferred.has(pending.id))
      if (lifecycle === 'recall') h.recall(pending)
      else assert.equal(h.edit(pending, '[topic:replacement] CURRENT_EDIT_BODY').status, 'edited')
      for (const barrier of held) barrier.release()
      await advance(t, 0)
      await advance(t, 100)
      assert.equal(h.state().routing.deferred.size, 0)
      assert.equal(h.calls.decision, lifecycle === 'recall' ? 4 : 5)
      assert.equal(
        h.received.get(pending.id),
        lifecycle === 'recall' ? 1 : 2,
        'explicit edit is new observed content, deferred retry is not',
      )
      if (lifecycle === 'recall') assert.equal(h.owner(pending.id), undefined)
      else {
        assert.deepEqual(h.edited, [pending.id])
        assert.match(JSON.stringify(h.decisions.at(-1)), /CURRENT_EDIT_BODY/)
        assert.doesNotMatch(JSON.stringify(h.decisions.at(-1)), /OLD_DEFERRED_BODY_CANARY/)
      }
      await advance(t, 2000)
      assert.equal(h.calls.decision, lifecycle === 'recall' ? 4 : 5)
      assert.equal(h.sends.length, 0)
    } finally {
      for (const barrier of held) barrier.release()
      await h.close()
    }
  })

for (const lifecycle of ['feature-off', 'participation-off', 'adapter-replaced'] as const)
  test(`${lifecycle} clears unadmitted routing without replay and preserves the correct pre-admitted WAIT lifecycle`, async (t) => {
    const h = fixture(t, true)
    try {
      h.decide(() => decision('wait', { waitForMs: 1000 }))
      for (let index = 0; index < 4; index++)
        h.ingest(`cancel-${index}`, `[topic:cancel-${index}] Waiting.`)
      await advance(t, 100)
      const pending = h.ingest('cancel-deferred', '[topic:deferred] Do not replay this input.')
      assert.equal(h.state().routing.deferred.size, 1)
      if (lifecycle === 'feature-off') h.feature(false)
      else if (lifecycle === 'participation-off') h.participation(false)
      else h.replaceAdapter()
      assert.equal(h.state().routing.deferred.size, 0)
      if (lifecycle === 'feature-off') h.feature(true)
      else if (lifecycle === 'participation-off') h.participation(true)
      await advance(t, 2000)
      assert.equal(h.calls.decision, 4)
      assert.equal(
        h.rechecks().length,
        lifecycle === 'feature-off' ? 4 : 0,
        'topic experiment OFF is not participation cancellation: existing WAIT may cheaply settle once',
      )
      assert.equal(h.owner(pending.id), undefined)
      assert.equal(h.received.get(pending.id), 1)
      h.decide(() => decision())
      h.ingest('fresh-after-lifecycle', '[topic:new-after] Fresh input only.')
      await advance(t, 100)
      assert.equal(h.calls.decision, 5)
      assert.equal(h.received.get(pending.id), 1)
      assert.equal(h.sends.length, 0)
    } finally {
      await h.close()
    }
  })

for (const shape of ['unknown', 'partial'] as const)
  test(`${shape} transport outcomes protect all four slots and are never replayed to free a fifth topic`, async (t) => {
    const h = fixture(t, true)
    try {
      h.decide(() => decision('reply'))
      h.reply((messages) =>
        messages.some((message) => message.role === 'tool')
          ? completion()
          : send(shape === 'partial' ? 2 : 1),
      )
      h.receipt((scope, index) =>
        shape === 'partial' && index % 2 === 1
          ? { status: 'sent', messageId: h.message(`confirmed-${index}`, '', 'bot', scope).id }
          : { status: 'unknown' },
      )
      for (let index = 0; index < 4; index++) {
        h.ingest(`uncertain-${index}`, `[topic:uncertain-${index}] Synthetic request.`)
        await advance(t, 100)
      }
      assert.equal(h.state().routing.protectedSends.size, 4)
      assert.equal(h.runtime.activeTasks, 0)
      const fifth = h.ingest('uncertain-fifth', '[topic:fifth] New request.')
      assert.equal(h.state().topics.size, 4)
      assert.ok(h.state().routing.deferred.has(fifth.id))
      const sent = h.sends.length,
        counts = { ...h.calls }
      assert.equal(sent, shape === 'partial' ? 8 : 4)
      await advance(t, 5000)
      assert.deepEqual(h.calls, counts)
      assert.equal(h.sends.length, sent)
      assert.equal(h.received.get(fifth.id), 1)
      assert.equal(h.state().routing.acknowledged.size, shape === 'partial' ? 4 : 0)
    } finally {
      await h.close()
    }
  })

test('bounded 100-message burst coalesces into four existing topic generations with measured synthetic call and terminal counts', async (t) => {
  const h = fixture(t, true),
    held = Array.from({ length: 4 }, gate)
  try {
    h.decide(async (_data, index) => {
      if (index <= 4) await held[index - 1]!.promise
      return decision()
    })
    const ids: string[] = []
    for (let index = 0; index < 4; index++)
      ids.push(h.ingest(`burst-${index}`, `[topic:burst-${index}] Initial message.`).id)
    const originalTopics = [...h.state().topics.keys()].sort()
    await advance(t, 100)
    assert.equal(h.runtime.activeTasks, 4)
    for (let index = 4; index < 100; index++)
      ids.push(
        h.ingest(`burst-${index}`, `[topic:burst-${index % 4}] Synthetic follow-up ${index}.`).id,
      )
    assert.equal(h.calls.decision, 4, 'active turns are not restarted on each arrival')
    assert.equal(h.runtime.activeTasks, 4)
    assert.deepEqual([...h.state().topics.keys()].sort(), originalTopics)
    for (const barrier of held) barrier.release()
    await advance(t, 0)
    await advance(t, 100)
    await advance(t, 2000)
    const routing = h.runtime.sceneState(h.scene.id).topicRouting
    const measured = {
      fixture: '100_live_ingress_four_explicit_topics',
      ingress: ids.length,
      received: [...h.received.values()].reduce((sum, count) => sum + count, 0),
      fakeModelCalls: Object.values(h.calls).reduce((sum, count) => sum + count, 0),
      decisionCalls: h.calls.decision,
      syntheticAdapterSends: h.sends.length,
      topics: h.runtime.sceneState(h.scene.id).topics.length,
      activeTasks: h.runtime.activeTasks,
      deferred: routing.deferred,
      overflowed: routing.overflowed,
    }
    assert.deepEqual(measured, {
      fixture: '100_live_ingress_four_explicit_topics',
      ingress: 100,
      received: 100,
      fakeModelCalls: 8,
      decisionCalls: 8,
      syntheticAdapterSends: 0,
      topics: 4,
      activeTasks: 0,
      deferred: 0,
      overflowed: 0,
    })
    assert.ok(ids.every((id) => h.received.get(id) === 1))
    assert.deepEqual([...h.state().topics.keys()].sort(), originalTopics)
    assert.equal(h.rechecks().length, 0)
    t.diagnostic(`synthetic_topic_burst_counts=${JSON.stringify(measured)}`)
  } finally {
    for (const barrier of held) barrier.release()
    await h.close()
  }
})

test('a blocked new-topic deferred head cannot starve a later existing-topic overflow after its pending batch is consumed', async (t) => {
  const h = fixture(t, true),
    held = Array.from({ length: 4 }, gate),
    second = gate()
  try {
    h.decide(async (_data, index) => {
      if (index <= 4) await held[index - 1]!.promise
      else if (index === 5) await second.promise
      return decision()
    })
    for (let index = 0; index < 4; index++)
      h.ingest(`mixed-${index}`, `[topic:mixed-${index}] Protected initial input.`)
    await advance(t, 100)
    const blocked = h.ingest(
      'blocked-new-cue',
      '[topic:needs-new-slot] This needs a free topic slot.',
    )
    let overflow!: Message
    for (let index = 0; index < 61; index++)
      overflow = h.ingest(`pending-${index}`, `[topic:mixed-0] Pending input ${index}.`)
    assert.deepEqual([...h.state().routing.deferred.keys()], [blocked.id, overflow.id])
    assert.equal(h.received.get(overflow.id), 1)
    held[0]!.release()
    await advance(t, 0)
    await advance(t, 100)
    assert.equal(h.calls.decision, 5)
    assert.equal(h.runtime.activeTasks, 4)
    h.ingest('poke-existing', '[topic:mixed-1] A fresh routing opportunity, not a replay.')
    assert.ok(h.state().routing.deferred.has(blocked.id), 'all four slots remain protected')
    assert.equal(
      h.state().routing.deferred.has(overflow.id),
      false,
      'finite deferred pass must skip the blocked head and admit the now-ready existing topic',
    )
    assert.equal(h.received.get(overflow.id), 1)
    for (const barrier of held) barrier.release()
    second.release()
    await advance(t, 0)
    await advance(t, 100)
    await advance(t, 1000)
    assert.equal(h.state().routing.deferred.size, 0)
    assert.equal(h.runtime.activeTasks, 0)
    assert.equal(h.calls.decision, 8)
    assert.equal(h.sends.length, 0)
    assert.ok([...h.received.values()].every((count) => count === 1))
  } finally {
    for (const barrier of held) barrier.release()
    second.release()
    await h.close()
  }
})

test('a late sent result from a replaced adapter cannot register an acknowledged owner in the replacement epoch', async (t) => {
  const h = fixture(t, true),
    held = gate()
  try {
    const ack = h.message('late-ack', '').id
    h.decide((_data, index) => decision(index === 1 ? 'reply' : 'silent'))
    h.reply((messages) =>
      messages.some((message) => message.role === 'tool') ? completion() : send(),
    )
    h.receipt(async () => {
      await held.promise
      return { status: 'sent', messageId: ack }
    })
    const original = h.ingest('before-adapter-replacement', '[topic:late-ack] Synthetic request.')
    const topic = h.owner(original.id)
    await advance(t, 100)
    assert.equal(h.sends.length, 1)
    assert.equal(h.runtime.activeTasks, 1)
    h.replaceAdapter()
    held.release()
    await advance(t, 0)
    assert.equal(h.runtime.activeTasks, 0)
    assert.equal(h.state().routing.acknowledged.has(ack), false)
    const follow = {
      ...h.message(
        'after-adapter-replacement',
        'Reply to an unconfirmed old transport result.',
        'new-person',
      ),
      replyToId: ack,
    }
    h.receive(follow)
    assert.notEqual(h.owner(follow.id), topic)
    assert.equal(h.state().routing.metadata.get(h.owner(follow.id)!)?.provisional, true)
    await advance(t, 100)
    await advance(t, 1000)
    assert.equal(h.calls.decision, 2)
    assert.equal(h.sends.length, 1)
  } finally {
    held.release()
    await h.close()
  }
})

for (const invalid of ['overlap', 'capacity'] as const)
  test(`model ${invalid} reassignment is rejected atomically without moving protected inputs, creating a fifth slot or sending`, async (t) => {
    const h = fixture(t, true)
    try {
      const originals = Array.from({ length: invalid === 'capacity' ? 4 : 2 }, (_, index) =>
        h.ingest(`model-${index}`, `[topic:model-${index}] Grouping fixture.`),
      )
      const before = originals.map((message) => h.owner(message.id))
      h.decide((data, index) => {
        if (index > 1) return decision('wait', { waitForMs: 1000 })
        const id = data.messages[0]!.id
        return decision('reply', {
          topicId: 'selected',
          targetMessageIds: [id],
          topics: [
            { topicId: 'selected', messageIds: [id] },
            { topicId: 'new-extra-slot', messageIds: invalid === 'overlap' ? [id] : [] },
          ],
        })
      })
      await advance(t, 100)
      assert.deepEqual(
        originals.map((message) => h.owner(message.id)),
        before,
      )
      assert.equal(h.state().topics.size, originals.length)
      assert.ok(
        h.store.logs.some((row) => row.errorCode === 'planner_invalid_topic'),
        'invalid grouping must be an explicit diagnosed failure',
      )
      assert.equal(h.calls.plan, 0)
      assert.equal(h.calls.reply, 0)
      assert.equal(h.sends.length, 0)
      assert.ok(originals.every((message) => h.received.get(message.id) === 1))
    } finally {
      await h.close()
    }
  })

const textOf = (message: Pick<Message, 'parts'>) =>
  message.parts.map((part) => (part.type === 'text' ? part.text : '')).join(' ')

for (const target of ['other-active', 'deferred'] as const)
  test(`omitting decision.topics cannot consume ${target} input; actual topic-local decision body excludes its canary`, async (t) => {
    const h = fixture(t, true)
    try {
      const originals = [
        h.ingest('isolated-a', '[topic:isolated-a] ADMITTED_A_BODY'),
        h.ingest('isolated-b', '[topic:isolated-b] OTHER_ACTIVE_B_CANARY'),
        h.ingest('isolated-c', '[topic:isolated-c] OTHER_ACTIVE_C_CANARY'),
        h.ingest('isolated-d', '[topic:isolated-d] OTHER_ACTIVE_D_CANARY'),
      ]
      const pending = h.ingest(
        'isolated-deferred',
        '[topic:isolated-deferred] DEFERRED_BODY_CANARY',
      )
      assert.ok(h.state().routing.deferred.has(pending.id))
      const owners = originals.map((message) => h.owner(message.id))
      let aPayload: DecisionInput | undefined
      h.decide((data) => {
        if (!data.messages.some((message) => textOf(message).includes('ADMITTED_A_BODY')))
          return decision('wait', { waitForMs: 1000 })
        aPayload = structuredClone(data)
        // Topics are projected in the same request; the second retained owner is B.
        // Never infer canonical equality from another turn's reused short ref such as m1.
        const foreign = target === 'other-active' ? data.topics[1]!.messageIds[0]! : pending.id
        return decision('reply', { targetMessageIds: [foreign] })
      })
      await advance(t, 100)
      assert.ok(aPayload)
      assert.deepEqual(aPayload.messages.map(textOf), ['[topic:isolated-a] ADMITTED_A_BODY'])
      assert.doesNotMatch(
        JSON.stringify(aPayload),
        /OTHER_ACTIVE_[BCD]_CANARY|DEFERRED_BODY_CANARY/,
      )
      // A failed idle attempt can lawfully yield its slot to the fifth input;
      // none of the still-protected foreign topics may be reassigned.
      assert.deepEqual(
        originals.slice(1).map((message) => h.owner(message.id)),
        owners.slice(1),
      )
      assert.equal(h.state().topics.size, 4)
      assert.ok(h.owner(pending.id))
      assert.equal(h.calls.plan, 0)
      assert.equal(h.calls.reply, 0)
      assert.equal(h.sends.length, 0)
      const denied = h.store.logs.filter((row) => row.stage === 'turn' && row.status === 'failed')
      assert.equal(denied.length, 1)
      assert.equal(
        denied[0]?.errorCode,
        target === 'other-active' ? 'planner_unknown_target' : 'context_reference_unknown',
      )
      assert.equal(h.received.get(pending.id), 1)
    } finally {
      await h.close()
    }
  })

for (const mode of ['get-active', 'around-deferred', 'by-user-active', 'around-readonly'] as const)
  test(`actual Planner ${mode} history receipt is evidence, not permission to retarget another topic or execute its actions`, async (t) => {
    const h = fixture(t, true)
    try {
      const a = h.ingest('history-a', '[topic:history-a] HISTORY_ADMITTED_A_BODY'),
        b = h.ingest('history-b', '[topic:history-b] HISTORY_OTHER_B_CANARY')
      const originals = [a, b]
      if (mode === 'around-deferred') {
        originals.push(h.ingest('history-c', '[topic:history-c] Other C.'))
        originals.push(h.ingest('history-d', '[topic:history-d] Other D.'))
      }
      const target =
        mode === 'around-deferred'
          ? h.ingest('history-deferred', '[topic:history-deferred] HISTORY_DEFERRED_CANARY')
          : b
      const canary =
        mode === 'around-deferred' ? 'HISTORY_DEFERRED_CANARY' : 'HISTORY_OTHER_B_CANARY'
      if (mode === 'around-deferred') assert.ok(h.state().routing.deferred.has(target.id))
      h.history([...originals, ...(mode === 'around-deferred' ? [target] : [])])
      let actions = 0
      h.runtime.addExtension({
        tools: [
          {
            name: 'fixture.propose',
            effect: 'learningCandidate',
            description: 'Synthetic observable action sentinel',
            inputSchema: { type: 'object', properties: {}, additionalProperties: false },
          },
        ],
        execute: async () => {
          actions++
          return { status: 'synthetic' }
        },
      })
      let aRef = '',
        bRef = '',
        userRef = '',
        toolName = '',
        returned: RuntimeMessage[] = []
      h.decide((data) => {
        const current = data.messages.find((message) =>
          textOf(message).includes('HISTORY_ADMITTED_A_BODY'),
        )
        if (!current) return decision('wait', { waitForMs: 1000 })
        aRef = current.id
        bRef = data.topics[1]!.messageIds[0]!
        userRef = current.senderId
        return decision('reply', { targetMessageIds: [aRef] })
      })
      h.plan((messages, index) => {
        if (index === 1) {
          toolName =
            mode === 'get-active'
              ? 'messages.get'
              : mode === 'by-user-active'
                ? 'messages.byUser'
                : 'messages.around'
          return completion([
            call(
              toolName,
              toolName === 'messages.get'
                ? { ids: [bRef] }
                : toolName === 'messages.byUser'
                  ? { userId: userRef, limit: 30 }
                  : { messageId: aRef, limit: 30 },
            ),
          ])
        }
        const receipt = JSON.parse(
          messages.findLast((message) => message.role === 'tool')!.content,
        ) as { toolName: string; value: { items: RuntimeMessage[] } }
        returned = structuredClone(receipt.value.items)
        const foreign = returned.find((message) => textOf(message).includes(canary))!
        assert.ok(
          foreign,
          'real history read must return the foreign-source body before testing the final admission guard',
        )
        const allowed = mode === 'around-readonly'
        const value = fixtureReplyPlan(messages, {
          targetMessageIds: [allowed ? aRef : foreign.id],
          contextMessageIds: [aRef, foreign.id],
          actions: allowed ? [] : [{ toolName: 'fixture.propose', arguments: {} }],
        })!
        return completion([call('reply.plan', value)])
      })
      h.reply((messages) => {
        if (messages.some((message) => message.role === 'tool')) return completion()
        return completion([
          call('messages.send', {
            operationId: 'readonly-history-answer',
            segments: [
              {
                replyToId: aRef,
                parts: [{ type: 'text', text: 'Synthetic answer to the admitted source only.' }],
              },
            ],
          }),
        ])
      })
      await advance(t, 100)
      assert.ok(
        returned.some((message) => textOf(message).includes(canary)),
        'assertions in the completion stub must not be swallowed as an unrelated runtime error',
      )
      assert.equal(h.calls.plan, 2)
      assert.ok(
        h.store.logs.some(
          (row) => row.stage === 'tool' && row.toolName === toolName && row.status === 'completed',
        ),
      )
      assert.equal(actions, 0)
      if (mode === 'around-readonly') {
        assert.equal(h.sends.length, 1)
        assert.equal(
          h.sends[0]?.replyToId,
          a.id,
          "the real adapter receives canonical A, not another turn's short ref",
        )
        const replyInput = h.inputs.find((messages) =>
          messages[0]?.content.includes('You are Replyer'),
        )!
        assert.match(replyInput[1]!.content, /HISTORY_OTHER_B_CANARY/)
      } else {
        assert.equal(h.calls.reply, 0)
        assert.equal(h.sends.length, 0)
        assert.ok(
          h.store.logs.some((row) => row.errorCode === 'planner_target_outside_admitted_topic'),
        )
      }
      assert.notEqual(h.owner(a.id), h.owner(b.id))
      assert.equal(h.received.get(target.id), 1)
    } finally {
      await h.close()
    }
  })

for (const freshWake of [false, true])
  test(`sent-first then stale validation false ${freshWake ? 'uses only the real pending wake' : 'does not mint a wake for unscheduled input'} and never replays the confirmed segment`, async (t) => {
    const h = fixture(t, true)
    try {
      h.decide((_data, index) => decision(index === 1 ? 'reply' : 'silent'))
      h.reply((messages) =>
        messages.some((message) => message.role === 'tool') ? completion() : send(2),
      )
      const validationInputs: string[] = []
      h.validate((messages) => {
        validationInputs.push(messages[1]!.content)
        return completion([call('reply.validate', { valid: false })])
      })
      let fresh!: Message
      h.receipt((scope) => {
        fresh = h.message('fresh-stale-signal', '[topic:stale-flow] NEW_STALE_SIGNAL')
        if (freshWake) h.receive(fresh)
        else h.runtime.ingest(fresh, scope, false)
        return { status: 'sent', messageId: h.message('first-confirmed', '', 'bot', scope).id }
      })
      h.ingest('stale-original', '[topic:stale-flow] ORIGINAL_QUALIFIED_INPUT')
      await advance(t, 100)
      assert.equal(h.sends.length, 1)
      assert.equal(h.calls.validation, 1)
      assert.match(validationInputs[0]!, /NEW_STALE_SIGNAL/)
      assert.equal(
        h.calls.decision,
        1,
        'a zero-delay direct run would already have fired during settled microtasks',
      )
      assert.equal(h.received.get(fresh.id), 1)
      const firstTurn = h.store.logs.find((row) => row.stage === 'turn' && row.status === 'partial')
      assert.equal(firstTurn?.counts?.sentSegments, 1)
      assert.equal(firstTurn?.counts?.cancelledSegments, 1)
      await advance(t, 99)
      assert.equal(h.calls.decision, 1)
      await advance(t, 1)
      assert.equal(h.calls.decision, freshWake ? 2 : 1)
      if (freshWake) assert.match(JSON.stringify(h.decisions[1]), /NEW_STALE_SIGNAL/)
      await advance(t, 2000)
      assert.equal(h.calls.decision, freshWake ? 2 : 1)
      assert.equal(h.calls.plan, 1)
      assert.equal(h.sends.length, 1)
      assert.equal(h.runtime.activeTasks, 0)
    } finally {
      await h.close()
    }
  })

test('cache OFF cleanup erases retained cue and ACK metadata without unprotecting an uncertain partial send', async (t) => {
  const h = fixture(t, true)
  try {
    h.store.set('settings', { ...h.store.settings, cacheEnabled: false })
    h.engine.settingsChanged()
    h.decide(() => decision('reply'))
    h.reply((messages) =>
      messages.some((message) => message.role === 'tool') ? completion() : send(2),
    )
    h.receipt((scope, index) =>
      index === 1
        ? { status: 'sent', messageId: h.message('cache-off-confirmed', '', 'bot', scope).id }
        : { status: 'unknown' },
    )
    const input = h.ingest('cache-off-source', '[topic:cache-secret-cue] Synthetic input.')
    const topic = h.owner(input.id)!
    await advance(t, 100)
    assert.equal(h.sends.length, 2)
    assert.deepEqual(h.runtime.preview(h.scene.id), [])
    assert.ok([...h.state().routing.metadata.values()].every((value) => value.cue === undefined))
    assert.equal(h.state().routing.acknowledged.size, 0)
    assert.ok(h.state().routing.protectedSends.has(topic))
    const calls = { ...h.calls }
    await advance(t, 2000)
    assert.deepEqual(h.calls, calls)
    assert.equal(h.sends.length, 2)
  } finally {
    await h.close()
  }
})
