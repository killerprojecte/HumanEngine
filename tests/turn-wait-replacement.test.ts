import test from 'node:test'
import assert from 'node:assert/strict'
import { TurnWaitCoordinator } from '../src/runtime/turn-wait.ts'
function fixture() {
  let now = 0
  const q = new TurnWaitCoordinator(
    { quietMs: 10, maxWaitMs: 100, addressedQuietMs: 2, maxModelWaitMs: 200, maxPending: 10 },
    () => now,
  )
  return {
    q,
    at(value: number) {
      now = value
    },
  }
}

test('trusted edit readmits only its exact ID while ordinary duplicate and recalled ingress stay deduplicated', () => {
  const { q, at } = fixture()
  assert.equal(q.observe({ id: 'edited', meaningful: true }), 'accepted')
  assert.equal(q.observe({ id: 'other', meaningful: true }), 'accepted')
  assert.equal(q.observe({ id: 'edited', meaningful: true }), 'duplicate')
  q.replace('edited')
  assert.equal(q.observe({ id: 'other', meaningful: true }), 'duplicate')
  at(5)
  assert.equal(q.observe({ id: 'edited', meaningful: true }), 'accepted')
  at(10)
  assert.equal(q.takeReady(), undefined)
  at(15)
  const wake = q.takeReady()!
  assert.deepEqual(wake.eventIds, ['other', 'edited'])
  q.finish(wake)
  q.recall('other')
  assert.equal(q.observe({ id: 'other', meaningful: true }), 'duplicate')
  assert.throws(() => q.replace(''), /invalid_turn_wait_event/)
})

test('replacement invalidates active generation without releasing it or replaying partial/unknown sends', () => {
  const { q, at } = fixture()
  q.observe({ id: 'edited', meaningful: true })
  at(10)
  const old = q.takeReady()!
  assert.equal(q.isCurrent(old), true)
  q.replace('edited')
  assert.equal(q.isCurrent(old), false)
  q.observe({ id: 'edited', meaningful: true })
  at(100)
  assert.equal(q.takeReady(), undefined)
  assert.equal(q.nextWakeAt(), undefined)
  // The runtime alone determines what was delivered; stale finish may not create a WAIT.
  assert.equal(q.finish(old, { waitUntil: 150 }), true)
  const fresh = q.takeReady()!
  assert.deepEqual(fresh.eventIds, ['edited'])
  assert.ok(fresh.generation > old.generation)
  assert.equal(q.finish(old), false)
  q.finish(fresh)
  assert.equal(q.nextWakeAt(), undefined)
  assert.equal(q.takeReady(), undefined)
})

test('edited WAIT is cancelled, replacement needs fresh quiet window, and an edit alone cannot start a model loop', () => {
  const { q, at } = fixture()
  q.observe({ id: 'edited', meaningful: true })
  at(10)
  const wake = q.takeReady()!
  q.finish(wake, { waitUntil: 60 })
  q.replace('edited')
  at(70)
  assert.equal(q.nextWakeAt(), undefined)
  assert.equal(q.takeReady(), undefined)
  q.observe({ id: 'edited', meaningful: true })
  assert.equal(q.takeReady(), undefined)
  at(80)
  const replacement = q.takeReady()!
  q.finish(replacement, { waitUntil: 80 })
  for (let time = 81; time < 100; time++) {
    at(time)
    assert.equal(q.takeReady(), undefined)
  }
})
