import assert from 'node:assert/strict'
import test from 'node:test'
import { Engine } from '../src/engine.ts'
import { Store } from '../src/store.ts'
import { ModelError, type Completion } from '../src/models.ts'
import type { RuntimeMessage } from '../src/runtime.ts'

type Internals = {
  enqueueLearning(message: RuntimeMessage): void
  learn(sceneId: string): Promise<void>
  learningBatches: Map<string, RuntimeMessage[]>
  learningRetries: Map<string, { messages: RuntimeMessage[]; attempt: number; due: number }>
  learningTasks: Map<string, AbortController>
}
const message = (id: string, sceneId = 'scene', senderId = 'person'): RuntimeMessage => ({
  id,
  sceneId,
  senderId,
  sentAt: new Date().toISOString(),
  receivedAt: new Date().toISOString(),
  origin: 'live',
  status: 'available',
  parts: [{ type: 'text', text: `Durable evidence ${id}` }],
})
async function fixture(
  run: (engine: Engine, store: Store, queue: Internals, seen: string[][]) => Promise<void>,
) {
  const store = new Store(':memory:')
  store.set('settings', {
    ...store.settings,
    learningBatchMs: 300000,
    learningRetryBaseMs: 100,
    learningRetryAttempts: 3,
  })
  for (const id of ['scene', 'other'])
    store.saveScene({
      id,
      kind: 'group',
      adapterId: 'fixture',
      accountId: 'bot',
      platformSceneId: id,
      name: id,
      participationEnabled: false,
      learningEnabled: true,
      proactiveEnabled: false,
    })
  const engine = new Engine(store),
    queue = engine as unknown as Internals,
    seen: string[][] = []
  const router = engine.router()
  router.complete = async (_role, prompts) => {
    const input = JSON.parse(prompts[1]!.content) as { messages: RuntimeMessage[] }
    seen.push(input.messages.map((message) => message.id))
    return { toolCalls: [] } as unknown as Completion
  }
  engine.router = () => router
  try {
    await run(engine, store, queue, seen)
  } finally {
    await engine.dispose()
    store.close()
  }
}
const retryNow = async (queue: Internals, sceneId = 'scene') => {
  queue.learningRetries.get(sceneId)!.due = 0
  await queue.learn(sceneId)
}

test('FIFO learning offers every eligible message across a 137-message mixed-author burst', async () => {
  await fixture(async (engine, _store, queue, seen) => {
    for (let i = 0; i < 137; i++) queue.enqueueLearning(message(`m${i}`, 'scene', `p${i % 7}`))
    queue.enqueueLearning(message('m0')) // in-flight admission deduplicates transports
    queue.enqueueLearning({ ...message('history'), origin: 'history' })
    queue.enqueueLearning({ ...message('simulation'), origin: 'simulation' })
    for (let i = 0; i < 3; i++) await queue.learn('scene')
    assert.deepEqual(
      seen.map((batch) => batch.length),
      [60, 60, 17],
    )
    assert.deepEqual(
      seen.flat(),
      Array.from({ length: 137 }, (_, i) => `m${i}`),
    )
    assert.equal(engine.learningStatus().completed, 137)
    assert.equal(engine.learningStatus().queued, 0)
  })
})

test('Queue bounds reject newest visibly without erasing early evidence; other scenes still get turns', async () => {
  await fixture(async (engine, store, queue, seen) => {
    store.set('settings', {
      ...store.settings,
      learningQueueMaxMessages: 120,
      learningQueueMaxTotalMessages: 121,
    })
    for (let i = 0; i < 140; i++) queue.enqueueLearning(message(`spam${i}`))
    queue.enqueueLearning(message('other-first', 'other', 'different-person'))
    queue.enqueueLearning(message('other-overflow', 'other'))
    await queue.learn('scene')
    await queue.learn('other')
    await queue.learn('scene')
    assert.deepEqual(
      seen.map((batch) => batch.length),
      [60, 1, 60],
    )
    assert.equal(seen[0]![0], 'spam0')
    assert.equal(seen[1]![0], 'other-first')
    assert.equal(engine.learningStatus().overflow, 21)
    assert.equal(store.logs.filter((row) => row.code === 'learning_queue_overflow').length, 21)
    assert.equal(JSON.stringify(store.logs).includes('Durable evidence'), false)
  })
})

