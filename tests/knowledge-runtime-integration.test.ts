import assert from 'node:assert/strict'
import test, { type TestContext } from 'node:test'
import { mkdtempSync, rmSync } from 'node:fs'
import { join } from 'node:path'
import { tmpdir } from 'node:os'
import { setImmediate as immediate } from 'node:timers/promises'
import { Engine } from '../src/engine.ts'
import { Store } from '../src/store.ts'
import type { SceneRef } from '../src/onebot.ts'
import type { RuntimeMessage, RuntimeExtension, TurnContext } from '../src/runtime.ts'
import type { ChatMessage, Completion, Router } from '../src/models.ts'
import type {
  MemoryCandidate,
  MemoryContext,
  ProposalResult,
} from '../src/memory/domain/contracts.ts'
import type {
  KnowledgeProposal,
  KnowledgeQuery,
  KnowledgeEdge,
} from '../src/memory/knowledge/contracts.ts'
import { trustedSourceEvidence } from '../src/memory/learning/source-evidence.ts'
import { fixtureReplyPlan } from './helpers/reply-plan.ts'

// Scripted semantic candidates exercise authorization and retained qualifications only. They do not
// measure real-model extraction accuracy, quote recognition, usefulness, or production performance.
const scene: SceneRef = {
  id: 'knowledge-group',
  kind: 'group',
  adapterId: 'offline',
  accountId: 'account',
  platformSceneId: 'synthetic-group',
}
const other: SceneRef = {
  ...scene,
  id: 'knowledge-private',
  kind: 'private',
  platformSceneId: 'private',
}
const person = 'canonical-person',
  peer = 'canonical-peer'
const scope = { sceneId: scene.id, simulation: false }
const query: KnowledgeQuery = {
  anchor: { kind: 'person', personId: person },
  predicates: ['uses_tool', 'has_property', 'depends_on'],
  depth: 2,
}
const call = (name: string, args: Record<string, unknown> = {}, id = name) => ({
  id,
  name,
  arguments: args,
})
const completion = (toolCalls: Completion['toolCalls'] = []): Completion => ({
  text: '',
  toolCalls,
  modelId: 'offline-knowledge',
  usage: { inputTokens: 0, outputTokens: 0, totalTokens: 0 },
  assistant: { role: 'assistant', content: '', toolCalls },
})
const plan = (messages: ChatMessage[], patch: Parameters<typeof fixtureReplyPlan>[1] = {}) =>
  completion([call('reply.plan', fixtureReplyPlan(messages, patch)!)])
const receipts = (
  messages: ChatMessage[],
): Array<{
  id: string
  toolName: string
  value: any[]
  coverage?: { status: string; omittedRecords: number }
}> => messages.filter((m) => m.role === 'tool').map((m) => JSON.parse(m.content))
function clock(t: TestContext) {
  t.mock.timers.enable({
    apis: ['Date', 'setTimeout', 'setInterval'],
    now: new Date('2026-10-29T10:00:00Z'),
  })
}
async function advance(t: TestContext, ms = 10) {
  t.mock.timers.tick(ms)
  for (let n = 0; n < 16; n++) {
    await immediate()
    t.mock.timers.tick(0)
  }
}
function fixture() {
  const directory = mkdtempSync(join(tmpdir(), 'he-knowledge-runtime-')),
    path = join(directory, 'synthetic.sqlite')
  let store: Store,
    engine: Engine,
    sequence = 0,
    sends = 0
  const requests: Array<{ role: string; messages: ChatMessage[] }> = [],
    replies: ChatMessage[][] = [],
    errors: unknown[] = []
  let planner: (messages: ChatMessage[]) => Completion | Promise<Completion> = (messages) =>
    plan(messages)
  const open = () => {
    store = new Store(path)
    if (!store.scene(scene.id)) {
      store.set('settings', {
        ...store.settings,
        debounceMs: 10,
        maxWaitMs: 100,
        learningBatchMs: 300000,
        learningMaxWaitMs: 300000,
        learningMinIntervalMs: 0,
        sourceEventRevisionsEnabled: true,
      })
      for (const s of [scene, other])
        store.saveScene({
          ...s,
          name: 'Synthetic knowledge',
          participationEnabled: false,
          learningEnabled: true,
          proactiveEnabled: false,
        })
    }
    engine = new Engine(store)
    engine.indexMemory = async () => {}
    engine.runtime.addAdapter('offline', {
      send: async () => {
        sends++
        throw Error('Unexpected synthetic send')
      },
      getMessage: async () => ({ items: [], coverage: 'complete' }),
      history: async () => ({ items: [], coverage: 'complete' }),
      dispose() {},
    })
    engine.router = () =>
      ({
        complete: async (role, messages, tools = []) => {
          requests.push({ role, messages: structuredClone(messages) })
          try {
            if (tools.some((tool) => tool.name === 'participation.decide'))
              return completion([
                call('participation.decide', {
                  action: 'reply',
                  reasonCode: 'synthetic_knowledge',
                }),
              ])
            if (tools.some((tool) => tool.name === 'reply.plan')) return await planner(messages)
            assert.equal(role, 'replyer', 'no unscheduled background model call')
            replies.push(structuredClone(messages))
            return completion()
          } catch (error) {
            errors.push(error)
            throw error
          }
        },
      }) as Router
  }
  open()
  const extension = () =>
    (engine.runtime as unknown as { extensions: RuntimeExtension[] }).extensions.find((e) =>
      e.tools.some((tool) => tool.name === 'memory.person'),
    )!
  const ingest = (
    id = `source-${++sequence}`,
    text = '😀I use Tool A.',
    target = scene,
    senderId = person,
  ): RuntimeMessage => {
    engine.runtime.ingest(
      {
        id,
        sceneId: target.id,
        senderId,
        senderDisplayName: 'Same nickname',
        origin: 'live',
        status: 'available',
        sentAt: new Date().toISOString(),
        receivedAt: new Date().toISOString(),
        parts: [
          { type: 'text', text: 'Header' },
          { type: 'mention', userId: peer },
          { type: 'text', text },
        ],
      },
      target,
      false,
    )
    return structuredClone(
      engine.runtime.preview(target.id).find((m) => m.id === id)! as RuntimeMessage,
    )
  }
  const context = (
    sources: RuntimeMessage[],
    patch: Partial<MemoryContext> = {},
  ): MemoryContext => ({
    ...scope,
    provenance: 'source-only',
    sourceEvidence: trustedSourceEvidence(sources, () => false),
    ...patch,
  })
  const proposal = (
    source: RuntimeMessage,
    patch: Partial<KnowledgeProposal> = {},
  ): KnowledgeProposal => ({
    registryVersion: 1,
    predicate: 'uses_tool',
    subject: { kind: 'self', personId: source.senderId },
    object: { kind: 'new', entityKind: 'tool', title: 'Tool A' },
    polarity: 'positive',
    modality: 'asserted',
    temporalScope: 'current',
    conditions: [],
    sourceSpans: [
      {
        messageId: source.id,
        partIndex: 2,
        start: 2,
        end: (source.parts[2] as { text: string }).text.length,
        stance: 'self',
      },
    ],
    ...patch,
  })
  const candidate = (
    source: RuntimeMessage,
    knowledge?: KnowledgeProposal[],
    patch: Partial<MemoryCandidate> = {},
  ): MemoryCandidate => ({
    content: `Independent declaration ${source.id}`,
    kind: 'fact',
    epistemicKind: 'explicit',
    subject: { kind: 'self', sourceMessageId: source.id },
    sources: [
      {
        sceneId: source.sceneId,
        messageId: source.id,
        senderId: source.senderId,
        ...(source.sourceEventRef ? { eventRef: source.sourceEventRef } : {}),
      },
    ],
    ...(knowledge ? { knowledge } : {}),
    ...patch,
  })
  return {
    get store() {
      return store
    },
    get engine() {
      return engine
    },
    get extension() {
      return extension()
    },
    requests,
    replies,
    ingest,
    context,
    proposal,
    candidate,
    flags(patch: Record<string, unknown> = {}) {
      store.set('settings', {
        ...store.settings,
        memoryKnowledgeEnabled: true,
        memoryKnowledgeAnswerEnabled: true,
        memoryKnowledgeAutoAdmissionEnabled: true,
        ...patch,
      })
    },
    write(
      source: RuntimeMessage,
      proposals = [proposal(source)],
      patch: Partial<MemoryCandidate> = {},
      ctx: Partial<MemoryContext> = {},
    ) {
      return engine.memory.propose(context([source], ctx), candidate(source, proposals, patch))
    },
    seed(text = '😀I use Tool A.', title = 'Tool A') {
      const source = ingest(undefined, text)
      const result = engine.memory.propose(
        context([source]),
        candidate(source, [
          proposal(source, { object: { kind: 'new', entityKind: 'tool', title } }),
        ]),
      )
      assert.equal(result.status, 'saved')
      if (result.status !== 'saved') throw Error('Unexpected proposal result')
      assert.equal(result.knowledge?.created.length, 1, JSON.stringify(result))
      return { source, memory: result.memory, edge: result.knowledge!.created[0]! }
    },
    query(input = query, ctx: Partial<MemoryContext> = {}) {
      return engine.memory.knowledge.query({ ...scope, provenance: 'source-only', ...ctx }, input)
    },
    turn(simulation = false): TurnContext {
      return {
        scene,
        mode: simulation ? 'simulation' : 'real',
        turnId: `direct-${++sequence}`,
        signal: new AbortController().signal,
        dependencies: new Set(),
        validations: [],
        sourceSnapshots: structuredClone(engine.runtime.preview(scene.id)),
      }
    },
    read(ctx: TurnContext, input = query) {
      return extension().execute(
        'memory.knowledge',
        input as unknown as Record<string, unknown>,
        ctx,
      ) as Promise<any[]>
    },
    async ask(t: TestContext, fn: typeof planner) {
      planner = fn
      store.saveScene({ ...store.scene(scene.id)!, participationEnabled: true })
      const source = ingest(`query-${++sequence}`)
      engine.runtime.ingest({ ...source, id: `ask-${++sequence}` }, scene)
      await advance(t)
      if (errors.length) throw errors.at(-1)
      assert.equal(engine.runtime.activeTasks, 0)
    },
    async reopen() {
      await engine.dispose()
      store.close()
      open()
    },
    async close() {
      try {
        await engine.dispose()
      } finally {
        store.close()
        rmSync(directory, { recursive: true, force: true })
      }
      assert.equal(sends, 0)
    },
  }
}
function saved(result: ProposalResult) {
  assert.equal(result.status, 'saved', JSON.stringify(result))
  if (result.status !== 'saved') throw Error('Unexpected proposal result')
  return result
}
function entity(edge: KnowledgeEdge) {
  assert.equal(edge.object.kind, 'entity')
  if (edge.object.kind !== 'entity') throw Error('Missing entity')
  return edge.object.entity
}

