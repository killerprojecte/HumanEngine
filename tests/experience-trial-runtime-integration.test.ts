import assert from 'node:assert/strict'
import test, { type TestContext } from 'node:test'
import { setImmediate as immediate } from 'node:timers/promises'
import { mkdtempSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { Engine } from '../src/engine.ts'
import { modelScheduler } from '../src/scheduler.ts'
import { Store } from '../src/store.ts'
import type { ChatMessage, Completion, Router, ToolDefinition } from '../src/models.ts'
import type { RuntimeExtension, RuntimeMessage, TurnContext } from '../src/runtime.ts'
import type { SceneRef } from '../src/onebot.ts'
import type { Settings } from '../src/schema.ts'
import type { ExperienceOutcome } from '../src/memory/domain/experience.ts'
import { trustedSourceEvidence } from '../src/memory/learning/source-evidence.ts'
import { renderExperienceAction } from '../src/memory/experience/policy-actions.ts'
import { fixtureReplyPlan } from './helpers/reply-plan.ts'

// Scripted model contracts and actual runtime boundaries, not policy effectiveness or NLP accuracy.
const scene: SceneRef = {
  id: 'trial-scene',
  kind: 'group',
  adapterId: 'offline',
  accountId: 'bot',
  platformSceneId: 'synthetic',
}
const foreign: SceneRef = { ...scene, id: 'trial-private', kind: 'private' }
const person = 'trial-person'
const scope = { sceneId: scene.id, simulation: false }
const flags: Partial<Settings> = {
  memoryExperienceEnabled: true,
  memoryExperienceAutoActivateEnabled: true,
  memoryExperienceHintsEnabled: true,
  memoryExperienceFeedbackEnabled: true,
  sourceEventRevisionsEnabled: true,
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
    now: new Date('2026-10-29T12:00:00Z'),
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
  const directory = mkdtempSync(join(tmpdir(), 'he-experience-trial-runtime-')),
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
  let sendStatus: 'sent' | 'unknown' = 'sent'
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
      send: async () => ({ status: sendStatus, messageId: `offline-bot-${++transportCalls}` }),
      getMessage: async () => ({ items: [], coverage: 'complete' }),
      history: async () => ({ items: [], coverage: 'complete' }),
      dispose() {},
    })
    engine.router = (_config, admit) =>
      ({
        complete: async (role, messages, tools = []) => {
          admit?.()
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
    sendStatus(value: 'sent' | 'unknown') {
      sendStatus = value
    },
    trial(id: string) {
      return engine.experience.trials.get(id)
    },
    read(value: boolean) {
      store.saveScene({ ...store.scene(scene.id)!, memoryReadEnabled: value })
      engine.settingsChanged()
    },
    snapshot(id: string) {
      const row = engine.runtime.preview(scene.id).find((m) => m.id === id)
      assert.ok(row)
      assert.equal(row.origin, 'live')
      return row as RuntimeMessage
    },
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
        sourceSnapshots: engine.runtime.preview(scene.id),
        ...patch,
      }
    },
    async ask(
      t: TestContext,
      fn: typeof planner = (messages) => plan(messages),
      message = source(`question-${++sequence}`),
      send = true,
      target = scene,
      requireIdle = true,
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
      if (requireIdle) assert.equal(engine.runtime.activeTasks, 0, 'actual foreground turn settles')
      return transportCalls > before && sendStatus === 'sent'
        ? `offline-bot-${transportCalls}`
        : undefined
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
      assert.deepEqual(modelErrors, [])
    },
  }
}
type Harness = ReturnType<typeof fixture>
type Operation = 'person' | 'plan'
const instruction = (op: Operation) =>
  op === 'person' ? '涉及我的问题先查我的记忆' : '回答计划问题前先核对条件'
const positive = (op: Operation) =>
  op === 'person' ? '这次先查我的记忆有帮助' : '这次先核对计划条件有帮助'
const negative = (op: Operation) =>
  op === 'person' ? '这次先查我的记忆不合适' : '这次先核对计划条件不合适'
const question = (id: string, op: Operation = 'person') =>
  source(id, op === 'person' ? '我的资料是什么？' : '我的计划条件是什么？')
