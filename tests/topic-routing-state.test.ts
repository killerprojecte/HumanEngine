import test from 'node:test'
import assert from 'node:assert/strict'
import { TopicRoutingState } from '../src/runtime/topic-routing-state.ts'
import { validTopicReassignment } from '../src/runtime/topic-reassignment.ts'
import { TurnWaitCoordinator } from '../src/runtime/turn-wait.ts'
test('deferred queue bounded/deduplicated; recall clears cue and ACK but not send protection', () => {
  const state = new TopicRoutingState()
  for (let i = 0; i < 60; i++) assert.equal(state.defer(String(i), true), 'queued')
  assert.equal(state.defer('0', true), 'duplicate')
  assert.equal(state.defer('overflow', true), 'full')
  state.metadata.set('t', {
    provisional: false,
    cue: { initiatorId: 'u', label: 'private-label' },
    cueSourceId: '0',
  })
  state.acknowledge('0', 't')
  state.protectedSends.add('t')
  state.forgetMessage('0')
  assert.equal(state.metadata.get('t')!.cue, undefined)
  assert.equal(state.acknowledged.size, 0)
  state.cancel()
  assert.equal(state.deferred.size, 0)
  assert.equal(state.protectedSends.has('t'), true)
})
test('reassignment validates whole group before change; protected pending source cannot migrate', () => {
  const input = {
    groups: [{ topicId: 'selected', messageIds: ['a'] }],
    deliveredIds: ['a', 'b'],
    currentTopicId: 'one',
    selectedTopicId: 'selected',
    retained: [
      { id: 'one', messageIds: ['a'], protected: true },
      { id: 'two', messageIds: ['b'], protected: true },
    ],
  }
  assert.equal(validTopicReassignment(input), true)
  assert.equal(
    validTopicReassignment({
      ...input,
      groups: [...input.groups, { topicId: 'other', messageIds: ['a'] }],
    }),
    false,
  )
  assert.equal(
    validTopicReassignment({ ...input, groups: [{ topicId: 'other', messageIds: ['a'] }] }),
    false,
  )
  assert.equal(
    validTopicReassignment({ ...input, groups: [{ topicId: 'selected', messageIds: ['b'] }] }),
    false,
  )
  assert.equal(
    validTopicReassignment({
      ...input,
      groups: [{ topicId: 'selected', messageIds: ['foreign'] }],
    }),
    false,
  )
  assert.equal(
    validTopicReassignment({
      ...input,
      maxSlots: 2,
      groups: [...input.groups, { topicId: 'new', messageIds: [] }],
    }),
    false,
  )
})
test('retention is a read-only snapshot and does not consume WAIT/generation', () => {
  let now = 0
  const q = new TurnWaitCoordinator(
    { quietMs: 10, maxWaitMs: 20, addressedQuietMs: 5, maxModelWaitMs: 100, maxPending: 4 },
    () => now,
  )
  assert.deepEqual(q.retention(), { active: false, pending: false, waiting: false })
  q.observe({ id: 'a', meaningful: true })
  now = 10
  assert.equal(q.retention().pending, true)
  const wake = q.takeReady()!
  assert.equal(q.isCurrent(wake), true)
  assert.equal(q.retention().active, true)
  assert.equal(q.isCurrent(wake), true)
  q.finish(wake, { waitUntil: 50 })
  assert.equal(q.retention().waiting, true)
  assert.equal(q.nextWakeAt(), 50)
  now = 50
  assert.equal(q.takeReady()?.kind, 'recheck')
  assert.deepEqual(q.retention(), { active: false, pending: false, waiting: false })
})