test('independent default-OFF flags, learning-OFF and simulation never create graph rows, while ordinary assertions remain independently lawful', async () => {
  const h = fixture()
  try {
    assert.equal(h.store.settings.memoryKnowledgeEnabled, false)
    assert.equal(h.store.settings.memoryKnowledgeAnswerEnabled, false)
    assert.equal(h.store.settings.memoryKnowledgeAutoAdmissionEnabled, false)
    const a = h.ingest('off'),
      result = saved(h.write(a))
    assert.equal(result.knowledge!.created.length, 0)
    assert.equal(result.knowledge!.rejected[0]!.reason, 'knowledge_disabled')
    assert.equal(h.engine.memory.adminList().length, 1)
    h.flags({ memoryKnowledgeAutoAdmissionEnabled: false, memoryKnowledgeAnswerEnabled: false })
    const b = h.seed()
    assert.equal(b.edge.status, 'candidate')
    h.flags()
    const before = h.engine.memory.adminList().length
    assert.equal(
      h.write(h.ingest('simulation'), undefined, {}, { simulation: true }).status,
      'rejected',
    )
    assert.equal(h.engine.memory.adminList().length, before)
    h.store.saveScene({ ...h.store.scene(scene.id)!, learningEnabled: false })
    assert.equal(h.write(h.ingest('learning-off')).status, 'rejected')
    assert.equal(h.engine.memory.knowledge.storage.rows().length, 1)
    assert.deepEqual(h.query(query, { simulation: true }).edges, [])
    await h.reopen()
    assert.equal(h.engine.memory.knowledge.storage.rows().length, 1)
  } finally {
    await h.close()
  }
})

test('actual ingress binds original UTF16 parts and exact event refs; surrogate splits, wrong parts, cross-scene and bot evidence cannot enter graph', async () => {
  const h = fixture()
  try {
    h.flags()
    const source = h.ingest('unicode', '😀Tool A条件声明'),
      original = h.context([source])
    assert.deepEqual(original.sourceEvidence![0]!.parts, [
      { type: 'text', text: 'Header' },
      { type: 'other' },
      { type: 'text', text: '😀Tool A条件声明' },
    ])
    const result = saved(
      h.engine.memory.propose(original, h.candidate(source, [h.proposal(source)])),
    )
    const edge = result.knowledge!.created[0]!
    assert.equal(edge.status, 'accepted')
    assert.equal(edge.spans[0]!.start, 2)
    assert.deepEqual(edge.spans[0]!.source.eventRef, source.sourceEventRef)
    assert.equal(
      JSON.stringify(h.engine.memory.knowledge.storage.rows()).includes('😀Tool A条件声明'),
      false,
    )
    for (const [id, patch] of [
      ['split', { partIndex: 2, start: 0, end: 1 }],
      ['mention', { partIndex: 1, start: 0, end: 1 }],
      ['range', { partIndex: 2, start: 0, end: 999 }],
    ] as const) {
      const bad = h.ingest(id)
      const rejected = saved(
        h.write(bad, [
          h.proposal(bad, { sourceSpans: [{ messageId: bad.id, stance: 'self', ...patch }] }),
        ]),
      )
      assert.equal(rejected.knowledge!.created.length, 0)
    }
    const cross = h.ingest('cross', '😀Cross scene', other)
    assert.equal(h.write(cross).status, 'rejected')
    const bot = h.ingest('bot')
    assert.equal(
      h.write(
        bot,
        undefined,
        {},
        { sourceEvidence: [{ ...h.context([bot]).sourceEvidence![0]!, isBot: true }] },
      ).status,
      'rejected',
    )
    assert.equal(h.engine.memory.knowledge.storage.rows().length, 1)
    await h.engine.runtime.edit(
      { ...source, parts: [{ type: 'text', text: 'changed source' }] },
      scene,
      { editId: 'unicode-edit', order: { kind: 'platform_sequence', sequence: 1 } },
    )
    assert.equal(
      h.engine.memory.propose(
        original,
        h.candidate(source, [h.proposal(source)], { content: 'LATE_OLD_TEXT' }),
      ).status,
      'rejected',
    )
    assert.deepEqual(h.query().edges, [])
  } finally {
    await h.close()
  }
})