const hints = (messages: ChatMessage[]) =>
  initial(messages).policyContext?.flatMap((v: any) => v.hints) ?? []
async function seed(t: TestContext, h: Harness, op: Operation = 'person', id = 'instruction') {
  const target = await h.ask(t)
  assert.ok(target)
  const calls = h.stats()
  h.feedback(id, target, instruction(op))
  assert.deepEqual(h.stats(), calls, 'fixed feedback classification does not call a model or send')
  const row = h.rows().find((p) => p.sources.some((s) => s.source.messageId === id))!
  assert.ok(row)
  return row
}
function evidence(h: Harness, op: Operation = 'person') {
  if (op === 'person')
    return h.engine.memory.create({
      kind: 'fact',
      content: 'Synthetic exact scoped profile declaration.',
      scope: { kind: 'scene', sceneId: scene.id, personId: person },
    })
  h.settings({
    memoryGraphEnabled: true,
    memoryGraphAnswerEnabled: true,
    memoryGraphAutoAdmissionEnabled: true,
  })
  const id = 'qualified-plan-source'
  h.ingest(source(id, 'I only possibly participate if the weather permits; not confirmed.'))
  const message = h.snapshot(id)
  const result = h.engine.memory.propose(
    {
      ...scope,
      provenance: 'source-only',
      sourceEvidence: trustedSourceEvidence([message], (s, m) => h.store.recalled(s, m)),
    },
    {
      content: 'Only possibly participate if weather permits; not confirmed.',
      kind: 'claim',
      epistemicKind: 'explicit',
      subject: { kind: 'self', sourceMessageId: id },
      sources: [
        { sceneId: scene.id, messageId: id, senderId: person, eventRef: message.sourceEventRef },
      ],
      relations: [
        {
          predicate: 'participates_in',
          personId: person,
          target: { kind: 'new', entityKind: 'plan', title: 'Synthetic qualified plan' },
          polarity: 'negative',
          modality: 'possible',
          temporalScope: 'past',
          conditions: ['ONLY_IF_WEATHER_PERMITS_NOT_CONFIRMED'],
        },
      ],
    },
  )
  assert.equal(result.status, 'saved', JSON.stringify(result))
  return result
}
async function apply(
  t: TestContext,
  h: Harness,
  id: string,
  op: Operation = 'person',
  beforePlan?: () => void,
) {
  let rounds = 0
  const target = await h.ask(
    t,
    (messages) => {
      const selected = hints(messages)
      assert.ok(
        selected.some(
          (v: any) =>
            v.action ===
            (op === 'person' ? 'exact_person_before_semantic' : 'verify_plan_qualifiers'),
        ),
      )
      if (++rounds === 1)
        return completion([
          call(
            op === 'person' ? 'memory.person' : 'memory.relations',
            op === 'person'
              ? {
                  personId: selected.find((v: any) => v.action === 'exact_person_before_semantic')
                    .condition.personId,
                }
              : {},
          ),
        ])
      const read = receipt(messages)
      assert.ok(read.value.length)
      if (op === 'plan') {
        const relation = (read.value as any[]).flatMap((row) => row.relationEvidence)[0]
        assert.equal(relation.polarity, 'negative')
        assert.equal(relation.modality, 'possible')
        assert.equal(relation.temporalScope, 'past')
        assert.deepEqual(relation.conditions, ['ONLY_IF_WEATHER_PERMITS_NOT_CONFIRMED'])
      }
      beforePlan?.()
      return plan(messages)
    },
    question(id, op),
  )
  assert.equal(rounds, 2)
  return target
}
async function noHint(t: TestContext, h: Harness, id = 'unhinted', op: Operation = 'person') {
  await h.ask(
    t,
    (messages) => {
      assert.deepEqual(hints(messages), [])
      return plan(messages, { action: 'noReply' })
    },
    question(id, op),
  )
}

