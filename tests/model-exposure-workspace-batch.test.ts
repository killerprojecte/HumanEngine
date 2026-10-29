import assert from 'node:assert/strict'
import test, { type TestContext } from 'node:test'
import { setImmediate as immediate } from 'node:timers/promises'
import { Engine } from '../src/engine.ts'
import { Store } from '../src/store.ts'
import type { MemoryRecord } from '../src/memory.ts'
import type { ChatMessage, Completion, Router } from '../src/models.ts'
import type { RuntimeExtension, RuntimeMessage, TurnContext } from '../src/runtime.ts'
import type { ModelMemoryExposure } from '../src/memory/read/model-exposure.ts'
import type { WorkingSetItem } from '../src/memory/working-set/contracts.ts'
import type { SceneRef } from '../src/onebot.ts'
import { fixtureReplyPlan } from './helpers/reply-plan.ts'

// Count actual scene authorization calls, not elapsed time or tokens. All model outputs are
// synthetic contracts; these checks do not assess semantic retrieval or production latency.
const scene: SceneRef = {
  id: 'workspace-batch-scene',
  kind: 'group',
  adapterId: 'offline',
  accountId: 'bot',
  platformSceneId: 'synthetic-batch',
}
const personId = 'synthetic-person'
const context = { sceneId: scene.id, simulation: false }
const completion = (toolCalls: Completion['toolCalls'] = []): Completion => ({
  text: '',
  toolCalls,
  modelId: 'offline-batch',
  usage: { inputTokens: 0, outputTokens: 0, totalTokens: 0 },
  assistant: { role: 'assistant', content: '', toolCalls },
})
const call = (name: string, args: Record<string, unknown>) => ({ id: name, name, arguments: args })
const message = (id: string): RuntimeMessage => ({
  id,
  sceneId: scene.id,
  senderId: personId,
  origin: 'live',
  status: 'available',
  sentAt: new Date().toISOString(),
  receivedAt: new Date().toISOString(),
  parts: [{ type: 'text', text: `Synthetic current source ${id}.` }],
})
function fixture(size: number) {
  const store = new Store(':memory:')
  store.set('settings', {
    ...store.settings,
    memoryWorkingSetEnabled: true,
    memoryEpisodesEnabled: true,
    sourceEventRevisionsEnabled: true,
    debounceMs: 10,
    maxWaitMs: 100,
  })
  store.saveScene({
    ...scene,
    name: 'Synthetic batch',
    participationEnabled: false,
    learningEnabled: false,
    proactiveEnabled: false,
  })
  const engine = new Engine(store)
  let models = 0,
    sends = 0
  engine.router = () => {
    models++
    throw Error('Unexpected model invocation')
  }
  engine.indexMemory = async () => {}
  engine.runtime.addAdapter(scene.adapterId, {
    send: async () => {
      sends++
      throw Error('Unexpected platform send')
    },
    getMessage: async () => ({ items: [], coverage: 'complete' }),
    history: async () => ({ items: [], coverage: 'complete' }),
    dispose() {},
  })
  const records: MemoryRecord[] = []
  store.db
    .transaction(() => {
      for (let index = 0; index < size; index++)
        records.push(
          engine.memory.create({
            scope: { kind: 'scene', sceneId: scene.id, personId },
            kind: 'claim',
            epistemicKind: 'hypothesis',
            content: `BATCH_RECORD_${index}: not confirmed; only this occasion.`,
            sources: [
              { sceneId: scene.id, messageId: `manual-source-${index}`, senderId: personId },
            ],
          }),
        )
    })
    .immediate()
  const extension = (
    engine.runtime as unknown as { extensions: RuntimeExtension[] }
  ).extensions.find((value) => value.tools.some((tool) => tool.name === 'memory.person'))!
  const exposure = (engine as unknown as { memoryExposure: ModelMemoryExposure }).memoryExposure
  const list = engine.memory.list.bind(engine.memory)
  let lists = 0,
    batches: number[] = []
  engine.memory.list = (...args) => {
    lists++
    return list(...args)
  }
  const resolve = engine.memory.workspace.resolve.bind(engine.memory.workspace)
  engine.memory.workspace.resolve = (ctx, refs) => {
    batches.push(refs.length)
    return resolve(ctx, refs)
  }
  const turn = (id = crypto.randomUUID()): TurnContext => ({
    scene,
    mode: 'real',
    turnId: id,
    signal: new AbortController().signal,
    dependencies: new Set(),
    validations: [],
    deferMemoryExposure: true,
  })
  return {
    store,
    engine,
    extension,
    exposure,
    records,
    turn,
    reset() {
      lists = 0
      batches = []
    },
    counts() {
      return { lists, batches: [...batches] }
    },
    async person(ctx: TurnContext) {
      return (await extension.execute('memory.person', { personId }, ctx)) as MemoryRecord[]
    },
    deliver(ctx: TurnContext, values: MemoryRecord[]) {
      return extension.delivered!('memory.person', values, ctx) as MemoryRecord[]
    },
    selected(ctx: TurnContext, ids: string[]) {
      extension.contextSelected!(ctx, { evidence: [{ memories: ids.map((id) => ({ id })) }] })
    },
    items() {
      return (
        store.db.prepare('SELECT value FROM memory_working_set_items').all() as { value: string }[]
      ).map((row) => JSON.parse(row.value) as WorkingSetItem)
    },
    truth() {
      return [
        'memories',
        'memory_supports',
        'memory_assertion_revisions',
        'memory_assertion_state',
      ].map((table) => [table, store.db.prepare(`SELECT * FROM ${table}`).all()])
    },
    learned(id: string, content: string) {
      engine.runtime.ingest(message(id), scene, false)
      const live = engine.runtime
        .preview(scene.id)
        .find((item) => item.id === id)! as RuntimeMessage
      store.saveScene({ ...store.scene(scene.id)!, learningEnabled: true })
      const evidence = {
        sceneId: scene.id,
        messageId: live.id,
        senderId: personId,
        eventRef: live.sourceEventRef,
        available: true,
        isBot: false,
        sentAt: live.sentAt,
        receivedAt: live.receivedAt,
      }
      const result = engine.memory.propose(
        { ...context, provenance: 'source-only', sourceEvidence: [evidence] },
        {
          kind: 'experience',
          epistemicKind: 'explicit',
          subject: { kind: 'self', sourceMessageId: id },
          content,
          sources: [evidence],
        },
      )
      store.saveScene({ ...store.scene(scene.id)!, learningEnabled: false })
      assert.equal(result.status, 'saved')
      if (result.status !== 'saved') throw Error('synthetic_OR_fixture_rejected')
      return { live, record: result.memory }
    },
    async close() {
      try {
        await engine.dispose()
      } finally {
        store.close()
      }
      assert.equal(sends, 0)
      assert.equal(models, 0)
    },
  }
}

