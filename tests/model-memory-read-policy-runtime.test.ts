import assert from 'node:assert/strict'
import test, { type TestContext } from 'node:test'
import { mkdtempSync, rmSync } from 'node:fs'
import { join } from 'node:path'
import { tmpdir } from 'node:os'
import { setImmediate as immediate } from 'node:timers/promises'
import { Engine } from '../src/engine.ts'
import { Store } from '../src/store.ts'
import { createServer } from 'node:http'
import sharp from 'sharp'
import { modelScheduler } from '../src/scheduler.ts'
import type { Scene } from '../src/schema.ts'
import type { SceneRef } from '../src/onebot.ts'
import type { MemoryRecord } from '../src/memory.ts'
import type { ChatMessage, Completion, Router, ToolDefinition } from '../src/models.ts'
import type { RuntimeExtension, RuntimeMessage, TurnContext } from '../src/runtime.ts'
import { fixtureReplyPlan } from './helpers/reply-plan.ts'

// Synthetic model responses prove read authorization and lifecycle contracts, not NLP quality.
const scene: SceneRef = {
  id: 'read-policy-group',
  kind: 'group',
  adapterId: 'offline-read-policy',
  accountId: 'bot',
  platformSceneId: 'synthetic-group',
}
const person = 'synthetic-person'
const scope = { sceneId: scene.id, simulation: false }
const PRIVATE = 'PRIVATE_READ_POLICY_CANARY'
const GENERAL = 'GENERAL_READ_POLICY_CANARY'
const HABIT = 'HABIT_READ_POLICY_CANARY'
const MANUAL = 'MANUAL_RULE_REMAINS_CANARY'
const LIVE = 'CURRENT_CHAT_REMAINS_CANARY'
const call = (name: string, args: Record<string, unknown> = {}) => ({
  id: name,
  name,
  arguments: args,
})
const completion = (toolCalls: Completion['toolCalls'] = []): Completion => ({
  text: '',
  toolCalls,
  modelId: 'offline-read-policy',
  usage: { inputTokens: 0, outputTokens: 0, totalTokens: 0 },
  assistant: { role: 'assistant', content: '', toolCalls },
})
const source = (id: string, text = LIVE): RuntimeMessage => ({
  id,
  sceneId: scene.id,
  senderId: person,
  origin: 'live',
  status: 'available',
  sentAt: new Date().toISOString(),
  receivedAt: new Date().toISOString(),
  parts: [{ type: 'text', text }],
})
function clock(t: TestContext) {
  t.mock.timers.enable({
    apis: ['Date', 'setTimeout', 'setInterval'],
    now: new Date('2026-10-29T03:00:00Z'),
  })
}
async function advance(t: TestContext, ms = 10) {
  t.mock.timers.tick(ms)
  for (let n = 0; n < 10; n++) {
    await immediate()
    t.mock.timers.tick(0)
  }
}
const initial = (messages: ChatMessage[]) =>
  JSON.parse(messages.find((m) => m.role === 'user')!.content)
const receipt = (messages: ChatMessage[]) =>
  JSON.parse(messages.filter((m) => m.role === 'tool').at(-1)!.content) as {
    id: string
    value: MemoryRecord[]
  }
const plan = (messages: ChatMessage[], patch: Parameters<typeof fixtureReplyPlan>[1] = {}) =>
  completion([call('reply.plan', fixtureReplyPlan(messages, patch)!)])
