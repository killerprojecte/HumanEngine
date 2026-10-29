import assert from 'node:assert/strict'
import test, { type TestContext } from 'node:test'
import { setImmediate as immediate } from 'node:timers/promises'
import { mkdtempSync, rmSync } from 'node:fs'
import { join } from 'node:path'
import { tmpdir } from 'node:os'
import sharp from 'sharp'
import type { RuntimeAdapter } from '../src/runtime.ts'
import { Engine } from '../src/engine.ts'
import { Store } from '../src/store.ts'
import type { Completion, Router } from '../src/models.ts'
import type { Message, SceneRef } from '../src/onebot.ts'
import type { RuntimeMessage, RuntimeExtension, TurnContext } from '../src/runtime.ts'
import { installAdapter, type AdapterContext } from '../src/plugins/adapters.ts'
import { sourceIdentity } from '../src/runtime/source-events.ts'
import { SourceEventStore } from '../src/memory/storage/event-revisions.ts'
import { recordSourceEventMutation } from '../src/memory/recovery/source-events.ts'
import { fixtureReplyPlan } from './helpers/reply-plan.ts'

// Scripted semantic outputs test trusted version binding, not natural-language edit recognition.
const completion = (toolCalls: Completion['toolCalls'] = [], text = ''): Completion => ({
  text,
  toolCalls,
  modelId: 'offline-script',
  usage: { inputTokens: 0, outputTokens: 0, totalTokens: 0 },
  assistant: { role: 'assistant', content: text, toolCalls },
})
const decide = (action: 'silent' | 'reply' = 'silent') =>
  completion([
    {
      id: 'decision',
      name: 'participation.decide',
      arguments: { action, reasonCode: 'offline_source_event' },
    },
  ])
const proposal = (sourceMessageIds: string[], content = 'SYNTHETIC_RETAINED_ASSERTION') =>
  completion([
    {
      id: 'proposal',
      name: 'learning.propose',
      arguments: { content, kind: 'claim', sourceMessageIds },
    },
  ])
function clock(t: TestContext) {
  t.mock.timers.enable({
    apis: ['Date', 'setTimeout', 'setInterval'],
    now: new Date('2026-10-28T12:00:00Z'),
  })
}
async function advance(t: TestContext, ms: number) {
  t.mock.timers.tick(ms)
  await immediate()
  t.mock.timers.tick(0)
  await immediate()
}
function fixture(
  options: { enabled?: boolean; learning?: boolean; participation?: boolean; quiet?: number } = {},
) {
  const root = mkdtempSync(join(tmpdir(), 'he-source-event-runtime-'))
  const path = join(root, 'synthetic.sqlite')
  const store = new Store(path)
  store.set('settings', {
    ...store.settings,
    sourceEventRevisionsEnabled: options.enabled ?? true,
    debounceMs: 10,
    maxWaitMs: 100,
    cacheEnabled: true,
    learningBatchMs: options.quiet ?? 100,
    learningMaxWaitMs: 300000,
    learningMinIntervalMs: 0,
  })
  const engine = new Engine(store)
  let sends = 0
  let context!: AdapterContext
  const controller = new AbortController()
  const registration = installAdapter(
    'source-fixture',
    (ctx) => {
      context = ctx
      return {
        send: async () => {
          sends++
          return { status: 'sent' as const, messageId: ctx.messageId(scene, `bot-${sends}`) }
        },
        history: async () => ({ items: [], coverage: 'complete' as const }),
        getMessage: async () => ({ items: [], coverage: 'complete' as const }),
        dispose() {},
      }
    },
    engine.runtime,
    store,
    controller.signal,
    {},
  )
  const scene = context.scene('synthetic-account', 'group', 'synthetic-group')
  store.saveScene({
    ...scene,
    name: 'Source-event fixture',
    participationEnabled: options.participation ?? false,
    learningEnabled: options.learning ?? false,
    proactiveEnabled: false,
  })
  engine.router = () =>
    ({
      complete: async () => {
        throw new Error('Unexpected model invocation')
      },
    }) as unknown as Router
  engine.indexMemory = async () => {}
  const message = (
    id: string,
    body = 'SYNTHETIC_SOURCE_BODY_A',
    scope: SceneRef = scene,
  ): Message => ({
    id: context.messageId(scope, id),
    sceneId: scope.id,
    senderId: 'synthetic-person',
    origin: 'live',
    status: 'available',
    sentAt: new Date().toISOString(),
    receivedAt: new Date().toISOString(),
    parts: [{ type: 'text', text: body }],
  })
  return {
    root,
    path,
    store,
    engine,
    context,
    scene,
    message,
    receive(value: Message) {
      context.receive(value, scene, false)
    },
    current(id: string) {
      return store.sourceEvents.registered(scene.id, id)!
    },
    source(id: string) {
      return engine.runtime.preview(scene.id).find((m) => m.id === id)! as RuntimeMessage
    },
    install(complete: Router['complete']) {
      engine.router = () => ({ complete }) as Router
    },
    change(patch: Partial<NonNullable<ReturnType<Store['scene']>>>) {
      const changed = { ...store.scene(scene.id)!, ...patch }
      store.saveScene(changed)
      engine.sceneChanged(changed)
    },
    feature(enabled: boolean) {
      store.set('settings', { ...store.settings, sourceEventRevisionsEnabled: enabled })
      engine.settingsChanged()
    },
    sends: () => sends,
    async close() {
      try {
        registration.dispose()
        controller.abort()
        await engine.dispose()
      } finally {
        store.close()
        rmSync(root, { recursive: true, force: true })
      }
    },
  }
}
const sourcesIn = (prompts: Parameters<Router['complete']>[1]) =>
  (JSON.parse(prompts[1]!.content) as { messages: RuntimeMessage[] }).messages
