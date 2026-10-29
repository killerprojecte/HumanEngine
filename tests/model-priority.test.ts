import test from 'node:test'
import { AsyncLocalStorage } from 'node:async_hooks'
import assert from 'node:assert/strict'
import { WorkScheduler, withModelPriority, effectiveModelPriority } from '../src/scheduler.ts'

function gate() {
  let release!: () => void
  const promise = new Promise<void>((resolve) => (release = resolve))
  return { promise, release }
}
async function flush() {
  for (let i = 0; i < 8; i++) await Promise.resolve()
}

test('model reservation leaves capacity for foreground while allowing background progress', async () => {
  const queue = new WorkScheduler(4, 20, { reserveForeground: 1 })
  const gates = Array.from({ length: 5 }, gate)
  const started: number[] = []
  const work = gates.slice(0, 4).map((g, i) =>
    queue.run(2, undefined, async () => {
      started.push(i)
      await g.promise
    }),
  )
  await flush()
  assert.deepEqual(started, [0, 1, 2])
  assert.deepEqual(queue.status, { running: 3, queued: 1, concurrency: 4 })
  work.push(
    queue.run(0, undefined, async () => {
      started.push(4)
      await gates[4]!.promise
    }),
  )
  await flush()
  assert.deepEqual(started, [0, 1, 2, 4])
  gates[0]!.release()
  await flush()
  assert.deepEqual(started, [0, 1, 2, 4, 3])
  for (const g of gates) g.release()
  await Promise.all(work)
  await flush()
  assert.equal(queue.status.running, 0)
})

test('default schedulers retain full background capacity and equal-priority FIFO', async () => {
  const queue = new WorkScheduler(2)
  const first = gate(),
    second = gate()
  const order: string[] = []
  const jobs = [
    queue.run(2, undefined, async () => {
      order.push('a')
      await first.promise
    }),
    queue.run(2, undefined, async () => {
      order.push('b')
      await second.promise
    }),
    queue.run(1, undefined, async () => {
      order.push('c')
    }),
    queue.run(1, undefined, async () => {
      order.push('d')
    }),
  ]
  await flush()
  assert.deepEqual(order, ['a', 'b'])
  first.release()
  await jobs[3]
  assert.deepEqual(order, ['a', 'b', 'c', 'd'])
  second.release()
  await Promise.all(jobs)
})

test('aging promotes maintenance only to realtime, never ahead of foreground', async () => {
  let now = 0
  const queue = new WorkScheduler(1, 10, {
    reserveForeground: 1,
    backgroundAgingMs: 100,
    now: () => now,
  })
  const held = gate(),
    foreground = gate()
  const order: string[] = []
  const jobs = [queue.run(1, undefined, () => held.promise)]
  jobs.push(
    queue.run(2, undefined, async () => {
      order.push('aged')
    }),
  )
  now = 100
  jobs.push(
    queue.run(1, undefined, async () => {
      order.push('realtime')
    }),
  )
  jobs.push(
    queue.run(0, undefined, async () => {
      order.push('foreground')
      await foreground.promise
    }),
  )
  held.release()
  await flush()
  assert.deepEqual(order, ['foreground'])
  foreground.release()
  await Promise.all(jobs)
  assert.deepEqual(order, ['foreground', 'aged', 'realtime'])
})

test('queued cancellation removes work; running cancellation cannot falsely free a slot', async () => {
  const queue = new WorkScheduler(2, 2, { reserveForeground: 1 })
  const held = gate(),
    started: string[] = []
  const activeSignal = new AbortController(),
    waitingSignal = new AbortController()
  const active = queue.run(1, activeSignal.signal, async () => {
    started.push('active')
    await held.promise
  })
  const waiting = queue.run(1, waitingSignal.signal, async () => {
    started.push('cancelled')
  })
  const rejected = assert.rejects(waiting, /request_cancelled/)
  waitingSignal.abort()
  await rejected
  activeSignal.abort()
  const next = queue.run(1, undefined, async () => {
    started.push('next')
  })
  await flush()
  assert.deepEqual(started, ['active'])
  assert.equal(queue.status.queued, 1)
  held.release()
  await Promise.all([active, next])
  assert.deepEqual(started, ['active', 'next'])
})