for (const op of ['person', 'plan'] as const)
  test(`actual ${op} operation trial reaches only user-reported support after two independent delivered targets, with no feedback model calls`, async (t) => {
    clock(t)
    const h = fixture()
    try {
      evidence(h, op)
      const row = await seed(t, h, op)
      assert.equal(row.status, 'validated')
      assert.equal(row.activationBasis, undefined)
      assert.equal(row.validation?.ruleVersion, 'experience-mechanical-v2')
      assert.equal(row.validation?.datasetVersion, 'experience-fixed-behavior-holdout-v2')
      assert.equal(row.validation?.tier, 'mechanical_only')
      const trial = h.trial(row.id)!
      assert.equal(trial.phase, 'trial')
      assert.equal(trial.positives.length, 0)
      assert.equal(trial.policyVersion, row.version)
      const targets: string[] = []
      for (const n of [1, 2]) {
        const target = await apply(t, h, `qualified-read-${n}`, op)
        assert.ok(target)
        targets.push(target)
        const uses = h.engine.experience.targetUses(scope, target)
        assert.deepEqual(uses.map((u) => u.kind).sort(), ['applied', 'exposed'])
        assert.equal(
          uses.find((u) => u.kind === 'applied')!.operation,
          op === 'person' ? 'memory_person' : 'plan_qualifiers_checked',
        )
        const counts = h.stats(),
          beforeModels = h.planners.length + h.decisions.length + h.replies.length
        h.feedback(`positive-${n}`, target, positive(op))
        assert.deepEqual(h.stats(), counts)
        assert.equal(h.planners.length + h.decisions.length + h.replies.length, beforeModels)
        assert.equal(h.trial(row.id)!.positives.length, n)
        assert.equal(h.rows()[0]!.version, row.version + (n === 2 ? 1 : 0))
      }
      const promoted = h.trial(row.id)!
      assert.equal(promoted.phase, 'promoted')
      assert.equal(promoted.reason, 'user_reported_helpfulness')
      for (const key of ['sourceRoot', 'targetRoot', 'turnId'] as const)
        assert.equal(new Set(promoted.positives.map((p) => p[key])).size, 2)
      assert.notEqual(targets[0], targets[1])
      assert.equal(
        h.rows()[0]!.status,
        'validated',
        'promotion does not manufacture active/objective-success state',
      )
      await h.ask(
        t,
        (messages) => {
          const hint = hints(messages)[0]
          assert.equal(hint.eligibilityBasis, 'user_reported_support')
          assert.equal(hint.reportedHelpfulCount, 2)
          assert.match(hint.interpretation, /not independently verified task success/)
          return plan(messages, { action: 'noReply' })
        },
        question('promoted-hint', op),
      )
      const fixed = renderExperienceAction(row.action)
      assert.ok(h.decisions.every((m) => !JSON.stringify(m).includes(fixed)))
      assert.ok(h.replies.every((m) => !JSON.stringify(m).includes(fixed)))
      assert.equal(h.stats().extractionCalls, 0)
      await h.reopen()
      assert.equal(h.trial(row.id)!.phase, 'promoted')
      assert.equal(h.stats().extractionCalls, 0)
    } finally {
      await h.close()
    }
  })

test('style remains an instruction preference; actual send ACK and positive operation words never become applied benefit', async (t) => {
  clock(t)
  const h = fixture()
  try {
    const target = await h.ask(t)
    h.feedback('style-instruction', target!, '请先说结论')
    const style = h.rows()[0]!
    assert.equal(style.status, 'active')
    assert.equal(h.trial(style.id), undefined)
    const reply = await h.ask(t)
    assert.deepEqual(
      h.engine.experience.targetUses(scope, reply!).map((u) => u.kind),
      ['exposed'],
    )
    h.feedback('style-feedback', reply!, positive('person'))
    assert.equal(h.rows()[0]!.uses.applied, 0)
    assert.equal(h.trial(style.id), undefined)
    assert.equal(h.engine.experience.listOutcomes(scope).length, 1)
  } finally {
    await h.close()
  }
})