const memoryContext = (scene: SceneRef) => ({ sceneId: scene.id, simulation: false })

test('trusted adapter ordinary duplicate IDs never edit even with changed bodies or caller-forged refs; event SQL retains metadata only', async () => {
  const h = fixture()
  try {
    const first = h.message('duplicate')
    h.receive({
      ...first,
      sourceEventRef: { eventId: 'forged', revision: 88, deletionEpoch: 8 },
    } as Message)
    const initial = h.current(first.id)
    assert.equal(initial.ref.revision, 1)
    assert.notEqual(initial.ref.eventId, 'forged')
    h.receive(first)
    h.receive({ ...first, parts: [{ type: 'text', text: 'SYNTHETIC_CHANGED_DUPLICATE_CANARY' }] })
    assert.deepEqual(h.current(first.id), initial)
    assert.deepEqual(h.source(first.id).parts, first.parts)
    assert.equal(h.engine.runtime.preview(h.scene.id).length, 1)
    for (const table of [
      'source_event_heads',
      'source_event_revisions',
      'source_event_edit_keys',
    ]) {
      const serialized = JSON.stringify(h.store.db.prepare(`SELECT * FROM ${table}`).all())
      assert.equal(serialized.includes('SYNTHETIC_SOURCE_BODY_A'), false)
      assert.equal(serialized.includes('SYNTHETIC_CHANGED_DUPLICATE_CANARY'), false)
    }
    assert.equal(initial.bodyCoverage, 'unavailable')
    const restartedStore = new Store(h.path)
    const restarted = new Engine(restartedStore)
    restarted.router = () => {
      throw new Error('Restart must not replay old source learning')
    }
    try {
      restarted.runtime.ingest(first, h.scene, false)
      restarted.runtime.ingest(
        { ...first, parts: [{ type: 'text', text: 'Changed replay after restart' }] },
        h.scene,
        false,
      )
      assert.deepEqual(restartedStore.sourceEvents.registered(h.scene.id, first.id), initial)
      assert.deepEqual(restarted.runtime.preview(h.scene.id), [])
      assert.equal(restarted.learningStatus().queued, 0)
    } finally {
      await restarted.dispose()
      restartedStore.close()
    }
  } finally {
    await h.close()
  }
})

test('explicit adapter CAS edits are idempotent, reject stale/conflicting/scoped inputs, and A→B→A is a new revision rather than an old ref upgrade', async () => {
  const h = fixture()
  try {
    const first = h.message('cas')
    h.receive(first)
    const one = h.current(first.id).ref
    const second = { ...first, parts: [{ type: 'text' as const, text: 'SYNTHETIC_SOURCE_BODY_B' }] }
    const change = { editId: 'edit-b', order: { kind: 'observed_cas' as const, expectedBase: one } }
    const result = h.context.edit(second, h.scene, change)
    assert.equal(result.status, 'edited')
    assert.equal(result.ref?.revision, 2)
    assert.equal(h.context.edit(second, h.scene, change).status, 'duplicate')
    assert.equal(h.context.edit(first, h.scene, change).status, 'conflict')
    assert.equal(
      h.context.edit(first, h.scene, { ...change, editId: 'late-stale' }).status,
      'conflict',
    )
    const third = h.context.edit(first, h.scene, {
      editId: 'edit-back-a',
      order: { kind: 'observed_cas', expectedBase: result.ref! },
    })
    assert.equal(third.status, 'edited')
    assert.equal(third.ref?.revision, 3)
    assert.equal(third.ref?.eventId, one.eventId)
    const identity = sourceIdentity(h.scene, first)
    assert.equal(h.store.sourceEvents.current(one, identity).eligible, false)
    assert.equal(h.store.sourceEvents.historical(one, identity).eligible, true)
    assert.deepEqual(h.source(first.id).sourceEventRef, third.ref)
    const foreign = h.context.scene('other-account', 'group', 'synthetic-group')
    assert.throws(() => h.context.edit(first, foreign, change), /invalid_adapter_message/)
    assert.throws(
      () => h.context.edit({ ...first, origin: 'history' }, h.scene, change),
      /live_origin/,
    )
    assert.throws(() => h.context.edit({ ...first, senderId: 'bot' }, h.scene, change), /live_user/)
    assert.equal(
      h.context.edit({ ...first, senderId: 'other-person' }, h.scene, change).status,
      'conflict',
    )
    assert.equal(h.current(first.id).ref.revision, 3)
  } finally {
    await h.close()
  }
})

