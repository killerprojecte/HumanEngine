import { fixturePlanCompletion, fixtureReplyPlan, withFixturePlan } from './helpers/reply-plan.ts'
import test from 'node:test'
import assert from 'node:assert/strict'
import { Runtime, type RuntimeMessage, type RuntimeAdapter } from '../src/runtime.ts'
import { Store } from '../src/store.ts'
import type { Router, Completion, ChatMessage } from '../src/models.ts'
import type { SceneRef, SendResult } from '../src/onebot.ts'

const completion = (toolCalls: Completion['toolCalls'] = [], text = ''): Completion => ({
  text,
  toolCalls,
  modelId: 'fixture',
  usage: { inputTokens: 0, outputTokens: 0, totalTokens: 0 },
  assistant: { role: 'assistant', content: text, toolCalls },
})
const decide = (action: 'reply' | 'silent' | 'wait' = 'reply') =>
  completion([
    {
      id: 'decision',
      name: 'participation.decide',
      arguments: { action, reasonCode: 'private-model-reason-canary' },
    },
  ])
const send = () =>
  completion([
    {
      id: 'send-call',
      name: 'messages.send',
      arguments: {
        operationId: 'fixture-operation',
        segments: [
          { parts: [{ type: 'text', text: 'fixture answer one' }] },
          { parts: [{ type: 'text', text: 'fixture answer two' }] },
        ],
      },
    },
  ])
const scene = (id = 'flow-scene'): SceneRef => ({
  id,
  kind: 'group',
  adapterId: 'fixture',
  accountId: 'bot',
  platformSceneId: id,
})
const message = (s: SceneRef, id = 'incoming'): RuntimeMessage => ({
  id,
  sceneId: s.id,
  senderId: 'fixture-person',
  origin: 'live',
  status: 'available',
  sentAt: new Date().toISOString(),
  receivedAt: new Date().toISOString(),
  parts: [{ type: 'text', text: 'private-message-canary' }],
})
const pause = (ms = 5) => new Promise((resolve) => setTimeout(resolve, ms))
async function until(check: () => boolean) {
  for (let n = 0; n < 200; n++) {
    if (check()) return
    await pause()
  }
  assert.fail('fixture did not settle')
}
function fixture(
  complete: (role: string, messages: ChatMessage[]) => Promise<Completion>,
  receipt: () => Promise<SendResult> = async () => ({
    status: 'sent',
    messageId: crypto.randomUUID(),
  }),
  planning?: (messages: ChatMessage[]) => Promise<Completion>,
) {
  const store = new Store(':memory:')
  store.set('settings', { ...store.settings, debounceMs: 5, maxWaitMs: 100 })
  const runtime = new Runtime(
    store,
    () =>
      ({
        complete: async (role: string, messages: ChatMessage[]) => {
          if (role === 'planner' && fixtureReplyPlan(messages) && planning)
            return planning(messages)
          return withFixturePlan(complete)(role, messages)
        },
      }) as Router,
  )
  const adapter: RuntimeAdapter = {
    send: receipt,
    history: async () => ({ items: [], coverage: 'partial' }),
    getMessage: async () => ({ items: [], coverage: 'partial' }),
    dispose() {},
  }
  runtime.addAdapter('fixture', adapter)
  const add = (s: SceneRef) =>
    store.saveScene({
      ...s,
      name: 'Fixture',
      participationEnabled: true,
      proactiveEnabled: true,
      learningEnabled: false,
    })
  const end = () => {
    runtime.dispose()
    store.close()
  }
  return { store, runtime, add, end }
}
function balanced(store: Store) {
  const rows = store.logs.filter((row) => row.module === 'flow')
  const started = rows.filter((row) => row.status === 'started')
  for (const row of started) {
    const ends = rows.filter(
      (other) =>
        other.spanId === row.spanId &&
        other.status !== 'started' &&
        other.status !== 'cancel_requested',
    )
    assert.equal(ends.length, 1, `one terminal for ${row.stage}`)
    assert.equal(ends[0]?.traceId, row.traceId)
    assert.equal(ends[0]?.turnId, row.turnId)
    assert.ok((ends[0]?.durationMs ?? -1) >= 0)
  }
  assert.equal(JSON.stringify(rows).includes('private-message-canary'), false)
  assert.equal(JSON.stringify(rows).includes('private-model-reason-canary'), false)
  assert.equal(JSON.stringify(rows).includes('fixture answer one'), false)
}

