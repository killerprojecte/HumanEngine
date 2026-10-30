import assert from 'node:assert/strict'
import test, { type TestContext } from 'node:test'
import { setImmediate as immediate } from 'node:timers/promises'
import { mkdtempSync, rmSync } from 'node:fs'
import { join } from 'node:path'
import { tmpdir } from 'node:os'
import { Engine } from '../src/engine.ts'
import { modelScheduler } from '../src/scheduler.ts'
import type { CommitmentTimelineEvidence } from '../src/memory/read/commitment-timeline-exposure.ts'
import { Store } from '../src/store.ts'
import { installAdapter, type AdapterContext } from '../src/plugins/adapters.ts'
import type { ChatMessage, Completion, Router, ToolDefinition } from '../src/models.ts'
import type { Message, SceneRef } from '../src/onebot.ts'
import type { RuntimeMessage, RuntimeExtension, TurnContext } from '../src/runtime.ts'
import type {
  MemoryCandidate,
  MemoryContext,
  MemorySource,
} from '../src/memory/domain/contracts.ts'
import type {
  CommitmentCommand,
  CommitmentRef,
  CommitmentTerms,
  CommitmentView,
} from '../src/memory/domain/commitments.ts'
import { fixtureReplyPlan } from './helpers/reply-plan.ts'

// Scripted declarations validate identity, state and provenance contracts, not semantic recognition.
const alice = 'canonical-alice',
  bob = 'canonical-bob'