test('self explicit low-risk auto-admission does not bleach quoted/reported negative possible conditional claims or merge equal titles', async () => {
  const h = fixture()
  try {
    h.flags()
    const positive = h.seed()
    for (const stance of ['quoted', 'reported'] as const) {
      const source = h.ingest(stance, '😀Someone said Tool A might not apply.')
      const p = h.proposal(source, {
        polarity: 'negative',
        modality: 'possible',
        temporalScope: 'past',
        conditions: ['Only if offline'],
        sourceSpans: [{ messageId: source.id, partIndex: 2, start: 2, end: 12, stance }],
      })
      assert.deepEqual(h.write(source, [p]), {
        status: 'rejected',
        reason: 'knowledge_stance_mismatch',
      })
      const result = saved(h.write(source, [p], { kind: 'claim', epistemicKind: 'reported' }))
      assert.equal(result.knowledge!.created[0]!.status, 'candidate')
      assert.notEqual(entity(result.knowledge!.created[0]!).id, entity(positive.edge).id)
    }
    const all = h.query()
    assert.equal(all.edges.length, 3)
    assert.equal(all.interpretation, 'attributed_claims_not_transitive_truth')
    for (const edge of all.edges.filter((e) => e.polarity === 'negative')) {
      assert.equal(edge.modality, 'possible')
      assert.equal(edge.applicability, 'past')
      assert.deepEqual(edge.conditions, ['Only if offline'])
      assert.equal(edge.validFrom, undefined)
    }
    const mismatch = h.ingest('wrong-self')
    assert.equal(
      saved(
        h.write(mismatch, [h.proposal(mismatch, { subject: { kind: 'self', personId: peer } })]),
      ).knowledge!.created.length,
      0,
    )
    await h.reopen()
    assert.equal(h.query().edges.length, 3)
  } finally {
    await h.close()
  }
})

test('real independent OR survives recall with exact new epoch while OFF, without reviving stale metadata revisions', async () => {
  const h = fixture()
  try {
    h.flags()
    const a = h.ingest('or-a'),
      b = h.ingest('or-b')
    const first = saved(
      h.engine.memory.propose(
        h.context([a]),
        h.candidate(a, undefined, { content: 'Same explicit independent assertion' }),
      ),
    )
    const p = h.proposal(b, {
      sourceSpans: [h.proposal(a).sourceSpans[0]!, h.proposal(b).sourceSpans[0]!],
    })
    const merged = saved(
      h.engine.memory.propose(
        h.context([a, b]),
        h.candidate(b, [p], { content: first.memory.content }),
      ),
    )
    assert.equal(merged.memory.id, first.memory.id)
    assert.equal(merged.knowledge!.created.length, 1)
    const before = merged.knowledge!.created[0]!.backing
    h.flags({ memoryKnowledgeEnabled: false })
    h.engine.runtime.recall(scene.id, a.id)
    h.flags()
    const kept = h.query().edges[0]!
    assert.ok(kept)
    assert.ok(kept.backing.deletionEpoch > before.deletionEpoch)
    assert.deepEqual(
      kept.spans.map((s) => s.source.messageId),
      [b.id],
    )
    assert.deepEqual(
      kept.proofs.flatMap((p) => p.sources.map((s) => s.messageId)),
      [b.id],
    )
    await h.reopen()
    assert.equal(h.query().edges.length, 1)
    h.engine.memory.update(first.memory.id, { locked: true })
    assert.deepEqual(h.query().edges, [])
    h.engine.runtime.recall(scene.id, 'unrelated')
    assert.deepEqual(
      h.query().edges,
      [],
      'unrelated recall must not bless a metadata-drifted backing',
    )
    h.engine.runtime.recall(scene.id, b.id)
    assert.equal(h.engine.memory.knowledge.storage.rows().length, 0)
    await h.reopen()
    assert.deepEqual(h.query().edges, [])
  } finally {
    await h.close()
  }
})

test('contextual existing entities require exact refs, persist only a hidden marker, and reverse typed dependencies physically purge on recall', async () => {
  const h = fixture()
  try {
    h.flags()
    const root = h.seed('😀I use my private tool.', 'PRIVATE_GRAPH_TITLE_CANARY'),
      target = entity(root.edge)
    const source = h.ingest('dependent', '😀RAW_CURRENT_CHAT_CANARY'),
      p = h.proposal(source, {
        predicate: 'has_property',
        property: 'duration',
        subject: { kind: 'existing', entityId: target.id },
        object: { kind: 'literal', valueType: 'number', value: 3, unit: 'hours' },
        sourceSpans: [
          { messageId: source.id, partIndex: 2, start: 2, end: 10, stance: 'asserted' },
        ],
      })
    const ctx = h.context([source], {
      provenance: 'contextual',
      exposedKnowledgeEntities: [{ entityId: target.id, backingRef: target.backing }],
    })
    const child = saved(
      h.engine.memory.propose(
        ctx,
        h.candidate(source, [p], { content: 'MODEL_COPIED_PRIVATE_GRAPH_TITLE_CANARY' }),
      ),
    )
    assert.equal(child.knowledge!.created.length, 1)
    assert.equal(child.memory.internalUse, 'knowledge_declaration')
    assert.doesNotMatch(child.memory.content, /PRIVATE_GRAPH|RAW_CURRENT_CHAT/)
    assert.equal(
      h.engine.memory.list(scope).some((m) => m.id === child.memory.id),
      false,
    )
    assert.equal(
      h.engine.memory.personProfile(scope, person).records.some((m) => m.id === child.memory.id),
      false,
    )
    assert.equal(
      h.engine.memory.search(scope, 'declaration').some((m) => m.id === child.memory.id),
      false,
    )
    const bad = h.ingest('unexposed')
    assert.equal(
      saved(
        h.write(
          bad,
          [
            {
              ...p,
              sourceSpans: h.proposal(bad).sourceSpans.map((s) => ({ ...s, stance: 'asserted' })),
            },
          ],
          {},
          { provenance: 'contextual' },
        ),
      ).knowledge!.created.length,
      0,
    )
    h.engine.runtime.recall(scene.id, root.source.id)
    assert.equal(h.engine.memory.knowledge.storage.rows().length, 0)
    assert.equal(
      JSON.stringify(h.store.db.prepare('SELECT * FROM memory_knowledge_edges').all()).includes(
        'PRIVATE_GRAPH_TITLE_CANARY',
      ),
      false,
    )
    assert.ok(
      h.engine.memory.adminList().some((m) => m.id === child.memory.id),
      'independent source-only marker can survive without copied graph prose',
    )
    await h.reopen()
    assert.equal(h.engine.memory.knowledge.storage.rows().length, 0)
  } finally {
    await h.close()
  }
})