test('adapter platform sequence ordering never derives ordering from arrival, and same raw IDs across accounts stay separate', async () => {
  const h = fixture()
  try {
    const first = h.message('same-external-id')
    h.receive(first)
    const next = { ...first, parts: [{ type: 'text' as const, text: 'Ordered B' }] }
    const edit = (sequence: number, editId = `sequence-${sequence}`) => ({
      editId,
      order: { kind: 'platform_sequence' as const, sequence },
    })
    assert.equal(h.context.edit(next, h.scene, edit(10)).status, 'edited')
    assert.equal(h.context.edit(first, h.scene, edit(9)).status, 'conflict')
    assert.equal(
      h.context.edit(first, h.scene, edit(10, 'same-sequence-other-body')).status,
      'conflict',
    )
    assert.equal(h.current(first.id).ref.revision, 2)
    const foreign = h.context.scene('other-account', 'group', 'synthetic-group')
    h.store.saveScene({
      ...foreign,
      name: 'Independent account',
      participationEnabled: false,
      learningEnabled: false,
      proactiveEnabled: false,
    })
    const other = h.message('same-external-id', 'Independent account body', foreign)
    h.context.receive(other, foreign, false)
    assert.notEqual(other.id, first.id)
    assert.notEqual(
      h.store.sourceEvents.registered(foreign.id, other.id)!.ref.eventId,
      h.current(first.id).ref.eventId,
    )
    assert.equal(h.store.sourceEvents.registered(foreign.id, other.id)!.ref.revision, 1)
  } finally {
    await h.close()
  }
})

test('feature OFF does not enroll unobserved sources, but turning it OFF never disables explicit edits for enrolled sources', async () => {
  const h = fixture({ enabled: false })
  try {
    const old = h.message('not-enrolled')
    h.receive(old)
    assert.equal(h.store.sourceEvents.registered(h.scene.id, old.id), undefined)
    assert.equal(h.source(old.id).sourceEventRef, undefined)
    assert.equal(
      h.context.edit(old, h.scene, {
        editId: 'unknown-edit',
        order: { kind: 'platform_sequence', sequence: 1 },
      }).status,
      'unorderable',
    )
    h.feature(true)
    const enrolled = h.message('enrolled')
    h.receive(enrolled)
    const one = h.current(enrolled.id).ref
    h.feature(false)
    assert.equal(
      h.context.edit(
        { ...enrolled, parts: [{ type: 'text', text: 'Corrected while feature OFF' }] },
        h.scene,
        { editId: 'off-edit', order: { kind: 'observed_cas', expectedBase: one } },
      ).status,
      'edited',
    )
    assert.equal(h.current(enrolled.id).ref.revision, 2)
    const later = h.message('still-not-enrolled')
    h.receive(later)
    assert.equal(h.store.sourceEvents.registered(h.scene.id, later.id), undefined)
    assert.equal(h.engine.memory.adminList().length, 0)
    assert.equal(h.sends(), 0)
  } finally {
    await h.close()
  }
})

test('simulation ingress and edit produce no source-event persistence or real learning', async () => {
  const h = fixture()
  try {
    const id = h.engine.runtime.createSimulation()
    const scope = {
      id,
      kind: 'simulation' as const,
      adapterId: 'simulation',
      accountId: 'simulation',
      platformSceneId: id,
    }
    const first: RuntimeMessage = {
      ...h.message('simulation'),
      id: 'sim-source',
      sceneId: id,
      origin: 'simulation',
    }
    h.engine.runtime.ingest(first, scope, false)
    assert.equal(
      h.engine.runtime.edit(first, scope, {
        editId: 'sim-edit',
        order: { kind: 'platform_sequence', sequence: 1 },
      }).status,
      'simulation',
    )
    assert.deepEqual(
      h.store.db.prepare("SELECT name FROM sqlite_master WHERE name LIKE 'source_event_%'").all(),
      [],
    )
    assert.equal(h.engine.memory.adminList().length, 0)
    assert.equal(h.sends(), 0)
  } finally {
    await h.close()
  }
})

test('actual silent background extraction captures the trusted source ref; editing changes current eligibility without purging authorized history', async (t) => {
  clock(t)
  const h = fixture({ learning: true, participation: true })
  let calls = 0
  h.install(async (_role, prompts, tools) => {
    if (tools?.some((tool) => tool.name === 'participation.decide')) return decide()
    if (tools?.some((tool) => tool.name === 'learning.propose')) {
      calls++
      return proposal(sourcesIn(prompts).map((source) => source.id))
    }
    return completion()
  })
  try {
    const first = h.message('learned')
    h.receive(first)
    await advance(t, 10)
    await advance(t, 90)
    assert.equal(calls, 1)
    const memory = h.engine.memory.list(memoryContext(h.scene))[0]!
    assert.ok(memory)
    assert.deepEqual(memory.sources[0]!.eventRef, h.current(first.id).ref)
    const original = h.current(first.id).ref
    h.change({ learningEnabled: false })
    h.feature(false)
    const edited = h.context.edit(
      { ...first, parts: [{ type: 'text', text: 'Corrected while learning and feature OFF' }] },
      h.scene,
      { editId: 'learned-edit', order: { kind: 'observed_cas', expectedBase: original } },
    )
    assert.equal(edited.status, 'edited')
    assert.equal(
      h.engine.memory.list(memoryContext(h.scene)).some((record) => record.id === memory.id),
      false,
    )
    assert.equal(
      h.engine.memory.adminList().find((record) => record.id === memory.id)?.status,
      'active',
    )
    const history = h.engine.memory.historyFor(memoryContext(h.scene), memory.id)
    assert.ok(history?.revisions.some((revision) => revision.record.content === memory.content))
    await advance(t, 1000)
    assert.equal(calls, 1)
    h.change({ learningEnabled: true })
    await advance(t, 1000)
    assert.equal(calls, 1, 'enabling learning must not replay the edited OFF interval')
    assert.equal(h.sends(), 0)
  } finally {
    await h.close()
  }
})