test('new switch defaults OFF and enabling it does not activate consumed operation candidates; a fresh instruction may start a trial', async (t) => {
  clock(t)
  const h = fixture({
    memoryExperienceEnabled: true,
    memoryExperienceAutoActivateEnabled: true,
    memoryExperienceHintsEnabled: true,
    sourceEventRevisionsEnabled: true,
  })
  try {
    assert.equal(h.store.settings.memoryExperienceFeedbackEnabled, false)
    evidence(h)
    const old = await seed(t, h)
    assert.equal(old.status, 'candidate')
    assert.equal(h.trial(old.id), undefined)
    h.settings({ memoryExperienceFeedbackEnabled: true })
    await noHint(t, h)
    assert.equal(h.trial(old.id), undefined)
    const fresh = await seed(t, h, 'person', 'fresh-instruction')
    assert.notEqual(fresh.id, old.id)
    assert.equal(h.trial(fresh.id)!.phase, 'trial')
    assert.ok(await apply(t, h, 'fresh-valid-read'))
  } finally {
    await h.close()
  }
})

test('repeated source and repeated target families cannot supply two reports; a new actual target and source can', async (t) => {
  clock(t)
  const h = fixture()
  try {
    evidence(h)
    const row = await seed(t, h),
      target = await apply(t, h, 'first-read')
    h.feedback('same-feedback', target!, positive('person'))
    const first = h.trial(row.id)!
    h.feedback('same-feedback', target!, positive('person'))
    h.feedback('different-message-same-target', target!, positive('person'))
    assert.deepEqual(h.trial(row.id), first)
    assert.equal(first.phase, 'trial')
    const next = await apply(t, h, 'second-read')
    h.feedback('independent-feedback', next!, positive('person'))
    assert.equal(h.trial(row.id)!.phase, 'promoted')
  } finally {
    await h.close()
  }
})

for (const op of ['person', 'plan'] as const)
  test(`negative report suspends the exact applied ${op} trial while style withdrawal cannot cross actions`, async (t) => {
    clock(t)
    const h = fixture()
    try {
      evidence(h, op)
      const row = await seed(t, h, op),
        target = await apply(t, h, 'negative-target', op)
      h.feedback('wrong-action-withdraw', target!, '不要先说结论')
      h.feedback('wrong-operation-positive', target!, positive(op === 'person' ? 'plan' : 'person'))
      assert.equal(h.trial(row.id)!.phase, 'trial')
      assert.equal(h.trial(row.id)!.positives.length, 0)
      h.feedback('negative-report', target!, negative(op))
      assert.equal(h.rows()[0]!.status, 'suspended')
      assert.equal(h.trial(row.id)!.phase, 'suspended')
      await noHint(t, h, 'after-negative', op)
      await h.reopen()
      assert.equal(h.trial(row.id)!.phase, 'suspended')
    } finally {
      await h.close()
    }
  })

for (const op of ['person', 'plan'] as const)
  test(`explicit ${op} withdrawal needs the exact exposed trial but not an applied read`, async (t) => {
    clock(t)
    const h = fixture()
    try {
      const row = await seed(t, h, op)
      const target = await h.ask(
        t,
        (messages) => {
          assert.equal(hints(messages).length, 1)
          return plan(messages)
        },
        question('exposed-only-withdrawal', op),
      )
      assert.deepEqual(
        h.engine.experience.targetUses(scope, target!).map((u) => u.kind),
        ['exposed'],
      )
      const cancel = op === 'person' ? '取消先查我的记忆' : '取消先核对计划条件'
      h.feedback('wrong-person-cancel', target!, cancel, { senderId: 'unrelated-person' })
      h.feedback(
        'wrong-action-cancel',
        target!,
        op === 'person' ? '取消先核对计划条件' : '取消先查我的记忆',
      )
      assert.equal(h.trial(row.id)!.phase, 'trial')
      h.feedback('right-cancel', target!, cancel)
      assert.equal(h.rows()[0]!.status, 'suspended')
      assert.equal(h.trial(row.id)!.phase, 'suspended')
      assert.equal(h.rows()[0]!.uses.applied, 0)
      await noHint(t, h, 'withdrawn', op)
    } finally {
      await h.close()
    }
  })