const modelCandidate = (source: RuntimeMessage, knowledge?: KnowledgeProposal[]) => ({
  content: 'MODEL_EXPANDED_GRAPH_CANARY',
  kind: 'claim',
  epistemicKind: 'explicit',
  sourceMessageIds: [source.id],
  subject: { kind: 'self', sourceMessageId: source.id },
  ...(knowledge ? { knowledge } : {}),
})
const propertyProposal = (
  h: ReturnType<typeof fixture>,
  source: RuntimeMessage,
  target: ReturnType<typeof entity>,
): KnowledgeProposal =>
  h.proposal(source, {
    predicate: 'has_property',
    property: 'duration',
    subject: { kind: 'existing', entityId: target.id },
    object: { kind: 'literal', valueType: 'number', value: 3, unit: 'hours' },
    polarity: 'negative',
    modality: 'possible',
    temporalScope: 'future',
    validFrom: '2027-01-01T00:00:00Z',
    conditions: ['Only if the offline trial succeeds'],
    sourceSpans: h.proposal(source).sourceSpans.map((s) => ({ ...s, stance: 'asserted' })),
  })

test('actual silent source-only extraction creates the typed index within its existing model call, with original UTF16 parts and no graph-model side call', async (t) => {
  clock(t)
  const h = fixture()
  let extracts = 0
  try {
    h.flags()
    h.store.set('settings', { ...h.store.settings, learningBatchMs: 100, learningMaxWaitMs: 300 })
    h.engine.settingsChanged()
    h.engine.router = () =>
      ({
        complete: async (role, messages, tools = []) => {
          assert.equal(role, 'learning')
          assert.ok(tools.some((tool) => tool.name === 'learning.propose'))
          extracts++
          const source = JSON.parse(messages[1]!.content).messages[0]
          assert.deepEqual(
            source.textParts.map((p: { partIndex: number }) => p.partIndex),
            [0, 2],
          )
          assert.equal(source.textParts[1].text, '😀I use Tool A.')
          return completion([
            call('learning.propose', {
              content: 'Tool A is the stated tool.',
              kind: 'fact',
              epistemicKind: 'explicit',
              sourceMessageIds: [source.id],
              subject: { kind: 'self', sourceMessageId: source.id },
              knowledge: [
                {
                  registryVersion: 1,
                  predicate: 'uses_tool',
                  subject: { kind: 'self', personId: source.senderId },
                  object: { kind: 'new', entityKind: 'tool', title: 'Tool A' },
                  polarity: 'positive',
                  modality: 'asserted',
                  temporalScope: 'current',
                  conditions: [],
                  sourceSpans: [
                    {
                      messageId: source.id,
                      partIndex: 2,
                      start: 2,
                      end: source.textParts[1].text.length,
                      stance: 'self',
                    },
                  ],
                },
              ],
            }),
          ])
        },
      }) as Router
    const source = h.ingest('background')
    await advance(t, 100)
    assert.equal(extracts, 1)
    assert.equal(h.engine.memory.adminList().length, 1)
    const edge = h.query().edges[0]!
    assert.ok(edge)
    assert.equal(edge.status, 'accepted')
    assert.deepEqual(edge.spans[0]!.source.eventRef, source.sourceEventRef)
    await advance(t, 1000)
    assert.equal(extracts, 1)
  } finally {
    await h.close()
  }
})

test('actual Engine to Planner to Replyer retains whole two-hop numeric values, units, counterclaims and qualifications, not transitive facts', async (t) => {
  clock(t)
  const h = fixture()
  let rounds = 0
  try {
    h.flags()
    const root = h.seed(),
      target = entity(root.edge),
      source = h.ingest('property')
    saved(
      h.write(
        source,
        [propertyProposal(h, source, target)],
        { kind: 'claim' },
        {
          provenance: 'contextual',
          exposedKnowledgeEntities: [{ entityId: target.id, backingRef: target.backing }],
        },
      ),
    )
    const negative = h.ingest('negative')
    saved(
      h.write(
        negative,
        [
          h.proposal(negative, {
            polarity: 'negative',
            modality: 'possible',
            conditions: ['Only on weekdays'],
            temporalScope: 'unspecified',
          }),
        ],
        { kind: 'claim' },
      ),
    )
    await h.ask(t, (messages) => {
      if (++rounds === 1)
        return completion([call('memory.knowledge', query as unknown as Record<string, unknown>)])
      const read = receipts(messages)[0]!
      assert.equal(read.value[0].page.edges.length, 3)
      return plan(messages, {
        stateEvidenceIds: [read.id],
        constraints: ['Qualifications are not optional.'],
      })
    })
    assert.equal(h.replies.length, 1)
    const data = JSON.parse(h.replies[0]![1]!.content),
      wire = h.replies[0]![1]!.content
    const evidence = data.evidence.find((e: any) => e.states?.[0]?.stateKind === 'knowledge_graph')
    assert.ok(evidence, wire)
    const page = evidence.states[0].page,
      property = page.edges.find((e: any) => e.property === 'duration')
    assert.deepEqual(property.object, {
      kind: 'literal',
      valueType: 'number',
      value: 3,
      unit: 'hours',
    })
    assert.equal(property.polarity, 'negative')
    assert.equal(property.modality, 'possible')
    assert.equal(property.temporalScope, 'future')
    assert.equal(property.validFrom, '2027-01-01T00:00:00Z')
    assert.deepEqual(property.conditions, ['Only if the offline trial succeeds'])
    assert.equal(page.interpretation, 'attributed_claims_not_transitive_truth')
    assert.ok(
      page.edges.some((e: any) => e.temporalScope === 'unspecified' && e.polarity === 'negative'),
    )
    assert.ok(page.paths.some((p: string[]) => p.length === 2))
    assert.ok(
      page.paths.flat().every((id: string) => page.edges.some((edge: any) => edge.id === id)),
    )
    for (const edge of page.edges)
      for (const endpoint of [edge.subject, edge.object])
        if (endpoint.kind === 'entity')
          assert.ok(page.edges.some((item: any) => item.id === endpoint.entity.originEdgeId))
    assert.equal(h.requests.length, 4)
  } finally {
    await h.close()
  }
})

