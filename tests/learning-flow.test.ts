import assert from 'node:assert/strict'
import test from 'node:test'
import { Engine } from '../src/engine.ts'
import { Store } from '../src/store.ts'
import { ModelError, type Completion, type Router } from '../src/models.ts'
import type { RuntimeMessage } from '../src/runtime.ts'
import { withFlowContext } from '../src/diagnostics/flow.ts'

type Queue = {
  enqueueLearning(message: RuntimeMessage): void
  learn(scene: string): Promise<void>
  learningTasks: Map<string, AbortController>
  learningRetries: Map<string, { due: number }>
}
const source = (scene: string): RuntimeMessage => ({
  id: 'input',
  sceneId: scene,
  senderId: 'person',
  origin: 'live',
  status: 'available',
  sentAt: new Date().toISOString(),
  receivedAt: new Date().toISOString(),
  parts: [{ type: 'text', text: 'private-source-canary hello' }],
})
const completion = (save = false): Completion => ({
  text: '',
  toolCalls: save
    ? [
        {
          id: 'proposal',
          name: 'learning.propose',
          arguments: {
            content: 'private-memory-canary durable fact',
            kind: 'fact',
            sourceMessageIds: ['input'],
          },
        },
      ]
    : [],
  usage: { inputTokens: 5, outputTokens: 3, totalTokens: 8 },
  modelId: 'fixture',
  assistant: { role: 'assistant', content: '' },
})
async function fixture(run: (engine: Engine, store: Store, queue: Queue) => Promise<void>) {
  const store = new Store(':memory:')
  store.set('settings', { ...store.settings, learningBatchMs: 300000, learningRetryBaseMs: 100 })
  for (const id of ['one', 'two'])
    store.saveScene({
      id,
      name: 'Fixture',
      kind: 'group',
      adapterId: 'fixture',
      accountId: 'bot',
      platformSceneId: id,
      participationEnabled: false,
      learningEnabled: true,
      proactiveEnabled: false,
    })
  const engine = new Engine(store)
  try {
    await run(engine, store, engine.learning as unknown as Queue)
  } finally {
    await engine.dispose()
    store.close()
  }
}
function paired(store: Store) {
  const rows = store.logs.filter((row) => row.stage)
  for (const row of rows.filter((row) => row.status === 'started')) {
    assert.equal(
      rows.filter(
        (other) =>
          other.spanId === row.spanId &&
          other.status !== 'started' &&
          other.status !== 'cancel_requested',
      ).length,
      1,
      `span ${row.stage} must terminate exactly once`,
    )
  }
  assert.doesNotMatch(
    JSON.stringify(rows),
    /private-source-canary|private-memory-canary|private-error-canary/,
  )
}

test('concurrent learning batches have independent traces linked to admission and truthful persistence counts', async () =>
  fixture(async (engine, store, queue) => {
    engine.router = () =>
      ({
        complete: async () => {
          await Promise.resolve()
          return completion(true)
        },
      }) as unknown as Router
    for (const scene of ['one', 'two'])
      withFlowContext(
        { traceId: `input-trace-${scene}`, sceneId: scene, turnId: `unrelated-turn-${scene}` },
        () => queue.enqueueLearning(source(scene)),
        store,
      )
    await Promise.all([queue.learn('one'), queue.learn('two')])
    const batches = store.logs.filter(
      (row) => row.stage === 'learning.batch' && row.status === 'completed',
    )
    assert.equal(batches.length, 2)
    assert.notEqual(batches[0]!.traceId, batches[1]!.traceId)
    for (const row of batches) {
      assert.equal(row.batchId, row.traceId)
      assert.equal(row.turnId, undefined)
      assert.equal(row.parentTraceId, `input-trace-${row.sceneId}`)
      assert.deepEqual(row.linkedTraceIds, [`input-trace-${row.sceneId}`])
      assert.equal(row.counts?.savedCount, 1)
      assert.equal(row.counts?.candidateCount, 1)
    }
    assert.equal(engine.memory.adminList().length, 2)
    assert.ok(
      store.logs.some(
        (row) =>
          row.stage === 'memory.search' &&
          row.action === 'lexical' &&
          row.reason === 'no_active_vector_index',
      ),
    )
    paired(store)
  }))