for (const change of [
  'instruction-recall',
  'positive-recall',
  'instruction-edit',
  'positive-edit',
] as const)
  test(`${change} revokes the actual first-snapshot trial without replay or upgrading an edited root`, async (t) => {
    clock(t)
    const h = fixture()
    try {
      evidence(h)
      const row = await seed(t, h),
        target = await apply(t, h, 'before-revocation')
      h.feedback('positive-source', target!, positive('person'))
      assert.equal(h.trial(row.id)!.positives.length, 1)
      const id = change.startsWith('instruction') ? 'instruction' : 'positive-source'
      if (change.endsWith('recall')) h.engine.runtime.recall(scene.id, id)
      else {
        const old = h.snapshot(id)
        const edited = h.engine.runtime.edit(
          {
            ...old,
            parts: [
              { type: 'text', text: 'Changed declaration, not the previous instruction/report.' },
            ],
          },
          scene,
          {
            editId: `edit-${id}`,
            order: { kind: 'observed_cas', expectedBase: old.sourceEventRef! },
          },
          false,
        )
        assert.equal(edited.status, 'edited')
      }
      await noHint(t, h, 'after-source-revoke')
      assert.equal(h.rows().find((p) => p.id === row.id)?.trialEligible ?? false, false)
      if (change.endsWith('recall')) {
        assert.equal(h.trial(row.id), undefined)
        const sql = JSON.stringify(
          h.store.db.prepare('SELECT value FROM memory_experience_trials').all(),
        )
        assert.doesNotMatch(
          sql,
          /sourceRoot|targetRoot|requesterId/,
          'revocation removes private receipt metadata, not just its readable projection',
        )
      }
      await h.reopen()
      await noHint(t, h, 'after-reopen')
      assert.equal(h.stats().extractionCalls, 0)
    } finally {
      await h.close()
    }
  })

test('seven-day expiry is an explicit expired trial, not a negative outcome or an automatic retry', async (t) => {
  clock(t)
  const h = fixture()
  try {
    const row = await seed(t, h)
    h.learning(false)
    t.mock.timers.tick(7 * 86400000 + 1)
    h.learning(true)
    await noHint(t, h)
    assert.equal(h.trial(row.id)!.phase, 'expired')
    assert.equal(h.trial(row.id)!.reason, 'expiry')
    assert.equal(h.rows()[0]!.counterexampleIds.length, 0)
    await h.reopen()
    assert.equal(h.trial(row.id)!.phase, 'expired')
    assert.equal(h.stats().extractionCalls, 0)
  } finally {
    await h.close()
  }
})

for (const change of ['feedback-aba', 'read-aba', 'policy-version-aba', 'negative'] as const)
  test(`actual trial hint delivered before ${change} cannot authorize a stale Replyer send`, async (t) => {
    clock(t)
    const h = fixture()
    try {
      evidence(h)
      const row = await seed(t, h)
      const original = await apply(t, h, 'original-target')
      const beforeReplies = h.replies.length,
        beforeSends = h.stats().transportCalls
      const target = await apply(t, h, 'stale-turn', 'person', () => {
        if (change === 'feedback-aba') {
          h.settings({ memoryExperienceFeedbackEnabled: false })
          h.settings({ memoryExperienceFeedbackEnabled: true })
        } else if (change === 'read-aba') {
          h.read(false)
          h.read(true)
        } else if (change === 'policy-version-aba') {
          const active = h.engine.experience.review(scope, row.id, row.version, 'active')
          const suspended = h.engine.experience.review(scope, row.id, active.version, 'suspended')
          h.engine.experience.validate(scope, row.id, suspended.version)
        } else h.feedback('during-turn-negative', original!, negative('person'))
      })
      assert.equal(target, undefined)
      assert.equal(h.stats().transportCalls, beforeSends)
      assert.equal(h.replies.length, beforeReplies)
      if (change === 'feedback-aba') {
        await noHint(t, h, 'old-trial-after-aba')
        assert.equal(h.rows().find((p) => p.id === row.id)!.trialEligible, false)
        const fresh = await seed(t, h, 'person', 'fresh-after-aba')
        assert.equal(h.trial(fresh.id)!.phase, 'trial')
        assert.ok(await apply(t, h, 'fresh-after-aba-read'))
      } else if (change === 'read-aba') assert.ok(await apply(t, h, 'fresh-after-read-resume'))
      else if (change === 'policy-version-aba') {
        assert.equal(h.rows()[0]!.status, 'validated')
        assert.ok(h.rows()[0]!.version > row.version)
        await noHint(t, h, 'after-policy-version-aba')
      } else assert.equal(h.trial(row.id)!.phase, 'suspended')
    } finally {
      await h.close()
    }
  })

