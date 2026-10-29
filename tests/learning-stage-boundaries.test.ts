import assert from 'node:assert/strict'
import test from 'node:test'
import { persistCandidates } from '../src/memory/learning/persistence.ts'
import type {
  BatchStore,
  CommitProgress,
  LearningPorts,
  SourceStageContext,
} from '../src/memory/learning/batch-contracts.ts'
import { buildLearningCandidateSchema } from '../src/memory/learning/contracts.ts'
import { ContextProjection } from '../src/runtime/context-projection.ts'
import { harness, message, proposal, sceneId } from './helpers/learning-technical-fixture.ts'
const progress = (): CommitProgress => ({
  applying: false,
  writePending: false,
  totals: { candidateCount: 0, savedCount: 0, rejectedCount: 0, ignoredCount: 0 },
})
function stage(h: ReturnType<typeof harness>, controller = new AbortController()) {
  const source = message(451, 'The encoder uses fixed-width unsigned integers.')
  const context: SourceStageContext & { store: BatchStore } = {
    sceneId,
    messages: [source],
    signal: controller.signal,
    store: h.store,
    projection: new ContextProjection(sceneId),
    router: {
      complete: async () => {
        throw Error('persistence_must_not_call_model')
      },
    },
    candidateSchema: buildLearningCandidateSchema(false, false),
    relationInstructions: '',
  }
  const ports = (h.engine.learning as unknown as { ports: LearningPorts }).ports
  return { context, source, ports }
}
test('persistence stops before write intent when disposed, learning disabled or cancelled', async () => {
  for (const reason of ['disposed', 'disabled', 'cancelled']) {
    const h = harness(),
      controller = new AbortController(),
      state = progress()
    try {
      const { context, source } = stage(h, controller)
      if (reason === 'disabled')
        h.store.saveScene({ ...h.store.scene(sceneId)!, learningEnabled: false })
      if (reason === 'cancelled') controller.abort()
      const noWrite = () => {
        throw Error('unexpected_write')
      }
      assert.equal(
        await persistCandidates(
          context,
          [proposal('The encoder uses integers.', source)],
          { proposeSource: noWrite, proposeHabit: noWrite },
          () => reason === 'disposed',
          state,
        ),
        false,
      )
      assert.equal(state.applying, false)
      assert.equal(state.writePending, false)
    } finally {
      await h.close()
    }
  }
})
test('write intent precedes await and stays set after rejected candidate then unknown exception', async () => {
  const h = harness(),
    state = progress()
  try {
    const { context, source, ports } = stage(h)
    let writes = 0
    const error = new Error('synthetic_unknown_storage_result')
    await assert.rejects(
      persistCandidates(
        context,
        [proposal('One.', source), proposal('Two.', source)],
        {
          ...ports,
          proposeSource: async () => {
            writes++
            assert.equal(state.applying, true)
            assert.equal(state.writePending, true)
            if (writes === 1) return { status: 'rejected', reason: 'synthetic_rejection' }
            throw error
          },
        },
        () => false,
        state,
      ),
      (value) => value === error,
    )
    assert.equal(writes, 2)
    assert.equal(state.totals.savedCount, 0)
    assert.equal(state.totals.rejectedCount, 1)
    assert.equal(state.applying, true)
    assert.equal(state.writePending, true)
  } finally {
    await h.close()
  }
})
test('committed effect followed by exception or cancellation remains observable; no later sibling writes', async () => {
  for (const outcome of ['throw', 'cancel']) {
    const h = harness(),
      state = progress(),
      controller = new AbortController()
    try {
      const { context, source, ports } = stage(h, controller)
      let writes = 0
      const run = persistCandidates(
        context,
        [
          proposal('The encoder uses fixed-width integers.', source),
          proposal('Second must not be saved.', source),
        ],
        {
          ...ports,
          proposeSource: async (...args) => {
            writes++
            const saved = await ports.proposeSource(...args)
            assert.equal(saved.status, 'saved')
            if (outcome === 'throw') throw Error('synthetic_postcommit_failure')
            controller.abort('source_recalled')
            return saved
          },
        },
        () => false,
        state,
      )
      if (outcome === 'throw') await assert.rejects(run, /synthetic_postcommit_failure/)
      else assert.equal(await run, false)
      assert.equal(writes, 1)
      assert.equal(h.engine.memory.adminList().length, 1)
      assert.equal(state.applying, true)
      assert.equal(state.writePending, outcome === 'throw')
      assert.equal(state.totals.savedCount, outcome === 'throw' ? 0 : 1)
    } finally {
      await h.close()
    }
  }
})