function fixture() {
  const directory = mkdtempSync(join(tmpdir(), 'he-read-policy-'))
  const path = join(directory, 'synthetic.sqlite')
  let store: Store, engine: Engine, extension: RuntimeExtension
  const requests: Array<{ role: string; messages: ChatMessage[]; tools: string[] }> = []
  let sends = 0,
    sequence = 0
  let respond: Router['complete'] = async (_role, messages, tools = []) => {
    if (tools.some((t) => t.name === 'participation.decide'))
      return completion([
        call('participation.decide', { action: 'reply', reasonCode: 'synthetic_read_policy' }),
      ])
    if (tools.some((t) => t.name === 'reply.plan')) return plan(messages)
    return completion()
  }
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
        memoryWorkingSetEnabled: true,
        memoryEpisodesEnabled: true,
        memoryGraphEnabled: true,
        memoryGraphAnswerEnabled: true,
        memoryCommitmentsEnabled: true,
        memoryCommitmentsAnswerEnabled: true,
        memoryHistoricalReadEnabled: true,
        memoryExperienceEnabled: true,
        memoryExperienceAutoActivateEnabled: true,
        memoryExperienceHintsEnabled: true,
      })
      store.saveScene({
        ...scene,
        name: 'Synthetic read policy',
        participationEnabled: false,
        learningEnabled: true,
        proactiveEnabled: false,
      })
      store.set('rules', [
        {
          id: 'manual',
          scope: 'scene',
          sceneId: scene.id,
          enabled: true,
          nonOverridable: true,
          content: MANUAL,
        },
      ])
    }
    engine = new Engine(store)
    engine.indexMemory = async () => {}
    engine.router = () =>
      ({
        complete: async (role, messages, tools = [], ...rest) => {
          requests.push({
            role,
            messages: structuredClone(messages),
            tools: tools.map((t) => t.name),
          })
          return respond(role, messages, tools, ...rest)
        },
      }) as Router
    engine.runtime.addAdapter(scene.adapterId, {
      send: async () => ({ status: 'sent', messageId: `synthetic-bot-${++sends}` }),
      getMessage: async () => ({ items: [], coverage: 'complete' }),
      history: async () => ({ items: [], coverage: 'complete' }),
      dispose() {},
    })
    extension = (engine.runtime as unknown as { extensions: RuntimeExtension[] }).extensions.find(
      (e) => e.tools.some((t) => t.name === 'memory.person'),
    )!
  }
  open()
  const flip = (patch: Partial<Scene>) => {
    store.saveScene({ ...store.scene(scene.id)!, ...patch })
    engine.settingsChanged()
  }
  return {
    get store() {
      return store
    },
    get engine() {
      return engine
    },
    get extension() {
      return extension
    },
    requests,
    sends: () => sends,
    install(fn: Router['complete']) {
      respond = fn
    },
    flip,
    seed() {
      const general = engine.memory.create({
        scope: { kind: 'scene', sceneId: scene.id },
        kind: 'fact',
        content: `${GENERAL}: synthetic codec uses fixed width integers.`,
      })
      const personal = engine.memory.create({
        scope: { kind: 'scene', sceneId: scene.id, personId: person },
        kind: 'claim',
        epistemicKind: 'hypothesis',
        content: `${PRIVATE}: unconfirmed, only this occasion.`,
      })
      const experience = engine.memory.create({
        scope: { kind: 'scene', sceneId: scene.id },
        kind: 'experience',
        epistemicKind: 'explicit',
        content: 'UNSCOPED_EXPERIENCE_CANARY',
      })
      return { general, personal, experience }
    },
    seedHabit() {
      const s = { sceneId: scene.id, messageId: 'habit-source', senderId: person }
      const result = engine.habits.propose(
        {
          ...scope,
          sourceEvidence: [
            {
              ...s,
              available: true,
              isBot: false,
              origin: 'live',
              text: 'Use this synthetic convention.',
              timestamp: Date.now(),
            },
          ],
        },
        { kind: 'sceneRule', content: HABIT, sources: [s] },
      )
      assert.equal(result.status, 'saved')
    },
    turn(id = `direct-${++sequence}`): TurnContext {
      return {
        scene,
        mode: 'real',
        turnId: id,
        signal: new AbortController().signal,
        dependencies: new Set(),
        validations: [],
        sourceSnapshots: structuredClone(engine.runtime.preview(scene.id)),
      }
    },
    ingest(id: string, text = LIVE) {
      engine.runtime.ingest(source(id, text), scene, false)
    },
    async ask(t: TestContext, text = LIVE) {
      flip({ participationEnabled: true })
      engine.runtime.ingest(source(`question-${++sequence}`, text), scene)
      await advance(t)
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
    },
  }
}

const policyDenied = async (operation: () => Promise<unknown>) => {
  await assert.rejects(
    operation,
    /memory_read_disabled|memory_read_revoked|personalization_disabled/,
  )
}

test('actual Engine defaults preserve complete personal reads; read OFF denies model tools without deleting admin-visible records', async () => {
  const h = fixture()
  try {
    const { personal } = h.seed()
    assert.equal(h.store.scene(scene.id)!.memoryReadEnabled, undefined)
    const values = (await h.extension.execute(
      'memory.person',
      { personId: person },
      h.turn(),
    )) as MemoryRecord[]
    assert.deepEqual(
      values.map((r) => r.id),
      [personal.id],
    )
    h.flip({ memoryReadEnabled: false })
    for (const [name, args] of [
      ['memory.search', { query: 'READ_POLICY' }],
      ['memory.person', { personId: person }],
      ['memory.source', { memoryId: personal.id }],
      ['memory.workingSet', {}],
      ['memory.episodes', {}],
      ['memory.commitments', {}],
      ['memory.relations', {}],
      ['experience.outcomes', {}],
    ] as const)
      await policyDenied(() => h.extension.execute(name, args, h.turn()))
    assert.equal(h.engine.memory.adminList().length, 3)
    assert.equal(h.engine.memory.personProfile(scope, person).records[0]?.id, personal.id)
    assert.equal(h.requests.length, 0)
    assert.equal(h.sends(), 0)
  } finally {
    await h.close()
  }
})

test('personalization OFF keeps non-person general knowledge but does not disguise person or experience records as general facts', async () => {
  const h = fixture()
  try {
    const { general, personal } = h.seed()
    h.seedHabit()
    h.flip({ personalizationEnabled: false })
    const values = (await h.extension.execute(
      'memory.search',
      { query: 'CANARY' },
      h.turn(),
    )) as MemoryRecord[]
    assert.deepEqual(
      values.map((r) => r.id),
      [general.id],
    )
    for (const [name, args] of [
      ['memory.person', { personId: person }],
      ['memory.workingSet', {}],
      ['memory.episodes', {}],
      ['memory.commitments', {}],
      ['memory.relations', {}],
      ['experience.outcomes', {}],
    ] as const)
      await policyDenied(() => h.extension.execute(name, args, h.turn()))
    assert.deepEqual(
      await h.extension.execute('memory.source', { memoryId: personal.id }, h.turn()),
      { error: 'Memory unavailable' },
    )
    const rules = (await h.extension.execute('rules.list', {}, h.turn())) as {
      manual: unknown[]
      learned: unknown[]
    }
    assert.match(JSON.stringify(rules.manual), new RegExp(MANUAL))
    assert.deepEqual(rules.learned, [])
    assert.equal(h.engine.memory.adminList().length, 3)
    assert.equal(h.engine.habits.adminList().length, 1)
  } finally {
    await h.close()
  }
})