const call = (name: string, arguments_: Record<string, unknown> = {}, id = name) => ({
  id,
  name,
  arguments: arguments_,
})
const completion = (toolCalls: Completion['toolCalls'] = []): Completion => ({
  text: '',
  toolCalls,
  modelId: 'offline-commitment-script',
  usage: { inputTokens: 0, outputTokens: 0, totalTokens: 0 },
  assistant: { role: 'assistant', content: '', toolCalls },
})
function clock(t: TestContext) {
  t.mock.timers.enable({
    apis: ['Date', 'setTimeout', 'setInterval'],
    now: new Date('2026-10-29T00:00:00Z'),
  })
}
async function advance(t: TestContext, ms = 100) {
  t.mock.timers.tick(ms)
  for (let i = 0; i < 12; i++) {
    await immediate()
    t.mock.timers.tick(0)
  }
}
const terms = (
  hour = '18:00; date and timezone unknown',
  participants = [alice, bob],
): CommitmentTerms => ({
  kind: 'plan',
  title: 'SYNTHETIC_SAME_DISPLAY_TITLE',
  participantIds: participants,
  temporalScope: 'future',
  polarity: 'positive',
  modality: 'asserted',
  conditions: [],
  timeQualification: hour,
})
function fixture() {
  const root = mkdtempSync(join(tmpdir(), 'he-commit-timeline-runtime-')),
    path = join(root, 'synthetic.sqlite')
  let store: Store,
    engine: Engine,
    context: AdapterContext,
    registration: ReturnType<typeof installAdapter>
  let scene: SceneRef,
    other: SceneRef,
    sends = 0,
    models = 0,
    extracts = 0
  let extract = (_messages: RuntimeMessage[]): Record<string, unknown>[] => []
  let planner: ((messages: ChatMessage[], tools: ToolDefinition[]) => Completion) | undefined
  const replyInputs: ChatMessage[][] = [],
    offered: ToolDefinition[][] = [],
    executionErrors: unknown[] = [],
    modelErrors: unknown[] = []
  const open = () => {
    store = new Store(path)
    if (!store.get('commitment-test-initialized', false)) {
      store.set('settings', {
        ...store.settings,
        memoryCommitmentsEnabled: true,
        memoryCommitmentsAnswerEnabled: true,
        memoryHistoricalReadEnabled: true,
        sourceEventRevisionsEnabled: true,
        debounceMs: 10,
        maxWaitMs: 100,
        learningBatchMs: 300000,
        learningMaxWaitMs: 300000,
        learningMinIntervalMs: 0,
      })
      store.set('commitment-test-initialized', true)
    }
    engine = new Engine(store)
    registration = installAdapter(
      'commitment-fixture',
      (ctx) => {
        context = ctx
        return {
          send: async () => {
            sends++
            return { status: 'sent' as const, messageId: ctx.messageId(scene, `bot-${sends}`) }
          },
          history: async () => ({ items: [], coverage: 'complete' as const }),
          getMessage: async () => ({ items: [], coverage: 'complete' as const }),
          dispose() {},
        }
      },
      engine.runtime,
      store,
      new AbortController().signal,
      {},
    )
    scene = context.scene('synthetic-account', 'group', 'synthetic-group')
    other = context.scene('synthetic-account', 'private', 'synthetic-private')
    for (const s of [scene, other])
      if (!store.scene(s.id))
        store.saveScene({
          ...s,
          name: 'Synthetic commitments',
          participationEnabled: false,
          learningEnabled: true,
          proactiveEnabled: false,
        })
    engine.indexMemory = async () => {}
    const extension = (
      engine.runtime as unknown as { extensions: RuntimeExtension[] }
    ).extensions.find((e) => e.tools.some((t) => t.name === 'memory.search'))!
    const execute = extension.execute.bind(extension)
    extension.execute = async (...args) => {
      try {
        return await execute(...args)
      } catch (error) {
        executionErrors.push(error)
        throw error
      }
    }
    engine.router = (_config, admit) =>
      ({
        complete: async (role, messages, tools = []) => {
          admit?.()
          models++
          offered.push(structuredClone(tools))
          if (tools.some((t) => t.name === 'participation.decide'))
            return completion([
              call('participation.decide', { action: 'reply', reasonCode: 'synthetic_commitment' }),
            ])
          if (tools.some((t) => t.name === 'reply.plan')) {
            assert.ok(planner)
            try {
              return planner(messages, tools)
            } catch (error) {
              modelErrors.push(error)
              throw error
            }
          }
          if (role === 'replyer') {
            replyInputs.push(structuredClone(messages))
            return completion()
          }
          assert.equal(role, 'learning')
          if (tools.some((t) => t.name === 'learning.propose')) {
            extracts++
            return completion(
              extract(JSON.parse(messages[1]!.content).messages).map((p, i) =>
                call('learning.propose', p, `learn-${i}`),
              ),
            )
          }
          return completion()
        },
      }) as Router
  }
  open()
  const message = (
    id: string,
    senderId = alice,
    target = scene,
    body = `Synthetic declaration ${id}.`,
  ): RuntimeMessage => ({
    id: context.messageId(target, id),
    sceneId: target.id,
    senderId,
    senderDisplayName: 'Same nickname',
    origin: 'live',
    status: 'available',
    sentAt: new Date().toISOString(),
    receivedAt: new Date().toISOString(),
    parts: [
      { type: 'text', text: body },
      { type: 'mention', userId: senderId === alice ? bob : alice },
    ],
  })
  const ctx = (target = scene): MemoryContext => ({
    sceneId: target.id,
    simulation: false,
    provenance: 'source-only',
  })
  const stateContext = (ref?: CommitmentRef, target = scene) => ({
    ...ctx(target),
    provenance: ref ? ('contextual' as const) : ('source-only' as const),
    ...(ref
      ? {
          exposedRefs: [
            {
              ...ref,
              mutationVersion: engine.memory.commitments.storage.head(ref.id)!.mutationVersion,
            },
          ],
        }
      : {}),
  })
  const save = (
    id: string,
    person: string,
    command?: CommitmentCommand,
    options: {
      content?: string
      sources?: RuntimeMessage[]
      target?: SceneRef
      patch?: Partial<MemoryCandidate>
      exposed?: ReturnType<typeof stateContext>['exposedRefs']
    } = {},
  ) => {
    const target = options.target ?? scene,
      sources = options.sources ?? [
        (() => {
          const input = message(id, person, target)
          context.receive(input as Message, target, false)
          return engine.runtime.preview(target.id).find((m) => m.id === input.id)! as RuntimeMessage
        })(),
      ]
    const source = (m: RuntimeMessage): MemorySource => ({
      sceneId: m.sceneId,
      messageId: m.id,
      senderId: m.senderId,
      ...(m.sourceEventRef ? { eventRef: m.sourceEventRef } : {}),
    })
    const input: MemoryCandidate = {
      content: options.content ?? `SYNTHETIC_ASSERTION_${id}`,
      kind: 'fact',
      epistemicKind: 'explicit',
      subject: { kind: 'self', sourceMessageId: sources[0]!.id },
      sources: sources.map(source),
      ...(command ? { commitment: command } : {}),
      ...options.patch,
    }
    const result = engine.memory.propose(
      {
        ...ctx(target),
        sourceEvidence: sources.map((m) => ({
          ...source(m),
          available: true,
          isBot: false,
          sourceText: m.parts
            .filter((p) => p.type === 'text')
            .map((p) => p.text)
            .join('\n'),
          sentAt: m.sentAt,
          receivedAt: m.receivedAt,
          mentions: m.parts.flatMap((p, partIndex) =>
            p.type === 'mention' ? [{ partIndex, userId: p.userId }] : [],
          ),
        })),
        ...(command && command.kind !== 'propose'
          ? {
              provenance: 'contextual',
              dependencyMemoryIds: [],
              dependencyParentReferences: [],
              exposedCommitments: options.exposed ?? stateContext(command.target).exposedRefs,
            }
          : {}),
      },
      input,
    )
    assert.equal(result.status, 'saved', JSON.stringify(result))
    if (result.status !== 'saved') throw Error('fixture_assertion_rejected')
    return result
  }
  return {
    get store() {
      return store
    },
    get engine() {
      return engine
    },
    get scene() {
      return scene
    },
    get other() {
      return other
    },
    get adapter() {
      return context
    },
    root,
    path,
    ctx,
    stateContext,
    message,
    save,
    replyInputs,
    offered,
    rows: () => engine.memory.commitments.list(stateContext()).items,
    current: (ref: CommitmentRef) => engine.memory.commitments.current(stateContext(), ref)!,
    receive(m: RuntimeMessage, target = scene) {
      context.receive(m as Message, target, false)
      return engine.runtime.preview(target.id).find((p) => p.id === m.id)! as RuntimeMessage
    },
    setExtract(fn: typeof extract) {
      extract = fn
    },
    settings(patch: Partial<Store['settings']>) {
      store.set('settings', { ...store.settings, ...patch })
      engine.settingsChanged()
    },
    sceneFlags(patch: Partial<NonNullable<ReturnType<Store['scene']>>>) {
      const s = { ...store.scene(scene.id)!, ...patch }
      store.saveScene(s)
      engine.sceneChanged(s)
    },
    extension() {
      return (engine.runtime as unknown as { extensions: RuntimeExtension[] }).extensions.find(
        (e) => e.tools.some((t) => t.name === 'memory.search'),
      )!
    },
    turn(m: RuntimeMessage, deferMemoryExposure = false): TurnContext {
      return {
        scene: store.scene(m.sceneId)!,
        mode: 'real',
        turnId: 'synthetic-direct-turn',
        signal: new AbortController().signal,
        dependencies: new Set([m.id]),
        validations: [],
        sourceSnapshots: structuredClone(engine.runtime.preview(m.sceneId)),
        deferMemoryExposure,
      } as TurnContext
    },
    async ask(
      t: TestContext,
      fn: NonNullable<typeof planner>,
      m = message(`query-${models}`),
      requireIdle = true,
    ) {
      planner = fn
      store.set('settings', {
        ...store.settings,
        learningBatchMs: 300000,
        learningMaxWaitMs: 300000,
      })
      engine.settingsChanged()
      store.saveScene({ ...store.scene(scene.id)!, participationEnabled: true })
      engine.runtime.ingest(m, scene, true)
      await advance(t, 10)
      await advance(t, 100)
      await engine.contextualLearning.queue.whenIdle()
      if (requireIdle) assert.equal(engine.runtime.activeTasks, 0)
      assert.deepEqual(executionErrors, [])
      assert.deepEqual(modelErrors, [])
    },
    stats: () => ({ sends, models, extracts }),
    async reopen() {
      registration.dispose()
      await engine.dispose()
      store.close()
      open()
    },
    async close() {
      registration.dispose()
      try {
        await engine.dispose()
      } finally {
        store.close()
        rmSync(root, { recursive: true, force: true })
      }
    },
  }
}
function saved(result: ReturnType<ReturnType<typeof fixture>['save']>): CommitmentView {
  assert.equal(result.commitment?.status, 'saved', JSON.stringify(result.commitment))
  assert.ok(result.commitment.item)
  return result.commitment.item
}