for (const size of [80, 120])
  test(`actual Engine person delivery keeps all ${size} records with constant scene reads while workspace captures exact 40-record batches`, async (t) => {
    const h = fixture(size)
    try {
      const turn = h.turn()
      h.reset()
      const prepared = await h.person(turn)
      assert.equal(prepared.length, size)
      assert.deepEqual(
        new Set(prepared.map((row) => row.id)),
        new Set(h.records.map((row) => row.id)),
      )
      assert.deepEqual(h.counts(), { lists: 2, batches: [] })
      assert.equal(h.exposure.readExposed(turn).length, 0)
      assert.equal(turn.dependencies.size, 0)
      h.reset()
      const delivered = h.deliver(turn, prepared)
      assert.equal(delivered.length, size)
      assert.deepEqual(h.counts(), { lists: 2, batches: Array(size / 40).fill(40) })
      assert.equal(h.exposure.readExposed(turn).length, size)
      assert.equal(turn.dependencies.size, size)
      h.reset()
      assert.ok(turn.validations.every((check) => check()))
      assert.deepEqual(h.counts(), { lists: 1, batches: [] })
      const before = h.truth()
      h.reset()
      h.selected(
        turn,
        delivered.map((row) => row.id),
      )
      assert.deepEqual(h.counts(), { lists: 0, batches: Array(size / 40).fill(40) })
      assert.equal(h.items().length, size)
      assert.ok(h.items().every((item) => item.contextSelectedCount === 1))
      h.selected(
        turn,
        delivered.map((row) => row.id),
      )
      assert.ok(
        h.items().every((item) => item.contextSelectedCount === 1),
        'same turn and exact ref remains idempotent',
      )
      assert.deepEqual(h.truth(), before)
      t.diagnostic(
        `workspace_exposure_stage_counts=${JSON.stringify({ records: size, prepareSceneReads: 2, deliverSceneReads: 2, validateSceneReads: 1, selectedSceneReads: 0, exactWorkspaceBatches: size / 40 })}`,
      )
    } finally {
      await h.close()
    }
  })

