import assert from 'node:assert/strict'
import test, { type TestContext } from 'node:test'
import { mkdtempSync, rmSync } from 'node:fs'
import { join } from 'node:path'
import { tmpdir } from 'node:os'
import { setImmediate as immediate } from 'node:timers/promises'
import { Engine } from '../src/engine.ts'
import { Store } from '../src/store.ts'
import { createApp } from '../src/api.ts'
import type { SceneRef } from '../src/onebot.ts'
import type { RuntimeMessage, RuntimeExtension, TurnContext } from '../src/runtime.ts'
import type { ChatMessage, Completion, Router } from '../src/models.ts'
import type { MemoryRecord } from '../src/memory.ts'
import type { IdentityEndpoint, IdentityAlias } from '../src/memory/identities/contracts.ts'
import type { IdentityView } from '../src/memory/read/identity-exposure.ts'
import { fixtureReplyPlan } from './helpers/reply-plan.ts'

// Synthetic model contracts only: verified links remain qualified pointers, not semantic identity
// inference, personal facts, expanded access, or measured model-quality benefit.
const scene: SceneRef = {
  id: 'identity-group',
  kind: 'group',
  adapterId: 'offline',
  accountId: 'account',
  platformSceneId: 'synthetic-group',
}
const privateScene: SceneRef = {
  ...scene,
  id: 'identity-private',
  kind: 'private',
  platformSceneId: 'synthetic-private',
}
const otherNamespace: SceneRef = {
  ...scene,
  id: 'identity-namespace',
  accountId: 'other-account',
  platformSceneId: 'synthetic-other',
}
const person = 'canonical-a',
  peer = 'canonical-b',
  third = 'canonical-c'
const scope = { sceneId: scene.id, simulation: false }
const endpoint = (personId: string, target = scene): IdentityEndpoint => ({
  adapterId: target.adapterId,
  accountId: target.accountId,
  personId,
})
const call = (name: string, args: Record<string, unknown> = {}, id = name) => ({
  id,
  name,
  arguments: args,
})
const completion = (toolCalls: Completion['toolCalls'] = []): Completion => ({
  text: '',
  toolCalls,
  modelId: 'offline-identity',
  usage: { inputTokens: 0, outputTokens: 0, totalTokens: 0 },
  assistant: { role: 'assistant', content: '', toolCalls },
})
const plan = (messages: ChatMessage[], patch: Parameters<typeof fixtureReplyPlan>[1] = {}) =>
  completion([call('reply.plan', fixtureReplyPlan(messages, patch)!)])
const message = (id: string, senderId = person, target = scene): RuntimeMessage => ({
  id,
  sceneId: target.id,
  senderId,
  senderDisplayName: 'Same nickname',
  origin: 'live',
  status: 'available',
  sentAt: new Date().toISOString(),
  receivedAt: new Date().toISOString(),
  parts: [{ type: 'text', text: `Synthetic current declaration ${id}.` }],
})
function clock(t: TestContext) {
  t.mock.timers.enable({
    apis: ['Date', 'setTimeout', 'setInterval'],
    now: new Date('2026-10-29T09:00:00Z'),
  })
}
async function advance(t: TestContext, ms = 10) {
  t.mock.timers.tick(ms)
  for (let i = 0; i < 12; i++) {
    await immediate()
    t.mock.timers.tick(0)
  }
}
type Receipt = {
  id: string
  toolName: string
  value: any[]
  coverage?: { status: string; returnedRecords: number; omittedRecords: number; reason?: string }
}
const receipts = (messages: ChatMessage[]): Receipt[] =>
  messages.filter((m) => m.role === 'tool').map((m) => JSON.parse(m.content))
const payload = (messages: ChatMessage[]) =>
  JSON.parse(messages.find((m) => m.role === 'user')!.content)
