import assert from 'node:assert/strict'
import test from 'node:test'
import { LearningAdmission } from '../src/memory/learning/admission.ts'

function fixture(options: ConstructorParameters<typeof LearningAdmission>[0] = {}) {
  let time = 0
  return {
    policy: new LearningAdmission({ now: () => time, ...options }),
    at: (value: number) => {
      time = value
    },
  }
}

test('quiet window coalesces new sources, duplicate triggers do not postpone work, no idle polling', () => {
  const { policy, at } = fixture()
  assert.deepEqual(policy.inspect('a'), { state: 'idle' })
  policy.admit('a', 'm1')
  at(1_000)
  policy.admit('a', 'm2')
  at(2_900)
  assert.equal(policy.admit('a', 'm2'), 'duplicate')
  assert.equal(policy.take('a'), undefined)
  at(3_000)
  const batch = policy.take('a')!
  assert.deepEqual(batch.sourceKeys, ['m1', 'm2'])
  assert.equal(policy.admit('a', 'm1'), 'duplicate')
  assert.equal(policy.take('a'), undefined)
  assert.equal(policy.finish('a', batch.token), true)
  at(100_000)
  assert.deepEqual(policy.inspect('a'), { state: 'idle' })
  assert.deepEqual(policy.readyScenes(), [])
})

test('continuous burst reaches max wait, bounded batches preserve oldest backlog, minimum interval is hard', () => {
  const { policy, at } = fixture()
  for (let n = 0; n < 8; n++) {
    at(n * 1_000)
    policy.admit('a', `m${n}`)
  }
  at(8_000)
  const first = policy.take('a', 3)!
  assert.deepEqual(first.sourceKeys, ['m0', 'm1', 'm2'])
  policy.finish('a', first.token)
  at(22_999)
  assert.equal(policy.take('a'), undefined)
  at(23_000)
  assert.deepEqual(policy.take('a')!.sourceKeys, ['m3', 'm4', 'm5', 'm6', 'm7'])
})

test('foreground and budget defer ready work without removing sources or generating retry wakeups', () => {
  const { policy, at } = fixture()
  policy.admit('a', 'silent-message')
  at(30_000)
  assert.deepEqual(policy.inspect('a', { foregroundBusy: true }), {
    state: 'blocked',
    reason: 'foreground_busy',
  })
  assert.equal(policy.take('a', 60, { foregroundBusy: true }), undefined)
  assert.deepEqual(policy.inspect('a', { budgetAvailable: false }), {
    state: 'blocked',
    reason: 'budget_unavailable',
  })
  assert.equal(policy.take('a', 60, { budgetAvailable: false }), undefined)
  assert.deepEqual(policy.take('a')!.sourceKeys, ['silent-message'])
})

test('disable cancels active tokens, rejects paused arrivals and never replays them on enable', () => {
  const { policy, at } = fixture()
  policy.admit('a', 'old')
  at(2_000)
  const old = policy.take('a')!
  policy.admit('b', 'pending')
  assert.deepEqual(policy.setEnabled(false), [old])
  assert.equal(policy.admit('a', 'paused'), 'disabled')
  assert.equal(policy.finish('a', old.token, 0), false)
  policy.setEnabled(true)
  assert.deepEqual(policy.inspect('a'), { state: 'idle' })
  assert.deepEqual(policy.inspect('b'), { state: 'idle' })
  policy.admit('a', 'new')
  at(4_000)
  const fresh = policy.take('a')!
  assert.notEqual(fresh.token, old.token)
  assert.deepEqual(fresh.sourceKeys, ['new'])
  assert.equal(policy.finish('a', old.token), false)
})

test('explicit precommit recall retains safe siblings and removes recalled source', () => {
  const { policy, at } = fixture()
  policy.admit('a', 'recalled-before-run')
  policy.recall('a', 'recalled-before-run')
  assert.deepEqual(policy.inspect('a'), { state: 'idle' })
  policy.admit('a', 'recalled')
  policy.admit('a', 'survivor')
  at(2_000)
  const running = policy.take('a')!
  assert.deepEqual(policy.recall('a', 'recalled', { requeueSurvivors: true }), running)
  assert.equal(policy.finish('a', running.token, 0), false)
  at(17_000)
  assert.deepEqual(policy.take('a')!.sourceKeys, ['survivor'])
})

test('scene cancellation is isolated, returned metadata cannot mutate pending work', () => {
  const { policy, at } = fixture()
  policy.admit('a', 'same-key')
  policy.admit('b', 'same-key')
  at(2_000)
  const a = policy.take('a')!
  a.sourceKeys.push('injected')
  assert.deepEqual(policy.cancelScene('a')!.sourceKeys, ['same-key'])
  assert.equal(policy.finish('a', a.token), false)
  assert.deepEqual(policy.take('b')!.sourceKeys, ['same-key'])
})