test('actual Runtime hands the full eighty-record person receipt to Planner but counts only the three final retained records', async (t) => {
  t.mock.timers.enable({
    apis: ['Date', 'setTimeout', 'setInterval'],
    now: new Date('2026-10-29T03:00:00Z'),
  })
  const h = fixture(80)
  const replyInputs: ChatMessage[][] = []
  let rounds = 0,
    modelCalls = 0,
    deliveredLength = 0
  const selectedCanonical = [h.records[0]!.id, h.records[40]!.id, h.records[79]!.id]
  let selectedCapture: { ctx: TurnContext; brief: unknown } | undefined
  const selected = h.extension.contextSelected!.bind(h.extension)
  h.extension.contextSelected = (ctx, brief) => {
    selectedCapture = { ctx, brief }
    selected(ctx, brief)
  }
  h.engine.router = () =>
    ({
      complete: async (role, messages, tools = []) => {
        modelCalls++
        if (tools.some((tool) => tool.name === 'participation.decide'))
          return completion([
            call('participation.decide', { action: 'reply', reasonCode: 'synthetic_batch' }),
          ])
        if (tools.some((tool) => tool.name === 'reply.plan')) {
          if (++rounds === 1) return completion([call('memory.person', { personId })])
          const receipt = JSON.parse(
            messages.find((message) => message.role === 'tool')!.content,
          ) as { id: string; value: MemoryRecord[] }
          deliveredLength = receipt.value.length
          const chosen = receipt.value.filter((row) => /^BATCH_RECORD_(0|40|79):/.test(row.content))
          return completion([
            call(
              'reply.plan',
              fixtureReplyPlan(messages, {
                memorySelections: [
                  { evidenceId: receipt.id, memoryIds: chosen.map((row) => row.id) },
                ],
              })!,
            ),
          ])
        }
        assert.equal(role, 'replyer')
        replyInputs.push(structuredClone(messages))
        return completion()
      },
    }) as Router
  try {
    h.store.saveScene({ ...h.store.scene(scene.id)!, participationEnabled: true })
    const before = h.truth()
    h.reset()
    h.engine.runtime.ingest(message('actual-batch-query'), scene)
    t.mock.timers.tick(10)
    for (let i = 0; i < 10; i++) {
      await immediate()
      t.mock.timers.tick(0)
    }
    assert.equal(h.engine.runtime.activeTasks, 0)
    assert.equal(deliveredLength, 80)
    assert.equal(modelCalls, 4)
    assert.equal(replyInputs.length, 1)
    assert.match(replyInputs[0]![1]!.content, /BATCH_RECORD_79: not confirmed; only this occasion/)
    assert.doesNotMatch(replyInputs[0]![1]!.content, /BATCH_RECORD_78:/)
    assert.deepEqual(
      new Set(
        h
          .items()
          .filter((item) => item.contextSelectedCount === 1)
          .map((item) => item.snapshot.ref.id),
      ),
      new Set(selectedCanonical),
    )
    assert.equal(
      h.items().reduce((sum, item) => sum + item.contextSelectedCount, 0),
      3,
    )
    assert.deepEqual(h.truth(), before)
    assert.ok(selectedCapture)
    h.reset()
    selected(selectedCapture.ctx, selectedCapture.brief)
    assert.equal(
      h.items().reduce((sum, item) => sum + item.contextSelectedCount, 0),
      3,
    )
    assert.equal(h.counts().lists, 0)
  } finally {
    await h.close()
  }
})