for (const stage of ['planner', 'replyer'] as const)
  test(`actual ${stage} byte budget omits a complete knowledge page and its dependent conclusions without retaining entity authority from an undelivered page`, async (t) => {
    clock(t)
    const h = fixture()
    let rounds = 0
    try {
      h.flags()
      const source = h.ingest('oversized')
      saved(
        h.write(
          source,
          [
            h.proposal(source, {
              conditions: Array.from(
                { length: 8 },
                (_, n) => `OMITTED_QUALIFIER_${n}_${'限定'.repeat(850)}`,
              ),
            }),
          ],
          { kind: 'claim' },
        ),
      )
      h.store.set('settings', {
        ...h.store.settings,
        replySuggestionsEnabled: true,
        ...(stage === 'planner'
          ? { plannerEvidenceMaxBytes: 32768 }
          : { replyEvidenceMaxBytes: 4096 }),
      })
      await h.ask(t, (messages) => {
        if (++rounds === 1)
          return completion([call('memory.knowledge', query as unknown as Record<string, unknown>)])
        const read = receipts(messages)[0]!
        if (stage === 'planner') {
          assert.deepEqual(read.value, [])
          assert.equal(read.coverage?.omittedRecords, 1)
          h.engine.memory.update(h.engine.memory.adminList()[0]!.id, { locked: true })
        } else assert.equal(read.value.length, 1)
        return plan(messages, {
          ...(stage === 'replyer'
            ? {
                stateEvidenceIds: [read.id],
                facts: [{ text: 'OMITTED_GRAPH_CONCLUSION', evidenceIds: [read.id] }],
              }
            : {}),
          replySuggestion: 'OMITTED_GRAPH_DRAFT',
          expressionGoals: ['OMITTED_GRAPH_GOAL'],
          constraints: ['Missing evidence is not an empty graph.'],
        })
      })
      assert.equal(h.replies.length, 1)
      const data = JSON.parse(h.replies[0]![1]!.content),
        wire = h.replies[0]![1]!.content
      assert.equal(data.memoryEvidenceCoverage.status, 'omitted')
      assert.deepEqual(data.evidence, [])
      assert.deepEqual(data.plan.facts, [])
      assert.doesNotMatch(wire, /OMITTED_QUALIFIER|OMITTED_GRAPH/)
      assert.match(wire, /Missing evidence/)
    } finally {
      await h.close()
    }
  })

test('copied, omitted, foreign-context and simulation receipts grant no graph authority; actual delivery refuses free-form learning but a fresh independent source-only turn can learn', async () => {
  const h = fixture()
  try {
    h.flags()
    const root = h.seed(),
      target = entity(root.edge),
      source = h.ingest('independent')
    const ctx = { ...h.turn(), deferMemoryExposure: true },
      prepared = await h.read(ctx),
      before = ctx.validations.length
    assert.equal(prepared.length, 1)
    assert.deepEqual(h.extension.delivered!('memory.knowledge', structuredClone(prepared), ctx), [])
    assert.deepEqual(h.extension.delivered!('memory.knowledge', [], ctx), [])
    const foreign = { ...h.turn(), scene: other }
    assert.deepEqual(h.extension.delivered!('memory.knowledge', prepared, foreign), [])
    assert.equal(ctx.validations.length, before)
    assert.equal(ctx.dependencies.size, 0)
    const anchored: KnowledgeQuery = {
      ...query,
      anchor: { kind: 'entity', entityId: target.id, backingRef: target.backing },
    }
    assert.deepEqual(await h.read(ctx, anchored), [])
    assert.equal(
      (
        (await h.extension.execute(
          'learning.propose',
          { ...modelCandidate(source), content: 'FRESH_INDEPENDENT_SOURCE' },
          ctx,
        )) as { status: string }
      ).status,
      'queued',
    )
    await h.engine.contextualLearning.queue.whenIdle()
    assert.ok(h.engine.memory.adminList().some((m) => m.content === 'FRESH_INDEPENDENT_SOURCE'))
    const delivered = h.turn()
    assert.equal((await h.read(delivered)).length, 1)
    assert.equal((await h.read(delivered, anchored)).length, 1)
    for (const tool of ['learning.propose', 'habits.propose', 'experience.propose'])
      assert.deepEqual(await h.extension.execute(tool, modelCandidate(source), delivered), {
        status: 'rejected',
        reason: 'knowledge_state_not_freeform_learning_authority',
      })
    const beforeSQL = h.engine.memory.knowledge.storage.rows()
    assert.deepEqual(await h.read(h.turn(true)), [])
    assert.deepEqual(h.engine.memory.knowledge.storage.rows(), beforeSQL)
  } finally {
    await h.close()
  }
})

for (const revoke of ['metadata', 'source-edit', 'answer-off', 'flags-aba'] as const)
  test(`actual first graph delivery cannot be rebound after ${revoke} before typed submission or Replyer`, async (t) => {
    clock(t)
    const h = fixture()
    let rounds = 0
    try {
      h.flags()
      const root = h.seed()
      await h.ask(t, async (messages) => {
        if (++rounds === 1)
          return completion([call('memory.knowledge', query as unknown as Record<string, unknown>)])
        const read = receipts(messages)[0]!
        assert.equal(read.value.length, 1)
        if (revoke === 'metadata') {
          h.engine.memory.update(root.memory.id, { locked: true })
          h.engine.memory.update(root.memory.id, { locked: false })
        } else if (revoke === 'source-edit')
          await h.engine.runtime.edit(
            { ...root.source, parts: [{ type: 'text', text: 'changed' }] },
            scene,
            { editId: 'changed', order: { kind: 'platform_sequence', sequence: 1 } },
          )
        else {
          h.flags({ memoryKnowledgeAnswerEnabled: false })
          if (revoke === 'flags-aba') h.flags()
        }
        return plan(messages, { stateEvidenceIds: [read.id] })
      })
      assert.equal(h.replies.length, 0)
      assert.equal(h.requests.length, 3)
      assert.equal(h.engine.contextualLearning.queue.stats().accepted, 0)
    } finally {
      await h.close()
    }
  })

for (const revoke of ['none', 'answer-off', 'source-recall', 'flags-aba'] as const)
  test(`actual noReply typed queue after whole graph delivery ${revoke === 'none' ? 'survives normal turn finish without raw-chat or graph-prose backing' : `is cancelled by ${revoke} before commit`}`, async (t) => {
    clock(t)
    const h = fixture()
    let rounds = 0
    try {
      h.flags()
      const root = h.seed(),
        target = entity(root.edge)
      await h.ask(t, (messages) => {
        if (++rounds === 1)
          return completion([call('memory.knowledge', query as unknown as Record<string, unknown>)])
        const read = receipts(messages)[0]!,
          projectedRoot = read.value[0].page.edges.find((e: any) => e.predicate === 'uses_tool')
        const data = JSON.parse(messages.find((m) => m.role === 'user')!.content),
          sourceId = data.planContract.contextMessageIds.at(-1)
        return plan(messages, {
          action: 'noReply',
          actions: [
            {
              toolName: 'learning.propose',
              arguments: {
                content: 'MODEL_EXPANDED_GRAPH_CANARY',
                kind: 'claim',
                epistemicKind: 'explicit',
                sourceMessageIds: [sourceId],
                subject: { kind: 'self', sourceMessageId: sourceId },
                knowledge: [
                  {
                    registryVersion: 1,
                    predicate: 'has_property',
                    property: 'duration',
                    subject: { kind: 'existing', entityId: projectedRoot.object.entity.id },
                    object: { kind: 'literal', valueType: 'number', value: 3, unit: 'hours' },
                    polarity: 'negative',
                    modality: 'possible',
                    temporalScope: 'future',
                    conditions: ['Only if approved'],
                    sourceSpans: [
                      { messageId: sourceId, partIndex: 2, start: 2, end: 10, stance: 'asserted' },
                    ],
                  },
                ],
              },
            },
          ],
        })
      })
      assert.equal(h.engine.contextualLearning.queue.stats().accepted, 1)
      assert.equal(h.engine.contextualLearning.queue.stats().queued, 1)
      if (revoke === 'answer-off') h.flags({ memoryKnowledgeAnswerEnabled: false })
      if (revoke === 'flags-aba') {
        h.flags({ memoryKnowledgeAnswerEnabled: false })
        h.flags()
      }
      if (revoke === 'source-recall') h.engine.runtime.recall(scene.id, root.source.id)
      await advance(t, 100)
      await h.engine.contextualLearning.queue.whenIdle()
      assert.equal(h.replies.length, 0)
      const markers = h.engine.memory
        .adminList()
        .filter((m) => m.internalUse === 'knowledge_declaration')
      if (revoke === 'none') {
        assert.equal(h.engine.contextualLearning.queue.stats().completed, 1)
        assert.equal(markers.length, 1)
        assert.doesNotMatch(markers[0]!.content, /MODEL_EXPANDED_GRAPH|Synthetic|I use/)
        const child = h.engine.memory.knowledge.storage
          .rows()
          .find((e) => e.predicate === 'has_property')!
        assert.ok(child)
        assert.equal(child.dependencies[0]!.entity.id, target.id)
        assert.equal(
          h.engine.memory.list(scope).some((m) => m.id === markers[0]!.id),
          false,
        )
      } else {
        assert.equal(markers.length, 0)
        assert.equal(h.engine.contextualLearning.queue.stats().completed, 0)
      }
    } finally {
      await h.close()
    }
  })