type Harness = ReturnType<typeof fixture>
let readSequence = 0
const receipt = (messages: ChatMessage[]) =>
  JSON.parse(messages.filter((m) => m.role === 'tool').at(-1)!.content) as {
    id: string
    toolName: string
    value: any[]
    coverage?: { status: string; omittedRecords: number }
  }
const plan = (messages: ChatMessage[], patch: Parameters<typeof fixtureReplyPlan>[1] = {}) =>
  completion([call('reply.plan', fixtureReplyPlan(messages, patch)!)])
function sequence(t: TestContext, h: Harness) {
  const recorded: string[] = []
  const record = (id: string, person: string, command: CommitmentCommand) => {
    t.mock.timers.tick(1000)
    recorded.push(new Date().toISOString())
    return saved(h.save(id, person, command))
  }
  const a18 = record('a18', alice, {
    kind: 'propose',
    terms: terms('18:00'),
  })
  const b18 = record('b18', bob, { kind: 'confirm', target: a18.ref })
  const a19 = record('a19', alice, {
    kind: 'amend',
    target: b18.ref,
    terms: terms('19:00'),
  })
  const b19 = record('b19', bob, { kind: 'confirm', target: a19.ref })
  return { a18, b18, a19, b19, recorded }
}
async function exposed(h: Harness, ref: CommitmentRef, defer = false) {
  const query = h.receive(h.message(`history-read-${++readSequence}`)),
    ctx = h.turn(query, defer)
  const values = (await h.extension().execute('memory.commitments', {}, ctx)) as Array<{
    view: CommitmentView
  }>
  assert.ok(values.some((v) => v.view.ref.id === ref.id && v.view.ref.version === ref.version))
  if (defer) h.extension().delivered?.('memory.commitments', values, ctx)
  return ctx
}
async function timeline(
  h: Harness,
  ref: CommitmentRef,
  input: { asOfRecorded?: string; limit?: number } = {},
) {
  const ctx = await exposed(h, ref)
  const values = (await h
    .extension()
    .execute(
      'memory.commitmentTimeline',
      { ...ref, ...input },
      ctx,
    )) as CommitmentTimelineEvidence[]
  assert.equal(values.length, 1)
  return values[0]!
}

