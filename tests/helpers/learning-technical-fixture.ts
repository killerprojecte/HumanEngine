// Authored programmatically for public boundary tests; not derived from conversation captures.
import assert from 'node:assert/strict'
import { Engine } from '../../src/engine.ts'
import { Store } from '../../src/store.ts'
import type { Completion, Router, ToolCall } from '../../src/models.ts'
import type { MemoryCandidate, MemoryContext, MemoryRecord } from '../../src/memory.ts'
import type { RuntimeMessage } from '../../src/runtime.ts'

export const identifier = (n: number) => `testcase-${String(n).padStart(3, '0')}`
export const epoch = new Date(0).toISOString()
export const future = new Date(1000 * 365 * 86_400_000).toISOString()
export const sceneId = identifier(1)
export const subjectId = identifier(2)
export const otherSceneId = identifier(9)
export const scope = (id = sceneId) => ({
  id,
  kind: 'group' as const,
  adapterId: identifier(10),
  accountId: identifier(11),
  platformSceneId: id,
  name: id,
  participationEnabled: false,
  learningEnabled: true,
  proactiveEnabled: false,
})
export const message = (
  n: number,
  text: string,
  senderId = subjectId,
  location = sceneId,
): RuntimeMessage => ({
  id: identifier(n),
  sceneId: location,
  senderId,
  sentAt: epoch,
  receivedAt: epoch,
  origin: 'live',
  status: 'available',
  parts: [{ type: 'text', text }],
})
export const completed = (toolCalls: ToolCall[] = []): Completion => ({
  text: '',
  modelId: identifier(12),
  toolCalls,
  assistant: { role: 'assistant', content: '', toolCalls },
  usage: { inputTokens: 0, outputTokens: 0, totalTokens: 0 },
})
export const call = (name: string, args: Record<string, unknown>, n = 800): ToolCall => ({
  id: identifier(n),
  name,
  arguments: args,
})
export const proposal = (content: string, source: RuntimeMessage, personal = false): ToolCall =>
  call('learning.propose', {
    content,
    kind: 'fact',
    sourceMessageIds: [source.id],
    ...(personal ? { personId: source.senderId } : {}),
  })
export interface LearningAccess {
  learningBatches: Map<string, RuntimeMessage[]>
  learningRetries: Map<string, { due: number }>
  learn(id: string): Promise<void>
  searchMemory(ctx: MemoryContext, query: string): Promise<MemoryRecord[]>
  indexMemory(id: string, content: string): Promise<void>
}
export function harness() {
  const store = new Store(':memory:')
  store.set('settings', {
    ...store.settings,
    learningBatchMs: 300000,
    learningRetryBaseMs: 30000,
    debounceMs: 0,
    maxWaitMs: 100,
  })
  store.saveScene(scope())
  store.saveScene(scope(otherSceneId))
  const engine = new Engine(store)
  const access = engine as unknown as LearningAccess
  const install = (complete: Router['complete']) => {
    const router = engine.router()
    router.complete = complete
    engine.router = () => router
  }
  const seed = (content: string, source: RuntimeMessage, patch: Partial<MemoryCandidate> = {}) => {
    const evidence = { sceneId: source.sceneId, messageId: source.id, senderId: source.senderId }
    const outcome = engine.memory.propose(
      {
        sceneId: source.sceneId,
        simulation: false,
        provenance: 'source-only',
        sourceEvidence: [{ ...evidence, available: true, isBot: false }],
      },
      { content, kind: 'fact', sources: [evidence], ...patch },
    )
    assert.equal(outcome.status, 'saved')
    if (outcome.status !== 'saved') throw Error('fixture_seed_rejected')
    return outcome.memory
  }
  const run = async (messages: RuntimeMessage[]) => {
    assert.ok(messages.every((item) => item.sceneId === sceneId))
    access.learningBatches.set(sceneId, [...messages])
    await access.learn(sceneId)
  }
  return {
    store,
    engine,
    access,
    install,
    seed,
    run,
    close: async () => {
      await engine.dispose()
      store.close()
    },
  }
}
