import test from 'node:test'
import assert from 'node:assert/strict'
import { TurnWaitCoordinator } from '../src/runtime/turn-wait.ts'

function fixture() {
  let now = 0
  const queue = new TurnWaitCoordinator(
    { quietMs: 1200, maxWaitMs: 6000, addressedQuietMs: 300, maxModelWaitMs: 10000, maxPending: 8 },
    () => now,
  )
  return { queue, at: (value: number) => (now = value) }
}

test('quiet merging and max wait produce bounded cheap admission, not periodic model calls', () => {
  const { queue, at } = fixture()
  for (let i = 0; i < 7; i++) {
    at(i * 1000)
    queue.observe({ id: `chat-${i}`, meaningful: false })
  }
  assert.equal(queue.nextWakeAt(), 6000)
  const lowValue = queue.takeReady()!
  assert.equal(lowValue.kind, 'skip')
  assert.equal(lowValue.eventIds.length, 7)
  at(12000)
  assert.equal(queue.takeReady(), undefined)
  assert.equal(queue.nextWakeAt(), undefined)
  queue.observe({ id: 'question', meaningful: true })
  at(12500)
  queue.observe({ id: 'clarification', meaningful: true })
  assert.equal(queue.nextWakeAt(), 13700)
  at(13700)
  const wake = queue.takeReady()!
  assert.equal(wake.kind, 'run')
  assert.deepEqual(wake.eventIds, ['question', 'clarification'])
  assert.equal(queue.takeReady(), undefined)
})

test('explicit addressing uses configurable shorter quiet window', () => {
  const { queue, at } = fixture()
  queue.observe({ id: 'mention', meaningful: true, addressed: true })
  assert.equal(queue.nextWakeAt(), 300)
  at(299)
  assert.equal(queue.takeReady(), undefined)
  at(300)
  assert.equal(queue.takeReady()?.kind, 'run')
})

test('WAIT wakes one cheap recheck; unchanged or past WAIT cannot cause model polling', () => {
  const { queue, at } = fixture()
  queue.observe({ id: 'question', meaningful: true })
  at(1200)
  const wake = queue.takeReady()!
  assert.equal(queue.finish(wake, { waitUntil: 90000 }), true)
  assert.equal(queue.nextWakeAt(), 11200)
  at(11200)
  assert.deepEqual(queue.takeReady(), {
    kind: 'recheck',
    generation: wake.generation,
    eventIds: [],
    reason: 'model_wait',
  })
  assert.equal(queue.finish(wake, { waitUntil: 20000 }), false)
  assert.equal(queue.nextWakeAt(), undefined)
  at(90000)
  assert.equal(queue.takeReady(), undefined)
  queue.observe({ id: 'new-question', meaningful: true })
  at(91200)
  const next = queue.takeReady()!
  queue.finish(next, { waitUntil: 91200 })
  assert.equal(queue.nextWakeAt(), undefined)
})

test('new input invalidates old generation and merges only unprocessed events after completion', () => {
  const { queue, at } = fixture()
  queue.observe({ id: 'first', meaningful: true })
  at(1200)
  const old = queue.takeReady()!
  assert.equal(queue.isCurrent(old), true)
  queue.observe({ id: 'correction', meaningful: true })
  assert.equal(queue.isCurrent(old), false)
  at(1300)
  queue.observe({ id: 'detail', meaningful: false })
  assert.equal(queue.nextWakeAt(), undefined)
  queue.finish(old, { waitUntil: 10000 })
  assert.equal(queue.nextWakeAt(), 2500)
  at(2500)
  const next = queue.takeReady()!
  assert.deepEqual(next.eventIds, ['correction', 'detail'])
  queue.finish(next)
  assert.equal(queue.nextWakeAt(), undefined)
  assert.equal(queue.observe({ id: 'first', meaningful: true }), 'duplicate')
})

test('WAIT expiry cannot force a fresh unready batch or assume low value text warrants a model', () => {
  const { queue, at } = fixture()
  queue.observe({ id: 'first', meaningful: true })
  at(1200)
  queue.finish(queue.takeReady()!, { waitUntil: 2000 })
  at(1900)
  queue.observe({ id: 'noise', meaningful: false })
  at(2000)
  assert.equal(queue.takeReady()?.kind, 'recheck')
  assert.equal(queue.nextWakeAt(), 3100)
  at(3100)
  assert.equal(queue.takeReady()?.kind, 'skip')
  assert.equal(queue.nextWakeAt(), undefined)
})

