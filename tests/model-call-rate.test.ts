import test, { type TestContext } from 'node:test'
import assert from 'node:assert/strict'
import { WorkScheduler, modelScheduler, withModelPriority } from '../src/scheduler.ts'
import { scheduleModel } from '../src/diagnostics/model-flow.ts'
import { withRoleBudget } from '../src/models/role-budget.ts'

async function flush() {
  for (let i = 0; i < 16; i++) await Promise.resolve()
}
function clock(t: TestContext) {
  t.mock.timers.enable({ apis: ['Date', 'setTimeout'], now: new Date('2026-10-28T00:00:00Z') })
}

test('rolling-minute starts use the existing bounded queue, retaining concurrency and FIFO', async (t) => {
  clock(t)
  const queue = new WorkScheduler(2, 2)
  queue.configure(2, 2)
  const starts: number[] = []
  const run = () =>
    queue.run(0, undefined, async () => {
      starts.push(Date.now())
    })
  await run()
  await flush()
  t.mock.timers.tick(1000)
  await run()
  await flush()
  const third = run(),
    fourth = run()
  await assert.rejects(run(), /work_queue_full/)
  await flush()
  assert.equal(starts.length, 2)
  t.mock.timers.tick(58999)
  await flush()
  assert.equal(starts.length, 2)
  t.mock.timers.tick(1)
  await third
  await flush()
  assert.equal(starts.length, 3)
  t.mock.timers.tick(1000)
  await fourth
  await flush()
  assert.equal(starts.length, 4)
  assert.equal(queue.status.queued, 0)
  assert.equal(queue.status.running, 0)
})

test('global request admission covers foreground and background; a queued stage timeout spends no call', async (t) => {
  clock(t)
  modelScheduler.configure(4, 1)
  let calls = 0
  const invoke = async () => {
    calls++
    return calls
  }
  try {
    await scheduleModel(0, undefined, invoke)
    const timed = withModelPriority(2, () =>
      withRoleBudget(
        20000,
        undefined,
        (signal) => scheduleModel(0, signal, invoke),
        () => new Error('model_stage_timeout'),
      ),
    )
    const rejected = assert.rejects(timed, /model_stage_timeout/)
    await flush()
    assert.equal(modelScheduler.status.queued, 1)
    t.mock.timers.tick(20000)
    await rejected
    await flush()
    assert.equal(calls, 1)
    assert.equal(modelScheduler.status.queued, 0)
    const background = withModelPriority(2, () => scheduleModel(0, undefined, invoke))
    t.mock.timers.tick(39999)
    await flush()
    assert.equal(calls, 1)
    t.mock.timers.tick(1)
    await background
    await flush()
    assert.equal(calls, 2)
    assert.equal(modelScheduler.status.queued, 0)
  } finally {
    modelScheduler.configure(4, 0)
  }
})

test('cancelled rate wait cannot invoke later; disabling the limit drains surviving jobs once', async (t) => {
  clock(t)
  const queue = new WorkScheduler(1)
  queue.configure(1, 1)
  let calls = 0
  await queue.run(0, undefined, async () => {
    calls++
  })
  await flush()
  const controller = new AbortController()
  const cancelled = queue.run(0, controller.signal, async () => {
    calls++
  })
  const rejected = assert.rejects(cancelled, /request_cancelled/)
  const survivor = queue.run(0, undefined, async () => {
    calls++
  })
  controller.abort()
  await rejected
  queue.configure(1, 0)
  await survivor
  await flush()
  t.mock.timers.tick(120000)
  await flush()
  assert.equal(calls, 2)
  assert.equal(queue.status.queued, 0)
})