test('two canonical people create four actual recorded declaration transitions; as-of uses server recording time, not occurrence or today confirmation', async (t) => {
  clock(t)
  const h = fixture()
  try {
    const s = sequence(t, h),
      complete = await timeline(h, s.b19.ref)
    assert.equal(complete.stateTime, 'server_recorded_not_occurred')
    assert.equal(complete.interpretation, 'previously_recorded_declaration_state_not_current_truth')
    assert.equal(complete.sourceBodyCoverage, 'unavailable')
    assert.deepEqual(
      complete.recordedStates.map((r) => r.recordedAt),
      s.recorded,
    )
    assert.deepEqual(
      complete.recordedStates.map((r) => r.mutationVersion),
      [1, 2, 3, 4],
    )
    const initial = (await timeline(h, s.b19.ref, { asOfRecorded: s.recorded[0] }))
      .recordedStates[0]!
    assert.equal(initial.states[0]!.confirmation, 'proposed')
    assert.deepEqual(initial.states[0]!.confirmedParticipantIds, [alice])
    assert.deepEqual(initial.states[0]!.missingParticipantIds, [bob])
    const eighteen = (await timeline(h, s.b19.ref, { asOfRecorded: s.recorded[1] }))
      .recordedStates[0]!
    assert.equal(eighteen.states[0]!.confirmation, 'confirmed')
    assert.equal(eighteen.states[0]!.terms!.timeQualification, '18:00')
    const pending = (await timeline(h, s.b19.ref, { asOfRecorded: s.recorded[2] }))
      .recordedStates[0]!
    assert.equal(pending.activeVersion, 1)
    assert.deepEqual(
      pending.states.map((v) => v.confirmation),
      ['confirmed', 'proposed'],
    )
    assert.equal(complete.recordedStates.at(-1)!.activeVersion, 2)
    assert.ok(
      complete.recordedStates
        .flatMap((r) => r.states)
        .every((v) => v.terms!.occurredAt === undefined),
    )
    assert.deepEqual(
      (await timeline(h, s.b19.ref, { asOfRecorded: '2020-01-01T00:00:00Z' })).recordedStates,
      [],
    )
    const persisted = JSON.stringify(h.engine.memory.commitments.temporal.read(s.b19.ref.id))
    await h.reopen()
    assert.equal(JSON.stringify(h.engine.memory.commitments.temporal.read(s.b19.ref.id)), persisted)
    assert.equal(h.stats().models, 0, 'history recording and restart do not invoke a model')
  } finally {
    await h.close()
  }
})

test('actual Planner and Replyer receive the complete qualified temporal page with closed short refs and no invented occurrence time', async (t) => {
  clock(t)
  const h = fixture()
  try {
    const s = sequence(t, h)
    let rounds = 0
    await h.ask(t, (messages) => {
      if (++rounds === 1) return completion([call('memory.commitments')])
      const read = receipt(messages)
      if (rounds === 2)
        return completion([
          call('memory.commitmentTimeline', {
            ...read.value.find((v) => v.view.ref.version === 2).view.ref,
          }),
        ])
      assert.equal(read.value[0].stateKind, 'commitment_timeline')
      assert.deepEqual(
        read.value[0].recordedStates.map((r: any) => r.recordedAt),
        s.recorded,
      )
      return plan(messages, { stateEvidenceIds: [read.id] })
    })
    assert.equal(rounds, 3)
    assert.equal(h.replyInputs.length, 1)
    const wire = h.replyInputs[0]![1]!.content
    const page = JSON.parse(wire).evidence.find(
      (e: any) => e.toolName === 'memory.commitmentTimeline',
    ).states[0]
    assert.equal(page.recordedStates.length, 4)
    assert.match(wire, /server_recorded_not_occurred/)
    assert.match(wire, /declared_not_verified/)
    assert.match(wire, /previously_recorded_declaration_state_not_current_truth/)
    assert.ok(
      page.recordedStates
        .flatMap((r: any) => r.states)
        .every((v: any) => v.ref.id === page.anchor.id && !v.terms.occurredAt),
    )
    assert.ok(!wire.includes(s.b19.ref.id))
    assert.doesNotMatch(wire, /termsHash|fingerprint|proofId|qualificationHash/)
    assert.equal(h.stats().sends, 0)
  } finally {
    await h.close()
  }
})