test('learning OFF neither starts a new trial nor collects a report and is not replayed on resume', async (t) => {
  clock(t)
  const h = fixture()
  try {
    evidence(h)
    const row = await seed(t, h),
      target = await apply(t, h, 'before-learning-off')
    h.learning(false)
    h.feedback('off-report', target!, positive('person'))
    h.feedback('off-instruction', target!, instruction('person'))
    await noHint(t, h)
    assert.equal(h.rows().length, 1)
    assert.equal(h.trial(row.id)!.positives.length, 0)
    h.learning(true)
    assert.equal(h.trial(row.id)!.positives.length, 0)
    const next = await apply(t, h, 'after-learning-on')
    h.feedback('on-new-report', next!, positive('person'))
    assert.equal(h.trial(row.id)!.positives.length, 1)
    assert.equal(h.stats().extractionCalls, 0)
  } finally {
    await h.close()
  }
})

test('only exact live same-person same-scene report text can reinforce a genuinely applied target', async (t) => {
  clock(t)
  const h = fixture()
  try {
    evidence(h)
    const row = await seed(t, h),
      target = await apply(t, h, 'valid-target')
    const before = h.trial(row.id)!
    const cases: Array<[string, string, Partial<RuntimeMessage>]> = [
      ['quote', '“这次先查我的记忆有帮助”', {}],
      ['thanks', '谢谢，查记忆很好', {}],
      ['ambiguous', '也许这次先查我的记忆有帮助', {}],
      ['history', positive('person'), { origin: 'history' }],
      ['simulation', positive('person'), { origin: 'simulation' }],
      ['bot', positive('person'), { senderId: 'bot' }],
      ['other-person', positive('person'), { senderId: 'someone-else' }],
      ['missing-target', positive('person'), { replyToId: 'missing-bot' }],
    ]
    for (const [id, text, patch] of cases) h.feedback(id, target!, text, patch)
    h.ingest(
      source('cross-scene-report', positive('person'), { sceneId: foreign.id, replyToId: target }),
      foreign,
    )
    assert.deepEqual(h.trial(row.id), before)
    h.feedback('valid-new-report', target!, positive('person'))
    assert.equal(h.trial(row.id)!.positives.length, 1)
  } finally {
    await h.close()
  }
})

for (const mode of ['semantic-first', 'budget-omitted', 'ack-unknown'] as const)
  test(`${mode} cannot count as an applied target report even when the bot was offered a trial hint`, async (t) => {
    clock(t)
    const h = fixture()
    try {
      const record = evidence(h)
      const row = await seed(t, h)
      if (mode === 'budget-omitted') {
        assert.ok(record && 'id' in record)
        h.engine.memory.update(record.id, { content: 'BOUNDARY_COMPLETE_BODY_'.repeat(600) })
        h.settings({ plannerEvidenceMaxBytes: 16384 })
      }
      if (mode === 'ack-unknown') h.sendStatus('unknown')
      let rounds = 0
      const target = await h.ask(
        t,
        (messages) => {
          const personId = hints(messages)[0].condition.personId
          rounds++
          if (rounds === 1 && mode === 'semantic-first')
            return completion([call('memory.search', { query: 'NO_MATCH_AT_ALL_362' })])
          if (rounds === 1 || (rounds === 2 && mode === 'semantic-first'))
            return completion([call('memory.person', { personId })])
          if (mode === 'budget-omitted') {
            assert.deepEqual(receipt(messages).value, [])
            assert.equal(receipt(messages).coverage.status, 'limited')
          }
          return plan(messages)
        },
        question(`invalid-applied-${mode}`),
      )
      const targetId = target ?? `offline-bot-${h.stats().transportCalls}`
      assert.equal(
        h.engine.experience.targetUses(scope, targetId).filter((u) => u.kind === 'applied').length,
        0,
      )
      h.feedback('not-helpful-evidence', targetId, positive('person'))
      assert.equal(h.trial(row.id)!.positives.length, 0)
    } finally {
      await h.close()
    }
  })

