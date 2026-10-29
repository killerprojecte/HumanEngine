import assert from 'node:assert/strict'
import test, { type TestContext } from 'node:test'
import { setImmediate as immediate } from 'node:timers/promises'
import { Engine } from '../src/engine.ts'
import { Store } from '../src/store.ts'
import { ModelError, type Completion, type Router } from '../src/models.ts'
import type { RuntimeMessage } from '../src/runtime.ts'
import type { SceneRef } from '../src/onebot.ts'
import type { LearningAdmission, LearningBatch } from '../src/memory/learning/admission.ts'

const scene: SceneRef = {
  id: 'admission-recovery-scene',
  kind: 'group',
  adapterId: 'offline-fixture',
  accountId: 'offline-bot',
  platformSceneId: 'synthetic-group',
}
const completion = (toolCalls: Completion['toolCalls'] = []): Completion => ({
  text: '',
  toolCalls,
  modelId: 'scripted-offline',
  usage: { inputTokens: 0, outputTokens: 0, totalTokens: 0 },
  assistant: { role: 'assistant', content: '', toolCalls },
})
const message = (id: string): RuntimeMessage => ({
  id,
  sceneId: scene.id,
  senderId: 'synthetic-person',
  sentAt: new Date().toISOString(),
  receivedAt: new Date().toISOString(),
  origin: 'live',
  status: 'available',
  parts: [{ type: 'text', text: `Synthetic neutral input ${id}.` }],
})
const sorted = (ids: Iterable<string>) => [...ids].sort()
function clock(t: TestContext) {
  t.mock.timers.enable({
    apis: ['Date', 'setTimeout', 'setInterval'],
    now: new Date('2026-10-28T00:00:00Z'),
  })
}
async function advance(t: TestContext, ms = 100) {
  t.mock.timers.tick(ms)
  await immediate()
  t.mock.timers.tick(0)
  await immediate()
}
type Attempt = {
  ids: string[]
  projected: RuntimeMessage[]
  signal: AbortSignal | undefined
  release(): void
  returned: boolean
}
function fixture(heldCalls = 0) {
  const store = new Store(':memory:')
  store.set('settings', {
    ...store.settings,
    learningBatchMs: 100,
    learningMaxWaitMs: 100,
    learningMinIntervalMs: 100,
    learningQueueMaxMessages: 120,
    learningQueueMaxTotalMessages: 120,
  })
  store.saveScene({
    ...scene,
    name: 'Synthetic admission recovery',
    participationEnabled: false,
    learningEnabled: true,
    proactiveEnabled: false,
  })
  const engine = new Engine(store)
  let sends = 0
  engine.runtime.addAdapter(scene.adapterId, {
    send: async () => {
      sends++
      throw new Error('Unexpected synthetic transport invocation')
    },
    getMessage: async () => ({ items: [], coverage: 'complete' }),
    history: async () => ({ items: [], coverage: 'complete' }),
    dispose() {},
  })
  // Test-only inspection/fault injection: production entry and scheduling remain Engine-owned.
  const internals = engine.learning as unknown as {
    admission: LearningAdmission
    learningBatches: Map<string, RuntimeMessage[]>
  }
  const tickets: LearningBatch[] = []
  const take = internals.admission.take.bind(internals.admission)
  internals.admission.take = (...args) => {
    const ticket = take(...args)
    if (ticket) tickets.push(structuredClone(ticket))
    return ticket
  }
  const attempts: Attempt[] = []
  let respond: (attempt: Attempt, index: number) => Completion = () => completion()
  engine.router = () =>
    ({
      complete: async (role, prompts, _tools, signal) => {
        assert.equal(role, 'learning')
        const projected = (JSON.parse(prompts[1]!.content) as { messages: RuntimeMessage[] })
          .messages
        const ids = projected.map((item) => {
          const text = item.parts.find((part) => part.type === 'text')
          assert.ok(text?.type === 'text')
          const match = /^Synthetic neutral input (.+)\.$/.exec(text.text)
          assert.ok(match, 'synthetic bodies identify originals despite projected model IDs')
          return match[1]!
        })
        let release!: () => void
        const pending = new Promise<void>((resolve) => {
          release = resolve
        })
        const attempt: Attempt = {
          ids,
          projected,
          signal,
          release,
          returned: false,
        }
        attempts.push(attempt)
        if (attempts.length <= heldCalls) await pending
        attempt.returned = true
        return respond(attempt, attempts.length)
      },
    }) as Router
  const setLearning = (enabled: boolean) => {
    const next = { ...store.scene(scene.id)!, learningEnabled: enabled }
    store.saveScene(next)
    engine.sceneChanged(next)
  }
  return {
    store,
    engine,
    internals,
    tickets,
    attempts,
    ingest: (id: string) => engine.runtime.ingest(message(id), scene),
    setLearning,
    setResponse: (fn: typeof respond) => {
      respond = fn
    },
    sends: () => sends,
    close: async () => {
      for (const attempt of attempts) attempt.release()
      await engine.dispose()
      store.close()
    },
  }
}
type Fixture = ReturnType<typeof fixture>
async function drain(t: TestContext, h: Fixture) {
  // A bounded fake-clock observation window; stalled payloads fail rather than hang the suite.
  for (let tick = 0; tick < 8; tick++) await advance(t)
  const status = h.engine.learningStatus()
  assert.equal(status.queued, 0, 'authorized pending payloads must not be orphaned')
  assert.equal(status.active, 0)
  assert.equal(status.retrying, 0)
  assert.equal(status.paused, 0)
  assert.equal(h.internals.admission.inspect(scene.id).state, 'idle')
  assert.equal(h.sends(), 0)
}
function completedIds(h: Fixture) {
  return h.attempts
    .filter((attempt) => attempt.returned && !attempt.signal?.aborted)
    .flatMap((attempt) => attempt.ids)
}
function assertExactTickets(h: Fixture) {
  assert.equal(h.attempts.length, h.tickets.length)
  h.attempts.forEach((attempt, index) => {
    assert.deepEqual(sorted(attempt.ids), sorted(h.tickets[index]!.sourceKeys))
    assert.equal(new Set(attempt.ids).size, attempt.ids.length)
    assert.ok(attempt.ids.length <= 60)
  })
}