test('a mock group flow has correlated ingress, queue, decision, tool and two authoritative segment outcomes', async () => {
  let thinker = 0
  const f = fixture(async (role) =>
    role === 'planner'
      ? decide()
      : ++thinker === 1
        ? send()
        : completion([], 'private-terminal-text-canary'),
  )
  const s = scene()
  f.add(s)
  f.runtime.installTypingDelay({ speedFactor: 10 }, new AbortController().signal)
  try {
    f.runtime.ingest(message(s), s)
    await until(() =>
      f.store.logs.some((row) => row.stage === 'turn' && row.status === 'completed'),
    )
    const rows = f.store.logs.filter((row) => row.module === 'flow')
    for (const stage of [
      'ingress',
      'routing',
      'queue',
      'turn',
      'context_prepare',
      'decision',
      'tool',
      'send',
      'send_segment',
      'typing_delay',
      'send_validation',
    ])
      assert.ok(
        rows.some((row) => row.stage === stage),
        stage,
      )
    const turns = rows.filter((row) => row.stage === 'turn' && row.status === 'completed')
    assert.equal(turns.length, 1)
    assert.equal(turns[0]?.counts?.sentSegments, 2)
    assert.equal(turns[0]?.reason, 'reply_delivered')
    assert.equal(
      rows.filter((row) => row.stage === 'send_segment' && row.receiptState === 'sent').length,
      2,
    )
    assert.equal(new Set(rows.map((row) => row.traceId)).size, 1)
    assert.equal(rows.find((row) => row.stage === 'queue')?.turnId, undefined)
    assert.equal(JSON.stringify(rows).includes('private-terminal-text-canary'), false)
    balanced(f.store)
  } finally {
    f.end()
  }
})

test('silence, wait and disabled participation identify intentional skips rather than delivery', async () => {
  for (const action of ['silent', 'wait'] as const) {
    const f = fixture(async () => decide(action))
    const s = scene()
    f.add(s)
    try {
      f.runtime.ingest(message(s), s)
      await until(() =>
        f.store.logs.some((row) => row.stage === 'turn' && row.status === 'skipped'),
      )
      assert.equal(
        f.store.logs.find((row) => row.stage === 'turn' && row.status === 'skipped')?.reason,
        `decision_${action}`,
      )
      assert.equal(
        f.store.logs.some((row) => row.stage === 'send_segment'),
        false,
      )
      balanced(f.store)
    } finally {
      f.end()
    }
  }
  const f = fixture(async () => {
    throw new Error('should not run')
  })
  const s = scene()
  try {
    f.runtime.ingest(message(s), s)
    assert.ok(f.store.logs.some((row) => row.reason === 'participation_disabled'))
    balanced(f.store)
  } finally {
    f.end()
  }
})

test('forbidden dialogue tool and unknown send remain diagnostic failures, never successful delivery', async () => {
  for (const forbidden of [true, false]) {
    let thinker = 0,
      executed = 0
    const f = fixture(
      async (role) =>
        role === 'planner'
          ? decide()
          : ++thinker === 1
            ? forbidden
              ? completion([{ id: 'tool', name: 'fixture.fail', arguments: {} }])
              : send()
            : completion(),
      async () => ({ status: 'unknown', detail: 'private-upstream-canary' }),
    )
    const s = scene()
    f.add(s)
    f.runtime.addExtension({
      tools: [{ name: 'fixture.fail', description: 'Fixture', inputSchema: { type: 'object' } }],
      async execute() {
        executed++
        throw new Error('private-error-canary')
      },
    })
    try {
      f.runtime.ingest(message(s), s)
      await until(() => f.store.logs.some((row) => row.stage === 'turn' && row.status === 'failed'))
      assert.ok(f.store.logs.some((row) => row.stage === 'tool' && row.status === 'failed'))
      assert.equal(executed, 0)
      assert.equal(thinker, 1)
      if (forbidden)
        assert.equal(
          f.store.logs.some((row) => row.stage === 'send_segment'),
          false,
        )
      else
        assert.ok(
          f.store.logs.some(
            (row) =>
              row.stage === 'send_segment' &&
              row.status === 'partial' &&
              row.receiptState === 'unknown',
          ),
        )
      assert.equal(
        f.store.logs.find((row) => row.stage === 'turn' && row.status === 'failed')?.counts
          ?.sentSegments,
        0,
      )
      assert.equal(JSON.stringify(f.store.logs).includes('private-upstream-canary'), false)
      assert.equal(JSON.stringify(f.store.logs).includes('private-error-canary'), false)
      balanced(f.store)
    } finally {
      f.end()
    }
  }
})

