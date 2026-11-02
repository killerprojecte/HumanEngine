import assert from 'node:assert/strict'
import test from 'node:test'
import { Store } from '../src/store.ts'
import { LearningCoordinator } from '../src/memory/learning/coordinator.ts'
import type { LearningPorts } from '../src/memory/learning/batch-contracts.ts'
import type { RuntimeMessage } from '../src/runtime.ts'

function fixture(max = 120) {
  const store = new Store(':memory:')
  store.set('settings', {
    ...store.settings,
    learningBatchMs: 300000,
    learningQueueMaxMessages: max,
    learningQueueMaxTotalMessages: max,
  })
  store.saveScene({
    id: 's',
    name: 's',
    kind: 'group',
    adapterId: 'fixture',
    accountId: 'bot',
    platformSceneId: 'group',
    participationEnabled: false,
    learningEnabled: true,
    proactiveEnabled: false,
  })
  const unexpected = () => {
    throw new Error('unexpected_model_or_memory')
  }
  const ports: LearningPorts = {
    foregroundBusy: () => true,
    router: unexpected,
    memory: { reconciliationTargets: unexpected, adminList: unexpected },
    searchMemory: unexpected,
    proposeSource: unexpected,
    proposeHabit: unexpected,
    unsupportedSourcePronouns: unexpected,
  }
  const queue = new LearningCoordinator(store, ports)
  let reads = 0
  const recalled = store.recalled.bind(store)
  store.recalled = (scene, id) => {
    reads++
    return recalled(scene, id)
  }
  const message = (id: string): RuntimeMessage => ({
    id,
    sceneId: 's',
    senderId: 'person',
    origin: 'live',
    status: 'available',
    sentAt: new Date().toISOString(),
    receivedAt: new Date().toISOString(),
    parts: [{ type: 'text', text: 'synthetic' }],
  })
  return {
    store,
    queue,
    message,
    reads: () => reads,
    close() {
      queue.dispose()
      store.close()
    },
  }
}
test('normal accepted burst checks each new source once instead of rescanning pending payloads', () => {
  const f = fixture()
  try {
    for (let i = 0; i < 100; i++) f.queue.enqueueLearning(f.message(String(i)))
    assert.equal(f.queue.learningStatus().queued, 100)
    assert.equal(f.reads(), 100)
    f.queue.enqueueLearning(f.message('0'))
    assert.equal(f.queue.learningStatus().queued, 100)
    assert.equal(f.reads(), 101)
  } finally {
    f.close()
  }
})
test('settings repair stays enabled and cannot resurrect recalled or cancelled sources', () => {
  const f = fixture()
  try {
    f.queue.enqueueLearning(f.message('a'))
    f.queue.enqueueLearning(f.message('b'))
    f.store.recall('s', 'a')
    f.queue.recall('s', 'a')
    const before = f.reads()
    f.queue.settingsChanged()
    assert.equal(f.reads() - before, 1)
    assert.equal(f.queue.learningStatus().queued, 1)
    f.queue.cancelLearning('s')
    f.queue.settingsChanged()
    assert.equal(f.queue.learningStatus().queued, 0)
    f.queue.enqueueLearning(f.message('a'))
    assert.equal(f.queue.learningStatus().queued, 0)
  } finally {
    f.close()
  }
})
test('capacity rejection remains bounded and does not reschedule or read old sources', () => {
  const f = fixture(60)
  try {
    for (let i = 0; i < 61; i++) f.queue.enqueueLearning(f.message(String(i)))
    assert.equal(f.queue.learningStatus().queued, 60)
    assert.equal(f.queue.learningStatus().overflow, 1)
    assert.equal(f.reads(), 61)
  } finally {
    f.close()
  }
})

test('explicit retry keeps repair enabled for surviving payloads', () => {
  const f = fixture()
  try {
    const failures = (f.queue as unknown as { learningFailures: Map<string, unknown> })
      .learningFailures
    failures.set('failure', {
      id: 'failure',
      sceneId: 's',
      attempt: 1,
      messageCount: 2,
      partial: false,
      canRetry: true,
      createdAt: new Date().toISOString(),
      messages: [f.message('recalled'), f.message('valid')],
    })
    f.store.recall('s', 'recalled')
    const before = f.reads()
    assert.deepEqual(f.queue.retryLearning('failure'), { ok: true })
    assert.equal(f.queue.learningStatus().queued, 1)
    assert.equal(f.reads() - before, 3) // Two eligibility reads and one conservative repair read.
  } finally {
    f.close()
  }
})

test('synchronous recall after admission logging cannot resurrect a removed payload', () => {
  const f = fixture()
  const log = f.store.log.bind(f.store)
  let recalled = false
  f.store.log = (...args) => {
    const row = log(...args)
    if (!recalled && row.stage === 'learning.admission' && row.status === 'completed') {
      recalled = true
      f.store.recall('s', 'a')
      f.queue.recall('s', 'a')
    }
    return row
  }
  try {
    f.queue.enqueueLearning(f.message('a'))
    assert.equal(recalled, true)
    assert.equal(f.queue.learningStatus().queued, 0)
    f.queue.settingsChanged()
    assert.equal(f.queue.learningStatus().queued, 0)
  } finally {
    f.close()
  }
})