function seedGeneral(h: ReturnType<typeof fixture>) {
  const source = h.ingest('general-source', '😀General tool version is v2.')
  const result = saved(
    h.write(
      source,
      [
        h.proposal(source, {
          predicate: 'has_property',
          property: 'version',
          subject: { kind: 'new', entityKind: 'tool', title: 'General tool' },
          object: { kind: 'literal', valueType: 'text', value: 'v2' },
          sourceSpans: h
            .proposal(source)
            .sourceSpans.map((span) => ({ ...span, stance: 'asserted' })),
        }),
      ],
      { subject: undefined, content: 'General tool version declaration.' },
    ),
  )
  assert.equal(result.knowledge!.created.length, 1)
  assert.deepEqual(result.memory.scope, { kind: 'scene', sceneId: scene.id })
  return result
}

test('actual nonpersonal graph discovery requires this turn exact ordinary-memory delivery, not a copied, omitted or stale backing ID', async () => {
  const h = fixture()
  try {
    h.flags()
    const root = seedGeneral(h),
      input = {
        anchor: { kind: 'memory', memoryId: root.memory.id },
        predicates: ['has_property'],
        depth: 1,
      }
    const execute = (ctx: TurnContext) =>
      h.extension.execute('memory.knowledge', input, ctx) as Promise<any[]>
    assert.deepEqual(await execute(h.turn()), [])
    const omitted = { ...h.turn(), deferMemoryExposure: true }
    const prepared = (await h.extension.execute(
      'memory.search',
      { query: 'General tool version' },
      omitted,
    )) as any[]
    assert.equal(prepared.length, 1)
    assert.deepEqual(h.extension.delivered!('memory.search', [], omitted), [])
    assert.deepEqual(await execute(omitted), [])
    h.store.saveScene({ ...h.store.scene(scene.id)!, personalizationEnabled: false })
    const fresh = h.turn()
    const read = (await h.extension.execute(
      'memory.search',
      { query: 'General tool version' },
      fresh,
    )) as any[]
    assert.equal(read.length, 1)
    const page = await execute(fresh)
    assert.equal(page.length, 1)
    assert.equal(page[0].page.edges[0].object.value, 'v2')
    const before = fresh.validations.length
    h.engine.memory.update(root.memory.id, { locked: true })
    h.engine.memory.update(root.memory.id, { locked: false })
    assert.deepEqual(await execute(fresh), [])
    assert.ok(fresh.validations.some((valid) => !valid()))
    assert.equal(
      fresh.validations.length,
      before,
      'failed re-read cannot replace the first exact exposure',
    )
    await assert.rejects(h.read(h.turn()), /personalization_disabled/)
  } finally {
    await h.close()
  }
})

test('actual Planner can discover nonpersonal literal knowledge through an actually delivered memory ID with personalization OFF', async (t) => {
  clock(t)
  const h = fixture()
  let rounds = 0
  try {
    h.flags()
    seedGeneral(h)
    h.store.saveScene({ ...h.store.scene(scene.id)!, personalizationEnabled: false })
    await h.ask(t, (messages) => {
      if (++rounds === 1)
        return completion([call('memory.search', { query: 'General tool version' })])
      const reads = receipts(messages)
      if (rounds === 2) {
        assert.equal(reads[0]!.value.length, 1)
        return completion([
          call('memory.knowledge', {
            anchor: { kind: 'memory', memoryId: reads[0]!.value[0].id },
            predicates: ['has_property'],
            depth: 1,
          }),
        ])
      }
      const graph = reads.find((read) => read.toolName === 'memory.knowledge')!
      assert.equal(graph.value[0].page.edges.length, 1)
      return plan(messages, { stateEvidenceIds: [graph.id] })
    })
    assert.equal(h.replies.length, 1)
    const data = JSON.parse(h.replies[0]![1]!.content),
      graph = data.evidence.find((item: any) => item.toolName === 'memory.knowledge')
    assert.equal(graph.states[0].page.edges[0].object.value, 'v2')
    assert.equal(h.requests.length, 5)
  } finally {
    await h.close()
  }
})

test('personalization OFF refuses an entire general-anchored graph if a personal counterclaim is in its closure, rather than claiming a filtered complete result', async () => {
  const h = fixture()
  try {
    h.flags()
    const root = seedGeneral(h),
      edge = root.knowledge!.created[0]!
    assert.equal(edge.subject.kind, 'entity')
    if (edge.subject.kind !== 'entity') throw Error('Missing subject entity')
    const target = edge.subject.entity,
      source = h.ingest('personal-edge')
    const attached = saved(
      h.write(
        source,
        [
          h.proposal(source, {
            object: { kind: 'existing', entityId: target.id },
            polarity: 'negative',
            conditions: ['Only if personally requested'],
          }),
        ],
        { kind: 'claim' },
        {
          provenance: 'contextual',
          exposedKnowledgeEntities: [{ entityId: target.id, backingRef: target.backing }],
        },
      ),
    )
    assert.equal(attached.knowledge!.created.length, 1)
    const input = {
      anchor: { kind: 'memory', memoryId: root.memory.id },
      predicates: ['has_property', 'uses_tool'],
      depth: 2,
    }
    const context = h.turn()
    await h.extension.execute('memory.search', { query: 'General tool version' }, context)
    const full = (await h.extension.execute('memory.knowledge', input, context)) as any[]
    assert.equal(full[0].page.edges.length, 2)
    h.store.saveScene({ ...h.store.scene(scene.id)!, personalizationEnabled: false })
    const disabled = h.turn()
    const ordinary = (await h.extension.execute(
      'memory.search',
      { query: 'General tool version' },
      disabled,
    )) as any[]
    assert.equal(ordinary.length, 1)
    await assert.rejects(
      h.extension.execute('memory.knowledge', input, disabled),
      /personalization_disabled/,
    )
    assert.equal(
      h.engine.memory.knowledge.storage.rows().length,
      2,
      'model controls do not delete authorized administrative data',
    )
    assert.deepEqual(h.query(query, { sceneId: other.id }).edges, [])
  } finally {
    await h.close()
  }
})