test('queue cancellation and concurrent scenes close spans with isolated traces and no inherited foreign turn', async () => {
  let release!: () => void
  const gate = new Promise<void>((resolve) => {
    release = resolve
  })
  const f = fixture(async (role) => {
    if (role === 'planner') {
      await gate
      return decide('silent')
    }
    return completion()
  })
  const a = scene('flow-a'),
    b = scene('flow-b')
  f.add(a)
  f.add(b)
  try {
    f.runtime.ingest(message(a), a)
    f.runtime.ingest(message(b), b)
    await until(() => f.runtime.activeTasks === 2)
    f.runtime.ingest(message(a, 'second'), a)
    f.runtime.cancel(a.id)
    release()
    await until(() => f.runtime.activeTasks === 0)
    const aRows = f.store.logs.filter((row) => row.sceneId === a.id && row.module === 'flow')
    const bRows = f.store.logs.filter((row) => row.sceneId === b.id && row.module === 'flow')
    assert.ok(aRows.some((row) => row.stage === 'queue' && row.status === 'cancelled'))
    assert.ok(aRows.some((row) => row.stage === 'turn' && row.status === 'cancel_requested'))
    assert.equal(
      aRows.some((row) => bRows.some((other) => other.traceId === row.traceId)),
      false,
    )
    for (const row of f.store.logs.filter((row) => row.stage === 'queue'))
      assert.equal(row.turnId, undefined)
    assert.ok(f.store.logs.some((row) => row.stage === 'turn' && row.status === 'cancelled'))
    balanced(f.store)
  } finally {
    release()
    f.end()
  }
})

test('typing cancellation and router setup failure close every started span without completed delivery', async () => {
  let thinker = 0
  const f = fixture(async (role) =>
    role === 'planner' ? decide() : ++thinker === 1 ? send() : completion(),
  )
  const s = scene()
  f.add(s)
  f.runtime.installTypingDelay({ speedFactor: 0.1 }, new AbortController().signal)
  try {
    f.runtime.ingest(message(s), s)
    await until(() =>
      f.store.logs.some((row) => row.stage === 'typing_delay' && row.status === 'started'),
    )
    f.runtime.recall(s.id, 'incoming')
    await until(() => f.runtime.activeTasks === 0)
    await until(() =>
      f.store.logs.some((row) => row.stage === 'turn' && row.status === 'cancel_requested'),
    )
    assert.ok(
      f.store.logs.some((row) => row.stage === 'typing_delay' && row.status === 'cancel_requested'),
    )
    assert.equal(
      f.store.logs.some((row) => row.stage === 'send_segment' && row.status === 'completed'),
      false,
    )
    balanced(f.store)
  } finally {
    f.end()
  }
  const store = new Store(':memory:')
  store.set('settings', { ...store.settings, debounceMs: 0 })
  store.saveScene({
    ...s,
    name: 'Fixture',
    participationEnabled: true,
    learningEnabled: false,
    proactiveEnabled: true,
  })
  const runtime = new Runtime(store, () => {
    throw new Error('private-router-setup-canary')
  })
  try {
    runtime.ingest(message(s), s)
    await until(() => store.logs.some((row) => row.stage === 'turn' && row.status === 'failed'))
    balanced(store)
    assert.equal(JSON.stringify(store.logs).includes('private-router-setup-canary'), false)
  } finally {
    runtime.dispose()
    store.close()
  }
})

