import assert from 'node:assert/strict'
import test, { type TestContext } from 'node:test'
import { setImmediate as immediate } from 'node:timers/promises'
import { mkdtempSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { Engine } from '../src/engine.ts'
import { Store } from '../src/store.ts'
import { createApp } from '../src/api.ts'
import type { ChatMessage, Completion, Router, ToolDefinition } from '../src/models.ts'
import type { RuntimeMessage, RuntimeExtension, TurnContext } from '../src/runtime.ts'
import type { SceneRef } from '../src/onebot.ts'
import type { Settings } from '../src/schema.ts'
import type { RelationProposal, RelationRecord } from '../src/memory/domain/relations.ts'
import { fixtureReplyPlan } from './helpers/reply-plan.ts'

// Synthetic contracts, provenance and transport scheduling only; not semantic graph accuracy.
const scene: SceneRef = {
  id: 'relation-scene',
  kind: 'group',
  adapterId: 'offline',
  accountId: 'bot',
  platformSceneId: 'synthetic-group',
}
const other: SceneRef = { ...scene, id: 'relation-private', kind: 'private' }
const person = 'person-a',
  reportedPerson = 'person-b',
  token = 'synthetic-relation-admin'
const enabled: Partial<Settings> = {
  memoryGraphEnabled: true,
  memoryGraphAutoAdmissionEnabled: true,
  memoryGraphAnswerEnabled: true,
}
type Proposal = Record<string, unknown>
type RelationReceipt = {
  id: string
  value: Array<{
    id: string
    content: string
    relationEvidence: Array<{
      id: string
      target: { entityId: string }
      requiredMemoryIds: string[]
    }>
  }>
  coverage?: { status: string; omittedRecords: number }
}
const completion = (toolCalls: Completion['toolCalls'] = []): Completion => ({
  text: '',
  toolCalls,
  modelId: 'offline-relation-script',
  usage: { inputTokens: 0, outputTokens: 0, totalTokens: 0 },
  assistant: { role: 'assistant', content: '', toolCalls },
})
const call = (name: string, args: Record<string, unknown> = {}, id = name) => ({
  id,
  name,
  arguments: args,
})
const source = (id: string, patch: Partial<RuntimeMessage> = {}): RuntimeMessage => ({
  id,
  sceneId: scene.id,
  senderId: person,
  sentAt: new Date().toISOString(),
  receivedAt: new Date().toISOString(),
  origin: 'live',
  status: 'available',
  parts: [{ type: 'text', text: `Synthetic participation source ${id}.` }],
  ...patch,
})
const relation = (personId: string, patch: Partial<RelationProposal> = {}): RelationProposal => ({
  predicate: 'participates_in',
  modality: 'asserted',
  temporalScope: 'current',
  personId,
  target: { kind: 'new', entityKind: 'plan', title: 'SYNTHETIC_PLAN_TITLE' },
  polarity: 'positive',
  conditions: [],
  ...patch,
})
const proposal = (message: RuntimeMessage, patch: Proposal = {}): Proposal => ({
  content: `Synthetic participation assertion ${message.id}.`,
  kind: 'claim',
  epistemicKind: 'explicit',
  subject: { kind: 'self', sourceMessageId: message.id },
  sourceMessageIds: [message.id],
  relations: [relation(message.senderId)],
  ...patch,
})
const receipt = (messages: ChatMessage[]): RelationReceipt =>
  JSON.parse(messages.filter((message) => message.role === 'tool').at(-1)!.content)
const plan = (messages: ChatMessage[], patch: Parameters<typeof fixtureReplyPlan>[1] = {}) =>
  completion([call('reply.plan', fixtureReplyPlan(messages, patch)!, 'plan')])
function clock(t: TestContext) {
  t.mock.timers.enable({
    apis: ['Date', 'setTimeout', 'setInterval'],
    now: new Date('2026-10-28T00:00:00Z'),
  })
}
async function advance(t: TestContext, ms = 100) {
  t.mock.timers.tick(ms)
  for (let i = 0; i < 8; i++) {
    await immediate()
    t.mock.timers.tick(0)
  }
}
function fixture(flags: Partial<Settings> = enabled) {
  const directory = mkdtempSync(join(tmpdir(), 'he-relations-integration-')),
    path = join(directory, 'synthetic.sqlite')
  let store: Store, engine: Engine, app: ReturnType<typeof createApp>
  let extracts = 0,
    extraModels = 0,
    sends = 0,
    querySequence = 0
  let extract = (_messages: RuntimeMessage[]): Proposal[] => []
  let planner: ((messages: ChatMessage[], tools: ToolDefinition[]) => Completion) | undefined
  const offered: ToolDefinition[][] = [],
    replyInputs: ChatMessage[][] = []
  const open = () => {
    store = new Store(path)
    store.set('settings', {
      ...store.settings,
      debounceMs: 10,
      maxWaitMs: 100,
      learningBatchMs: 100,
      learningMaxWaitMs: 300,
      learningMinIntervalMs: 0,
      ...flags,
    })
    for (const target of [scene, other])
      if (!store.scene(target.id))
        store.saveScene({
          ...target,
          name: 'Synthetic relation scene',
          participationEnabled: false,
          learningEnabled: true,
          proactiveEnabled: false,
        })
    engine = new Engine(store)
    engine.runtime.addAdapter('offline', {
      send: async () => {
        sends++
        throw new Error('Unexpected platform send')
      },
      getMessage: async () => ({ items: [], coverage: 'complete' }),
      history: async () => ({ items: [], coverage: 'complete' }),
      dispose() {},
    })
    engine.router = () =>
      ({
        complete: async (role, messages, tools = []) => {
          offered.push(structuredClone(tools))
          if (tools.some((tool) => tool.name === 'participation.decide'))
            return completion([
              call('participation.decide', {
                action: 'reply',
                reasonCode: 'synthetic_relation_query',
              }),
            ])
          if (tools.some((tool) => tool.name === 'reply.plan')) {
            assert.ok(planner)
            return planner(messages, tools)
          }
          if (role === 'replyer') {
            replyInputs.push(structuredClone(messages))
            return completion()
          }
          assert.equal(role, 'learning')
          if (tools.some((tool) => tool.name === 'learning.propose')) {
            extracts++
            return completion(
              extract(JSON.parse(messages[1]!.content).messages).map((args, i) =>
                call('learning.propose', args, `extract-${i}`),
              ),
            )
          }
          extraModels++
          return completion()
        },
      }) as Router
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
    offered,
    replyInputs,
    setExtract(fn: typeof extract) {
      extract = fn
    },
    ingest(message: RuntimeMessage, target = scene) {
      engine.runtime.ingest(message, target)
    },
    settings(patch: Partial<Settings>) {
      store.set('settings', { ...store.settings, ...patch })
      engine.settingsChanged()
    },
    stats: () => ({ extracts, extraModels, sends }),
    rows(target = scene.id): RelationRecord[] {
      return engine.memory.relations.list({
        sceneId: target,
        simulation: false,
        provenance: 'contextual',
      }).items
    },
    async request(route: string, body?: unknown, authenticated = true) {
      return app.request(route, {
        ...(body === undefined ? {} : { method: 'POST', body: JSON.stringify(body) }),
        headers: {
          'Content-Type': 'application/json',
          ...(authenticated ? { Authorization: `Bearer ${token}` } : {}),
        },
      })
    },
    async apiRows(target = scene.id) {
      const response = await this.request(
        `/api/memory-relations?${new URLSearchParams({ sceneId: target })}`,
      )
      assert.equal(response.status, 200)
      return response.json()
    },
    async ask(
      t: TestContext,
      fn: NonNullable<typeof planner>,
      message = source(`query-${++querySequence}`),
      afterTurn?: () => void | Promise<void>,
    ) {
      planner = fn
      store.set('settings', {
        ...store.settings,
        learningBatchMs: 300000,
        learningMaxWaitMs: 300000,
      })
      engine.settingsChanged()
      store.saveScene({ ...store.scene(scene.id)!, participationEnabled: true })
      engine.runtime.ingest(message, scene)
      await advance(t, 10)
      await afterTurn?.()
      await advance(t, 100)
      await engine.contextualLearning.queue.whenIdle()
      assert.equal(engine.runtime.activeTasks, 0)
    },
    async reopen() {
      await engine.dispose()
      store.close()
      planner = undefined
      open()
    },
    async close() {
      await engine.dispose()
      store.close()
      rmSync(directory, { recursive: true, force: true })
      assert.equal(sends, 0)
    },
  }
}
async function seed(t: TestContext, h: ReturnType<typeof fixture>, patch: Proposal = {}) {
  h.setExtract((messages) => [proposal(messages[0]!, patch)])
  h.ingest(source('accepted-source'))
  await advance(t)
  assert.equal(h.rows().length, 1)
  return h.rows()[0]!
}
async function addNegative(t: TestContext, h: ReturnType<typeof fixture>) {
  let rounds = 0
  await h.ask(
    t,
    (messages) => {
      if (++rounds === 1) return completion([call('memory.relations', {}, 'read-relations')])
      const evidence = receipt(messages)
      const entityId = evidence.value[0]!.relationEvidence[0]!.target.entityId
      return plan(messages, {
        action: 'noReply',
        actions: [
          {
            toolName: 'learning.propose',
            arguments: proposal(source('negative-source'), {
              content:
                'NEGATIVE_BACKING: participation is not confirmed; only if the organizer confirms.',
              relations: [
                relation(person, {
                  target: { kind: 'existing', entityId },
                  polarity: 'negative',
                  conditions: ['Only if the organizer explicitly confirms.'],
                  validUntil: '2026-10-27T23:00:00Z',
                }),
              ],
            }),
          },
        ],
      })
    },
    source('negative-source'),
  )
  assert.equal(h.rows().length, 2)
  assert.equal(
    h.engine.contextualLearning.queue.stats().completed,
    1,
    'a committed negative counterexample must report completion even though it changes the earlier relation snapshot',
  )
  assert.equal(
    h.engine.contextualLearning.queue.stats().discarded,
    0,
    'never describe a committed relation as discarded',
  )
  return h.rows().find((row) => row.polarity === 'negative')!
}

test('all relation flags default OFF: actual extraction schema hides edges, undeclared writes reject, and administrator read stays disabled', async (t) => {
  clock(t)
  const h = fixture({})
  try {
    for (const key of [
      'memoryGraphEnabled',
      'memoryGraphAutoAdmissionEnabled',
      'memoryGraphAnswerEnabled',
      'memoryGraphShadowEnabled',
    ] as const)
      assert.equal(h.store.settings[key], false)
    h.setExtract((messages) => [proposal(messages[0]!)])
    h.ingest(source('off-source'))
    await advance(t)
    const tool = h.offered.flat().find((tool) => tool.name === 'learning.propose')!
    assert.equal(
      Object.hasOwn((tool.inputSchema as { properties: object }).properties, 'relations'),
      false,
    )
    assert.ok(h.offered.every((tools) => tools.every((tool) => tool.name !== 'memory.relations')))
    assert.equal(h.engine.memory.adminList().length, 0)
    assert.deepEqual(h.rows(), [])
    assert.equal((await h.apiRows()).enabled, false)
  } finally {
    await h.close()
  }
})

test('actual silent learning automatically admits the narrow self-explicit positive relation in one model pass and survives restart', async (t) => {
  clock(t)
  const h = fixture()
  try {
    const row = await seed(t, h)
    assert.equal(row.status, 'accepted')
    assert.equal(row.admission, 'automatic')
    assert.equal(row.autoReason, 'self_explicit_no_visible_conflict')
    assert.equal(row.proofs[0]!.subjectWitness?.kind, 'self')
    assert.equal(row.proofs[0]!.sources[0]!.messageId, 'accepted-source')
    assert.deepEqual(h.stats(), { extracts: 1, extraModels: 0, sends: 0 })
    assert.equal((await h.apiRows()).items[0].status, 'accepted')
    const before = h.rows()
    await h.reopen()
    assert.deepEqual(h.rows(), before)
    await advance(t, 1000)
    assert.equal(h.stats().extracts, 1)
  } finally {
    await h.close()
  }
})

test('negative, conditional, historical, reported, possible and noncurrent evidence stays candidate; identical new titles never imply one entity', async (t) => {
  clock(t)
  const h = fixture()
  try {
    h.setExtract((messages) =>
      messages.map((message) =>
        proposal(message, {
          ...(message.id === 'reported'
            ? {
                subject: {
                  kind: 'mentioned',
                  personId: message.parts.find((part) => part.type === 'mention')!.userId,
                  sourceMessageId: message.id,
                  mentionPartIndex: 1,
                },
              }
            : {}),
          relations: [
            relation(
              message.id === 'reported'
                ? message.parts.find((part) => part.type === 'mention')!.userId
                : message.senderId,
              {
                ...(message.id === 'negative' ? { polarity: 'negative' } : {}),
                ...(message.id === 'conditional'
                  ? { conditions: ['Only after explicit confirmation.'] }
                  : {}),
                ...(message.id === 'historical' ? { occurredAt: '2025-01-01T00:00:00Z' } : {}),
                ...(message.id === 'possible' ? { modality: 'possible' } : {}),
                ...(['past', 'future', 'unspecified'].includes(message.id)
                  ? { temporalScope: message.id as 'past' | 'future' | 'unspecified' }
                  : {}),
              },
            ),
          ],
        }),
      ),
    )
    for (const id of [
      'negative',
      'conditional',
      'historical',
      'reported',
      'possible',
      'past',
      'future',
      'unspecified',
    ])
      h.ingest(
        source(
          id,
          id === 'reported'
            ? {
                parts: [
                  { type: 'text', text: 'Synthetic third-party participation report.' },
                  { type: 'mention', userId: reportedPerson },
                ],
              }
            : {},
        ),
      )
    await advance(t)
    const rows = h.rows()
    assert.equal(rows.length, 8)
    assert.ok(rows.every((row) => row.status === 'candidate' && row.admission === 'candidate'))
    assert.equal(new Set(rows.map((row) => row.target.id)).size, 8)
    assert.equal(new Set(rows.map((row) => row.target.title)).size, 1)
    const reported = rows.find((row) => row.personId === reportedPerson)!
    assert.equal(reported.epistemicKind, 'reported')
    assert.equal(reported.proofs[0]!.sources[0]!.senderId, person)
    assert.equal(reported.proofs[0]!.subjectWitness?.kind, 'mentioned')
    for (const scope of ['past', 'future', 'unspecified']) {
      const row = rows.find((row) => row.temporalScope === scope)!
      assert.ok(row)
      assert.equal(
        row.occurredAt,
        undefined,
        'unknown occurrence never becomes a fabricated instant',
      )
    }
    assert.equal(h.stats().extracts, 1)
  } finally {
    await h.close()
  }
})

test('authenticated administrator review is CAS guarded, scene scoped, and never rewrites negative evidence into a positive relation', async (t) => {
  clock(t)
  const h = fixture()
  try {
    const row = await seed(t, h, {
      relations: [relation(person, { polarity: 'negative', conditions: ['Not yet confirmed.'] })],
    })
    const route = `/api/memory-relations/${row.id}/review`
    const body = { sceneId: scene.id, expectedVersion: row.version, status: 'accepted' }
    assert.equal((await h.request(route, body, false)).status, 401)
    assert.equal((await h.request(route, { ...body, sceneId: other.id })).status, 404)
    const accepted = await h.request(route, body)
    assert.equal(accepted.status, 200)
    const view = await accepted.json()
    assert.equal(view.version, row.version + 1)
    assert.equal(view.admission, 'reviewed')
    assert.equal(view.polarity, 'negative')
    assert.deepEqual(view.conditions, ['Not yet confirmed.'])
    assert.equal((await h.request(route, { ...body, status: 'rejected' })).status, 409)
    assert.equal(h.rows()[0]!.status, 'accepted')
    await h.reopen()
    assert.equal(h.rows()[0]!.version, row.version + 1)
  } finally {
    await h.close()
  }
})

test('actual Planner relation read, exact selections and Replyer preserve an accepted edge together with its negative conditional expired counterexample', async (t) => {
  clock(t)
  const h = fixture()
  try {
    const positive = await seed(t, h)
    const negative = await addNegative(t, h)
    assert.equal(negative.status, 'candidate')
    assert.equal(negative.target.id, positive.target.id)
    assert.ok(
      negative.proofs.some((proof) =>
        proof.parentReferences?.some((ref) => ref.id === positive.backing.id),
      ),
    )
    let rounds = 0
    await h.ask(t, (messages) => {
      if (++rounds === 1) return completion([call('memory.relations', {}, 'relation-answer-read')])
      const evidence = receipt(messages)
      assert.equal(evidence.value.length, 2)
      return plan(messages, {
        memorySelections: [
          { evidenceId: evidence.id, memoryIds: evidence.value.map((record) => record.id) },
        ],
        facts: [
          {
            text: 'There are conflicting qualified participation assertions, not permission or verified truth.',
            evidenceIds: [evidence.id],
          },
        ],
      })
    })
    assert.equal(h.replyInputs.length, 1)
    const payload = JSON.parse(
      h.replyInputs[0]!.find((message) => message.role === 'user')!.content,
    )
    const memories = payload.evidence.find(
      (item: { toolName: string }) => item.toolName === 'memory.relations',
    ).memories
    assert.equal(memories.length, 2)
    const edges = memories.flatMap(
      (memory: { relationEvidence: unknown[] }) => memory.relationEvidence,
    )
    assert.ok(
      edges.some(
        (edge: { polarity: string; status: string }) =>
          edge.polarity === 'positive' && edge.status === 'accepted',
      ),
    )
    assert.ok(
      edges.some(
        (edge: { polarity: string; status: string; applicability: string; conditions: string[] }) =>
          edge.polarity === 'negative' &&
          edge.status === 'candidate' &&
          edge.applicability === 'expired' &&
          edge.conditions.includes('Only if the organizer explicitly confirms.'),
      ),
    )
    assert.match(JSON.stringify(payload), /NEGATIVE_BACKING|not confirmed/)
    assert.ok(
      edges.every((edge: { requiredMemoryIds: string[] }) =>
        edge.requiredMemoryIds.every((id) =>
          memories.some((memory: { id: string }) => memory.id === id),
        ),
      ),
    )
  } finally {
    await h.close()
  }
})

test('actual Planner cannot select one memory while omitting another required relation backing', async (t) => {
  clock(t)
  const h = fixture()
  try {
    await seed(t, h)
    await addNegative(t, h)
    let rounds = 0
    await h.ask(t, (messages) => {
      if (++rounds === 1)
        return completion([call('memory.relations', {}, 'partial-selection-read')])
      const evidence = receipt(messages)
      assert.equal(evidence.value.length, 2)
      return plan(messages, {
        memorySelections: [{ evidenceId: evidence.id, memoryIds: [evidence.value[0]!.id] }],
      })
    })
    assert.equal(h.replyInputs.length, 0)
    assert.ok(h.store.logs.some((log) => log.stage === 'planner' && log.status === 'failed'))
  } finally {
    await h.close()
  }
})

test('actual budget-omitted relation receipt registers no parent or entity exposure for contextual learning', async (t) => {
  clock(t)
  const h = fixture({ ...enabled, plannerEvidenceMaxBytes: 32768 })
  try {
    await seed(t, h, {
      content: `Large relation qualification ${'中文"\\\n'.repeat(1600)}not a promise.`,
    })
    const calls: Array<{ parents: unknown[]; entities: unknown[] }> = []
    const original = h.engine.contextualLearning.submit.bind(h.engine.contextualLearning)
    h.engine.contextualLearning.submit = (ctx, input, exposed, entities = [], guard) => {
      calls.push({
        parents: structuredClone([...exposed]),
        entities: structuredClone([...entities]),
      })
      return original(ctx, input, exposed, entities, guard)
    }
    let rounds = 0
    await h.ask(
      t,
      (messages) => {
        if (++rounds === 1) return completion([call('memory.relations', {}, 'omitted-read')])
        const evidence = receipt(messages)
        assert.deepEqual(evidence.value, [])
        assert.equal(evidence.coverage?.status, 'limited')
        return plan(messages, {
          action: 'noReply',
          actions: [
            {
              toolName: 'learning.propose',
              arguments: proposal(source('budget-source'), {
                content: 'Independent source-only context assertion.',
                relations: [],
              }),
            },
          ],
        })
      },
      source('budget-source'),
    )
    assert.deepEqual(calls, [{ parents: [], entities: [] }])
    assert.equal(h.rows().length, 1)
  } finally {
    await h.close()
  }
})

for (const change of ['recall', 'delete', 'version'] as const)
  test(`actual ${change} of target backing physically purges both relation directions, entity body and reverse references across restart`, async (t) => {
    clock(t)
    const h = fixture()
    try {
      const target = await seed(t, h)
      const child = await addNegative(t, h)
      assert.equal(h.rows().length, 2)
      if (change === 'recall') h.engine.runtime.recall(scene.id, 'accepted-source')
      else if (change === 'delete') h.engine.memory.delete(target.backing.id)
      else h.engine.memory.update(target.backing.id, { locked: true })
      const purged = () => {
        assert.deepEqual(h.rows(), [])
        for (const table of ['memory_relation_candidates', 'memory_relation_entities'])
          assert.equal(
            (h.store.db.prepare(`SELECT COUNT(*) n FROM ${table}`).get() as { n: number }).n,
            0,
          )
        assert.equal(
          (
            h.store.db
              .prepare('SELECT COUNT(*) n FROM memory_relation_tombstones WHERE id IN (?,?)')
              .get(target.id, child.id) as { n: number }
          ).n,
          2,
        )
      }
      purged()
      assert.deepEqual((await h.apiRows()).items, [])
      await h.reopen()
      purged()
    } finally {
      await h.close()
    }
  })

test('relation checkpoint and assertion creation roll back together if relation persistence throws', async (t) => {
  clock(t)
  const h = fixture()
  try {
    h.engine.memory.relations.admit = () => {
      throw new Error('synthetic_relation_storage_failure')
    }
    h.setExtract((messages) => [proposal(messages[0]!)])
    h.ingest(source('rollback-source'))
    await advance(t)
    assert.equal(h.engine.memory.adminList().length, 0)
    for (const table of [
      'memory_relation_candidates',
      'memory_relation_entities',
      'memory_assertion_state',
      'memory_assertion_revisions',
    ])
      assert.equal(
        (h.store.db.prepare(`SELECT COUNT(*) n FROM ${table}`).get() as { n: number }).n,
        0,
      )
    await h.reopen()
    assert.equal(h.engine.memory.adminList().length, 0)
    assert.deepEqual(h.rows(), [])
  } finally {
    await h.close()
  }
})

test('existing graph is inaccessible across scenes and simulations; turning graph/answer OFF does not replay learning or offer the tool', async (t) => {
  clock(t)
  const h = fixture()
  try {
    await seed(t, h)
    assert.deepEqual(h.rows(other.id), [])
    assert.deepEqual((await h.apiRows(other.id)).items, [])
    assert.deepEqual(
      h.engine.memory.relations.list({
        sceneId: scene.id,
        simulation: true,
        provenance: 'contextual',
      }).items,
      [],
    )
    h.ingest(source('simulation', { origin: 'simulation' }))
    await advance(t)
    assert.equal(h.stats().extracts, 1)
    h.settings({ memoryGraphEnabled: false, memoryGraphAnswerEnabled: false })
    await h.ask(t, (messages, tools) => {
      assert.ok(tools.every((tool) => tool.name !== 'memory.relations'))
      return plan(messages, { action: 'noReply' })
    })
    assert.deepEqual((await h.apiRows()).items, [])
    assert.equal(h.stats().extracts, 1)
  } finally {
    await h.close()
  }
})

test('a real Engine contextual entry cannot bind a known entity ID without having delivered its exact entity reference', async (t) => {
  clock(t)
  const h = fixture()
  try {
    const existing = await seed(t, h)
    const fresh = source('unexposed-entity-source')
    h.ingest(fresh)
    const extension = (
      h.engine.runtime as unknown as { extensions: RuntimeExtension[] }
    ).extensions.find((item) => item.tools.some((tool) => tool.name === 'learning.propose'))!
    const context: TurnContext = {
      scene,
      mode: 'real',
      turnId: 'unexposed-entity-turn',
      signal: new AbortController().signal,
      dependencies: new Set(),
      validations: [],
    }
    await extension.execute(
      'learning.propose',
      proposal(fresh, {
        content: 'Independent assertion with unauthorized existing-target binding.',
        relations: [
          relation(person, { target: { kind: 'existing', entityId: existing.target.id } }),
        ],
      }),
      context,
    )
    await h.engine.contextualLearning.queue.whenIdle()
    assert.deepEqual(h.rows(), [existing])
    assert.equal(
      h.engine.memory
        .adminList()
        .filter(
          (memory) =>
            memory.content === 'Independent assertion with unauthorized existing-target binding.',
        ).length,
      1,
      'rejected graph binding need not delete the independent valid assertion',
    )
  } finally {
    await h.close()
  }
})

test('changing target backing after actual Planner delivery rejects stale contextual relation submission', async (t) => {
  clock(t)
  const h = fixture()
  try {
    const existing = await seed(t, h)
    let rounds = 0,
      changed = false
    await h.ask(
      t,
      (messages) => {
        if (++rounds === 1) return completion([call('memory.relations', {}, 'read-stale-target')])
        const delivered = receipt(messages)
        const entityId = delivered.value[0]!.relationEvidence[0]!.target.entityId
        h.engine.memory.update(existing.backing.id, { locked: true })
        changed = true
        return plan(messages, {
          action: 'noReply',
          actions: [
            {
              toolName: 'learning.propose',
              arguments: proposal(source('stale-target-source'), {
                content: 'STALE_ENTITY_CHILD_MUST_NOT_SAVE',
                relations: [relation(person, { target: { kind: 'existing', entityId } })],
              }),
            },
          ],
        })
      },
      source('stale-target-source'),
    )
    assert.equal(changed, true)
    assert.deepEqual(h.rows(), [])
    assert.equal(
      h.engine.memory
        .adminList()
        .some((memory) => memory.content === 'STALE_ENTITY_CHILD_MUST_NOT_SAVE'),
      false,
    )
    assert.equal(h.engine.contextualLearning.queue.stats().accepted, 0)
    assert.equal(h.replyInputs.length, 0)
  } finally {
    await h.close()
  }
})

test('relation endpoints cannot replace the authoritative subject of a valid automatic assertion', async (t) => {
  clock(t)
  const h = fixture()
  try {
    h.setExtract((messages) => [
      proposal(messages[0]!, {
        relations: [relation(messages[0]!.parts.find((part) => part.type === 'mention')!.userId)],
      }),
    ])
    h.ingest(
      source('wrong-endpoint', {
        parts: [
          { type: 'text', text: 'Synthetic self statement mentioning someone else.' },
          { type: 'mention', userId: reportedPerson },
        ],
      }),
    )
    await advance(t)
    assert.equal(h.engine.memory.adminList().length, 1)
    assert.equal(h.engine.memory.adminList()[0]!.scope.kind, 'scene')
    assert.deepEqual(h.rows(), [])
    assert.deepEqual((await h.apiRows()).items, [])
  } finally {
    await h.close()
  }
})

test('actual Replyer retains possible modality and unknown past/unspecified time without converting them into current positive facts', async (t) => {
  clock(t)
  const h = fixture()
  try {
    h.setExtract((messages) =>
      messages.map((message) =>
        proposal(message, {
          relations: [
            relation(
              message.senderId,
              message.id === 'possible-source'
                ? { modality: 'possible' }
                : { temporalScope: message.id === 'past-source' ? 'past' : 'unspecified' },
            ),
          ],
        }),
      ),
    )
    for (const id of ['possible-source', 'past-source', 'unspecified-source']) h.ingest(source(id))
    await advance(t)
    let rounds = 0
    await h.ask(t, (messages) => {
      if (++rounds === 1) return completion([call('memory.relations', {}, 'qualified-read')])
      const evidence = receipt(messages)
      assert.equal(evidence.value.length, 3)
      return plan(messages, {
        memorySelections: [
          { evidenceId: evidence.id, memoryIds: evidence.value.map((memory) => memory.id) },
        ],
      })
    })
    assert.equal(h.replyInputs.length, 1)
    const payload = JSON.parse(
      h.replyInputs[0]!.find((message) => message.role === 'user')!.content,
    )
    const edges = payload.evidence.flatMap(
      (item: { memories?: Array<{ relationEvidence: unknown[] }> }) =>
        item.memories?.flatMap((memory) => memory.relationEvidence) ?? [],
    )
    assert.equal(edges.length, 3)
    assert.ok(
      edges.every(
        (edge: { status: string; occurredAt?: string; validFrom?: string; validUntil?: string }) =>
          edge.status === 'candidate' &&
          edge.occurredAt === undefined &&
          edge.validFrom === undefined &&
          edge.validUntil === undefined,
      ),
    )
    assert.ok(
      edges.some(
        (edge: { modality: string; temporalScope: string }) =>
          edge.modality === 'possible' && edge.temporalScope === 'current',
      ),
    )
    assert.ok(
      edges.some(
        (edge: { temporalScope: string; applicability: string }) =>
          edge.temporalScope === 'past' && edge.applicability === 'past',
      ),
    )
    assert.ok(
      edges.some(
        (edge: { temporalScope: string; applicability: string }) =>
          edge.temporalScope === 'unspecified' && edge.applicability === 'unspecified',
      ),
    )
  } finally {
    await h.close()
  }
})

test('graph storage, automatic admission and answer exposure are independent opt-ins with no retroactive acceptance', async (t) => {
  clock(t)
  const h = fixture({
    ...enabled,
    memoryGraphAutoAdmissionEnabled: false,
    memoryGraphAnswerEnabled: false,
  })
  try {
    const candidate = await seed(t, h)
    assert.equal(candidate.status, 'candidate')
    assert.equal((await h.apiRows()).items.length, 1)
    await h.ask(t, (messages, tools) => {
      assert.ok(tools.every((tool) => tool.name !== 'memory.relations'))
      return plan(messages, { action: 'noReply' })
    })
    h.settings({ memoryGraphAutoAdmissionEnabled: true, memoryGraphAnswerEnabled: true })
    assert.equal(h.rows()[0]!.status, 'candidate')
    await h.ask(t, (messages, tools) => {
      assert.ok(tools.some((tool) => tool.name === 'memory.relations'))
      return plan(messages, { action: 'noReply' })
    })
    assert.equal(h.rows()[0]!.id, candidate.id)
    assert.equal(h.rows()[0]!.status, 'candidate')
    assert.equal(h.stats().extracts, 1)
  } finally {
    await h.close()
  }
})

for (const revoke of ['review', 'answer-off'] as const)
  test(`actual queued contextual entity capability is invalidated by ${revoke} without changing its backing assertion`, async (t) => {
    clock(t)
    const h = fixture()
    try {
      const existing = await seed(t, h)
      const backingBefore = h.engine.memory.revisionState(existing.backing.id)
      let rounds = 0
      await h.ask(
        t,
        (messages) => {
          if (++rounds === 1)
            return completion([call('memory.relations', {}, 'read-before-revocation')])
          const delivered = receipt(messages)
          const entityId = delivered.value[0]!.relationEvidence[0]!.target.entityId
          return plan(messages, {
            action: 'noReply',
            actions: [
              {
                toolName: 'learning.propose',
                arguments: proposal(source('queued-entity-source'), {
                  content: 'QUEUED_REVOKED_ENTITY_MUST_NOT_COMMIT',
                  relations: [relation(person, { target: { kind: 'existing', entityId } })],
                }),
              },
            ],
          })
        },
        source('queued-entity-source'),
        async () => {
          assert.equal(h.engine.contextualLearning.queue.stats().accepted, 1)
          assert.equal(
            h.engine.contextualLearning.queue.stats().queued,
            1,
            'the foreground completion must leave this independently queued job pending',
          )
          if (revoke === 'review') {
            const response = await h.request(`/api/memory-relations/${existing.id}/review`, {
              sceneId: scene.id,
              expectedVersion: existing.version,
              status: 'rejected',
            })
            assert.equal(response.status, 200)
          } else h.settings({ memoryGraphAnswerEnabled: false })
          assert.deepEqual(h.engine.memory.revisionState(existing.backing.id), backingBefore)
        },
      )
      assert.equal(h.engine.contextualLearning.queue.stats().discarded, 1)
      assert.equal(
        h.engine.memory
          .adminList()
          .some((memory) => memory.content === 'QUEUED_REVOKED_ENTITY_MUST_NOT_COMMIT'),
        false,
      )
      assert.equal(h.rows().length, 1)
      assert.equal(h.replyInputs.length, 0)
    } finally {
      await h.close()
    }
  })

for (const ordinary of ['memory.search', 'memory.person'] as const)
  test(`ordinary ${ordinary} exposure does not acquire relation-review cancellation authority`, async (t) => {
    clock(t)
    const h = fixture()
    try {
      const existing = await seed(t, h)
      const backingBefore = h.engine.memory.revisionState(existing.backing.id)
      let rounds = 0
      await h.ask(
        t,
        (messages) => {
          if (++rounds === 1)
            return completion([
              call(
                ordinary,
                ordinary === 'memory.search'
                  ? { query: 'Synthetic participation assertion accepted-source' }
                  : { personId: person },
                'ordinary-read',
              ),
            ])
          const delivered = receipt(messages)
          assert.equal(delivered.value.length, 1)
          assert.equal(delivered.value[0]!.relationEvidence, undefined)
          return plan(messages, {
            action: 'noReply',
            actions: [
              {
                toolName: 'learning.propose',
                arguments: proposal(source('ordinary-source'), {
                  content: 'ORDINARY_CONTEXT_REMAINS_VALID_AFTER_EDGE_REVIEW',
                  relations: [],
                }),
              },
            ],
          })
        },
        source('ordinary-source'),
        async () => {
          assert.equal(h.engine.contextualLearning.queue.stats().queued, 1)
          const response = await h.request(`/api/memory-relations/${existing.id}/review`, {
            sceneId: scene.id,
            expectedVersion: existing.version,
            status: 'rejected',
          })
          assert.equal(response.status, 200)
          assert.deepEqual(h.engine.memory.revisionState(existing.backing.id), backingBefore)
        },
      )
      const child = h.engine.memory
        .adminList()
        .find((memory) => memory.content === 'ORDINARY_CONTEXT_REMAINS_VALID_AFTER_EDGE_REVIEW')
      assert.ok(child)
      assert.equal(h.engine.contextualLearning.queue.stats().completed, 1)
      assert.equal(h.engine.contextualLearning.queue.stats().discarded, 0)
      const proofs = JSON.parse(
        (
          h.store.db
            .prepare('SELECT value FROM memory_supports WHERE memory_id=?')
            .get(child.id) as { value: string }
        ).value,
      )
      assert.ok(
        proofs.some(
          (proof: {
            parentReferences?: Array<{ id: string; version: number; deletionEpoch: number }>
          }) =>
            proof.parentReferences?.some(
              (ref) =>
                ref.id === existing.backing.id &&
                ref.version === existing.backing.version &&
                ref.deletionEpoch === existing.backing.deletionEpoch,
            ),
        ),
      )
      assert.equal(h.replyInputs.length, 0)
    } finally {
      await h.close()
    }
  })

for (const revoke of ['review', 'answer-off'] as const)
  test(`direct Engine submission cannot treat ${revoke}-revoked delivered entity authority as never exposed`, async (t) => {
    clock(t)
    const h = fixture()
    try {
      const existing = await seed(t, h)
      const fresh = source('revoked-before-submit-source')
      h.ingest(fresh)
      const extension = (
        h.engine.runtime as unknown as { extensions: RuntimeExtension[] }
      ).extensions.find((item) => item.tools.some((tool) => tool.name === 'learning.propose'))!
      const context: TurnContext = {
        scene,
        mode: 'real',
        turnId: 'revoked-before-submit',
        signal: new AbortController().signal,
        dependencies: new Set(),
        validations: [],
      }
      const delivered = (await extension.execute('memory.relations', {}, context)) as unknown[]
      assert.equal(delivered.length, 1)
      assert.ok(context.validations.length > 0)
      assert.ok(context.validations.every((validate) => validate()))
      const backingBefore = h.engine.memory.revisionState(existing.backing.id)
      if (revoke === 'review') {
        const response = await h.request(`/api/memory-relations/${existing.id}/review`, {
          sceneId: scene.id,
          expectedVersion: existing.version,
          status: 'rejected',
        })
        assert.equal(response.status, 200)
      } else h.settings({ memoryGraphAnswerEnabled: false })
      assert.deepEqual(h.engine.memory.revisionState(existing.backing.id), backingBefore)
      assert.ok(context.validations.some((validate) => !validate()))
      await extension.execute(
        'learning.propose',
        proposal(fresh, {
          content: 'REVOKED_EXPOSURE_CANNOT_COMMIT_AS_UNEXPOSED',
          relations: [
            relation(person, { target: { kind: 'existing', entityId: existing.target.id } }),
          ],
        }),
        context,
      )
      await h.engine.contextualLearning.queue.whenIdle()
      assert.equal(
        h.engine.memory
          .adminList()
          .some((memory) => memory.content === 'REVOKED_EXPOSURE_CANNOT_COMMIT_AS_UNEXPOSED'),
        false,
      )
      assert.equal(h.rows().length, 1)
      assert.equal(h.engine.contextualLearning.queue.stats().completed, 0)
    } finally {
      await h.close()
    }
  })