for (const key of ['memoryReadEnabled', 'personalizationEnabled'] as const)
  for (const aba of [false, true])
    test(`actual Runtime delivered person receipt then ${key} OFF${aba ? '→ON' : ''} cannot reach Replyer or send`, async (t) => {
      clock(t)
      const h = fixture()
      let rounds = 0,
        exposed = false
      try {
        h.seed()
        h.install(async (_role, messages, tools = []) => {
          if (tools.some((t) => t.name === 'participation.decide'))
            return completion([
              call('participation.decide', { action: 'reply', reasonCode: 'synthetic' }),
            ])
          if (tools.some((t) => t.name === 'reply.plan')) {
            if (++rounds === 1) {
              return completion([call('memory.person', { personId: person })])
            }
            const r = receipt(messages)
            assert.match(JSON.stringify(r.value), new RegExp(PRIVATE))
            exposed = true
            h.flip({ [key]: false })
            if (aba) h.flip({ [key]: true })
            return plan(messages, {
              memorySelections: [{ evidenceId: r.id, memoryIds: r.value.map((x) => x.id) }],
            })
          }
          throw Error('Revoked memory must not reach Replyer')
        })
        await h.ask(t)
        assert.equal(exposed, true)
        assert.equal(rounds, 2)
        assert.equal(h.requests.filter((r) => r.role === 'replyer').length, 0)
        assert.equal(h.sends(), 0)
        assert.equal(h.engine.memory.adminList().length, 3)
      } finally {
        await h.close()
      }
    })

for (const key of ['memoryReadEnabled', 'personalizationEnabled'] as const)
  test(`actual Runtime learned hint delivery is revoked by ${key} OFF→ON even without a memory tool`, async (t) => {
    clock(t)
    const h = fixture()
    try {
      h.seedHabit()
      let delivered = false
      h.install(async (_role, messages, tools = []) => {
        if (tools.some((t) => t.name === 'participation.decide'))
          return completion([
            call('participation.decide', { action: 'reply', reasonCode: 'synthetic' }),
          ])
        if (tools.some((t) => t.name === 'reply.plan')) {
          assert.match(JSON.stringify(messages), new RegExp(HABIT))
          delivered = true
          h.flip({ [key]: false })
          h.flip({ [key]: true })
          return plan(messages)
        }
        throw Error('Revoked learned hint must not produce a new model call')
      })
      await h.ask(t)
      assert.equal(delivered, true)
      assert.equal(h.requests.length, 2)
      assert.equal(h.sends(), 0)
    } finally {
      await h.close()
    }
  })

for (const key of ['memoryReadEnabled', 'personalizationEnabled'] as const)
  test(`${key} OFF actual Runtime retains manual rules and current chat but omits learned hints`, async (t) => {
    clock(t)
    const h = fixture()
    try {
      h.seed()
      h.seedHabit()
      h.flip({ [key]: false })
      await h.ask(t)
      assert.equal(h.requests.length, 3)
      const wire = JSON.stringify(h.requests)
      assert.match(wire, new RegExp(MANUAL))
      assert.match(wire, new RegExp(LIVE))
      assert.doesNotMatch(wire, new RegExp(`${PRIVATE}|${HABIT}|UNSCOPED_EXPERIENCE_CANARY`))
      assert.equal(h.requests.filter((r) => r.role === 'replyer').length, 1)
    } finally {
      await h.close()
    }
  })

test('a previously exposed direct turn cannot downgrade learning after policy ABA, while a fresh source-only turn can learn with reads OFF', async () => {
  const h = fixture()
  try {
    h.seed()
    h.ingest('fresh-declaration', 'Synthetic newly declared preference.')
    const old = h.turn()
    await h.extension.execute('memory.person', { personId: person }, old)
    h.flip({ memoryReadEnabled: false })
    h.flip({ memoryReadEnabled: true })
    const candidate = {
      content: 'Synthetic newly declared preference.',
      kind: 'claim',
      sourceMessageIds: ['fresh-declaration'],
    }
    const rejected = (await h.extension.execute('learning.propose', candidate, old)) as {
      status: string
    }
    assert.equal(rejected.status, 'rejected')
    assert.equal(h.engine.memory.adminList().length, 3)
    h.flip({ memoryReadEnabled: false })
    const queued = (await h.extension.execute('learning.propose', candidate, h.turn())) as {
      status: string
    }
    assert.equal(queued.status, 'queued')
    await h.engine.contextualLearning.queue.whenIdle()
    const saved = h.engine.memory.adminList().find((r) => r.content === candidate.content)
    assert.ok(saved)
    const row = h.store.db
      .prepare('SELECT value FROM memory_supports WHERE memory_id=?')
      .get(saved.id) as { value: string }
    const proofs = JSON.parse(row.value) as Array<{ parents: string[] }>
    assert.ok(proofs.length > 0)
    assert.ok(proofs.every((p) => p.parents.length === 0))
    assert.equal(h.requests.length, 0)
  } finally {
    await h.close()
  }
})