test('image hooks prepare incoming topic context before participation, while text-only silence does not fetch images', async () => {
  let hookCalls = 0,
    observed: string[] = []
  const f = fixture(async (role) =>
    role === 'planner'
      ? completion([
          {
            id: 'decision',
            name: 'participation.decide',
            arguments: {
              action: 'reply',
              reasonCode: 'fixture',
              topicId: 'selected',
              targetMessageIds: ['text'],
              topics: [
                { topicId: 'old', messageIds: ['old-image'] },
                { topicId: 'selected', messageIds: ['text'] },
              ],
            },
          },
        ])
      : completion(),
  )
  const s = scene()
  f.add(s)
  f.runtime.addExtension({
    tools: [],
    async execute() {
      return {}
    },
    async describeImages(_context, messages) {
      hookCalls++
      observed = messages.map((item) => item.id)
      return { status: 'unavailable' }
    },
  })
  try {
    f.runtime.ingest(
      {
        ...message(s, 'old-image'),
        parts: [{ type: 'media', mediaType: 'image/png', assetId: 'opaque-image' }],
      },
      s,
      false,
    )
    f.runtime.ingest(message(s, 'text'), s)
    await until(() =>
      f.store.logs.some((row) => row.stage === 'turn' && row.status === 'completed'),
    )
    assert.equal(hookCalls, 1)
    assert.deepEqual(observed, ['old-image', 'text'])
  } finally {
    f.end()
  }
  const silent = fixture(async () => decide('silent'))
  silent.add(s)
  silent.runtime.addExtension({
    tools: [],
    async execute() {
      return {}
    },
    async describeImages() {
      assert.fail('silent decision must not start vision')
    },
  })
  try {
    silent.runtime.ingest(message(s), s)
    await until(() =>
      silent.store.logs.some((row) => row.stage === 'turn' && row.status === 'skipped'),
    )
  } finally {
    silent.end()
  }
})

test('cancellation closes active spans immediately but does not falsely report pending tool cleanup as finished', async () => {
  let release!: () => void,
    entered = false,
    planner = 0
  const gate = new Promise<void>((resolve) => {
    release = resolve
  })
  const f = fixture(
    async (role) => (role === 'planner' ? decide() : completion()),
    undefined,
    async (messages) =>
      ++planner === 1
        ? completion([{ id: 'pending', name: 'fixture.pending', arguments: {} }])
        : fixturePlanCompletion('planner', messages)!,
  )
  const s = scene()
  f.add(s)
  f.runtime.addExtension({
    tools: [
      {
        name: 'fixture.pending',
        effect: 'read',
        description: 'Fixture',
        inputSchema: { type: 'object' },
      },
    ],
    async execute() {
      entered = true
      await gate
      return {}
    },
  })
  try {
    f.runtime.ingest(message(s), s)
    await until(() => entered)
    f.runtime.cancel(s.id)
    assert.ok(
      f.store.logs.some(
        (row) =>
          row.stage === 'tool' &&
          row.status === 'cancel_requested' &&
          row.reason === 'cancellation_requested' &&
          row.nextStep === 'wait_for_cleanup',
      ),
    )
    assert.ok(f.store.logs.some((row) => row.stage === 'turn' && row.status === 'cancel_requested'))
    assert.equal(
      f.store.logs.some((row) => row.stage === 'turn_cleanup'),
      false,
    )
    assert.equal(f.runtime.activeTasks, 1)
    assert.equal(
      f.store.logs.some((row) => row.stage === 'tool' && row.status === 'cancelled'),
      false,
    )
    release()
    await until(() => f.runtime.activeTasks === 0)
    assert.ok(
      f.store.logs.some((row) => row.stage === 'turn_cleanup' && row.status === 'completed'),
    )
    assert.equal(
      f.store.logs.some((row) => row.stage === 'tool' && row.status === 'completed'),
      false,
    )
    assert.ok(f.store.logs.some((row) => row.stage === 'tool' && row.status === 'cancelled'))
    assert.ok(f.store.logs.some((row) => row.stage === 'turn' && row.status === 'cancelled'))
    balanced(f.store)
  } finally {
    release()
    f.end()
  }
})

