import assert from 'node:assert/strict'
import test, { type TestContext } from 'node:test'
import { setImmediate as immediate } from 'node:timers/promises'
import { Runtime, type RuntimeMessage } from '../src/runtime.ts'
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
const decision = (action: 'wait' | 'silent' = 'wait', waitForMs?: number): Completion => {
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
function fixture(complete: Router['complete'], quietMs = 100, maxWaitMs = 400) {
  const store = new Store(':memory:')
  store.set('settings', { ...store.settings, debounceMs: quietMs, maxWaitMs })
  store.saveScene({
    ...scene,
    name: 'Offline wait fixture',
    participationEnabled: true,
    learningEnabled: false,
    proactiveEnabled: false,
  })
  let sends = 0
  const runtime = new Runtime(store, () => ({ complete }) as Router)
  runtime.addAdapter('fixture', {
    send: async () => {
      sends++
      throw new Error('Unexpected synthetic transport invocation')
    },
    getMessage: async () => ({ items: [], coverage: 'complete' }),
    history: async () => ({ items: [], coverage: 'complete' }),
    dispose() {},
  })
  return {
    store,
    runtime,
    ingest: (id: string) => runtime.ingest(message(id), scene),
    rechecks: () => store.logs.filter((entry) => entry.code === 'turn_wait_recheck'),
    sends: () => sends,
    close: () => {
      runtime.dispose()
      store.close()
    },
  }
}

test('Runtime WAIT expires into exactly one cheap recheck, with no new model call or send on unchanged input', async (t) => {
  clock(t)
  let calls = 0
  const h = fixture(async (_role, _prompts, tools) => {
    assert.ok(tools?.some((tool) => tool.name === 'participation.decide'))
    calls++
    return decision('wait', 50)
  })
  try {
    h.ingest('initial')
    await advance(t, 100)
    assert.equal(calls, 1)
    assert.equal(h.runtime.activeTasks, 0)
    await advance(t, 49)
    assert.equal(h.rechecks().length, 0)
    await advance(t, 1)
    assert.equal(h.rechecks().length, 1)
    await advance(t, 2000)
    assert.equal(calls, 1)
    assert.equal(h.rechecks().length, 1)
    assert.equal(h.sends(), 0)
  } finally {
    h.close()
  }
})

for (const mode of ['silent', 'zero-wait'] as const)
  test(`Runtime ${mode} settles without a timer-driven model or cheap-recheck loop`, async (t) => {
    clock(t)
    let calls = 0
    const h = fixture(async () => {
      calls++
      return decision(mode === 'silent' ? 'silent' : 'wait', 0)
    })
    try {
      h.ingest('terminal')
      await advance(t, 100)
      await advance(t, 5000)
      assert.equal(calls, 1)
      assert.equal(h.rechecks().length, 0)
      assert.equal(h.runtime.activeTasks, 0)
      assert.equal(h.sends(), 0)
    } finally {
      h.close()
    }
  })

test('fresh input during WAIT respects its own quiet window instead of being forced into a model call by old wake time', async (t) => {
  clock(t)
  const batches: string[] = []
  const h = fixture(async (_role, prompts) => {
    batches.push(prompts[1]!.content)
    return batches.length === 1 ? decision('wait', 50) : decision('silent')
  })
  try {
    h.ingest('old')
    await advance(t, 100)
    await advance(t, 20)
    h.ingest('fresh')
    await advance(t, 30)
    assert.equal(batches.length, 1)
    assert.equal(h.rechecks().length, 1)
    await advance(t, 69)
    assert.equal(batches.length, 1)
    await advance(t, 1)
    assert.equal(batches.length, 2)
    assert.match(batches[1]!, /WAIT_INPUT_fresh/)
    await advance(t, 1000)
    assert.equal(batches.length, 2)
    assert.equal(h.sends(), 0)
  } finally {
    h.close()
  }
})

test("new messages during an active decision coalesce and invalidate that generation's requested WAIT", async (t) => {
  clock(t)
  let release!: () => void
  const pending = new Promise<void>((resolve) => {
    release = resolve
  })
  const batches: string[] = []
  const h = fixture(async (_role, prompts) => {
    batches.push(prompts[1]!.content)
    if (batches.length === 1) {
      await pending
      return decision('wait', 1000)
    }
    return decision('silent')
  })
  try {
    h.ingest('generation-one')
    await advance(t, 100)
    assert.equal(h.runtime.activeTasks, 1)
    await advance(t, 20)
    h.ingest('generation-two-a')
    await advance(t, 40)
    h.ingest('generation-two-b')
    release()
    await advance(t, 0)
    assert.equal(h.runtime.activeTasks, 0)
    await advance(t, 99)
    assert.equal(batches.length, 1)
    await advance(t, 1)
    assert.equal(batches.length, 2)
    assert.match(batches[1]!, /WAIT_INPUT_generation-two-a/)
    assert.match(batches[1]!, /WAIT_INPUT_generation-two-b/)
    await advance(t, 2000)
    assert.equal(h.rechecks().length, 0, 'stale-generation WAIT must not remain scheduled')
    assert.equal(batches.length, 2)
    assert.equal(h.sends(), 0)
  } finally {
    release()
    h.close()
  }
})

test('recall while WAITING cancels the old wake and fresh input does not recover recalled body content', async (t) => {
  clock(t)
  const batches: string[] = []
  const h = fixture(async (_role, prompts) => {
    batches.push(prompts[1]!.content)
    return batches.length === 1 ? decision('wait', 300) : decision('silent')
  })
  try {
    h.ingest('RECALL_CANARY')
    await advance(t, 100)
    h.runtime.recall(scene.id, 'RECALL_CANARY')
    await advance(t, 500)
    assert.equal(batches.length, 1)
    assert.equal(h.rechecks().length, 0)
    h.ingest('fresh-after-recall')
    await advance(t, 100)
    assert.equal(batches.length, 2)
    assert.doesNotMatch(batches[1]!, /WAIT_INPUT_RECALL_CANARY/)
    assert.match(batches[1]!, /WAIT_INPUT_fresh-after-recall/)
    assert.equal(h.sends(), 0)
  } finally {
    h.close()
  }
})

test('disabling participation clears WAIT and queued input; enabling alone never replays a turn', async (t) => {
  clock(t)
  let calls = 0
  const h = fixture(async () => {
    calls++
    return decision('wait', 300)
  })
  try {
    h.ingest('before-disable')
    await advance(t, 100)
    h.ingest('queued-before-disable')
    const disabled = { ...h.store.scene(scene.id)!, participationEnabled: false }
    h.store.saveScene(disabled)
    h.runtime.sceneChanged(disabled)
    await advance(t, 1000)
    assert.equal(calls, 1)
    assert.equal(h.rechecks().length, 0)
    const enabled = { ...disabled, participationEnabled: true }
    h.store.saveScene(enabled)
    h.runtime.sceneChanged(enabled)
    await advance(t, 1000)
    assert.equal(calls, 1)
    h.ingest('fresh-after-enable')
    await advance(t, 100)
    assert.equal(calls, 2)
    assert.equal(h.sends(), 0)
  } finally {
    h.close()
  }
})

test('cancelling an in-flight decision suppresses its late WAIT result and does not create another model turn', async (t) => {
  clock(t)
  let calls = 0,
    release!: () => void,
    signal: AbortSignal | undefined
  const pending = new Promise<void>((resolve) => {
    release = resolve
  })
  const h = fixture(async (_role, _prompts, _tools, currentSignal) => {
    calls++
    signal = currentSignal
    await pending
    return decision('wait', 300)
  })
  try {
    h.ingest('cancel-active')
    await advance(t, 100)
    assert.equal(h.runtime.activeTasks, 1)
    h.runtime.cancel(scene.id)
    assert.equal(signal?.aborted, true)
    release()
    await advance(t, 1000)
    assert.equal(calls, 1)
    assert.equal(h.rechecks().length, 0)
    assert.equal(h.runtime.activeTasks, 0)
    assert.equal(h.sends(), 0)
  } finally {
    release()
    h.close()
  }
})

test('runtime collection reaches maximum wait under a continuous burst without planning once per ingress', async (t) => {
  clock(t)
  const batches: string[] = []
  const h = fixture(
    async (_role, prompts) => {
      batches.push(prompts[1]!.content)
      return decision('silent')
    },
    100,
    300,
  )
  try {
    for (let i = 0; i < 6; i++) {
      h.ingest(`burst-${i}`)
      await advance(t, 50)
      assert.equal(batches.length, i === 5 ? 1 : 0)
    }
    for (let i = 0; i < 6; i++) assert.match(batches[0]!, new RegExp(`WAIT_INPUT_burst-${i}`))
    await advance(t, 1000)
    assert.equal(batches.length, 1)
    assert.equal(h.sends(), 0)
  } finally {
    h.close()
  }
})
