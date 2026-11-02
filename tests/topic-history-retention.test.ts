import assert from 'node:assert/strict'
import test from 'node:test'
import { mkdtempSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import Database from 'better-sqlite3'
import { metadata, revoke } from '../src/memory/recovery/ledger.ts'
import { Store } from '../src/store.ts'
import { Runtime, type RuntimeMessage } from '../src/runtime.ts'
import type { Router, Completion } from '../src/models.ts'
import { TopicRoutingHistory } from '../src/runtime/topic-history.ts'
import { TurnWaitCoordinator } from '../src/runtime/turn-wait.ts'
const scene = {
  id: 'history-fixture',
  kind: 'group' as const,
  adapterId: 'fixture',
  accountId: 'bot',
  platformSceneId: 'group',
}
const silent = (): Completion => ({
  text: '',
  modelId: 'fixture',
  toolCalls: [
    {
      id: 'decision',
      name: 'participation.decide',
      arguments: { action: 'silent', reasonCode: 'fixture' },
    },
  ],
  assistant: { role: 'assistant', content: '' },
  usage: { inputTokens: 0, outputTokens: 0, totalTokens: 0 },
})
function fixture(admission = false, max = 500, path = ':memory:') {
  const store = new Store(path)
  store.set('settings', {
    ...store.settings,
    topicAdmissionEnabled: admission,
    topicHistoryMaxMessages: max,
    cacheHours: 0.1,
  })
  store.saveScene({
    ...scene,
    name: 'synthetic',
    participationEnabled: true,
    learningEnabled: false,
    proactiveEnabled: false,
  })
  const runtime = new Runtime(
    store,
    () => ({ complete: async () => silent() }) as unknown as Router,
  )
  const message = (id: string): RuntimeMessage => ({
    id,
    sceneId: scene.id,
    senderId: 'person',
    sentAt: new Date().toISOString(),
    receivedAt: new Date().toISOString(),
    origin: 'live',
    status: 'available',
    parts: [{ type: 'text', text: 'synthetic' }],
  })
  const ingest = (id: string) => runtime.ingest(message(id), scene, false)
  return {
    store,
    runtime,
    message,
    ingest,
    state: () => (runtime as any).state(scene.id),
    close() {
      runtime.dispose()
      store.close()
    },
  }
}
const membership = (state: any, id: string) =>
  [...state.topics.values()].some((topic: any) => topic.messageIds.includes(id))

test('routing index prefers retained bodies, expires old IDs and protects only exact live references', () => {
  const history = new TopicRoutingHistory()
  for (let i = 0; i < 8; i++) history.observe(String(i), new Date(i * 1000).toISOString())
  assert.equal(
    history.prune({
      cutoff: 2000,
      maxUnprotected: 3,
      preferred: new Set(['3', '4']),
      protected: new Set(['0']),
    }),
    4,
  )
  assert.equal(history.size, 4)
  assert(history.has('0'))
  assert(history.has('3'))
  assert(history.has('4'))
  assert(history.has('7'))
  history.prune({
    cutoff: 9000,
    maxUnprotected: 3,
    preferred: new Set(),
    protected: new Set(),
  })
  assert.equal(history.size, 0)
})
for (const admission of [false, true])
  test(`actual busy ingress bounds historical topic IDs, admission=${admission}`, () => {
    const f = fixture(admission)
    try {
      for (let i = 0; i < 650; i++) f.ingest(String(i))
      assert.equal(f.state().messages.length, 500)
      assert.equal(f.state().topicHistory.size, 500)
      assert.equal(membership(f.state(), '0'), false)
      assert.equal(membership(f.state(), '649'), true)
      assert([...f.state().topics.values()].every((topic: any) => topic.messageIds.length <= 500))
      const trimmed = f.store.logs.filter((row) => row.code === 'topic_history_trimmed')
      assert(trimmed.length <= 1)
      f.runtime.ingest({ ...f.message('reply-to-expired'), replyToId: '0' }, scene, false)
      if (!admission)
        assert(f.store.logs.some((row) => row.code === 'topic_history_reply_unavailable'))
    } finally {
      f.close()
    }
  })
test('saved cap shrink trims idle real scenes immediately; cache OFF clears unprotected history', () => {
  const f = fixture(false, 700)
  try {
    for (let i = 0; i < 650; i++) f.ingest(String(i))
    assert.equal(f.state().topicHistory.size, 650)
    f.store.set('settings', {
      ...f.store.settings,
      topicHistoryMaxMessages: 500,
    })
    f.runtime.configureAdmission()
    assert.equal(f.state().topicHistory.size, 500)
    f.store.set('settings', { ...f.store.settings, cacheEnabled: false })
    f.runtime.configureAdmission()
    assert.equal(f.state().topicHistory.size, 0)
    assert.equal(f.state().messages.length, 0)
    f.ingest('cache-off-idle')
    assert.equal(f.state().topicHistory.size, 0)
  } finally {
    f.close()
  }
})
test('pending/active/WAIT IDs survive TTL and release after exact lifecycle completion', () => {
  const f = fixture()
  let now = 0
  const wait = new TurnWaitCoordinator(
    {
      quietMs: 0,
      maxWaitMs: 10,
      addressedQuietMs: 0,
      maxModelWaitMs: 100,
      maxPending: 60,
    },
    () => now,
  )
  try {
    f.ingest('pinned')
    const state = f.state()
    const topic = [...state.topics.keys()][0]
    wait.observe({ id: 'pinned', meaningful: true })
    state.admissions.set(topic, wait)
    state.messages = []
    ;(f.runtime as any).pruneTopicHistory(state, Infinity, 500)
    assert(membership(state, 'pinned'))
    const wake = wait.takeReady()!
    assert(wake)
    assert.deepEqual(wait.retainedEventIds(), ['pinned'])
    wait.finish(wake, { waitUntil: 50 })
    assert.deepEqual(wait.retainedEventIds(), ['pinned'])
    ;(f.runtime as any).pruneTopicHistory(state, Infinity, 500)
    assert(membership(state, 'pinned'))
    now = 51
    assert.equal(wait.takeReady()?.kind, 'recheck')
    assert.deepEqual(wait.retainedEventIds(), [])
    ;(f.runtime as any).pruneTopicHistory(state, Infinity, 500)
    assert.equal(membership(state, 'pinned'), false)
    assert.equal(state.topicHistory.size, 0)
  } finally {
    f.close()
  }
})
test('uncertain send protection does not retain every historical source ID or grant another send', () => {
  const f = fixture(true)
  try {
    f.ingest('old')
    const state = f.state()
    const topic = [...state.topics.keys()][0]
    state.routing.protectedSends.add(topic)
    for (let i = 0; i < 550; i++) f.ingest('new' + i)
    assert.equal(membership(state, 'old'), false)
    assert.equal(state.routing.protectedSends.has(topic), true)
    assert.equal(state.topics.has(topic), true)
    assert.equal(f.runtime.activeTasks, 0)
  } finally {
    f.close()
  }
})
test(
  'source snapshots stay pinned during history await before dependencies are populated, then cleanup reclaims them',
  { timeout: 15000 },
  async (t) => {
    t.mock.timers.enable({ apis: ['Date'], now: new Date('2026-11-02T00:00:00Z') })
    const f = fixture(false, 700)
    let release!: (value: any) => void, started!: () => void
    const waiting = new Promise<void>((resolve) => {
      started = resolve
    })
    f.runtime.addAdapter('fixture', {
      send: async () => {
        throw new Error('unexpected_send')
      },
      getMessage: async () => {
        started()
        return new Promise((resolve) => {
          release = resolve
        })
      },
      history: async () => ({ items: [], coverage: 'complete' }),
      dispose() {},
    })
    try {
      f.runtime.ingest({ ...f.message('source'), replyToId: 'missing-quoted-source' }, scene, false)
      f.store.saveScene({ ...f.store.scene(scene.id)!, proactiveEnabled: true })
      const running = f.runtime.proactive(scene.id)
      await waiting
      const state = f.state()
      const task = [...state.tasks.values()][0] as any
      assert.equal(task.dependencies.has('source'), false)
      assert.equal(task.context.sourceSnapshots[0].id, 'source')
      for (let i = 0; i < 550; i++) f.ingest('during-await' + i)
      f.store.set('settings', {
        ...f.store.settings,
        topicHistoryMaxMessages: 500,
      })
      f.runtime.configureAdmission()
      assert(membership(state, 'source'))
      assert.equal(state.topicHistory.size, 501)
      // Force TTL expiry while the exact source snapshot is still live.
      t.mock.timers.tick(360001)
      f.runtime.configureAdmission()
      assert(membership(state, 'source'))
      assert.equal(state.topicHistory.size, 1)
      release({ items: [], coverage: 'complete' })
      await running
      assert.equal(f.runtime.activeTasks, 0)
      assert.equal(membership(state, 'source'), false)
      assert.equal(state.topicHistory.size, 0)
    } finally {
      release?.({ items: [], coverage: 'complete' })
      f.close()
    }
  },
)

for (const invalidation of ['recall', 'corruption'] as const)
  test(
    `source ${invalidation} during history await terminates without send or cleanup masking`,
    { timeout: 10000 },
    async () => {
      const root = mkdtempSync(join(tmpdir(), 'topic-retention-authority-'))
      const f = fixture(
        false,
        500,
        invalidation === 'corruption' ? join(root, 'data', 'state.sqlite') : ':memory:',
      )
      let release!: (value: any) => void, started!: () => void
      let sends = 0
      const waiting = new Promise<void>((resolve) => {
        started = resolve
      })
      f.runtime.addAdapter('fixture', {
        send: async () => {
          sends++
          return { status: 'sent', messageId: 'unexpected' }
        },
        getMessage: async () => {
          started()
          return new Promise((resolve) => {
            release = resolve
          })
        },
        history: async () => ({ items: [], coverage: 'complete' }),
        dispose() {},
      })
      try {
        f.store.saveScene({ ...f.store.scene(scene.id)!, proactiveEnabled: true })
        if (invalidation === 'corruption')
          f.store.db.transaction(() =>
            revoke(f.store.db, { kind: 'source', sceneId: 'other', messageId: 'synthetic' }),
          )()
        f.runtime.ingest(
          { ...f.message('source'), replyToId: 'missing-quoted-source' },
          scene,
          false,
        )
        const running = f.runtime.proactive(scene.id)
        await waiting
        if (invalidation === 'recall') f.runtime.recall(scene.id, 'source')
        else {
          const authority = new Database(join(metadata(f.store.db)!.authority, 'authority.sqlite'))
          try {
            authority.prepare('UPDATE revocations SET hash=? WHERE sequence=1').run('0'.repeat(64))
          } finally {
            authority.close()
          }
        }
        release({ items: [], coverage: 'complete' })
        await running
        assert.equal(sends, 0)
        assert.equal(f.runtime.activeTasks, 0)
        assert(
          f.store.logs.some((row) => row.stage === 'turn_cleanup' && row.status === 'completed'),
        )
        assert.equal(
          f.store.logs.some((row) => row.stage === 'turn_cleanup' && row.status === 'failed'),
          false,
        )
        if (invalidation === 'recall') assert.equal(f.store.recalled(scene.id, 'source'), true)
        else assert.throws(() => f.store.scene(scene.id), /recovery_chain_invalid/)
      } finally {
        release?.({ items: [], coverage: 'complete' })
        f.close()
        rmSync(root, { recursive: true, force: true })
      }
    },
  )

test('cancelled task cleanup does not read routing settings after runtime disposal and store close', async () => {
  const f = fixture(false)
  let release!: (value: any) => void, started!: () => void
  const waiting = new Promise<void>((resolve) => {
    started = resolve
  })
  let sends = 0
  f.runtime.addAdapter('fixture', {
    send: async () => {
      sends++
      return { status: 'sent', messageId: 'unexpected' }
    },
    getMessage: async () => {
      started()
      return new Promise((resolve) => {
        release = resolve
      })
    },
    history: async () => ({ items: [], coverage: 'complete' }),
    dispose() {},
  })
  let closed = false
  try {
    f.store.saveScene({ ...f.store.scene(scene.id)!, proactiveEnabled: true })
    f.runtime.ingest({ ...f.message('source'), replyToId: 'missing-quoted-source' }, scene, false)
    const running = f.runtime.proactive(scene.id)
    await waiting
    const state = f.state()
    assert.equal(state.tasks.size, 1)
    f.close()
    closed = true
    release({ items: [], coverage: 'complete' })
    await running
    assert.equal(sends, 0)
    assert.equal(state.tasks.size, 0)
    assert.equal(state.pendingTurns.size, 0)
    assert.equal(state.timers.size, 0)
    assert.equal((f.runtime as any).states.size, 0)
    assert.equal((f.runtime as any).sendGuards.size, 0)
  } finally {
    release?.({ items: [], coverage: 'complete' })
    if (!closed) f.close()
  }
})
