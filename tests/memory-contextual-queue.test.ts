import assert from 'node:assert/strict'
import test from 'node:test'
import {
  ContextualLearningQueue,
  type ContextualLearningJob,
  type ContextualLearningEvent,
} from '../src/memory/learning/contextual-queue.ts'

function deferred() {
  let resolve!: () => void
  const promise = new Promise<void>((done) => {
    resolve = done
  })
  return { promise, resolve }
}
function job(overrides: Partial<ContextualLearningJob> = {}): ContextualLearningJob {
  return {
    sceneId: 'scene-a',
    key: 'candidate-a',
    sourceIds: ['source-a'],
    validate: () => true,
    run: async () => undefined,
    ...overrides,
  }
}
const tick = () => new Promise<void>((resolve) => queueMicrotask(resolve))

test('enqueue returns queued before work settles and uses a fresh signal independent of normal turn completion', async () => {
  const gate = deferred()
  const turn = new AbortController()
  let signal: AbortSignal | undefined
  const queue = new ContextualLearningQueue()
  assert.equal(
    queue.enqueue(
      job({
        run: async (current) => {
          signal = current
          await gate.promise
        },
      }),
    ).status,
    'queued',
  )
  turn.abort('turn_finished')
  await tick()
  assert.ok(signal)
  assert.equal(signal.aborted, false)
  assert.equal(queue.stats().completed, 0)
  gate.resolve()
  await queue.whenIdle()
  assert.equal(queue.stats().completed, 1)
  queue.dispose()
})

test('single concurrency and canonical provenance dedup do not conflate other candidates or scenes', async () => {
  const gate = deferred()
  const starts: string[] = []
  const queue = new ContextualLearningQueue({ maxQueued: 3 })
  const first = job({
    sourceIds: ['b', 'a', 'a'],
    run: async () => {
      starts.push('first')
      await gate.promise
    },
  })
  const submitted = queue.enqueue(first)
  assert.equal(submitted.status, 'queued')
  assert.equal(queue.enqueue({ ...first, sourceIds: ['a', 'b'] }).status, 'duplicate')
  queue.enqueue(
    job({
      key: 'second',
      run: async () => {
        starts.push('second')
      },
    }),
  )
  queue.enqueue(
    job({
      sceneId: 'scene-b',
      run: async () => {
        starts.push('other-scene')
      },
    }),
  )
  assert.deepEqual(queue.enqueue(job({ key: 'capacity' })), {
    status: 'rejected',
    reason: 'capacity',
  })
  await tick()
  assert.deepEqual(starts, ['first'])
  gate.resolve()
  await queue.whenIdle()
  assert.deepEqual(starts, ['first', 'second', 'other-scene'])
  assert.equal(queue.enqueue(first).status, 'duplicate')
  queue.dispose()
})

test('foreground busy defers without losing work, explicit resume and bounded polling both release work', async () => {
  let busy = true
  let calls = 0
  const queue = new ContextualLearningQueue({ foregroundBusy: () => busy, pollMs: 5 })
  queue.enqueue(
    job({
      run: async () => {
        calls++
      },
    }),
  )
  await tick()
  assert.equal(calls, 0)
  assert.equal(queue.stats().queued, 1)
  busy = false
  queue.resume()
  await queue.whenIdle()
  assert.equal(calls, 1)
  busy = true
  queue.enqueue(
    job({
      key: 'poll',
      run: async () => {
        calls++
      },
    }),
  )
  await tick()
  busy = false
  await queue.whenIdle()
  assert.equal(calls, 2)
  queue.dispose()
})

test('authority is checked at submission, before dispatch and after completion, not treated as rollback', async () => {
  let valid = true
  let calls = 0
  const queue = new ContextualLearningQueue({ foregroundBusy: () => true })
  assert.deepEqual(queue.enqueue(job({ validate: () => false })), {
    status: 'rejected',
    reason: 'invalid',
  })
  queue.enqueue(
    job({
      validate: () => valid,
      run: async () => {
        calls++
      },
    }),
  )
  valid = false
  queue.cancelScene('scene-a')
  assert.equal(calls, 0)
  queue.dispose()
  const second = new ContextualLearningQueue()
  valid = true
  second.enqueue(
    job({
      validate: () => valid,
      run: async () => {
        calls++
      },
    }),
  )
  valid = false
  await second.whenIdle()
  assert.equal(calls, 0)
  assert.equal(second.stats().discarded, 1)
  valid = true
  second.enqueue(
    job({
      key: 'post',
      validate: () => valid,
      run: async () => {
        calls++
        valid = false
      },
    }),
  )
  await second.whenIdle()
  assert.equal(calls, 1)
  assert.equal(second.stats().discarded, 2)
  second.dispose()
})