test('Explicit transient failures recover with bounded backoff and preserve the full batch', async () => {
  await fixture(async (engine, store, queue) => {
    let attempts = 0
    engine.router().complete = async () => {
      attempts++
      if (attempts < 3) throw new ModelError('provider_http_503', 503)
      return { toolCalls: [] } as unknown as Completion
    }
    queue.enqueueLearning(message('source'))
    await queue.learn('scene')
    assert.equal(queue.learningRetries.get('scene')!.attempt, 2)
    assert.ok(queue.learningRetries.get('scene')!.due >= Date.now())
    await retryNow(queue)
    await retryNow(queue)
    assert.equal(attempts, 3)
    assert.equal(engine.learningStatus().completed, 1)
    assert.equal(engine.learningStatus().retrying, 0)
    assert.equal(store.logs.filter((row) => row.code === 'learning_retry_scheduled').length, 2)
  })
})

test('Exhaustion is observable; auth, malformed and uncertain outcomes are never automatically retried', async () => {
  for (const error of [
    new ModelError('provider_http_503', 503),
    new ModelError('provider_http_401', 401),
    new ModelError('provider_request_failed'),
    new ModelError('invalid_provider_response'),
  ]) {
    await fixture(async (engine, store, queue) => {
      let calls = 0
      engine.router().complete = async () => {
        calls++
        throw error
      }
      queue.enqueueLearning(message('source'))
      await queue.learn('scene')
      while (queue.learningRetries.size) await retryNow(queue)
      assert.equal(calls, error.retryable ? 3 : 1)
      assert.equal(engine.learningStatus().failed, 1)
      assert.equal(
        store.logs.at(-1)?.code,
        error.retryable ? 'learning_retry_exhausted' : 'learning_failed',
      )
    })
  }
})

test('Failure after a proposal has committed does not replay the model or duplicate candidates', async () => {
  await fixture(async (engine, _store, queue) => {
    let calls = 0
    engine.router().complete = async () => {
      calls++
      return {
        toolCalls: [
          {
            id: 'fact',
            name: 'learning.propose',
            arguments: {
              content: 'Independent durable fact',
              kind: 'fact',
              sourceMessageIds: ['source'],
            },
          },
        ],
      } as unknown as Completion
    }
    engine.indexMemory = async () => {
      throw new ModelError('provider_http_503', 503)
    }
    queue.enqueueLearning(message('source'))
    await queue.learn('scene')
    assert.equal(calls, 1)
    assert.equal(engine.memory.adminList().length, 1)
    assert.equal(queue.learningRetries.size, 0)
  })
})

test('Disabled learning and disposal discard pending retries and never create restart replay', async () => {
  for (const stop of ['disable', 'dispose'] as const)
    await fixture(async (engine, store, queue) => {
      engine.router().complete = async () => {
        throw new ModelError('provider_http_503', 503)
      }
      queue.enqueueLearning(message('source'))
      await queue.learn('scene')
      if (stop === 'disable') {
        const scene = { ...store.scene('scene')!, learningEnabled: false }
        store.saveScene(scene)
        engine.sceneChanged(scene)
      } else await engine.dispose()
      assert.equal(queue.learningRetries.size, 0)
      assert.equal(queue.learningBatches.size, 0)
      const next = new Engine(store)
      assert.equal(next.learningStatus().queued, 0)
      assert.equal(next.learningStatus().retrying, 0)
      await next.dispose()
    })
})

test('Paused failures retain bounded input for explicit recovery or discard, without hidden automatic replay', async () => {
  await fixture(async (engine, store, queue) => {
    engine.router().complete = async () => {
      throw new ModelError('provider_http_503', 503)
    }
    queue.enqueueLearning(message('recall-me'))
    queue.enqueueLearning(message('keep-me'))
    await queue.learn('scene')
    await retryNow(queue)
    await retryNow(queue)
    assert.equal(engine.learningStatus().paused, 2)
    const failure = engine.learningStatus().failures[0]!
    assert.equal(failure.canRetry, true)
    assert.equal(JSON.stringify(failure).includes('Durable evidence'), false)
    engine.runtime.recall('scene', 'recall-me')
    assert.equal(engine.learningStatus().paused, 1)
    const seen: string[] = []
    engine.router().complete = async (_role, prompts) => {
      seen.push(...JSON.parse(prompts[1]!.content).messages.map((m: RuntimeMessage) => m.id))
      return { toolCalls: [] } as unknown as Completion
    }
    assert.equal(engine.retryLearning(failure.id).ok, true)
    await queue.learn('scene')
    assert.deepEqual(seen, ['keep-me'])
    assert.equal(engine.learningStatus().paused, 0)
    assert.equal(engine.retryLearning(failure.id).ok, false)
    assert.equal(store.recalled('scene', 'recall-me'), true)
  })
})

