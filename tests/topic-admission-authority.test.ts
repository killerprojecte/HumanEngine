import assert from 'node:assert/strict'
import test from 'node:test'
import { mkdtemp, mkdir, rm } from 'node:fs/promises'
import { join } from 'node:path'
import { tmpdir } from 'node:os'
import { setImmediate as immediate } from 'node:timers/promises'
import { Store } from '../src/store.ts'
import { Runtime } from '../src/runtime.ts'
import type { Router, Completion } from '../src/models.ts'
import { metadata, RecoveryAuthority } from '../src/memory/recovery/ledger.ts'

test('topic WAIT expiry stops before consuming a wake when external authority advances', async (t) => {
  const root = await mkdtemp(join(tmpdir(), 'he-topic-authority-'))
  await mkdir(join(root, 'data'))
  t.mock.timers.enable({
    apis: ['Date', 'setTimeout', 'setInterval'],
    now: new Date('2026-10-29T02:00:00Z'),
  })
  t.mock.method(globalThis, 'fetch', async () => {
    throw new Error('Unexpected network')
  })
  const store = new Store(join(root, 'data', 'store.sqlite'))
  store.set('settings', {
    ...store.settings,
    topicAdmissionEnabled: true,
    debounceMs: 100,
    maxWaitMs: 500,
  })
  const scene = {
    id: 'authority-topic',
    kind: 'group' as const,
    adapterId: 'offline',
    accountId: 'synthetic',
    platformSceneId: 'g',
  }
  store.saveScene({
    ...scene,
    name: 'Synthetic',
    learningEnabled: false,
    participationEnabled: true,
    proactiveEnabled: false,
  })
  let calls = 0,
    sends = 0
  const toolCalls = [
    {
      id: 'decision',
      name: 'participation.decide',
      arguments: { action: 'wait', reasonCode: 'synthetic_wait', waitForMs: 50 },
    },
  ]
  const receipt: Completion = {
    text: '',
    toolCalls,
    modelId: 'synthetic',
    usage: { inputTokens: 0, outputTokens: 0, totalTokens: 0 },
    assistant: { role: 'assistant', content: '', toolCalls },
  }
  const runtime = new Runtime(
    store,
    () =>
      ({
        complete: async () => {
          calls++
          return receipt
        },
      }) as unknown as Router,
  )
  runtime.addAdapter('offline', {
    send: async () => {
      sends++
      throw new Error('Unexpected send')
    },
    history: async () => ({ items: [], coverage: 'complete' }),
    getMessage: async () => ({ items: [], coverage: 'complete' }),
    dispose() {},
  })
  const authority = new RecoveryAuthority(metadata(store.db)!.authority)
  const tick = async (ms: number) => {
    t.mock.timers.tick(ms)
    for (let i = 0; i < 8; i++) {
      await immediate()
      t.mock.timers.tick(0)
    }
  }
  try {
    runtime.ingest(
      {
        id: 'm',
        sceneId: scene.id,
        senderId: 'user',
        sentAt: new Date().toISOString(),
        receivedAt: new Date().toISOString(),
        origin: 'live',
        status: 'available',
        parts: [{ type: 'text', text: '[topic: fixture] synthetic input' }],
      },
      scene,
    )
    await tick(100)
    assert.equal(calls, 1)
    assert.equal(runtime.activeTasks, 0)
    authority.append({ kind: 'source', sceneId: scene.id, messageId: 'm' }, authority.head())
    assert.throws(() => store.assertRecoveryReady(), /recovery_authority_head_changed/)
    await tick(50)
    await tick(1000)
    assert.equal(calls, 1)
    assert.equal(sends, 0)
    // Reading these private counters is diagnostic only; do not mutate scheduler state.
    const state = [
      ...(
        runtime as unknown as {
          states: Map<
            string,
            {
              timers: Map<string, unknown>
              admissions: Map<string, { retention(): { waiting: boolean } }>
            }
          >
        }
      ).states.values(),
    ][0]!
    assert.equal(state.timers.size, 0)
    assert.equal(
      [...state.admissions.values()][0]!.retention().waiting,
      true,
      'blocked timer did not consume the WAIT wake',
    )
  } finally {
    runtime.dispose()
    authority.close()
    store.close()
    t.mock.timers.reset()
    await rm(root, { recursive: true, force: true })
  }
})