test('in-flight background model result for v1 cannot commit after an explicit source edit even if the provider ignores abort', async (t) => {
  clock(t)
  const h = fixture({ learning: true })
  let release = () => {},
    signal: AbortSignal | undefined,
    calls = 0
  const pending = new Promise<void>((resolve) => {
    release = resolve
  })
  h.install(async (_role, prompts, _tools, currentSignal) => {
    calls++
    if (calls !== 1) return completion()
    signal = currentSignal
    const sourceIds = sourcesIn(prompts).map((message) => message.id)
    await pending
    return proposal(sourceIds, 'STALE_V1_LATE_LEARNING_CANARY')
  })
  try {
    const first = h.message('late-learning')
    h.receive(first)
    await advance(t, 100)
    assert.equal(calls, 1)
    h.context.edit({ ...first, parts: [{ type: 'text', text: 'New source revision' }] }, h.scene, {
      editId: 'late-learning-edit',
      order: { kind: 'observed_cas', expectedBase: h.current(first.id).ref },
    })
    assert.equal(signal?.aborted, true)
    release()
    await advance(t, 1000)
    assert.equal(
      h.engine.memory
        .adminList()
        .some((record) => record.content === 'STALE_V1_LATE_LEARNING_CANARY'),
      false,
    )
    assert.equal(h.sends(), 0)
  } finally {
    release()
    await h.close()
  }
})

function extension(h: ReturnType<typeof fixture>) {
  const item = (h.engine.runtime as unknown as { extensions: RuntimeExtension[] }).extensions.find(
    (item) => item.tools.some((tool) => tool.name === 'learning.propose'),
  )
  assert.ok(item)
  return item
}
function context(
  h: ReturnType<typeof fixture>,
  messages: RuntimeMessage[],
  id: string,
): TurnContext {
  return {
    scene: h.scene,
    mode: 'real',
    turnId: id,
    signal: new AbortController().signal,
    dependencies: new Set(messages.map((message) => message.id)),
    validations: [],
    sourceSnapshots: structuredClone(messages),
  }
}

test('actual Replyer late send for v1 stays cancelled through A→B→A and never acquires the new matching-body revision', async (t) => {
  clock(t)
  const h = fixture({ participation: true })
  let release = () => {},
    replySignal: AbortSignal | undefined,
    decisions = 0,
    replies = 0
  const pending = new Promise<void>((resolve) => {
    release = resolve
  })
  h.install(async (role, prompts, tools, signal) => {
    if (tools?.some((tool) => tool.name === 'participation.decide'))
      return decide(++decisions === 1 ? 'reply' : 'silent')
    if (tools?.some((tool) => tool.name === 'reply.plan'))
      return completion([{ id: 'plan', name: 'reply.plan', arguments: fixtureReplyPlan(prompts)! }])
    assert.equal(role, 'replyer')
    replies++
    replySignal = signal
    await pending
    return completion([
      {
        id: 'late-send',
        name: 'messages.send',
        arguments: {
          operationId: 'stale-v1-send',
          segments: [{ parts: [{ type: 'text', text: 'STALE_V1_REPLY_CANARY' }] }],
        },
      },
    ])
  })
  try {
    const first = h.message('late-reply')
    h.receive(first)
    await advance(t, 10)
    assert.equal(replies, 1)
    const old = h.current(first.id).ref
    const changed = h.context.edit(
      { ...first, parts: [{ type: 'text', text: 'Source B' }] },
      h.scene,
      { editId: 'reply-b', order: { kind: 'observed_cas', expectedBase: old } },
    )
    h.context.edit(first, h.scene, {
      editId: 'reply-a-again',
      order: { kind: 'observed_cas', expectedBase: changed.ref! },
    })
    assert.equal(replySignal?.aborted, true)
    release()
    await advance(t, 1000)
    assert.equal(h.current(first.id).ref.revision, 3)
    assert.equal(h.sends(), 0)
    assert.ok(h.store.logs.some((entry) => entry.stage === 'turn' && entry.status === 'cancelled'))
  } finally {
    release()
    await h.close()
  }
})

