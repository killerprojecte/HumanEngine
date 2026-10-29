import assert from 'node:assert/strict'
import test, { type TestContext } from 'node:test'
import { setImmediate as immediate } from 'node:timers/promises'
import { mkdtempSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { Engine } from '../src/engine.ts'
import { Store } from '../src/store.ts'
import { createApp } from '../src/api.ts'
import type { Completion, Router } from '../src/models.ts'
import type { RuntimeMessage } from '../src/runtime.ts'
import type { SceneRef } from '../src/onebot.ts'
import type { SupportProof } from '../src/memory/domain/proofs.ts'
import { fixtureReplyPlan } from './helpers/reply-plan.ts'

// Scripted proposals prove identity/time authorization, not real-model joke or quote recognition.
const scene: SceneRef = {
  id: `ob:s:${Buffer.from(JSON.stringify(['offline', 'bot-account', 'group', 'synthetic-group'])).toString('base64url')}`,
  kind: 'group',
  adapterId: 'offline',
  accountId: 'bot-account',
  platformSceneId: 'synthetic-group',
}
const other: SceneRef = {
  ...scene,
  id: `ob:s:${Buffer.from(JSON.stringify(['offline', 'bot-account', 'private', 'synthetic-private'])).toString('base64url')}`,
  kind: 'private',
  platformSceneId: 'synthetic-private',
}
const alice = 'canonical:account-a:person-1',
  bob = 'canonical:account-a:person-2'
const receiptTime = '2026-10-27T23:59:50.000Z'
const sourceTime = '2026-10-26T16:30:00.000Z'
const token = 'synthetic-subject-admin-token'
type WireMessage = RuntimeMessage & {
  subjectMentions?: Array<{ personId: string; mentionPartIndex: number }>
}
type Candidate = Record<string, unknown>
const complete = (toolCalls: Completion['toolCalls'] = []): Completion => ({
  text: '',
  toolCalls,
  modelId: 'offline-script',
  usage: { inputTokens: 0, outputTokens: 0, totalTokens: 0 },
  assistant: { role: 'assistant', content: '', toolCalls },
})
const silent = (action: 'silent' | 'reply' = 'silent') =>
  complete([
    {
      id: 'decision',
      name: 'participation.decide',
      arguments: { action, reasonCode: 'synthetic_subject_test' },
    },
  ])
const source = (id: string, patch: Partial<RuntimeMessage> = {}): RuntimeMessage => ({
  id,
  sceneId: scene.id,
  senderId: alice,
  senderDisplayName: '同名',
  senderPlatformId: '10001',
  sentAt: sourceTime,
  receivedAt: receiptTime,
  origin: 'live',
  status: 'available',
  parts: [{ type: 'text', text: 'Synthetic durable source statement.' }],
  ...patch,
})
const candidate = (message: WireMessage, patch: Candidate = {}): Candidate => ({
  content: 'Synthetic personal preference.',
  kind: 'claim',
  sourceMessageIds: [message.id],
  subject: { kind: 'self', sourceMessageId: message.id },
  ...patch,
})
const mention = (message: WireMessage) => ({
  kind: 'mentioned',
  personId: message.parts.find((part) => part.type === 'mention')!.userId,
  sourceMessageId: message.id,
  mentionPartIndex: message.parts.findIndex((part) => part.type === 'mention'),
})
function clock(t: TestContext) {
  t.mock.timers.enable({
    apis: ['Date', 'setTimeout', 'setInterval'],
    now: new Date('2026-10-28T00:00:00.000Z'),
  })
}
async function advance(t: TestContext, ms = 100) {
  t.mock.timers.tick(ms)
  for (let index = 0; index < 6; index++) {
    await immediate()
    t.mock.timers.tick(0)
  }
}
function fixture(participation = false, quiet = 100) {
  const directory = mkdtempSync(join(tmpdir(), 'he-subject-integration-'))
  const path = join(directory, 'synthetic.sqlite')
  let store: Store, engine: Engine, app: ReturnType<typeof createApp>
  let extracts = 0,
    decisions = 0,
    sends = 0
  let extract = async (_messages: WireMessage[], _signal?: AbortSignal): Promise<Candidate[]> => []
  let override: Router['complete'] | undefined
  const open = () => {
    store = new Store(path)
    store.set('settings', {
      ...store.settings,
      debounceMs: 10,
      maxWaitMs: 100,
      learningBatchMs: quiet,
      learningMaxWaitMs: Math.max(quiet, 300),
      learningMinIntervalMs: 0,
    })
    for (const item of [scene, other])
      if (!store.scene(item.id))
        store.saveScene({
          ...item,
          name: 'Synthetic subject scene',
          participationEnabled: participation,
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
        complete: async (...args: Parameters<Router['complete']>) => {
          if (override) return override(...args)
          const [role, prompts, tools, signal] = args
          if (tools?.some((tool) => tool.name === 'participation.decide')) {
            decisions++
            return silent()
          }
          assert.equal(role, 'learning')
          if (tools?.some((tool) => tool.name === 'learning.propose')) {
            extracts++
            const messages = JSON.parse(prompts[1]!.content).messages as WireMessage[]
            const proposals = await extract(messages, signal)
            return complete(
              proposals.map((arguments_, index) => ({
                id: `candidate-${index}`,
                name: 'learning.propose',
                arguments: arguments_,
              })),
            )
          }
          return complete()
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
    extract(
      fn: (messages: WireMessage[], signal?: AbortSignal) => Candidate[] | Promise<Candidate[]>,
    ) {
      extract = async (...args) => fn(...args)
    },
    router(fn: Router['complete']) {
      override = fn
    },
    ingest(message: RuntimeMessage, target = scene) {
      engine.runtime.ingest(message, target)
    },
    stats: () => ({ extracts, decisions, sends }),
    proofs(id: string): SupportProof[] {
      return JSON.parse(
        (
          store.db.prepare('SELECT value FROM memory_supports WHERE memory_id=?').get(id) as {
            value: string
          }
        ).value,
      )
    },
    async profile(personId: string, sceneId = scene.id) {
      const response = await app.request(
        `/api/memory-people/profile?${new URLSearchParams({ sceneId, personId })}`,
        { headers: { Authorization: `Bearer ${token}` } },
      )
      assert.equal(response.status, 200, await response.clone().text())
      return response.json()
    },
    async people(sceneId = scene.id) {
      const response = await app.request(`/api/memory-people?${new URLSearchParams({ sceneId })}`, {
        headers: { Authorization: `Bearer ${token}` },
      })
      assert.equal(response.status, 200)
      return response.json()
    },
    async reopen() {
      await engine.dispose()
      store.close()
      override = undefined
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

test('silent actual ingress creates a self profile with trusted source clocks, unknown occurrence and persistent witness/API output', async (t) => {
  clock(t)
  const h = fixture(true)
  try {
    h.extract((messages) => [candidate(messages[0]!)])
    h.ingest(source('self-source'))
    await advance(t, 10)
    assert.equal(h.stats().decisions, 1)
    await advance(t, 90)
    assert.equal(h.stats().extracts, 1)
    const profile = await h.profile(alice)
    assert.equal(profile.displayName, '同名')
    assert.equal(profile.records.length, 1)
    const record = profile.records[0]
    assert.equal(record.scope.personId, alice)
    assert.equal(record.observedAt, receiptTime)
    assert.equal(record.occurredAt, undefined)
    assert.equal(record.validFrom, undefined)
    assert.notEqual(record.learnedAt, record.observedAt)
    assert.equal(profile.timeline[0].recordedAt, record.learnedAt)
    assert.equal(profile.timeline[0].observedAt, receiptTime)
    assert.equal(profile.timeline[0].occurredAt, undefined)
    assert.deepEqual(record.subjectEvidence.witnesses[0], {
      kind: 'self',
      personId: alice,
      subjectDisplayName: '同名',
      sourceMessageId: 'self-source',
      sources: [
        { sceneId: scene.id, messageId: 'self-source', senderId: alice, senderDisplayName: '同名' },
      ],
      observations: [
        {
          sceneId: scene.id,
          messageId: 'self-source',
          senderId: alice,
          sentAt: sourceTime,
          receivedAt: receiptTime,
        },
      ],
    })
    assert.equal(h.proofs(record.id)[0]!.subjectWitness?.kind, 'self')
    await h.reopen()
    assert.deepEqual(await h.profile(alice), profile)
    await advance(t, 1000)
    assert.equal(h.stats().extracts, 1, 'restart does not replay old ingress')
  } finally {
    await h.close()
  }
})

test('mentioned testimony keeps original speaker, defaults to reported claim, and same-name canonical people remain separate', async (t) => {
  clock(t)
  const h = fixture()
  try {
    h.extract((messages) => {
      const observed = mention(messages[0]!)
      assert.deepEqual(messages[0]!.subjectMentions, [
        { personId: observed.personId, mentionPartIndex: 1 },
      ])
      return [
        candidate(messages[0]!, {
          content: 'Synthetic third-party tea report.',
          kind: 'fact',
          epistemicKind: 'explicit',
          subject: observed,
        }),
      ]
    })
    h.ingest(
      source('report', {
        parts: [
          { type: 'text', text: 'Synthetic third-party report about ' },
          { type: 'mention', userId: bob },
        ],
      }),
    )
    await advance(t)
    const report = (await h.profile(bob)).records[0]
    assert.ok(report)
    assert.equal(report.kind, 'claim')
    assert.equal(report.epistemicKind, 'reported')
    assert.equal(report.sources[0].senderId, alice)
    assert.equal(report.subjectEvidence.witnesses[0].mentionPartIndex, 1)
    assert.equal((await h.profile(alice)).records.length, 0)
    h.extract((messages) => [
      candidate(messages[0]!, { content: 'Synthetic independent self preference.' }),
    ])
    h.ingest(source('alice-self'))
    await advance(t)
    h.ingest(source('bob-self', { senderId: bob, senderPlatformId: '10002' }))
    await advance(t)
    const people = (await h.people()).people
    assert.equal(people.length, 2)
    assert.ok(people.every((person: { displayName: string }) => person.displayName === '同名'))
    assert.deepEqual(
      new Set(people.map((person: { personId: string }) => person.personId)),
      new Set([alice, bob]),
    )
    assert.equal((await h.profile('同名')).records.length, 0)
    assert.equal((await h.profile(alice, other.id)).records.length, 0)
    assert.equal(
      (await h.profile(bob)).records.find((record: { id: string }) => record.id === report.id)
        .sources[0].senderId,
      alice,
    )
  } finally {
    await h.close()
  }
})

test('scripted unresolved nickname, quotation and joke candidates stay qualified scene claims without speaker fallback', async (t) => {
  clock(t)
  const h = fixture()
  try {
    h.extract((messages) =>
      messages.map((message, index) =>
        candidate(message, {
          content: `Qualified synthetic unresolved scenario ${index}; not an established personal fact.`,
          subject: { kind: 'unresolved' },
          ...(index ? { epistemicKind: 'hypothesis' } : {}),
          kind: 'fact',
        }),
      ),
    )
    for (const id of ['bare-nickname', 'quotation', 'joke']) h.ingest(source(id))
    await advance(t)
    assert.equal(h.engine.memory.adminList().length, 3)
    assert.ok(
      h.engine.memory
        .adminList()
        .every(
          (record) =>
            record.scope.kind === 'scene' &&
            record.scope.personId === undefined &&
            record.kind === 'claim',
        ),
    )
    assert.deepEqual((await h.people()).people, [])
    assert.equal((await h.profile(alice)).records.length, 0)
  } finally {
    await h.close()
  }
})

for (const mode of [
  'wrong-index',
  'wrong-person',
  'uncited-source',
  'bot',
  'all',
  'legacy-third-party',
  'cross-scene',
] as const)
  test(`actual source-only extraction rejects ${mode} attribution without a person record`, async (t) => {
    clock(t)
    const h = fixture()
    try {
      h.extract((messages) => {
        const first = messages.find((message) => message.id === 'local')!
        const witnessed = mention(first)
        const patch: Candidate =
          mode === 'legacy-third-party'
            ? { subject: undefined, personId: witnessed.personId }
            : {
                subject: {
                  ...witnessed,
                  ...(mode === 'wrong-index' ? { mentionPartIndex: 0 } : {}),
                  ...(mode === 'wrong-person' ? { personId: first.senderId } : {}),
                  ...(mode === 'uncited-source'
                    ? { sourceMessageId: messages.find((message) => message.id === 'uncited')!.id }
                    : {}),
                  ...(mode === 'cross-scene' ? { sourceMessageId: 'foreign-only-source' } : {}),
                },
              }
        return [candidate(first, patch)]
      })
      h.ingest(
        source('local', {
          parts: [
            { type: 'text', text: 'Synthetic mention witness.' },
            { type: 'mention', userId: mode === 'bot' ? 'bot' : mode === 'all' ? 'all' : bob },
          ],
        }),
      )
      if (mode === 'uncited-source') h.ingest(source('uncited'))
      if (mode === 'cross-scene') {
        h.store.saveScene({ ...h.store.scene(other.id)!, learningEnabled: false })
        h.ingest(source('foreign-only-source', { sceneId: other.id }), other)
      }
      await advance(t)
      assert.equal(h.stats().extracts, 1)
      assert.equal(h.engine.memory.adminList().length, 0)
      assert.deepEqual((await h.people()).people, [])
    } finally {
      await h.close()
    }
  })

test('independent self proofs preserve clocks across duplicate ingress/restart and recall recomputes earliest trusted observation', async (t) => {
  clock(t)
  const h = fixture()
  try {
    h.extract((messages) => [candidate(messages[0]!, { content: 'Exact repeated self claim.' })])
    const first = source('first-proof')
    h.ingest(first)
    await advance(t)
    const firstRecord = (await h.profile(alice)).records[0]
    h.ingest(first)
    await advance(t)
    assert.equal(h.stats().extracts, 1)
    const laterTime = '2026-10-28T00:00:00.150Z'
    h.ingest(source('second-proof', { receivedAt: laterTime, sentAt: '2026-10-27T23:58:00.000Z' }))
    await advance(t)
    let profile = await h.profile(alice)
    assert.equal(profile.records.length, 1)
    assert.equal(profile.records[0].learnedAt, firstRecord.learnedAt)
    assert.equal(profile.records[0].observedAt, receiptTime)
    assert.equal(h.proofs(firstRecord.id).length, 2)
    await h.reopen()
    assert.equal((await h.profile(alice)).records[0].subjectEvidence.witnesses.length, 2)
    h.engine.runtime.recall(scene.id, 'first-proof')
    profile = await h.profile(alice)
    assert.equal(profile.records[0].observedAt, laterTime)
    assert.equal(profile.records[0].occurredAt, undefined)
    assert.doesNotMatch(JSON.stringify(profile), /first-proof/)
    const revokedProof = h
      .proofs(firstRecord.id)
      .find((proof) => proof.sources.some((item) => item.messageId === 'first-proof'))
    assert.equal(revokedProof?.subjectWitness, undefined)
    assert.equal(revokedProof?.sourceObservations, undefined)
    await h.reopen()
    assert.equal((await h.profile(alice)).records[0].observedAt, laterTime)
    h.engine.runtime.recall(scene.id, 'second-proof')
    assert.deepEqual((await h.people()).people, [])
  } finally {
    await h.close()
  }
})

for (const mode of ['mentioned', 'legacy-self', 'legacy-third-party'] as const)
  test(`actual contextual Planner learning uses the same ${mode} subject boundary and preserves exact parent references`, async (t) => {
    clock(t)
    const h = fixture(true, 300000)
    try {
      const parent = h.engine.memory.create({
        scope: { kind: 'scene', sceneId: scene.id },
        kind: 'claim',
        content: 'Synthetic contextual subject premise.',
      })
      const reference = h.engine.memory.captureParentReference(
        { sceneId: scene.id, simulation: false },
        parent,
      )!
      let rounds = 0
      h.router(async (_role, prompts, tools) => {
        if (tools?.some((tool) => tool.name === 'participation.decide')) return silent('reply')
        if (tools?.some((tool) => tool.name === 'reply.plan')) {
          if (++rounds === 1)
            return complete([
              { id: 'read-parent', name: 'memory.search', arguments: { query: parent.content } },
            ])
          const input = JSON.parse(prompts.find((message) => message.role === 'user')!.content)
          const message = input.evidence.find((receipt: { value: { parts?: unknown[] } }) =>
            Array.isArray(receipt.value.parts),
          ).value as WireMessage
          const witness = mention(message)
          const proposal = candidate(message, {
            content: 'Synthetic contextual attributed child.',
            ...(mode === 'mentioned'
              ? { subject: witness }
              : {
                  subject: undefined,
                  personId: mode === 'legacy-self' ? message.senderId : witness.personId,
                }),
          })
          return complete([
            {
              id: 'no-reply-plan',
              name: 'reply.plan',
              arguments: fixtureReplyPlan(prompts, {
                action: 'noReply',
                actions: [{ toolName: 'learning.propose', arguments: proposal }],
              })!,
            },
          ])
        }
        return complete()
      })
      h.ingest(
        source('contextual-source', {
          parts: [
            { type: 'text', text: 'Synthetic source.' },
            { type: 'mention', userId: bob },
          ],
        }),
      )
      await advance(t, 10)
      await advance(t, 100)
      await h.engine.contextualLearning.queue.whenIdle()
      const child = h.engine.memory
        .adminList()
        .find((record) => record.content === 'Synthetic contextual attributed child.')
      if (mode === 'legacy-third-party') assert.equal(child, undefined)
      else {
        assert.ok(child)
        const proof = h.proofs(child.id)[0]!
        assert.deepEqual(proof.parentReferences, [reference])
        assert.equal(
          proof.subjectWitness?.kind,
          mode === 'mentioned' ? 'mentioned' : 'legacy_sender',
        )
        assert.equal(proof.sources[0]!.senderId, alice)
        assert.equal((await h.profile(mode === 'mentioned' ? bob : alice)).records[0].id, child.id)
        if (mode === 'mentioned') assert.equal(child.epistemicKind, 'reported')
      }
      assert.equal(h.stats().sends, 0)
    } finally {
      await h.close()
    }
  })

for (const cancel of ['off', 'recall'] as const)
  test(`in-flight subject extraction ${cancel} cancels before attribution commits and never resumes old work after restart`, async (t) => {
    clock(t)
    const h = fixture()
    let release!: () => void
    const pending = new Promise<void>((resolve) => {
      release = resolve
    })
    let signal: AbortSignal | undefined
    try {
      h.extract(async (messages, active) => {
        signal = active
        await pending
        return [candidate(messages[0]!)]
      })
      h.ingest(source('cancel-source'))
      await advance(t)
      assert.ok(signal)
      if (cancel === 'off') {
        const disabled = { ...h.store.scene(scene.id)!, learningEnabled: false }
        h.store.saveScene(disabled)
        h.engine.sceneChanged(disabled)
      } else h.engine.runtime.recall(scene.id, 'cancel-source')
      assert.equal(signal.aborted, true)
      release()
      await advance(t, 0)
      assert.equal(h.engine.memory.adminList().length, 0)
      await h.reopen()
      const enabled = { ...h.store.scene(scene.id)!, learningEnabled: true }
      h.store.saveScene(enabled)
      h.engine.sceneChanged(enabled)
      await advance(t, 10000)
      assert.equal(h.stats().extracts, 1)
      assert.deepEqual((await h.people()).people, [])
    } finally {
      release()
      await h.close()
    }
  })

test('manual legacy person records stay readable while OFF and bot/simulation ingress never creates automatic profiles', async (t) => {
  clock(t)
  const h = fixture()
  try {
    const disabled = { ...h.store.scene(scene.id)!, learningEnabled: false }
    h.store.saveScene(disabled)
    h.engine.sceneChanged(disabled)
    const manual = h.engine.memory.create({
      scope: { kind: 'scene', sceneId: scene.id, personId: alice },
      kind: 'fact',
      content: 'Legacy manual authorized fact.',
    })
    h.extract((messages) => [candidate(messages[0]!)])
    h.ingest(source('off-source'))
    h.ingest(source('bot-source', { senderId: 'bot' }))
    const enabled = { ...disabled, learningEnabled: true }
    h.store.saveScene(enabled)
    h.engine.sceneChanged(enabled)
    h.ingest(source('simulation-source', { origin: 'simulation' }))
    await advance(t, 1000)
    assert.equal(h.stats().extracts, 0)
    const record = (await h.profile(alice)).records[0]
    assert.equal(record.id, manual.id)
    assert.equal(record.observedAt, undefined)
    assert.equal(record.occurredAt, undefined)
    assert.deepEqual(record.subjectEvidence.witnesses, [])
    assert.deepEqual(
      h.engine.memory.personProfile({ sceneId: scene.id, simulation: true }, alice).records,
      [],
    )
  } finally {
    await h.close()
  }
})

test('explicit unresolved subject cannot promote a private source into shared public technical knowledge', async (t) => {
  clock(t)
  const h = fixture()
  try {
    h.extract((messages) => [
      candidate(messages[0]!, {
        content: 'JSON synthetic unresolved private source claim.',
        kind: 'fact',
        subject: { kind: 'unresolved' },
        publicTechnicalDomain: 'json',
      }),
    ])
    h.ingest(source('private-public-request'))
    await advance(t)
    assert.equal(h.stats().extracts, 1)
    assert.deepEqual(h.engine.memory.adminList(), [])
    assert.deepEqual(h.engine.memory.list({ sceneId: other.id, simulation: false }), [])
    assert.deepEqual((await h.people()).people, [])
  } finally {
    await h.close()
  }
})

test('a later third-party exact-content report cannot replace or strengthen a self assertion', async (t) => {
  clock(t)
  const h = fixture()
  try {
    h.extract((messages) => [
      candidate(messages[0]!, { content: 'Synthetic immutable self statement.' }),
    ])
    h.ingest(source('original-self', { senderId: bob, senderPlatformId: '10002' }))
    await advance(t)
    const before = (await h.profile(bob)).records[0]
    h.extract((messages) => [
      candidate(messages[0]!, {
        content: before.content,
        subject: mention(messages[0]!),
        kind: 'fact',
        epistemicKind: 'explicit',
      }),
    ])
    h.ingest(
      source('later-report', {
        parts: [
          { type: 'text', text: 'Synthetic report.' },
          { type: 'mention', userId: bob },
        ],
      }),
    )
    await advance(t)
    const after = (await h.profile(bob)).records
    assert.equal(after.length, 1)
    assert.equal(after[0].id, before.id)
    assert.equal(after[0].status, before.status)
    assert.deepEqual(after[0].sources, before.sources)
    assert.equal(h.proofs(before.id).length, 1)
    assert.equal(h.proofs(before.id)[0]!.subjectWitness?.kind, 'self')
  } finally {
    await h.close()
  }
})

test('automatic self profiles for the same canonical person in private and group scenes remain isolated', async (t) => {
  clock(t)
  const h = fixture()
  try {
    h.extract((messages) => [
      candidate(messages[0]!, { content: `Scoped statement from ${messages[0]!.id}.` }),
    ])
    h.ingest(source('group-statement'))
    h.ingest(source('private-statement', { sceneId: other.id }), other)
    await advance(t)
    const group = await h.profile(alice)
    const privateProfile = await h.profile(alice, other.id)
    assert.equal(group.records.length, 1)
    assert.equal(privateProfile.records.length, 1)
    assert.match(group.records[0].content, /group-statement/)
    assert.match(privateProfile.records[0].content, /private-statement/)
    assert.doesNotMatch(JSON.stringify(group), /private-statement/)
    assert.doesNotMatch(JSON.stringify(privateProfile), /group-statement/)
  } finally {
    await h.close()
  }
})

test('automatic occurrence and validity instants stay distinct from source observation, while model-authored server clocks and ambiguous dates reject', async (t) => {
  clock(t)
  const h = fixture()
  try {
    const occurredAt = '2026-10-25T20:00:00+08:00'
    const validFrom = '2026-10-26T00:00:00Z'
    const validUntil = '2027-01-01T00:00:00Z'
    h.extract((messages) => [
      candidate(messages[0]!, {
        content: 'Synthetic precisely dated source assertion.',
        occurredAt,
        validFrom,
        validUntil,
      }),
    ])
    h.ingest(source('dated-source'))
    await advance(t)
    const profile = await h.profile(alice)
    assert.equal(profile.records[0].occurredAt, occurredAt)
    assert.equal(profile.records[0].observedAt, receiptTime)
    assert.equal(profile.records[0].validFrom, validFrom)
    assert.equal(profile.records[0].validUntil, validUntil)
    assert.equal(profile.timeline[0].occurredAt, occurredAt)
    assert.equal(profile.timeline[0].validFrom, validFrom)
    h.extract((messages) => [
      candidate(messages[0]!, {
        content: 'FORGED_OBSERVATION',
        observedAt: '2000-01-01T00:00:00Z',
      }),
      candidate(messages[0]!, { content: 'FORGED_COMMIT', learnedAt: '2000-01-01T00:00:00Z' }),
      candidate(messages[0]!, { content: 'AMBIGUOUS_DATE', occurredAt: '2026-10-27' }),
    ])
    h.ingest(source('forged-clock-source'))
    await advance(t)
    assert.equal(h.engine.memory.adminList().length, 1)
    assert.doesNotMatch(JSON.stringify(await h.profile(alice)), /FORGED_|AMBIGUOUS_DATE/)
  } finally {
    await h.close()
  }
})