test('Recall cancels active extraction and only unaffected sources return; pending retry respects tombstones', async () => {
  await fixture(async (engine, _store, queue, seen) => {
    const original = engine.router().complete
    engine.router().complete = async (_role, _prompts, _tools, signal) => {
      engine.runtime.recall('scene', 'recalled')
      assert.equal(signal?.aborted, true)
      throw new Error('cancelled')
    }
    queue.enqueueLearning(message('recalled'))
    queue.enqueueLearning(message('survivor'))
    await queue.learn('scene')
    assert.deepEqual(
      queue.learningBatches.get('scene')!.map((m) => m.id),
      ['survivor'],
    )
    engine.router().complete = original
    await queue.learn('scene')
    assert.deepEqual(seen, [['survivor']])
    engine.router().complete = async () => {
      throw new ModelError('provider_http_503', 503)
    }
    queue.enqueueLearning(message('retry-recalled'))
    await queue.learn('scene')
    engine.runtime.recall('scene', 'retry-recalled')
    await retryNow(queue)
    assert.equal(queue.learningRetries.size, 0)
    assert.equal(engine.learningStatus().paused, 0)
  })
})

test('A failing scene backoff does not block a different scene and does not consume newer input', async () => {
  await fixture(async (engine, _store, queue, seen) => {
    const original = engine.router().complete
    engine.router().complete = async (...args) => {
      const input = JSON.parse(args[1][1]!.content)
      if (input.messages[0].id === 'failure') throw new ModelError('provider_http_503', 503)
      return original(...args)
    }
    queue.enqueueLearning(message('failure'))
    await queue.learn('scene')
    queue.enqueueLearning(message('newer'))
    queue.enqueueLearning(message('unrelated', 'other'))
    await queue.learn('other')
    assert.deepEqual(seen, [['unrelated']])
    assert.deepEqual(
      queue.learningBatches.get('scene')!.map((m) => m.id),
      ['newer'],
    )
    assert.deepEqual(
      queue.learningRetries.get('scene')!.messages.map((m) => m.id),
      ['failure'],
    )
  })
})

test('Extraction never receives remembered private content; reconciliation cannot rewrite independent content or sources', async () => {
  await fixture(async (engine, _store, queue) => {
    const seed = engine.memory.propose(
      {
        sceneId: 'scene',
        simulation: false,
        provenance: 'source-only',
        dependencyMemoryIds: [],
        sourceEvidence: [
          { sceneId: 'scene', messageId: 'old', senderId: 'person', available: true, isBot: false },
        ],
      },
      {
        content: 'Private old credential sentinel',
        kind: 'fact',
        confidence: 0.6,
        importance: 'medium',
        sources: [{ sceneId: 'scene', messageId: 'old', senderId: 'person' }],
      },
    )
    assert.equal(seed.status, 'saved')
    if (seed.status !== 'saved') return
    engine.searchMemory = async () => engine.memory.list({ sceneId: 'scene', simulation: false })
    let calls = 0
    engine.router().complete = async (_role, prompts, tools) => {
      calls++
      if (tools?.some((tool) => tool.name === 'learning.reconcile')) {
        assert.ok(prompts[1]!.content.includes('Private old credential sentinel'))
        return {
          toolCalls: [
            {
              id: 'rewrite',
              name: 'learning.reconcile',
              arguments: {
                candidateIndex: 0,
                targetId: JSON.parse(prompts[1]!.content).relatedMemories[0].id,
                action: 'merge',
                content: 'Private old credential sentinel',
                sourceMessageIds: ['source'],
              },
            },
          ],
        } as unknown as Completion
      }
      assert.equal(prompts[1]!.content.includes('Private old credential sentinel'), false)
      assert.equal('relatedMemories' in JSON.parse(prompts[1]!.content), false)
      return {
        toolCalls: [
          {
            id: 'fact',
            name: 'learning.propose',
            arguments: {
              content: 'Fresh independent joke convention',
              kind: 'fact',
              sourceMessageIds: ['source'],
              mergeIntoId: seed.memory.id,
            },
          },
        ],
      } as unknown as Completion
    }
    queue.enqueueLearning(message('source'))
    await queue.learn('scene')
    assert.equal(calls, 2)
    const fresh = engine.memory
      .adminList()
      .find((record) => record.content === 'Fresh independent joke convention')!
    assert.ok(fresh)
    assert.deepEqual(
      fresh.sources.map((source) => source.messageId),
      ['source'],
    )
    engine.runtime.recall('scene', 'old')
    assert.ok(
      engine.memory
        .list({ sceneId: 'scene', simulation: false })
        .some((record) => record.id === fresh.id),
    )
    assert.ok(
      !engine.memory
        .list({ sceneId: 'scene', simulation: false })
        .some((record) => record.id === seed.memory.id),
    )
  })
})