test('empty extraction explains skipped optional work, and transient retry retains batch identity', async () =>
  fixture(async (engine, store, queue) => {
    let attempt = 0
    engine.router = () =>
      ({
        complete: async () => {
          if (++attempt === 1) throw new ModelError('provider_http_503', 503)
          return completion()
        },
      }) as unknown as Router
    queue.enqueueLearning(source('one'))
    await queue.learn('one')
    queue.learningRetries.get('one')!.due = 0
    await queue.learn('one')
    const terminal = store.logs.filter(
      (row) =>
        row.stage === 'learning.batch' &&
        row.status !== 'started' &&
        row.status !== 'cancel_requested',
    )
    assert.deepEqual(
      terminal.map((row) => row.status),
      ['failed', 'completed'],
    )
    assert.equal(terminal[0]!.traceId, terminal[1]!.traceId)
    assert.equal(terminal[0]!.nextStep, 'retry_scheduled')
    assert.equal(terminal[1]!.reason, 'no_learning_candidates')
    assert.ok(
      store.logs.some(
        (row) =>
          row.stage === 'learning.review' &&
          row.status === 'skipped' &&
          row.reason === 'no_durable_review_signal',
      ),
    )
    assert.ok(
      store.logs.some((row) => row.stage === 'learning.reconcile' && row.status === 'skipped'),
    )
    paired(store)
  }))

test('cancelled learning terminates spans without a completed batch; uncertain write failure is partial and not replayed', async () => {
  await fixture(async (engine, store, queue) => {
    let entered!: () => void
    const started = new Promise<void>((resolve) => {
      entered = resolve
    })
    engine.router = () =>
      ({
        complete: async (
          _role: unknown,
          _messages: unknown,
          _tools: unknown,
          signal: AbortSignal,
        ) => {
          entered()
          return new Promise((_resolve, reject) =>
            signal.addEventListener('abort', () => reject(new Error('private-error-canary')), {
              once: true,
            }),
          )
        },
      }) as unknown as Router
    queue.enqueueLearning(source('one'))
    const pending = queue.learn('one')
    await started
    queue.learningTasks.get('one')!.abort('source_recalled')
    await pending
    assert.ok(
      store.logs.some((row) => row.stage === 'learning.batch' && row.status === 'cancelled'),
    )
    assert.equal(
      store.logs.some((row) => row.stage === 'learning.batch' && row.status === 'completed'),
      false,
    )
    paired(store)
  })
  await fixture(async (engine, store, queue) => {
    engine.router = () => ({ complete: async () => completion(true) }) as unknown as Router
    engine.indexMemory = async () => {
      throw new Error('private-error-canary')
    }
    queue.enqueueLearning(source('one'))
    await queue.learn('one')
    assert.equal(engine.memory.adminList().length, 1)
    assert.equal(queue.learningRetries.size, 0)
    const partial = store.logs.find(
      (row) => row.stage === 'learning.batch' && row.status === 'partial',
    )
    assert.equal(partial?.reason, 'partial_commit_no_replay')
    assert.equal(partial?.counts?.unconfirmedCandidateCount, 1)
    assert.equal(partial?.nextStep, 'inspect_partial_learning')
    paired(store)
  })
})

test('cancellation after a committed candidate reports partial and never replays the batch', async () => {
  for (const throwAfterWrite of [false, true]) {
    await fixture(async (engine, store, queue) => {
      engine.router = () => ({ complete: async () => completion(true) }) as unknown as Router
      engine.indexMemory = async () => {
        // The memory transaction already committed before indexing starts.
        assert.equal(engine.memory.adminList().length, 1)
        queue.learningTasks.get('one')!.abort('source_recalled')
        if (throwAfterWrite) throw new ModelError('request_cancelled')
      }
      queue.enqueueLearning(source('one'))
      await queue.learn('one')
      const terminal = store.logs.filter(
        (row) =>
          row.stage === 'learning.batch' &&
          row.status !== 'started' &&
          row.status !== 'cancel_requested',
      )
      assert.equal(terminal.length, 1)
      assert.equal(terminal[0]!.status, 'partial')
      assert.equal(terminal[0]!.reason, 'partial_commit_no_replay')
      assert.equal(terminal[0]!.nextStep, 'inspect_partial_learning')
      assert.equal(terminal[0]!.counts?.savedCount, throwAfterWrite ? 0 : 1)
      assert.equal(terminal[0]!.counts?.unconfirmedCandidateCount, throwAfterWrite ? 1 : 0)
      const persistence = store.logs.filter(
        (row) =>
          row.stage === 'learning.persist' &&
          row.status !== 'started' &&
          row.status !== 'cancel_requested',
      )
      assert.equal(persistence.length, 1)
      assert.equal(persistence[0]!.status, 'partial')
      assert.equal(persistence[0]!.reason, 'partial_commit_no_replay')
      assert.equal(persistence[0]!.counts?.savedCount, throwAfterWrite ? 0 : 1)
      assert.equal(persistence[0]!.counts?.unconfirmedCandidateCount, throwAfterWrite ? 1 : 0)
      assert.equal(queue.learningRetries.size, 0)
      await queue.learn('one')
      assert.equal(engine.memory.adminList().length, 1)
      assert.equal(
        store.logs.filter((row) => row.stage === 'learning.batch' && row.status === 'started')
          .length,
        1,
      )
      paired(store)
    })
  }
})