test('two actually applied policies on one target are ambiguous and cannot be guessed into a helpfulness report', async (t) => {
  clock(t)
  const h = fixture()
  try {
    evidence(h)
    evidence(h, 'plan')
    const personal = await seed(t, h),
      planning = await seed(t, h, 'plan', 'plan-instruction')
    let rounds = 0
    const target = await h.ask(
      t,
      (messages) => {
        const selected = hints(messages)
        assert.equal(selected.length, 2)
        if (++rounds === 1)
          return completion([
            call('memory.person', {
              personId: selected.find((v: any) => v.action === 'exact_person_before_semantic')
                .condition.personId,
            }),
            call('memory.relations'),
          ])
        return plan(messages)
      },
      question('ambiguous-target', 'plan'),
    )
    const applied = h.engine.experience
      .targetUses(scope, target!)
      .filter((u) => u.kind === 'applied')
    assert.equal(applied.length, 2)
    h.feedback('ambiguous-positive', target!, positive('person'))
    assert.equal(h.trial(personal.id)!.positives.length, 0)
    assert.equal(h.trial(planning.id)!.positives.length, 0)
  } finally {
    await h.close()
  }
})

test('a copied feedback receipt or model-supplied passed/reward fields never authorize trial promotion', async (t) => {
  clock(t)
  const h = fixture({ ...flags, memoryExperienceFeedbackEnabled: false })
  try {
    const row = await seed(t, h)
    const ctx = h.context({ deferMemoryExposure: true }),
      extension = h.extension()
    const prepared = (await extension.execute(
      'experience.outcomes',
      {},
      ctx,
    )) as ExperienceOutcome[]
    assert.equal(prepared.length, 1)
    assert.deepEqual(
      extension.delivered?.('experience.outcomes', structuredClone(prepared), ctx),
      [],
    )
    const args = {
      action: row.action,
      condition: row.condition,
      outcomeRefs: [{ id: prepared[0]!.id, version: prepared[0]!.version }],
    }
    await assert.rejects(() => extension.execute('experience.propose', args, ctx), /not_exposed/)
    extension.delivered?.('experience.outcomes', prepared, ctx)
    await assert.rejects(() =>
      extension.execute('experience.propose', { ...args, passed: true, reward: 1 }, ctx),
    )
    assert.equal(h.rows()[0]!.status, 'candidate')
    assert.equal(h.trial(row.id), undefined)
  } finally {
    await h.close()
  }
})