test('same-body ABA revision on a late sibling rejects the entire 120-record receipt and never upgrades its first snapshot', async () => {
  const h = fixture(120)
  try {
    const ctx = h.turn(),
      prepared = await h.person(ctx),
      tail = prepared.at(-1)!
    const beforeState = h.engine.memory.revisionState(tail.id)
    h.engine.memory.update(tail.id, { locked: true })
    h.engine.memory.update(tail.id, { locked: false })
    assert.ok(h.engine.memory.revisionState(tail.id).version > beforeState.version)
    assert.deepEqual(
      h.engine.memory.list(context).find((row) => row.id === tail.id),
      tail,
    )
    h.reset()
    assert.deepEqual(h.deliver(ctx, prepared), [])
    assert.deepEqual(h.counts(), { lists: 1, batches: [] })
    assert.deepEqual(h.exposure.readExposed(ctx), [])
    assert.equal(ctx.dependencies.size, 0)
    const fresh = h.turn(),
      current = await h.person(fresh)
    assert.equal(h.deliver(fresh, current).length, 120)
    h.engine.memory.update(tail.id, { locked: true })
    h.engine.memory.update(tail.id, { locked: false })
    h.reset()
    assert.equal(
      fresh.validations.every((check) => check()),
      false,
    )
    assert.equal(h.counts().lists, 1)
    assert.deepEqual(
      h.deliver(fresh, await h.person(fresh)),
      [],
      'a new read in the old turn cannot silently replace its first exposure',
    )
  } finally {
    await h.close()
  }
})

test('real source edit changes the current OR proof without assertion revision; batch authorization and workspace hashes both reject old delivery', async () => {
  const h = fixture(80)
  try {
    const a = h.learned('or-a', 'OR_BATCH_ASSERTION'),
      b = h.learned('or-b', 'OR_BATCH_ASSERTION')
    assert.equal(a.record.id, b.record.id)
    const preparedTurn = h.turn(),
      old = await h.person(preparedTurn)
    const exposedTurn = h.turn(),
      exposed = await h.person(exposedTurn)
    assert.equal(h.deliver(exposedTurn, exposed).length, 81)
    const state = h.engine.memory.revisionState(a.record.id)
    assert.equal(
      h.engine.runtime.edit(
        {
          ...a.live,
          parts: [
            { type: 'text', text: 'Corrected first source, not the second independent proof.' },
          ],
        },
        scene,
        {
          editId: 'or-edit',
          order: { kind: 'observed_cas', expectedBase: a.live.sourceEventRef! },
        },
        false,
      ).status,
      'edited',
    )
    assert.deepEqual(h.engine.memory.revisionState(a.record.id), state)
    h.reset()
    assert.deepEqual(h.deliver(preparedTurn, old), [])
    assert.equal(h.counts().lists, 1)
    assert.equal(preparedTurn.dependencies.size, 0)
    assert.deepEqual(h.exposure.readExposed(preparedTurn), [])
    h.reset()
    assert.equal(
      exposedTurn.validations.every((check) => check()),
      false,
    )
    assert.equal(h.counts().lists, 1)
    h.selected(exposedTurn, [a.record.id])
    assert.ok(
      !h
        .items()
        .some((item) => item.snapshot.ref.id === a.record.id && item.contextSelectedCount > 0),
    )
    const fresh = h.turn(),
      rows = await h.person(fresh)
    assert.equal(rows.length, 81)
    assert.deepEqual(
      rows.find((row) => row.id === a.record.id)!.sources.map((source) => source.messageId),
      ['or-b'],
    )
    h.reset()
    assert.equal(h.deliver(fresh, rows).length, 81)
    assert.deepEqual(h.counts(), { lists: 2, batches: [40, 40, 1] })
    assert.equal(fresh.dependencies.has('or-a'), false)
    assert.equal(fresh.dependencies.has('or-b'), true)
    h.selected(fresh, [a.record.id])
    assert.equal(
      h.items().find((item) => item.snapshot.ref.id === a.record.id)?.contextSelectedCount,
      1,
    )
  } finally {
    await h.close()
  }
})

