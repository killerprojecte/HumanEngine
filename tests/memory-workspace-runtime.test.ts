import assert from 'node:assert/strict'
import test, { type TestContext } from 'node:test'
import { mkdtempSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { setImmediate as immediate } from 'node:timers/promises'
import { Store } from '../src/store.ts'
import { Engine } from '../src/engine.ts'
import { createApp } from '../src/api.ts'
import type { ChatMessage, Completion, Router, ToolDefinition } from '../src/models.ts'
import type { MemoryRecord } from '../src/memory.ts'
import type { RuntimeExtension, RuntimeMessage, TurnContext } from '../src/runtime.ts'
import type { SceneRef } from '../src/onebot.ts'
import type { Settings } from '../src/schema.ts'
import type { CommitmentCommand, CommitmentRef } from '../src/memory/domain/commitments.ts'
import type { WorkingSetSelection } from '../src/memory/working-set/contracts.ts'
import { fixtureReplyPlan } from './helpers/reply-plan.ts'

// Synthetic router outputs establish transport/authorization contracts, not semantic quality or reward.
const scene: SceneRef = {
  id: 'workspace-group',
  kind: 'group',
  adapterId: 'offline-workspace',
  accountId: 'bot',
  platformSceneId: 'synthetic-group',
}
const other: SceneRef = {
  ...scene,
  id: 'workspace-private',
  kind: 'private',
  platformSceneId: 'synthetic-private',
}
const person = 'canonical-a',
  peer = 'canonical-b',
  token = 'synthetic-workspace-test'
const ctx = (sceneId = scene.id) => ({ sceneId, simulation: false })
const flags: Partial<Settings> = {
  memoryWorkingSetEnabled: true,
  memoryEpisodesEnabled: true,
  memoryCommitmentsEnabled: true,
  memoryCommitmentsAnswerEnabled: true,
  memoryHistoricalReadEnabled: true,
}
const call = (name: string, args: Record<string, unknown> = {}, id = name) => ({
  id,
  name,
  arguments: args,
})
const completion = (toolCalls: Completion['toolCalls'] = []): Completion => ({
  text: '',
  toolCalls,
  modelId: 'offline-workspace-script',
  usage: { inputTokens: 0, outputTokens: 0, totalTokens: 0 },
  assistant: { role: 'assistant', content: '', toolCalls },
})
const plan = (messages: ChatMessage[], patch: Parameters<typeof fixtureReplyPlan>[1] = {}) =>
  completion([call('reply.plan', fixtureReplyPlan(messages, patch)!, 'plan')])
function clock(t: TestContext) {
  t.mock.timers.enable({
    apis: ['Date', 'setTimeout', 'setInterval'],
    now: new Date('2026-10-29T01:00:00Z'),
  })
}
async function advance(t: TestContext, ms = 100) {
  t.mock.timers.tick(ms)
  for (let i = 0; i < 10; i++) {
    await immediate()
    t.mock.timers.tick(0)
  }
}
const source = (id: string, senderId = person): RuntimeMessage => ({
  id,
  sceneId: scene.id,
  senderId,
  senderDisplayName: 'Same nickname',
  origin: 'live',
  status: 'available',
  sentAt: new Date().toISOString(),
  receivedAt: new Date().toISOString(),
  parts: [
    { type: 'text', text: `Synthetic declaration ${id}.` },
    { type: 'mention', userId: senderId === person ? peer : person },
  ],
})
function fixture(initial: Partial<Settings> = flags) {
  const root = mkdtempSync(join(tmpdir(), 'he-workspace-runtime-')),
    path = join(root, 'synthetic.sqlite')
  let store: Store,
    engine: Engine,
    app: ReturnType<typeof createApp>,
    sends = 0,
    models = 0,
    sequence = 0
  let planner: ((messages: ChatMessage[], tools: ToolDefinition[]) => Completion) | undefined
  let reply: (() => Completion) | undefined
  const offered: ToolDefinition[][] = [],
    replyInputs: ChatMessage[][] = [],
    selections: Array<{ context: TurnContext; brief: unknown }> = []
  const open = () => {
    store = new Store(path)
    if (!store.get('workspace-fixture-initialized', false)) {
      store.set('settings', {
        ...store.settings,
        debounceMs: 10,
        maxWaitMs: 100,
        learningBatchMs: 300000,
        learningMaxWaitMs: 300000,
        ...initial,
      })
      store.set('workspace-fixture-initialized', true)
      for (const s of [scene, other])
        store.saveScene({
          ...s,
          name: 'Synthetic workspace',
          learningEnabled: false,
          participationEnabled: false,
          proactiveEnabled: false,
        })
    }
    engine = new Engine(store)
    engine.runtime.addAdapter(scene.adapterId, {
      send: async () => {
        sends++
        throw Error('Unexpected platform send')
      },
      history: async () => ({ items: [], coverage: 'complete' }),
      getMessage: async () => ({ items: [], coverage: 'complete' }),
      dispose() {},
    })
    engine.indexMemory = async () => {}
    engine.router = () =>
      ({
        complete: async (role, messages, tools = []) => {
          models++
          offered.push(structuredClone(tools))
          if (tools.some((t) => t.name === 'participation.decide'))
            return completion([
              call('participation.decide', { action: 'reply', reasonCode: 'synthetic_workspace' }),
            ])
          if (tools.some((t) => t.name === 'reply.plan')) {
            assert.ok(planner)
            return planner(messages, tools)
          }
          if (role === 'replyer') {
            replyInputs.push(structuredClone(messages))
            return reply?.() ?? completion()
          }
          throw Error('Unexpected learning or maintenance model call')
        },
      }) as Router
    const ext = (engine.runtime as unknown as { extensions: RuntimeExtension[] }).extensions.find(
      (e) => e.tools.some((t) => t.name === 'memory.search'),
    )!
    const selected = ext.contextSelected?.bind(ext)
    ext.contextSelected = (context, brief) => {
      selections.push({ context, brief: structuredClone(brief) })
      selected?.(context, brief)
    }
    app = createApp(store, { engine, token })
  }
  open()
  return {
    get store() {
      return store
    },
    get engine() {
      return engine
    },
    root,
    path,
    offered,
    replyInputs,
    selections,
    stats: () => ({ models, sends }),
    settings(patch: Partial<Settings>) {
      store.set('settings', { ...store.settings, ...patch })
      engine.settingsChanged()
    },
    record(
      content = 'SYNTHETIC_QUALIFIED_MEMORY: not confirmed; only for this occasion.',
      patch: Partial<Parameters<Engine['memory']['create']>[0]> = {},
    ) {
      return engine.memory.create({
        scope: { kind: 'scene', sceneId: scene.id, personId: person },
        kind: 'claim',
        epistemicKind: 'hypothesis',
        content,
        ...patch,
      })
    },
    extension() {
      return (engine.runtime as unknown as { extensions: RuntimeExtension[] }).extensions.find(
        (e) => e.tools.some((t) => t.name === 'memory.search'),
      )!
    },
    turn(id = 'direct-turn', simulation = false): TurnContext {
      return {
        scene,
        mode: simulation ? 'simulation' : 'real',
        turnId: id,
        signal: new AbortController().signal,
        dependencies: new Set(),
        validations: [],
        sourceSnapshots: [],
      }
    },
    async request(path: string, body?: unknown, authenticated = true) {
      return app.request(path, {
        ...(body === undefined ? {} : { method: 'POST', body: JSON.stringify(body) }),
        headers: {
          'Content-Type': 'application/json',
          ...(authenticated ? { Authorization: `Bearer ${token}` } : {}),
        },
      })
    },
    working(includeCold = true) {
      return engine.memory.workspace.workingSet(ctx(), { includeCold })
    },
    count(id: string) {
      const item = this.working().items.find(
        (item) => item.kind === 'assertion' && item.snapshot.record.id === id,
      )
      return item?.accessibility.contextSelectedCount ?? 0
    },
    truth() {
      return ['memories', 'memory_supports', 'memory_assertion_revisions', 'memory_assertion_state']
        .filter((table) =>
          store.db.prepare('SELECT name FROM sqlite_master WHERE name=?').get(table),
        )
        .map((table) => [table, store.db.prepare(`SELECT * FROM ${table}`).all()])
    },
    async ask(t: TestContext, fn: NonNullable<typeof planner>, replyFn?: () => Completion) {
      planner = fn
      reply = replyFn
      store.saveScene({ ...store.scene(scene.id)!, participationEnabled: true })
      engine.runtime.ingest(source(`workspace-query-${++sequence}`), scene)
      await advance(t, 10)
      await advance(t, 100)
      assert.equal(engine.runtime.activeTasks, 0)
    },
    async reopen() {
      await engine.dispose()
      store.close()
      planner = undefined
      reply = undefined
      open()
    },
    async close() {
      try {
        await engine.dispose()
      } finally {
        store.close()
        rmSync(root, { recursive: true, force: true })
      }
      assert.equal(sends, 0)
    },
  }
}
function select(h: ReturnType<typeof fixture>, record: MemoryRecord): WorkingSetSelection {
  const item = h
    .working()
    .items.find((item) => item.kind === 'assertion' && item.snapshot.record.id === record.id)!
  assert.ok(item)
  return { ref: item.accessibility.ref, dependencyKey: item.accessibility.dependencyKey }
}
function declaration(
  h: ReturnType<typeof fixture>,
  id: string,
  senderId: string,
  command: CommitmentCommand,
) {
  h.store.saveScene({ ...h.store.scene(scene.id)!, learningEnabled: true })
  const m = source(id, senderId),
    sourceRef = { sceneId: scene.id, messageId: id, senderId }
  const contextual = command.kind !== 'propose'
  const result = h.engine.memory.propose(
    {
      ...ctx(),
      provenance: contextual ? 'contextual' : 'source-only',
      dependencyMemoryIds: [],
      dependencyParentReferences: [],
      sourceEvidence: [
        {
          ...sourceRef,
          available: true,
          isBot: false,
          sourceText: m.parts
            .filter((p) => p.type === 'text')
            .map((p) => p.text)
            .join('\n'),
          sentAt: m.sentAt,
          receivedAt: m.receivedAt,
          mentions: [{ partIndex: 1, userId: senderId === person ? peer : person }],
        },
      ],
      ...(contextual
        ? {
            exposedCommitments: [
              {
                ...command.target,
                mutationVersion: h.engine.memory.commitments.storage.head(command.target.id)!
                  .mutationVersion,
              },
            ],
          }
        : {}),
    },
    {
      content: 'Model-generated expansion is not persistent authority.',
      kind: 'claim',
      epistemicKind: 'explicit',
      subject: { kind: 'self', sourceMessageId: id },
      sources: [sourceRef],
      commitment: command,
    },
  )
  assert.equal(result.status, 'saved')
  if (result.status !== 'saved') throw Error('fixture_declaration_rejected')
  assert.equal(result.commitment?.status, 'saved')
  assert.ok(result.commitment.item)
  h.store.saveScene({ ...h.store.scene(scene.id)!, learningEnabled: false })
  return result.commitment.item
}
function sharedPlan(h: ReturnType<typeof fixture>) {
  const first = declaration(h, 'plan-a', person, {
    kind: 'propose',
    terms: {
      kind: 'plan',
      title: 'Same display title',
      participantIds: [person, peer],
      modality: 'asserted',
      polarity: 'positive',
      conditions: [],
      temporalScope: 'future',
      timeQualification: '18:00; date and timezone unknown',
    },
  })
  return declaration(h, 'plan-b', peer, { kind: 'confirm', target: first.ref })
}

test('default OFF keeps actual Runtime baseline and administrator reads free of workspace counters or new model calls', async (t) => {
  clock(t)
  const h = fixture({})
  try {
    const record = h.record(),
      before = h.truth()
    assert.equal(h.store.settings.memoryWorkingSetEnabled, false)
    assert.equal(h.store.settings.memoryEpisodesEnabled, false)
    assert.equal(
      (await (await h.request(`/api/memory-working-set?sceneId=${scene.id}`)).json()).enabled,
      false,
    )
    assert.equal(
      (await (await h.request(`/api/memory-episodes?sceneId=${scene.id}`)).json()).enabled,
      false,
    )
    let rounds = 0
    await h.ask(t, (messages) => {
      if (++rounds === 1)
        return completion([call('memory.person', { personId: person }, 'ordinary-read')])
      const read = JSON.parse(messages.find((m) => m.role === 'tool')!.content)
      return plan(messages, {
        memorySelections: [{ evidenceId: read.id, memoryIds: [read.value[0].id] }],
      })
    })
    assert.equal(h.stats().models, 4)
    assert.equal(h.replyInputs.length, 1)
    assert.ok(
      h.offered.every((tools) =>
        tools.every((t) => !['memory.workingSet', 'memory.episodes'].includes(t.name)),
      ),
    )
    assert.equal(
      (
        h.store.db
          .prepare("SELECT count(*) AS n FROM sqlite_master WHERE name LIKE 'memory_working_set_%'")
          .get() as { n: number }
      ).n,
      0,
    )
    assert.deepEqual(h.truth(), before)
    assert.equal(h.engine.memory.list(ctx())[0]!.id, record.id)
  } finally {
    await h.close()
  }
})

test('cold and review metadata never changes truth or history; authorized person/history and includeCold recover omitted records', async (t) => {
  clock(t)
  const h = fixture()
  try {
    h.settings({ memoryColdAfterDays: 1, memoryReviewAfterDays: 1 })
    const record = h.record(),
      selection = select(h, record),
      before = h.truth()
    assert.equal(h.working().items[0]!.accessibility.state, 'hot')
    t.mock.timers.tick(2 * 86400_000)
    const cold = h.working().items[0]!
    assert.equal(cold.accessibility.state, 'cold')
    assert.equal(cold.accessibility.reviewDue, true)
    const hotOnly = h.working(false)
    assert.deepEqual(hotOnly.items, [])
    assert.equal(hotOnly.coverage, 'limited')
    assert.equal(hotOnly.omittedCold, 1)
    assert.equal(h.engine.memory.personProfile(ctx(), person)!.records[0]!.id, record.id)
    assert.ok(h.engine.memory.historyFor(ctx(), record.id)!.revisions.length)
    const response = await h.request(`/api/memory-working-set?sceneId=${scene.id}&includeCold=true`)
    assert.equal(response.status, 200)
    assert.equal((await response.json()).items[0].record.id, record.id)
    assert.equal(
      h.engine.memory.workspace.access.recordSelected(ctx(), {
        turnId: 'cold-selected',
        selectedAt: Date.now(),
        selections: [selection],
      }).recorded,
      1,
    )
    assert.equal(h.count(record.id), 1)
    assert.equal(h.working().items[0]!.accessibility.state, 'hot')
    assert.equal(h.working().items[0]!.accessibility.reviewDue, true)
    assert.deepEqual(h.truth(), before)
    assert.equal(h.stats().models, 0)
    await h.reopen()
    assert.equal(h.count(record.id), 1)
    assert.deepEqual(h.truth(), before)
  } finally {
    await h.close()
  }
})

test('actual API pin CAS cannot grant stale data; OFF can release existing intent and administrator reads never count selection', async () => {
  const h = fixture()
  try {
    const record = h.record(),
      selection = select(h, record)
    const path = '/api/memory-working-set/pin'
    const body = { sceneId: scene.id, selection, expectedRevision: 0, pinned: true }
    assert.equal((await h.request(path, body, false)).status, 401)
    assert.equal((await h.request(path, { ...body, sceneId: other.id })).status, 409)
    const pinned = await h.request(path, body)
    assert.equal(pinned.status, 200)
    assert.equal((await pinned.json()).policyRevision, 1)
    assert.equal((await h.request(path, { ...body, pinned: false })).status, 409)
    for (const route of ['memory-working-set', 'memory-episodes'])
      assert.equal((await h.request(`/api/${route}?sceneId=${scene.id}`)).status, 200)
    assert.equal(
      (await h.request('/api/memory-working-set/rebuild', { sceneId: scene.id })).status,
      200,
    )
    assert.equal(h.count(record.id), 0)
    h.engine.memory.update(record.id, { content: 'NEW_BODY_AFTER_PIN; not the old snapshot.' })
    assert.equal((await h.request(path, { ...body, expectedRevision: 1 })).status, 409)
    const current = h.working().items[0]!
    assert.equal(current.accessibility.pinStatus, 'stale')
    assert.equal(current.accessibility.pinned, false)
    h.settings({ memoryWorkingSetEnabled: false, memoryEpisodesEnabled: false })
    assert.equal(
      (await h.request(path, { ...body, expectedRevision: 1, pinned: false })).status,
      200,
    )
    await h.reopen()
    h.settings(flags)
    assert.equal(h.working().items[0]!.accessibility.pinRevision, 2)
    assert.equal(h.working().items[0]!.accessibility.pinIntent, false)
    assert.match(JSON.stringify(h.working()), /NEW_BODY_AFTER_PIN/)
    assert.equal(h.stats().models, 0)
  } finally {
    await h.close()
  }
})

test('same-title independent reported experiences remain separate with unknown occurrence, while plan completion retains each participant self-report', async () => {
  const h = fixture()
  try {
    h.record('Same display title', { kind: 'experience', epistemicKind: 'explicit' })
    h.record('Same display title', {
      kind: 'experience',
      epistemicKind: 'observed',
      scope: { kind: 'scene', sceneId: scene.id, personId: peer },
    })
    h.record('Other-scene private canary', {
      kind: 'experience',
      epistemicKind: 'explicit',
      scope: { kind: 'scene', sceneId: other.id, personId: person },
    })
    const shared = sharedPlan(h)
    declaration(h, 'done-a', person, { kind: 'complete', target: shared.ref })
    let page = h.engine.memory.workspace.episodes(ctx())
    assert.equal(page.items.length, 3)
    assert.equal(page.items.filter((item) => item.kind === 'reported_experience').length, 2)
    for (const item of page.items)
      if (item.kind === 'reported_experience') assert.equal(item.time.occurredAt, null)
    let entry = page.items.find((item) => item.kind === 'shared_plan_declaration')!
    assert.equal(entry.kind, 'shared_plan_declaration')
    if (entry.kind !== 'shared_plan_declaration') throw Error('fixture_plan_missing')
    assert.equal(entry.view.lifecycle, 'pending')
    assert.deepEqual(entry.view.completedParticipantIds, [person])
    declaration(h, 'done-b', peer, { kind: 'complete', target: shared.ref })
    page = h.engine.memory.workspace.episodes(ctx())
    entry = page.items.find((item) => item.kind === 'shared_plan_declaration')!
    assert.equal(entry.kind, 'shared_plan_declaration')
    if (entry.kind !== 'shared_plan_declaration') throw Error('fixture_plan_missing')
    assert.equal(entry.view.lifecycle, 'done')
    assert.equal(entry.completionBasis, 'participant_self_report_not_observed_event')
    assert.equal(entry.view.terms!.occurredAt, undefined)
    assert.equal(page.interpretation, 'independent_qualified_records_not_synthesized_episodes')
    const api = await (await h.request(`/api/memory-episodes?sceneId=${scene.id}`)).json()
    assert.equal(api.items.length, 3)
    assert.doesNotMatch(JSON.stringify(api), /Other-scene private canary/)
    assert.equal(
      api.items.find((item: { kind: string }) => item.kind === 'shared_plan_declaration').view
        .lifecycle,
      'done',
    )
    assert.equal(h.stats().models, 0)
  } finally {
    await h.close()
  }
})

test('open and needs-review plans stay prominent without inventing completion; done plans cool and recall restores needs-review protection', async (t) => {
  clock(t)
  const h = fixture()
  try {
    h.settings({ memoryColdAfterDays: 1 })
    const shared = sharedPlan(h)
    h.working()
    t.mock.timers.tick(2 * 86400_000)
    let item = h.working(false).items.find((item) => item.kind === 'commitment')!
    assert.equal(item.accessibility.state, 'hot')
    assert.equal(item.accessibility.protection, 'open_commitment')
    declaration(h, 'finish-a', person, { kind: 'complete', target: shared.ref })
    const done = declaration(h, 'finish-b', peer, { kind: 'complete', target: shared.ref })
    h.engine.memory.workspace.episodes(ctx())
    const ref = { kind: 'commitment' as const, ...done.ref, mutationVersion: done.mutationVersion }
    const snapshot = h.engine.memory.workspace.resolve(ctx(), [ref])[0]!
    assert.equal(
      h.engine.memory.workspace.access.view(ctx(), [
        { ref: snapshot.ref, dependencyKey: snapshot.dependencyKey },
      ]).items[0]!.state,
      'cold',
    )
    assert.ok(!h.working(false).items.some((item) => item.kind === 'commitment'))
    h.engine.runtime.recall(scene.id, 'finish-b')
    item = h.working(false).items.find((item) => item.kind === 'commitment')!
    assert.equal(item.kind, 'commitment')
    if (item.kind !== 'commitment') throw Error('fixture_plan_missing')
    assert.equal(item.view.lifecycle, 'needs_review')
    assert.equal(item.accessibility.protection, 'needs_review_commitment')
    assert.equal(item.accessibility.state, 'hot')
    assert.equal(h.stats().models, 0)
  } finally {
    await h.close()
  }
})

type Receipt = {
  id: string
  toolName: string
  value: any[]
  coverage?: { status: string; omittedRecords: number }
}
const receipts = (messages: ChatMessage[]): Receipt[] =>
  messages.filter((m) => m.role === 'tool').map((m) => JSON.parse(m.content))
const replyData = (h: ReturnType<typeof fixture>) => {
  assert.ok(h.replyInputs.length)
  return JSON.parse(h.replyInputs.at(-1)!.find((m) => m.role === 'user')!.content)
}

test('actual ordinary candidates are not uses: only final selected memory counts, independently of an empty Replyer result or sending', async (t) => {
  clock(t)
  const h = fixture()
  try {
    const a = h.record('SELECTED_CANDIDATE: not confirmed.'),
      b = h.record('UNSELECTED_CANDIDATE: only conditional.')
    const before = h.truth()
    let selectedId = '',
      rounds = 0
    await h.ask(t, (messages) => {
      if (++rounds === 1)
        return completion([call('memory.person', { personId: person }, 'read-candidates')])
      const read = receipts(messages)[0]!
      assert.equal(read.value.length, 2)
      const chosen = read.value.find((record) => record.content.startsWith('SELECTED_CANDIDATE'))
      selectedId = chosen.id
      return plan(messages, { memorySelections: [{ evidenceId: read.id, memoryIds: [chosen.id] }] })
    })
    assert.ok(selectedId)
    assert.equal(h.count(a.id), 1)
    assert.equal(h.count(b.id), 0)
    assert.equal(h.replyInputs.length, 1)
    assert.equal(h.stats().sends, 0)
    assert.deepEqual(h.truth(), before)
    const captured = h.selections.at(-1)!
    h.extension().contextSelected!(captured.context, captured.brief)
    assert.equal(h.count(a.id), 1, 'same turn and exact ref is idempotent')
    await h.reopen()
    assert.equal(h.count(a.id), 1)
  } finally {
    await h.close()
  }
})

for (const tool of ['memory.workingSet', 'memory.episodes'] as const)
  test(`actual ${tool} whole-page handoff preserves qualifiers and counts each retained assertion once across duplicate receipts`, async (t) => {
    clock(t)
    const h = fixture()
    try {
      const a = h.record('QUALIFIED_ENTRY_A: only this occasion, not externally verified.', {
        kind: 'experience',
        epistemicKind: 'explicit',
      })
      const b = h.record('QUALIFIED_ENTRY_B: separate observation, occurrence time unknown.', {
        kind: 'experience',
        epistemicKind: 'observed',
        scope: { kind: 'scene', sceneId: scene.id, personId: peer },
      })
      const shared = sharedPlan(h)
      declaration(h, 'workspace-self-done-a', person, { kind: 'complete', target: shared.ref })
      const before = h.truth()
      let rounds = 0
      await h.ask(t, (messages) => {
        if (++rounds === 1)
          return completion([call(tool, {}, 'read-one'), call(tool, {}, 'read-again')])
        const reads = receipts(messages)
        assert.equal(reads.length, 2)
        assert.ok(reads.every((r) => r.value.length === 1 && r.value[0].page.items.length === 3))
        return plan(messages, {
          stateEvidenceIds: reads.map((r) => r.id),
          facts: [
            {
              text: 'These are independent qualified records and participant self-reports, not proof of a shared real-world event.',
              evidenceIds: [reads[0]!.id],
            },
          ],
        })
      })
      const data = replyData(h)
      assert.equal(data.evidence.length, 2)
      for (const read of data.evidence) {
        const state = read.states[0]
        assert.equal(state.page.items.length, 3)
        assert.match(JSON.stringify(state), /QUALIFIED_ENTRY_A|QUALIFIED_ENTRY_B/)
        const entry = state.page.items.find(
          (item: any) => item.kind === 'commitment' || item.kind === 'shared_plan_declaration',
        )
        assert.equal(entry.view.lifecycle, 'pending')
        assert.equal(entry.view.qualification, 'declared_not_verified')
        assert.equal(entry.view.completedParticipantIds.length, 1)
        assert.equal(entry.view.terms.occurredAt, undefined)
        if (tool === 'memory.episodes') {
          assert.equal(
            state.page.interpretation,
            'independent_qualified_records_not_synthesized_episodes',
          )
          assert.equal(entry.completionBasis, 'participant_self_report_not_observed_event')
          assert.ok(
            state.page.items
              .filter((item: any) => item.kind === 'reported_experience')
              .every((item: any) => item.time.occurredAt === null),
          )
        }
      }
      assert.equal(h.count(a.id), 1)
      assert.equal(h.count(b.id), 1)
      assert.deepEqual(h.truth(), before)
      assert.equal(h.stats().models, 4)
      const rows = h.engine.memory.workspace.access.storage.listPins(scene.id, 10)
      assert.deepEqual(rows, [])
    } finally {
      await h.close()
    }
  })

for (const stage of ['planner', 'replyer'] as const)
  test(`actual ${stage} byte omission removes the whole workspace page and dependent conclusion without counting or claiming absence`, async (t) => {
    clock(t)
    const h = fixture()
    try {
      const record = h.record(
        `OMITTED_QUALIFIED_BODY ${'中文限制'.repeat(4000)} Only if expressly confirmed.`,
      )
      h.settings({
        replySuggestionsEnabled: true,
        ...(stage === 'planner'
          ? { plannerEvidenceMaxBytes: 32768 }
          : { replyEvidenceMaxBytes: 4096 }),
      })
      let rounds = 0
      await h.ask(t, (messages) => {
        if (++rounds === 1) return completion([call('memory.workingSet', {}, 'large-page')])
        const read = receipts(messages)[0]!
        if (stage === 'planner') {
          assert.deepEqual(read.value, [])
          assert.equal(read.coverage!.omittedRecords, 1)
          h.engine.memory.update(record.id, { locked: true })
        } else assert.equal(read.value.length, 1)
        return plan(messages, {
          ...(stage === 'replyer'
            ? {
                stateEvidenceIds: [read.id],
                facts: [{ text: 'OMITTED_STATE_CONCLUSION', evidenceIds: [read.id] }],
              }
            : {}),
          replySuggestion: 'OMITTED_STATE_DRAFT: there were no remembered events.',
          expressionGoals: ['OMITTED_STATE_GOAL'],
          constraints: ['Unknown evidence is not evidence of absence.'],
        })
      })
      const data = replyData(h),
        wire = h.replyInputs.at(-1)!.find((m) => m.role === 'user')!.content
      assert.equal(data.memoryEvidenceCoverage.status, 'omitted')
      assert.equal(data.evidence.length, 0)
      assert.equal(data.plan.facts.length, 0)
      assert.doesNotMatch(wire, /OMITTED_QUALIFIED_BODY|OMITTED_STATE_/)
      assert.equal(h.count(record.id), 0)
      if (stage === 'replyer') assert.ok(Buffer.byteLength(wire, 'utf8') <= 4096)
    } finally {
      await h.close()
    }
  })

test('direct preparation, copies, simulation and stale final selections never create context-selected evidence', async () => {
  const h = fixture()
  try {
    const record = h.record(),
      extension = h.extension(),
      context = { ...h.turn('exact-first'), deferMemoryExposure: true }
    const prepared = (await extension.execute('memory.workingSet', {}, context)) as unknown[]
    assert.equal(prepared.length, 1)
    assert.equal(h.count(record.id), 0)
    assert.deepEqual(
      extension.delivered!('memory.workingSet', structuredClone(prepared), context),
      [],
    )
    assert.equal(h.count(record.id), 0)
    assert.deepEqual(extension.delivered!('memory.workingSet', prepared, context), prepared)
    const selection = select(h, record)
    h.engine.memory.update(record.id, { locked: true })
    const result = h.engine.memory.workspace.access.recordSelected(ctx(), {
      turnId: 'late-selection',
      selectedAt: Date.now(),
      selections: [selection],
    })
    assert.equal(result.recorded, 0)
    assert.equal(result.unavailable, 1)
    assert.equal(
      context.validations.every((check) => check()),
      false,
    )
    const before = h.store.db.prepare('SELECT total_changes() AS n').get()
    const simulation = h.turn('simulation', true)
    assert.deepEqual(await extension.execute('memory.workingSet', {}, simulation), [])
    assert.deepEqual(await extension.execute('memory.episodes', {}, simulation), [])
    extension.contextSelected!(simulation, { evidence: [{ memories: [{ id: record.id }] }] })
    assert.deepEqual(h.store.db.prepare('SELECT total_changes() AS n').get(), before)
    assert.equal(h.count(record.id), 0)
    assert.deepEqual(h.engine.memory.workspace.episodes(ctx(other.id)).items, [])
    assert.equal(h.stats().models, 0)
  } finally {
    await h.close()
  }
})

test('actual ordinary exposure callback failure cancels the Planner before contextual actions or Replyer instead of reporting normal delivery', async (t) => {
  clock(t)
  const h = fixture()
  try {
    h.record('CALLBACK_FAILURE_BODY')
    const before = h.truth(),
      extension = h.extension(),
      original = extension.delivered!.bind(extension)
    let failedValidation = false,
      rounds = 0
    const resolve = h.engine.memory.workspace.resolve.bind(h.engine.memory.workspace)
    h.engine.memory.workspace.resolve = () => {
      throw Error('synthetic_workspace_selection_callback_failure')
    }
    extension.delivered = (...args) => {
      try {
        return original(...args)
      } catch (error) {
        failedValidation = args[2].validations.some((check) => !check())
        throw error
      }
    }
    try {
      await h.ask(t, (messages) => {
        if (++rounds === 1)
          return completion([
            call('memory.person', { personId: person }, 'read-before-callback-failure'),
          ])
        return plan(messages, {
          actions: [
            {
              toolName: 'learning.propose',
              arguments: {
                content: 'MUST_NOT_CONTINUE_AFTER_CALLBACK_FAILURE',
                kind: 'claim',
                sourceMessageIds: ['not-delivered'],
              },
            },
          ],
        })
      })
    } finally {
      h.engine.memory.workspace.resolve = resolve
    }
    assert.equal(rounds, 1)
    assert.equal(failedValidation, true)
    assert.equal(h.replyInputs.length, 0)
    assert.equal(h.selections.length, 0)
    assert.equal(h.engine.contextualLearning.queue.stats().accepted, 0)
    assert.ok(h.store.logs.some((log) => log.stage === 'planner' && log.status === 'failed'))
    assert.deepEqual(h.truth(), before)
  } finally {
    await h.close()
  }
})

function learned(h: ReturnType<typeof fixture>, m: RuntimeMessage, content: string) {
  h.engine.runtime.ingest(m, scene, false)
  const live = h.engine.runtime.preview(scene.id).find((p) => p.id === m.id)! as RuntimeMessage
  h.store.saveScene({ ...h.store.scene(scene.id)!, learningEnabled: true })
  const evidence = {
    sceneId: scene.id,
    messageId: live.id,
    senderId: live.senderId,
    eventRef: live.sourceEventRef,
    available: true,
    isBot: false,
    sentAt: live.sentAt,
    receivedAt: live.receivedAt,
  }
  const result = h.engine.memory.propose(
    { ...ctx(), provenance: 'source-only', sourceEvidence: [evidence] },
    {
      content,
      kind: 'experience',
      epistemicKind: 'explicit',
      subject: { kind: 'self', sourceMessageId: live.id },
      sources: [evidence],
    },
  )
  h.store.saveScene({ ...h.store.scene(scene.id)!, learningEnabled: false })
  assert.equal(result.status, 'saved')
  if (result.status !== 'saved') throw Error('fixture_learning_rejected')
  return { memory: result.memory, live }
}
function exactSelection(h: ReturnType<typeof fixture>, record: MemoryRecord): WorkingSetSelection {
  const state = h.engine.memory.revisionState(record.id)
  const snapshot = h.engine.memory.workspace.resolve(ctx(), [
    {
      kind: 'assertion',
      id: record.id,
      version: state.version,
      deletionEpoch: state.deletionEpoch,
    },
  ])[0]!
  assert.ok(snapshot)
  return { ref: snapshot.ref, dependencyKey: snapshot.dependencyKey }
}

test('real source edit changes OR dependency watermark without assertion revision; old selection and old lease cannot regain authority', async () => {
  const h = fixture()
  try {
    h.settings({ sourceEventRevisionsEnabled: true })
    const first = learned(h, source('versioned-proof-a'), 'EXACT_OR_EXPERIENCE')
    const second = learned(h, source('versioned-proof-b'), 'EXACT_OR_EXPERIENCE')
    assert.equal(first.memory.id, second.memory.id)
    const before = h.engine.memory.revisionState(first.memory.id),
      selection = exactSelection(h, second.memory)
    h.engine.memory.workspace.access.enqueue(ctx(), [selection])
    const lease = h.engine.memory.workspace.access
      .claim(ctx())
      .find((lease) => lease.ref.id === first.memory.id)!
    assert.ok(lease)
    const changed = h.engine.runtime.edit(
      {
        ...first.live,
        parts: [{ type: 'text', text: 'Explicit correction of the first assertion only.' }],
      },
      scene,
      {
        editId: 'first-proof-edit',
        order: { kind: 'observed_cas', expectedBase: first.live.sourceEventRef! },
      },
      false,
    )
    assert.equal(changed.status, 'edited')
    assert.deepEqual(
      h.engine.memory.revisionState(first.memory.id),
      before,
      'source head drift is not an assertion revision',
    )
    const current = exactSelection(h, second.memory)
    assert.deepEqual(current.ref, selection.ref)
    assert.notEqual(current.dependencyKey, selection.dependencyKey)
    assert.equal(
      h.engine.memory.workspace.access.recordSelected(ctx(), {
        turnId: 'late-old-hash',
        selectedAt: Date.now(),
        selections: [selection],
      }).recorded,
      0,
    )
    assert.equal(h.engine.memory.workspace.access.complete(ctx(), [lease]).completed, 0)
    const page = h.working()
    const item = page.items.find(
      (item) => item.kind === 'assertion' && item.snapshot.record.id === first.memory.id,
    )!
    assert.equal(item.kind, 'assertion')
    if (item.kind !== 'assertion') throw Error('fixture_or_missing')
    assert.deepEqual(
      item.snapshot.record.sources.map((s) => s.messageId),
      ['versioned-proof-b'],
    )
    assert.equal(item.accessibility.dependencyKey, current.dependencyKey)
    const sql = JSON.stringify(
      h.store.db.prepare('SELECT value FROM memory_working_set_items').all(),
    )
    assert.doesNotMatch(sql, /EXACT_OR_EXPERIENCE|Explicit correction/)
    await h.reopen()
    assert.equal(h.working().items[0]!.accessibility.dependencyKey, current.dependencyKey)
    assert.equal(h.stats().models, 0)
  } finally {
    await h.close()
  }
})

test('actual failed handoff leaves no usable parent grant even for a direct subsequent learning action on the same live context', async () => {
  const h = fixture()
  try {
    h.record('CALLBACK_PARENT_PREMISE')
    h.store.saveScene({ ...h.store.scene(scene.id)!, learningEnabled: true })
    const message = source('callback-child-source'),
      context = { ...h.turn('callback-direct'), sourceSnapshots: [message] },
      extension = h.extension()
    const resolve = h.engine.memory.workspace.resolve.bind(h.engine.memory.workspace)
    h.engine.memory.workspace.resolve = () => {
      throw Error('synthetic_workspace_callback_failure')
    }
    try {
      await assert.rejects(
        extension.execute('memory.search', { query: 'CALLBACK_PARENT_PREMISE' }, context),
        /synthetic_workspace_callback_failure/,
      )
    } finally {
      h.engine.memory.workspace.resolve = resolve
    }
    assert.equal(context.signal.aborted, false)
    assert.equal(
      context.validations.every((check) => check()),
      false,
    )
    const before = h.truth()
    assert.deepEqual(
      await extension.execute(
        'learning.propose',
        { content: 'MUST_NOT_SAVE_CALLBACK_CHILD', kind: 'claim', sourceMessageIds: [message.id] },
        context,
      ),
      { status: 'rejected', reason: 'context_ineligible' },
    )
    await h.engine.contextualLearning.queue.whenIdle()
    assert.deepEqual(h.truth(), before)
    assert.equal(h.engine.contextualLearning.queue.stats().accepted, 0)
  } finally {
    await h.close()
  }
})

test('real assertion commits enqueue only exact changed metadata, coalesce old leases and roll back dirty jobs with failed SQLite transactions', async () => {
  const h = fixture()
  try {
    const a = h.record('DIRTY_AUTHORITY_A'),
      b = h.record('DIRTY_AUTHORITY_B')
    const access = h.engine.memory.workspace.access,
      store = access.storage
    const sa = exactSelection(h, a),
      sb = exactSelection(h, b)
    const ja = store.job(scene.id, sa.ref),
      jb = store.job(scene.id, sb.ref)
    assert.ok(ja)
    assert.ok(jb)
    assert.equal(store.pending(scene.id), 2)
    assert.equal(store.item(scene.id, sa.ref), undefined)
    const baseline = {
      truth: h.truth(),
      jobs: h.store.db.prepare('SELECT * FROM memory_working_set_jobs ORDER BY entity_id').all(),
    }
    assert.throws(
      () =>
        h.store.db.transaction(() => {
          h.record('ROLLED_BACK_AUTHORITY_BODY')
          throw Error('synthetic_transaction_abort')
        })(),
      /synthetic_transaction_abort/,
    )
    assert.deepEqual(
      {
        truth: h.truth(),
        jobs: h.store.db.prepare('SELECT * FROM memory_working_set_jobs ORDER BY entity_id').all(),
      },
      baseline,
    )
    const leases = access.claim(ctx()),
      oldLease = leases.find((lease) => lease.ref.id === a.id)!
    const untouched = store.job(scene.id, sb.ref)
    h.engine.memory.update(a.id, { content: 'DIRTY_AUTHORITY_A_NEW' })
    const current = exactSelection(h, a),
      replacement = store.job(scene.id, current.ref)!
    assert.ok(replacement)
    assert.notEqual(replacement.generation, oldLease.generation)
    assert.deepEqual(store.job(scene.id, sb.ref), untouched)
    assert.equal(access.complete(ctx(), [oldLease]).completed, 0)
    assert.equal(
      access.complete(ctx(), [leases.find((lease) => lease.ref.id === b.id)!]).completed,
      1,
    )
    assert.equal(access.drain(ctx()).completed, 1)
    assert.equal(store.pending(scene.id), 0)
    const beforeClean = h.store.db.prepare('SELECT total_changes() AS n').get()
    assert.equal(access.drain(ctx()).completed, 0)
    assert.deepEqual(h.store.db.prepare('SELECT total_changes() AS n').get(), beforeClean)
    const metadata = JSON.stringify(
      h.store.db.prepare('SELECT value FROM memory_working_set_items').all(),
    )
    assert.doesNotMatch(metadata, /DIRTY_AUTHORITY|ROLLED_BACK/)
    await h.reopen()
    assert.equal(h.engine.memory.workspace.access.storage.pending(scene.id), 0)
    assert.equal(h.stats().models, 0)
  } finally {
    await h.close()
  }
})

test('actual source edit automatically invalidates only dependent rows and enqueues authorized OR survivors without scene-wide dirty replay', async () => {
  const h = fixture()
  try {
    h.settings({ sourceEventRevisionsEnabled: true })
    const a = learned(h, source('auto-or-a'), 'AUTOMATIC_OR_EXPERIENCE')
    const b = learned(h, source('auto-or-b'), 'AUTOMATIC_OR_EXPERIENCE')
    const unaffected = h.record('UNAFFECTED_EXPERIENCE', {
      kind: 'experience',
      epistemicKind: 'explicit',
    })
    h.working()
    const access = h.engine.memory.workspace.access,
      old = exactSelection(h, b.memory),
      otherSelection = exactSelection(h, unaffected)
    assert.equal(access.storage.pending(scene.id), 0)
    const independent = access.storage.item(scene.id, otherSelection.ref)
    const before = h.engine.memory.revisionState(b.memory.id)
    h.engine.runtime.edit(
      { ...a.live, parts: [{ type: 'text', text: 'First proof is corrected.' }] },
      scene,
      {
        editId: 'auto-source-edit',
        order: { kind: 'observed_cas', expectedBase: a.live.sourceEventRef! },
      },
      false,
    )
    assert.deepEqual(h.engine.memory.revisionState(b.memory.id), before)
    assert.equal(access.storage.item(scene.id, old.ref), undefined)
    const job = access.storage.job(scene.id, old.ref)!
    assert.ok(job)
    assert.notEqual(job.snapshot.dependencyKey, old.dependencyKey)
    assert.equal(access.storage.pending(scene.id), 1)
    assert.deepEqual(access.storage.item(scene.id, otherSelection.ref), independent)
    assert.equal(access.storage.job(scene.id, otherSelection.ref), undefined)
    assert.equal(access.drain(ctx()).completed, 1)
    assert.equal(h.working().items.filter((item) => item.kind === 'assertion').length, 2)
    assert.equal(h.stats().models, 0)
  } finally {
    await h.close()
  }
})

test('terminal deletion while OFF clears exact pin and all soft metadata; stale completion and restart cannot revive it or affect another scene', async () => {
  const h = fixture()
  try {
    const a = h.record('TERMINAL_PRIVATE_BODY'),
      b = h.record('TERMINAL_PRIVATE_BODY', {
        scope: { kind: 'scene', sceneId: other.id, personId: person },
      })
    const selection = select(h, a),
      access = h.engine.memory.workspace.access
    access.setPinned(ctx(), selection, { expectedRevision: 0, pinned: true })
    access.recordSelected(ctx(), {
      turnId: 'before-delete',
      selectedAt: Date.now(),
      selections: [selection],
    })
    h.engine.memory.update(a.id, { locked: true })
    const lease = access.claim(ctx()).find((lease) => lease.ref.id === a.id)!
    assert.ok(lease)
    h.settings({ memoryWorkingSetEnabled: false, memoryEpisodesEnabled: false })
    h.engine.memory.delete(a.id)
    const policy = access.storage.pin(scene.id, selection.ref)!
    assert.deepEqual(access.listPinPolicies(ctx()).items, [])
    assert.equal(policy.terminal, true)
    assert.equal(policy.pinned, false)
    for (const table of [
      'memory_working_set_items',
      'memory_working_set_jobs',
      'memory_working_set_receipts',
    ])
      assert.ok(
        !JSON.stringify(h.store.db.prepare(`SELECT * FROM ${table}`).all()).includes(a.id),
        table,
      )
    h.settings(flags)
    assert.equal(access.complete(ctx(), [lease]).completed, 0)
    assert.deepEqual(h.working().items, [])
    assert.equal(
      h.engine.memory.workspace.workingSet(ctx(other.id), { includeCold: true }).items[0]!.kind,
      'assertion',
    )
    assert.equal(h.engine.memory.list(ctx(other.id))[0]!.id, b.id)
    await h.reopen()
    assert.deepEqual(h.working().items, [])
    assert.equal(
      h.engine.memory.workspace.access.storage.pin(scene.id, selection.ref)!.terminal,
      true,
    )
    assert.equal(h.stats().models, 0)
  } finally {
    await h.close()
  }
})

test('actual Runtime source recall physically clears all soft metadata across versions, preserves nonterminal pin intent, and retains independent OR and other-scene evidence', async () => {
  const h = fixture()
  try {
    const single = learned(h, source('privacy-single'), 'PRIVATE_SOFT_METADATA_CANARY')
    learned(h, source('privacy-or-a'), 'INDEPENDENT_OR_CANARY')
    const independent = learned(h, source('privacy-or-b'), 'INDEPENDENT_OR_CANARY')
    const foreign = h.record('PRIVATE_SOFT_METADATA_CANARY', {
      scope: { kind: 'scene', sceneId: other.id, personId: peer },
      kind: 'experience',
      epistemicKind: 'explicit',
    })
    const selected = select(h, single.memory),
      orSelection = select(h, independent.memory),
      access = h.engine.memory.workspace.access
    const pin = access.setPinned(ctx(), selected, { expectedRevision: 0, pinned: true })
    access.recordSelected(ctx(), {
      turnId: 'before-private-recall',
      selectedAt: Date.now(),
      selections: [selected, orSelection],
    })
    h.engine.memory.update(single.memory.id, { locked: true })
    const lease = access.claim(ctx()).find((item) => item.ref.id === single.memory.id)!
    assert.ok(lease)
    h.settings({ memoryWorkingSetEnabled: false, memoryEpisodesEnabled: false })
    h.engine.runtime.recall(scene.id, 'privacy-single')
    for (const table of [
      'memory_working_set_items',
      'memory_working_set_jobs',
      'memory_working_set_receipts',
    ])
      assert.ok(
        !JSON.stringify(h.store.db.prepare(`SELECT * FROM ${table}`).all()).includes(
          single.memory.id,
        ),
        `${table} must physically purge person/time/version metadata, not merely hide it`,
      )
    assert.deepEqual(
      access.storage.pin(scene.id, selected.ref),
      pin,
      'source recall is not terminal deletion or explicit unpin',
    )
    h.settings(flags)
    assert.equal(access.complete(ctx(), [lease]).completed, 0)
    assert.ok(
      !h
        .working()
        .items.some(
          (item) => item.kind === 'assertion' && item.snapshot.ref.id === single.memory.id,
        ),
    )
    assert.ok(h.engine.memory.list(ctx()).some((item) => item.id === independent.memory.id))
    assert.equal(h.engine.memory.list(ctx(other.id))[0]!.id, foreign.id)
    h.engine.runtime.recall(scene.id, 'privacy-or-a')
    const surviving = h
      .working()
      .items.find(
        (item) => item.kind === 'assertion' && item.snapshot.ref.id === independent.memory.id,
      )!
    assert.equal(surviving.kind, 'assertion')
    if (surviving.kind !== 'assertion') throw Error('fixture_or_lost')
    assert.deepEqual(
      surviving.snapshot.record.sources.map((s) => s.messageId),
      ['privacy-or-b'],
    )
    assert.equal(
      surviving.accessibility.contextSelectedCount,
      0,
      'old proof-qualified counters are not silently rebound',
    )
    await h.reopen()
    for (const table of [
      'memory_working_set_items',
      'memory_working_set_jobs',
      'memory_working_set_receipts',
    ])
      assert.ok(
        !JSON.stringify(h.store.db.prepare(`SELECT * FROM ${table}`).all()).includes(
          single.memory.id,
        ),
        table,
      )
    assert.deepEqual(h.engine.memory.workspace.access.storage.pin(scene.id, selected.ref), pin)
    assert.equal(h.engine.memory.workspace.access.complete(ctx(), [lease]).completed, 0)
    assert.ok(
      h
        .working()
        .items.some(
          (item) => item.kind === 'assertion' && item.snapshot.ref.id === independent.memory.id,
        ),
    )
    assert.equal(h.stats().models, 0)
  } finally {
    await h.close()
  }
})