test('Engine contextual submission uses the first delivered source snapshot, while a new v3 turn can add a fresh proof after A→B→A', async (t) => {
  clock(t)
  const h = fixture({ learning: true, quiet: 300000 })
  let commitError: unknown
  const originalPropose = h.engine.memory.propose.bind(h.engine.memory)
  h.engine.memory.propose = (...args) => {
    try {
      return originalPropose(...args)
    } catch (error) {
      commitError = error
      throw error
    }
  }
  try {
    const first = h.message('contextual-reversion')
    h.receive(first)
    const oldSnapshot = h.source(first.id)
    const oldContext = context(h, [oldSnapshot], 'first-contextual-turn')
    const input = {
      content: 'CONTEXTUAL_REVERSION_ASSERTION',
      kind: 'claim',
      sourceMessageIds: [first.id],
    }
    const initial = (await extension(h).execute('learning.propose', input, oldContext)) as {
      status: string
    }
    assert.equal(initial.status, 'queued')
    await h.engine.contextualLearning.queue.whenIdle()
    const original = h.engine.memory
      .list(memoryContext(h.scene))
      .find((record) => record.content === input.content)!
    assert.ok(original)
    assert.equal(original.sources[0]!.eventRef?.revision, 1)
    const changed = h.context.edit(
      { ...first, parts: [{ type: 'text', text: 'Contextual body B' }] },
      h.scene,
      {
        editId: 'context-b',
        order: { kind: 'observed_cas', expectedBase: oldSnapshot.sourceEventRef! },
      },
    )
    h.context.edit(first, h.scene, {
      editId: 'context-a-again',
      order: { kind: 'observed_cas', expectedBase: changed.ref! },
    })
    assert.equal(
      h.engine.memory.list(memoryContext(h.scene)).some((record) => record.id === original.id),
      false,
    )
    const oldResult = (await extension(h).execute(
      'learning.propose',
      { ...input, content: 'FORBIDDEN_OLD_TURN_REBIND' },
      oldContext,
    )) as { status: string }
    assert.equal(oldResult.status, 'rejected')
    const fresh = (await extension(h).execute(
      'learning.propose',
      input,
      context(h, [h.source(first.id)], 'fresh-contextual-turn'),
    )) as { status: string }
    assert.equal(
      fresh.status,
      'queued',
      'source event generation is part of contextual dedup identity',
    )
    await h.engine.contextualLearning.queue.whenIdle()
    assert.equal(commitError, undefined, 'the fresh exact-generation proof must commit atomically')
    const current = h.engine.memory
      .list(memoryContext(h.scene))
      .find((record) => record.content === input.content)
    assert.ok(current)
    assert.ok(current.sources.some((source) => source.eventRef?.revision === 3))
    assert.equal(
      h.engine.memory.adminList().some((record) => record.content === 'FORBIDDEN_OLD_TURN_REBIND'),
      false,
    )
  } finally {
    await h.close()
  }
})

test('an already queued contextual candidate cannot recapture the edited source at dispatch', async (t) => {
  clock(t)
  const h = fixture({ learning: true, quiet: 300000 })
  try {
    const first = h.message('queued-context')
    h.receive(first)
    const captured = h.source(first.id)
    const queued = extension(h).execute(
      'learning.propose',
      { content: 'QUEUED_OLD_SOURCE_CANARY', kind: 'claim', sourceMessageIds: [first.id] },
      context(h, [captured], 'queued-turn'),
    )
    h.context.edit(
      { ...first, parts: [{ type: 'text', text: 'Replacement source content' }] },
      h.scene,
      {
        editId: 'queued-change',
        order: { kind: 'observed_cas', expectedBase: captured.sourceEventRef! },
      },
    )
    assert.equal(((await queued) as { status: string }).status, 'queued')
    await h.engine.contextualLearning.queue.whenIdle()
    assert.equal(
      h.engine.memory.adminList().some((record) => record.content === 'QUEUED_OLD_SOURCE_CANARY'),
      false,
    )
    assert.ok(h.engine.contextualLearning.queue.stats().cancelled > 0)
  } finally {
    await h.close()
  }
})

test('actual learning proof AND loses current eligibility after one edit, while an independently learned OR proof survives and old history is retained', async (t) => {
  clock(t)
  const h = fixture({ learning: true })
  const a = h.message('proof-a'),
    b = h.message('proof-b'),
    c = h.message('proof-c')
  h.install(async (_role, prompts) => {
    const ids = sourcesIn(prompts).map((message) => message.id)
    return ids.length === 1
      ? proposal(ids, 'INDEPENDENT_OR_ASSERTION')
      : completion([
          ...proposal(ids, 'JOINT_AND_ASSERTION').toolCalls,
          {
            ...proposal([ids[0]!], 'INDEPENDENT_OR_ASSERTION').toolCalls[0]!,
            id: 'second-proposal',
          },
        ])
  })
  try {
    h.receive(a)
    h.receive(b)
    await advance(t, 100)
    h.receive(c)
    await advance(t, 100)
    const records = h.engine.memory.list(memoryContext(h.scene))
    const and = records.find((record) => record.content === 'JOINT_AND_ASSERTION')!
    const or = records.find((record) => record.content === 'INDEPENDENT_OR_ASSERTION')!
    assert.ok(and)
    assert.ok(or)
    h.context.edit({ ...a, parts: [{ type: 'text', text: 'Proof A changed' }] }, h.scene, {
      editId: 'proof-a-change',
      order: { kind: 'observed_cas', expectedBase: h.current(a.id).ref },
    })
    const visible = h.engine.memory.list(memoryContext(h.scene))
    assert.equal(
      visible.some((record) => record.id === and.id),
      false,
    )
    assert.equal(
      visible.some((record) => record.id === or.id),
      true,
    )
    assert.equal(
      h.engine.memory.adminList().find((record) => record.id === and.id)?.status,
      'active',
    )
    assert.ok(h.engine.memory.historyFor(memoryContext(h.scene), and.id)?.revisions.length)
    assert.ok(h.engine.memory.historyFor(memoryContext(h.scene), or.id)?.revisions.length)
    const restartedStore = new Store(h.path)
    const restarted = new Engine(restartedStore)
    try {
      assert.equal(
        restarted.memory.list(memoryContext(h.scene)).some((record) => record.id === and.id),
        false,
      )
      assert.equal(
        restarted.memory.list(memoryContext(h.scene)).some((record) => record.id === or.id),
        true,
      )
      assert.ok(restarted.memory.historyFor(memoryContext(h.scene), and.id)?.revisions.length)
    } finally {
      await restarted.dispose()
      restartedStore.close()
    }
    assert.equal(h.sends(), 0)
  } finally {
    await h.close()
  }
})