async function queuedKnowledge(
  h: ReturnType<typeof fixture>,
  context: TurnContext,
  source: RuntimeMessage,
  proposal: KnowledgeProposal,
) {
  const before = new Set(h.engine.memory.knowledge.storage.rows().map((edge) => edge.id))
  const result = (await h.extension.execute(
    'learning.propose',
    modelCandidate(source, [proposal]),
    context,
  )) as { status: string }
  assert.equal(result.status, 'queued')
  await h.engine.contextualLearning.queue.whenIdle()
  const created = h.engine.memory.knowledge.storage.rows().filter((edge) => !before.has(edge.id))
  assert.equal(
    created.length,
    1,
    'the positive control must commit, not pass by rejecting all typed learning',
  )
  return created[0]!
}
function structuredSQL(h: ReturnType<typeof fixture>) {
  return JSON.stringify(h.store.db.prepare('SELECT * FROM memory_knowledge_edges').all())
}

for (const tool of ['memory.person', 'memory.search'] as const)
  test(`actual ${tool} exposure is inherited by a fully new typed declaration; source recall physically purges copied structured terms across restart`, async () => {
    const h = fixture(),
      canary = `ORDINARY_${tool.replace('.', '_')}_PRIVATE_CANARY`
    try {
      h.flags()
      const source = h.ingest('ordinary-private', '😀Private old premise')
      const old = saved(
        h.engine.memory.propose(
          h.context([source]),
          h.candidate(source, undefined, { content: canary }),
        ),
      )
      const fresh = h.ingest('ordinary-child', '😀Independent present declaration')
      const context = h.turn()
      const read = (await h.extension.execute(
        tool,
        tool === 'memory.person' ? { personId: person } : { query: canary },
        context,
      )) as any[]
      assert.ok(read.some((record) => record.id === old.memory.id))
      const child = await queuedKnowledge(
        h,
        context,
        fresh,
        h.proposal(fresh, { object: { kind: 'new', entityKind: 'tool', title: canary } }),
      )
      assert.ok(
        child.readDependencies.some((dep) => dep.backing.id === old.memory.id && !dep.edgeId),
      )
      assert.match(structuredSQL(h), new RegExp(canary))
      const independent = h.seed('😀Unrelated legal source', 'INDEPENDENT_LEGAL_TOOL')
      h.engine.runtime.recall(scene.id, source.id)
      assert.doesNotMatch(structuredSQL(h), new RegExp(canary))
      assert.equal(
        h.engine.memory.knowledge.storage.rows().some((edge) => edge.id === child.id),
        false,
      )
      assert.ok(
        h.engine.memory.knowledge.storage.rows().some((edge) => edge.id === independent.edge.id),
      )
      assert.ok(
        h.engine.memory.adminList().some((memory) => memory.id === child.backing.id),
        'the independent marker need not be deleted, but must never contain copied prose',
      )
      assert.doesNotMatch(
        h.engine.memory.adminList().find((memory) => memory.id === child.backing.id)!.content,
        new RegExp(canary),
      )
      await h.reopen()
      assert.doesNotMatch(structuredSQL(h), new RegExp(canary))
      assert.ok(h.query().edges.some((edge) => edge.id === independent.edge.id))
      const freshSource = h.ingest('fresh-after-private-recall')
      const freshResult = saved(h.write(freshSource))
      assert.equal(
        freshResult.knowledge!.created.length,
        1,
        'fresh source-only learning remains lawful',
      )
    } finally {
      await h.close()
    }
  })

test('whole graph exposure inherits unselected B even when a typed candidate names only existing A and copies B into a literal', async () => {
  const h = fixture(),
    canary = 'UNSELECTED_B_PRIVATE_GRAPH_CANARY'
  try {
    h.flags()
    const a = h.seed('😀Tool A declaration', 'PUBLIC_A'),
      b = h.seed('😀Tool B declaration', canary)
    const source = h.ingest('select-only-a', '😀A has a version')
    const context = h.turn(),
      page = await h.read(context)
    assert.ok(page[0].page.edges.some((edge: any) => edge.id === b.edge.id))
    const target = entity(a.edge)
    const child = await queuedKnowledge(
      h,
      context,
      source,
      h.proposal(source, {
        predicate: 'has_property',
        property: 'version',
        subject: { kind: 'existing', entityId: target.id },
        object: { kind: 'literal', valueType: 'text', value: canary },
        sourceSpans: h
          .proposal(source)
          .sourceSpans.map((span) => ({ ...span, stance: 'asserted' })),
      }),
    )
    assert.deepEqual(
      child.dependencies.map((dep) => dep.entity.id),
      [target.id],
    )
    assert.ok(
      child.readDependencies.some((dep) => dep.edgeId === b.edge.id),
      'required read closure is server-derived, not candidate-selected entities',
    )
    h.engine.runtime.recall(scene.id, b.source.id)
    assert.doesNotMatch(structuredSQL(h), new RegExp(canary))
    assert.deepEqual(
      h.query().edges.map((edge) => edge.id),
      [a.edge.id],
    )
    await h.reopen()
    assert.doesNotMatch(structuredSQL(h), new RegExp(canary))
    assert.equal(h.query().edges.length, 1)
  } finally {
    await h.close()
  }
})

test('a delivered person-to-literal graph with no entities still binds fully new typed learning to the complete source closure', async () => {
  const h = fixture(),
    canary = 'LITERAL_GRAPH_PRIVATE_CANARY'
  try {
    h.flags()
    const source = h.ingest('literal-root')
    const root = saved(
      h.write(
        source,
        [
          h.proposal(source, {
            predicate: 'prefers_topic',
            object: { kind: 'literal', valueType: 'text', value: canary },
            polarity: 'negative',
            modality: 'possible',
            conditions: ['Only under the original condition'],
          }),
        ],
        { kind: 'claim' },
      ),
    )
    const fresh = h.ingest('literal-child'),
      context = h.turn()
    const read = await h.read(context, {
      anchor: query.anchor,
      predicates: ['prefers_topic'],
      depth: 1,
    })
    assert.equal(read[0].page.edges.length, 1)
    assert.equal(read[0].page.edges[0].object.kind, 'literal')
    const child = await queuedKnowledge(
      h,
      context,
      fresh,
      h.proposal(fresh, { object: { kind: 'new', entityKind: 'tool', title: canary } }),
    )
    assert.deepEqual(child.dependencies, [])
    assert.ok(child.readDependencies.some((dep) => dep.edgeId === root.knowledge!.created[0]!.id))
    h.flags({ memoryKnowledgeEnabled: false })
    h.engine.runtime.recall(scene.id, source.id)
    assert.doesNotMatch(structuredSQL(h), new RegExp(canary))
    assert.equal(h.engine.memory.knowledge.storage.rows().length, 0)
    await h.reopen()
    assert.equal(h.engine.memory.knowledge.storage.rows().length, 0)
    h.flags()
    assert.equal(h.seed().edge.readDependencies.length, 0)
  } finally {
    await h.close()
  }
})