for (const revoke of ['feedback-aba', 'read-aba', 'learning-aba'] as const)
  test(`actual Engine managed Replyer queued after trial application rechecks ${revoke} before physical custom transport`, async (t) => {
    clock(t)
    const h = fixture(),
      controller = new AbortController()
    let release: () => void = () => {},
      blockers: Promise<void>[] = [],
      providerCalls = 0
    try {
      evidence(h)
      const row = await seed(t, h)
      h.engine.protocols.register(
        'fixture:trial',
        'Synthetic managed trial',
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
            name: 'Offline trial',
            protocol: 'fixture:trial',
            baseUrl: 'http://127.0.0.1:1',
            credentialEnv: '',
            retryCount: 0,
            timeoutMs: 2000,
          },
        ],
        models: [
          {
            id: 'synthetic',
            displayName: 'Offline trial',
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
      const beforeSends = h.stats().transportCalls
      await h.ask(
        t,
        (messages) => {
          if (++rounds === 1)
            return completion([
              call('memory.person', { personId: hints(messages)[0].condition.personId }),
            ])
          assert.ok(receipt(messages).value.length)
          blockers = Array.from({ length: modelScheduler.status.concurrency }, () =>
            modelScheduler.run(0, undefined, () => wait),
          )
          return plan(messages)
        },
        question('managed-queued'),
        true,
        scene,
        false,
      )
      assert.equal(modelScheduler.status.queued, 1)
      assert.equal(providerCalls, 0)
      assert.equal(h.engine.runtime.activeTasks, 1)
      if (revoke === 'feedback-aba') {
        h.settings({ memoryExperienceFeedbackEnabled: false })
        h.settings({ memoryExperienceFeedbackEnabled: true })
      } else if (revoke === 'read-aba') {
        h.read(false)
        h.read(true)
      } else {
        for (const learningEnabled of [false, true]) {
          h.store.saveScene({ ...h.store.scene(scene.id)!, learningEnabled })
          h.engine.sceneChanged(h.store.scene(scene.id)!)
        }
      }
      release()
      await Promise.all(blockers)
      await advance(t, 0)
      assert.equal(h.engine.runtime.activeTasks, 0)
      assert.equal(providerCalls, 0)
      assert.equal(h.stats().transportCalls, beforeSends)
      assert.equal(h.trial(row.id)!.positives.length, 0)
      await advance(t, 1000)
      assert.equal(providerCalls, 0, 'consumed turn is not replayed after dispatch rejection')
      if (revoke === 'learning-aba') {
        h.engine.router = scripted
        const fresh = await apply(t, h, 'fresh-after-learning-resume')
        assert.ok(fresh, 'fresh current turn may use the same still-valid instruction receipt')
        assert.deepEqual(
          h.engine.experience
            .targetUses(scope, fresh)
            .map((use) => use.kind)
            .sort(),
          ['applied', 'exposed'],
        )
        assert.equal(h.trial(row.id)!.policyVersion, row.version)
        assert.equal(h.trial(row.id)!.phase, 'trial')
        h.feedback('fresh-helpfulness-after-learning-resume', fresh, positive('person'))
        assert.equal(h.trial(row.id)!.positives.length, 1)
        assert.equal(providerCalls, 0, 'the revoked queued request remains unexecuted')
      }
    } finally {
      release()
      await Promise.all(blockers)
      controller.abort()
      await h.close()
    }
  })

test("Bob mentioning Alice cannot consume Alice's personal trial; Alice self-question and administrator-authorized hints remain distinct", async (t) => {
  clock(t)
  const h = fixture()
  try {
    evidence(h)
    const alice = await seed(t, h)
    const bobQuestion = (id: string) =>
      source(id, 'Who is this?', {
        senderId: 'bob',
        parts: [
          { type: 'text', text: 'Who is this?' },
          { type: 'mention', userId: person },
        ],
      })
    const target = await h.ask(
      t,
      (messages) => {
        assert.deepEqual(hints(messages), [], 'mentioned subject is not the trial requester')
        return plan(messages)
      },
      bobQuestion('bob-mentions-alice'),
    )
    assert.ok(target)
    assert.deepEqual(h.engine.experience.targetUses(scope, target), [])
    assert.equal(h.rows()[0]!.uses.exposed, 0)
    assert.equal(h.rows()[0]!.uses.applied, 0)
    h.feedback('bob-cannot-report-alice-helpfulness', target, positive('person'), {
      senderId: 'bob',
    })
    assert.equal(h.trial(alice.id)!.positives.length, 0)
    const ownTarget = await apply(t, h, 'alice-own-question')
    assert.ok(ownTarget)
    assert.deepEqual(
      h.engine.experience
        .targetUses(scope, ownTarget)
        .map((u) => u.kind)
        .sort(),
      ['applied', 'exposed'],
    )
    assert.equal(h.trial(alice.id)!.phase, 'trial')
    const active = h.engine.experience.review(scope, alice.id, alice.version, 'active')
    assert.equal(active.activationBasis, 'administrator_review')
    await h.ask(
      t,
      (messages) => {
        const selected = hints(messages)
        assert.equal(
          selected.length,
          1,
          'explicit administrator authority preserves its pre-existing subject scope',
        )
        assert.equal(selected[0].action, 'exact_person_before_semantic')
        assert.equal(
          selected[0].eligibilityBasis,
          undefined,
          'administrator activation is not a personal trial grant',
        )
        return plan(messages, { action: 'noReply' })
      },
      bobQuestion('bob-after-administrator-review'),
    )
  } finally {
    await h.close()
  }
})