for (const key of ['memoryReadEnabled', 'personalizationEnabled'] as const)
  test(`${key} OFF background received-to-learning never reads old memories; only permitted general-knowledge review can run`, async (t) => {
    clock(t)
    const h = fixture()
    try {
      h.seed()
      h.flip({ [key]: false })
      h.store.set('settings', { ...h.store.settings, learningBatchMs: 100, learningMaxWaitMs: 100 })
      h.engine.settingsChanged()
      let searches = 0
      const search = h.engine.searchMemory.bind(h.engine)
      h.engine.searchMemory = (...args) => {
        searches++
        return search(...args)
      }
      h.install(async (role, messages, tools = []) => {
        assert.equal(role, 'learning')
        assert.doesNotMatch(
          JSON.stringify(messages),
          new RegExp(`${PRIVATE}|${GENERAL}|UNSCOPED_EXPERIENCE_CANARY`),
        )
        if (tools.some((t) => t.name === 'knowledge.review')) {
          assert.equal(key, 'personalizationEnabled')
          assert.equal(
            initial(messages).candidate,
            'UTF-8 uses variable-width encoding for Unicode code points.',
          )
          return completion([
            call('knowledge.review', {
              shareable: false,
              publicnessConfidence: 0,
              sourceSpecific: false,
              containsPersonalData: false,
            }),
          ])
        }
        assert.ok(tools.some((t) => t.name === 'learning.propose'))
        const id = initial(messages).messages[0].id
        return completion([
          call('learning.propose', {
            content: 'UTF-8 uses variable-width encoding for Unicode code points.',
            kind: 'fact',
            publicTechnicalDomain: 'unicode',
            sourceMessageIds: [id],
          }),
        ])
      })
      h.ingest('fresh-technical', 'UTF-8 uses variable-width encoding for Unicode code points.')
      await advance(t, 100)
      assert.equal(searches, 0)
      assert.equal(h.requests.length, key === 'memoryReadEnabled' ? 1 : 2)
      assert.equal(h.engine.learningStatus().completed, 1)
      const saved = h.engine.memory.adminList().filter((r) => r.origin === 'learned')
      assert.equal(saved.length, 1)
      assert.equal(saved[0]!.scope.kind, 'scene')
      assert.equal(h.sends(), 0)
    } finally {
      await h.close()
    }
  })

test('scene read flags and independent revision survive restart; old callers and caller-supplied revision cannot reopen them', async () => {
  const h = fixture()
  try {
    const { personal } = h.seed()
    h.flip({ memoryReadEnabled: false, personalizationEnabled: false })
    const firstRevision = h.store.scene(scene.id)!.memoryReadRevision!
    assert.ok(firstRevision > 0)
    await h.reopen()
    assert.equal(h.requests.length, 0, 'restart never creates model replay')
    assert.equal(h.store.scene(scene.id)!.memoryReadEnabled, false)
    assert.equal(h.store.scene(scene.id)!.personalizationEnabled, false)
    const {
      memoryReadEnabled: _read,
      personalizationEnabled: _personal,
      ...oldCaller
    } = h.store.scene(scene.id)!
    h.store.saveScene({ ...oldCaller, memoryReadRevision: 0, name: 'Cosmetic rename' })
    assert.equal(h.store.scene(scene.id)!.memoryReadEnabled, false)
    assert.equal(h.store.scene(scene.id)!.memoryReadRevision, firstRevision)
    await policyDenied(() => h.extension.execute('memory.person', { personId: person }, h.turn()))
    h.flip({ memoryReadEnabled: true, personalizationEnabled: true })
    assert.ok(h.store.scene(scene.id)!.memoryReadRevision! > firstRevision)
    await h.reopen()
    const records = (await h.extension.execute(
      'memory.person',
      { personId: person },
      h.turn(),
    )) as MemoryRecord[]
    assert.deepEqual(
      records.map((r) => r.id),
      [personal.id],
    )
    assert.equal(h.requests.length, 0)
  } finally {
    await h.close()
  }
})

