import assert from 'node:assert/strict'
import test, { type TestContext } from 'node:test'
import { setImmediate as immediate } from 'node:timers/promises'
import { Runtime, type RuntimeMessage } from '../src/runtime.ts'
import { fixturePlanCompletion } from './helpers/reply-plan.ts'
import { Store } from '../src/store.ts'
import type { Completion, Router } from '../src/models.ts'
import type { SceneRef } from '../src/onebot.ts'

const scene: SceneRef = {
  id: 'wait-fixture',
  kind: 'group',
  adapterId: 'fixture',
  accountId: 'offline-bot',
  platformSceneId: 'synthetic-group',
}
const decision = (action: 'wait' | 'silent' | 'reply' = 'wait', waitForMs?: number): Completion => {
  const toolCalls = [
    {
      id: 'decision',
      name: 'participation.decide',
      arguments: {
        action,
        reasonCode: 'scripted_offline_wait',
        ...(waitForMs === undefined ? {} : { waitForMs }),
      },
    },
  ]
  return {
    text: '',
    toolCalls,
    modelId: 'scripted-offline',
    usage: { inputTokens: 0, outputTokens: 0, totalTokens: 0 },
    assistant: { role: 'assistant', content: '', toolCalls },
  }
}
const message = (id: string): RuntimeMessage => ({
  id,
  sceneId: scene.id,
  senderId: 'fixture-person',
  sentAt: new Date().toISOString(),
  receivedAt: new Date().toISOString(),
  origin: 'live',
  status: 'available',
  parts: [{ type: 'text', text: `WAIT_INPUT_${id}` }],
})
function clock(t: TestContext) {
  t.mock.timers.enable({
    apis: ['Date', 'setTimeout', 'setInterval'],
    now: new Date('2026-10-28T00:00:00Z'),
  })
}
async function advance(t: TestContext, ms: number) {
  t.mock.timers.tick(ms)
  await immediate()
  t.mock.timers.tick(0)
  await immediate()
}
function fixture(
  complete: Router['complete'],
  quietMs = 100,
  maxWaitMs = 400,
  kind: SceneRef['kind'] = 'group',
) {
  const scope = { ...scene, kind }
  const store = new Store(':memory:')
  store.set('settings', { ...store.settings, debounceMs: quietMs, maxWaitMs })
  store.saveScene({
    ...scope,
    name: 'Offline wait fixture',
    participationEnabled: true,
    learningEnabled: false,
    proactiveEnabled: false,
  })
  let sends = 0
  const runtime = new Runtime(store, () => ({ complete }) as Router)
  let received = 0
  runtime.on('message.received', (event) => {
    if (event.type === 'message.received') received++
  })
  runtime.addAdapter('fixture', {
    send: async () => {
      sends++
      return { status: 'sent', messageId: `answer-${sends}` }
    },
    getMessage: async () => ({ items: [], coverage: 'complete' }),
    history: async () => ({ items: [], coverage: 'complete' }),
    dispose() {},
  })
  return {
    store,
    runtime,
    ingest: (id: string) => runtime.ingest(message(id), scope),
    rechecks: () => store.logs.filter((entry) => entry.code === 'turn_wait_recheck'),
    sends: () => sends,
    received: () => received,
    close: () => {
      runtime.dispose()
      store.close()
    },
  }
}

for (const topicAdmissionEnabled of [false, true])
  test(`ordinary group burst is coalesced before model invocation and eventually replies (topic admission ${topicAdmissionEnabled})`, async (t) => {
    clock(t)
    const inputs: string[] = []
    let calls = 0
    const h = fixture(async (role, prompts, tools) => {
      calls++
      if (tools?.some((tool) => tool.name === 'participation.decide')) {
        inputs.push(prompts[1]!.content)
        return decision(inputs.length === 1 ? 'silent' : 'reply')
      }
      const plan = fixturePlanCompletion(role, prompts)
      if (plan) return plan
      if (calls === 4) {
        const toolCalls = [
          {
            id: 'send',
            name: 'messages.send',
            arguments: {
              operationId: 'one-answer',
              segments: [{ parts: [{ type: 'text', text: 'Offline answer.' }] }],
            },
          },
        ]
        return {
          ...decision('silent'),
          toolCalls,
          assistant: { role: 'assistant', content: '', toolCalls },
        }
      }
      return { ...decision('silent'), toolCalls: [], assistant: { role: 'assistant', content: '' } }
    })
    h.store.set('settings', { ...h.store.settings, topicAdmissionEnabled })
    try {
      h.ingest('first')
      await advance(t, 100)
      assert.equal(calls, 1)
      for (let i = 0; i < 20; i++) {
        h.ingest(`burst-${i}`)
        await advance(t, 50)
      }
      assert.equal(calls, 1, 'no model decision is purchased during cooldown')
      assert.equal(h.received(), 21, 'all ingress remains observable to history/learning')
      await advance(t, 1999)
      assert.equal(calls, 1)
      await advance(t, 1)
      for (let i = 0; i < 8; i++) await advance(t, 0)
      assert.equal(inputs.length, 2)
      assert.match(inputs[1]!, /WAIT_INPUT_burst-19/)
      assert.equal(h.sends(), 1)
      assert.ok(calls <= 5, 'one coalesced turn, not twenty planner/replyer turns')
      await advance(t, 10000)
      assert.equal(inputs.length, 2)
    } finally {
      h.close()
    }
  })

