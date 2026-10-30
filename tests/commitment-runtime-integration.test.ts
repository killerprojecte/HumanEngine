import assert from 'node:assert/strict'
import test, { type TestContext } from 'node:test'
import { setImmediate as immediate } from 'node:timers/promises'
import { mkdtempSync, rmSync } from 'node:fs'
import { join } from 'node:path'
import { tmpdir } from 'node:os'
import { Engine } from '../src/engine.ts'
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
import { metadata, RecoveryAuthority } from '../src/memory/recovery/ledger.ts'
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
  const root = mkdtempSync(join(tmpdir(), 'he-commit-runtime-')),
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
    executionErrors: unknown[] = []
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
    engine.router = () =>
      ({
        complete: async (role, messages, tools = []) => {
          models++
          offered.push(structuredClone(tools))
          if (tools.some((t) => t.name === 'participation.decide'))
            return completion([
              call('participation.decide', { action: 'reply', reasonCode: 'synthetic_commitment' }),
            ])
          if (tools.some((t) => t.name === 'reply.plan')) {
            assert.ok(planner)
            return planner(messages, tools)
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
      sources = options.sources ?? [message(id, person, target)]
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
    async ask(t: TestContext, fn: NonNullable<typeof planner>, m = message(`query-${models}`)) {
      planner = fn
      store.set('settings', {
        ...store.settings,
        learningBatchMs: 300000,
        learningMaxWaitMs: 300000,
      })
      engine.settingsChanged()
      store.saveScene({ ...store.scene(scene.id)!, participationEnabled: true })
      context.receive(m as Message, scene, false)
      await advance(t, 10)
      await advance(t, 100)
      await engine.contextualLearning.queue.whenIdle()
      assert.equal(engine.runtime.activeTasks, 0)
      assert.deepEqual(executionErrors, [])
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
function confirmed(h: ReturnType<typeof fixture>, hour = '18:00; date and timezone unknown') {
  const first = saved(h.save('proposal-' + hour, alice, { kind: 'propose', terms: terms(hour) }))
  return saved(h.save('confirm-b-' + hour, bob, { kind: 'confirm', target: first.ref }))
}
function twoVersions(h: ReturnType<typeof fixture>) {
  const first = confirmed(h)
  const next = saved(
    h.save('proposal-19', alice, {
      kind: 'amend',
      target: first.ref,
      terms: terms('19:00; date and timezone unknown'),
    }),
  )
  assert.equal(h.rows().find((r) => r.confirmation === 'confirmed')!.ref.version, 1)
  assert.deepEqual(next.confirmedParticipantIds, [alice])
  const active = saved(h.save('confirm-b-19', bob, { kind: 'confirm', target: next.ref }))
  return { first, next: active }
}

test('actual MemoryService requires both canonical people on the same terms; amendment activation survives restart without inventing dates', async () => {
  const h = fixture()
  try {
    const first = saved(h.save('proposal-18', alice, { kind: 'propose', terms: terms() }))
    assert.deepEqual(first.confirmedParticipantIds, [alice])
    assert.deepEqual(first.missingParticipantIds, [bob])
    const second = saved(h.save('confirm-b-18', bob, { kind: 'confirm', target: first.ref }))
    assert.equal(second.confirmation, 'confirmed')
    const oldExposure = h.stateContext(first.ref).exposedRefs
    const next = saved(
      h.save('proposal-19', alice, {
        kind: 'amend',
        target: first.ref,
        terms: terms('19:00; date and timezone unknown'),
      }),
    )
    assert.deepEqual(
      h.rows().map((r) => [r.ref.version, r.confirmation]),
      [
        [1, 'confirmed'],
        [2, 'proposed'],
      ],
    )
    const stale = h.save(
      'stale-confirm-b',
      bob,
      { kind: 'confirm', target: first.ref },
      { exposed: oldExposure },
    )
    assert.equal(stale.commitment?.status, 'rejected')
    assert.equal(stale.commitment?.reason, 'commitment_snapshot_changed')
    const final = saved(h.save('confirm-b-19', bob, { kind: 'confirm', target: next.ref }))
    assert.equal(final.confirmation, 'confirmed')
    assert.equal(final.ref.version, 2)
    for (const key of ['occurredAt', 'validFrom', 'validUntil'] as const)
      assert.equal(final.terms![key], undefined)
    const history = h.engine.memory.commitments.history(h.stateContext(final.ref), final.ref)!
    assert.equal(history.interpretation, 'previously_recorded_not_current_truth')
    assert.ok(
      history.revisions.some((r) => r.terms?.timeQualification === first.terms!.timeQualification),
    )
    await h.reopen()
    assert.equal(h.current(final.ref).confirmation, 'confirmed')
    assert.equal(h.rows()[0]!.terms!.timeQualification, final.terms!.timeQualification)
    assert.equal(h.stats().models, 0)
    assert.equal(h.stats().sends, 0)
  } finally {
    await h.close()
  }
})

test('actual Runtime recall of activated amendment confirmation yields incomplete, never a certain earlier arrangement', async () => {
  const h = fixture()
  try {
    const { first, next } = twoVersions(h)
    h.engine.runtime.recall(h.scene.id, h.message('confirm-b-19', bob).id)
    const current = h.current(next.ref)
    assert.equal(current.confirmation, 'incomplete')
    assert.equal(current.ref.version, 2)
    assert.deepEqual(current.missingParticipantIds, [bob])
    assert.ok(!h.rows().some((r) => r.ref.version === 1 && r.confirmation === 'confirmed'))
    const history = h.engine.memory.commitments.history(h.stateContext(next.ref), next.ref)!
    assert.ok(
      history.revisions.some(
        (r) =>
          r.ref.version === first.ref.version && r.terms?.timeQualification?.startsWith('18:00'),
      ),
    )
    const bytes = JSON.stringify(
      h.store.db.prepare('SELECT value FROM memory_commitment_revisions').all(),
    )
    assert.ok(!bytes.includes('confirm-b-19'))
    await h.reopen()
    assert.equal(h.current(next.ref).confirmation, 'incomplete')
    assert.equal(h.engine.memory.commitments.storage.head(next.ref.id)!.everActivatedVersion, 2)
  } finally {
    await h.close()
  }
})

test('revoked amendment proposal physically removes its private terms while retaining only an incomplete activation marker', async () => {
  const h = fixture(),
    canary = 'PRIVATE_AMENDMENT_TERMS_CANARY_19'
  try {
    const first = confirmed(h)
    const next = saved(
      h.save('private-amendment', alice, {
        kind: 'amend',
        target: first.ref,
        terms: { ...terms('19:00'), title: canary },
      }),
    )
    saved(h.save('private-confirm-b', bob, { kind: 'confirm', target: next.ref }))
    h.engine.runtime.recall(h.scene.id, h.message('private-amendment').id)
    assert.equal(h.current(next.ref).confirmation, 'incomplete')
    assert.equal(h.current(next.ref).terms, undefined)
    for (const table of [
      'memory_commitment_heads',
      'memory_commitment_revisions',
      'memory_commitment_commands',
      'memory_assertion_revisions',
    ])
      assert.ok(
        !JSON.stringify(h.store.db.prepare(`SELECT * FROM ${table}`).all()).includes(canary),
        table,
      )
    await h.reopen()
    assert.equal(h.current(next.ref).terms, undefined)
    assert.ok(!h.rows().some((r) => r.confirmation === 'confirmed'))
  } finally {
    await h.close()
  }
})

test('multi-person completion is separately self-reported; withdrawing a terminal proof requires review instead of silently reopening', async () => {
  const h = fixture()
  try {
    const first = confirmed(h)
    const a = saved(h.save('done-a', alice, { kind: 'complete', target: first.ref }))
    assert.equal(a.lifecycle, 'pending')
    assert.deepEqual(a.completedParticipantIds, [alice])
    const b = saved(h.save('done-b', bob, { kind: 'complete', target: first.ref }))
    assert.equal(b.lifecycle, 'done')
    assert.equal(b.qualification, 'declared_not_verified')
    h.engine.runtime.recall(h.scene.id, h.message('done-b', bob).id)
    assert.equal(h.current(first.ref).lifecycle, 'needs_review')
    assert.equal(h.current(first.ref).confirmation, 'incomplete')
    await h.reopen()
    assert.equal(h.current(first.ref).lifecycle, 'needs_review')
  } finally {
    await h.close()
  }
})

for (const disabled of [false, true])
  test(`real OR backing survives source recall and epoch requalification with feature OFF=${disabled}`, async () => {
    const h = fixture()
    try {
      const content = 'SAME_SELF_ASSERTION_TWO_INDEPENDENT_PROOFS'
      h.save('or-a', alice, undefined, { content })
      const result = h.save('or-b', alice, undefined, { content })
      const before = h.engine.memory.revisionState(result.memory.id)
      // Existing lawful OR assertions use the real authority port; fresh model commands stay single-source.
      const admitted = h.store.db.transaction(() =>
        h.engine.memory.commitments.admit(
          h.stateContext(),
          { id: result.memory.id, version: before.version, deletionEpoch: before.deletionEpoch },
          { kind: 'propose', terms: terms('18:00', [alice]) },
        ),
      )()
      assert.equal(admitted.status, 'saved')
      assert.ok(admitted.item)
      const first = admitted.item
      assert.equal(first.evidence[0]!.proofs.length, 2)
      if (disabled) h.settings({ memoryCommitmentsEnabled: false })
      h.engine.runtime.recall(h.scene.id, h.message('or-a').id)
      if (disabled) {
        assert.deepEqual(h.rows(), [])
        h.settings({ memoryCommitmentsEnabled: true })
      }
      const current = h.current(first.ref)
      assert.equal(current.confirmation, 'confirmed')
      assert.equal(current.evidence[0]!.proofs.length, 1)
      assert.equal(current.evidence[0]!.proofs[0]!.sources[0]!.messageId, h.message('or-b').id)
      assert.ok(current.evidence[0]!.qualificationRef!.deletionEpoch > before.deletionEpoch)
      assert.equal(h.engine.memory.list(h.ctx())[0]!.status, 'disputed')
      await h.reopen()
      assert.equal(h.current(first.ref).confirmation, 'confirmed')
    } finally {
      await h.close()
    }
  })

test('unrelated recall cannot launder ordinary metadata drift into a fresh commitment backing', async () => {
  const h = fixture()
  try {
    const result = h.save('drifting', alice, { kind: 'propose', terms: terms('18:00', [alice]) }),
      first = saved(result)
    h.engine.memory.update(result.memory.id, { locked: true })
    assert.equal(h.current(first.ref).confirmation, 'incomplete')
    h.engine.runtime.recall(h.scene.id, 'unrelated-source')
    assert.equal(h.current(first.ref).confirmation, 'incomplete')
    assert.equal(h.current(first.ref).evidence.length, 0)
    await h.reopen()
    assert.equal(h.current(first.ref).confirmation, 'incomplete')
  } finally {
    await h.close()
  }
})

for (const phase of ['prepare', 'late'] as const)
  test(`Runtime ${phase} recall failure preserves durable root intent, rolls back local commitment state and blocks reads`, async () => {
    const h = fixture()
    try {
      const first = confirmed(h),
        sourceId = h.message('confirm-b-18:00; date and timezone unknown', bob).id
      const tables = [
        'memory_commitment_heads',
        'memory_commitment_revisions',
        'memories',
        'memory_assertion_revisions',
        'recall_tombstones',
      ]
      const snapshot = () =>
        tables.map((table) => h.store.db.prepare(`SELECT * FROM ${table}`).all())
      const before = snapshot(),
        gate = metadata(h.store.db)!
      const fail = () => {
        throw Error(`synthetic_${phase}_recall_failure`)
      }
      h.engine.runtime.addExtension({
        tools: [],
        execute: async () => undefined,
        ...(phase === 'prepare' ? { prepareRecall: fail } : { recalled: fail }),
      })
      assert.throws(
        () => h.engine.runtime.recall(h.scene.id, sourceId),
        new RegExp(`synthetic_${phase}_recall_failure`),
      )
      assert.deepEqual(snapshot(), before)
      assert.deepEqual(metadata(h.store.db), gate)
      const authority = new RecoveryAuthority(gate.authority)
      try {
        assert.ok(authority.head().sequence > gate.head.sequence)
        assert.ok(
          authority
            .entries()
            .some((e) => e.revocation.kind === 'source' && e.revocation.messageId === sourceId),
        )
      } finally {
        authority.close()
      }
      assert.throws(() => h.current(first.ref), /recovery_authority_head_changed/)
      assert.throws(() => h.engine.memory.list(h.ctx()), /recovery_authority_head_changed/)
      assert.equal(h.stats().models, 0)
      assert.equal(h.stats().sends, 0)
    } finally {
      await h.close()
    }
  })

type StateReceipt = {
  id: string
  toolName: string
  value: Array<{ stateKind: string; view: CommitmentView }>
  coverage?: { status: string; omittedRecords: number }
}
const receipts = (messages: ChatMessage[]) =>
  messages.filter((m) => m.role === 'tool').map((m) => JSON.parse(m.content) as StateReceipt)
const plan = (messages: ChatMessage[], patch: Parameters<typeof fixtureReplyPlan>[1] = {}) =>
  completion([call('reply.plan', fixtureReplyPlan(messages, patch)!, 'final-plan')])
const candidate = (
  id: string,
  command: CommitmentCommand,
  content = `Synthetic commitment declaration ${id}.`,
) => ({
  content,
  kind: 'fact',
  epistemicKind: 'explicit',
  subject: { kind: 'self', sourceMessageId: id },
  sourceMessageIds: [id],
  commitment: command,
})
async function learnInitial(
  t: TestContext,
  h: ReturnType<typeof fixture>,
  value = terms(),
  body?: string,
) {
  h.settings({ learningBatchMs: 100 })
  h.setExtract((messages) => [candidate(messages[0]!.id, { kind: 'propose', terms: value })])
  h.receive(h.message('automatic-proposal', alice, h.scene, body))
  await advance(t)
  assert.equal(h.rows().length, 1)
  assert.equal(h.stats().extracts, 1)
  assert.equal(h.stats().sends, 0)
  return h.rows()[0]!
}
async function commandTurn(
  t: TestContext,
  h: ReturnType<typeof fixture>,
  id: string,
  person: string,
  build: (states: CommitmentView[]) => CommitmentCommand,
  options: { body?: string; content?: string } = {},
) {
  let rounds = 0
  await h.ask(
    t,
    (messages) => {
      if (++rounds === 1) return completion([call('memory.commitments', {}, 'read-commitments')])
      const read = receipts(messages).at(-1)!
      assert.ok(read.value.length)
      const contract = fixtureReplyPlan(messages)!
      return plan(messages, {
        action: 'noReply',
        stateEvidenceIds: [read.id],
        actions: [
          {
            toolName: 'learning.propose',
            arguments: candidate(
              contract.contextMessageIds.at(-1)!,
              build(read.value.map((item) => item.view)),
              options.content ?? `SYNTHETIC_CONTEXTUAL_${id}`,
            ),
          },
        ],
      })
    },
    h.message(id, person, h.scene, options.body),
  )
  assert.equal(rounds, 2)
}
function replyData(h: ReturnType<typeof fixture>) {
  assert.ok(h.replyInputs.length, 'actual Runtime must hand qualified evidence to the Replyer')
  return JSON.parse(h.replyInputs.at(-1)!.find((m) => m.role === 'user')!.content)
}

test('synthetic adapter to silent learning to actual Planner commands completes both-person 18 to 19 transition and qualified Replyer handoff', async (t) => {
  clock(t)
  const h = fixture()
  try {
    const first = await learnInitial(t, h)
    assert.equal(first.confirmation, 'proposed')
    assert.deepEqual(first.confirmedParticipantIds, [alice])
    await commandTurn(t, h, 'automatic-b-18', bob, (states) => ({
      kind: 'confirm',
      target: states[0]!.ref,
    }))
    assert.equal(h.rows()[0]!.confirmation, 'confirmed')
    await commandTurn(t, h, 'automatic-a-19', alice, (states) => ({
      kind: 'amend',
      target: states[0]!.ref,
      terms: { ...states[0]!.terms!, timeQualification: '19:00; date and timezone unknown' },
    }))
    assert.deepEqual(
      h.rows().map((r) => [r.ref.version, r.confirmation]),
      [
        [1, 'confirmed'],
        [2, 'proposed'],
      ],
    )
    await commandTurn(t, h, 'automatic-b-19', bob, (states) => ({
      kind: 'confirm',
      target: states.find((s) => s.ref.version === 2)!.ref,
    }))
    assert.deepEqual(
      h.rows().map((r) => [r.ref.version, r.confirmation]),
      [[2, 'confirmed']],
    )
    assert.equal(
      h.replyInputs.length,
      0,
      'noReply learning candidates never invoke Replyer or send',
    )
    assert.equal(h.engine.contextualLearning.queue.stats().completed, 3)
    let rounds = 0
    await h.ask(t, (messages) => {
      if (++rounds === 1) return completion([call('memory.commitments', {}, 'read-current')])
      const read = receipts(messages)[0]!
      if (rounds === 2)
        return completion([
          call('memory.commitmentHistory', read.value[0]!.view.ref, 'read-retained-plan-history'),
        ])
      const history = receipts(messages)[1]!
      assert.equal(history.value.length, 1)
      return plan(messages, {
        stateEvidenceIds: [read.id, history.id],
        facts: [
          {
            text: 'The current arrangement is declared for 19:00; date and timezone remain unknown.',
            evidenceIds: [read.id],
          },
          {
            text: '18:00 is only the previously recorded arrangement, not the current one.',
            evidenceIds: [history.id],
          },
        ],
      })
    })
    const data = replyData(h),
      state = data.evidence[0].states[0]
    assert.equal(state.stateKind, 'commitment')
    assert.equal(state.view.ref.version, 2)
    assert.equal(state.view.confirmation, 'confirmed')
    assert.equal(state.view.qualification, 'declared_not_verified')
    assert.equal(state.view.sourceBodyCoverage, 'unavailable')
    assert.equal(state.view.terms.timeQualification, '19:00; date and timezone unknown')
    assert.equal(state.view.terms.occurredAt, undefined)
    assert.equal(state.view.confirmedParticipantIds.length, 2)
    assert.ok(state.view.declarations.every((d: { sources: unknown[] }) => d.sources.length > 0))
    const historical = data.evidence[1].states[0]
    assert.equal(rounds, 3)
    assert.equal(historical.stateKind, 'commitment_history')
    assert.equal(historical.coverage, 'limited')
    assert.equal(historical.interpretation, 'previously_recorded_not_current_truth')
    assert.equal(historical.sourceBodyCoverage, 'unavailable')
    assert.deepEqual(
      historical.revisions.map((r: CommitmentView) => r.ref.version),
      [1, 2],
    )
    assert.ok(historical.revisions[0].terms.timeQualification.startsWith('18:00'))
    assert.ok(historical.revisions[1].terms.timeQualification.startsWith('19:00'))
    assert.ok(
      historical.revisions.every(
        (r: CommitmentView) =>
          r.qualification === 'declared_not_verified' && r.terms?.occurredAt === undefined,
      ),
    )
    assert.equal(historical.historyWindow.throughVersion, 2)
    assert.equal(h.stats().extracts, 1, 'contextual commands add no second extraction model loop')
    assert.equal(h.stats().sends, 0)
    h.engine.runtime.recall(h.scene.id, h.message('automatic-b-19', bob).id)
    assert.equal(h.rows()[0]!.confirmation, 'incomplete')
    assert.equal(h.rows()[0]!.ref.version, 2)
    await h.reopen()
    assert.equal(h.rows()[0]!.confirmation, 'incomplete')
  } finally {
    await h.close()
  }
})

test('actual silent extraction does not promote reported assertions or qualified candidates, and same-title entities stay distinct', async (t) => {
  clock(t)
  const h = fixture()
  try {
    h.settings({ learningBatchMs: 100 })
    const kinds = ['reported', 'possible', 'negative', 'conditional', 'past', 'unspecified']
    h.setExtract((messages) =>
      messages.map((m, i) => ({
        ...candidate(
          m.id,
          {
            kind: 'propose',
            terms: {
              ...terms('Unknown time', [alice]),
              ...(kinds[i] === 'possible' ? { modality: 'possible' } : {}),
              ...(kinds[i] === 'negative' ? { polarity: 'negative' } : {}),
              ...(kinds[i] === 'conditional'
                ? { conditions: ['Only if both expressly agree.'] }
                : {}),
              ...(['past', 'unspecified'].includes(kinds[i]!)
                ? { temporalScope: kinds[i] as 'past' | 'unspecified' }
                : {}),
            },
          },
          `SYNTHETIC_CANDIDATE_${kinds[i]}`,
        ),
        ...(kinds[i] === 'reported'
          ? {
              commitment: undefined,
              subject: {
                kind: 'mentioned',
                personId: alice,
                sourceMessageId: m.id,
                mentionPartIndex: 1,
              },
            }
          : {}),
      })),
    )
    for (const kind of kinds) h.receive(h.message(kind, kind === 'reported' ? bob : alice))
    await advance(t)
    assert.equal(h.rows().length, kinds.length - 1)
    assert.equal(new Set(h.rows().map((r) => r.ref.id)).size, kinds.length - 1)
    assert.ok(h.engine.memory.adminList().some((r) => r.epistemicKind === 'reported'))
    assert.equal(new Set(h.rows().map((r) => r.terms!.title)).size, 1)
    assert.ok(
      h
        .rows()
        .every((r) => r.confirmation === 'proposed' && r.confirmedParticipantIds.length === 0),
    )
    assert.ok(h.rows().every((r) => r.terms!.occurredAt === undefined))
    assert.equal(h.stats().extracts, 1)
    assert.deepEqual(h.engine.memory.commitments.list(h.stateContext(undefined, h.other)).items, [])
  } finally {
    await h.close()
  }
})

test('actual exposure captures first mutation: a prepared or queued stale confirmation cannot borrow a newer head', async () => {
  const h = fixture()
  try {
    const first = saved(h.save('first', alice, { kind: 'propose', terms: terms() }))
    const m = h.receive(h.message('fresh-confirmation', bob)),
      ctx = h.turn(m),
      extension = h.extension()
    const delivered = (await extension.execute('memory.commitments', {}, ctx)) as unknown[]
    assert.equal(delivered.length, 1)
    saved(h.save('concurrent-confirmation', bob, { kind: 'confirm', target: first.ref }))
    const result = (await extension.execute(
      'learning.propose',
      candidate(m.id, { kind: 'complete', target: first.ref }, 'STALE_STATE_MUST_NOT_WRITE'),
      ctx,
    )) as { status: string }
    assert.equal(result.status, 'rejected')
    assert.ok(!h.engine.memory.adminList().some((r) => r.content === 'STALE_STATE_MUST_NOT_WRITE'))
    const next = h.turn(m, true),
      prepared = (await extension.execute('memory.commitments', {}, next)) as unknown[]
    saved(h.save('concurrent-done-a', alice, { kind: 'complete', target: first.ref }))
    assert.deepEqual(extension.delivered!('memory.commitments', prepared, next), [])
    assert.equal(h.current(first.ref).lifecycle, 'pending')
    const historyContext = h.turn(m)
    assert.deepEqual(
      await extension.execute('memory.commitmentHistory', first.ref, historyContext),
      [],
    )
    await extension.execute('memory.commitments', {}, historyContext)
    assert.equal(
      (
        (await extension.execute(
          'memory.commitmentHistory',
          first.ref,
          historyContext,
        )) as unknown[]
      ).length,
      1,
    )
    assert.deepEqual(
      await extension.execute(
        'learning.propose',
        candidate(m.id, { kind: 'complete', target: first.ref }),
        historyContext,
      ),
      { status: 'rejected', reason: 'historical_read_not_learning_authority' },
    )
  } finally {
    await h.close()
  }
})

function largeCandidate(h: ReturnType<typeof fixture>) {
  return h.save('large-state', alice, {
    kind: 'propose',
    terms: {
      ...terms('Unknown local date', [alice]),
      conditions: Array.from(
        { length: 8 },
        (_, i) => `UNCONFIRMED_CONDITION_${i}: ${'中文限制'.repeat(350)}`,
      ),
    },
  })
}

test('Planner whole-state omission registers no authority, survives backing mutation and preserves limited coverage through Replyer', async (t) => {
  clock(t)
  const h = fixture()
  try {
    const result = largeCandidate(h)
    h.settings({ plannerEvidenceMaxBytes: 32768, replySuggestionsEnabled: true })
    let rounds = 0
    await h.ask(t, (messages) => {
      if (++rounds === 1) return completion([call('memory.commitments', {}, 'read-too-broad')])
      const read = receipts(messages)[0]!
      assert.deepEqual(read.value, [])
      assert.equal(read.coverage!.status, 'limited')
      assert.equal(read.coverage!.omittedRecords, 1)
      h.engine.memory.update(result.memory.id, { locked: true })
      return plan(messages, {
        strategy: 'OMITTED_STATE_NO_PLANS_STRATEGY',
        expressionGoals: ['OMITTED_STATE_GOAL'],
        replySuggestion: 'OMITTED_STATE_DRAFT: nobody made a plan.',
      })
    })
    const data = replyData(h)
    assert.equal(data.memoryEvidenceCoverage.status, 'omitted')
    assert.equal(data.memoryEvidenceCoverage.omitted[0].reason, 'planner_evidence_budget')
    assert.equal(data.memoryEvidenceCoverage.omitted[0].omittedRecords, 1)
    assert.doesNotMatch(
      JSON.stringify(data),
      /OMITTED_STATE_(?:NO_PLANS|GOAL|DRAFT)|UNCONFIRMED_CONDITION/,
    )
    assert.equal(h.stats().sends, 0)
  } finally {
    await h.close()
  }
})

test('Replyer byte cap removes the entire qualified state and dependent conclusion, never only its long conditions', async (t) => {
  clock(t)
  const h = fixture()
  try {
    largeCandidate(h)
    h.settings({ replyEvidenceMaxBytes: 4096, replySuggestionsEnabled: true })
    let rounds = 0
    await h.ask(t, (messages) => {
      if (++rounds === 1)
        return completion([call('memory.commitments', {}, 'read-qualified-state')])
      const read = receipts(messages)[0]!
      assert.equal(read.value.length, 1)
      assert.equal(read.coverage!.status, 'complete')
      return plan(messages, {
        stateEvidenceIds: [read.id],
        facts: [{ text: 'DEPENDENT_STATE_CONCLUSION', evidenceIds: [read.id] }],
        constraints: ['Do not interpret omitted evidence as an empty result.'],
        replySuggestion: 'DEPENDENT_STATE_DRAFT',
        expressionGoals: ['DEPENDENT_STATE_GOAL'],
      })
    })
    const data = replyData(h),
      wire = h.replyInputs.at(-1)!.find((m) => m.role === 'user')!.content
    assert.ok(Buffer.byteLength(wire, 'utf8') <= 4096)
    assert.equal(data.memoryEvidenceCoverage.status, 'omitted')
    assert.equal(data.evidence.length, 0)
    assert.equal(data.plan.facts.length, 0)
    assert.deepEqual(data.plan.constraints, [
      'Do not interpret omitted evidence as an empty result.',
    ])
    assert.doesNotMatch(wire, /UNCONFIRMED_CONDITION|DEPENDENT_STATE_/)
  } finally {
    await h.close()
  }
})

test('Planner cannot cite an omitted state receipt as selected evidence', async (t) => {
  clock(t)
  const h = fixture()
  try {
    largeCandidate(h)
    h.settings({ plannerEvidenceMaxBytes: 32768 })
    let rounds = 0
    await h.ask(t, (messages) => {
      if (++rounds === 1) return completion([call('memory.commitments', {}, 'read-too-broad')])
      const read = receipts(messages)[0]!
      assert.deepEqual(read.value, [])
      assert.equal(read.coverage?.status, 'limited')
      if (rounds === 3) {
        const hint = receipts(messages).at(-1) as unknown as {
          coverage: string
          memorySelectionOptions: unknown[]
        }
        assert.equal(hint.coverage, 'limited')
        assert.deepEqual(hint.memorySelectionOptions, [])
      }
      return plan(messages, {
        stateEvidenceIds: [read.id],
        facts: [{ text: 'INVALID_SELECTED_STATE', evidenceIds: [read.id] }],
      })
    })
    assert.equal(rounds, 3)
    assert.equal(h.replyInputs.length, 0)
    assert.equal(h.stats().sends, 0)
    assert.ok(h.store.logs.some((log) => log.errorCode === 'planner_unknown_state_evidence'))
  } finally {
    await h.close()
  }
})

test('actual Planner anchored history stays separately typed previously-recorded evidence with unknown validity through Replyer', async (t) => {
  clock(t)
  const h = fixture()
  try {
    const record = h.engine.memory.create({
      scope: { kind: 'scene', sceneId: h.scene.id, personId: alice },
      content: 'HISTORICAL_ONLY: plan was 18:00, not a current promise.',
      kind: 'claim',
    })
    t.mock.timers.tick(1000)
    h.engine.memory.update(record.id, { content: 'CURRENT_ONLY: no arrangement is confirmed.' })
    let rounds = 0
    await h.ask(t, (messages) => {
      if (++rounds === 1)
        return completion([call('memory.person', { personId: alice }, 'read-current-anchor')])
      const all = receipts(messages)
      if (rounds === 2) {
        const record = all[0]!.value[0] as unknown as { id: string }
        assert.ok(record.id)
        return completion([
          call(
            'memory.history',
            { memoryId: record.id, asOfRecorded: '2026-10-29T00:00:00.500Z' },
            'read-old',
          ),
        ])
      }
      const read = all[1]!
      const historical = read.value[0] as unknown as {
        interpretation: string
        sourceBodyCoverage: string
        revisions: Array<{ record: { content: string }; validityCoverage: string }>
      }
      assert.equal(historical.interpretation, 'previously_recorded_not_current_truth')
      assert.equal(historical.sourceBodyCoverage, 'unavailable')
      assert.ok(historical.revisions.some((r) => r.record.content.includes('HISTORICAL_ONLY')))
      assert.ok(
        historical.revisions.every(
          (r) => !r.record.content.includes('CURRENT_ONLY') && r.validityCoverage === 'unknown',
        ),
      )
      return plan(messages, {
        stateEvidenceIds: [read.id],
        facts: [
          {
            text: 'This is only a previously recorded 18:00 statement, not the current arrangement.',
            evidenceIds: [read.id],
          },
        ],
      })
    })
    const data = replyData(h),
      state = data.evidence[0].states[0]
    assert.equal(rounds, 3)
    assert.equal(state.stateKind, 'assertion_history')
    assert.equal(state.interpretation, 'previously_recorded_not_current_truth')
    assert.equal(state.sourceBodyCoverage, 'unavailable')
    assert.equal(state.coverage.status, 'limited')
    assert.match(JSON.stringify(state), /HISTORICAL_ONLY/)
    assert.doesNotMatch(JSON.stringify(state), /CURRENT_ONLY/)
    assert.equal(h.stats().extracts, 0)
    assert.equal(h.stats().sends, 0)
  } finally {
    await h.close()
  }
})

test('history requires an actually delivered current anchor and cannot authorize fresh learning; recall revokes retained receipt', async () => {
  const h = fixture()
  try {
    const result = h.save('history-source', alice),
      m = h.receive(h.message('history-query'))
    const extension = h.extension(),
      ctx = h.turn(m)
    await assert.rejects(
      extension.execute('memory.history', { memoryId: result.memory.id }, ctx),
      /memory_history_anchor_not_exposed/,
    )
    const current = (await extension.execute(
      'memory.person',
      { personId: alice },
      ctx,
    )) as unknown[]
    assert.equal(current.length, 1)
    const history = (await extension.execute(
      'memory.history',
      { memoryId: result.memory.id },
      ctx,
    )) as unknown[]
    assert.equal(history.length, 1)
    const before = h.engine.memory.adminList().length
    const rejected = await extension.execute(
      'learning.propose',
      candidate(
        m.id,
        { kind: 'propose', terms: terms('18', [alice]) },
        'HISTORICAL_BODY_MUST_NOT_BECOME_NEW_FACT',
      ),
      ctx,
    )
    assert.deepEqual(rejected, {
      status: 'rejected',
      reason: 'historical_read_not_learning_authority',
    })
    assert.equal(h.engine.memory.adminList().length, before)
    h.engine.runtime.recall(h.scene.id, h.message('history-source').id)
    assert.equal(
      ctx.validations.every((check) => check()),
      false,
    )
    await assert.rejects(
      extension.execute('memory.history', { memoryId: result.memory.id }, h.turn(m)),
      /memory_history_anchor_not_exposed/,
    )
    assert.equal(h.engine.memory.historyFor(h.ctx(), result.memory.id), undefined)
  } finally {
    await h.close()
  }
})

for (const revoke of ['learning_off', 'source_recall', 'state_change'] as const)
  test(`actual queued exact commitment command cannot commit after ${revoke}`, async () => {
    const h = fixture()
    try {
      const initial = saved(h.save('queued-plan', alice, { kind: 'propose', terms: terms() }))
      const m = h.receive(h.message('queued-confirm-b', bob)),
        ctx = h.turn(m),
        extension = h.extension()
      await extension.execute('memory.commitments', {}, ctx)
      const beforeCount = h.engine.memory.adminList().length
      const job = extension.execute(
        'learning.propose',
        candidate(
          m.id,
          { kind: 'confirm', target: initial.ref },
          'CANCELLED_CONTEXTUAL_COMMAND_CANARY',
        ),
        ctx,
      )
      if (revoke === 'learning_off') h.sceneFlags({ learningEnabled: false })
      else if (revoke === 'source_recall') h.engine.runtime.recall(h.scene.id, m.id)
      else
        saved(h.save('concurrent-queue-confirm-b', bob, { kind: 'confirm', target: initial.ref }))
      const receipt = (await job) as { status: string }
      assert.equal(receipt.status, 'queued')
      await h.engine.contextualLearning.queue.whenIdle()
      assert.ok(
        !h.engine.memory
          .adminList()
          .some((r) => r.content === 'CANCELLED_CONTEXTUAL_COMMAND_CANARY'),
      )
      assert.equal(h.engine.contextualLearning.queue.stats().completed, 0)
      assert.equal(
        h.engine.memory.adminList().length,
        beforeCount + (revoke === 'state_change' ? 1 : 0),
      )
      assert.equal(
        h.current(initial.ref).confirmation,
        revoke === 'state_change' ? 'confirmed' : 'proposed',
      )
    } finally {
      await h.close()
    }
  })

test('actual state-derived confirmation and amendment never persist expanded model text or raw chat; recalling proposal erases inherited private terms', async (t) => {
  clock(t)
  const h = fixture(),
    canary = 'PRIVATE_TERMS_CANARY_FROM_OLD_PROPOSAL',
    raw = 'FULL_RAW_CHAT_NOT_FOR_PERMANENT_MEMORY'
  try {
    const initial = await learnInitial(
      t,
      h,
      { ...terms(), title: canary },
      `${raw}: Synthetic proposal ${canary} at 18:00, date unknown.`,
    )
    await commandTurn(
      t,
      h,
      'only-agree',
      bob,
      (states) => ({ kind: 'confirm', target: states[0]!.ref }),
      {
        body: '同意。RAW_CONFIRMATION_CHAT_NOT_FOR_MEMORY',
        content: `${canary}: Bob confirms the private expanded terms.`,
      },
    )
    assert.equal(h.current(initial.ref).confirmation, 'confirmed')
    await commandTurn(
      t,
      h,
      'change-hour-only',
      alice,
      (states) => ({
        kind: 'amend',
        target: states[0]!.ref,
        terms: { ...states[0]!.terms!, timeQualification: '19:00; unknown date' },
      }),
      {
        body: '改成19点。RAW_AMENDMENT_CHAT_NOT_FOR_MEMORY',
        content: `${canary}: inherited private terms, except now 19:00.`,
      },
    )
    assert.equal(h.rows().length, 2)
    for (const table of ['memories', 'memory_assertion_revisions']) {
      const sql = JSON.stringify(h.store.db.prepare(`SELECT * FROM ${table}`).all())
      assert.doesNotMatch(
        sql,
        /PRIVATE_TERMS_CANARY|FULL_RAW_CHAT_NOT|RAW_CONFIRMATION_CHAT|RAW_AMENDMENT_CHAT/,
      )
    }
    assert.equal(h.engine.memory.adminList().length, 3)
    assert.ok(
      h.engine.memory
        .adminList()
        .every((r) => r.content.startsWith('Model-extracted plan declaration (')),
    )
    assert.deepEqual(h.engine.memory.list(h.ctx()), [])
    h.engine.runtime.recall(h.scene.id, h.message('automatic-proposal').id)
    assert.ok(h.rows().every((r) => r.terms === undefined))
    assert.ok(h.rows().some((r) => r.confirmation === 'incomplete'))
    for (const table of [
      'memory_commitment_heads',
      'memory_commitment_revisions',
      'memories',
      'memory_assertion_revisions',
    ])
      assert.ok(
        !JSON.stringify(h.store.db.prepare(`SELECT * FROM ${table}`).all()).includes(canary),
        table,
      )
    await h.reopen()
    assert.ok(h.rows().every((r) => r.terms === undefined))
    assert.equal(h.stats().sends, 0)
  } finally {
    await h.close()
  }
})

test('delivered commitment state is not free-form learning authority; learning OFF preserves reads and feature OFF hides schemas without restart replay', async (t) => {
  clock(t)
  const h = fixture()
  try {
    const initial = await learnInitial(t, h)
    const source = h.receive(h.message('only-agree-now', bob)),
      ctx = h.turn(source),
      ext = h.extension()
    await ext.execute('memory.commitments', {}, ctx)
    const input = candidate(source.id, {
      kind: 'propose',
      terms: terms('Unsupported expanded new plan'),
    })
    assert.deepEqual(await ext.execute('learning.propose', input, ctx), {
      status: 'rejected',
      reason: 'commitment_state_not_freeform_learning_authority',
    })
    const { commitment: _commitment, ...freeform } = input
    assert.deepEqual(await ext.execute('learning.propose', freeform, ctx), {
      status: 'rejected',
      reason: 'commitment_state_not_freeform_learning_authority',
    })
    h.sceneFlags({ learningEnabled: false })
    assert.equal(
      ((await ext.execute('memory.commitments', {}, h.turn(source))) as unknown[]).length,
      1,
    )
    assert.deepEqual(
      await ext.execute(
        'learning.propose',
        candidate(source.id, { kind: 'confirm', target: initial.ref }),
        h.turn(source),
      ),
      { status: 'rejected', reason: 'learning_disabled' },
    )
    h.receive(h.message('off-not-backlog'))
    h.settings({ memoryCommitmentsEnabled: false, memoryCommitmentsAnswerEnabled: false })
    assert.ok(ext.tools.every((tool) => tool.name !== 'memory.commitments'))
    assert.equal(
      Object.hasOwn(
        (
          ext.tools.find((tool) => tool.name === 'learning.propose')!.inputSchema as {
            properties: object
          }
        ).properties,
        'commitment',
      ),
      false,
    )
    assert.deepEqual(await ext.execute('memory.commitments', {}, h.turn(source)), [])
    const before = h.stats().models
    await h.reopen()
    h.sceneFlags({ learningEnabled: true })
    h.settings({ memoryCommitmentsEnabled: true, memoryCommitmentsAnswerEnabled: true })
    await advance(t, 300000)
    assert.equal(h.stats().models, before)
    assert.equal(h.rows()[0]!.confirmation, 'proposed')
    assert.equal(h.rows()[0]!.ref.id, initial.ref.id)
  } finally {
    await h.close()
  }
})

for (const operation of ['edit', 'delete'] as const)
  test(`activated 19:00 commitment loses an exact confirmation after ${operation}, without reverting to 18:00`, async () => {
    const h = fixture()
    try {
      const first = confirmed(h)
      const next = saved(
        h.save('event-amendment', alice, {
          kind: 'amend',
          target: first.ref,
          terms: terms('19:00; date unknown'),
        }),
      )
      const source = h.receive(h.message('event-confirm-b', bob))
      const backing = h.save(
        'event-confirm-b',
        bob,
        { kind: 'confirm', target: next.ref },
        { sources: [source] },
      )
      assert.equal(saved(backing).confirmation, 'confirmed')
      const before = h.engine.memory.commitments.storage.revisions(next.ref.id)
      if (operation === 'edit') {
        const result = h.adapter.edit(
          {
            ...source,
            origin: 'live',
            parts: [{ type: 'text', text: 'Synthetic correction: confirmation withdrawn.' }],
          },
          h.scene,
          {
            editId: 'explicit-confirmation-edit',
            order: { kind: 'observed_cas', expectedBase: source.sourceEventRef! },
          },
        )
        assert.equal(result.status, 'edited')
        assert.deepEqual(
          h.engine.memory.commitments.storage.revisions(next.ref.id),
          before,
          'editing changes current eligibility, not privacy history',
        )
      } else {
        h.engine.memory.delete(backing.memory.id)
        const sql = JSON.stringify(
          h.store.db.prepare('SELECT value FROM memory_commitment_revisions').all(),
        )
        assert.ok(
          !sql.includes(backing.memory.id),
          'terminal assertion delete removes all backing references',
        )
      }
      assert.equal(h.current(next.ref).confirmation, 'incomplete')
      assert.ok(!h.rows().some((r) => r.ref.version === 1 && r.confirmation === 'confirmed'))
      await h.reopen()
      assert.equal(h.current(next.ref).confirmation, 'incomplete')
      assert.equal(h.engine.memory.commitments.storage.head(next.ref.id)!.everActivatedVersion, 2)
    } finally {
      await h.close()
    }
  })

test('actual Engine state tools isolate private scenes and simulation performs no model, command or database mutation', async () => {
  const h = fixture()
  try {
    const first = confirmed(h)
    const source = h.receive(h.message('isolation-query')),
      extension = h.extension(),
      ordinary = h.turn(source)
    const privateContext = { ...ordinary, scene: h.other }
    assert.deepEqual(await extension.execute('memory.commitments', {}, privateContext), [])
    const tables = [
      'memory_commitment_heads',
      'memory_commitment_revisions',
      'memory_commitment_commands',
      'memories',
      'memory_assertion_revisions',
    ]
    const snapshot = () => tables.map((table) => h.store.db.prepare(`SELECT * FROM ${table}`).all())
    const before = snapshot(),
      gate = metadata(h.store.db)!
    const simulated: TurnContext = {
      ...ordinary,
      scene: { ...h.scene, kind: 'simulation' },
      mode: 'simulation',
    }
    assert.deepEqual(await extension.execute('memory.commitments', {}, simulated), [])
    assert.deepEqual(
      await extension.execute(
        'learning.propose',
        candidate(source.id, { kind: 'cancel', target: first.ref }),
        simulated,
      ),
      { status: 'rejected', reason: 'simulation_does_not_learn' },
    )
    assert.deepEqual(snapshot(), before)
    assert.deepEqual(metadata(h.store.db), gate)
    assert.equal(h.stats().models, 0)
    assert.equal(h.stats().sends, 0)
  } finally {
    await h.close()
  }
})

test('actual learning refuses reported, hypothetical or missing epistemic qualification instead of manufacturing explicit declarations', async (t) => {
  clock(t)
  const h = fixture()
  try {
    h.settings({ learningBatchMs: 100 })
    const kinds = ['reported', 'hypothesis', undefined]
    h.setExtract((messages) =>
      messages.map((m, i) => ({
        ...candidate(m.id, { kind: 'propose', terms: terms('18:00', [alice]) }),
        epistemicKind: kinds[i],
      })),
    )
    for (const id of ['reported-command', 'hypothesis-command', 'unqualified-command'])
      h.receive(h.message(id))
    await advance(t)
    assert.equal(h.stats().extracts, 1)
    assert.deepEqual(h.rows(), [])
    assert.deepEqual(h.engine.memory.adminList(), [])
    assert.equal(
      (
        h.store.db.prepare('SELECT count(*) AS n FROM memory_commitment_commands').get() as {
          n: number
        }
      ).n,
      0,
    )
  } finally {
    await h.close()
  }
})

test('typed commitment declaration markers remain usable by commitments but never become ordinary searchable memories or vector candidates', async () => {
  const h = fixture()
  try {
    confirmed(h)
    const m = h.receive(h.message('marker-query')),
      ctx = h.turn(m),
      extension = h.extension()
    const rows = h.engine.memory.adminList()
    assert.equal(rows.length, 2)
    assert.ok(rows.every((r) => r.internalUse === 'commitment_declaration'))
    assert.deepEqual(h.engine.memory.list(h.ctx()), [])
    assert.deepEqual(h.engine.memory.people(h.ctx()), [])
    assert.deepEqual(h.engine.memory.personProfile(h.ctx(), alice)!.records, [])
    assert.deepEqual(h.engine.memory.indexable(), [])
    assert.deepEqual(
      await extension.execute('memory.search', { query: 'Model-extracted plan declaration' }, ctx),
      [],
    )
    assert.deepEqual(await extension.execute('memory.person', { personId: alice }, ctx), [])
    assert.equal(((await extension.execute('memory.commitments', {}, ctx)) as unknown[]).length, 1)
    assert.equal(h.stats().models, 0)
  } finally {
    await h.close()
  }
})