for (const change of ['none', 'memoryReadEnabled', 'personalizationEnabled'] as const)
  test(`actual noReply queued learning retains its original read permission across turn completion: ${change}`, async (t) => {
    clock(t)
    const h = fixture()
    const content = `Synthetic contextual result for ${change}.`
    let rounds = 0,
      queuedContext: TurnContext | undefined
    try {
      h.seed()
      const execute = h.extension.execute.bind(h.extension)
      h.extension.execute = async (name, input, context) => {
        const result = await execute(name, input, context)
        if (name === 'learning.propose') {
          assert.equal((result as { status: string }).status, 'queued')
          queuedContext = context
        }
        return result
      }
      h.install(async (_role, messages, tools = []) => {
        if (tools.some((tool) => tool.name === 'participation.decide'))
          return completion([
            call('participation.decide', { action: 'reply', reasonCode: 'synthetic' }),
          ])
        assert.ok(tools.some((tool) => tool.name === 'reply.plan'))
        if (++rounds === 1) return completion([call('memory.person', { personId: person })])
        assert.match(JSON.stringify(receipt(messages)), new RegExp(PRIVATE))
        return plan(messages, {
          action: 'noReply',
          actions: [
            {
              toolName: 'learning.propose',
              arguments: {
                content,
                kind: 'claim',
                personId: person,
                sourceMessageIds: initial(messages).planContract.contextMessageIds,
              },
            },
          ],
        })
      })
      await h.ask(t)
      assert.ok(queuedContext)
      assert.equal(
        queuedContext.signal.aborted,
        true,
        'normal turn has finished before independent work',
      )
      assert.equal(h.engine.contextualLearning.queue.stats().accepted, 1)
      assert.equal(h.engine.memory.adminList().length, 3, 'foreground priority deferred the write')
      if (change !== 'none') {
        h.flip({ [change]: false })
        h.flip({ [change]: true })
      }
      await advance(t, 100)
      await h.engine.contextualLearning.queue.whenIdle()
      assert.equal(
        h.engine.memory.adminList().some((record) => record.content === content),
        change === 'none',
      )
      assert.equal(h.engine.contextualLearning.queue.stats().completed, change === 'none' ? 1 : 0)
      assert.equal(h.requests.filter((request) => request.role === 'replyer').length, 0)
      assert.equal(h.sends(), 0)
      if (change !== 'none') {
        const fresh = h.turn()
        await h.extension.execute('memory.person', { personId: person }, fresh)
        const retry = (await h.extension.execute(
          'learning.propose',
          {
            content,
            kind: 'claim',
            personId: person,
            sourceMessageIds: fresh.sourceSnapshots!.map((message) => message.id),
          },
          fresh,
        )) as { status: string }
        assert.equal(
          retry.status,
          'queued',
          'new exact read ticket is not deduplicated against the discarded old ticket',
        )
        await h.engine.contextualLearning.queue.whenIdle()
        assert.ok(h.engine.memory.adminList().some((record) => record.content === content))
      }
    } finally {
      await h.close()
    }
  })

test('read permission is captured before asynchronous Engine search, so an OFF→ON handoff cannot expose a result from the old ticket', async () => {
  const h = fixture()
  try {
    const { personal } = h.seed()
    h.engine.searchMemory = async () => {
      h.flip({ memoryReadEnabled: false })
      await Promise.resolve()
      h.flip({ memoryReadEnabled: true })
      return [personal]
    }
    const turn = h.turn()
    await policyDenied(() => h.extension.execute('memory.search', { query: 'synthetic' }, turn))
    assert.equal(turn.dependencies.size, 0)
    assert.ok(turn.validations.some((valid) => !valid()))
    const rejected = (await h.extension.execute(
      'learning.propose',
      { content: 'Cannot reclassify an old read.', kind: 'claim', sourceMessageIds: ['unknown'] },
      turn,
    )) as { status: string }
    assert.equal(rejected.status, 'rejected')
    assert.equal(h.engine.contextualLearning.queue.stats().accepted, 0)
    assert.equal(h.requests.length, 0)
  } finally {
    await h.close()
  }
})

for (const key of ['memoryReadEnabled', 'personalizationEnabled'] as const)
  test(`actual experience Planner-only hint is omitted when ${key} OFF and cannot survive later OFF→ON`, async (t) => {
    clock(t)
    const h = fixture()
    try {
      // Establish a real synthetic adapter ACK, then real live explicit feedback. No fixture policy row.
      h.install(async (role, messages, tools = []) => {
        if (tools.some((tool) => tool.name === 'participation.decide'))
          return completion([
            call('participation.decide', { action: 'reply', reasonCode: 'synthetic' }),
          ])
        if (tools.some((tool) => tool.name === 'reply.plan')) return plan(messages)
        assert.equal(role, 'replyer')
        return messages.some((message) => message.role === 'tool')
          ? completion()
          : completion([
              call('messages.send', {
                operationId: 'synthetic-seed',
                segments: [
                  {
                    parts: [
                      {
                        type: 'text',
                        text: 'Synthetic response to establish an exact feedback target.',
                      },
                    ],
                  },
                ],
              }),
            ])
      })
      await h.ask(t)
      assert.equal(h.sends(), 1)
      h.flip({ participationEnabled: false })
      h.engine.runtime.ingest(
        { ...source('preference-source', '请先说结论'), replyToId: 'synthetic-bot-1' },
        scene,
        false,
      )
      const policies = h.engine.experience.adminList(scope).items
      assert.equal(policies.length, 1)
      assert.equal(policies[0]!.status, 'active')
      h.requests.length = 0
      h.flip({ [key]: false })
      h.install(async (_role, messages, tools = []) => {
        if (tools.some((tool) => tool.name === 'participation.decide'))
          return completion([
            call('participation.decide', { action: 'reply', reasonCode: 'synthetic' }),
          ])
        assert.ok(tools.some((tool) => tool.name === 'reply.plan'))
        assert.equal(initial(messages).policyContext, undefined)
        return plan(messages, { action: 'noReply' })
      })
      await h.ask(t)
      assert.equal(h.sends(), 1)
      h.flip({ [key]: true })
      let delivered = false
      h.install(async (_role, messages, tools = []) => {
        if (tools.some((tool) => tool.name === 'participation.decide')) {
          assert.doesNotMatch(JSON.stringify(messages), /conclusion_before_detail/)
          return completion([
            call('participation.decide', { action: 'reply', reasonCode: 'synthetic' }),
          ])
        }
        assert.ok(tools.some((tool) => tool.name === 'reply.plan'))
        assert.equal(initial(messages).policyContext[0].hints[0].action, 'conclusion_before_detail')
        delivered = true
        h.flip({ [key]: false })
        h.flip({ [key]: true })
        return plan(messages)
      })
      await h.ask(t)
      assert.equal(delivered, true)
      assert.equal(h.requests.filter((request) => request.role === 'replyer').length, 0)
      assert.equal(h.sends(), 1, 'the original seed ACK is not a new policy-driven send')
      assert.equal(
        h.engine.experience.adminList(scope).items[0]!.uses.applied,
        0,
        'style exposure is not applied behavior or evidence of efficacy',
      )
    } finally {
      await h.close()
    }
  })