test('new mutation invalidates an actual first head snapshot even when the terms ID and version are unchanged; a fresh read may inspect the new state', async (t) => {
  clock(t)
  const h = fixture()
  try {
    const first = saved(h.save('first-proposal', alice, { kind: 'propose', terms: terms() }))
    const ctx = await exposed(h, first.ref, true)
    const prepared = (await h
      .extension()
      .execute('memory.commitmentTimeline', { ...first.ref }, ctx)) as CommitmentTimelineEvidence[]
    assert.equal(prepared.length, 1)
    const head = h.engine.memory.commitments.storage.head(first.ref.id)!.mutationVersion
    t.mock.timers.tick(1000)
    saved(h.save('new-confirmation', bob, { kind: 'confirm', target: first.ref }))
    assert.ok(h.engine.memory.commitments.storage.head(first.ref.id)!.mutationVersion > head)
    assert.deepEqual(h.extension().delivered?.('memory.commitmentTimeline', prepared, ctx), [])
    assert.ok(ctx.validations.some((valid) => !valid()))
    assert.deepEqual(
      await h.extension().execute('memory.commitmentTimeline', { ...first.ref }, ctx),
      [],
    )
    assert.equal(
      (await timeline(h, first.ref)).recordedStates.at(-1)!.states[0]!.confirmation,
      'confirmed',
    )
  } finally {
    await h.close()
  }
})

test('timeline authority is whole-object and scene-bound; copies and simulation grant no history-derived learning authority', async (t) => {
  clock(t)
  const h = fixture()
  try {
    const s = sequence(t, h),
      ctx = await exposed(h, s.b19.ref, true)
    const prepared = (await h
      .extension()
      .execute('memory.commitmentTimeline', { ...s.b19.ref }, ctx)) as CommitmentTimelineEvidence[]
    assert.equal(prepared.length, 1)
    assert.deepEqual(
      h.extension().delivered?.('memory.commitmentTimeline', structuredClone(prepared), ctx),
      [],
    )
    const foreign = { ...ctx, scene: h.other, dependencies: new Set<string>(), validations: [] }
    assert.deepEqual(h.extension().delivered?.('memory.commitmentTimeline', prepared, foreign), [])
    assert.deepEqual(
      await h
        .extension()
        .execute('memory.commitmentTimeline', { ...s.b19.ref }, { ...ctx, mode: 'simulation' }),
      [],
    )
    assert.equal(
      (h.extension().delivered?.('memory.commitmentTimeline', prepared, ctx) as unknown[]).length,
      1,
    )
    const source = h.receive(
      h.message('fresh-learning', alice, h.scene, 'Independent fresh personal assertion.'),
    )
    const candidate = {
      content: 'Independent fresh personal assertion.',
      kind: 'fact',
      epistemicKind: 'explicit',
      subject: { kind: 'self', sourceMessageId: source.id },
      sourceMessageIds: [source.id],
    }
    const direct = await h.extension().execute('learning.propose', candidate, ctx)
    assert.deepEqual(direct, {
      status: 'rejected',
      reason: 'historical_read_not_learning_authority',
    })
    const fresh = (await h.extension().execute('learning.propose', candidate, h.turn(source))) as {
      status: string
    }
    assert.equal(fresh.status, 'queued')
    await h.engine.contextualLearning.queue.whenIdle()
    assert.ok(h.engine.memory.list(h.ctx()).some((m) => m.content === candidate.content))
  } finally {
    await h.close()
  }
})