test('source recall removes all event revisions and learned historical bodies without touching an independent same-body scene', async (t) => {
  clock(t)
  const h = fixture({ learning: true })
  h.install(async (_role, prompts) =>
    proposal(
      sourcesIn(prompts).map((message) => message.id),
      'SOURCE_RECALL_ASSERTION_CANARY',
    ),
  )
  try {
    const first = h.message('recall-all')
    h.receive(first)
    await advance(t, 100)
    const learned = h.engine.memory.adminList()[0]!
    assert.ok(learned)
    const changed = h.context.edit(
      { ...first, parts: [{ type: 'text', text: 'Source revised before recall' }] },
      h.scene,
      {
        editId: 'recall-v2',
        order: { kind: 'observed_cas', expectedBase: h.current(first.id).ref },
      },
    )
    const foreign = h.context.scene('other-account', 'group', 'other-group')
    h.store.saveScene({
      ...foreign,
      name: 'Independent source scene',
      participationEnabled: false,
      learningEnabled: false,
      proactiveEnabled: false,
    })
    const other = h.message('recall-all', 'SYNTHETIC_SOURCE_BODY_A', foreign)
    h.context.receive(other, foreign, false)
    const independent = h.store.sourceEvents.registered(foreign.id, other.id)!
    h.context.recall(h.scene, first.id)
    assert.equal(h.current(first.id), undefined)
    assert.equal(
      h.store.sourceEvents.historical(changed.ref!, sourceIdentity(h.scene, first)).eligible,
      false,
    )
    for (const table of [
      'source_event_heads',
      'source_event_revisions',
      'source_event_edit_keys',
    ]) {
      const serialized = JSON.stringify(h.store.db.prepare(`SELECT * FROM ${table}`).all())
      assert.equal(serialized.includes(changed.ref!.eventId), false)
    }
    assert.deepEqual(h.store.sourceEvents.registered(foreign.id, other.id), independent)
    const retained = h.store.db.prepare('SELECT value FROM memory_assertion_revisions').all()
    assert.equal(JSON.stringify(retained).includes('SOURCE_RECALL_ASSERTION_CANARY'), false)
    assert.equal(
      h.engine.memory.historyFor(memoryContext(h.scene), learned.id)?.revisions.length ?? 0,
      0,
    )
    h.context.receive(first, h.scene, false)
    assert.equal(h.current(first.id), undefined)
    assert.equal(h.sends(), 0)
  } finally {
    await h.close()
  }
})

async function mediaFixture(options: { participation?: boolean } = {}) {
  const h = fixture(options)
  const red = await sharp({ create: { width: 2, height: 2, channels: 3, background: '#ff0000' } })
    .png()
    .toBuffer()
  const blue = await sharp({ create: { width: 2, height: 2, channels: 3, background: '#0000ff' } })
    .png()
    .toBuffer()
  let bytes: Buffer = red,
    fail = false,
    reads: string[] = [],
    refreshes = 0
  let history: Message[] = []
  const adapter: RuntimeAdapter = {
    send: async () => {
      throw new Error('Unexpected media test platform send')
    },
    history: async () => ({ items: history, coverage: 'complete' }),
    getMessage: async () => {
      refreshes++
      return { items: history, coverage: 'complete' }
    },
    readImage: async (_scene, assetId) => {
      reads.push(assetId)
      if (fail) throw new Error('synthetic_locator_unavailable')
      return bytes
    },
    dispose() {},
  }
  // Media transport's actual Runtime port is exercised separately from normalized AdapterContext ingress.
  h.engine.runtime.addAdapter(h.scene.adapterId, adapter)
  const first: RuntimeMessage = {
    ...h.message('image-source'),
    parts: [{ type: 'media', assetId: 'synthetic-opaque-image-reference', mediaType: 'image/png' }],
  }
  return {
    ...h,
    first,
    red,
    blue,
    reads,
    refreshes: () => refreshes,
    pixels(value: Buffer) {
      bytes = value
    },
    fail() {
      fail = true
    },
    history(items: Message[]) {
      history = items
    },
    ingest(schedule = false) {
      h.engine.runtime.ingest(first, h.scene, schedule)
    },
    edit(value: RuntimeMessage, editId: string, schedule = false) {
      return h.engine.runtime.edit(
        value,
        h.scene,
        { editId, order: { kind: 'observed_cas', expectedBase: h.current(first.id).ref } },
        schedule,
      )
    },
  }
}

