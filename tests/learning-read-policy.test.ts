import assert from 'node:assert/strict'
import test from 'node:test'
import { Store } from '../src/store.ts'
import { MemoryService } from '../src/memory.ts'
import { runLearningBatch } from '../src/memory/learning/batch-runner.ts'
import type {
  BatchStore,
  CommitProgress,
  LearningPorts,
} from '../src/memory/learning/batch-contracts.ts'
import type { Completion } from '../src/models.ts'
import type { RuntimeMessage } from '../src/runtime.ts'
function fixture(read = true, personal = true) {
  const store = new Store(':memory:')
  store.saveScene({
    id: 's',
    kind: 'group',
    adapterId: 'a',
    accountId: 'a',
    platformSceneId: 's',
    name: 'Synthetic',
    participationEnabled: false,
    learningEnabled: true,
    proactiveEnabled: false,
  })
  const memory = new MemoryService(store)
  const old = memory.create({
    scope: { kind: 'scene', sceneId: 's' },
    kind: 'fact',
    content: 'OLD_MEMORY_CANARY',
  })
  let state = { memoryReadEnabled: read, personalizationEnabled: personal, memoryReadRevision: 0 }
  const facade: BatchStore = {
    get settings() {
      return store.settings
    },
    log: (...args) => store.log(...args),
    recalled: (...args) => store.recalled(...args),
    scene: (id) => {
      const scene = store.scene(id)
      return scene ? { ...scene, ...state } : undefined
    },
  }
  const message: RuntimeMessage = {
    id: 'fresh',
    sceneId: 's',
    senderId: 'user',
    origin: 'live',
    status: 'available',
    sentAt: '2026-10-29T00:00:00Z',
    receivedAt: '2026-10-29T00:00:00Z',
    parts: [{ type: 'text', text: 'The new encoder uses fixed-width unsigned integers.' }],
  }
  let searches = 0,
    writes = 0,
    models = 0
  let searching = async () => [old],
    modelEffect = async () => {},
    queueEffect = async () => {}
  const ports: LearningPorts = {
    memory,
    foregroundBusy: () => false,
    unsupportedSourcePronouns: () => false,
    searchMemory: async () => {
      searches++
      return searching()
    },
    proposeHabit: () => ({ status: 'rejected', reason: 'not_used' }),
    proposeSource: async () => {
      writes++
      return { status: 'rejected', reason: 'synthetic_write_receipt' }
    },
    router: (admit) => ({
      complete: async (_role, messages) => {
        await queueEffect()
        admit?.()
        models++
        await modelEffect()
        const payload = messages.find((m) => m.role === 'user')!.content as string
        assert.equal(
          payload.includes('OLD_MEMORY_CANARY'),
          false,
          'source extraction never includes old memory',
        )
        const input = JSON.parse(payload),
          id = input.messages[0].id
        const toolCalls = [
          {
            id: 'proposal',
            name: 'learning.propose',
            arguments: {
              content: 'The encoder uses fixed-width unsigned integers.',
              kind: 'fact',
              sourceMessageIds: [id],
            },
          },
        ]
        return {
          text: '',
          toolCalls,
          modelId: 'synthetic',
          usage: { inputTokens: 0, outputTokens: 0, totalTokens: 0 },
          assistant: { role: 'assistant', content: '', toolCalls },
        } as Completion
      },
    }),
  }
  const progress: CommitProgress = {
    applying: false,
    writePending: false,
    totals: { candidateCount: 0, savedCount: 0, rejectedCount: 0, ignoredCount: 0 },
  }
  return {
    run: () =>
      runLearningBatch({
        sceneId: 's',
        messages: [message],
        signal: new AbortController().signal,
        store: facade,
        ports,
        isDisposed: () => false,
        progress,
      }),
    flip: (r: boolean, p = personal) => {
      state = {
        memoryReadEnabled: r,
        personalizationEnabled: p,
        memoryReadRevision: state.memoryReadRevision + 1,
      }
    },
    onSearch: (f: typeof searching) => (searching = f),
    onQueue: (f: typeof queueEffect) => (queueEffect = f),
    onModel: (f: typeof modelEffect) => (modelEffect = f),
    counts: () => ({ searches, writes, models }),
    close: () => {
      memory.dispose()
      store.close()
    },
  }
}
for (const [name, read, personal] of [
  ['readOFF', false, true],
  ['personalOFF', true, false],
] as const)
  test(`${name}: background fresh-source extraction reaches persistence without old-memory search/reconcile`, async () => {
    const f = fixture(read, personal)
    try {
      assert.equal(await f.run(), true)
      assert.deepEqual(f.counts(), { searches: 0, models: 1, writes: 1 })
    } finally {
      f.close()
    }
  })
test('revocation during old-memory search cancels batch before any model/persist, even after re-enable', async () => {
  const f = fixture()
  try {
    f.onSearch(async () => {
      f.flip(false)
      await Promise.resolve()
      f.flip(true)
      return []
    })
    await assert.rejects(f.run(), /memory_read_revoked/)
    assert.deepEqual(f.counts(), { searches: 1, models: 0, writes: 0 })
  } finally {
    f.close()
  }
})
test('batch which already began old-memory reading cannot downgrade to source-only after model await', async () => {
  const f = fixture()
  try {
    f.onSearch(async () => [])
    f.onModel(async () => {
      f.flip(false)
      await Promise.resolve()
    })
    await assert.rejects(f.run(), /memory_read_revoked/)
    assert.deepEqual(f.counts(), { searches: 1, models: 1, writes: 0 })
  } finally {
    f.close()
  }
})

test('queued physical admission rechecks the original ticket before dispatch', async () => {
  const f = fixture()
  try {
    f.onSearch(async () => [])
    f.onQueue(async () => {
      f.flip(false)
      await Promise.resolve()
      f.flip(true)
    })
    await assert.rejects(f.run(), /memory_read_revoked/)
    assert.deepEqual(f.counts(), { searches: 1, models: 0, writes: 0 })
  } finally {
    f.close()
  }
})