test('recalling an indirect parent physically removes only a graph proof depending on that parent, while its ordinary OR assertion and independent graph alternative survive', async () => {
  const h = fixture(),
    canary = 'INDIRECT_PRIVATE_PARENT_CANARY'
  try {
    h.flags()
    const p = h.ingest('indirect-parent')
    const parent = saved(
      h.engine.memory.propose(
        h.context([p]),
        h.candidate(p, undefined, { content: 'Private contextual premise.' }),
      ),
    )
    const parentRef = h.engine.memory.captureParentReference(scope, parent.memory)!
    assert.ok(parentRef)
    const a = h.ingest('dependent-a'),
      b = h.ingest('independent-b')
    const first = saved(
      h.engine.memory.propose(
        h.context([a], {
          provenance: 'contextual',
          dependencyMemoryIds: [parent.memory.id],
          dependencyParentReferences: [parentRef],
        }),
        h.candidate(a, undefined, { content: 'Same OR-supported assertion' }),
      ),
    )
    const second = saved(
      h.engine.memory.propose(
        h.context([b]),
        h.candidate(b, undefined, { content: first.memory.content }),
      ),
    )
    assert.equal(second.memory.id, first.memory.id)
    const state = h.engine.memory.revisionState(first.memory.id),
      ref = { id: first.memory.id, version: state.version, deletionEpoch: state.deletionEpoch }
    const admit = (source: RuntimeMessage, title: string) =>
      h.store.db.transaction(() =>
        h.engine.memory.knowledge.admit(
          { ...scope, provenance: 'contextual', sourceEvidence: h.context([a, b]).sourceEvidence },
          ref,
          [h.proposal(source, { object: { kind: 'new', entityKind: 'tool', title } })],
        ),
      )()
    const dependent = admit(a, canary),
      independent = admit(b, 'INDEPENDENT_OR_GRAPH')
    assert.equal(dependent.created.length, 1)
    assert.equal(independent.created.length, 1)
    assert.ok(
      dependent.created[0]!.proofs.every((proof) => proof.parents.includes(parent.memory.id)),
    )
    const originalIndependent = independent.created[0]!
    h.engine.runtime.recall(scene.id, p.id)
    assert.ok(h.engine.memory.list(scope).some((memory) => memory.id === first.memory.id))
    assert.doesNotMatch(structuredSQL(h), new RegExp(canary))
    assert.equal(
      h.engine.memory.knowledge.storage.rows().some((edge) => edge.id === dependent.created[0]!.id),
      false,
    )
    const kept = h.query().edges.find((edge) => edge.id === originalIndependent.id)!
    assert.ok(kept)
    assert.ok(kept.backing.deletionEpoch > originalIndependent.backing.deletionEpoch)
    assert.deepEqual(
      kept.spans.map((span) => span.source.messageId),
      [b.id],
    )
    await h.reopen()
    assert.doesNotMatch(structuredSQL(h), new RegExp(canary))
    assert.ok(h.engine.memory.list(scope).some((memory) => memory.id === first.memory.id))
    assert.equal(h.query().edges[0]!.id, originalIndependent.id)
    const retained = structuredSQL(h)
    h.engine.memory.update(first.memory.id, { locked: true })
    assert.deepEqual(h.query().edges, [])
    assert.equal(
      structuredSQL(h),
      retained,
      'ordinary metadata drift does not masquerade as privacy deletion of retained graph history',
    )
  } finally {
    await h.close()
  }
})

test('actual Replyer receives inherited negative graph qualifications and ordinary backing text in whole supportClosure with closed short references', async (t) => {
  clock(t)
  const h = fixture()
  let rounds = 0
  try {
    h.flags()
    const source = h.ingest('closure-root')
    const root = saved(
      h.write(
        source,
        [
          h.proposal(source, {
            predicate: 'prefers_topic',
            object: { kind: 'literal', valueType: 'text', value: 'ORIGINAL_NEGATIVE_LITERAL' },
            polarity: 'negative',
            modality: 'possible',
            temporalScope: 'past',
            conditions: ['ONLY_UNDER_ORIGINAL_CONDITION'],
          }),
        ],
        { kind: 'claim', content: 'ORIGINAL_BACKING_QUALIFICATION not confirmed.' },
      ),
    )
    const fresh = h.ingest(
        'closure-child',
        '😀New declaration by a different canonical person',
        scene,
        peer,
      ),
      context = h.turn()
    await h.read(context, { anchor: query.anchor, predicates: ['prefers_topic'], depth: 1 })
    const child = await queuedKnowledge(
      h,
      context,
      fresh,
      h.proposal(fresh, {
        object: { kind: 'new', entityKind: 'tool', title: 'Derived tool label' },
      }),
    )
    assert.ok(child.readDependencies.some((dep) => dep.edgeId === root.knowledge!.created[0]!.id))
    await h.ask(t, (messages) => {
      if (++rounds === 1)
        return completion([
          call('memory.knowledge', {
            anchor: { kind: 'person', personId: peer },
            predicates: ['uses_tool'],
            depth: 1,
          }),
        ])
      const read = receipts(messages)[0]!,
        page = read.value[0].page
      assert.equal(page.edges.length, 1)
      assert.equal(page.supportClosure.edges.length, 1)
      return plan(messages, { stateEvidenceIds: [read.id] })
    })
    assert.equal(h.replies.length, 1)
    const wire = h.replies[0]![1]!.content,
      data = JSON.parse(wire)
    const page = data.evidence.find((item: any) => item.toolName === 'memory.knowledge').states[0]
      .page
    assert.equal(page.edges.length, 1)
    const inherited = page.supportClosure.edges[0]
    assert.equal(inherited.polarity, 'negative')
    assert.equal(inherited.modality, 'possible')
    assert.equal(inherited.temporalScope, 'past')
    assert.deepEqual(inherited.conditions, ['ONLY_UNDER_ORIGINAL_CONDITION'])
    assert.equal(inherited.object.value, 'ORIGINAL_NEGATIVE_LITERAL')
    assert.ok(
      page.supportClosure.records.some(
        (entry: any) => entry.record.content === 'ORIGINAL_BACKING_QUALIFICATION not confirmed.',
      ),
    )
    const edges = [...page.edges, ...page.supportClosure.edges],
      ids = new Set(edges.map((edge: any) => edge.id)),
      recordIds = new Set(page.supportClosure.records.map((entry: any) => entry.ref.id))
    for (const edge of edges) {
      assert.ok(recordIds.has(edge.backing.id))
      for (const dep of edge.readDependencies) {
        assert.ok(recordIds.has(dep.backing.id))
        if (dep.edgeId) assert.ok(ids.has(dep.edgeId))
      }
      for (const endpoint of [edge.subject, edge.object])
        if (endpoint.kind === 'entity') assert.ok(ids.has(endpoint.entity.originEdgeId))
    }
    assert.ok(page.paths.flat().every((id: string) => ids.has(id)))
    assert.doesNotMatch(wire, /"backingFingerprint"|"spanHash"|"proofId"/)
  } finally {
    await h.close()
  }
})