function fixture() {
  const directory = mkdtempSync(join(tmpdir(), 'he-identity-runtime-')),
    path = join(directory, 'synthetic.sqlite')
  let store: Store, engine: Engine, extension: RuntimeExtension, app: ReturnType<typeof createApp>
  let sequence = 0,
    sends = 0
  const replies: ChatMessage[][] = [],
    requests: Array<{ role: string; messages: ChatMessage[] }> = [],
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
      for (const s of [scene, privateScene, otherNamespace])
        store.saveScene({
          ...s,
          name: 'Synthetic identities',
          participationEnabled: false,
          learningEnabled: false,
          proactiveEnabled: false,
        })
    }
    engine = new Engine(store)
    engine.indexMemory = async () => {}
    engine.runtime.addAdapter(scene.adapterId, {
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
                call('participation.decide', { action: 'reply', reasonCode: 'synthetic_identity' }),
              ])
            if (tools.some((tool) => tool.name === 'reply.plan')) return await planner(messages)
            assert.equal(role, 'replyer', 'no background model call is expected')
            replies.push(structuredClone(messages))
            return completion()
          } catch (error) {
            errors.push(error)
            throw error
          }
        },
      }) as Router
    extension = (engine.runtime as unknown as { extensions: RuntimeExtension[] }).extensions.find(
      (e) => e.tools.some((tool) => tool.name === 'memory.identities'),
    )!
    assert.ok(extension)
    app = createApp(store, { token: 'synthetic-identity-admin', engine })
  }
  open()
  const command = () => ({
    operationId: `operation-${++sequence}`,
    expectedRevision: engine.memory.identities.witness().revision,
    adminConfirmed: true as const,
  })
  const transaction = <T>(fn: () => T) => store.db.transaction(fn).immediate()
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
    replies,
    requests,
    record(
      personId = person,
      target = scene,
      content = `FACT_${personId}_${target.id}: not independently verified.`,
    ) {
      return engine.memory.create({
        scope: { kind: 'scene', sceneId: target.id, personId },
        kind: 'claim',
        epistemicKind: 'hypothesis',
        content,
        sources: [
          {
            sceneId: target.id,
            messageId: `original-${personId}-${target.id}`,
            senderId: personId,
          },
        ],
      })
    },
    seed() {
      return [this.record(person), this.record(peer), this.record(third)]
    },
    link(a = endpoint(person), b = endpoint(peer)) {
      return transaction(() => engine.memory.identities.registry.link(command(), a, b))
    },
    unlink(a = endpoint(person), b = endpoint(peer)) {
      return transaction(() => engine.memory.identities.registry.unlink(command(), a, b))
    },
    split(partition = [endpoint(person)]) {
      return transaction(() => engine.memory.identities.registry.split(command(), partition))
    },
    alias(
      label: string,
      personId = person,
      target = scene,
      dates: Partial<Pick<IdentityAlias, 'validFrom' | 'validUntil'>> = {},
    ) {
      const id = `alias-${++sequence}`
      transaction(() =>
        engine.memory.identities.putManualAlias(
          command(),
          {
            id,
            endpoint: endpoint(personId, target),
            scopeSceneId: target.id,
            label,
            validFrom: null,
            validUntil: null,
            ...dates,
          },
          'synthetic-admin',
        ),
      )
      return id
    },
    observed(label = 'OBSERVED_ALIAS_CANARY', id = `observation-${++sequence}`) {
      engine.runtime.ingest(message(id), scene, false)
      const source = engine.runtime.preview(scene.id).find((m) => m.id === id)! as RuntimeMessage
      const evidence = {
        sceneId: scene.id,
        messageId: id,
        senderId: person,
        eventRef: source.sourceEventRef,
        available: true,
        isBot: false,
      }
      transaction(() =>
        engine.memory.identities.observeAlias({ ...scope, sourceEvidence: [evidence] }, command(), {
          id: `alias-${id}`,
          endpoint: endpoint(person),
          scopeSceneId: scene.id,
          label,
          source: {
            kind: 'observation',
            sceneId: scene.id,
            messageId: id,
            senderId: person,
            eventRef: source.sourceEventRef,
          },
          validFrom: null,
          validUntil: null,
        }),
      )
      return source
    },
    turn(target = scene, simulation = false): TurnContext {
      return {
        scene: target,
        mode: simulation ? 'simulation' : 'real',
        turnId: `direct-${++sequence}`,
        signal: new AbortController().signal,
        dependencies: new Set(),
        validations: [],
        sourceSnapshots: structuredClone(engine.runtime.preview(target.id)),
      }
    },
    read(context: TurnContext, id = person) {
      return extension.execute('memory.identities', { personId: id }, context) as Promise<
        IdentityView[]
      >
    },
    async request(body?: object, authenticated = true) {
      return app.request('/api/memory-identities', {
        ...(body ? { method: 'POST', body: JSON.stringify(body) } : {}),
        headers: {
          'Content-Type': 'application/json',
          ...(authenticated ? { Authorization: 'Bearer synthetic-identity-admin' } : {}),
        },
      })
    },
    async ask(t: TestContext, fn: typeof planner) {
      planner = fn
      store.saveScene({ ...store.scene(scene.id)!, participationEnabled: true })
      engine.runtime.ingest(message(`query-${++sequence}`), scene)
      await advance(t)
      if (errors.length) throw errors.at(-1)
      assert.equal(engine.runtime.activeTasks, 0)
    },
    truth() {
      return [
        'memories',
        'memory_supports',
        'memory_assertion_revisions',
        'memory_assertion_state',
      ].map((table) => [table, store.db.prepare(`SELECT * FROM ${table}`).all()])
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

test('actual authenticated administration links equal-nickname people without merging ordinary exact-person memories, across restart', async () => {
  const h = fixture()
  try {
    const [a, b] = h.seed()
    h.alias('Same nickname', person)
    h.alias('Same nickname', peer)
    assert.deepEqual(
      (await h.read(h.turn()))[0]!.page.items.map((item) => item.personId),
      [person],
    )
    const before = h.truth(),
      revision = h.engine.memory.identities.witness().revision
    const body = {
      action: 'link',
      operationId: 'api-link',
      expectedRevision: revision,
      confirmed: true,
      left: endpoint(person),
      right: endpoint(peer),
    }
    assert.equal((await h.request(undefined, false)).status, 401)
    assert.equal((await h.request(body)).status, 200)
    assert.deepEqual(await (await h.request(body)).json(), {
      revision: revision + 1,
      duplicate: true,
    })
    assert.equal(
      (await h.request({ ...body, operationId: 'stale-command', right: endpoint(third) })).status,
      409,
    )
    for (let read = 0; read < 2; read++) {
      const linked = (await h.read(h.turn()))[0]!
      assert.deepEqual(
        linked.page.items.map((item) => item.personId),
        [person, peer],
      )
      assert.equal(
        linked.page.interpretation,
        'administrator_verified_links_not_read_authority_or_personal_facts',
      )
      const ordinary = (await h.extension.execute(
        'memory.person',
        { personId: person },
        h.turn(),
      )) as MemoryRecord[]
      assert.deepEqual(
        ordinary.map((record) => record.id),
        [a!.id],
      )
      assert.ok(!ordinary.some((record) => record.id === b!.id))
      assert.deepEqual(h.truth(), before)
      if (!read) await h.reopen()
    }
    assert.equal(h.requests.length, 0)
  } finally {
    await h.close()
  }
})

test('actual Engine identity links never grant another scene or adapter-account namespace, nor rewrite original source identities', async () => {
  const h = fixture()
  try {
    const a = h.record(person),
      b = h.record(peer, privateScene),
      foreign = h.record('foreign', otherNamespace)
    h.record(person, privateScene)
    h.record(person, otherNamespace)
    h.link()
    h.link(endpoint(person), endpoint('foreign', otherNamespace))
    h.alias('PRIVATE_SCENE_ALIAS', person, privateScene)
    h.alias('OTHER_ACCOUNT_ALIAS', person, otherNamespace)
    const publicPage = (await h.read(h.turn()))[0]!
    assert.deepEqual(
      publicPage.page.items.map((item) => item.personId),
      [person],
    )
    assert.doesNotMatch(JSON.stringify(publicPage), /PRIVATE_SCENE_ALIAS|OTHER_ACCOUNT_ALIAS/)
    const records = (await h.extension.execute(
      'memory.person',
      { personId: person },
      h.turn(),
    )) as MemoryRecord[]
    assert.deepEqual(
      records.map((record) => record.id),
      [a.id],
    )
    assert.ok(!records.some((record) => [b.id, foreign.id].includes(record.id)))
    assert.deepEqual(await h.read(h.turn(), peer), [])
    assert.match(JSON.stringify(await h.read(h.turn(privateScene))), /PRIVATE_SCENE_ALIAS/)
    assert.doesNotMatch(JSON.stringify(await h.read(h.turn(privateScene))), /OTHER_ACCOUNT_ALIAS/)
    assert.equal(
      h.engine.memory.adminList().find((record) => record.id === b.id)!.scope.kind,
      'scene',
    )
    assert.deepEqual(h.engine.memory.adminList().find((record) => record.id === b.id)!.scope, {
      kind: 'scene',
      sceneId: privateScene.id,
      personId: peer,
    })
  } finally {
    await h.close()
  }
})

test('trusted observed alias source recall physically clears its label and invalidates delivered identity state across restart', async () => {
  const h = fixture()
  try {
    h.seed()
    h.link()
    const observed = h.observed()
    h.alias('LAWFUL_PRIVATE_ALIAS', person, privateScene)
    const context = h.turn(),
      values = await h.read(context)
    assert.match(JSON.stringify(values), /OBSERVED_ALIAS_CANARY/)
    assert.ok(context.dependencies.has(observed.id))
    assert.ok(context.validations.every((valid) => valid()))
    h.engine.runtime.recall(scene.id, observed.id)
    assert.ok(context.validations.some((valid) => !valid()))
    for (let read = 0; read < 2; read++) {
      const raw = h.store.db.prepare('SELECT value FROM memory_identity_registry').get() as {
        value: string
      }
      assert.doesNotMatch(raw.value, /OBSERVED_ALIAS_CANARY/)
      assert.match(raw.value, /LAWFUL_PRIVATE_ALIAS/)
      assert.doesNotMatch(JSON.stringify(await h.read(h.turn())), /OBSERVED_ALIAS_CANARY/)
      assert.equal(
        h.engine.memory.adminList().length,
        3,
        'alias recall is not an unrelated assertion deletion',
      )
      if (!read) await h.reopen()
    }
  } finally {
    await h.close()
  }
})

test('alias validity is exact-scene and time-qualified; expiry revokes a delivered page without deleting retained alias history', async (t) => {
  clock(t)
  const h = fixture()
  try {
    h.seed()
    const expires = new Date(Date.now() + 1000).toISOString()
    h.alias('EXPIRING_ALIAS', person, scene, { validUntil: expires })
    h.alias('FUTURE_ALIAS', person, scene, { validFrom: new Date(Date.now() + 2000).toISOString() })
    const context = h.turn(),
      values = await h.read(context)
    assert.equal(values[0]!.page.items[0]!.aliases[0]!.validUntil, expires)
    assert.doesNotMatch(JSON.stringify(values), /FUTURE_ALIAS/)
    t.mock.timers.tick(1000)
    assert.ok(context.validations.some((valid) => !valid()))
    assert.deepEqual((await h.read(h.turn()))[0]!.page.items[0]!.aliases, [])
    assert.equal(h.engine.memory.identities.adminList(true).aliases.length, 2)
  } finally {
    await h.close()
  }
})

for (const mutation of ['unlink', 'split-cycle', 'ABA', 'recall-alias'] as const)
  test(`actual Planner identity delivery followed by ${mutation} prevents old-state Replyer delivery`, async (t) => {
    clock(t)
    const h = fixture()
    let rounds = 0,
      delivered = false
    try {
      h.seed()
      h.link()
      if (mutation === 'split-cycle') {
        h.link(endpoint(peer), endpoint(third))
        h.link(endpoint(third), endpoint(person))
      }
      const observed = h.observed()
      await h.ask(t, (messages) => {
        if (++rounds === 1) return completion([call('memory.identities', { personId: person })])
        const read = receipts(messages)[0]!
        assert.match(JSON.stringify(read.value), /OBSERVED_ALIAS_CANARY/)
        assert.ok(read.value[0].page.items.length >= 2)
        delivered = true
        if (mutation === 'split-cycle') h.split()
        else if (mutation === 'recall-alias') h.engine.runtime.recall(scene.id, observed.id)
        else {
          h.unlink()
          if (mutation === 'ABA') h.link()
        }
        return plan(messages, { stateEvidenceIds: [read.id] })
      })
      assert.equal(delivered, true)
      assert.equal(h.replies.length, 0)
      assert.equal(h.requests.length, 3)
      if (mutation === 'split-cycle') {
        assert.deepEqual(
          (await h.read(h.turn()))[0]!.page.items.map((item) => item.personId),
          [person],
        )
        assert.deepEqual(
          (await h.read(h.turn(), peer))[0]!.page.items.map((item) => item.personId),
          [peer, third],
        )
      }
    } finally {
      await h.close()
    }
  })

test('actual Planner and Replyer retain a complete qualified identity page while ordinary person evidence remains exact', async (t) => {
  clock(t)
  const h = fixture()
  let rounds = 0
  try {
    const [a] = h.seed()
    h.link()
    h.alias('QUALIFIED_ALIAS', person, scene, { validUntil: '2026-10-30T09:00:00Z' })
    const before = h.truth()
    await h.ask(t, (messages) => {
      if (++rounds === 1)
        return completion([
          call('memory.identities', { personId: person }),
          call('memory.person', { personId: person }),
        ])
      const identity = receipts(messages).find((read) => read.toolName === 'memory.identities')!,
        ordinary = receipts(messages).find((read) => read.toolName === 'memory.person')!
      assert.equal(identity.value[0].page.items.length, 2)
      assert.equal(ordinary.value.length, 1)
      assert.equal(ordinary.value[0].content, a!.content)
      return plan(messages, {
        stateEvidenceIds: [identity.id],
        memorySelections: [{ evidenceId: ordinary.id, memoryIds: [ordinary.value[0].id] }],
      })
    })
    assert.equal(h.replies.length, 1)
    const data = payload(h.replies[0]!),
      wire = h.replies[0]![1]!.content
    const view = data.evidence.flatMap((read: any) => read.states ?? [])[0] as IdentityView
    assert.equal(view.stateKind, 'identity_view')
    assert.equal(view.page.items.length, 2)
    assert.equal(
      view.page.interpretation,
      'administrator_verified_links_not_read_authority_or_personal_facts',
    )
    assert.equal(view.page.items[0]!.aliases[0]!.sourceKind, 'manual')
    assert.equal(view.page.items[0]!.aliases[0]!.validUntil, '2026-10-30T09:00:00Z')
    assert.match(wire, /QUALIFIED_ALIAS/)
    assert.doesNotMatch(wire, /FACT_canonical-b_|administratorId|qualificationHash/)
    assert.deepEqual(h.truth(), before)
  } finally {
    await h.close()
  }
})

for (const stage of ['planner', 'replyer'] as const)
  test(`actual ${stage} budget omission removes the whole identity page and dependent conclusions instead of implying no aliases exist`, async (t) => {
    clock(t)
    const h = fixture()
    let rounds = 0
    try {
      h.seed()
      h.link()
      for (let n = 0; n < 60; n++) h.alias(`OMITTED_ALIAS_${n}_${'限定'.repeat(85)}`)
      h.store.set('settings', {
        ...h.store.settings,
        replySuggestionsEnabled: true,
        ...(stage === 'planner'
          ? { plannerEvidenceMaxBytes: 32768 }
          : { replyEvidenceMaxBytes: 4096 }),
      })
      await h.ask(t, (messages) => {
        if (++rounds === 1) return completion([call('memory.identities', { personId: person })])
        const read = receipts(messages)[0]!
        if (stage === 'planner') {
          assert.deepEqual(read.value, [])
          assert.equal(read.coverage?.status, 'limited')
          assert.equal(read.coverage?.omittedRecords, 1)
          h.unlink() // Omitted state never grants an identity validator capable of cancelling this turn.
        } else assert.equal(read.value[0].page.items[0].aliases.length, 60)
        return plan(messages, {
          ...(stage === 'replyer'
            ? {
                stateEvidenceIds: [read.id],
                facts: [{ text: 'OMITTED_IDENTITY_CONCLUSION', evidenceIds: [read.id] }],
              }
            : {}),
          replySuggestion: 'OMITTED_IDENTITY_DRAFT: there are no aliases.',
          expressionGoals: ['OMITTED_IDENTITY_GOAL'],
          constraints: ['Withheld evidence is not evidence of absence.'],
        })
      })
      assert.equal(h.replies.length, 1)
      const data = payload(h.replies[0]!),
        wire = h.replies[0]![1]!.content
      assert.equal(data.memoryEvidenceCoverage.status, 'omitted')
      assert.deepEqual(data.evidence, [])
      assert.deepEqual(data.plan.facts, [])
      assert.doesNotMatch(wire, /OMITTED_ALIAS|OMITTED_IDENTITY/)
      assert.match(wire, /Withheld evidence/)
    } finally {
      await h.close()
    }
  })

test('copied, omitted and simulation identity receipts never acquire typed authority; exact delivered state forbids free-form learning', async () => {
  const h = fixture()
  try {
    h.seed()
    h.link()
    const observed = h.observed()
    h.store.saveScene({ ...h.store.scene(scene.id)!, learningEnabled: true })
    const preparedContext = { ...h.turn(), deferMemoryExposure: true },
      prepared = await h.read(preparedContext)
    assert.equal(prepared.length, 1)
    const foreignContext = { ...h.turn(privateScene), deferMemoryExposure: true }
    assert.deepEqual(h.extension.delivered!('memory.identities', prepared, foreignContext), [])
    assert.equal(foreignContext.dependencies.size, 0)
    const beforeValidators = preparedContext.validations.length
    assert.deepEqual(
      h.extension.delivered!('memory.identities', structuredClone(prepared), preparedContext),
      [],
    )
    assert.deepEqual(h.extension.delivered!('memory.identities', [], preparedContext), [])
    assert.equal(preparedContext.validations.length, beforeValidators)
    assert.equal(preparedContext.dependencies.size, 0)
    const candidate = {
      kind: 'claim',
      content: 'Independent current source declaration.',
      sourceMessageIds: [observed.id],
    }
    const queued = (await h.extension.execute('learning.propose', candidate, preparedContext)) as {
      status: string
    }
    assert.equal(queued.status, 'queued')
    await h.engine.contextualLearning.queue.whenIdle()
    const saved = h.engine.memory.adminList().find((record) => record.content === candidate.content)
    assert.ok(saved)
    const rows = h.store.db
      .prepare('SELECT value FROM memory_supports WHERE memory_id=?')
      .get(saved.id) as { value: string }
    assert.ok(
      (JSON.parse(rows.value) as { parents: string[] }[]).every((proof) => !proof.parents.length),
    )
    const actualContext = h.turn()
    await h.read(actualContext)
    for (const name of ['learning.propose', 'habits.propose', 'experience.propose']) {
      const denied = (await h.extension.execute(name, candidate, actualContext)) as {
        status: string
        reason: string
      }
      assert.deepEqual(denied, {
        status: 'rejected',
        reason: 'identity_read_not_learning_authority',
      })
    }
    h.unlink()
    h.link()
    await h.read(actualContext)
    assert.ok(
      actualContext.validations.some((valid) => !valid()),
      'rereading after ABA cannot replace the original delivered group',
    )
    const fresh = h.turn()
    await h.read(fresh)
    assert.ok(fresh.validations.every((valid) => valid()))
    assert.equal(
      (
        (await h.extension.execute('learning.propose', candidate, actualContext)) as {
          status: string
        }
      ).status,
      'rejected',
    )
    const state = h.engine.memory.identities.adminList(true)
    assert.deepEqual(await h.read(h.turn(scene, true)), [])
    assert.deepEqual(h.engine.memory.identities.adminList(true), state)
  } finally {
    await h.close()
  }
})

test('actual noReply identity-derived proposal is refused but a fresh independent source-only turn can still learn', async (t) => {
  clock(t)
  const h = fixture()
  let rounds = 0,
    proposalResult: unknown
  try {
    h.seed()
    h.link()
    h.store.saveScene({ ...h.store.scene(scene.id)!, learningEnabled: true })
    const execute = h.extension.execute.bind(h.extension)
    h.extension.execute = async (name, input, context) => {
      const result = await execute(name, input, context)
      if (name === 'learning.propose') proposalResult = result
      return result
    }
    await h.ask(t, (messages) => {
      if (++rounds === 1) return completion([call('memory.identities', { personId: person })])
      assert.equal(receipts(messages)[0]!.value.length, 1)
      return plan(messages, {
        action: 'noReply',
        actions: [
          {
            toolName: 'learning.propose',
            arguments: {
              kind: 'claim',
              content: 'IDENTITY_DERIVED_CANARY',
              sourceMessageIds: payload(messages).planContract.contextMessageIds,
            },
          },
        ],
      })
    })
    assert.deepEqual(proposalResult, {
      status: 'rejected',
      reason: 'identity_read_not_learning_authority',
    })
    assert.equal(h.engine.contextualLearning.queue.stats().accepted, 0)
    assert.equal(h.engine.memory.adminList().length, 3)
    assert.equal(h.replies.length, 0)
    h.store.saveScene({ ...h.store.scene(scene.id)!, participationEnabled: false })
    h.engine.runtime.ingest(message('fresh-independent'), scene, false)
    const result = (await h.extension.execute(
      'learning.propose',
      {
        kind: 'claim',
        content: 'FRESH_INDEPENDENT_DECLARATION',
        sourceMessageIds: ['fresh-independent'],
      },
      h.turn(),
    )) as { status: string }
    assert.equal(result.status, 'queued')
    await h.engine.contextualLearning.queue.whenIdle()
    assert.ok(
      h.engine.memory
        .adminList()
        .some((record) => record.content === 'FRESH_INDEPENDENT_DECLARATION'),
    )
    assert.ok(
      !h.engine.memory.adminList().some((record) => record.content === 'IDENTITY_DERIVED_CANARY'),
    )
  } finally {
    await h.close()
  }
})

test('actual Planner cannot select an identity receipt omitted for the byte budget', async (t) => {
  clock(t)
  const h = fixture()
  let rounds = 0,
    omitted = false
  try {
    h.seed()
    h.link()
    for (let n = 0; n < 60; n++) h.alias(`CANNOT_SELECT_OMITTED_${n}_${'限定'.repeat(82)}`)
    h.store.set('settings', { ...h.store.settings, plannerEvidenceMaxBytes: 32768 })
    await h.ask(t, (messages) => {
      if (++rounds === 1) return completion([call('memory.identities', { personId: person })])
      const read = receipts(messages)[0]!
      assert.deepEqual(read.value, [])
      assert.equal(read.coverage?.omittedRecords, 1)
      omitted = true
      return plan(messages, { stateEvidenceIds: [read.id] })
    })
    assert.equal(omitted, true)
    assert.equal(h.replies.length, 0)
    assert.equal(h.engine.contextualLearning.queue.stats().accepted, 0)
    assert.match(JSON.stringify(h.store.logs), /planner_unknown_state_evidence/)
  } finally {
    await h.close()
  }
})

test('identity model reads obey scene read and personalization controls while authenticated administration remains available', async () => {
  const h = fixture()
  try {
    h.seed()
    h.link()
    h.alias('ADMIN_ONLY_ALIAS')
    for (const key of ['memoryReadEnabled', 'personalizationEnabled'] as const) {
      h.store.saveScene({ ...h.store.scene(scene.id)!, [key]: false })
      await assert.rejects(
        h.read(h.turn()),
        key === 'memoryReadEnabled' ? /memory_read_disabled/ : /personalization_disabled/,
      )
      const response = await h.request()
      assert.equal(response.status, 200)
      assert.match(JSON.stringify(await response.json()), /ADMIN_ONLY_ALIAS/)
      h.store.saveScene({ ...h.store.scene(scene.id)!, [key]: true })
      assert.match(JSON.stringify(await h.read(h.turn())), /ADMIN_ONLY_ALIAS/)
    }
    assert.equal(h.requests.length, 0)
  } finally {
    await h.close()
  }
})

test('actual legacy unversioned alias cannot borrow a later first enrollment of the same source ID', async () => {
  const h = fixture()
  try {
    h.seed()
    h.store.set('settings', { ...h.store.settings, sourceEventRevisionsEnabled: false })
    h.engine.settingsChanged()
    const oldSource = h.observed('UNVERSIONED_OLD_ALIAS', 'reused-source')
    assert.equal(oldSource.sourceEventRef, undefined)
    assert.match(JSON.stringify(await h.read(h.turn())), /UNVERSIONED_OLD_ALIAS/)
    h.store.set('settings', { ...h.store.settings, sourceEventRevisionsEnabled: true })
    await h.reopen()
    h.engine.runtime.ingest(
      {
        ...message(oldSource.id),
        parts: [
          {
            type: 'text',
            text: 'Different new observation; not an edit of the old retained source.',
          },
        ],
      },
      scene,
      false,
    )
    assert.ok(h.store.sourceEvents.registered(scene.id, oldSource.id))
    assert.doesNotMatch(JSON.stringify(await h.read(h.turn())), /UNVERSIONED_OLD_ALIAS/)
    const before = h.engine.memory.identities.witness()
    assert.throws(
      () =>
        h.store.db.transaction(() =>
          h.engine.memory.identities.observeAlias(
            {
              ...scope,
              sourceEvidence: [
                {
                  sceneId: scene.id,
                  messageId: oldSource.id,
                  senderId: person,
                  available: true,
                  isBot: false,
                },
              ],
            },
            {
              expectedRevision: before.revision,
              operationId: 'cannot-rebind-old-observation',
              adminConfirmed: true,
            },
            {
              id: 'cannot-rebind-old-alias',
              endpoint: endpoint(person),
              scopeSceneId: scene.id,
              label: 'STALE_OBSERVATION_REBIND_CANARY',
              validFrom: null,
              validUntil: null,
              source: {
                kind: 'observation',
                sceneId: scene.id,
                messageId: oldSource.id,
                senderId: person,
              },
            },
          ),
        )(),
      /identity_observation_untrusted/,
    )
    assert.deepEqual(h.engine.memory.identities.witness(), before)
    assert.doesNotMatch(
      JSON.stringify(h.engine.memory.identities.adminList(true)),
      /STALE_OBSERVATION_REBIND_CANARY/,
    )
    assert.equal(h.requests.length, 0)
  } finally {
    await h.close()
  }
})