test('read OFF still permits bounded source-only neutral repair without old memory or rejected wording in repair input', async (t) => {
  clock(t)
  const h = fixture()
  try {
    h.seed()
    h.flip({ memoryReadEnabled: false })
    h.store.set('settings', { ...h.store.settings, learningBatchMs: 100, learningMaxWaitMs: 100 })
    h.engine.settingsChanged()
    h.engine.searchMemory = async () => {
      throw Error('No old-memory search is permitted')
    }
    let repairs = 0
    h.install(async (role, messages, tools = []) => {
      assert.equal(role, 'learning')
      assert.doesNotMatch(JSON.stringify(messages), new RegExp(`${PRIVATE}|${GENERAL}`))
      if (tools.some((tool) => tool.name === 'learning.repair')) {
        repairs++
        assert.doesNotMatch(JSON.stringify(messages), /He prefers SYNTHETIC_REJECTED_WORDING/)
        assert.match(JSON.stringify(messages), /prefer neutral wording/)
        return completion([
          call('learning.repair', {
            candidateIndex: 0,
            content: 'The source requests neutral wording.',
          }),
        ])
      }
      assert.ok(tools.some((tool) => tool.name === 'learning.propose'))
      return completion([
        call('learning.propose', {
          kind: 'claim',
          content: 'He prefers SYNTHETIC_REJECTED_WORDING.',
          sourceMessageIds: [initial(messages).messages[0].id],
        }),
      ])
    })
    h.ingest('repair-source', 'I prefer neutral wording.')
    await advance(t, 100)
    assert.equal(repairs, 1)
    assert.equal(h.requests.length, 2)
    assert.equal(
      h.engine.memory.adminList().filter((record) => record.origin === 'learned')[0]?.content,
      'The source requests neutral wording.',
    )
    assert.equal(h.sends(), 0)
  } finally {
    await h.close()
  }
})