for (const failure of ['throw', 'revision-drift'] as const)
  test(`the third exact-ID workspace callback batch ${failure} cannot partially grant any of 120 prepared parents`, async () => {
    const h = fixture(120)
    try {
      const ctx = h.turn(),
        prepared = await h.person(ctx)
      const original = h.engine.memory.workspace.resolve.bind(h.engine.memory.workspace)
      let calls = 0
      h.engine.memory.workspace.resolve = (scope, refs) => {
        const value = original(scope, refs)
        if (++calls === 3) {
          if (failure === 'throw') throw Error('synthetic_third_workspace_batch_failed')
          h.engine.memory.update(prepared[0]!.id, { locked: true })
          h.engine.memory.update(prepared[0]!.id, { locked: false })
        }
        return value
      }
      h.reset()
      if (failure === 'throw')
        assert.throws(() => h.deliver(ctx, prepared), /synthetic_third_workspace_batch_failed/)
      else assert.deepEqual(h.deliver(ctx, prepared), [])
      h.engine.memory.workspace.resolve = original
      assert.equal(calls >= 3, true)
      assert.equal(h.counts().lists, failure === 'throw' ? 1 : 2)
      assert.deepEqual(h.exposure.readExposed(ctx), [])
      assert.equal(ctx.dependencies.size, 0)
      assert.equal(
        ctx.validations.every((check) => check()),
        false,
      )
      // contextSelected is a trusted post-eligibility callback, not an untrusted tool.
      // The actual Runtime cases below verify that a failed turn cannot invoke it.
      assert.ok(
        h.items().every((item) => item.contextSelectedCount === 0),
        'failed delivery is never a retained context selection',
      )
      const before = h.truth()
      assert.deepEqual(
        await h.extension.execute(
          'learning.propose',
          {
            content: 'MUST_NOT_SAVE_BATCH_FAILURE_CHILD',
            kind: 'claim',
            sourceMessageIds: ['synthetic-child-source'],
          },
          ctx,
        ),
        { status: 'rejected', reason: 'context_ineligible' },
      )
      assert.deepEqual(h.truth(), before)
      assert.equal(h.engine.contextualLearning.queue.stats().accepted, 0)
    } finally {
      await h.close()
    }
  })

for (const failure of ['throw', 'revision-drift'] as const)
  test(`actual Runtime blocks final selection and Replyer after a third workspace batch ${failure}`, async (t) => {
    t.mock.timers.enable({
      apis: ['Date', 'setTimeout', 'setInterval'],
      now: new Date('2026-10-29T03:00:00Z'),
    })
    const h = fixture(120)
    let callbacks = 0,
      selectedCalls = 0,
      decisions = 0,
      plans = 0,
      replies = 0
    let actualContext: TurnContext | undefined
    const delivered = h.extension.delivered!.bind(h.extension)
    h.extension.delivered = (name, value, ctx, input) => {
      actualContext = ctx
      return delivered(name, value, ctx, input)
    }
    const selected = h.extension.contextSelected!.bind(h.extension)
    h.extension.contextSelected = (ctx, brief) => {
      selectedCalls++
      selected(ctx, brief)
    }
    const resolve = h.engine.memory.workspace.resolve.bind(h.engine.memory.workspace)
    h.engine.memory.workspace.resolve = (scope, refs) => {
      const result = resolve(scope, refs)
      if (++callbacks === 3) {
        if (failure === 'throw') throw Error('synthetic_runtime_third_batch_failed')
        h.engine.memory.update(h.records[0]!.id, { locked: true })
        h.engine.memory.update(h.records[0]!.id, { locked: false })
      }
      return result
    }
    h.engine.router = () =>
      ({
        complete: async (role, _messages, tools = []) => {
          if (tools.some((tool) => tool.name === 'participation.decide')) {
            decisions++
            return completion([
              call('participation.decide', {
                action: 'reply',
                reasonCode: 'synthetic_failed_batch',
              }),
            ])
          }
          if (tools.some((tool) => tool.name === 'reply.plan')) {
            plans++
            return completion([call('memory.person', { personId })])
          }
          assert.equal(role, 'replyer')
          replies++
          return completion()
        },
      }) as Router
    try {
      h.store.saveScene({ ...h.store.scene(scene.id)!, participationEnabled: true })
      h.engine.runtime.ingest(message('failed-runtime-batch-query'), scene)
      t.mock.timers.tick(10)
      for (let i = 0; i < 10; i++) {
        await immediate()
        t.mock.timers.tick(0)
      }
      assert.equal(h.engine.runtime.activeTasks, 0)
      assert.ok(callbacks >= 3)
      assert.ok(actualContext)
      assert.deepEqual(h.exposure.readExposed(actualContext), [])
      assert.ok(
        h.records.every((record) =>
          record.sources.every((source) => !actualContext!.dependencies.has(source.messageId)),
        ),
      )
      assert.equal(selectedCalls, 0)
      assert.equal(replies, 0)
      assert.equal(decisions, 1)
      assert.equal(plans, 1, 'failed receipt never reaches a second Planner request')
      assert.ok(h.items().every((item) => item.contextSelectedCount === 0))
      assert.ok(h.store.logs.some((row) => row.stage === 'planner' && row.status === 'failed'))
    } finally {
      await h.close()
    }
  })