test('recalling the 19:00 confirmation makes current state incomplete and physically redacts its past confirmation without falling back to 18:00', async (t) => {
  clock(t)
  const h = fixture()
  try {
    const s = sequence(t, h),
      id = h.message('b19', bob).id
    t.mock.timers.tick(1000)
    h.engine.runtime.recall(h.scene.id, id)
    const current = h.rows().find((v) => v.ref.version === 2)!
    assert.equal(current.confirmation, 'incomplete')
    const old = await timeline(h, current.ref, { asOfRecorded: s.recorded[3] })
    assert.equal(old.recordedStates.length, 1)
    assert.equal(old.recordedStates[0]!.recordedAt, s.recorded[3])
    assert.equal(old.recordedStates[0]!.status, 'redacted')
    assert.deepEqual(old.recordedStates[0]!.states, [])
    assert.ok(old.reasons.includes('privacy_or_proof_unavailable'))
    const earlier = await timeline(h, current.ref, { asOfRecorded: s.recorded[1] })
    assert.equal(earlier.recordedStates[0]!.states[0]!.terms!.timeQualification, '18:00')
    const physical = JSON.stringify(
      h.store.db
        .prepare('SELECT value FROM memory_commitment_transitions WHERE id=?')
        .all(current.ref.id),
    )
    assert.ok(
      !physical.includes(id),
      'withdrawn source identity must not remain in retained temporal evidence',
    )
    await h.reopen()
    const after = await timeline(h, current.ref, { asOfRecorded: s.recorded[3] })
    assert.deepEqual(after.recordedStates[0]!.states, [])
    assert.equal(h.stats().models, 0)
  } finally {
    await h.close()
  }
})

test('ordinary source edits remove current eligibility but do not rewrite or privacy-purge recorded declaration snapshots', async (t) => {
  clock(t)
  const h = fixture()
  try {
    const s = sequence(t, h),
      old = h.engine.runtime
        .preview(h.scene.id)
        .find((m) => m.id === h.message('b19', bob).id)! as RuntimeMessage
    const before = JSON.stringify(h.engine.memory.commitments.temporal.read(s.b19.ref.id))
    const edited = h.engine.runtime.edit(
      {
        ...old,
        parts: [{ type: 'text', text: 'New different declaration; not a privacy recall.' }],
      },
      h.scene,
      { editId: 'actual-edit', order: { kind: 'observed_cas', expectedBase: old.sourceEventRef! } },
      false,
    )
    assert.equal(edited.status, 'edited')
    assert.equal(JSON.stringify(h.engine.memory.commitments.temporal.read(s.b19.ref.id)), before)
    const current = h.rows().find((v) => v.ref.version === 2)!
    assert.equal(current.confirmation, 'incomplete')
    const historical = await timeline(h, current.ref, { asOfRecorded: s.recorded[3] })
    assert.equal(historical.recordedStates[0]!.states[0]!.confirmation, 'confirmed')
  } finally {
    await h.close()
  }
})

test('legacy unknown and bounded result coverage are explicit at the actual tool boundary, never fabricated complete history', async (t) => {
  clock(t)
  const h = fixture()
  try {
    const first = saved(h.save('legacy-proposal', alice, { kind: 'propose', terms: terms() }))
    h.store.db.exec(
      'DROP TABLE memory_commitment_transitions; DROP TABLE memory_commitment_temporal_windows',
    )
    const old = await timeline(h, first.ref)
    assert.deepEqual(old.recordedStates, [])
    assert.ok(old.reasons.includes('legacy_history_unknown'))
    t.mock.timers.tick(1000)
    saved(h.save('first-new-mutation', bob, { kind: 'confirm', target: first.ref }))
    t.mock.timers.tick(1000)
    saved(h.save('second-new-mutation', alice, { kind: 'confirm', target: first.ref }))
    const narrow = await timeline(h, first.ref, { limit: 1 })
    assert.equal(narrow.recordedStates.length, 1)
    assert.equal(narrow.window.firstMutationVersion, 2)
    assert.equal(narrow.window.maxTransitions, 64)
    assert.ok(narrow.reasons.includes('legacy_history_unknown'))
    assert.ok(narrow.reasons.includes('result_limit'))
    assert.equal(narrow.coverage, 'limited')
    const ctx = await exposed(h, first.ref)
    await assert.rejects(() =>
      h.extension().execute('memory.commitmentTimeline', { ...first.ref, limit: 21 }, ctx),
    )
  } finally {
    await h.close()
  }
})

