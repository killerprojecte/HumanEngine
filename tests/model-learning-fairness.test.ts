import test from 'node:test'
import assert from 'node:assert/strict'
import {
  WorkScheduler,
  effectiveModelPriority,
  getOverdueLearningAt,
  modelScheduler,
  withModelPriority,
  withOverdueLearning,
} from '../src/scheduler.ts'
import { scheduleModel } from '../src/diagnostics/model-flow.ts'

function gate() {
  let release!: () => void
  const promise = new Promise<void>((resolve) => (release = resolve))
  return { promise, release }
}
async function flush() {
  for (let i = 0; i < 96; i++) await Promise.resolve()
}

test('continuous foreground gives only explicitly overdue learning one oldest slot per four starts', async () => {
  const scheduler = new WorkScheduler(1, 32, { reserveForeground: 1, backgroundAgingMs: 0 })
  const held = gate()
  const order: string[] = []
  const jobs = [scheduler.run(0, undefined, () => held.promise)]
  jobs.push(
    scheduler.run(2, undefined, async () => {
      order.push('unrelated-background')
    }),
  )
  jobs.push(
    scheduler.run(
      2,
      undefined,
      async () => {
        order.push('older-learning')
      },
      { overdueLearningAt: 0 },
    ),
  )
  jobs.push(
    scheduler.run(
      1,
      undefined,
      async () => {
        order.push('newer-learning')
      },
      { overdueLearningAt: 0 },
    ),
  )
  for (let i = 0; i < 12; i++)
    jobs.push(
      scheduler.run(
        0,
        undefined,
        async () => {
          order.push(`foreground-${i}`)
        },
        { overdueLearningAt: 0 },
      ),
    )
  await flush()
  assert.deepEqual(order, [])
  assert.equal(scheduler.status.running, 1)
  held.release()
  await Promise.all(jobs)
  await flush()
  assert.deepEqual(order, [
    ...Array.from({ length: 4 }, (_, i) => `foreground-${i}`),
    'older-learning',
    ...Array.from({ length: 4 }, (_, i) => `foreground-${i + 4}`),
    'newer-learning',
    ...Array.from({ length: 4 }, (_, i) => `foreground-${i + 8}`),
    'unrelated-background',
  ])
  assert.deepEqual(scheduler.status, { running: 0, queued: 0, concurrency: 1 })
})

test('a learning batch queued before its deadline becomes fair only after four subsequent foreground starts', async () => {
  let now = 0
  const scheduler = new WorkScheduler(1, 32, { now: () => now })
  const held = gate()
  const halfway = gate()
  const order: string[] = []
  const jobs = [scheduler.run(0, undefined, () => held.promise)]
  jobs.push(
    scheduler.run(
      1,
      undefined,
      async () => {
        order.push('learning')
      },
      { overdueLearningAt: 100 },
    ),
  )
  for (let i = 0; i < 10; i++)
    jobs.push(
      scheduler.run(0, undefined, async () => {
        order.push(`foreground-${i}`)
        if (i === 4) await halfway.promise
      }),
    )
  held.release()
  await flush()
  assert.deepEqual(
    order,
    Array.from({ length: 5 }, (_, i) => `foreground-${i}`),
  )
  now = 100
  halfway.release()
  await Promise.all(jobs)
  assert.deepEqual(order, [
    ...Array.from({ length: 9 }, (_, i) => `foreground-${i}`),
    'learning',
    'foreground-9',
  ])
})

test('unmarked aged background retains foreground priority under sustained demand', async () => {
  const scheduler = new WorkScheduler(1, 32, { backgroundAgingMs: 0 })
  const held = gate()
  const order: string[] = []
  const jobs = [scheduler.run(0, undefined, () => held.promise)]
  jobs.push(
    scheduler.run(2, undefined, async () => {
      order.push('background')
    }),
  )
  for (let i = 0; i < 12; i++)
    jobs.push(
      scheduler.run(0, undefined, async () => {
        order.push(`foreground-${i}`)
      }),
    )
  held.release()
  await Promise.all(jobs)
  assert.deepEqual(order, [
    ...Array.from({ length: 12 }, (_, i) => `foreground-${i}`),
    'background',
  ])
})

