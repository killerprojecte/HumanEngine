import { retainLegacyProofFormat } from './legacy-memory-proof.ts'
import assert from 'node:assert/strict'
import {
  MemoryService,
  type MemoryCandidate,
  type MemoryContext,
  type MemoryRecord,
  type MemorySource,
} from '../../src/memory.ts'
import { Store } from '../../src/store.ts'
import { memoryScenarioIds as ids } from '../fixtures/memory-scenarios.ts'

/** Exercises the production service and SQLite, with no router, adapter or network. */
export function memoryScenarioHarness(path = ':memory:') {
  const store = new Store(path)
  for (const id of [ids.group, ids.otherGroup, ids.private, ids.otherAccount])
    if (!store.scene(id))
      store.saveScene({
        id,
        kind: id === ids.private ? 'private' : 'group',
        adapterId: 'fixture',
        accountId: id === ids.otherAccount ? 'account-b' : 'account-a',
        platformSceneId: id,
        name: 'Synthetic fixture',
        participationEnabled: false,
        learningEnabled: true,
        proactiveEnabled: false,
      })
  const memory = new MemoryService(store)
  const context = (sceneId: string = ids.group): MemoryContext => ({
    sceneId,
    simulation: false,
  })
  const source = (
    messageId: string,
    senderId: string = ids.lin,
    sceneId: string = ids.group,
  ): MemorySource => ({ sceneId, messageId, senderId })
  function propose(
    content: string,
    sources: MemorySource[],
    patch: Partial<MemoryCandidate> = {},
    parents: string[] = [],
    sceneId: string = sources[0]?.sceneId ?? ids.group,
  ) {
    const result = memory.propose(
      {
        ...context(sceneId),
        provenance: parents.length ? 'contextual' : 'source-only',
        dependencyMemoryIds: parents,
        dependencyParentReferences: parents.flatMap((id) => {
          const ctx = context(sceneId)
          const record = memory.list(ctx).find((item) => item.id === id)
          const reference = record ? memory.captureParentReference(ctx, record) : undefined
          return reference ? [reference] : []
        }),
        sourceEvidence: sources.map((item) => ({ ...item, available: true, isBot: false })),
      },
      { content, kind: 'claim', personId: ids.lin, sources, ...patch },
    )
    // Existing scenario assertions cover legacy databases; current bound proofs
    // are covered independently by memory-parent-integration.test.ts.
    if (result.status === 'saved' && parents.length)
      retainLegacyProofFormat(store, result.memory.id)
    return result
  }
  function saved(result: ReturnType<typeof propose>): MemoryRecord {
    assert.equal(result.status, 'saved', JSON.stringify(result))
    if (result.status !== 'saved') throw new Error('scenario_proposal_not_saved')
    return result.memory
  }
  function snapshot() {
    return JSON.stringify(
      [
        'memories',
        'memory_sources',
        'memory_supports',
        'memory_deleted',
        'memory_vectors',
        'recall_tombstones',
      ].map((table) => [table, store.db.prepare(`SELECT * FROM ${table} ORDER BY rowid`).all()]),
    )
  }
  return { store, memory, context, source, propose, saved, snapshot, close: () => store.close() }
}
