import { LearningCoordinator } from '../src/memory/learning/coordinator.ts'
import type { LearningPorts } from '../src/memory/learning/batch-contracts.ts'
import type { RuntimeMessage } from '../src/runtime.ts'
import { currentBudgetPriority } from '../src/budget/native-attempt.ts'
import { effectiveModelPriority, withModelPriority } from '../src/scheduler.ts'
import { currentFlowContext } from '../src/diagnostics/flow.ts'
import { sceneId } from './helpers/learning-technical-fixture.ts'
import assert from 'node:assert/strict'
import { createHash } from 'node:crypto'
import test from 'node:test'
import {
  harness,
  message,
  proposal,
  completed,
  call,
} from './helpers/learning-technical-fixture.ts'

// Synthetic baseline captured before decomposition. Normalize only generated observation
// time; source IDs, model/tool order, full prompts and tool schemas remain exact.
const hash = (value: unknown) =>
  createHash('sha256')
    .update(JSON.stringify(value, (key, item) => (key === 'now' ? '<clock>' : item)))
    .digest('hex')
for (const scenario of ['empty', 'repair'] as const)
  test(`learning stage parity: ${scenario}`, async () => {
    const h = harness()
    const source = message(401, 'The encoder uses fixed-width unsigned integers.')
    const requests: unknown[] = []
    try {
      h.install(async (role, prompts, tools) => {
        requests.push({
          role,
          prompts: prompts.map((p) => ({
            ...p,
            content: p.role === 'user' ? JSON.parse(p.content) : p.content,
          })),
          tools,
        })
        if (scenario === 'empty') return completed()
        if (tools?.some((t) => t.name === 'learning.repair'))
          return completed([
            call('learning.repair', {
              candidateIndex: 0,
              content: 'The encoder uses fixed-width unsigned integers.',
            }),
          ])
        return completed([proposal('She configures the encoder.', source, true)])
      })
      await h.run([source])
      const records = h.engine.memory
        .adminList()
        .map((r) => ({ content: r.content, kind: r.kind, scope: r.scope, sources: r.sources }))
      const trace = { requests, records, status: h.engine.learningStatus() }
      assert.equal(
        hash(trace),
        scenario === 'empty'
          ? 'f70b84ef86337aefb363b7d1736d6517538c1cbc29aeec0eb1aa9150e29af8f2'
          : 'f22b387a85eafce1b50b6808777993d38bd806957d54556198a5c31906453010',
      )
      assert.equal(requests.length, scenario === 'empty' ? 1 : 2)
      assert.equal(records.length, scenario === 'empty' ? 0 : 1)
    } finally {
      await h.close()
    }
  })

// These baselines were independently executed against the frozen pre-refactor class
// and the modular class. Dynamic IDs are mapped one-to-one, never collapsed globally.
for (const scenario of ['reconcile', 'partial'] as const)
  test(`learning lifecycle golden: ${scenario}`, async (t) => {
    t.mock.timers.enable({ apis: ['Date'], now: new Date('2026-10-28T12:00:00Z') })
    const h = harness(),
      requests: unknown[] = [],
      writes: unknown[] = [],
      ids = new Map<string, string>()
    let queue: LearningCoordinator | undefined
    const normalize = (value: unknown) =>
      JSON.parse(
        JSON.stringify(value, (key, item) => {
          if (key === 'now') return '<wall-clock>'
          return typeof item === 'string' && ids.has(item) ? ids.get(item) : item
        }),
      )
    const text = 'The encoder uses fixed-width unsigned integers.'
    const source = message(411, text)
    try {
      if (scenario === 'reconcile')
        ids.set(h.seed(text, message(410, text)).id, 'generated-memory-1')
      h.install(async (role, prompts, tools) => {
        const flow = currentFlowContext()
        if (flow.batchId && !ids.has(flow.batchId)) ids.set(flow.batchId, 'generated-batch-1')
        requests.push(
          normalize({
            role,
            prompts: prompts.map((p) => ({
              ...p,
              content: p.role === 'user' ? JSON.parse(p.content) : p.content,
            })),
            tools,
            budgetPriority: currentBudgetPriority(0),
            modelPriority: effectiveModelPriority(0),
            batchId: flow.batchId,
          }),
        )
        if (tools?.some((tool) => tool.name === 'learning.reconcile')) {
          const payload = JSON.parse(prompts[1]!.content)
          return completed([
            call('learning.reconcile', {
              candidateIndex: 0,
              targetId: payload.relatedMemories[0].id,
              action: 'merge',
            }),
          ])
        }
        return completed([proposal(text, source)])
      })
      const original = (h.engine.learning as unknown as { ports: LearningPorts }).ports
      queue = new LearningCoordinator(h.store, {
        ...original,
        proposeSource: async (...args) => {
          writes.push(
            normalize({ kind: 'memory', scene: args[0], input: args[1], sources: args[2] }),
          )
          const result = await original.proposeSource(...args)
          if (scenario === 'partial') throw Error('synthetic_postcommit_failure')
          return result
        },
        proposeHabit: (...args) => {
          writes.push(
            normalize({ kind: 'habit', scene: args[0], input: args[2], sources: args[3] }),
          )
          return original.proposeHabit(...args)
        },
      })
      const internal = queue as unknown as {
        learningBatches: Map<string, RuntimeMessage[]>
        learn(scene: string): Promise<void>
      }
      internal.learningBatches.set(sceneId, [source])
      await withModelPriority(1, () => internal.learn(sceneId))
      const status = queue.learningStatus()
      for (const [index, failure] of status.failures.entries())
        ids.set(failure.id, `generated-failure-${index + 1}`)
      const trace = normalize({ requests, writes, status })
      assert.equal(
        createHash('sha256').update(JSON.stringify(trace)).digest('hex'),
        scenario === 'reconcile'
          ? '64ea3c40ede9a325376fe552cb9c125c48ea850b3cdbdd3d16f67e85ca4ebfbb'
          : '488aa0b296bdbc4b38971dc61e14ab30df03e42c0f55cb12f92f1ffbe7404c40',
      )
      assert.equal(writes.length, 1)
      assert.equal(h.engine.memory.adminList().length, 1)
      if (scenario === 'partial') {
        assert.equal(status.failures[0]!.partial, true)
        assert.equal(status.failures[0]!.canRetry, false)
        assert.deepEqual(queue.retryLearning(status.failures[0]!.id), { ok: false })
        assert.equal(status.retrying, 0)
      }
    } finally {
      queue?.dispose()
      await h.close()
    }
  })
