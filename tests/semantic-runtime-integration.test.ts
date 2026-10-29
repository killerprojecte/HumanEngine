import assert from 'node:assert/strict'
import test, { type TestContext } from 'node:test'
import { mkdtempSync, rmSync } from 'node:fs'
import { join } from 'node:path'
import { tmpdir } from 'node:os'
import { setImmediate as immediate } from 'node:timers/promises'
import { Engine } from '../src/engine.ts'
import { Store } from '../src/store.ts'
import type { SceneRef } from '../src/onebot.ts'
import type { RuntimeExtension, RuntimeMessage, TurnContext } from '../src/runtime.ts'
import type { ChatMessage, Completion, Router } from '../src/models.ts'
import type {
  MemoryCandidate,
  MemoryContext,
  MemoryRecord,
} from '../src/memory/domain/contracts.ts'
import type { SemanticProposal } from '../src/memory/semantic/contracts.ts'
import { trustedSourceEvidence } from '../src/memory/learning/source-evidence.ts'
import { currentBudgetPriority } from '../src/budget/native-attempt.ts'
import { markRestoredModelHold } from '../src/memory/recovery/model-hold.ts'
import { modelScheduler } from '../src/scheduler.ts'
import { fixtureReplyPlan } from './helpers/reply-plan.ts'

// Synthetic model contracts establish authorization, scheduling, provenance and wire preservation.
// They are not semantic entailment, summary quality, corroboration or measured user benefit.
const scene: SceneRef = {
  id: 'semantic-group',
  kind: 'group',
  adapterId: 'offline',
  accountId: 'account',
  platformSceneId: 'synthetic-group',
}
const other: SceneRef = {
  ...scene,
  id: 'semantic-private',
  kind: 'private',
  platformSceneId: 'private',
}
const person = 'canonical-person',
  peer = 'canonical-peer'
const scope = { sceneId: scene.id, simulation: false }
const call = (name: string, args: Record<string, unknown> = {}) => ({
  id: name,
  name,
  arguments: args,
})
const completion = (toolCalls: Completion['toolCalls'] = []): Completion => ({
  text: '',
  toolCalls,
  modelId: 'offline-semantic',
  usage: { inputTokens: 0, outputTokens: 0, totalTokens: 0 },
  assistant: { role: 'assistant', content: '', toolCalls },
})
const payload = (messages: ChatMessage[]) =>
  JSON.parse(messages.find((m) => m.role === 'user')!.content)
const receipts = (
  messages: ChatMessage[],
): Array<{
  id: string
  toolName: string
  value: any[]
  coverage?: { status: string; omittedRecords: number }
}> => messages.filter((m) => m.role === 'tool').map((m) => JSON.parse(m.content))
const plan = (messages: ChatMessage[], patch: Parameters<typeof fixtureReplyPlan>[1] = {}) =>
  completion([call('reply.plan', fixtureReplyPlan(messages, patch)!)])
function clock(t: TestContext) {
  t.mock.timers.enable({
    apis: ['Date', 'setTimeout', 'setInterval'],
    now: new Date('2026-10-29T11:00:00Z'),
  })
}
async function settle(t?: TestContext) {
  for (let i = 0; i < 16; i++) {
    await immediate()
    t?.mock.timers.tick(0)
  }
}
async function advance(t: TestContext, ms = 30000) {
  await settle(t)
  t.mock.timers.tick(ms)
  await settle(t)
}
const summary = (messages: ChatMessage[], patch: Partial<SemanticProposal> = {}) =>
  completion([
    call('memory.semantic_propose', {
      kind: 'summary_candidate',
      selectedParentIds: payload(messages)
        .declarations.slice(0, 2)
        .map((d: any) => d.ref.id),
      text: 'Attributed declarations only; original qualifications remain attached.',
      hypothesis: false,
      ...patch,
    }),
  ])
