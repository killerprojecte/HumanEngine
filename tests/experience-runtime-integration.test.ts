import assert from 'node:assert/strict'
import test, { type TestContext } from 'node:test'
import { setImmediate as immediate } from 'node:timers/promises'
import { mkdtempSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { Engine } from '../src/engine.ts'
import { Store } from '../src/store.ts'
import type { ChatMessage, Completion, Router, ToolDefinition } from '../src/models.ts'
import type { RuntimeExtension, RuntimeMessage, TurnContext } from '../src/runtime.ts'
import type { SceneRef } from '../src/onebot.ts'
import type { Settings } from '../src/schema.ts'
import type { ExperienceOutcome } from '../src/memory/domain/experience.ts'
import { renderExperienceAction } from '../src/memory/experience/policy-actions.ts'
import { fixtureReplyPlan } from './helpers/reply-plan.ts'

// Scripted model contracts and actual runtime boundaries, not policy effectiveness or NLP accuracy.
const scene: SceneRef = {
  id: 'experience-scene',
  kind: 'group',
  adapterId: 'offline',
  accountId: 'bot',
  platformSceneId: 'synthetic',
}
const foreign: SceneRef = { ...scene, id: 'experience-private', kind: 'private' }
const person = 'experience-person'
const scope = { sceneId: scene.id, simulation: false }
const flags: Partial<Settings> = {
  memoryExperienceEnabled: true,
  memoryExperienceAutoActivateEnabled: true,
  memoryExperienceHintsEnabled: true,
}
const call = (name: string, args: Record<string, unknown> = {}, id = name) => ({
  id,
  name,
  arguments: args,
})
const completion = (toolCalls: Completion['toolCalls'] = []): Completion => ({
  text: '',
  toolCalls,
  assistant: { role: 'assistant', content: '', toolCalls },
  modelId: 'scripted-experience',
  usage: { inputTokens: 0, outputTokens: 0, totalTokens: 0 },
})
const plan = (messages: ChatMessage[], patch: Parameters<typeof fixtureReplyPlan>[1] = {}) =>
  completion([call('reply.plan', fixtureReplyPlan(messages, patch)!, 'plan')])
const source = (
  id: string,
  text = 'Synthetic question.',
  patch: Partial<RuntimeMessage> = {},
): RuntimeMessage => ({
  id,
  sceneId: scene.id,
  senderId: person,
  origin: 'live',
  status: 'available',
  sentAt: new Date().toISOString(),
  receivedAt: new Date().toISOString(),
  parts: [{ type: 'text', text }],
  ...patch,
})
function clock(t: TestContext) {
  t.mock.timers.enable({
    apis: ['Date', 'setTimeout', 'setInterval'],
    now: new Date('2026-10-28T00:00:00Z'),
  })
}
async function advance(t: TestContext, ms = 10) {
  t.mock.timers.tick(ms)
  for (let n = 0; n < 12; n++) {
    await immediate()
    t.mock.timers.tick(0)
  }
}
const initial = (messages: ChatMessage[]) =>
  JSON.parse(messages.find((item) => item.role === 'user')!.content)
const receipt = (messages: ChatMessage[]) =>
  JSON.parse(messages.filter((item) => item.role === 'tool').at(-1)!.content) as {
    value: ExperienceOutcome[]
    coverage: { status: string; omittedRecords: number }
  }
const proposal = (outcome: Pick<ExperienceOutcome, 'id' | 'version'>, personId = person) => ({
  action: 'conclusion_before_detail',
  condition: { kind: 'response_style', personId },
  outcomeRefs: [{ id: outcome.id, version: outcome.version }],
})
function fixture(initialFlags: Partial<Settings> = flags) {
  const directory = mkdtempSync(join(tmpdir(), 'he-experience-runtime-')),
    path = join(directory, 'synthetic.sqlite')
  let store: Store,
    engine: Engine,
    sequence = 0,
    transportCalls = 0,
    extractionCalls = 0
  let planner: (
    messages: ChatMessage[],
    tools: ToolDefinition[],
  ) => Completion | Promise<Completion> = (messages) => plan(messages)
  let sendReply = true
  const decisions: ChatMessage[][] = [],
    planners: ChatMessage[][] = [],
    replies: ChatMessage[][] = []
  const outcomeContexts: TurnContext[] = []
  const modelErrors: unknown[] = []
  const actionResults: unknown[] = [],
    actionErrors: string[] = []
  const open = (first = false) => {
    store = new Store(path)
    if (first)
      store.set('settings', {
        ...store.settings,
        debounceMs: 10,
        maxWaitMs: 100,
        learningBatchMs: 300000,
        learningMaxWaitMs: 300000,
        learningMinIntervalMs: 0,
        ...initialFlags,
      })
    for (const s of [scene, foreign])
      if (!store.scene(s.id))
        store.saveScene({
          ...s,
          name: 'Synthetic experience',
          participationEnabled: false,
          learningEnabled: true,
          proactiveEnabled: false,
        })
    engine = new Engine(store)
    engine.runtime.addAdapter('offline', {
      send: async () => ({ status: 'sent', messageId: `offline-bot-${++transportCalls}` }),
      getMessage: async () => ({ items: [], coverage: 'complete' }),
      history: async () => ({ items: [], coverage: 'complete' }),
      dispose() {},
    })
    engine.router = () =>
      ({
        complete: async (role, messages, tools = []) => {
          if (tools.some((tool) => tool.name === 'participation.decide')) {
            decisions.push(structuredClone(messages))
            return completion([
              call('participation.decide', { action: 'reply', reasonCode: 'synthetic_experience' }),
            ])
          }
          if (tools.some((tool) => tool.name === 'reply.plan')) {
            planners.push(structuredClone(messages))
            try {
              return await planner(messages, tools)
            } catch (error) {
              modelErrors.push(error)
              throw error
            }
          }
          if (role === 'replyer') {
            replies.push(structuredClone(messages))
            if (!sendReply || messages.some((message) => message.role === 'tool'))
              return completion()
            return completion([
              call('messages.send', {
                operationId: `offline-operation-${++sequence}`,
                segments: [{ parts: [{ type: 'text', text: 'Synthetic acknowledged response.' }] }],
              }),
            ])
          }
          assert.equal(role, 'learning')
          extractionCalls++
          return completion()
        },
      }) as Router
    const extension = (
      engine.runtime as unknown as { extensions: RuntimeExtension[] }
    ).extensions.find((item) => item.tools.some((tool) => tool.name === 'learning.propose'))!
    const execute = extension.execute.bind(extension)
    extension.execute = async (name, input, context) => {
      try {
        if (name === 'experience.outcomes') outcomeContexts.push(context)
        const value = await execute(name, input, context)
        if (name === 'experience.propose') actionResults.push(value)
        return value
      } catch (error) {
        if (name === 'experience.propose') actionErrors.push(String(error))
        throw error
      }
    }
  }
  open(true)
  return {
    get store() {
      return store
    },
    get engine() {
      return engine
    },
    decisions,
    planners,
    replies,
    actionResults,
    outcomeContexts,
    actionErrors,
    stats: () => ({ transportCalls, extractionCalls }),
    rows: () => engine.experience.adminList(scope).items,
    settings(patch: Partial<Settings>) {
      store.set('settings', { ...store.settings, ...patch })
      engine.settingsChanged()
    },
    learning(value: boolean) {
      store.saveScene({ ...store.scene(scene.id)!, learningEnabled: value })
      engine.settingsChanged()
    },
    ingest(message: RuntimeMessage, target = scene) {
      store.saveScene({ ...store.scene(target.id)!, participationEnabled: false })
      engine.runtime.ingest(message, target)
    },
    feedback(id: string, target: string, text = '请先说结论', patch: Partial<RuntimeMessage> = {}) {
      this.ingest(source(id, text, { replyToId: target, ...patch }))
    },
    extension() {
      return (engine.runtime as unknown as { extensions: RuntimeExtension[] }).extensions.find(
        (item) => item.tools.some((tool) => tool.name === 'learning.propose'),
      )!
    },
    context(patch: Partial<TurnContext> = {}): TurnContext {
      return {
        scene,
        mode: 'real',
        turnId: `direct-${++sequence}`,
        signal: new AbortController().signal,
        dependencies: new Set(),
        validations: [],
        ...patch,
      }
    },
    async ask(
      t: TestContext,
      fn: typeof planner = (messages) => plan(messages),
      message = source(`question-${++sequence}`),
      send = true,
      target = scene,
    ) {
      planner = fn
      sendReply = send
      store.saveScene({ ...store.scene(target.id)!, participationEnabled: true })
      const before = transportCalls,
        beforeErrors = modelErrors.length,
        beforePlans = planners.length
      engine.runtime.ingest(message, target)
      await advance(t)
      if (modelErrors.length > beforeErrors) throw modelErrors.at(-1)
      assert.ok(planners.length > beforePlans, 'actual Planner callback ran')
      assert.equal(engine.runtime.activeTasks, 0, 'actual foreground turn settles')
      return transportCalls > before ? `offline-bot-${transportCalls}` : undefined
    },
    async reopen() {
      await engine.dispose()
      store.close()
      open()
    },
    async close() {
      await engine.dispose()
      store.close()
      rmSync(directory, { recursive: true, force: true })
    },
  }
}
async function seed(t: TestContext, h: ReturnType<typeof fixture>, id = 'preference-source') {
  const target = await h.ask(t)
  assert.ok(target)
  h.feedback(id, target)
  assert.equal(h.rows().length, 1)
  return h.rows()[0]!
}

test('live exact feedback closes the actual send, Planner-only hint and explicit withdrawal loop without claiming ACK effectiveness', async (t) => {
  clock(t)
  const h = fixture()
  try {
    const active = await seed(t, h)
    assert.equal(active.status, 'active')
    assert.equal(active.activationBasis, 'explicit_preference')
    assert.equal(active.validation?.tier, 'mechanical_only')
    assert.equal(active.validation?.passed, true)
    assert.equal(active.sources[0]?.source.messageId, 'preference-source')
    assert.equal(h.stats().extractionCalls, 0, 'mechanical activation needs no extra model')
    const hint = renderExperienceAction('conclusion_before_detail')
    const target = await h.ask(t, (messages) => {
      const contexts = initial(messages).policyContext
      assert.equal(contexts.length, 1)
      assert.equal(contexts[0].hints.length, 1)
      assert.equal(contexts[0].hints[0].hint, hint)
      assert.equal(contexts[0].hints[0].tier, 'mechanical_only')
      return plan(messages)
    })
    assert.ok(target)
    assert.ok(h.decisions.every((messages) => !JSON.stringify(messages).includes(hint)))
    assert.ok(h.replies.every((messages) => !JSON.stringify(messages).includes(hint)))
    assert.ok(h.replies.every((messages) => !JSON.stringify(messages).includes('generationRoots')))
    const uses = h.engine.experience.targetUses(scope, target)
    assert.deepEqual(
      uses.map((use) => use.kind),
      ['exposed'],
    )
    assert.equal(uses[0]?.policyRef.id, active.id)
    assert.equal(uses[0]?.policyRef.version, active.version)
    assert.equal(h.rows()[0]?.uses.applied, 0)
    h.feedback('withdraw-source', target, '不要先说结论')
    assert.equal(h.rows()[0]?.status, 'suspended')
    assert.equal(h.rows()[0]?.reason, 'explicit_instruction_withdrawn')
    assert.equal(
      h.engine.experience
        .listOutcomes(scope)
        .find((outcome) => outcome.source.messageId === 'withdraw-source')?.code,
      'withdraw_instruction',
    )
    await h.ask(t, (messages) => {
      assert.equal(initial(messages).policyContext, undefined)
      return plan(messages, { action: 'noReply' })
    })
    await h.reopen()
    assert.equal(h.rows()[0]?.status, 'suspended')
    assert.equal(h.rows()[0]?.uses.applied, 0)
  } finally {
    await h.close()
  }
})

test('three flags default OFF; enabling automatic activation does not retroactively promote an old candidate', async (t) => {
  clock(t)
  const h = fixture({})
  try {
    for (const key of Object.keys(flags) as Array<keyof typeof flags>)
      assert.equal(h.store.settings[key], false)
    const target = await h.ask(t, (messages, tools) => {
      assert.ok(!tools.some((tool) => tool.name === 'experience.outcomes'))
      return plan(messages)
    })
    h.feedback('disabled-feedback', target!)
    assert.equal(h.rows().length, 0)
    h.settings({ memoryExperienceEnabled: true })
    h.feedback('candidate-feedback', target!)
    assert.equal(h.rows()[0]?.status, 'candidate')
    h.settings({ memoryExperienceAutoActivateEnabled: true, memoryExperienceHintsEnabled: true })
    await h.ask(t, (messages) => {
      assert.equal(initial(messages).policyContext, undefined)
      return plan(messages, { action: 'noReply' })
    })
    assert.equal(h.rows()[0]?.status, 'candidate')
    const candidate = h.rows()[0]!
    const validated = h.engine.experience.validate(scope, candidate.id, candidate.version)
    assert.throws(
      () => h.engine.experience.review(scope, candidate.id, candidate.version, 'active'),
      /version_conflict/,
    )
    h.engine.experience.review(scope, candidate.id, validated.version, 'active')
    h.settings({ memoryExperienceHintsEnabled: false })
    await h.ask(t, (messages) => {
      assert.equal(initial(messages).policyContext, undefined)
      return plan(messages, { action: 'noReply' })
    })
    h.settings({ memoryExperienceHintsEnabled: true })
    await h.reopen()
    await h.ask(t, (messages) => {
      assert.equal(initial(messages).policyContext[0].hints.length, 1)
      return plan(messages, { action: 'noReply' })
    })
  } finally {
    await h.close()
  }
})

test('learning OFF reads existing scoped hints but neither admits new feedback nor exposes learning outcomes', async (t) => {
  clock(t)
  const h = fixture()
  try {
    const active = await seed(t, h)
    h.learning(false)
    const target = await h.ask(t, (messages) => {
      assert.equal(initial(messages).policyContext[0].hints.length, 1)
      return plan(messages)
    })
    h.feedback('off-new-feedback', target!)
    assert.equal(h.rows().length, 1)
    assert.equal(h.rows()[0]?.version, active.version)
    assert.deepEqual(h.engine.experience.listOutcomes(scope), [])
    h.learning(true)
    assert.equal(h.engine.experience.listOutcomes(scope).length, 1, 'OFF message is never replayed')
    await h.ask(
      t,
      (messages) => {
        assert.equal(initial(messages).policyContext, undefined)
        return plan(messages, { action: 'noReply' })
      },
      source('other-person-query', 'Hello', { senderId: 'different-person' }),
    )
    await h.ask(
      t,
      (messages) => {
        assert.equal(initial(messages).policyContext, undefined)
        return plan(messages, { action: 'noReply' })
      },
      source('private-query', 'Hello', { sceneId: foreign.id }),
      false,
      foreign,
    )
  } finally {
    await h.close()
  }
})

const invalidFeedback: Array<[string, string, Partial<RuntimeMessage>]> = [
  ['missing target', '请先说结论', { replyToId: 'nonexistent' }],
  ['history', '请先说结论', { origin: 'history' }],
  ['bot', '请先说结论', { senderId: 'bot' }],
  ['simulation', '请先说结论', { origin: 'simulation' }],
  ['quote', '“请先说结论”', {}],
  ['thanks', '谢谢，先说结论很好', {}],
  ['ambiguous', '也许先说结论比较好？', {}],
]
for (const [name, text, patch] of invalidFeedback)
  test(`actual ingress ${name} feedback does not create or reinforce a policy`, async (t) => {
    clock(t)
    const h = fixture()
    try {
      const target = await h.ask(t)
      h.feedback(`invalid-${name}`, target!, text, patch)
      assert.equal(h.rows().length, 0)
      assert.equal(h.engine.experience.listOutcomes(scope).length, 0)
    } finally {
      await h.close()
    }
  })

test('foreign targets and another person cannot withdraw a scoped exposed policy', async (t) => {
  clock(t)
  const h = fixture()
  try {
    const active = await seed(t, h),
      target = await h.ask(t)
    h.feedback('wrong-person-withdraw', target!, '不要先说结论', { senderId: 'different-person' })
    assert.equal(h.rows()[0]?.version, active.version)
    h.ingest(
      source('foreign-request', '请先说结论', { sceneId: foreign.id, replyToId: target }),
      foreign,
    )
    assert.equal(
      h.engine.experience.list({ sceneId: foreign.id, simulation: false }).items.length,
      0,
    )
    assert.equal(h.rows()[0]?.status, 'active')
  } finally {
    await h.close()
  }
})

test('actual Planner outcome read permits only a candidate action on noReply and never sends or promotes it', async (t) => {
  clock(t)
  const h = fixture({ ...flags, memoryExperienceAutoActivateEnabled: false })
  try {
    const candidate = await seed(t, h),
      before = h.stats().transportCalls,
      beforeReplies = h.replies.length
    let round = 0
    await h.ask(t, (messages) => {
      if (++round === 1) return completion([call('experience.outcomes')])
      const result = receipt(messages)
      assert.equal(result.coverage.status, 'complete')
      assert.equal(result.value.length, 1)
      return plan(messages, {
        action: 'noReply',
        actions: [
          {
            toolName: 'experience.propose',
            arguments: proposal(result.value[0]!, result.value[0]!.source.senderId),
          },
        ],
      })
    })
    assert.equal(round, 2)
    assert.equal(h.stats().transportCalls, before)
    assert.equal(h.replies.length, beforeReplies)
    assert.equal(h.rows()[0]?.version, candidate.version)
    assert.equal(h.rows()[0]?.status, 'candidate')
    assert.deepEqual(h.actionResults, [
      {
        status: 'candidate',
        candidateId: candidate.id,
        duplicate: true,
        active: false,
        validationTier: 'mechanical_only',
      },
    ])
    assert.deepEqual(h.actionErrors, [])
  } finally {
    await h.close()
  }
})

test('known or forged outcome refs without actual delivery and recalled delivered refs cannot authorize a proposal', async (t) => {
  clock(t)
  const h = fixture({ ...flags, memoryExperienceAutoActivateEnabled: false })
  try {
    await seed(t, h)
    const outcome = h.engine.experience.listOutcomes(scope)[0]!,
      extension = h.extension()
    await assert.rejects(
      () => extension.execute('experience.propose', proposal(outcome), h.context()),
      /not_exposed/,
    )
    const context = h.context({ deferMemoryExposure: true })
    const prepared = (await extension.execute(
      'experience.outcomes',
      {},
      context,
    )) as ExperienceOutcome[]
    assert.deepEqual(
      extension.delivered?.(
        'experience.outcomes',
        prepared.map((item) => structuredClone(item)),
        context,
      ),
      [],
    )
    await assert.rejects(
      () => extension.execute('experience.propose', proposal(outcome), context),
      /not_exposed/,
    )
    assert.equal(
      (extension.delivered?.('experience.outcomes', prepared, context) as ExperienceOutcome[])
        .length,
      1,
    )
    assert.ok(context.dependencies.has('preference-source'))
    h.engine.runtime.recall(scene.id, 'preference-source')
    await assert.rejects(
      () => extension.execute('experience.propose', proposal(outcome), context),
      /context_invalid/,
    )
    for (const table of ['outcomes', 'policies', 'validations', 'uses'])
      assert.equal(
        (
          h.store.db.prepare(`SELECT count(*) AS n FROM memory_experience_${table}`).get() as {
            n: number
          }
        ).n,
        0,
      )
    await h.reopen()
    assert.equal(h.rows().length, 0)
  } finally {
    await h.close()
  }
})

test('whole-receipt Planner budget omission is explicit and grants no outcome authority even with a known exact ref', async (t) => {
  clock(t)
  const h = fixture({ ...flags, memoryExperienceAutoActivateEnabled: false })
  try {
    const target = await h.ask(t)
    for (let n = 0; n < 20; n++) h.feedback(`feedback-${n}-${'s'.repeat(400)}`, target!)
    const outcome = h.engine.experience.listOutcomes(scope)[0]!
    await h.reopen()
    h.settings({ plannerEvidenceMaxBytes: 16384 })
    let round = 0
    await h.ask(t, async (messages) => {
      if (++round === 1) return completion([call('experience.outcomes')])
      const result = receipt(messages)
      assert.deepEqual(result.value, [])
      assert.equal(result.coverage.status, 'limited')
      assert.equal(result.coverage.omittedRecords, 20)
      await assert.rejects(
        () =>
          h.extension().execute('experience.propose', proposal(outcome), h.outcomeContexts.at(-1)!),
        /not_exposed/,
      )
      return plan(messages, { action: 'noReply' })
    })
    assert.equal(round, 2)
    assert.equal(h.actionResults.length, 0)
    assert.ok(h.actionErrors.some((error) => error.includes('not_exposed')))
    assert.equal(h.rows().length, 20)
    assert.ok(h.rows().every((policy) => policy.status === 'candidate'))
  } finally {
    await h.close()
  }
})

for (const order of ['person-first', 'empty-search-first'] as const)
  test(`actual delivered tool order ${order} distinguishes applied from merely exposed`, async (t) => {
    clock(t)
    const h = fixture()
    try {
      const target = await h.ask(t)
      h.feedback('exact-lookup-source', target!, '涉及我的问题先查我的记忆')
      const candidate = h.rows()[0]!
      assert.equal(candidate.action, 'exact_person_before_semantic')
      assert.equal(candidate.status, 'candidate', 'non-style operation never auto-activates')
      const validated = h.engine.experience.validate(scope, candidate.id, candidate.version)
      h.engine.experience.review(scope, candidate.id, validated.version, 'active')
      h.engine.memory.create({
        content: 'Exact scoped profile record.',
        kind: 'fact',
        scope: { kind: 'scene', sceneId: scene.id, personId: person },
      })
      let round = 0
      const response = await h.ask(
        t,
        (messages) => {
          round++
          const personId = initial(messages).policyContext[0].hints[0].condition.personId
          if (round === 1 && order === 'empty-search-first')
            return completion([call('memory.search', { query: 'ZZZ_NO_MATCH_927401' })])
          if (round === 1 || (round === 2 && order === 'empty-search-first')) {
            if (round === 2) assert.deepEqual(receipt(messages).value, [])
            return completion([call('memory.person', { personId })])
          }
          assert.equal(receipt(messages).value.length, 1)
          return plan(messages)
        },
        source(`question-${order}`, 'Who is this?', {
          senderId: 'asker',
          parts: [
            { type: 'text', text: 'Who is this?' },
            { type: 'mention', userId: person },
          ],
        }),
      )
      assert.ok(response)
      const uses = h.engine.experience.targetUses(scope, response)
      assert.deepEqual(
        uses.map((use) => use.kind).sort(),
        order === 'person-first' ? ['applied', 'exposed'] : ['exposed'],
      )
      assert.equal(
        h.rows()[0]?.uses.applied,
        order === 'person-first' ? 1 : 0,
        'turn and target records count once, not as two efficacy observations',
      )
      h.feedback(`negative-${order}`, response, '这次先查我的记忆不合适')
      assert.equal(h.rows()[0]?.status, order === 'person-first' ? 'suspended' : 'active')
      const negative = h.engine.experience
        .listOutcomes(scope)
        .find((outcome) => outcome.source.messageId === `negative-${order}`)
      assert.equal(negative?.code, order === 'person-first' ? 'counterexample' : undefined)
      if (order === 'person-first') {
        assert.equal(h.rows()[0]?.reason, 'explicit_counterexample')
        await h.ask(
          t,
          (messages) => {
            assert.equal(initial(messages).policyContext, undefined)
            return plan(messages, { action: 'noReply' })
          },
          source('after-counterexample', 'Who is this?', {
            senderId: 'asker',
            parts: [
              { type: 'text', text: 'Who is this?' },
              { type: 'mention', userId: person },
            ],
          }),
        )
      }
    } finally {
      await h.close()
    }
  })

test('runtime hint expiry and source recall survive restart without automatic relearning', async (t) => {
  clock(t)
  const h = fixture()
  try {
    await seed(t, h)
    await h.reopen()
    assert.equal(h.rows()[0]?.status, 'active')
    t.mock.timers.tick(8 * 86400000)
    await h.ask(t, (messages) => {
      assert.equal(initial(messages).policyContext, undefined)
      return plan(messages, { action: 'noReply' })
    })
    assert.equal(h.rows()[0]?.status, 'suspended')
    assert.equal(h.rows()[0]?.reason, 'support_or_expiry_invalid')
    h.engine.runtime.recall(scene.id, 'preference-source')
    assert.equal(h.rows().length, 0)
    await h.reopen()
    assert.equal(h.rows().length, 0)
    assert.equal(h.stats().extractionCalls, 0)
  } finally {
    await h.close()
  }
})

test('actual Planner cancellation after delivering an outcome and recalling its source prevents the candidate action', async (t) => {
  clock(t)
  const h = fixture({ ...flags, memoryExperienceAutoActivateEnabled: false })
  try {
    await seed(t, h)
    const before = h.stats().transportCalls
    let round = 0
    await h.ask(t, (messages) => {
      if (++round === 1) return completion([call('experience.outcomes')])
      const outcome = receipt(messages).value[0]!
      assert.ok(outcome)
      assert.ok(h.outcomeContexts.at(-1)!.dependencies.has('preference-source'))
      h.engine.runtime.recall(scene.id, 'preference-source')
      return plan(messages, {
        action: 'noReply',
        actions: [
          { toolName: 'experience.propose', arguments: proposal(outcome, outcome.source.senderId) },
        ],
      })
    })
    assert.equal(round, 2)
    assert.equal(h.stats().transportCalls, before)
    assert.deepEqual(h.actionResults, [])
    assert.equal(h.rows().length, 0)
    assert.ok(h.store.logs.some((row) => row.stage === 'planner' && row.status === 'cancelled'))
    await h.reopen()
    assert.equal(h.rows().length, 0)
  } finally {
    await h.close()
  }
})

test('multiple exposed policies on one actual target are ambiguous and cannot license a guessed withdrawal', async (t) => {
  clock(t)
  const h = fixture()
  try {
    const target = await h.ask(t)
    h.feedback('preference-one', target!)
    h.feedback('plan-qualification-request', target!, '回答计划问题前先核对条件')
    const operation = h.rows().find((policy) => policy.action === 'verify_plan_qualifiers')!
    assert.equal(operation.status, 'candidate')
    const validated = h.engine.experience.validate(scope, operation.id, operation.version)
    h.engine.experience.review(scope, operation.id, validated.version, 'active')
    assert.equal(h.rows().length, 2)
    const versions = h
      .rows()
      .map((policy) => [policy.id, policy.version])
      .sort()
    const hintedTarget = await h.ask(
      t,
      (messages) => {
        assert.equal(initial(messages).policyContext[0].hints.length, 2)
        return plan(messages)
      },
      source('two-policy-query', 'What is the plan?'),
    )
    assert.ok(hintedTarget)
    assert.equal(h.engine.experience.targetUses(scope, hintedTarget).length, 2)
    h.feedback('ambiguous-withdrawal', hintedTarget!, '不要先说结论')
    assert.deepEqual(
      h
        .rows()
        .map((policy) => [policy.id, policy.version])
        .sort(),
      versions,
    )
    assert.ok(h.rows().every((policy) => policy.status === 'active' && policy.uses.applied === 0))
    assert.equal(h.engine.experience.listOutcomes(scope).length, 2)
  } finally {
    await h.close()
  }
})

test('style-only exposure and transport ACK cannot authorize an operation counterexample', async (t) => {
  clock(t)
  const h = fixture()
  try {
    const active = await seed(t, h),
      target = await h.ask(t)
    assert.deepEqual(
      h.engine.experience.targetUses(scope, target!).map((use) => use.kind),
      ['exposed'],
    )
    h.feedback('unsupported-operation-feedback', target!, '这次先查我的记忆不合适')
    assert.equal(h.rows()[0]?.status, 'active')
    assert.equal(h.rows()[0]?.version, active.version)
    assert.equal(h.rows()[0]?.uses.applied, 0)
    assert.equal(h.engine.experience.listOutcomes(scope).length, 1)
  } finally {
    await h.close()
  }
})

for (const action of ['exact_person_before_semantic', 'verify_plan_qualifiers'] as const)
  test(`style withdrawal cannot suspend an actually exposed ${action} policy`, async (t) => {
    clock(t)
    const h = fixture()
    try {
      const initialTarget = await h.ask(t)
      assert.ok(initialTarget)
      h.feedback(
        'non-style-request',
        initialTarget,
        action === 'exact_person_before_semantic'
          ? '涉及我的问题先查我的记忆'
          : '回答计划问题前先核对条件',
      )
      const candidate = h.rows()[0]!
      assert.equal(candidate.action, action)
      assert.equal(candidate.status, 'candidate')
      const validated = h.engine.experience.validate(scope, candidate.id, candidate.version)
      const active = h.engine.experience.review(scope, candidate.id, validated.version, 'active')
      if (action === 'exact_person_before_semantic')
        h.engine.memory.create({
          content: 'Synthetic exact-person lookup evidence.',
          kind: 'fact',
          scope: { kind: 'scene', sceneId: scene.id, personId: person },
        })
      const question = (id: string) =>
        action === 'exact_person_before_semantic'
          ? source(id, 'Who is this?', {
              parts: [
                { type: 'text', text: 'Who is this?' },
                { type: 'mention', userId: person },
              ],
            })
          : source(id, 'What is the plan?')
      let round = 0
      const target = await h.ask(
        t,
        (messages) => {
          const hints = initial(messages).policyContext[0].hints
          assert.equal(hints.length, 1)
          assert.equal(hints[0].action, action)
          if (++round === 1 && action === 'exact_person_before_semantic')
            return completion([call('memory.person', { personId: hints[0].condition.personId })])
          if (action === 'exact_person_before_semantic')
            assert.equal(receipt(messages).value.length, 1)
          return plan(messages)
        },
        question('non-style-query'),
      )
      assert.ok(target)
      const uses = h.engine.experience.targetUses(scope, target)
      assert.equal(uses.filter((use) => use.kind === 'exposed').length, 1)
      assert.equal(
        uses.filter((use) => use.kind === 'applied').length,
        action === 'exact_person_before_semantic' ? 1 : 0,
      )
      h.feedback('wrong-action-withdrawal', target, '不要先说结论')
      assert.equal(h.rows()[0]?.status, 'active')
      assert.equal(h.rows()[0]?.version, active.version)
      assert.equal(h.rows()[0]?.counterexampleIds.length, 0)
      assert.equal(h.engine.experience.listOutcomes(scope).length, 1)
      await h.ask(
        t,
        (messages) => {
          assert.equal(initial(messages).policyContext[0].hints[0].action, action)
          return plan(messages, { action: 'noReply' })
        },
        question('non-style-still-active'),
      )
    } finally {
      await h.close()
    }
  })
