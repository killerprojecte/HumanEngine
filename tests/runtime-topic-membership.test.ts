import assert from 'node:assert/strict'
import test from 'node:test'
import { Store } from '../src/store.ts'
import { Runtime, type RuntimeMessage } from '../src/runtime.ts'

function fixture(enabled: boolean) {
  const store = new Store(':memory:')
  store.set('settings', { ...store.settings, topicAdmissionEnabled: enabled })
  const runtime = new Runtime(store, () => {
    throw new Error('unexpected_model')
  })
  const scene = {
    id: 'synthetic',
    kind: 'group' as const,
    adapterId: 'fixture',
    accountId: 'bot',
    platformSceneId: 'group',
  }
  store.saveScene({
    ...scene,
    name: 'synthetic',
    participationEnabled: false,
    learningEnabled: false,
    proactiveEnabled: false,
  })
  const message = (id: string, status: RuntimeMessage['status'] = 'available'): RuntimeMessage => ({
    id,
    sceneId: scene.id,
    senderId: 'person',
    sentAt: new Date().toISOString(),
    receivedAt: new Date().toISOString(),
    origin: 'live',
    status,
    parts: [{ type: 'text', text: 'synthetic' }],
  })
  const state = (runtime as any).state(scene.id)
  return {
    store,
    runtime,
    scene,
    message,
    state,
    close() {
      runtime.dispose()
      store.close()
    },
  }
}
for (const enabled of [false, true]) {
  test(`scene topic counts preserve duplicates, availability and retained history (admission ${enabled})`, () => {
    const f = fixture(enabled)
    try {
      const ids = [...Array.from({ length: 1000 }, (_, i) => `gone-${i}`), 'a', 'a', 'b', 'missing']
      for (const id of ids) f.state.topicHistory.observe(id, new Date().toISOString())
      f.state.messages = [
        f.message('a'),
        { ...f.message('b'), status: 'recalled' },
        { ...f.message('expired'), receivedAt: '2000-01-01T00:00:00.000Z' },
      ]
      f.state.topics.set('topic', {
        id: 'topic',
        version: 1,
        messageIds: ids,
        lastAt: Date.now(),
        attention: 0.5,
      })
      const result = f.runtime.sceneState(f.scene.id)
      assert.equal(result.topics[0]?.messageCount, 2)
      assert.equal(f.state.topics.get('topic').messageIds, ids)
      assert.equal(ids.length, 1004)
      f.state.messages[0].status = 'recalled'
      assert.equal(f.runtime.sceneState(f.scene.id).topics[0]?.messageCount, 0)
    } finally {
      f.close()
    }
  })
}
test('admission routes a reply to a retained topic without changing membership order', () => {
  const f = fixture(true)
  try {
    const ids = [...Array.from({ length: 1000 }, (_, i) => `gone-${i}`), 'anchor']
    for (const id of ids) f.state.topicHistory.observe(id, new Date().toISOString())
    f.state.messages = [f.message('anchor')]
    f.state.topics.set('topic', {
      id: 'topic',
      version: 1,
      messageIds: ids,
      lastAt: Date.now(),
      attention: 0.5,
    })
    f.state.routing.metadata.set('topic', { provisional: false })
    const before = [...ids]
    f.runtime.ingest({ ...f.message('reply'), replyToId: 'anchor' }, f.scene, false)
    assert.deepEqual(f.state.topics.get('topic').messageIds, [...before, 'reply'])
    assert.equal(f.state.topics.size, 1)
    assert.equal(f.runtime.activeTasks, 0)
  } finally {
    f.close()
  }
})