test('recall/cancel invalidate active attempts and never replay consumed or disposed inputs', () => {
  const { queue, at } = fixture()
  queue.observe({ id: 'source', meaningful: true })
  at(1200)
  const active = queue.takeReady()!
  queue.recall('source')
  assert.equal(queue.isCurrent(active), false)
  queue.finish(active, { waitUntil: 3000 })
  assert.equal(queue.nextWakeAt(), undefined)
  queue.observe({ id: 'pending', meaningful: true })
  queue.cancel()
  at(10000)
  assert.equal(queue.takeReady(), undefined)
  assert.equal(queue.finish(active), false)
  assert.equal(queue.observe({ id: 'pending', meaningful: false }), 'duplicate')
  const restarted = fixture().queue
  assert.equal(restarted.nextWakeAt(), undefined)
})

test('queue bounds are explicit and topic coordinators do not share events', () => {
  const one = fixture(),
    two = fixture()
  for (let i = 0; i < 8; i++)
    assert.equal(one.queue.observe({ id: String(i), meaningful: false }), 'accepted')
  assert.equal(one.queue.observe({ id: 'overflow', meaningful: false }), 'full')
  assert.equal(one.queue.observe({ id: '0', meaningful: false }), 'duplicate')
  assert.equal(two.queue.nextWakeAt(), undefined)
  assert.equal(two.queue.observe({ id: '0', meaningful: false }), 'accepted')
  one.queue.recall('0')
  assert.equal(one.queue.observe({ id: 'overflow', meaningful: false }), 'accepted')
  assert.throws(
    () =>
      new TurnWaitCoordinator(
        { quietMs: 2, maxWaitMs: 1, addressedQuietMs: 1, maxModelWaitMs: 1, maxPending: 1 },
        () => 0,
      ),
    /invalid_turn_wait_config/,
  )
})

test('recalling a WAIT source cancels its wake; unrelated recall does not', () => {
  const { queue, at } = fixture()
  queue.observe({ id: 'source', meaningful: true })
  at(1200)
  queue.finish(queue.takeReady()!, { waitUntil: 3000 })
  queue.recall('other')
  assert.equal(queue.nextWakeAt(), 3000)
  queue.recall('source')
  assert.equal(queue.nextWakeAt(), undefined)
  at(3000)
  assert.equal(queue.takeReady(), undefined)
})

test('configuration updates pending deadlines without resetting active generation or deduplication', () => {
  const { queue, at } = fixture()
  const config = {
    quietMs: 500,
    maxWaitMs: 2000,
    addressedQuietMs: 100,
    maxModelWaitMs: 10000,
    maxPending: 8,
  }
  queue.observe({ id: 'first', meaningful: true })
  at(200)
  queue.observe({ id: 'second', meaningful: false })
  assert.equal(queue.nextWakeAt(), 1400)
  queue.configure(config)
  assert.equal(queue.nextWakeAt(), 700)
  config.quietMs = 900
  assert.equal(queue.nextWakeAt(), 700)
  at(700)
  const active = queue.takeReady()!
  assert.deepEqual(active.eventIds, ['first', 'second'])
  assert.equal(queue.isCurrent(active), true)
  queue.configure({ ...config, quietMs: 300, maxWaitMs: 600 })
  assert.equal(queue.isCurrent(active), true)
  assert.equal(queue.nextWakeAt(), undefined)
  assert.equal(queue.observe({ id: 'first', meaningful: true }), 'duplicate')
  queue.observe({ id: 'fresh', meaningful: true })
  queue.finish(active)
  assert.equal(queue.nextWakeAt(), 1000)
  assert.throws(() => queue.configure({ ...config, quietMs: 4000 }), /invalid_turn_wait_config/)
  assert.equal(queue.nextWakeAt(), 1000)
  at(1000)
  assert.deepEqual(queue.takeReady()!.eventIds, ['fresh'])
})
