import assert from 'node:assert/strict'
import test from 'node:test'
import { Engine } from '../src/engine.ts'
import { Store } from '../src/store.ts'
import type { Completion } from '../src/models.ts'
import type { RuntimeMessage } from '../src/runtime.ts'

type LearningInternals = {
  learningBatches: Map<string, RuntimeMessage[]>
  learningTasks: Map<string, AbortController>
  learn(sceneId: string): Promise<void>
}
const memory = (content: string) => ({
  name: 'learning.propose',
  arguments: { content, kind: 'fact', sourceMessageIds: ['source'] },
})
const habit = () => ({
  name: 'habits.propose',
  arguments: {
    kind: 'botExpression',
    preference: 'concise',
    direction: 1,
    sourceMessageIds: ['source'],
  },
})
const invalid = (name: string) => ({
  name,
  arguments: { content: 'private-invalid-sentinel', sourceMessageIds: [] },
})
async function fixture(
  proposals: Array<{ name: string; arguments: unknown }>,
  verify: (engine: Engine, store: Store, run: () => Promise<void>) => Promise<void>,
) {
  const store = new Store(':memory:'),
    engine = new Engine(store)
  store.saveScene({
    id: 'scene',
    kind: 'group',
    adapterId: 'fixture',
    accountId: 'bot',
    platformSceneId: 'group',
    name: 'Fixture',
    participationEnabled: false,
    learningEnabled: true,
    proactiveEnabled: false,
  })
  const internals = engine as unknown as LearningInternals
  const router = engine.router()
  router.complete = async () =>
    ({ toolCalls: proposals.map((call, index) => ({ ...call, id: String(index) })) }) as Completion
  engine.router = () => router
  const run = async () => {
    internals.learningBatches.set('scene', [
      {
        id: 'source',
        sceneId: 'scene',
        senderId: 'person',
        sentAt: new Date().toISOString(),
        receivedAt: new Date().toISOString(),
        origin: 'live',
        status: 'available',
        parts: [{ type: 'text', text: 'Bot, from now on keep your replies concise.' }],
      },
    ])
    await internals.learn('scene')
    assert.equal(internals.learningTasks.size, 0)
  }
  try {
    await verify(engine, store, run)
  } finally {
    await engine.dispose()
    store.close()
  }
}

test('Background learning isolates malformed siblings across memory and habit proposals', async () => {
  for (const reverse of [false, true]) {
    const proposals = [
      invalid('habits.propose'),
      invalid('learning.propose'),
      habit(),
      memory('Valid memory'),
    ]
    await fixture(reverse ? proposals.reverse() : proposals, async (engine, store, run) => {
      await run()
      assert.deepEqual(
        engine.memory.adminList().map((row) => row.content),
        ['Valid memory'],
      )
      assert.equal(engine.habits.list({ sceneId: 'scene', simulation: false }).length, 1)
      assert.equal(store.logs.filter((row) => row.code === 'learning_candidate_invalid').length, 2)
      assert.equal(
        store.logs.some((row) => row.code === 'learning_failed'),
        false,
      )
      assert.equal(JSON.stringify(store.logs).includes('private-invalid-sentinel'), false)
    })
  }
})

test('Malformed memory dates and whitespace do not abort later useful candidates', async () => {
  await fixture(
    [
      { ...memory('   '), arguments: { ...memory('   ').arguments } },
      {
        ...memory('Invalid event'),
        arguments: { ...memory('Invalid event').arguments, occurredAt: 'not-a-date' },
      },
      {
        ...memory('Invalid expiry'),
        arguments: { ...memory('Invalid expiry').arguments, validUntil: 'not-a-date' },
      },
      {
        ...memory('Valid date'),
        arguments: { ...memory('Valid date').arguments, occurredAt: '2026-10-25T12:00:00Z' },
      },
    ],
    async (engine, store, run) => {
      await run()
      assert.deepEqual(
        engine.memory.adminList().map((row) => row.content),
        ['Valid date'],
      )
      assert.equal(store.logs.filter((row) => row.code === 'learning_candidate_invalid').length, 3)
      assert.equal(
        store.logs.some((row) => row.code === 'learning_failed'),
        false,
      )
    },
  )
})

test('Candidate isolation keeps the eight-call bound and source guards', async () => {
  await fixture(
    [
      invalid('learning.propose'),
      {
        ...memory('Forged source'),
        arguments: { content: 'Forged source', kind: 'fact', sourceMessageIds: ['missing'] },
      },
      ...Array.from({ length: 6 }, (_, index) => memory(`Bounded ${index}`)),
      memory('Outside limit'),
    ],
    async (engine, store, run) => {
      await run()
      assert.equal(engine.memory.adminList().length, 6)
      assert.equal(
        engine.memory
          .adminList()
          .some((row) => ['Forged source', 'Outside limit'].includes(row.content)),
        false,
      )
      assert.equal(store.logs.filter((row) => row.code === 'learning_candidate_invalid').length, 2)
    },
  )
})

test('Candidate validation does not swallow unexpected storage failures', async () => {
  await fixture(
    [memory('First committed'), memory('Storage failure'), memory('Must not run')],
    async (engine, store, run) => {
      const original = engine.memory.propose.bind(engine.memory)
      let attempts = 0
      engine.memory.propose = (context, candidate) => {
        attempts++
        if (attempts === 2) throw new Error('private-database-failure-sentinel')
        return original(context, candidate)
      }
      await run()
      assert.equal(attempts, 2)
      assert.deepEqual(
        engine.memory.adminList().map((row) => row.content),
        ['First committed'],
      )
      assert.equal(store.logs.filter((row) => row.code === 'learning_failed').length, 1)
      assert.equal(
        store.logs.some((row) => row.code === 'learning_candidate_invalid'),
        false,
      )
      assert.equal(JSON.stringify(store.logs).includes('private-database-failure-sentinel'), false)
    },
  )
})

test('Candidate siblings stop after cancellation, disabled learning or disposal during indexing', async () => {
  for (const boundary of ['abort', 'disable', 'dispose'] as const) {
    await fixture(
      [memory('Saved before boundary'), memory('Must not run')],
      async (engine, store, run) => {
        engine.indexMemory = async () => {
          if (boundary === 'abort')
            (engine as unknown as LearningInternals).learningTasks.get('scene')!.abort()
          else if (boundary === 'disable')
            store.saveScene({ ...store.scene('scene')!, learningEnabled: false })
          else await engine.dispose()
        }
        await run()
        assert.deepEqual(
          engine.memory.adminList().map((row) => row.content),
          ['Saved before boundary'],
        )
        assert.equal(
          store.logs.some((row) => row.code === 'learning_candidate_invalid'),
          false,
        )
      },
    )
  }
})