test('recall aborts active work and removes only matching scene/source queued work without replay', async () => {
  const started = deferred()
  const queue = new ContextualLearningQueue()
  let signal: AbortSignal | undefined
  let survivor = 0
  const active = job({
    run: async (current) => {
      signal = current
      started.resolve()
      await new Promise<void>((resolve) =>
        current.addEventListener('abort', () => resolve(), { once: true }),
      )
    },
  })
  queue.enqueue(active)
  queue.enqueue(job({ key: 'same-source' }))
  queue.enqueue(
    job({
      sceneId: 'other',
      run: async () => {
        survivor++
      },
    }),
  )
  await started.promise
  queue.recall('scene-a', 'source-a')
  assert.equal(signal?.aborted, true)
  await queue.whenIdle()
  assert.equal(queue.stats().cancelled, 2)
  assert.equal(survivor, 1)
  assert.equal(queue.enqueue(active).status, 'duplicate')
  queue.dispose()
})

test('dispose aborts running work, idle waits for actual settlement, failures never retry or leak errors', async () => {
  const gate = deferred()
  const events: ContextualLearningEvent[] = []
  const queue = new ContextualLearningQueue({
    onEvent: (event) => {
      events.push(event)
    },
  })
  let calls = 0
  let signal: AbortSignal | undefined
  queue.enqueue(
    job({
      run: async (current) => {
        calls++
        signal = current
        await gate.promise
        throw new Error('PRIVATE_CANARY')
      },
    }),
  )
  await tick()
  queue.dispose()
  assert.equal(signal?.aborted, true)
  let idle = false
  const waiting = queue.whenIdle().then(() => {
    idle = true
  })
  await tick()
  assert.equal(idle, false)
  gate.resolve()
  await waiting
  assert.equal(calls, 1)
  assert.equal(queue.stats().cancelled, 1)
  assert.equal(JSON.stringify(events).includes('PRIVATE_CANARY'), false)
  assert.deepEqual(queue.enqueue(job()), { status: 'rejected', reason: 'disposed' })
  const failed = new ContextualLearningQueue()
  failed.enqueue(
    job({
      run: async () => {
        calls++
        throw new Error('unknown_commit')
      },
    }),
  )
  await failed.whenIdle()
  assert.equal(failed.stats().failed, 1)
  assert.equal(failed.enqueue(job()).status, 'duplicate')
  assert.equal(calls, 2)
  failed.dispose()
})

test('caller source array mutation and observer exceptions cannot alter admission', async () => {
  const sources = ['source-a']
  const queue = new ContextualLearningQueue({
    foregroundBusy: () => true,
    onEvent: () => {
      throw new Error('observer')
    },
  })
  queue.enqueue(job({ sourceIds: sources }))
  sources[0] = 'different'
  queue.recall('scene-a', 'source-a')
  await queue.whenIdle()
  assert.equal(queue.stats().cancelled, 1)
  queue.dispose()
  assert.throws(() => new ContextualLearningQueue({ pollMs: 0 }))
})

test('trusted saved receipt completes after its own write changes validity; legacy void still discards', async () => {
  for (const saved of [true, false]) {
    let valid = true,
      calls = 0
    const events: ContextualLearningEvent[] = []
    const queue = new ContextualLearningQueue({ onEvent: (event) => events.push(event) })
    const submission = job({
      validate: () => valid,
      committed: (result) =>
        Boolean(result && typeof result === 'object' && 'saved' in result && result.saved === true),
      run: async () => {
        calls++
        valid = false
        return saved ? { saved: true } : undefined
      },
    })
    queue.enqueue(submission)
    await queue.whenIdle()
    assert.equal(queue.stats().completed, saved ? 1 : 0)
    assert.equal(queue.stats().discarded, saved ? 0 : 1)
    assert.equal(events.at(-1)!.committed, saved ? true : undefined)
    valid = true
    assert.equal(queue.enqueue(submission).status, 'duplicate')
    assert.equal(calls, 1)
    queue.dispose()
  }
})
test('pre-run revocation prevents commit, cancellation wins over a known saved receipt without replay', async () => {
  let valid = true,
    calls = 0
  const queue = new ContextualLearningQueue()
  queue.enqueue(
    job({
      validate: () => valid,
      committed: () => true,
      run: async () => {
        calls++
        return { saved: true }
      },
    }),
  )
  valid = false
  await queue.whenIdle()
  assert.equal(calls, 0)
  assert.equal(queue.stats().discarded, 1)
  queue.dispose()
  const gate = deferred(),
    events: ContextualLearningEvent[] = []
  const cancelled = new ContextualLearningQueue({ onEvent: (e) => events.push(e) })
  const submission = job({
    committed: () => true,
    run: async () => {
      calls++
      await gate.promise
      return { saved: true }
    },
  })
  cancelled.enqueue(submission)
  await tick()
  cancelled.recall('scene-a', 'source-a')
  gate.resolve()
  await cancelled.whenIdle()
  assert.equal(cancelled.stats().cancelled, 1)
  assert.equal(cancelled.stats().completed, 0)
  assert.equal(events.at(-1)!.committed, true)
  assert.equal(cancelled.enqueue(submission).status, 'duplicate')
  assert.equal(calls, 1)
  cancelled.dispose()
})