test('coalesced group messages retain linked receipt traces on their single queued turn', async () => {
  const f = fixture(async () => decide('silent'))
  const s = scene()
  f.add(s)
  try {
    f.runtime.ingest(message(s, 'one'), s)
    f.runtime.ingest(message(s, 'two'), s)
    await until(() => f.store.logs.some((row) => row.stage === 'turn' && row.status === 'skipped'))
    const receipts = f.store.logs
      .filter((row) => row.stage === 'ingress' && row.status === 'completed')
      .map((row) => row.traceId)
    const queued = f.store.logs.find((row) => row.stage === 'queue' && row.status === 'completed')
    const turn = f.store.logs.find((row) => row.stage === 'turn' && row.status === 'skipped')
    assert.equal(queued?.counts?.queuedMessages, 2)
    assert.deepEqual(new Set(queued?.linkedTraceIds), new Set(receipts))
    assert.deepEqual(new Set(turn?.linkedTraceIds), new Set(receipts))
    assert.equal(
      f.store.logs.filter((row) => row.stage === 'queue' && row.status === 'started').length,
      1,
    )
    balanced(f.store)
  } finally {
    f.end()
  }
})

test('cancellation after a confirmed segment preserves partial send and turn outcomes', async () => {
  let thinker = 0,
    dispatched = 0
  const s = scene()
  const f = fixture(
    async (role) => (role === 'planner' ? decide() : ++thinker === 1 ? send() : completion()),
    async () => {
      if (++dispatched === 1) return { status: 'sent', messageId: 'confirmed-first' }
      f.runtime.cancel(s.id)
      return { status: 'cancelled' }
    },
  )
  f.add(s)
  try {
    f.runtime.ingest(message(s), s)
    await until(() =>
      f.store.logs.some((row) => row.stage === 'turn_cleanup' && row.status === 'completed'),
    )
    assert.equal(dispatched, 2)
    for (const stage of ['send', 'turn']) {
      const terminal = f.store.logs.filter(
        (row) =>
          row.stage === stage && row.status !== 'started' && row.status !== 'cancel_requested',
      )
      assert.equal(terminal.length, 1)
      assert.equal(terminal[0]?.status, 'partial')
      assert.equal(terminal[0]?.counts?.sentSegments, 1)
      assert.equal(terminal[0]?.counts?.cancelledSegments, 1)
    }
    assert.ok(
      f.store.logs.some((row) => row.stage === 'send_segment' && row.status === 'cancelled'),
    )
    balanced(f.store)
  } finally {
    f.end()
  }
})

test('cancellation cannot erase an uncertain external send outcome from the turn', async () => {
  let thinker = 0
  const s = scene()
  const f = fixture(
    async (role) => (role === 'planner' ? decide() : ++thinker === 1 ? send() : completion()),
    async () => {
      f.runtime.cancel(s.id)
      return { status: 'unknown' }
    },
  )
  f.add(s)
  try {
    f.runtime.ingest(message(s), s)
    await until(() =>
      f.store.logs.some((row) => row.stage === 'turn_cleanup' && row.status === 'completed'),
    )
    const terminal = f.store.logs.find((row) => row.stage === 'turn' && row.status === 'partial')
    assert.equal(terminal?.counts?.unknownSegments, 1)
    assert.equal(terminal?.counts?.sentSegments, 0)
    assert.equal(terminal?.reason, 'delivery_unknown')
    assert.equal(
      f.store.logs.some(
        (row) => row.stage === 'turn' && ['completed', 'cancelled'].includes(row.status ?? ''),
      ),
      false,
    )
    balanced(f.store)
  } finally {
    f.end()
  }
})

test('simulation send and turn summaries do not claim platform-confirmed delivery', async () => {
  let thinker = 0,
    realSends = 0
  const f = fixture(
    async (role) => (role === 'planner' ? decide() : ++thinker === 1 ? send() : completion()),
    async () => {
      realSends++
      return { status: 'sent' }
    },
  )
  try {
    await f.runtime.simulate(f.runtime.createSimulation(), 'Synthetic input')
    assert.equal(realSends, 0)
    for (const stage of ['send', 'turn']) {
      const terminal = f.store.logs.find((row) => row.stage === stage && row.status === 'completed')
      assert.equal(terminal?.reason, 'simulated_delivery')
      assert.equal(terminal?.mode, 'simulation')
    }
    assert.equal(
      f.store.logs.some((row) =>
        ['reply_delivered', 'segments_delivered', 'segment_delivered'].includes(row.reason ?? ''),
      ),
      false,
    )
    balanced(f.store)
  } finally {
    f.end()
  }
})