test('mentions use a separately configurable cooldown, without resetting the ordinary group clock', async (t) => {
  clock(t)
  const batches: string[] = []
  const h = fixture(async (_role, prompts) => {
    batches.push(prompts[1]!.content)
    return decision('silent')
  })
  const mention = (id: string) =>
    h.runtime.ingest(
      { ...message(id), parts: [{ type: 'mention', userId: 'bot' }, ...message(id).parts] },
      scene,
    )
  try {
    h.ingest('ordinary-first')
    await advance(t, 100)
    mention('direct-first')
    await advance(t, 100)
    assert.equal(batches.length, 2)
    h.store.set('settings', { ...h.store.settings, directTurnCooldownMs: 1000 })
    mention('direct-second')
    await advance(t, 100)
    assert.equal(batches.length, 2)
    await advance(t, 900)
    assert.equal(batches.length, 3)
    h.ingest('ordinary-second')
    await advance(t, 1899)
    assert.equal(batches.length, 3)
    await advance(t, 1)
    assert.equal(batches.length, 4)
  } finally {
    h.close()
  }
})

test('pending topics share a scene slot FIFO and a busy topic cannot reclaim it first', async (t) => {
  clock(t)
  const batches: string[] = []
  let release!: () => void
  const held = new Promise<void>((resolve) => {
    release = resolve
  })
  const h = fixture(async (_role, prompts) => {
    batches.push(prompts[1]!.content)
    if (batches.length === 1) await held
    return decision('silent')
  })
  h.store.set('settings', {
    ...h.store.settings,
    topicAdmissionEnabled: true,
    groupTurnCooldownMs: 0,
  })
  const ingest = (id: string, topic: string) =>
    h.runtime.ingest(
      { ...message(id), parts: [{ type: 'text', text: `[topic:${topic}] INPUT_${id}` }] },
      scene,
    )
  try {
    ingest('a1', 'a')
    ingest('b1', 'b')
    ingest('c1', 'c')
    await advance(t, 100)
    assert.equal(batches.length, 1)
    assert.equal(h.runtime.activeTasks, 1)
    ingest('a2', 'a')
    await advance(t, 100)
    assert.equal(batches.length, 1)
    release()
    for (let i = 0; i < 10; i++) await advance(t, 0)
    assert.equal(batches.length, 4)
    assert.match(batches[1]!, /INPUT_b1/)
    assert.match(batches[2]!, /INPUT_c1/)
    assert.match(batches[3]!, /INPUT_a2/)
    assert.equal(h.runtime.activeTasks, 0)
  } finally {
    release()
    h.close()
  }
})

test('cooldown configuration, recall, disable and dispose cannot replay queued source messages', async (t) => {
  clock(t)
  const batches: string[] = []
  const h = fixture(async (_role, prompts) => {
    batches.push(prompts[1]!.content)
    return decision('silent')
  })
  try {
    h.ingest('initial')
    await advance(t, 100)
    h.ingest('recalled')
    h.runtime.recall(scene.id, 'recalled')
    await advance(t, 3000)
    assert.equal(batches.length, 1)
    h.ingest('fresh')
    await advance(t, 100)
    assert.equal(batches.length, 2)
    h.ingest('disabled-pending')
    const disabled = { ...h.store.scene(scene.id)!, participationEnabled: false }
    h.store.saveScene(disabled)
    h.runtime.sceneChanged(disabled)
    await advance(t, 5000)
    const enabled = { ...disabled, participationEnabled: true }
    h.store.saveScene(enabled)
    h.runtime.sceneChanged(enabled)
    await advance(t, 5000)
    assert.equal(batches.length, 2)
    h.ingest('enabled-fresh')
    await advance(t, 100)
    assert.equal(batches.length, 3)
    h.ingest('configuration-pending')
    h.store.set('settings', { ...h.store.settings, groupTurnCooldownMs: 0 })
    h.runtime.configureAdmission()
    await advance(t, 100)
    assert.equal(batches.length, 4)
    h.ingest('disposed-pending')
    h.runtime.dispose()
    await advance(t, 60000)
    assert.equal(batches.length, 4)
  } finally {
    h.close()
  }
})

test('private messages avoid the ordinary-group cooldown but obey the configured direct interval', async (t) => {
  clock(t)
  let calls = 0
  const h = fixture(
    async () => {
      calls++
      return decision('silent')
    },
    100,
    400,
    'private',
  )
  try {
    h.ingest('private-one')
    await advance(t, 100)
    h.ingest('private-two')
    await advance(t, 100)
    assert.equal(calls, 2)
    h.store.set('settings', { ...h.store.settings, directTurnCooldownMs: 1000 })
    h.ingest('private-three')
    await advance(t, 999)
    assert.equal(calls, 2)
    await advance(t, 1)
    assert.equal(calls, 3)
  } finally {
    h.close()
  }
})

test('periodic proactive opportunities yield during cooldown while explicit calls retain manual semantics', async (t) => {
  clock(t)
  let calls = 0
  const h = fixture(async () => {
    calls++
    return decision('silent')
  })
  try {
    h.store.saveScene({ ...h.store.scene(scene.id)!, proactiveEnabled: true })
    h.ingest('first')
    await advance(t, 100)
    assert.equal(calls, 1)
    await h.runtime.proactive(scene.id, true)
    assert.equal(calls, 1, 'the Engine periodic opportunity cannot bypass ordinary admission')
    await h.runtime.proactive(scene.id)
    assert.equal(
      calls,
      2,
      'an explicit invocation is not silently skipped as a periodic opportunity',
    )
    await advance(t, h.store.settings.groupTurnCooldownMs)
    await h.runtime.proactive(scene.id, true)
    assert.equal(calls, 3)
  } finally {
    h.close()
  }
})