for (const stage of ['planner', 'replyer'] as const)
  test(`${stage} budget omits a whole temporal page and its dependent facts rather than truncating declaration qualifications`, async (t) => {
    clock(t)
    const h = fixture()
    // Fit the fixed envelope with trusted scope metadata, but not the complete temporal page.
    const replyerBudget = 8192
    try {
      const long = '完整条件_ONLY_THIS_NOT_CONFIRMED_'.repeat(32)
      const first = saved(
        h.save('long-proposal', alice, {
          kind: 'propose',
          terms: { ...terms(), conditions: [long] },
        }),
      )
      let latest = first
      for (let n = 0; n < 5; n++) {
        t.mock.timers.tick(1000)
        latest = saved(
          h.save(`long-amend-${n}`, alice, {
            kind: 'amend',
            target: latest.ref,
            terms: { ...terms(), conditions: [long] },
          }),
        )
      }
      h.settings({
        replySuggestionsEnabled: true,
        ...(stage === 'planner'
          ? { plannerEvidenceMaxBytes: 30000 }
          : { replyEvidenceMaxBytes: replyerBudget }),
      })
      let rounds = 0
      await h.ask(t, (messages) => {
        if (++rounds === 1) return completion([call('memory.commitments')])
        const read = receipt(messages)
        if (rounds === 2) {
          assert.ok(read.value.length)
          return completion([call('memory.commitmentTimeline', { ...read.value[0].view.ref })])
        }
        if (stage === 'planner') {
          assert.deepEqual(read.value, [])
          assert.equal(read.coverage!.status, 'limited')
          return plan(messages, {
            replySuggestion: 'Missing timeline means nothing ever happened.',
          })
        }
        assert.equal(read.value.length, 1)
        assert.ok(Buffer.byteLength(JSON.stringify(read.value), 'utf8') > replyerBudget)
        return plan(messages, {
          stateEvidenceIds: [read.id],
          facts: [{ text: 'PRIVATE_TEMPORAL_CONCLUSION', evidenceIds: [read.id] }],
          replySuggestion: 'PRIVATE_TEMPORAL_CONCLUSION',
        })
      })
      assert.equal(h.replyInputs.length, 1)
      const wire = h.replyInputs[0]![1]!.content,
        result = JSON.parse(wire)
      assert.equal(result.memoryEvidenceCoverage.status, 'omitted')
      assert.doesNotMatch(
        wire,
        /PRIVATE_TEMPORAL_CONCLUSION|Missing timeline means nothing ever happened|ONLY_THIS_NOT_CONFIRMED/,
      )
      assert.ok(!result.evidence.some((e: any) => e.toolName === 'memory.commitmentTimeline'))
      if (stage === 'replyer') {
        assert.ok(Buffer.byteLength(wire, 'utf8') <= replyerBudget)
        assert.deepEqual(result.plan.facts, [])
        assert.equal(result.plan.replySuggestion, undefined)
        assert.deepEqual(
          result.coverageByEvidence.find((e: any) => e.toolName === 'memory.commitmentTimeline')
            .retrievalCoverage,
          { scope: 'retained_history_window', historyCoverage: 'not_established' },
        )
      }
    } finally {
      await h.close()
    }
  })

for (const change of ['head-mutation', 'read-aba'] as const)
  test(`an actual delivered temporal page becomes ineligible before Replyer after ${change}`, async (t) => {
    clock(t)
    const h = fixture()
    try {
      const s = sequence(t, h)
      const late = h.receive(h.message('late-confirmation', alice))
      let rounds = 0
      await h.ask(t, (messages) => {
        if (++rounds === 1) return completion([call('memory.commitments')])
        const read = receipt(messages)
        if (rounds === 2)
          return completion([
            call('memory.commitmentTimeline', {
              ...read.value.find((v) => v.view.ref.version === 2).view.ref,
            }),
          ])
        assert.equal(read.value.length, 1)
        if (change === 'head-mutation')
          saved(
            h.save(
              'late-confirmation',
              alice,
              { kind: 'confirm', target: s.b19.ref },
              { sources: [late] },
            ),
          )
        else {
          h.sceneFlags({ memoryReadEnabled: false })
          h.sceneFlags({ memoryReadEnabled: true })
        }
        return plan(messages, { stateEvidenceIds: [read.id] })
      })
      assert.equal(rounds, 3)
      assert.equal(h.replyInputs.length, 0)
      assert.equal(h.stats().sends, 0)
    } finally {
      await h.close()
    }
  })

test('an explicit old occurrence date and negative possible conditions survive actual history handoff without moving the recorded as-of boundary', async (t) => {
  clock(t)
  const h = fixture()
  try {
    const occurredAt = '2001-02-03T04:05:06.000Z'
    const entry = saved(
      h.save('past-qualified', alice, {
        kind: 'propose',
        terms: {
          ...terms(),
          temporalScope: 'past',
          polarity: 'negative',
          modality: 'possible',
          conditions: ['ONLY_IF_WEATHER_PERMITTED_NOT_CONFIRMED'],
          occurredAt,
        },
      }),
    )
    const earlier = await timeline(h, entry.ref, { asOfRecorded: occurredAt })
    assert.deepEqual(earlier.recordedStates, [])
    assert.ok(earlier.reasons.includes('no_recorded_state'))
    let rounds = 0
    await h.ask(t, (messages) => {
      if (++rounds === 1) return completion([call('memory.commitments')])
      const read = receipt(messages)
      if (rounds === 2)
        return completion([call('memory.commitmentTimeline', { ...read.value[0].view.ref })])
      return plan(messages, { stateEvidenceIds: [read.id] })
    })
    assert.equal(h.replyInputs.length, 1)
    const state = JSON.parse(h.replyInputs[0]![1]!.content).evidence.find(
      (e: any) => e.toolName === 'memory.commitmentTimeline',
    ).states[0]
    assert.equal(state.recordedStates[0].recordedAt, '2026-10-29T00:00:00.000Z')
    const qualified = state.recordedStates[0].states[0].terms
    assert.equal(qualified.occurredAt, occurredAt)
    assert.equal(qualified.polarity, 'negative')
    assert.equal(qualified.modality, 'possible')
    assert.equal(qualified.temporalScope, 'past')
    assert.deepEqual(qualified.conditions, ['ONLY_IF_WEATHER_PERMITTED_NOT_CONFIRMED'])
    assert.equal(state.stateTime, 'server_recorded_not_occurred')
  } finally {
    await h.close()
  }
})