const original = Array.from({ length: 120 }, (_, i) => `source-${i}`)
for (const scenario of [
  { name: 'forward with pending-source recall', ids: original, overflow: 0, off: false },
  { name: 'reverse with overflow', ids: [...original].reverse(), overflow: 7, off: false },
  {
    name: 'interleaved with overflow then OFF',
    ids: original.filter((_, i) => i % 2 === 0).concat(original.filter((_, i) => i % 2 === 1)),
    overflow: 13,
    off: true,
  },
])
  test(`120 live Engine sources survive repeated precommit recall: ${scenario.name}`, async (t) => {
    clock(t)
    const h = fixture(2)
    const cancelled = new Set<string>()
    try {
      for (const id of scenario.ids) {
        h.ingest(id)
        h.engine.learning.enqueueLearning(message(id))
      }
      for (let i = 0; i < scenario.overflow; i++) h.ingest(`overflow-${i}`)
      assert.equal(h.engine.learningStatus().accepted, 120)
      assert.equal(h.engine.learningStatus().queued, 120)
      assert.equal(h.engine.learningStatus().overflow, scenario.overflow)
      for (let round = 0; round < 2; round++) {
        await advance(t)
        assert.equal(h.attempts.length, round + 1)
        const attempt = h.attempts[round]!
        const id = round === 1 && scenario.overflow === 0 ? scenario.ids[1]! : attempt.ids[0]!
        cancelled.add(id)
        h.engine.runtime.recall(scene.id, id)
        assert.equal(attempt.signal?.aborted, true)
        attempt.release()
        await advance(t, 0)
      }
      assert.equal(h.engine.learningStatus().queued, 118)
      assert.equal(h.engine.learningStatus().completed, 0)
      if (scenario.off) {
        for (const id of scenario.ids) cancelled.add(id)
        h.setLearning(false)
        assert.equal(h.engine.learningStatus().queued, 0)
        h.ingest('ignored-while-off')
        h.engine.settingsChanged()
        await advance(t, 1000)
        h.setLearning(true)
        h.engine.settingsChanged()
        await advance(t, 1000)
        assert.equal(h.attempts.length, 2, 'metadata repair must not backfill OFF work')
      }
      await drain(t, h)
      const completed = completedIds(h)
      assert.deepEqual(sorted(completed), sorted(scenario.ids.filter((id) => !cancelled.has(id))))
      assert.equal(new Set(completed).size, completed.length)
      assert.equal(h.engine.learningStatus().completed, completed.length)
      assert.equal(completed.length + cancelled.size, 120, 'every admitted source is accounted for')
      assertExactTickets(h)
      assert.ok(h.attempts.length <= 4, 'recall recovery cannot create a model loop')
      if (scenario.off) {
        h.ingest('fresh-after-off')
        await drain(t, h)
        assert.deepEqual(completedIds(h), ['fresh-after-off'])
        assert.equal(h.engine.learningStatus().accepted, 121)
      }
    } finally {
      await h.close()
    }
  })