test('single concurrency does not preempt running background for foreground', async () => {
  const queue = new WorkScheduler(1, 10, { reserveForeground: 1 })
  const held = gate(),
    order: string[] = []
  const background = queue.run(2, undefined, async () => {
    order.push('background')
    await held.promise
  })
  await flush()
  const foreground = queue.run(0, undefined, async () => {
    order.push('foreground')
  })
  await flush()
  assert.deepEqual(order, ['background'])
  held.release()
  await Promise.all([background, foreground])
  assert.deepEqual(order, ['background', 'foreground'])
})

test('scoped model lanes propagate asynchronously without elevating background or leaking', async () => {
  assert.equal(effectiveModelPriority(0), 0)
  await withModelPriority(2, async () => {
    await Promise.resolve()
    assert.equal(effectiveModelPriority(0), 2)
    await withModelPriority(1, async () => {
      assert.equal(effectiveModelPriority(0), 2)
    })
  })
  await withModelPriority(1, async () => assert.equal(effectiveModelPriority(0), 1))
  assert.equal(effectiveModelPriority(0), 0)
  assert.throws(() => withModelPriority(0 as 1, () => {}), /invalid_model_priority/)
})

test('concurrency reconfiguration preserves running work and adjusts the reservation', async () => {
  const queue = new WorkScheduler(2, 10, { reserveForeground: 1 })
  const held = gate()
  const order: string[] = []
  const first = queue.run(2, undefined, async () => {
    order.push('first')
    await held.promise
  })
  const second = queue.run(1, undefined, async () => {
    order.push('second')
  })
  await flush()
  assert.deepEqual(order, ['first'])
  queue.configure(3)
  await second
  assert.deepEqual(order, ['first', 'second'])
  queue.configure(1)
  const foreground = queue.run(0, undefined, async () => {
    order.push('foreground')
  })
  await flush()
  assert.deepEqual(order, ['first', 'second'])
  held.release()
  await Promise.all([first, foreground])
  assert.deepEqual(order, ['first', 'second', 'foreground'])
})

test('queued tasks retain submitting async contexts for nested model scheduling without leaking lanes', async () => {
  const queue = new WorkScheduler(1)
  const nested = new WorkScheduler(1)
  const trace = new AsyncLocalStorage<string>()
  const held = gate()
  const blocker = queue.run(0, undefined, () => held.promise)
  const seen: Array<[number, string | undefined]> = []
  const submit = (priority: number) =>
    queue.run(priority, undefined, async () => {
      await Promise.resolve()
      const inherited = effectiveModelPriority(0)
      seen.push([inherited, trace.getStore()])
      return nested.run(inherited, undefined, async () => {
        await Promise.resolve()
        return [effectiveModelPriority(0), trace.getStore()] as const
      })
    })
  const background = trace.run('background-trace', () => withModelPriority(2, () => submit(2)))
  const realtime = trace.run('realtime-trace', () => withModelPriority(1, () => submit(1)))
  const foreground = submit(0)
  await flush()
  assert.equal(seen.length, 0)
  held.release()
  await blocker
  assert.deepEqual(await foreground, [0, undefined])
  assert.deepEqual(await realtime, [1, 'realtime-trace'])
  assert.deepEqual(await background, [2, 'background-trace'])
  assert.deepEqual(seen, [
    [0, undefined],
    [1, 'realtime-trace'],
    [2, 'background-trace'],
  ])
  assert.equal(effectiveModelPriority(0), 0)
  assert.equal(trace.getStore(), undefined)
})