test('fairness waits for the existing nonforeground cap and preserves the reserved foreground slot', async () => {
  const scheduler = new WorkScheduler(2, 32, { reserveForeground: 1 })
  const background = gate()
  const learning = gate()
  const foreground = Array.from({ length: 6 }, gate)
  const order: string[] = []
  const jobs = [scheduler.run(2, undefined, () => background.promise)]
  jobs.push(scheduler.run(0, undefined, () => foreground[0]!.promise))
  jobs.push(
    scheduler.run(
      1,
      undefined,
      async () => {
        order.push('learning-1')
        await learning.promise
      },
      { overdueLearningAt: 0 },
    ),
  )
  jobs.push(
    scheduler.run(
      1,
      undefined,
      async () => {
        order.push('learning-2')
      },
      { overdueLearningAt: 0 },
    ),
  )
  for (let i = 1; i < foreground.length; i++)
    jobs.push(
      scheduler.run(0, undefined, async () => {
        order.push(`foreground-${i}`)
        await foreground[i]!.promise
      }),
    )
  for (let i = 0; i < 4; i++) {
    foreground[i]!.release()
    await flush()
    assert.deepEqual(
      order,
      Array.from({ length: i + 1 }, (_, j) => `foreground-${j + 1}`),
    )
    assert.equal(scheduler.status.running, 2)
  }
  background.release()
  await flush()
  assert.deepEqual(order, [
    'foreground-1',
    'foreground-2',
    'foreground-3',
    'foreground-4',
    'learning-1',
  ])
  foreground[4]!.release()
  await flush()
  assert.equal(order.at(-1), 'foreground-5')
  assert.equal(order.includes('learning-2'), false)
  assert.equal(scheduler.status.running, 2)
  learning.release()
  foreground[5]!.release()
  await Promise.all(jobs)
  await flush()
  assert.equal(order.at(-1), 'learning-2')
  assert.equal(scheduler.status.running, 0)
})

test('queued overdue cancellation keeps the surviving learning turn; running cancellation never preempts', async () => {
  const scheduler = new WorkScheduler(1, 32, { reserveForeground: 1 })
  const held = gate()
  const fourth = gate()
  const learning = gate()
  const cancelled = new AbortController()
  const running = new AbortController()
  const order: string[] = []
  const jobs = [scheduler.run(0, undefined, () => held.promise)]
  const removed = scheduler.run(
    1,
    cancelled.signal,
    async () => {
      order.push('cancelled')
    },
    { overdueLearningAt: 0 },
  )
  const rejection = assert.rejects(removed, /request_cancelled/)
  jobs.push(
    scheduler.run(
      1,
      running.signal,
      async () => {
        order.push('learning')
        await learning.promise
      },
      { overdueLearningAt: 0 },
    ),
  )
  for (let i = 0; i < 8; i++)
    jobs.push(
      scheduler.run(0, undefined, async () => {
        order.push(`foreground-${i}`)
        if (i === 3) await fourth.promise
      }),
    )
  held.release()
  await flush()
  assert.equal(order.length, 4)
  cancelled.abort()
  await rejection
  assert.equal(scheduler.status.queued, 5)
  fourth.release()
  await flush()
  assert.equal(order.at(-1), 'learning')
  running.abort()
  await flush()
  assert.equal(order.length, 5)
  assert.equal(scheduler.status.running, 1)
  learning.release()
  await Promise.all(jobs)
  assert.deepEqual(order, [
    ...Array.from({ length: 4 }, (_, i) => `foreground-${i}`),
    'learning',
    ...Array.from({ length: 4 }, (_, i) => `foreground-${i + 4}`),
  ])
})

test('cancelling all overdue requests clears their accumulated foreground allowance', async () => {
  const scheduler = new WorkScheduler(1, 32)
  const held = gate()
  const fourth = gate()
  const controller = new AbortController()
  const order: string[] = []
  const jobs = [scheduler.run(0, undefined, () => held.promise)]
  const removed = scheduler.run(
    1,
    controller.signal,
    async () => {
      order.push('cancelled')
    },
    { overdueLearningAt: 0 },
  )
  const rejection = assert.rejects(removed, /request_cancelled/)
  for (let i = 0; i < 9; i++)
    jobs.push(
      scheduler.run(0, undefined, async () => {
        order.push(`foreground-${i}`)
        if (i === 3) await fourth.promise
      }),
    )
  held.release()
  await flush()
  controller.abort()
  await rejection
  jobs.push(
    scheduler.run(
      1,
      undefined,
      async () => {
        order.push('new-learning')
      },
      { overdueLearningAt: 0 },
    ),
  )
  fourth.release()
  await Promise.all(jobs)
  assert.deepEqual(order, [
    ...Array.from({ length: 8 }, (_, i) => `foreground-${i}`),
    'new-learning',
    'foreground-8',
  ])
})