test('missing admission metadata repairs only the bounded pending payload queue, independently of payload order', async (t) => {
  clock(t)
  const h = fixture()
  try {
    for (const id of original) h.ingest(id)
    h.internals.admission.cancelScene(scene.id)
    assert.equal(h.internals.admission.inspect(scene.id).state, 'idle')
    h.internals.learningBatches.get(scene.id)!.reverse()
    h.engine.settingsChanged()
    for (const id of original) {
      h.engine.learning.enqueueLearning(message(id))
      assert.equal(h.internals.admission.admit(scene.id, id), 'duplicate')
    }
    const admitted = h.internals.admission.inspect(scene.id)
    assert.ok(admitted.state === 'waiting' || admitted.state === 'ready')
    assert.equal(admitted.pending, 120)
    assert.equal(h.engine.learningStatus().accepted, 120)
    await drain(t, h)
    assert.deepEqual(sorted(completedIds(h)), sorted(original))
    assert.equal(h.attempts.length, 2)
    assert.equal(h.engine.learningStatus().completed, 120)
    assertExactTickets(h)
    h.engine.settingsChanged()
    await advance(t, 2000)
    assert.equal(h.attempts.length, 2, 'completed sources are never metadata-recovery input')
  } finally {
    await h.close()
  }
})

test('a ticket with missing payload is retired once while actual payload IDs still complete exactly once', async (t) => {
  clock(t)
  const h = fixture()
  try {
    for (const id of original.slice(0, 3)) h.ingest(id)
    assert.equal(h.internals.admission.admit(scene.id, 'missing-payload'), 'accepted')
    for (let i = 0; i < 20; i++)
      assert.equal(h.internals.admission.admit(scene.id, 'missing-payload'), 'duplicate')
    await drain(t, h)
    assert.equal(h.attempts.length, 1)
    assert.deepEqual(sorted(completedIds(h)), sorted(original.slice(0, 3)))
    assert.equal(h.tickets[0]!.sourceKeys.length, 4)
    assert.equal(h.engine.learningStatus().completed, 3)
    assert.equal(
      h.store.logs.filter((entry) => entry.code === 'learning_source_payload_unavailable').length,
      1,
    )
    h.engine.settingsChanged()
    await advance(t, 2000)
    assert.equal(h.attempts.length, 1)
  } finally {
    await h.close()
  }
})

test('metadata repair never readmits active payloads even if their ticket metadata disappears', async (t) => {
  clock(t)
  const h = fixture(1)
  try {
    for (const id of original) h.ingest(id)
    await advance(t)
    assert.equal(h.attempts.length, 1)
    h.internals.admission.cancelScene(scene.id)
    h.engine.settingsChanged()
    for (const id of h.attempts[0]!.ids) h.engine.learning.enqueueLearning(message(id))
    assert.equal(h.internals.admission.inspect(scene.id).state, 'idle')
    assert.equal(h.engine.learningStatus().active, 60)
    assert.equal(h.engine.learningStatus().queued, 60)
    assert.equal(h.engine.learningStatus().accepted, 120)
    await advance(t, 1000)
    assert.equal(h.attempts.length, 1, 'active authority cannot become another scheduled batch')
    h.attempts[0]!.release()
    await advance(t, 0)
    await drain(t, h)
    assert.equal(h.attempts.length, 2)
    assert.deepEqual(sorted(completedIds(h)), sorted(original))
    assert.equal(h.engine.learningStatus().completed, 120)
    assertExactTickets(h)
  } finally {
    await h.close()
  }
})

