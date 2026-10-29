import assert from 'node:assert/strict'
import test, { type TestContext } from 'node:test'
import { setImmediate as immediate } from 'node:timers/promises'
import { mkdtempSync, rmSync } from 'node:fs'
import { join } from 'node:path'
import { tmpdir } from 'node:os'
import { Engine } from '../src/engine.ts'
import { Store } from '../src/store.ts'
import { ModelError, type Completion, type Router } from '../src/models.ts'
import type { RuntimeMessage } from '../src/runtime.ts'
import type { SceneRef } from '../src/onebot.ts'
import { fixtureReplyPlan } from './helpers/reply-plan.ts'

const scene: SceneRef = {
  id: 'scheduling-scene',
  kind: 'group',
  adapterId: 'fixture',
  accountId: 'offline-bot',
  platformSceneId: 'synthetic-group',
}
const completion = (toolCalls: Completion['toolCalls'] = []): Completion => ({
  text: '',
  toolCalls,
  modelId: 'scripted-offline',
  usage: { inputTokens: 0, outputTokens: 0, totalTokens: 0 },
  assistant: { role: 'assistant', content: '', toolCalls },
})
const decision = () =>
  completion([
    {
      id: 'silent',
      name: 'participation.decide',
      arguments: { action: 'silent', reasonCode: 'fixture_silent' },
    },
  ])