test('explicit retry is delayed, bounded by minimum interval and never automatic', () => {
  const { policy, at } = fixture()
  policy.admit('a', 'm1')
  at(2_000)
  const first = policy.take('a')!
  policy.admit('a', 'm2')
  policy.finish('a', first.token, 20_000)
  at(21_999)
  assert.equal(policy.take('a'), undefined)
  at(22_000)
  const retry = policy.take('a')!
  assert.deepEqual(retry.sourceKeys, ['m1', 'm2'])
  policy.finish('a', retry.token)
  at(100_000)
  assert.deepEqual(policy.readyScenes(), [])
})

test('capacity includes active sources, never silently evicts, and idle scene slots are reclaimed', () => {
  const { policy, at } = fixture({ maxSourcesPerScene: 2, maxSourcesTotal: 3, maxScenes: 2 })
  assert.equal(policy.admit('a', '1'), 'accepted')
  assert.equal(policy.admit('a', '2'), 'accepted')
  assert.equal(policy.admit('a', '3'), 'capacity')
  assert.equal(policy.admit('b', '1'), 'accepted')
  assert.equal(policy.admit('b', '2'), 'capacity')
  at(2_000)
  const a = policy.take('a')!
  assert.equal(policy.admit('a', '3'), 'capacity')
  policy.finish('a', a.token)
  assert.equal(policy.admit('c', '1'), 'capacity')
  at(17_000)
  assert.equal(policy.admit('c', '1'), 'accepted')
})

test('ready ordering prioritizes realtime then oldest scene; no semantic classifier or model call is implied', () => {
  const { policy, at } = fixture()
  policy.admit('background', 'b', 'background')
  at(100)
  policy.admit('real-old', 'r')
  at(200)
  policy.admit('real-new', 'r')
  at(2_200)
  assert.deepEqual(policy.readyScenes(), ['real-old', 'real-new', 'background'])
  assert.equal(policy.take('background')!.lane, 'background')
})

test('invalid policy and dispatch bounds reject without dispatching', () => {
  assert.throws(() => fixture({ quietMs: -1 }))
  assert.throws(() => fixture({ quietMs: 9, maxWaitMs: 8 }))
  assert.throws(() => fixture({ maxScenes: 0 }))
  const { policy } = fixture()
  assert.throws(() => policy.admit('', 'source'))
  assert.throws(() => policy.take('a', 0))
  assert.throws(() => policy.finish('a', 1, -1))
})

test('default recall never replays possibly committed active siblings, but preserves unrelated pending work', () => {
  const { policy, at } = fixture()
  policy.admit('a', 'recalled')
  policy.admit('a', 'possibly-committed')
  at(2_000)
  const running = policy.take('a')!
  policy.admit('a', 'new-pending')
  assert.deepEqual(policy.recall('a', 'recalled'), running)
  assert.equal(policy.finish('a', running.token, 0), false)
  at(17_000)
  assert.deepEqual(policy.take('a')!.sourceKeys, ['new-pending'])
})

test('configure updates existing pending deadlines and cooldown without replacing clock or active tokens', () => {
  const { policy, at } = fixture()
  policy.admit('a', 'first')
  at(1_000)
  policy.admit('a', 'second')
  policy.configure({ quietMs: 4_000, maxWaitMs: 6_000, minIntervalMs: 20_000 })
  let status = policy.inspect('a')
  assert.ok(status.state === 'waiting')
  assert.equal(status.dueAt, 5_000)
  at(4_000)
  policy.admit('a', 'third')
  status = policy.inspect('a')
  assert.ok(status.state === 'waiting')
  assert.equal(status.dueAt, 6_000)
  at(6_000)
  const batch = policy.take('a', 2)!
  policy.configure({ quietMs: 1_000, maxWaitMs: 2_000, minIntervalMs: 30_000 })
  assert.deepEqual(policy.inspect('a'), { state: 'active' })
  assert.equal(policy.finish('a', batch.token), true)
  status = policy.inspect('a')
  assert.ok(status.state === 'waiting')
  assert.equal(status.dueAt, 36_000)
  at(36_000)
  assert.deepEqual(policy.take('a')!.sourceKeys, ['third'])
})

test('capacity reductions preserve accepted sources and invalid configuration leaves all options unchanged', () => {
  const { policy, at } = fixture()
  policy.admit('a', 'one')
  policy.admit('a', 'two')
  policy.admit('b', 'three')
  at(2_000)
  const active = policy.take('a', 1)!
  policy.configure({ maxSourcesPerScene: 1, maxSourcesTotal: 1, maxScenes: 1 })
  assert.equal(policy.admit('a', 'four'), 'capacity')
  assert.equal(policy.admit('c', 'four'), 'capacity')
  assert.throws(() => policy.configure({ quietMs: 9_000, minIntervalMs: 0 }))
  assert.equal(policy.finish('a', active.token), true)
  const status = policy.inspect('a')
  assert.ok(status.state === 'waiting')
  assert.equal(status.dueAt, 17_000)
  assert.deepEqual(policy.take('b')!.sourceKeys, ['three'])
  at(17_000)
  assert.deepEqual(policy.take('a')!.sourceKeys, ['two'])
})