test('same message and media locator in a new explicit event revision cannot reuse old image bytes or a previously authorized read handle', async () => {
  const h = await mediaFixture()
  try {
    h.ingest()
    const signal = new AbortController().signal
    const old = await h.engine.runtime.resolveDisplayImage(h.scene.id, h.first.id, 0, signal)
    assert.ok(old)
    assert.deepEqual(await old.read(), h.red)
    assert.deepEqual(await old.read(), h.red)
    assert.equal(h.reads.length, 1)
    h.pixels(h.blue)
    const changed = h.edit(h.first, 'same-locator-new-pixels')
    assert.equal(changed.status, 'edited')
    assert.equal(changed.ref?.revision, 2)
    assert.equal(old.available(), false)
    await assert.rejects(old.read(), /image_source_unavailable/)
    const current = await h.engine.runtime.resolveDisplayImage(h.scene.id, h.first.id, 0, signal)
    assert.ok(current)
    assert.deepEqual(await current.read(), h.blue)
    assert.equal(h.reads.length, 2)
    for (const table of [
      'source_event_heads',
      'source_event_revisions',
      'source_event_edit_keys',
    ]) {
      const metadata = JSON.stringify(h.store.db.prepare(`SELECT * FROM ${table}`).all())
      assert.equal(metadata.includes('synthetic-opaque-image-reference'), false)
      assert.equal(metadata.includes(h.red.toString('base64')), false)
    }
    h.engine.runtime.recall(h.scene.id, h.first.id)
    assert.equal(current.available(), false)
    assert.equal(
      await h.engine.runtime.resolveDisplayImage(h.scene.id, h.first.id, 0, signal),
      undefined,
    )
    assert.equal(h.reads.length, 2)
  } finally {
    await h.close()
  }
})

test('a bound image event cannot refresh an expired locator by silently substituting an unversioned history image', async () => {
  const h = await mediaFixture()
  try {
    h.ingest()
    h.fail()
    h.history([
      {
        ...h.first,
        origin: 'history',
        parts: [
          { type: 'media', assetId: 'UNAUTHORIZED_REPLACEMENT_IMAGE', mediaType: 'image/png' },
        ],
      },
    ])
    const old = await h.engine.runtime.resolveDisplayImage(
      h.scene.id,
      h.first.id,
      0,
      new AbortController().signal,
    )
    assert.ok(old)
    await assert.rejects(old.read(), /synthetic_locator_unavailable/)
    assert.deepEqual(h.reads, ['synthetic-opaque-image-reference'])
    assert.equal(h.refreshes(), 0, 'history is not a trusted edit or replacement source')
    assert.equal(h.current(h.first.id).ref.revision, 1)
    assert.equal(h.engine.memory.adminList().length, 0)
  } finally {
    await h.close()
  }
})

test('late vision for an actual old Runtime turn cannot populate a matching-locator new revision or reach participation', async (t) => {
  const realTimeout = setTimeout,
    realClearTimeout = clearTimeout
  clock(t)
  const h = await mediaFixture({ participation: true })
  h.store.set('models', {
    providers: [
      {
        id: 'synthetic-vision',
        name: 'Offline vision fixture',
        protocol: 'openai-chat',
        baseUrl: 'https://offline.invalid',
        credentialEnv: '',
      },
    ],
    models: [
      {
        id: 'vision',
        displayName: 'Offline vision',
        providerId: 'synthetic-vision',
        apiModelName: 'fixture',
        capabilities: ['vision'],
      },
    ],
    routes: [{ role: 'vision', orderedModelIds: ['vision'], timeoutMs: 5000 }],
  })
  let release = () => {},
    entered = () => {},
    visionSignal: AbortSignal | undefined,
    visionCalls = 0
  const pending = new Promise<void>((resolve) => {
    release = resolve
  })
  const started = new Promise<void>((resolve) => {
    entered = resolve
  })
  const participation: string[] = []
  let currentDone = () => {}
  const currentObserved = new Promise<void>((resolve) => {
    currentDone = resolve
  })
  h.install(async (role, prompts, tools, signal) => {
    if (role === 'vision') {
      visionCalls++
      if (visionCalls === 1) {
        visionSignal = signal
        entered()
        await pending
        return completion([], 'STALE_V1_VISION_DESCRIPTION_CANARY')
      }
      return completion([], 'CURRENT_V2_BLUE_DESCRIPTION')
    }
    assert.ok(tools?.some((tool) => tool.name === 'participation.decide'))
    participation.push(JSON.stringify(prompts))
    currentDone()
    return decide()
  })
  try {
    h.ingest(true)
    await advance(t, 10)
    await started
    h.pixels(h.blue)
    h.edit(h.first, 'vision-new-generation', true)
    assert.equal(visionSignal?.aborted, true)
    release()
    await advance(t, 1000)
    // Cancellation cleanup runs after the local decoder/provider settles; its replacement quiet
    // timer is measured from that cleanup, not from the preceding fake-clock jump.
    for (let i = 0; i < 100 && h.engine.runtime.activeTasks; i++) await immediate()
    await advance(t, 10)
    for (let i = 0; i < 100 && h.engine.runtime.activeTasks; i++) await immediate()
    let deadline: ReturnType<typeof setTimeout> | undefined
    try {
      await Promise.race([
        currentObserved,
        new Promise<void>((_resolve, reject) => {
          deadline = realTimeout(
            () => reject(new Error('current_revision_vision_did_not_settle')),
            5000,
          )
        }),
      ])
    } finally {
      if (deadline) realClearTimeout(deadline)
    }
    assert.equal(
      participation.some((input) => input.includes('STALE_V1_VISION_DESCRIPTION_CANARY')),
      false,
    )
    assert.ok(visionCalls >= 2, 'new revision receives its own current observation')
    assert.ok(participation.some((input) => input.includes('CURRENT_V2_BLUE_DESCRIPTION')))
  } finally {
    release()
    await h.close()
  }
})