async function until(predicate: () => boolean, description: string) {
  const deadline = Date.now() + 3000
  while (!predicate()) {
    assert.ok(Date.now() < deadline, description)
    await new Promise((resolve) => setTimeout(resolve, 5))
  }
}
for (const protocol of ['native', 'custom'] as const)
  test(`actual Engine Runtime ${protocol} physical request queued after person delivery is refused after read OFF→ON`, async () => {
    const h = fixture()
    let calls = 0,
      release = () => {},
      blocked = false
    let blockers: Promise<void>[] = []
    const nativeBodies: unknown[] = []
    const next = () => {
      calls++
      if (calls === 1)
        return call('participation.decide', {
          action: 'reply',
          reasonCode: 'synthetic_physical_boundary',
        })
      assert.equal(
        calls,
        2,
        'no physical request is permitted after the delivered ticket is revoked',
      )
      return call('memory.person', { personId: person })
    }
    const server = createServer(async (request, response) => {
      const chunks: Buffer[] = []
      for await (const chunk of request) chunks.push(Buffer.from(chunk))
      nativeBodies.push(JSON.parse(Buffer.concat(chunks).toString()))
      const result = next()
      response.setHeader('Content-Type', 'application/json')
      response.end(
        JSON.stringify({
          choices: [
            {
              finish_reason: 'tool_calls',
              message: {
                content: '',
                tool_calls: [
                  {
                    id: result.id,
                    type: 'function',
                    function: {
                      name: result.name.replaceAll('.', '__'),
                      arguments: JSON.stringify(result.arguments),
                    },
                  },
                ],
              },
            },
          ],
          usage: { prompt_tokens: 1, completion_tokens: 1, total_tokens: 2 },
        }),
      )
    })
    await new Promise<void>((resolve) => server.listen(0, '127.0.0.1', resolve))
    const lifecycle = new AbortController()
    try {
      h.seed()
      h.store.set('models', {
        providers: [
          {
            id: 'synthetic',
            name: 'Local read policy',
            protocol: protocol === 'native' ? 'openai-chat' : 'fixture:read-policy',
            baseUrl: `http://127.0.0.1:${(server.address() as { port: number }).port}`,
            credentialEnv: '',
            retryCount: 0,
            timeoutMs: 2000,
          },
        ],
        models: [
          {
            id: 'synthetic',
            displayName: 'Local read policy',
            providerId: 'synthetic',
            apiModelName: 'synthetic',
            capabilities: ['text', 'tools'],
          },
        ],
        routes: ['planner', 'replyer'].map((role) => ({
          role,
          orderedModelIds: ['synthetic'],
          timeoutMs: 3000,
        })),
      })
      h.engine.protocols.register(
        'fixture:read-policy',
        'Synthetic read policy',
        {
          async complete() {
            return {
              text: '',
              toolCalls: [next()],
              usage: { inputTokens: 1, outputTokens: 1, totalTokens: 2 },
            }
          },
        },
        lifecycle.signal,
      )
      // Restore the real factory: this case tests Engine's per-physical-attempt supplier,
      // unlike the scripted Router fixtures above, which test Runtime eligibility only.
      h.engine.router = Engine.prototype.router.bind(h.engine)
      const deliver = h.extension.delivered!.bind(h.extension)
      h.extension.delivered = (name, value, context, input) => {
        const result = deliver(name, value, context, input)
        if (name === 'memory.person') {
          assert.match(JSON.stringify(result), new RegExp(PRIVATE))
          const wait = new Promise<void>((resolve) => {
            release = resolve
          })
          blockers = Array.from({ length: modelScheduler.status.concurrency }, () =>
            modelScheduler.run(0, undefined, () => wait),
          )
          blocked = true
        }
        return result
      }
      h.flip({ participationEnabled: true })
      h.engine.runtime.ingest(source('queued-physical-query'), scene)
      await until(
        () => blocked && modelScheduler.status.queued === 1,
        'real next model request must be waiting for a scheduler slot',
      )
      assert.equal(calls, 2)
      h.flip({ memoryReadEnabled: false })
      h.flip({ memoryReadEnabled: true })
      release()
      await Promise.all(blockers)
      await until(() => h.engine.runtime.activeTasks === 0, 'revoked runtime must settle')
      assert.equal(calls, 2)
      assert.equal(nativeBodies.length, protocol === 'native' ? 2 : 0)
      assert.equal(h.sends(), 0)
      assert.equal(modelScheduler.status.queued, 0)
    } finally {
      release()
      await Promise.all(blockers)
      lifecycle.abort()
      await h.close()
      server.closeAllConnections()
      await new Promise<void>((resolve) => server.close(() => resolve()))
    }
  })

test('fresh read-OFF current PNG still reaches direct vision without stored library observations or background media learning', async () => {
  const h = fixture()
  let libraryReads = 0,
    imageReads = 0,
    visionCalls = 0,
    decisions = 0
  try {
    h.flip({ memoryReadEnabled: false, participationEnabled: true })
    h.store.set('models', {
      providers: [
        {
          id: 'synthetic-vision',
          name: 'Synthetic current vision',
          protocol: 'openai-chat',
          baseUrl: 'http://127.0.0.1:1',
          credentialEnv: '',
        },
      ],
      models: [
        {
          id: 'synthetic-vision',
          displayName: 'Synthetic current vision',
          providerId: 'synthetic-vision',
          apiModelName: 'synthetic',
          capabilities: ['vision'],
        },
      ],
      routes: [{ role: 'vision', orderedModelIds: ['synthetic-vision'], timeoutMs: 3000 }],
    })
    const png = await sharp({ create: { width: 2, height: 2, channels: 3, background: '#33aa99' } })
      .png()
      .toBuffer()
    const forbidden = () => {
      libraryReads++
      throw Error('Stored library observations are not authorized')
    }
    h.engine.memes.lookupMediaReference = forbidden
    h.engine.memes.recognizeBytes = forbidden
    h.engine.memes.hasRecognitionCandidates = forbidden
    h.engine.memes.analyze = forbidden
    h.engine.memes.search = forbidden
    h.engine.runtime.addAdapter(scene.adapterId, {
      send: async () => {
        throw Error('Unexpected synthetic send')
      },
      readImage: async () => {
        imageReads++
        return png
      },
      getMessage: async () => ({ items: [], coverage: 'complete' }),
      history: async () => ({ items: [], coverage: 'complete' }),
      dispose() {},
    })
    h.install(async (role, messages, tools = []) => {
      if (role === 'vision') {
        visionCalls++
        assert.match(messages[1]!.images![0]!, /^data:image\/png;base64,/)
        return { ...completion(), text: 'CURRENT_PNG_VISIBLE_SYNTHETIC_OBSERVATION' }
      }
      assert.ok(tools.some((tool) => tool.name === 'participation.decide'))
      decisions++
      assert.match(JSON.stringify(messages), /CURRENT_PNG_VISIBLE_SYNTHETIC_OBSERVATION/)
      return completion([
        call('participation.decide', { action: 'silent', reasonCode: 'synthetic' }),
      ])
    })
    h.engine.runtime.ingest(
      {
        ...source('fresh-current-image'),
        parts: [{ type: 'media', assetId: 'current-synthetic-png', mediaType: 'image/png' }],
      },
      scene,
    )
    await until(
      () => decisions === 1 && h.engine.runtime.activeTasks === 0,
      'current image must reach participation under read OFF',
    )
    assert.equal(visionCalls, 1)
    assert.equal(imageReads, 1)
    assert.equal(libraryReads, 0)
    assert.equal(h.engine.memory.adminList().length, 0)
    assert.equal(h.engine.memes.adminList().length, 0)
    assert.equal(h.sends(), 0)
  } finally {
    await h.close()
  }
})