test('failed precommit payloads remain explicitly paused rather than being silently metadata-recovered', async (t) => {
  clock(t)
  const h = fixture()
  h.setResponse((_attempt, index) => {
    if (index === 1) throw new Error('synthetic_nonretryable_extraction_failure')
    return completion()
  })
  try {
    for (const id of original) h.ingest(id)
    await advance(t)
    const failure = h.engine.learningStatus().failures[0]!
    assert.equal(failure.partial, false)
    assert.equal(failure.canRetry, true)
    assert.equal(failure.messageCount, 60)
    assert.equal(h.engine.learningStatus().paused, 60)
    h.internals.admission.cancelScene(scene.id)
    h.engine.settingsChanged()
    for (let tick = 0; tick < 8; tick++) await advance(t)
    const status = h.engine.learningStatus()
    assert.equal(status.queued, 0)
    assert.equal(status.active, 0)
    assert.equal(status.retrying, 0)
    assert.equal(status.paused, 60)
    assert.equal(status.completed, 60)
    assert.equal(status.completed + status.paused, status.accepted)
    assert.equal(h.attempts.length, 2)
    assert.deepEqual(sorted(h.attempts[1]!.ids), sorted(original.slice(60)))
    assert.deepEqual(
      status.failures.map((item) => item.id),
      [failure.id],
    )
    assertExactTickets(h)
    h.engine.settingsChanged()
    await advance(t, 2000)
    assert.equal(h.attempts.length, 2, 'only an explicit authorized retry may restore a failure')
    assert.equal(h.sends(), 0)
  } finally {
    await h.close()
  }
})

for (const outcome of ['partial-failure', 'unknown-write-cancelled'] as const)
  test(`metadata recovery never replays a ${outcome} batch or replaces its unprocessed backlog`, async (t) => {
    clock(t)
    const h = fixture()
    let release!: () => void,
      indexes = 0
    const pending = new Promise<void>((resolve) => {
      release = resolve
    })
    h.setResponse((attempt, index) =>
      index === 1
        ? completion([
            {
              id: 'synthetic-candidate',
              name: 'learning.propose',
              arguments: {
                content: 'Synthetic admission recovery claim.',
                kind: 'claim',
                sourceMessageIds: [attempt.projected[0]!.id],
              },
            },
          ])
        : completion(),
    )
    h.engine.indexMemory = async () => {
      indexes++
      if (outcome === 'unknown-write-cancelled') await pending
      throw new ModelError('provider_http_503', 503)
    }
    try {
      for (const id of original) h.ingest(id)
      await advance(t)
      assert.equal(h.attempts.length, 1)
      assert.equal(indexes, 1)
      assert.equal(h.engine.memory.adminList().length, 1, 'the authoritative write already ran')
      if (outcome === 'unknown-write-cancelled') {
        h.engine.runtime.recall(scene.id, h.attempts[0]!.ids[0]!)
        assert.equal(h.attempts[0]!.signal?.aborted, true)
        h.engine.settingsChanged()
        assert.equal(h.engine.learningStatus().active, 60)
        assert.equal(h.engine.learningStatus().queued, 60)
        release()
        await advance(t, 0)
      } else {
        const failure = h.engine.learningStatus().failures[0]!
        assert.equal(failure.partial, true)
        assert.equal(failure.canRetry, false)
        assert.deepEqual(h.engine.retryLearning(failure.id), { ok: false })
      }
      h.internals.admission.cancelScene(scene.id)
      h.engine.settingsChanged()
      await drain(t, h)
      assert.equal(h.attempts.length, 2)
      assert.deepEqual(sorted(h.attempts[1]!.ids), sorted(original.slice(60)))
      assert.equal(h.engine.learningStatus().completed, 60)
      assert.equal(indexes, 1, 'partial or unknown writes must not be tried again')
      assert.ok(
        h.store.logs.some(
          (entry) =>
            entry.stage === 'learning.batch' &&
            entry.status === 'partial' &&
            entry.reason === 'partial_commit_no_replay',
        ),
      )
      assert.equal(
        new Set(h.attempts.flatMap((attempt) => attempt.ids)).size,
        120,
        '60 explicitly partial/unknown plus 60 completed accounts for every admitted source',
      )
      assertExactTickets(h)
      h.engine.settingsChanged()
      await advance(t, 2000)
      assert.equal(h.attempts.length, 2)
    } finally {
      release()
      await h.close()
    }
  })