test('an OFF-captured in-flight source-only result cannot borrow authority from later ON enrollment of the same ID', async (t) => {
  clock(t)
  const h = fixture({ enabled: false, learning: true })
  let release = () => {},
    signal: AbortSignal | undefined,
    calls = 0
  const pending = new Promise<void>((resolve) => {
    release = resolve
  })
  h.install(async (_role, prompts, _tools, currentSignal) => {
    calls++
    if (calls !== 1) return completion()
    signal = currentSignal
    const captured = sourcesIn(prompts)
    assert.equal(captured[0]?.sourceEventRef, undefined)
    await pending
    return proposal(
      captured.map((source) => source.id),
      'UNVERSIONED_LATE_AUTHORITY_BORROW_CANARY',
    )
  })
  try {
    const first = h.message('off-to-on', 'OFF_CAPTURED_BODY_A')
    h.receive(first)
    await advance(t, 100)
    assert.equal(calls, 1)
    assert.equal(h.store.sourceEvents.registered(h.scene.id, first.id), undefined)
    // The existing supported cache-OFF path evicts idle Runtime messages, not in-flight
    // background evidence. No test-only head edits or guessed platform edit are involved.
    h.store.set('settings', {
      ...h.store.settings,
      cacheEnabled: false,
      sourceEventRevisionsEnabled: true,
    })
    h.engine.settingsChanged()
    assert.deepEqual(h.engine.runtime.preview(h.scene.id), [])
    h.receive({ ...first, parts: [{ type: 'text', text: 'ON_FIRST_ENROLLMENT_BODY_B' }] })
    assert.equal(
      h.current(first.id).ref.revision,
      1,
      'this is first enrollment, never an inferred edit',
    )
    assert.equal(
      signal?.aborted,
      false,
      'the commit guard must reject even a still-running provider result',
    )
    release()
    await advance(t, 1000)
    assert.equal(
      h.engine.memory
        .adminList()
        .some((record) => record.content === 'UNVERSIONED_LATE_AUTHORITY_BORROW_CANARY'),
      false,
    )
    assert.equal(h.current(first.id).ref.revision, 1)
    assert.equal(h.sends(), 0)
  } finally {
    release()
    await h.close()
  }
})

test('source event enrollment capacity preserves ordinary reply but cannot silently downgrade the source into unbound learning', async (t) => {
  clock(t)
  const h = fixture()
  // Small-cap injection preserves the production readiness and external-authority hooks.
  ;(h.store as unknown as { sourceEventStore: SourceEventStore }).sourceEventStore =
    new SourceEventStore(h.store.db, {
      maxHeadsPerScene: 1,
      maxHeadsTotal: 1,
      assertReady: () => h.store.assertRecoveryReady(),
      beforeWrite: (mutation) => recordSourceEventMutation(h.store.db, mutation),
    })
  let extracts = 0,
    replies = 0
  h.install(async (role, prompts, tools) => {
    if (tools?.some((tool) => tool.name === 'participation.decide')) return decide('reply')
    if (tools?.some((tool) => tool.name === 'reply.plan')) {
      const plan = fixtureReplyPlan(prompts)!
      plan.actions = [
        {
          toolName: 'learning.propose',
          arguments: {
            content: 'CAPACITY_SOURCE_MUST_NOT_LEARN',
            kind: 'claim',
            sourceMessageIds: [plan.contextMessageIds.at(-1)!],
          },
        },
      ]
      return completion([{ id: 'capacity-plan', name: 'reply.plan', arguments: plan }])
    }
    if (role === 'learning') {
      extracts++
      return completion()
    }
    assert.equal(role, 'replyer')
    if (++replies > 1) return completion()
    return completion([
      {
        id: 'capacity-reply',
        name: 'messages.send',
        arguments: {
          operationId: 'capacity-ordinary-reply',
          segments: [
            {
              parts: [
                { type: 'text', text: 'Synthetic ordinary reply without a learned assertion.' },
              ],
            },
          ],
        },
      },
    ])
  })
  try {
    const enrolled = h.message('capacity-occupant')
    h.receive(enrolled)
    assert.equal(h.current(enrolled.id).ref.revision, 1)
    h.change({ participationEnabled: true, learningEnabled: true })
    const limited = h.message('capacity-limited')
    h.receive(limited)
    assert.equal(h.store.sourceEvents.registered(h.scene.id, limited.id), undefined)
    assert.equal(h.source(limited.id).sourceEventRegistrationLimited, true)
    await advance(t, 10)
    await advance(t, 1000)
    await h.engine.contextualLearning.queue.whenIdle()
    assert.equal(
      h.sends(),
      1,
      'capacity is a learning limitation, not a blanket participation outage',
    )
    assert.equal(extracts, 0)
    assert.equal(h.engine.contextualLearning.queue.stats().accepted, 0)
    assert.equal(h.engine.memory.adminList().length, 0)
    assert.equal(
      (
        h.store.db.prepare('SELECT COUNT(*) AS count FROM source_event_heads').get() as {
          count: number
        }
      ).count,
      1,
    )
    assert.ok(h.store.logs.some((entry) => entry.code === 'source_event_registration_limited'))
  } finally {
    await h.close()
  }
})