function configureSyntheticEmbedding(h: ReturnType<typeof fixture>) {
  const inputs: string[][] = []
  const controller = new AbortController()
  h.engine.protocols.register(
    'fixture:read-embedding',
    'Synthetic read embedding',
    {
      async complete() {
        throw Error('Embedding fixture must not call completion')
      },
      async embeddings(_context, texts) {
        inputs.push([...texts])
        return {
          vectors: texts.map(() => [1, 0.5]),
          usage: { inputTokens: 1, outputTokens: 0, totalTokens: 1 },
        }
      },
    },
    controller.signal,
  )
  h.store.set('models', {
    providers: [
      {
        id: 'synthetic-embedding',
        name: 'Synthetic read embedding',
        protocol: 'fixture:read-embedding',
        baseUrl: 'http://127.0.0.1:1',
        credentialEnv: '',
        retryCount: 0,
        timeoutMs: 2000,
      },
    ],
    models: [
      {
        id: 'synthetic-embedding',
        displayName: 'Synthetic read embedding',
        providerId: 'synthetic-embedding',
        apiModelName: 'synthetic',
        capabilities: ['embedding'],
        embeddingDimensions: 2,
        embeddingBatchSize: 4,
      },
    ],
    routes: [{ role: 'embedding', orderedModelIds: ['synthetic-embedding'], timeoutMs: 3000 }],
  })
  h.engine.router = Engine.prototype.router.bind(h.engine)
  return { inputs, stop: () => controller.abort() }
}

test('actual Engine embedding respects read OFF without deleting vectors; personalization OFF permits only general-record indexing', async () => {
  const h = fixture(),
    embedding = configureSyntheticEmbedding(h)
  try {
    const records = h.seed()
    h.flip({ personalizationEnabled: false })
    await h.engine.embeddingIndex.index(records.general.id, records.general.content)
    assert.deepEqual(embedding.inputs, [[records.general.content]])
    const vectors = h.store.db.prepare('SELECT * FROM memory_vectors').all()
    assert.equal(vectors.length, 1)
    const active = h.engine.embeddingIndex.status().active
    assert.ok(active)
    await h.engine.embeddingIndex.index(records.personal.id, records.personal.content)
    await h.engine.embeddingIndex.index(records.experience.id, records.experience.content)
    assert.equal(embedding.inputs.length, 1)
    h.flip({ memoryReadEnabled: false })
    await h.engine.embeddingIndex.index(records.general.id, records.general.content)
    h.engine.embeddingIndex.start()
    await h.engine.embeddingIndex.settled()
    assert.equal(
      embedding.inputs.length,
      1,
      'neither direct index nor rebuild exposes old bodies with reads OFF',
    )
    const failed = h.engine.embeddingIndex.status()
    assert.equal(failed.job?.state, 'failed')
    assert.equal(failed.job?.error, 'memory_read_disabled')
    assert.deepEqual(failed.active, active, 'a blocked generation must not become the active index')
    assert.deepEqual(h.store.db.prepare('SELECT * FROM memory_vectors').all(), vectors)
    assert.equal(h.engine.memory.adminList().length, 3)
  } finally {
    embedding.stop()
    await h.close()
  }
})

for (const operation of ['index', 'rebuild'] as const)
  test(`actual Engine ${operation} embedding queued under an old ON ticket cannot execute or commit after OFF→ON`, async () => {
    const h = fixture(),
      embedding = configureSyntheticEmbedding(h)
    let release = () => {},
      task: Promise<void> | undefined
    let blockers: Promise<void>[] = []
    try {
      const { general } = h.seed()
      const wait = new Promise<void>((resolve) => {
        release = resolve
      })
      blockers = Array.from({ length: modelScheduler.status.concurrency }, () =>
        modelScheduler.run(0, undefined, () => wait),
      )
      await immediate()
      if (operation === 'index') task = h.engine.embeddingIndex.index(general.id, general.content)
      else {
        h.engine.embeddingIndex.start()
        task = h.engine.embeddingIndex.settled()
      }
      await until(
        () => modelScheduler.status.queued === 1,
        'embedding must be queued before permission changes',
      )
      assert.equal(embedding.inputs.length, 0)
      h.flip({ memoryReadEnabled: false })
      h.flip({ memoryReadEnabled: true })
      release()
      await Promise.all(blockers)
      await task
      assert.equal(embedding.inputs.length, 0)
      assert.equal(h.store.db.prepare('SELECT * FROM memory_vectors').all().length, 0)
      assert.equal(h.engine.embeddingIndex.status().active, undefined)
      if (operation === 'rebuild') {
        assert.equal(h.engine.embeddingIndex.status().job?.state, 'failed')
        assert.equal(h.engine.embeddingIndex.status().job?.error, 'memory_read_revoked')
      }
      assert.equal(h.engine.memory.adminList().length, 3)
    } finally {
      release()
      await Promise.all(blockers)
      await task
      embedding.stop()
      await h.close()
    }
  })