for (const change of ['head-mutation', 'read-aba'] as const)
  test(`actual managed Replyer queued with delivered timeline rechecks ${change} at physical dispatch and a fresh context remains usable`, async (t) => {
    clock(t)
    const h = fixture(),
      controller = new AbortController()
    let release: () => void = () => {},
      blockers: Promise<void>[] = [],
      providerCalls = 0
    try {
      const s = sequence(t, h),
        late = h.receive(h.message('queued-late-confirmation', alice))
      h.engine.protocols.register(
        'fixture:timeline',
        'Synthetic timeline transport',
        {
          async complete() {
            providerCalls++
            return {
              text: '',
              toolCalls: [],
              usage: { inputTokens: 0, outputTokens: 0, totalTokens: 0 },
            }
          },
        },
        controller.signal,
      )
      h.store.set('models', {
        providers: [
          {
            id: 'synthetic',
            name: 'Offline timeline',
            protocol: 'fixture:timeline',
            baseUrl: 'http://127.0.0.1:1',
            credentialEnv: '',
            retryCount: 0,
            timeoutMs: 2000,
          },
        ],
        models: [
          {
            id: 'synthetic',
            displayName: 'Offline timeline',
            providerId: 'synthetic',
            apiModelName: 'synthetic',
            capabilities: ['text', 'tools'],
          },
        ],
        routes: [{ role: 'replyer', orderedModelIds: ['synthetic'], timeoutMs: 3000 }],
      })
      const scripted = h.engine.router.bind(h.engine)
      h.engine.router = (config, admit) => {
        const local = scripted(config, admit),
          managed = Engine.prototype.router.call(h.engine, config, admit)
        return {
          complete: (...args: Parameters<Router['complete']>) =>
            args[0] === 'replyer' ? managed.complete(...args) : local.complete(...args),
        } as Router
      }
      const wait = new Promise<void>((resolve) => {
        release = resolve
      })
      let rounds = 0
      const choose = (messages: ChatMessage[], block: boolean) => {
        if (++rounds === 1) return completion([call('memory.commitments')])
        const read = receipt(messages)
        if (rounds === 2)
          return completion([
            call('memory.commitmentTimeline', {
              ...read.value.find((v) => v.view.ref.version === 2).view.ref,
            }),
          ])
        assert.equal(read.value[0].stateKind, 'commitment_timeline')
        if (block)
          blockers = Array.from({ length: modelScheduler.status.concurrency }, () =>
            modelScheduler.run(0, undefined, () => wait),
          )
        return plan(messages, { stateEvidenceIds: [read.id] })
      }
      await h.ask(t, (messages) => choose(messages, true), h.message('queued-history-query'), false)
      assert.equal(modelScheduler.status.queued, 1)
      assert.equal(providerCalls, 0)
      if (change === 'head-mutation')
        saved(
          h.save(
            'queued-late-confirmation',
            alice,
            { kind: 'confirm', target: s.b19.ref },
            { sources: [late] },
          ),
        )
      else {
        h.sceneFlags({ memoryReadEnabled: false })
        h.sceneFlags({ memoryReadEnabled: true })
      }
      release()
      await Promise.all(blockers)
      await advance(t, 0)
      assert.equal(h.engine.runtime.activeTasks, 0)
      assert.equal(providerCalls, 0)
      assert.equal(h.stats().sends, 0)
      rounds = 0
      await h.ask(t, (messages) => choose(messages, false), h.message('fresh-history-query'))
      assert.equal(
        providerCalls,
        1,
        'fresh current authority works; old blocked turn is not silently upgraded',
      )
      assert.equal(h.stats().sends, 0)
    } finally {
      release()
      await Promise.all(blockers)
      controller.abort()
      await h.close()
    }
  })