const message = (id: string): RuntimeMessage => ({
  id,
  sceneId: scene.id,
  senderId: 'fixture-person',
  sentAt: new Date().toISOString(),
  receivedAt: new Date().toISOString(),
  origin: 'live',
  status: 'available',
  parts: [{ type: 'text', text: `Synthetic scheduling input ${id}.` }],
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
function fixture(path = ':memory:', participationEnabled = false, learningQuietMs = 100) {
  const store = new Store(path)
  store.set('settings', {
    ...store.settings,
    debounceMs: 10,
    maxWaitMs: 100,
    learningBatchMs: learningQuietMs,
    learningMaxWaitMs: Math.max(learningQuietMs, 300),
    learningMinIntervalMs: 500,
    learningRetryBaseMs: 100,
  })
  if (!store.scene(scene.id))
    store.saveScene({
      ...scene,
      name: 'Synthetic scheduling',
      participationEnabled,
      learningEnabled: true,
      proactiveEnabled: false,
    })
  const engine = new Engine(store)
  let sends = 0
  engine.runtime.addAdapter('fixture', {
    send: async () => {
      sends++
      throw new Error('Unexpected synthetic transport invocation')
    },
    getMessage: async () => ({ items: [], coverage: 'complete' }),
    history: async () => ({ items: [], coverage: 'complete' }),
    dispose() {},
  })
  const install = (complete: Router['complete']) => {
    engine.router = () => ({ complete }) as Router
  }
  const ingest = (id: string) => engine.runtime.ingest(message(id), scene)
  return {
    store,
    engine,
    install,
    ingest,
    sends: () => sends,
    close: async () => {
      await engine.dispose()
      store.close()
    },
  }
}
function extracted(prompts: Parameters<Router['complete']>[1]) {
  return (JSON.parse(prompts[1]!.content) as { messages: RuntimeMessage[] }).messages
}
const proposal = (source: RuntimeMessage) =>
  completion([
    {
      id: 'candidate',
      name: 'learning.propose',
      arguments: {
        content: 'Synthetic durable preference.',
        kind: 'claim',
        sourceMessageIds: [source.id],
      },
    },
  ])

test('actual received-to-learning path persists a candidate while participation chooses silent, without a transport call', async (t) => {
  clock(t)
  const h = fixture(':memory:', true)
  let decisions = 0,
    extracts = 0
  h.install(async (role, prompts, tools) => {
    if (tools?.some((tool) => tool.name === 'participation.decide')) {
      decisions++
      return decision()
    }
    assert.equal(role, 'learning')
    if (tools?.some((tool) => tool.name === 'learning.propose')) {
      extracts++
      return proposal(extracted(prompts)[0]!)
    }
    return completion()
  })
  try {
    h.ingest('silent-source')
    await advance(t, 10)
    assert.equal(decisions, 1)
    assert.equal(h.engine.runtime.activeTasks, 0)
    assert.equal(extracts, 0)
    await advance(t, 90)
    assert.equal(extracts, 1)
    assert.equal(h.engine.memory.adminList().length, 1)
    assert.equal(h.engine.memory.adminList()[0]!.sources[0]!.messageId, 'silent-source')
    assert.equal(h.sends(), 0)
    h.ingest('silent-source')
    await advance(t, 1000)
    assert.equal(extracts, 1, 'duplicate ingress and an idle timer must not repeat extraction')
  } finally {
    await h.close()
  }
})

test('automatic quiet, maximum-wait and minimum-interval windows batch a flood rather than call per message', async (t) => {
  clock(t)
  const h = fixture()
  const batches: Array<{ at: number; texts: string[] }> = []
  h.install(async (role, prompts) => {
    assert.equal(role, 'learning')
    batches.push({
      at: Date.now(),
      texts: extracted(prompts).map((item) => JSON.stringify(item.parts)),
    })
    return completion()
  })
  try {
    const start = Date.now()
    for (let i = 0; i < 6; i++) {
      h.ingest(`flood-${i}`)
      await advance(t, 50)
      assert.equal(batches.length, i === 5 ? 1 : 0)
    }
    assert.equal(
      batches[0]!.at - start,
      300,
      'continuous traffic reaches max wait without waiting for quiet',
    )
    assert.equal(batches[0]!.texts.length, 6)
    assert.equal(h.engine.learningStatus().completed, 6)
    h.ingest('after-first-batch')
    await advance(t, 100)
    assert.equal(batches.length, 1)
    await advance(t, 399)
    assert.equal(batches.length, 1, 'quiet and max wait do not bypass hard minimum interval')
    await advance(t, 1)
    assert.equal(batches.length, 2)
    assert.equal(batches[1]!.at - batches[0]!.at, 500)
    assert.equal(batches[1]!.texts.length, 1)
    await advance(t, 1000)
    assert.equal(batches.length, 2)
    assert.equal(h.engine.learningStatus().queued, 0)
    assert.equal(h.sends(), 0)
  } finally {
    await h.close()
  }
})

test('active foreground runtime work postpones scheduled learning without starting a background model request', async (t) => {
  clock(t)
  const h = fixture(':memory:', true)
  let release!: () => void,
    extracts = 0,
    decisionStarted = false
  const pending = new Promise<void>((resolve) => {
    release = resolve
  })
  h.install(async (role, _prompts, tools) => {
    if (tools?.some((tool) => tool.name === 'participation.decide')) {
      decisionStarted = true
      await pending
      return decision()
    }
    assert.equal(role, 'learning')
    extracts++
    return completion()
  })
  try {
    h.ingest('foreground-busy')
    await advance(t, 10)
    assert.equal(decisionStarted, true)
    assert.equal(h.engine.runtime.activeTasks, 1)
    await advance(t, 1090)
    assert.equal(extracts, 0)
    assert.equal(h.engine.learningStatus().queued, 1)
    release()
    await advance(t, 0)
    assert.equal(h.engine.runtime.activeTasks, 0)
    await advance(t, 1000)
    assert.equal(extracts, 1)
    assert.equal(h.engine.learningStatus().completed, 1)
    assert.equal(h.sends(), 0)
  } finally {
    release()
    await h.close()
  }
})

test('learning OFF aborts in-flight extraction, discards paused-period ingress and only learns fresh messages after enabling', async (t) => {
  clock(t)
  const h = fixture()
  let release!: () => void,
    signal: AbortSignal | undefined,
    calls = 0
  const pending = new Promise<void>((resolve) => {
    release = resolve
  })
  h.install(async (_role, prompts, _tools, currentSignal) => {
    calls++
    if (calls === 1) {
      signal = currentSignal
      await pending
      return proposal(extracted(prompts)[0]!)
    }
    return completion()
  })
  try {
    h.ingest('before-off')
    await advance(t, 100)
    assert.equal(calls, 1)
    const disabled = { ...h.store.scene(scene.id)!, learningEnabled: false }
    h.store.saveScene(disabled)
    h.engine.sceneChanged(disabled)
    assert.equal(signal?.aborted, true)
    h.ingest('during-off')
    release()
    await advance(t, 1000)
    assert.equal(h.engine.memory.adminList().length, 0)
    assert.equal(h.engine.learningStatus().queued, 0)
    assert.equal(h.engine.learningStatus().active, 0)
    assert.equal(h.engine.learningStatus().retrying, 0)
    const enabled = { ...disabled, learningEnabled: true }
    h.store.saveScene(enabled)
    h.engine.sceneChanged(enabled)
    await advance(t, 1000)
    assert.equal(calls, 1, 'OFF input must not be backfilled on enabling')
    h.ingest('fresh-after-on')
    await advance(t, 100)
    assert.equal(calls, 2)
    assert.equal(h.engine.learningStatus().completed, 1)
  } finally {
    release()
    await h.close()
  }
})

test('queued learning input is not restored from a reopened database and fresh ingress still works', async (t) => {
  clock(t)
  const directory = mkdtempSync(join(tmpdir(), 'he-learning-schedule-'))
  const path = join(directory, 'fixture.sqlite')
  let h = fixture(path)
  let calls = 0
  const complete: Router['complete'] = async () => {
    calls++
    return completion()
  }
  h.install(complete)
  try {
    h.ingest('queued-before-restart')
    assert.equal(h.engine.learningStatus().queued, 1)
    await h.close()
    h = fixture(path)
    h.install(complete)
    await advance(t, 2000)
    assert.equal(calls, 0)
    assert.equal(h.engine.learningStatus().queued, 0)
    h.ingest('new-after-restart')
    await advance(t, 100)
    assert.equal(calls, 1)
    assert.equal(h.engine.learningStatus().completed, 1)
  } finally {
    await h.close()
    rmSync(directory, { recursive: true, force: true })
  }
})

test('a scheduled partial learning write never replays extraction or admits an explicit retry', async (t) => {
  clock(t)
  const h = fixture()
  let calls = 0
  h.install(async (_role, prompts, tools) => {
    if (tools?.some((tool) => tool.name === 'learning.propose')) {
      calls++
      return proposal(extracted(prompts)[0]!)
    }
    return completion()
  })
  h.engine.indexMemory = async () => {
    throw new ModelError('provider_http_503', 503)
  }
  try {
    h.ingest('partial-source')
    await advance(t, 100)
    assert.equal(calls, 1)
    assert.equal(h.engine.memory.adminList().length, 1)
    const failure = h.engine.learningStatus().failures[0]!
    assert.equal(failure.partial, true)
    assert.equal(failure.canRetry, false)
    assert.deepEqual(h.engine.retryLearning(failure.id), { ok: false })
    await advance(t, 5000)
    assert.equal(calls, 1)
    assert.equal(h.engine.learningStatus().retrying, 0)
    assert.equal(h.engine.memory.adminList().length, 1)
    assert.ok(
      h.store.logs.some(
        (entry) =>
          entry.stage === 'learning.batch' &&
          entry.status === 'partial' &&
          entry.reason === 'partial_commit_no_replay',
      ),
    )
  } finally {
    await h.close()
  }
})

test('runtime recall aborts in-flight learning, suppresses the stale candidate and never replays the recalled source', async (t) => {
  clock(t)
  const h = fixture()
  let release!: () => void,
    signal: AbortSignal | undefined,
    calls = 0
  const pending = new Promise<void>((resolve) => {
    release = resolve
  })
  h.install(async (_role, prompts, _tools, currentSignal) => {
    calls++
    signal = currentSignal
    await pending
    return proposal(extracted(prompts)[0]!)
  })
  try {
    h.ingest('recalled-source')
    await advance(t, 100)
    h.engine.runtime.recall(scene.id, 'recalled-source')
    assert.equal(signal?.aborted, true)
    release()
    await advance(t, 1000)
    assert.equal(calls, 1)
    assert.equal(h.engine.memory.adminList().length, 0)
    assert.equal(h.engine.learningStatus().queued, 0)
    assert.equal(h.engine.learningStatus().active, 0)
    assert.equal(h.engine.learningStatus().retrying, 0)
    assert.equal(h.store.recalled(scene.id, 'recalled-source'), true)
  } finally {
    release()
    await h.close()
  }
})

function plannerCandidate(
  prompts: Parameters<Router['complete']>[1],
  action: 'reply' | 'noReply' = 'reply',
) {
  const plan = fixtureReplyPlan(prompts, { action })!
  assert.ok(plan, 'expected the actual runtime Planner contract')
  plan.actions = [
    {
      toolName: 'learning.propose',
      arguments: {
        content: 'Synthetic durable preference.',
        kind: 'claim',
        sourceMessageIds: [plan.contextMessageIds.at(-1)!],
      },
    },
  ]
  return completion([{ id: 'plan', name: 'reply.plan', arguments: plan }])
}
const replyDecision = () =>
  completion([
    {
      id: 'reply-decision',
      name: 'participation.decide',
      arguments: { action: 'reply', reasonCode: 'scripted_candidate' },
    },
  ])

test('queued contextual learning does not block Replyer on indexing, survives normal turn cleanup and deduplicates an identical repeated plan', async (t) => {
  clock(t)
  const h = fixture(':memory:', true, 300000)
  let release!: () => void,
    indexCalls = 0,
    jobSignal: AbortSignal | undefined
  const pending = new Promise<void>((resolve) => {
    release = resolve
  })
  const receipts: Array<{ status: string; jobId?: string }> = []
  h.engine.indexMemory = async (_id, _content, signal) => {
    indexCalls++
    jobSignal = signal
    await pending
  }
  h.install(async (role, prompts, tools) => {
    if (tools?.some((tool) => tool.name === 'participation.decide')) return replyDecision()
    if (tools?.some((tool) => tool.name === 'reply.plan')) return plannerCandidate(prompts)
    assert.equal(role, 'replyer')
    const input = JSON.parse(prompts[1]!.content) as {
      actionReceipts: Array<{ status: string; jobId?: string }>
    }
    receipts.push(...input.actionReceipts)
    return completion()
  })
  try {
    h.ingest('contextual-source')
    await advance(t, 10)
    assert.equal(receipts.length, 1)
    assert.equal(receipts[0]!.status, 'queued', 'the Replyer receipt must not claim saved')
    assert.equal(indexCalls, 0, 'foreground Replyer runs before queued indexing')
    assert.equal(h.engine.memory.adminList().length, 0)
    await advance(t, 100)
    assert.equal(indexCalls, 1)
    assert.equal(
      jobSignal?.aborted,
      false,
      'normal turn_finished does not cancel independent learning',
    )
    const proactive = { ...h.store.scene(scene.id)!, proactiveEnabled: true }
    h.store.saveScene(proactive)
    h.engine.sceneChanged(proactive)
    await h.engine.runtime.proactive(scene.id)
    assert.equal(receipts.length, 2)
    assert.equal(receipts[1]!.status, 'duplicate')
    assert.equal(indexCalls, 1)
    assert.equal(h.engine.contextualLearning.queue.stats().accepted, 1)
    release()
    await advance(t, 0)
    await h.engine.contextualLearning.queue.whenIdle()
    assert.equal(h.engine.memory.adminList().length, 1)
    assert.equal(h.engine.contextualLearning.queue.stats().completed, 1)
    assert.equal(h.sends(), 0)
  } finally {
    release()
    await h.close()
  }
})

for (const cancellation of ['none', 'off', 'recall'] as const)
  test(`noReply may enqueue learning without invoking Replyer; ${cancellation} cancellation respects source authority`, async (t) => {
    clock(t)
    const h = fixture(':memory:', true, 300000)
    let replyerCalls = 0,
      indexCalls = 0
    h.engine.indexMemory = async () => {
      indexCalls++
    }
    h.install(async (role, prompts, tools) => {
      if (tools?.some((tool) => tool.name === 'participation.decide')) return replyDecision()
      if (tools?.some((tool) => tool.name === 'reply.plan'))
        return plannerCandidate(prompts, 'noReply')
      if (role === 'replyer') replyerCalls++
      return completion()
    })
    try {
      h.ingest('no-reply-source')
      await advance(t, 10)
      assert.equal(replyerCalls, 0)
      assert.equal(h.engine.contextualLearning.queue.stats().accepted, 1)
      assert.equal(h.engine.memory.adminList().length, 0)
      if (cancellation === 'off') {
        const disabled = { ...h.store.scene(scene.id)!, learningEnabled: false }
        h.store.saveScene(disabled)
        h.engine.sceneChanged(disabled)
      } else if (cancellation === 'recall') h.engine.runtime.recall(scene.id, 'no-reply-source')
      await advance(t, 100)
      await h.engine.contextualLearning.queue.whenIdle()
      assert.equal(indexCalls, cancellation === 'none' ? 1 : 0)
      assert.equal(h.engine.memory.adminList().length, cancellation === 'none' ? 1 : 0)
      assert.equal(
        h.engine.contextualLearning.queue.stats().cancelled,
        cancellation === 'none' ? 0 : 1,
      )
      assert.equal(replyerCalls, 0)
      assert.equal(h.sends(), 0)
    } finally {
      await h.close()
    }
  })

for (const forbidden of ['habits.propose', 'memes.feedback'] as const)
  test(`noReply does not expand authority to ${forbidden}`, async (t) => {
    clock(t)
    const h = fixture(':memory:', true, 300000)
    let replyerCalls = 0
    h.install(async (role, prompts, tools) => {
      if (tools?.some((tool) => tool.name === 'participation.decide')) return replyDecision()
      if (tools?.some((tool) => tool.name === 'reply.plan')) {
        const plan = fixtureReplyPlan(prompts, {
          action: 'noReply',
          actions: [{ toolName: forbidden, arguments: {} }],
        })!
        return completion([{ id: 'forbidden-plan', name: 'reply.plan', arguments: plan }])
      }
      if (role === 'replyer') replyerCalls++
      return completion()
    })
    try {
      h.ingest('forbidden-no-reply-action')
      await advance(t, 10)
      assert.equal(h.engine.contextualLearning.queue.stats().accepted, 0)
      assert.equal(h.engine.memory.adminList().length, 0)
      assert.equal(replyerCalls, 0)
      assert.equal(h.sends(), 0)
      assert.ok(
        h.store.logs.some((entry) => entry.stage === 'planner' && entry.status === 'failed'),
      )
    } finally {
      await h.close()
    }
  })

test('background source-only extraction and contextual learning share one record without dropping an independent proof', async (t) => {
  clock(t)
  const h = fixture(':memory:', true, 300000)
  const parentSource = {
    sceneId: scene.id,
    messageId: 'parent-evidence',
    senderId: 'fixture-person',
  }
  const parent = h.engine.memory.propose(
    {
      sceneId: scene.id,
      simulation: false,
      provenance: 'source-only',
      sourceEvidence: [{ ...parentSource, available: true, isBot: false }],
    },
    { content: 'Contextual premise.', kind: 'claim', sources: [parentSource] },
  )
  assert.equal(parent.status, 'saved')
  if (parent.status !== 'saved') throw new Error('parent_fixture_failed')
  let planningRounds = 0,
    extracts = 0
  h.install(async (role, prompts, tools) => {
    if (tools?.some((tool) => tool.name === 'participation.decide')) return replyDecision()
    if (tools?.some((tool) => tool.name === 'reply.plan')) {
      if (++planningRounds === 1)
        return completion([
          { id: 'read-parent', name: 'memory.search', arguments: { query: 'Contextual premise' } },
        ])
      return plannerCandidate(prompts, 'noReply')
    }
    assert.equal(role, 'learning')
    if (tools?.some((tool) => tool.name === 'learning.propose')) {
      extracts++
      return proposal(extracted(prompts)[0]!)
    }
    return completion()
  })
  try {
    h.ingest('shared-source')
    await advance(t, 10)
    await advance(t, 100)
    await h.engine.contextualLearning.queue.whenIdle()
    const first = h.engine.memory
      .adminList()
      .filter((item) => item.content === 'Synthetic durable preference.')
    assert.equal(first.length, 1)
    const childId = first[0]!.id
    let supports = JSON.parse(
      (
        h.store.db.prepare('SELECT value FROM memory_supports WHERE memory_id=?').get(childId) as {
          value: string
        }
      ).value,
    ) as Array<{ parents: string[]; sources: Array<{ messageId: string }> }>
    assert.ok(supports.some((proof) => proof.parents.includes(parent.memory.id)))
    await advance(t, 300000)
    assert.equal(extracts, 1, 'the independent background path is not suppressed wholesale')
    assert.equal(
      h.engine.memory.adminList().filter((item) => item.content === first[0]!.content).length,
      1,
    )
    supports = JSON.parse(
      (
        h.store.db.prepare('SELECT value FROM memory_supports WHERE memory_id=?').get(childId) as {
          value: string
        }
      ).value,
    )
    assert.equal(supports.length, 2)
    assert.ok(
      supports.some(
        (proof) => proof.parents.length === 0 && proof.sources[0]?.messageId === 'shared-source',
      ),
    )
    h.engine.memory.recall(scene.id, 'parent-evidence')
    assert.ok(
      h.engine.memory
        .list({ sceneId: scene.id, simulation: false })
        .some((item) => item.id === childId),
    )
    assert.equal(h.sends(), 0)
  } finally {
    await h.close()
  }
})

for (const window of ['planner-completion', 'submission'] as const)
  test(`a parent revision changed at ${window} cannot reuse the model's earlier exposure; a fresh turn can reread it`, async (t) => {
    clock(t)
    const h = fixture(':memory:', true, 300000)
    const parent = h.engine.memory.create({
      scope: { kind: 'scene', sceneId: scene.id },
      content: 'Exposure snapshot premise.',
      kind: 'claim',
    })
    const originalState = h.engine.memory.revisionState(parent.id)
    let rounds = 0,
      indexes = 0,
      changed = false
    const submit = h.engine.contextualLearning.submit.bind(h.engine.contextualLearning)
    const submissions: Array<ReturnType<typeof submit>> = []
    const changeLock = () => {
      const updated = h.engine.memory.update(parent.id, { locked: true })
      assert.equal(updated.content, parent.content, 'content equality must not hide a revision')
      assert.equal(updated.locked, true)
      assert.ok(h.engine.memory.revisionState(parent.id).version > originalState.version)
      changed = true
    }
    h.engine.contextualLearning.submit = (context, input, exposed) => {
      const observed = exposed.find((item) => item.id === parent.id)
      assert.ok(observed, 'the actual memory.search result must carry parent authority')
      if (!changed) {
        assert.equal(observed.version, originalState.version)
        assert.equal(observed.deletionEpoch, originalState.deletionEpoch)
        // Inject the competing write at the public submission boundary, after Runtime's check.
        changeLock()
      } else assert.equal(observed.version, h.engine.memory.revisionState(parent.id).version)
      const result = submit(context, input, exposed)
      submissions.push(result)
      return result
    }
    h.engine.indexMemory = async () => {
      indexes++
    }
    h.install(async (_role, prompts, tools) => {
      if (tools?.some((tool) => tool.name === 'participation.decide')) return replyDecision()
      if (tools?.some((tool) => tool.name === 'reply.plan')) {
        if (++rounds % 2 === 1)
          return completion([
            {
              id: `read-exposure-${rounds}`,
              name: 'memory.search',
              arguments: { query: parent.content },
            },
          ])
        assert.ok(
          prompts.some((item) => item.role === 'tool' && item.content.includes(parent.content)),
          'the scripted model must actually receive the search result before the write',
        )
        if (window === 'planner-completion' && !changed) changeLock()
        return plannerCandidate(prompts, 'noReply')
      }
      return completion()
    })
    try {
      h.ingest('stale-exposure-source')
      await advance(t, 10)
      assert.equal(rounds, 2)
      assert.equal(changed, true)
      assert.equal(h.engine.contextualLearning.queue.stats().accepted, 0)
      assert.equal(submissions.length, window === 'submission' ? 1 : 0)
      if (window === 'submission') assert.equal(submissions[0]!.status, 'rejected')
      await advance(t, 100)
      await h.engine.contextualLearning.queue.whenIdle()
      assert.equal(indexes, 0)
      assert.deepEqual(
        h.engine.memory.adminList().map((item) => item.id),
        [parent.id],
      )

      h.ingest('fresh-exposure-source')
      await advance(t, 10)
      assert.equal(rounds, 4, 'the fresh turn must search again, not reuse the stale exposure')
      assert.equal(submissions.at(-1)!.status, 'queued')
      assert.equal(h.engine.contextualLearning.queue.stats().accepted, 1)
      await advance(t, 100)
      await h.engine.contextualLearning.queue.whenIdle()
      assert.equal(indexes, 1)
      const children = h.engine.memory
        .adminList()
        .filter((item) => item.content === 'Synthetic durable preference.')
      assert.equal(children.length, 1)
      assert.deepEqual(
        children[0]!.sources.map((source) => source.messageId),
        ['fresh-exposure-source'],
      )
      const supports = JSON.parse(
        (
          h.store.db
            .prepare('SELECT value FROM memory_supports WHERE memory_id=?')
            .get(children[0]!.id) as { value: string }
        ).value,
      ) as Array<{ parents: string[] }>
      assert.ok(supports.some((proof) => proof.parents.includes(parent.id)))
      assert.equal(h.engine.contextualLearning.queue.stats().completed, 1)
      assert.equal(h.sends(), 0)
    } finally {
      await h.close()
    }
  })

for (const change of ['update', 'delete'] as const)
  test(`queued contextual work rejects ${change} of an exposed parent before dispatch`, async (t) => {
    clock(t)
    const h = fixture(':memory:', true, 300000)
    const parent = h.engine.memory.create({
      scope: { kind: 'scene', sceneId: scene.id },
      content: 'Versioned contextual premise.',
      kind: 'claim',
    })
    let rounds = 0,
      indexes = 0
    h.engine.indexMemory = async () => {
      indexes++
    }
    h.install(async (_role, prompts, tools) => {
      if (tools?.some((tool) => tool.name === 'participation.decide')) return replyDecision()
      if (tools?.some((tool) => tool.name === 'reply.plan')) {
        if (++rounds === 1)
          return completion([
            {
              id: 'read-versioned-parent',
              name: 'memory.search',
              arguments: { query: 'Versioned contextual premise' },
            },
          ])
        return plannerCandidate(prompts, 'noReply')
      }
      return completion()
    })
    try {
      h.ingest('parent-dependent-source')
      await advance(t, 10)
      assert.equal(h.engine.contextualLearning.queue.stats().accepted, 1)
      if (change === 'update')
        h.engine.memory.update(parent.id, { content: 'Revised contextual premise.' })
      else h.engine.memory.delete(parent.id)
      await advance(t, 100)
      await h.engine.contextualLearning.queue.whenIdle()
      assert.equal(indexes, 0)
      assert.equal(h.engine.contextualLearning.queue.stats().discarded, 1)
      assert.equal(
        h.engine.memory
          .adminList()
          .some((item) => item.content === 'Synthetic durable preference.'),
        false,
      )
      assert.equal(h.sends(), 0)
    } finally {
      await h.close()
    }
  })

test('identical contextual source IDs and content in two scenes never deduplicate across scope or borrow evidence', async (t) => {
  clock(t)
  const h = fixture(':memory:', true, 300000)
  const other: SceneRef = {
    ...scene,
    id: 'other-contextual-scene',
    accountId: 'other-bot',
    platformSceneId: 'other-group',
  }
  h.store.saveScene({
    ...other,
    name: 'Other synthetic scene',
    participationEnabled: true,
    learningEnabled: true,
    proactiveEnabled: false,
  })
  h.install(async (_role, prompts, tools) => {
    if (tools?.some((tool) => tool.name === 'participation.decide')) return replyDecision()
    if (tools?.some((tool) => tool.name === 'reply.plan'))
      return plannerCandidate(prompts, 'noReply')
    return completion()
  })
  try {
    h.ingest('same-platform-source')
    h.engine.runtime.ingest({ ...message('same-platform-source'), sceneId: other.id }, other)
    await advance(t, 10)
    assert.equal(h.engine.contextualLearning.queue.stats().accepted, 2)
    await advance(t, 100)
    await h.engine.contextualLearning.queue.whenIdle()
    const records = h.engine.memory.adminList()
    assert.equal(records.length, 2)
    for (const sceneId of [scene.id, other.id]) {
      const visible = h.engine.memory.list({ sceneId, simulation: false })
      assert.equal(visible.length, 1)
      assert.deepEqual(visible[0]!.scope, { kind: 'scene', sceneId })
      assert.equal(visible[0]!.sources[0]!.sceneId, sceneId)
      assert.equal(visible[0]!.sources[0]!.messageId, 'same-platform-source')
    }
    assert.equal(h.sends(), 0)
  } finally {
    await h.close()
  }
})

for (const cancellation of ['off', 'recall'] as const)
  test(`in-flight contextual ${cancellation} aborts its independent signal without replaying an already committed candidate`, async (t) => {
    clock(t)
    const h = fixture(':memory:', true, 300000)
    let release!: () => void,
      signal: AbortSignal | undefined,
      indexes = 0
    const pending = new Promise<void>((resolve) => {
      release = resolve
    })
    h.engine.indexMemory = async (_id, _content, currentSignal) => {
      indexes++
      signal = currentSignal
      await pending
    }
    h.install(async (_role, prompts, tools) => {
      if (tools?.some((tool) => tool.name === 'participation.decide')) return replyDecision()
      if (tools?.some((tool) => tool.name === 'reply.plan'))
        return plannerCandidate(prompts, 'noReply')
      return completion()
    })
    try {
      h.ingest('active-contextual-source')
      await advance(t, 10)
      await advance(t, 100)
      assert.equal(indexes, 1)
      assert.equal(
        h.engine.memory.adminList().length,
        1,
        'the authoritative write committed before the blocked index',
      )
      if (cancellation === 'off') {
        const disabled = { ...h.store.scene(scene.id)!, learningEnabled: false }
        h.store.saveScene(disabled)
        h.engine.sceneChanged(disabled)
      } else h.engine.runtime.recall(scene.id, 'active-contextual-source')
      assert.equal(signal?.aborted, true)
      release()
      await advance(t, 1000)
      await h.engine.contextualLearning.queue.whenIdle()
      assert.equal(indexes, 1)
      assert.equal(h.engine.contextualLearning.queue.stats().cancelled, 1)
      const current = h.engine.memory.list({ sceneId: scene.id, simulation: false })
      assert.equal(
        current.length,
        cancellation === 'off' ? 1 : 0,
        'OFF is not rollback; source recall must exclude committed derived knowledge',
      )
      assert.equal(h.sends(), 0)
    } finally {
      release()
      await h.close()
    }
  })