test('an overdue learning turn still consumes and waits for the rolling-minute rate allowance', async (t) => {
  t.mock.timers.enable({ apis: ['Date', 'setTimeout'], now: new Date('2026-10-28T00:00:00Z') })
  const scheduler = new WorkScheduler(1, 32)
  scheduler.configure(1, 5)
  const held = gate()
  const order: string[] = []
  const starts: number[] = []
  const record = (name: string) => {
    order.push(name)
    starts.push(Date.now())
  }
  const jobs = [
    scheduler.run(0, undefined, async () => {
      starts.push(Date.now())
      await held.promise
    }),
  ]
  jobs.push(
    scheduler.run(
      1,
      undefined,
      async () => {
        record('learning')
      },
      { overdueLearningAt: Date.now() },
    ),
  )
  for (let i = 0; i < 5; i++)
    jobs.push(
      scheduler.run(0, undefined, async () => {
        record(`foreground-${i}`)
      }),
    )
  held.release()
  await flush()
  assert.deepEqual(
    order,
    Array.from({ length: 4 }, (_, i) => `foreground-${i}`),
  )
  assert.deepEqual(scheduler.status, { running: 0, queued: 2, concurrency: 1 })
  t.mock.timers.tick(59_999)
  await flush()
  assert.equal(starts.length, 5)
  t.mock.timers.tick(1)
  await Promise.all(jobs)
  await flush()
  assert.deepEqual(order.slice(-2), ['learning', 'foreground-4'])
  assert.equal(starts[5]! - starts[0]!, 60_000)
  assert.equal(starts.length, 7)
  assert.deepEqual(scheduler.status, { running: 0, queued: 0, concurrency: 1 })
})

test('learning deadlines propagate through actual model scheduling without elevating or leaking priority', async () => {
  const held = gate()
  const order: string[] = []
  const seen: Array<[number, number | undefined]> = []
  const jobs: Promise<unknown>[] = []
  modelScheduler.configure(1)
  try {
    jobs.push(scheduleModel(0, undefined, () => held.promise))
    const deadline = Date.now() - 1
    jobs.push(
      withOverdueLearning(
        () =>
          withModelPriority(1, async () => {
            await Promise.resolve()
            assert.equal(effectiveModelPriority(0), 1)
            assert.equal(getOverdueLearningAt(), deadline)
            return scheduleModel(0, undefined, async () => {
              await Promise.resolve()
              seen.push([effectiveModelPriority(0), getOverdueLearningAt()])
              order.push('learning')
            })
          }),
        deadline,
      ),
    )
    assert.equal(effectiveModelPriority(0), 0)
    assert.equal(getOverdueLearningAt(), undefined)
    withOverdueLearning(() => {
      assert.equal(effectiveModelPriority(0), 0)
      assert.equal(getOverdueLearningAt(), 0)
    })
    await flush()
    for (let i = 0; i < 5; i++)
      jobs.push(
        scheduleModel(0, undefined, async () => {
          seen.push([effectiveModelPriority(0), getOverdueLearningAt()])
          order.push(`foreground-${i}`)
        }),
      )
    held.release()
    await Promise.all(jobs)
    await flush()
    assert.deepEqual(order, [
      'foreground-0',
      'foreground-1',
      'foreground-2',
      'foreground-3',
      'learning',
      'foreground-4',
    ])
    assert.deepEqual(seen, [
      [0, undefined],
      [0, undefined],
      [0, undefined],
      [0, undefined],
      [1, deadline],
      [0, undefined],
    ])
    assert.equal(getOverdueLearningAt(), undefined)
    assert.equal(effectiveModelPriority(0), 0)
  } finally {
    held.release()
    await Promise.allSettled(jobs)
    await flush()
    modelScheduler.configure(4)
  }
})