function fixture() {
  const directory = mkdtempSync(join(tmpdir(), 'he-semantic-runtime-')),
    path = join(directory, 'synthetic.sqlite')
  let store: Store,
    engine: Engine,
    sequence = 0,
    sends = 0
  const requests: Array<{ role: string; messages: ChatMessage[] }> = [],
    semanticRequests: ChatMessage[][] = [],
    replies: ChatMessage[][] = [],
    errors: unknown[] = []
  let generate: (
    messages: ChatMessage[],
    signal?: AbortSignal,
  ) => Completion | Promise<Completion> = (messages) => summary(messages)
  let extraction: ((messages: ChatMessage[]) => Completion | Promise<Completion>) | undefined
  let planner: (messages: ChatMessage[]) => Completion | Promise<Completion> = (messages) =>
    plan(messages)
  const open = () => {
    store = new Store(path)
    if (!store.scene(scene.id)) {
      store.set('settings', {
        ...store.settings,
        sourceEventRevisionsEnabled: true,
        debounceMs: 10,
        maxWaitMs: 100,
        learningBatchMs: 300000,
        learningMaxWaitMs: 300000,
        learningMinIntervalMs: 0,
      })
      for (const s of [scene, other])
        store.saveScene({
          ...s,
          name: 'Synthetic semantic scene',
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
        throw Error('Unexpected platform send')
      },
      getMessage: async () => ({ items: [], coverage: 'complete' }),
      history: async () => ({ items: [], coverage: 'complete' }),
      dispose() {},
    })
    engine.router = (_config, admit) =>
      ({
        complete: async (role, messages, tools = [], signal) => {
          admit?.()
          requests.push({ role, messages: structuredClone(messages) })
          try {
            let result: Completion
            if (tools.some((tool) => tool.name === 'memory.semantic_propose')) {
              assert.equal(role, 'learning')
              assert.equal(currentBudgetPriority(0), 2)
              semanticRequests.push(structuredClone(messages))
              result = await generate(messages, signal)
            } else if (
              role === 'learning' &&
              tools.some((tool) => tool.name === 'learning.propose') &&
              extraction
            )
              result = await extraction(messages)
            else if (tools.some((tool) => tool.name === 'participation.decide'))
              result = completion([
                call('participation.decide', { action: 'reply', reasonCode: 'synthetic_semantic' }),
              ])
            else if (tools.some((tool) => tool.name === 'reply.plan'))
              result = await planner(messages)
            else {
              assert.equal(
                role,
                'replyer',
                'no raw-source extraction is scheduled by these fixtures',
              )
              replies.push(structuredClone(messages))
              result = completion()
            }
            admit?.()
            return result
          } catch (error) {
            if (error instanceof assert.AssertionError) errors.push(error)
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
  const source = (
    id = `source-${++sequence}`,
    senderId = person,
    target = scene,
  ): RuntimeMessage => {
    engine.runtime.ingest(
      {
        id,
        sceneId: target.id,
        senderId,
        origin: 'live',
        status: 'available',
        sentAt: new Date().toISOString(),
        receivedAt: new Date().toISOString(),
        parts: [{ type: 'text', text: `Synthetic declaration ${id}.` }],
      },
      target,
      false,
    )
    return structuredClone(
      engine.runtime.preview(target.id).find((m) => m.id === id)! as RuntimeMessage,
    )
  }
  const write = (
    message: RuntimeMessage,
    content: string,
    patch: Partial<MemoryCandidate> = {},
    ctx: Partial<MemoryContext> = {},
  ): MemoryRecord => {
    const result = engine.memory.propose(
      {
        sceneId: message.sceneId,
        simulation: false,
        provenance: 'source-only',
        sourceEvidence: trustedSourceEvidence([message], () => false),
        ...ctx,
      },
      {
        content,
        kind: 'fact',
        epistemicKind: 'explicit',
        subject: { kind: 'self', sourceMessageId: message.id },
        sources: [
          {
            sceneId: message.sceneId,
            messageId: message.id,
            senderId: message.senderId,
            eventRef: message.sourceEventRef,
          },
        ],
        ...patch,
      },
    )
    assert.equal(result.status, 'saved', JSON.stringify(result))
    if (result.status !== 'saved') throw Error('Fixture assertion rejected')
    return result.memory
  }
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
    source,
    write,
    requests,
    semanticRequests,
    replies,
    planner(fn: typeof planner) {
      planner = fn
    },
    extract(fn: NonNullable<typeof extraction>) {
      extraction = fn
    },
    generate(fn: typeof generate) {
      generate = fn
    },
    flags(patch: Record<string, unknown> = {}) {
      store.set('settings', {
        ...store.settings,
        memorySemanticEnabled: true,
        memorySemanticAutoAdmissionEnabled: true,
        memorySemanticAnswerEnabled: true,
        ...patch,
      })
      engine.settingsChanged()
    },
    add(content: string, id?: string, senderId = person, patch: Partial<MemoryCandidate> = {}) {
      const message = source(id, senderId)
      return { source: message, memory: write(message, content, patch) }
    },
    pair() {
      return [this.add('First explicit declaration.'), this.add('Second explicit declaration.')]
    },
    rows() {
      return engine.memory.semantic.storage.rows(scene.id)
    },
    views() {
      return engine.memory.semantic.list(scope).items
    },
    sql() {
      return store.db
        .prepare("SELECT name FROM sqlite_master WHERE name='memory_semantic_candidates'")
        .get()
        ? JSON.stringify(store.db.prepare('SELECT * FROM memory_semantic_candidates').all())
        : '[]'
    },
    truth() {
      return [
        'memories',
        'memory_supports',
        'memory_assertion_revisions',
        'memory_assertion_state',
      ].map((table) => [table, store.db.prepare(`SELECT * FROM ${table} ORDER BY 1`).all()])
    },
    turn(): TurnContext {
      return {
        scene,
        mode: 'real',
        turnId: `direct-${++sequence}`,
        signal: new AbortController().signal,
        dependencies: new Set(),
        validations: [],
        sourceSnapshots: structuredClone(engine.runtime.preview(scene.id)),
      }
    },
    read(ctx: TurnContext) {
      return extension().execute('memory.summaries', {}, ctx) as Promise<any[]>
    },
    async ask(t: TestContext, fn: typeof planner) {
      planner = fn
      store.saveScene({ ...store.scene(scene.id)!, participationEnabled: true })
      const message = source(`query-${++sequence}`)
      engine.runtime.ingest({ ...message, id: `ask-${++sequence}` }, scene)
      await advance(t, 10)
      assert.equal(engine.runtime.activeTasks, 0)
      if (errors.length) throw errors.at(-1)
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
      if (errors.length) throw errors.at(-1)
    },
  }
}

test('default OFF and OFF→ON never scan old assertions; new ordinary commits automatically coalesce after quiet into one attributed summary without changing originals', async (t) => {
  clock(t)
  const h = fixture()
  try {
    assert.equal(h.store.settings.memorySemanticEnabled, false)
    assert.equal(h.store.settings.memorySemanticAutoAdmissionEnabled, false)
    assert.equal(h.store.settings.memorySemanticAnswerEnabled, false)
    h.pair()
    await advance(t)
    assert.equal(h.semanticRequests.length, 0)
    h.flags()
    await advance(t)
    assert.equal(h.semanticRequests.length, 0)
    h.add('Third explicit source triggers the complete partition.')
    const truth = h.truth()
    await advance(t, 29999)
    assert.equal(h.semanticRequests.length, 0)
    await advance(t, 1001) // The real coordinator polls quiet readiness at a one-second bound.
    assert.equal(h.engine.semanticLearning.stats().pending, 0)
    await h.engine.semanticLearning.whenIdle()
    assert.equal(h.semanticRequests.length, 1)
    assert.equal(payload(h.semanticRequests[0]!).declarations.length, 3)
    const view = h.views()[0]!
    assert.ok(view)
    assert.equal(view.candidate.status, 'admitted')
    assert.equal(view.interpretation, 'attributed_summary_not_verified_truth')
    assert.equal(view.candidate.parents.length, 3)
    assert.equal(view.candidate.selectedParentIds.length, 2)
    assert.deepEqual(h.truth(), truth)
    assert.equal(h.engine.memory.list(scope).length, 3)
    await advance(t, 120000)
    assert.equal(h.semanticRequests.length, 1)
    await h.reopen()
    await advance(t, 120000)
    assert.equal(h.semanticRequests.length, 1)
    assert.equal(h.views().length, 1)
  } finally {
    await h.close()
  }
})

test('rollback notifications, singleton roots, disabled scene and restart pending metadata never trigger a semantic model replay', async (t) => {
  clock(t)
  const h = fixture()
  try {
    h.flags()
    assert.throws(
      () =>
        h.store.db.transaction(() => {
          h.pair()
          throw Error('synthetic rollback')
        })(),
      /synthetic rollback/,
    )
    await advance(t)
    assert.equal(h.semanticRequests.length, 0)
    assert.equal(h.engine.memory.adminList().length, 0)
    const source = h.source('single-root')
    h.write(source, 'First declaration from one source.')
    h.write(source, 'Second declaration from that same source.')
    await advance(t)
    assert.equal(h.semanticRequests.length, 0)
    h.add('Independent new root.')
    await settle(t)
    assert.ok(h.engine.semanticLearning.stats().pending > 0)
    await h.reopen()
    await advance(t, 120000)
    assert.equal(h.semanticRequests.length, 0)
    h.store.saveScene({ ...h.store.scene(scene.id)!, learningEnabled: false })
    h.engine.settingsChanged()
    assert.equal(h.engine.semanticLearning.stats().pending, 0)
  } finally {
    await h.close()
  }
})

test('unselected third visible parent remains an AND dependency and its source recall physically clears generated text while independent scene survives', async (t) => {
  clock(t)
  const h = fixture()
  try {
    h.flags()
    const parents = [...h.pair(), h.add('UNSELECTED_PRIVATE_PARENT')]
    h.generate((messages) => {
      const data = payload(messages),
        selected = data.declarations
          .filter((d: any) => d.record.content !== 'UNSELECTED_PRIVATE_PARENT')
          .map((d: any) => d.ref.id)
      return summary(messages, {
        selectedParentIds: selected,
        text: 'COPIED_UNSELECTED_SEMANTIC_CANARY',
      })
    })
    await advance(t)
    const row = h.rows()[0]!
    assert.equal(row.parents.length, 3)
    assert.ok(!row.selectedParentIds.includes(parents[2]!.memory.id))
    h.generate((messages) => summary(messages, { text: 'Independent other-scene summary.' }))
    const otherSources = [
      h.source('private-a', person, other),
      h.source('private-b', person, other),
    ]
    for (const [n, source] of otherSources.entries())
      h.write(source, `Other scene independent declaration ${n}`)
    await advance(t)
    assert.equal(h.engine.memory.semantic.storage.rows(other.id).length, 1)
    h.flags({ memorySemanticEnabled: false })
    h.engine.runtime.recall(scene.id, parents[2]!.source.id)
    assert.doesNotMatch(h.sql(), /COPIED_UNSELECTED_SEMANTIC_CANARY/)
    assert.equal(h.rows().length, 0)
    assert.equal(h.engine.memory.semantic.storage.rows(other.id).length, 1)
    await h.reopen()
    assert.equal(h.rows().length, 0)
  } finally {
    await h.close()
  }
})

test('recall preserves original independent OR support with a new epoch but removes a summary when any required parent loses its final proof', async (t) => {
  clock(t)
  const h = fixture()
  try {
    h.flags()
    const a = h.add('Same OR declaration.', 'or-a'),
      b = h.source('or-b')
    const second = h.write(b, a.memory.content)
    assert.equal(second.id, a.memory.id)
    const c = h.add('Other required AND declaration.', 'and-c')
    await advance(t)
    const row = h.rows()[0]!,
      old = row.parents.find((p) => p.ref.id === a.memory.id)!
    assert.equal(old.proofs.length, 2)
    h.flags({ memorySemanticEnabled: false })
    h.engine.runtime.recall(scene.id, a.source.id)
    h.flags()
    const kept = h.views()[0]!
    assert.ok(kept)
    const current = kept.candidate.parents.find((p) => p.ref.id === a.memory.id)!
    assert.ok(current.ref.deletionEpoch > old.ref.deletionEpoch)
    assert.deepEqual(
      current.proofs.flatMap((p) => p.sources.map((s) => s.messageId)),
      [b.id],
    )
    h.engine.runtime.recall(scene.id, c.source.id)
    assert.equal(h.rows().length, 0)
    assert.ok(h.engine.memory.list(scope).some((record) => record.id === a.memory.id))
    await h.reopen()
    assert.equal(h.rows().length, 0)
  } finally {
    await h.close()
  }
})

for (const change of [
  'new-member',
  'same-body-revision',
  'source-edit',
  'read-aba',
  'flags-aba',
] as const)
  test(`semantic model awaiting ${change} cannot commit against its original full first-read snapshot`, async (t) => {
    clock(t)
    const h = fixture()
    let release: () => void = () => {}
    const wait = new Promise<void>((resolve) => {
      release = resolve
    })
    try {
      h.flags()
      const [a] = h.pair()
      h.generate(async (messages) => {
        await wait
        return summary(messages, { text: 'STALE_SEMANTIC_MUST_NOT_COMMIT' })
      })
      await advance(t)
      assert.equal(h.semanticRequests.length, 1)
      assert.equal(h.engine.semanticLearning.stats().running, 1)
      if (change === 'new-member') h.add('New C expands the subject partition.')
      if (change === 'same-body-revision') h.engine.memory.update(a!.memory.id, { locked: true })
      if (change === 'source-edit')
        await h.engine.runtime.edit(
          { ...a!.source, parts: [{ type: 'text', text: 'Changed original' }] },
          scene,
          { editId: 'edit-a', order: { kind: 'platform_sequence', sequence: 1 } },
        )
      if (change === 'read-aba') {
        h.store.saveScene({ ...h.store.scene(scene.id)!, memoryReadEnabled: false })
        h.store.saveScene({ ...h.store.scene(scene.id)!, memoryReadEnabled: true })
      }
      if (change === 'flags-aba') {
        h.flags({ memorySemanticAnswerEnabled: false })
        h.flags()
      }
      release()
      await settle(t)
      assert.equal(h.rows().length, 0)
      assert.doesNotMatch(h.sql(), /STALE_SEMANTIC/)
      assert.equal(h.engine.semanticLearning.stats().abandoned, 1)
      h.flags({ memorySemanticEnabled: false })
      await advance(t, 120000)
      assert.equal(h.semanticRequests.length, 1)
    } finally {
      release()
      await settle(t)
      await h.close()
    }
  })

test('new same-person membership immediately hides an old summary without privacy-deleting retained history or mutating original truths', async (t) => {
  clock(t)
  const h = fixture()
  try {
    h.flags()
    h.pair()
    await advance(t)
    const row = h.rows()[0]!,
      before = h.sql()
    h.add('C changes the complete relevant membership.')
    assert.equal(h.engine.memory.semantic.current(scope, row.id), undefined)
    assert.equal(h.sql(), before)
    h.flags({ memorySemanticEnabled: false })
    await advance(t, 120000)
    assert.equal(h.semanticRequests.length, 1)
  } finally {
    await h.close()
  }
})

test('a weak hypothesis expires to review-needed after seven days, never false and never an expiry-triggered generation', async (t) => {
  clock(t)
  const h = fixture()
  try {
    h.flags()
    h.generate((messages) => summary(messages, { hypothesis: true }))
    h.pair()
    await advance(t)
    const row = h.rows()[0]!,
      before = h.truth()
    assert.equal(row.status, 'candidate')
    assert.ok(row.applicableUntil)
    h.store.saveScene({ ...h.store.scene(scene.id)!, learningEnabled: false })
    h.engine.settingsChanged()
    await advance(t, 7 * 86400000)
    const view = h.engine.memory.semantic.current(scope, row.id)!
    assert.ok(view)
    assert.equal(view.applicability, 'review_needed')
    assert.equal(view.candidate.status, 'candidate')
    assert.equal(view.candidate.hypothesis, true)
    assert.deepEqual(h.truth(), before)
    assert.equal(h.semanticRequests.length, 1)
  } finally {
    await h.close()
  }
})

test('actual silent ingress to source extractor to ordinary assertions automatically reaches exactly one semantic maintenance request', async (t) => {
  clock(t)
  const h = fixture()
  let extracts = 0
  try {
    h.flags()
    h.store.set('settings', { ...h.store.settings, learningBatchMs: 100, learningMaxWaitMs: 300 })
    h.engine.settingsChanged()
    h.extract((messages) => {
      extracts++
      return completion(
        payload(messages).messages.map((message: any, index: number) =>
          call('learning.propose', {
            content: `Independent raw extraction declaration ${index}.`,
            kind: 'fact',
            epistemicKind: 'explicit',
            subject: { kind: 'self', sourceMessageId: message.id },
            sourceMessageIds: [message.id],
          }),
        ),
      )
    })
    h.source('raw-a')
    h.source('raw-b')
    await advance(t, 100)
    assert.equal(extracts, 1)
    assert.equal(h.engine.memory.list(scope).length, 2)
    assert.equal(h.semanticRequests.length, 0)
    await advance(t)
    assert.equal(h.semanticRequests.length, 1)
    assert.equal(h.views()[0]!.candidate.status, 'admitted')
    assert.equal(
      h.requests.length,
      2,
      'one source extraction and one separate semantic attempt, no extra unaccounted graph call',
    )
    assert.equal(h.replies.length, 0)
    let rounds = 0
    await h.ask(t, (messages) => {
      if (++rounds === 1) return completion([call('memory.summaries')])
      const read = receipts(messages)[0]!
      assert.equal(read.value[0].page.items[0].declarations.length, 2)
      return plan(messages, { stateEvidenceIds: [read.id] })
    })
    assert.equal(h.replies.length, 1)
    assert.match(h.replies[0]![1]!.content, /Independent raw extraction declaration/)
    assert.equal(extracts, 1)
    assert.equal(h.semanticRequests.length, 1)
  } finally {
    await h.close()
  }
})

test('failed or empty semantic attempts persist only nonreplayable receipts and are not retried by idle, disable/enable or restart', async (t) => {
  clock(t)
  const h = fixture()
  try {
    h.flags()
    h.generate(() => {
      throw Error('Synthetic provider failure')
    })
    h.pair()
    await advance(t)
    assert.equal(h.semanticRequests.length, 1)
    assert.equal(h.rows().length, 0)
    assert.equal(h.engine.memory.semantic.storage.attempts()[0]!.status, 'abandoned')
    h.engine.semanticLearning.resume()
    await advance(t, 120000)
    assert.equal(h.semanticRequests.length, 1)
    h.flags({ memorySemanticEnabled: false })
    h.flags()
    await h.reopen()
    await advance(t, 120000)
    assert.equal(h.semanticRequests.length, 1)
    h.generate(() => completion())
    h.add('Fresh changed input permits one new attempt.')
    await advance(t)
    assert.equal(h.semanticRequests.length, 2)
    assert.equal(h.rows().length, 0)
    assert.equal(
      h.engine.memory.semantic.storage.attempts().filter((attempt) => attempt.status === 'finished')
        .length,
      1,
    )
    await advance(t, 60000)
    assert.equal(h.semanticRequests.length, 2)
  } finally {
    await h.close()
  }
})

test('actual Planner and Replyer receive admitted summary with complete original long negation, conditions and epistemic declarations as one qualified state page', async (t) => {
  clock(t)
  const h = fixture()
  let rounds = 0
  const tail = '仅本次；尚未确认；绝不意味着共同出席。'
  try {
    h.flags()
    h.add(`${'限定说明'.repeat(600)}${tail}`)
    h.add('Second declaration applies only on weekdays, never Sunday.')
    await advance(t)
    await h.ask(t, (messages) => {
      if (++rounds === 1) return completion([call('memory.summaries')])
      const read = receipts(messages)[0]!
      assert.equal(read.value[0].page.items.length, 1)
      return plan(messages, {
        stateEvidenceIds: [read.id],
        constraints: ['An attributed summary is not verified reality.'],
      })
    })
    assert.equal(h.replies.length, 1)
    const wire = h.replies[0]![1]!.content,
      data = JSON.parse(wire),
      page = data.evidence.find((item: any) => item.toolName === 'memory.summaries').states[0].page,
      item = page.items[0]
    assert.equal(page.sourceBodyCoverage, 'unavailable')
    assert.equal(item.interpretation, 'attributed_summary_not_verified_truth')
    assert.equal(item.declarations.length, 2)
    assert.equal(item.parents.length, 2)
    assert.equal(item.rootCount, 2)
    assert.ok(
      item.selectedParentIds.every(
        (id: string) =>
          item.parents.some((parent: any) => parent.ref.id === id) &&
          item.declarations.some((d: any) => d.ref.id === id),
      ),
    )
    for (const parent of h.rows()[0]!.parents)
      assert.equal(
        wire.includes(parent.ref.id),
        false,
        'raw assertion IDs cannot bypass projected membership',
      )
    assert.ok(item.declarations.some((d: any) => d.record.content.endsWith(tail)))
    assert.ok(
      item.declarations.every(
        (d: any) => d.record.epistemicKind === 'explicit' && d.record.occurredAt === undefined,
      ),
    )
    assert.ok(
      item.parents.every((parent: any) =>
        item.declarations.some(
          (d: any) => d.ref.id === parent.ref.id && d.record.id === parent.ref.id,
        ),
      ),
    )
    assert.doesNotMatch(wire, /"proofId"|"fingerprint"|"dependencyKey"|"partitionKey"|"inputKey"/)
  } finally {
    await h.close()
  }
})

for (const stage of ['planner', 'replyer'] as const)
  test(`actual ${stage} budget omits the entire summary plus originals and dependent facts, not just their limiting tails`, async (t) => {
    clock(t)
    const h = fixture()
    let rounds = 0
    try {
      h.flags()
      const a = h.add(`${'原始限定'.repeat(1300)}ONLY_THIS_TIME_NOT_CONFIRMED_A`)
      h.add(`${'另一限定'.repeat(1300)}NEVER_JOINT_ATTENDANCE_B`)
      await advance(t)
      h.store.set('settings', {
        ...h.store.settings,
        replySuggestionsEnabled: true,
        ...(stage === 'planner'
          ? { plannerEvidenceMaxBytes: 32768 }
          : { replyEvidenceMaxBytes: 4096 }),
      })
      await h.ask(t, (messages) => {
        if (++rounds === 1) return completion([call('memory.summaries')])
        const read = receipts(messages)[0]!
        if (stage === 'planner') {
          assert.deepEqual(read.value, [])
          assert.equal(read.coverage?.omittedRecords, 1)
          h.engine.memory.update(a.memory.id, { locked: true })
        } else assert.equal(read.value.length, 1)
        return plan(messages, {
          ...(stage === 'replyer'
            ? {
                stateEvidenceIds: [read.id],
                facts: [{ text: 'OMITTED_SUMMARY_CONCLUSION', evidenceIds: [read.id] }],
              }
            : {}),
          replySuggestion: 'OMITTED_SUMMARY_DRAFT',
          expressionGoals: ['OMITTED_SUMMARY_GOAL'],
          constraints: ['Omitted summaries are not evidence of absence.'],
        })
      })
      assert.equal(h.replies.length, 1)
      const data = payload(h.replies[0]!),
        wire = h.replies[0]![1]!.content
      assert.equal(data.memoryEvidenceCoverage.status, 'omitted')
      assert.deepEqual(data.evidence, [])
      assert.deepEqual(data.plan.facts, [])
      assert.doesNotMatch(wire, /OMITTED_SUMMARY|ONLY_THIS_TIME|NEVER_JOINT/)
      assert.match(wire, /Omitted summaries are not evidence of absence/)
    } finally {
      await h.close()
    }
  })

test('only actual full summary delivery forbids laundering learned text; copied/omitted receipt grants no authority and a fresh source-only turn remains lawful', async (t) => {
  clock(t)
  const h = fixture()
  try {
    h.flags()
    h.pair()
    await advance(t)
    const source = h.source('fresh-source'),
      candidate = {
        kind: 'claim',
        content: 'Fresh source-only assertion.',
        sourceMessageIds: [source.id],
      }
    const omitted = { ...h.turn(), deferMemoryExposure: true },
      prepared = await h.read(omitted),
      before = omitted.validations.length
    assert.equal(prepared.length, 1)
    assert.deepEqual(
      h.extension.delivered!('memory.summaries', structuredClone(prepared), omitted),
      [],
    )
    assert.deepEqual(h.extension.delivered!('memory.summaries', [], omitted), [])
    assert.equal(omitted.validations.length, before)
    assert.deepEqual(await h.read({ ...h.turn(), mode: 'simulation' }), [])
    const actual = h.turn()
    assert.equal((await h.read(actual)).length, 1)
    for (const tool of ['learning.propose', 'habits.propose', 'experience.propose'])
      assert.deepEqual(await h.extension.execute(tool, candidate, actual), {
        status: 'rejected',
        reason: 'semantic_read_not_learning_authority',
      })
    const result = (await h.extension.execute('learning.propose', candidate, omitted)) as {
      status: string
    }
    assert.equal(result.status, 'queued')
    await h.engine.contextualLearning.queue.whenIdle()
    assert.ok(h.engine.memory.adminList().some((memory) => memory.content === candidate.content))
  } finally {
    await h.close()
  }
})

function managed(h: ReturnType<typeof fixture>) {
  const controller = new AbortController(),
    calls: ChatMessage[][] = []
  h.engine.protocols.register(
    'fixture:semantic',
    'Synthetic managed semantic provider',
    {
      async complete(_context, messages) {
        assert.equal(currentBudgetPriority(0), 2)
        calls.push(structuredClone(messages))
        const result = summary(messages)
        return {
          text: result.text,
          toolCalls: result.toolCalls,
          usage: { inputTokens: 1, outputTokens: 1, totalTokens: 2 },
        }
      },
    },
    controller.signal,
  )
  h.store.set('models', {
    providers: [
      {
        id: 'synthetic',
        name: 'Offline semantic',
        protocol: 'fixture:semantic',
        baseUrl: 'http://127.0.0.1:1',
        credentialEnv: '',
        retryCount: 0,
        timeoutMs: 2000,
      },
    ],
    models: [
      {
        id: 'synthetic',
        displayName: 'Offline semantic',
        providerId: 'synthetic',
        apiModelName: 'synthetic',
        capabilities: ['text', 'tools'],
      },
    ],
    routes: [{ role: 'learning', orderedModelIds: ['synthetic'], timeoutMs: 3000 }],
  })
  h.engine.router = Engine.prototype.router.bind(h.engine)
  return { calls, stop: () => controller.abort() }
}

for (const gate of ['hold', 'strict-budget', 'read-off', 'personal-off'] as const)
  test(`actual Engine managed semantic Router obeys ${gate} before any provider transport`, async (t) => {
    clock(t)
    const h = fixture(),
      m = managed(h)
    try {
      h.flags()
      if (gate === 'hold') h.store.db.transaction(() => markRestoredModelHold(h.store.db))()
      if (gate === 'strict-budget')
        h.store.set('settings', {
          ...h.store.settings,
          budgetExperimentEnabled: true,
          budgetMode: 'strict',
        })
      if (gate === 'read-off')
        h.store.saveScene({ ...h.store.scene(scene.id)!, memoryReadEnabled: false })
      if (gate === 'personal-off')
        h.store.saveScene({ ...h.store.scene(scene.id)!, personalizationEnabled: false })
      h.pair()
      await advance(t)
      assert.equal(m.calls.length, 0)
      assert.equal(h.rows().length, 0)
      h.engine.semanticLearning.resume()
      await advance(t, 60000)
      assert.equal(m.calls.length, 0)
      if (gate === 'strict-budget') {
        h.store.set('settings', { ...h.store.settings, budgetExperimentEnabled: false })
        await advance(t, 30000)
        assert.equal(m.calls.length, 0, 'unblocking does not replay a rejected attempt')
        h.add('A new original creates a new admission opportunity.')
        await advance(t)
        assert.equal(m.calls.length, 1)
        assert.equal(h.views()[0]!.candidate.status, 'admitted')
      }
    } finally {
      m.stop()
      await h.close()
    }
  })

for (const revoke of ['hold', 'read-aba', 'new-member'] as const)
  test(`actual semantic request already queued in the shared model scheduler is stopped by ${revoke} at physical dispatch`, async (t) => {
    clock(t)
    const h = fixture(),
      m = managed(h)
    let release: () => void = () => {},
      blockers: Promise<void>[] = []
    try {
      h.flags()
      const wait = new Promise<void>((resolve) => {
        release = resolve
      })
      blockers = Array.from({ length: modelScheduler.status.concurrency }, () =>
        modelScheduler.run(0, undefined, () => wait),
      )
      await settle(t)
      h.pair()
      await advance(t)
      assert.equal(modelScheduler.status.queued, 1)
      assert.equal(m.calls.length, 0)
      if (revoke === 'hold') h.store.db.transaction(() => markRestoredModelHold(h.store.db))()
      if (revoke === 'read-aba') {
        h.store.saveScene({ ...h.store.scene(scene.id)!, memoryReadEnabled: false })
        h.store.saveScene({ ...h.store.scene(scene.id)!, memoryReadEnabled: true })
      }
      if (revoke === 'new-member') h.add('New C invalidates the queued original set.')
      release()
      await Promise.all(blockers)
      await settle(t)
      assert.equal(m.calls.length, 0)
      assert.equal(h.rows().length, 0)
      assert.equal(h.engine.semanticLearning.stats().abandoned, 1)
      h.flags({ memorySemanticEnabled: false })
    } finally {
      release()
      await Promise.all(blockers)
      m.stop()
      await h.close()
    }
  })

for (const change of ['new-member', 'flags-aba'] as const)
  test(`actual delivered summary cannot reach Replyer after ${change} revokes its first complete page`, async (t) => {
    clock(t)
    const h = fixture()
    let rounds = 0
    try {
      h.flags()
      h.pair()
      await advance(t)
      await h.ask(t, (messages) => {
        if (++rounds === 1) return completion([call('memory.summaries')])
        const read = receipts(messages)[0]!
        assert.equal(read.value[0].page.items.length, 1)
        if (change === 'new-member') h.add('Third original after summary delivery.')
        else {
          h.flags({ memorySemanticAnswerEnabled: false })
          h.flags()
        }
        return plan(messages, { stateEvidenceIds: [read.id] })
      })
      assert.equal(h.replies.length, 0)
      h.flags({ memorySemanticEnabled: false })
    } finally {
      await h.close()
    }
  })

async function seedEpisode(
  h: ReturnType<typeof fixture>,
  entityKind: 'event' | 'plan' = 'event',
  shared = true,
) {
  h.flags({
    memoryKnowledgeEnabled: true,
    memoryKnowledgeAnswerEnabled: true,
    memoryKnowledgeAutoAdmissionEnabled: true,
  })
  const a = h.source('episode-a'),
    b = h.source('episode-b', peer)
  const relation = (
    source: RuntimeMessage,
    object: Record<string, unknown>,
    qualified = false,
  ) => ({
    registryVersion: 1,
    predicate: 'participates_in',
    subject: { kind: 'self', personId: source.senderId },
    object,
    polarity: qualified ? 'negative' : 'positive',
    modality: qualified ? 'possible' : 'asserted',
    temporalScope: qualified ? 'past' : 'current',
    conditions: qualified ? ['ONLY_IF_WEATHER_PERMITS_NOT_CONFIRMED'] : [],
    sourceSpans: [
      {
        messageId: source.id,
        partIndex: 0,
        start: 0,
        end: (source.parts[0] as { text: string }).text.length,
        stance: 'self',
      },
    ],
  })
  const first = h.write(a, 'A plans participation; not observed attendance.', {
    knowledge: [
      relation(a, { kind: 'new', entityKind, title: 'Same title is not identity' }) as any,
    ],
  })
  const edge = h.engine.memory.knowledge.storage.rows()[0]!
  assert.equal(edge.object.kind, 'entity')
  if (edge.object.kind !== 'entity') throw Error('Missing event')
  const second = h.write(b, 'B submitted a participation declaration; occurrence unknown.', {
    kind: 'claim',
    ...(shared
      ? {}
      : {
          knowledge: [
            relation(
              b,
              { kind: 'new', entityKind, title: 'Same title is not identity' },
              true,
            ) as any,
          ],
        }),
  })
  if (shared) {
    const marker = h.source('episode-b-typed', peer),
      ctx = h.turn()
    await h.extension.execute('memory.person', { personId: peer }, ctx)
    await h.extension.execute(
      'memory.knowledge',
      { anchor: { kind: 'person', personId: person }, predicates: ['participates_in'], depth: 1 },
      ctx,
    )
    const queued = (await h.extension.execute(
      'learning.propose',
      {
        content: 'Untrusted model text must not become an original declaration.',
        kind: 'claim',
        epistemicKind: 'explicit',
        subject: { kind: 'self', sourceMessageId: marker.id },
        sourceMessageIds: [marker.id],
        knowledge: [relation(marker, { kind: 'existing', entityId: edge.object.entity.id }, true)],
      },
      ctx,
    )) as { status: string }
    assert.equal(queued.status, 'queued')
    await h.engine.contextualLearning.queue.whenIdle()
    assert.equal(h.engine.memory.knowledge.storage.rows().length, 2)
    return { a, b, first, second, edge, marker }
  }
  return { a, b, first, second, edge }
}

function assertEpisodeQualifications(value: any, anchorId: string) {
  assert.ok(Array.isArray(value))
  assert.equal(value.length, 2)
  const negative = value.find((item: any) => item.polarity === 'negative')
  assert.ok(negative)
  assert.equal(negative.modality, 'possible')
  assert.equal(negative.temporalScope, 'past')
  assert.deepEqual(negative.conditions, ['ONLY_IF_WEATHER_PERMITS_NOT_CONFIRMED'])
  assert.equal(negative.occurredAt, null)
  assert.equal(negative.validFrom, null)
  assert.equal(negative.validUntil, null)
  assert.equal(negative.status, 'candidate')
  assert.equal(negative.interpretation, 'attributed_proposed_association_not_observed_event')
  const ids = new Set(value.map((item: any) => item.edgeId))
  for (const item of value) {
    assert.equal(item.predicate, 'participates_in')
    assert.equal(item.subject.kind, 'person')
    assert.equal(item.object.kind, 'entity')
    assert.equal(item.object.entityKind, 'event')
    assert.equal(item.object.entityId, anchorId)
    assert.ok(ids.has(item.object.originEdgeId))
    assert.equal(
      item.object.title,
      undefined,
      'compact qualifications must not reintroduce unbounded marker prose',
    )
  }
}

test('actual multi-person episode maintenance requires the same exact event and retains original declarations without inventing attendance or occurrence', async (t) => {
  clock(t)
  const h = fixture()
  let rounds = 0
  try {
    h.generate((messages) => {
      const wire = payload(messages)
      assert.equal(wire.mode, 'episode_only')
      assertEpisodeQualifications(wire.anchorQualifications, wire.episodeAnchor.id)
      assert.doesNotMatch(
        JSON.stringify(wire.declarations),
        /ONLY_IF_WEATHER_PERMITS_NOT_CONFIRMED/,
      )
      assert.equal(wire.declarations.length, 2)
      assert.ok(wire.declarations.every((entry: any) => !entry.record.internalUse))
      assert.ok(
        wire.declarations.some((entry: any) =>
          entry.record.content.includes('B submitted a participation declaration'),
        ),
      )
      return summary(messages, {
        kind: 'episode_candidate',
        episodeAnchor: wire.episodeAnchor,
        text: 'Grouped participant declarations, not a confirmed joint event.',
      })
    })
    const seed = await seedEpisode(h)
    await advance(t)
    assert.equal(h.semanticRequests.length, 1)
    const view = h.views()[0]!
    assert.ok(view)
    assert.equal(view.candidate.kind, 'episode_candidate')
    assert.equal(view.candidate.status, 'candidate')
    assert.equal(view.interpretation, 'grouped_declarations_not_observed_event')
    assert.equal(view.declarations.length, 2)
    assert.deepEqual(
      new Set(view.declarations.map((d) => d.ref.id)),
      new Set([seed.first.id, seed.second.id]),
    )
    assert.ok(view.declarations.every((d) => d.record.occurredAt === undefined))
    await h.ask(t, (messages) => {
      if (++rounds === 1) return completion([call('memory.summaries')])
      const read = receipts(messages)[0]!
      assert.equal(read.value[0].page.items[0].kind, 'episode_candidate')
      return plan(messages, { stateEvidenceIds: [read.id] })
    })
    assert.equal(h.replies.length, 1)
    const wire = h.replies[0]![1]!.content
    const finalItem = JSON.parse(wire).evidence.find(
      (entry: any) => entry.toolName === 'memory.summaries',
    ).states[0].page.items[0]
    assertEpisodeQualifications(finalItem.anchorQualifications, finalItem.episodeAnchor.id)
    assert.doesNotMatch(
      JSON.stringify(finalItem.declarations),
      /ONLY_IF_WEATHER_PERMITS_NOT_CONFIRMED/,
    )
    assert.match(wire, /grouped_declarations_not_observed_event/)
    assert.match(wire, /B submitted a participation declaration/)
    assert.match(wire, /ONLY_IF_WEATHER_PERMITS_NOT_CONFIRMED/)
    assert.ok(
      finalItem.declarations.every(
        (declaration: any) => declaration.record.occurredAt === undefined,
      ),
    )
    h.engine.runtime.recall(scene.id, seed.marker!.id)
    assert.equal(
      h.rows().length,
      0,
      'withdrawn typed event-identity proof must physically purge the grouping despite both ordinary assertions surviving',
    )
    assert.equal(h.engine.memory.list(scope).length, 2)
    assert.doesNotMatch(h.sql(), /Grouped participant declarations, not a confirmed joint event/)
    await h.reopen()
    assert.equal(h.rows().length, 0)
  } finally {
    await h.close()
  }
})

for (const mode of ['equal-title-events', 'shared-plan'] as const)
  test(`actual ${mode} declarations do not establish a multi-person observed event or schedule an episode model call`, async (t) => {
    clock(t)
    const h = fixture()
    try {
      await seedEpisode(h, mode === 'shared-plan' ? 'plan' : 'event', mode === 'shared-plan')
      await advance(t)
      assert.equal(h.semanticRequests.length, 0)
      assert.equal(h.rows().length, 0)
      assert.equal(h.engine.memory.list(scope).length, 2)
    } finally {
      await h.close()
    }
  })

test('an actually active foreground Planner defers semantic maintenance without invoking a model, then committed pending metadata resumes once foreground finishes', async (t) => {
  clock(t)
  const h = fixture()
  let release: () => void = () => {}
  const wait = new Promise<void>((resolve) => {
    release = resolve
  })
  try {
    h.flags()
    h.planner(async (messages) => {
      await wait
      return plan(messages, { action: 'noReply' })
    })
    h.store.saveScene({ ...h.store.scene(scene.id)!, participationEnabled: true })
    const source = h.source('foreground')
    h.engine.runtime.ingest({ ...source, id: 'foreground-live' }, scene)
    await advance(t, 10)
    assert.equal(h.engine.runtime.activeTasks, 1)
    h.pair()
    await advance(t)
    assert.equal(h.semanticRequests.length, 0)
    assert.ok(h.engine.semanticLearning.stats().pending > 0)
    release()
    await settle(t)
    assert.equal(h.engine.runtime.activeTasks, 0)
    await advance(t, 1000)
    assert.equal(h.semanticRequests.length, 1)
    assert.equal(h.views().length, 1)
    assert.equal(h.replies.length, 0)
  } finally {
    release()
    await settle(t)
    await h.close()
  }
})

test('new committed revisions supersede stale pending dirty refs before dispatch without dropping the latest complete subject partition', async (t) => {
  clock(t)
  const h = fixture()
  try {
    h.flags()
    const [a] = h.pair()
    await settle(t)
    assert.equal(h.engine.semanticLearning.stats().pending, 2)
    h.engine.memory.update(a!.memory.id, { locked: true })
    await settle(t)
    const current = h.engine.memory.revisionState(a!.memory.id)
    await advance(t, 31000)
    assert.equal(
      h.semanticRequests.length,
      1,
      'stale pending A1 must not discard the newly authorized A2/B partition',
    )
    const input = payload(h.semanticRequests[0]!)
    assert.equal(input.declarations.length, 2)
    assert.equal(
      input.declarations.find((entry: any) => entry.record.content === a!.memory.content).ref
        .version,
      current.version,
    )
    assert.equal(
      h.views()[0]!.candidate.parents.find((parent) => parent.ref.id === a!.memory.id)!.ref.version,
      current.version,
    )
    assert.equal(h.engine.semanticLearning.stats().pending, 0)
    await advance(t, 60000)
    assert.equal(
      h.semanticRequests.length,
      1,
      'only pending metadata is replaced, not replayed completed attempts',
    )
  } finally {
    await h.close()
  }
})

test('actual semantic canonical UTF8 input exceeding 65536 bytes is rejected whole before the model, without truncation, original mutation or automatic retry', async (t) => {
  clock(t)
  const h = fixture()
  try {
    h.flags()
    const records = Array.from({ length: 4 }, (_, index) =>
      h.add(`${'完整原声明'.repeat(1400)}ONLY_THIS_ORIGINAL_${index}_NOT_CONFIRMED`),
    )
    assert.ok(records.every(({ memory }) => memory.content.length < 20000))
    assert.ok(
      new TextEncoder().encode(JSON.stringify(records.map(({ memory }) => memory))).length > 65536,
    )
    const truth = h.truth()
    await advance(t)
    assert.equal(h.semanticRequests.length, 0)
    assert.equal(h.requests.length, 0)
    assert.equal(h.rows().length, 0)
    assert.equal(h.engine.memory.semantic.storage.attempts()[0]!.status, 'abandoned')
    assert.deepEqual(h.truth(), truth)
    await advance(t, 120000)
    assert.equal(h.requests.length, 0)
    await h.reopen()
    await advance(t, 120000)
    assert.equal(h.requests.length, 0)
    assert.deepEqual(h.truth(), truth)
  } finally {
    await h.close()
  }
})
