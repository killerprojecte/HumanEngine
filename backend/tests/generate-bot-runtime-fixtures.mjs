/** Actual Runtime orchestration oracle. All stores, tools and transports are synthetic. */
import assert from 'node:assert/strict'
import { readFileSync, writeFileSync } from 'node:fs'
import { createHash } from 'node:crypto'
import { mock } from 'node:test'
import { Runtime, DecisionSchema, SendSchema } from '../../src/runtime.ts'
import { Store } from '../../src/store.ts'
import { fixtureReplyPlan } from '../../tests/helpers/reply-plan.ts'
import { z } from 'zod'
import { defaultSettingsInput, settingsReadValue } from '../../src/config-read-values.ts'
assert.equal(process.version, 'v24.22.1')
process.env.TZ = 'UTC'
mock.timers.enable({ apis: ['Date'], now: new Date('2026-10-28T00:00:00Z') })
const check = process.argv.includes('--check')
const write = (path, value) => {
  const s = JSON.stringify(value, null, 2) + '\n'
  if (check) assert.equal(readFileSync(path, 'utf8'), s, path)
  else writeFileSync(path, s)
}
const contracts = {
  defaults: settingsReadValue(defaultSettingsInput()),
  decisionSchema: z.toJSONSchema(DecisionSchema),
  sendSchema: z.toJSONSchema(SendSchema),
  prompts: {},
  tools: {},
}
const call = (name, args, id = name) => ({ id, name, arguments: args })
const complete = (toolCalls = [], text = '') => ({
  text,
  toolCalls,
  modelId: 'fixture',
  usage: { inputTokens: 0, outputTokens: 0, totalTokens: 0 },
  assistant: { role: 'assistant', content: text, toolCalls },
})
const cases = []
for (const name of [
  'reply',
  'simulation',
  'silent',
  'wait',
  'noReply',
  'missingPlan',
  'terminalText',
  'twoCalls',
  'unknown',
  'failed',
  'partial',
  'forbiddenTool',
  'actionQueued',
  'actionFailed',
  'noReplyAction',
  'read',
  'readFailed',
  'staleValidation',
  'recallDecision',
  'recallReplyer',
  'cancelReplyer',
  'disposeReplyer',
  'grounding',
  'suggestions',
  'simulationRawSettings',
  'simulationModels',
  'simulationRules',
  'simulationRecoveryOff',
  'reasonOverLimit',
  'reasonLimitInvalid',
  'reasonAstralFits',
]) {
  const store = new Store(':memory:')
  store.set('settings', {
    ...store.settings,
    persona: 'Fixture persona',
    timezone: 'UTC',
    debounceMs: 0,
    maxWaitMs: 100,
    groupTurnCooldownMs: 0,
    replySuggestionsEnabled: name === 'suggestions',
    promptContextWindowEnabled: name !== 'simulationRecoveryOff',
  })
  const simulation = name.startsWith('simulation')
  const scene = {
    id: simulation ? 'sim:fixture' : 'scene',
    kind: simulation ? 'simulation' : 'group',
    adapterId: 'fixture',
    accountId: 'bot',
    platformSceneId: 'group',
  }
  if (!simulation)
    store.saveScene({
      ...scene,
      name: 'Fixture',
      participationEnabled: true,
      learningEnabled: true,
      proactiveEnabled: true,
    })
  const modelStages = [],
    modelRequests = [],
    actions = [],
    reads = [],
    sent = [],
    effects = []
  let planCalls = 0,
    replyCalls = 0,
    transportCalls = 0
  const message = (id, text) => ({
    id,
    sceneId: scene.id,
    senderId: 'user',
    origin: simulation ? 'simulation' : 'live',
    status: 'available',
    sentAt: new Date().toISOString(),
    receivedAt: new Date().toISOString(),
    parts: [{ type: 'text', text }],
  })
  let runtime
  runtime = new Runtime(store, () => ({
    complete: async (role, messages, tools) => {
      const stage = tools.some((t) => t.name === 'participation.decide')
        ? 'decision'
        : tools.some((t) => t.name === 'reply.validate')
          ? 'validation'
          : role === 'replyer'
            ? 'replyer'
            : 'plan'
      modelStages.push(stage)
      effects.push({ kind: 'model', stage })
      modelRequests.push({
        stage,
        role,
        messages: structuredClone(messages),
        tools: structuredClone(tools),
      })
      const key = stage === 'plan' && name === 'suggestions' ? 'planSuggestions' : stage
      contracts.prompts[key] ??= messages[0].content.replace(/^Fixture persona\n\n/, '')
      for (const t of tools) if (t.name !== 'reply.plan') contracts.tools[t.name] ??= t
      const data = messages
        .filter((m) => m.role === 'user')
        .map((m) => JSON.parse(m.content))
        .reduce((a, b) => Object.assign(a, b), {})
      if (stage === 'decision') {
        const rawKey =
          name === 'simulationRawSettings'
            ? 'settings'
            : name === 'simulationModels'
              ? 'models'
              : name === 'simulationRules'
                ? 'rules'
                : undefined
        if (rawKey) {
          const existing = store.db.prepare('SELECT value FROM config WHERE key=?').get(rawKey)
          store.db
            .prepare(
              'INSERT INTO config(key,value) VALUES (?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value',
            )
            .run(rawKey, (existing?.value ?? 'null') + ' ')
        }
        if (name === 'simulationRecoveryOff')
          store.assertRecoveryReady = () => {
            throw new Error('recovery_gate_invalid')
          }

        if (name === 'recallDecision') runtime.recall(scene.id, 'source')
        return complete([
          call('participation.decide', {
            action: name === 'silent' ? 'silent' : name === 'wait' ? 'wait' : 'reply',
            reasonCode:
              name === 'reasonOverLimit' || name === 'reasonLimitInvalid'
                ? 'x'.repeat(201)
                : name === 'reasonAstralFits'
                  ? '😀'.repeat(101)
                  : 'fixture',
            targetMessageIds:
              name === 'reasonLimitInvalid'
                ? Array.from({ length: 21 }, () => 'source')
                : ['source'],
            ...(name === 'wait' ? { waitForMs: 50 } : {}),
          }),
        ])
      }
      if (stage === 'validation') return complete([call('reply.validate', { valid: false })])
      if (stage === 'plan') {
        planCalls++
        if (name === 'missingPlan') return complete()
        if (['read', 'readFailed'].includes(name) && planCalls === 1)
          return complete([call('fixture.read', {})])
        if (name === 'staleValidation')
          runtime.ingest(
            message('correction', 'Correction: do not send that answer.'),
            scene,
            false,
          )
        const patch = {}
        if (['noReply', 'noReplyAction'].includes(name)) patch.action = 'noReply'
        if (['actionQueued', 'actionFailed', 'noReplyAction'].includes(name))
          patch.actions = [{ toolName: 'learning.propose', arguments: { fixture: true } }]
        return complete([call('reply.plan', fixtureReplyPlan(messages, patch))])
      }
      replyCalls++
      if (name === 'terminalText') return complete([], 'This must not be delivered.')
      if (name === 'forbiddenTool') return complete([call('fixture.forbidden', {})])
      if (name === 'recallReplyer') runtime.recall(scene.id, 'source')
      if (name === 'cancelReplyer') runtime.cancel(scene.id)
      if (name === 'disposeReplyer') runtime.dispose()
      const text =
        name === 'grounding' ? 'I will send you another image tomorrow.' : 'A concise response.'
      const send = call('messages.send', {
        operationId: 'op',
        segments: [
          { replyToId: 'source', parts: [{ type: 'text', text }] },
          ...(['reply', 'simulation', 'partial'].includes(name)
            ? [{ replyToId: 'source', parts: [{ type: 'text', text: 'A second segment.' }] }]
            : []),
        ],
      })
      return complete(
        name === 'twoCalls'
          ? [send, { ...send, id: 'send2', arguments: { ...send.arguments, operationId: 'op2' } }]
          : [send],
      )
    },
  }))
  runtime.addAdapter('fixture', {
    send: async (_scene, segment, _signal, before) => {
      before?.()
      transportCalls++
      sent.push(segment)
      effects.push({ kind: 'send', segment })
      const receipt =
        name === 'unknown'
          ? { status: 'unknown', detail: 'delivery_unknown' }
          : name === 'failed' || (name === 'partial' && transportCalls === 2)
            ? { status: 'failed', detail: 'fixture_failed' }
            : { status: 'sent', messageId: `sent-${transportCalls}` }
      effects.push({ kind: 'receipt', receipt })
      return receipt
    },
    history: async () => ({ items: [], coverage: 'partial' }),
    getMessage: async () => ({ items: [], coverage: 'partial' }),
    dispose() {},
  })
  if (['actionQueued', 'actionFailed', 'noReplyAction', 'read', 'readFailed'].includes(name))
    runtime.addExtension({
      tools: [
        {
          name: 'learning.propose',
          description: 'Synthetic learning',
          inputSchema: { type: 'object' },
        },
        {
          name: 'fixture.read',
          description: 'Synthetic read',
          effect: 'read',
          inputSchema: { type: 'object' },
        },
      ],
      execute: async (tool, args) => {
        effects.push({ kind: 'tool', tool, args })
        if (tool === 'fixture.read') {
          reads.push(tool)
          if (name === 'readFailed') throw new Error('fixture_read_failed')
          return { items: ['synthetic evidence'] }
        }
        actions.push({ tool, args })
        if (name === 'actionFailed') throw new Error('fixture_action_failed')
        return { status: 'queued', jobId: 'fixture-job' }
      },
    })
  if (name === 'simulationModels')
    store.db
      .prepare('INSERT OR IGNORE INTO config(key,value) VALUES (?,?)')
      .run('models', JSON.stringify(store.models))
  if (name === 'simulationRules')
    store.db.prepare('INSERT OR IGNORE INTO config(key,value) VALUES (?,?)').run('rules', '[]')
  const before = store.db.prepare('SELECT count(*) AS n FROM config').get().n
  runtime.ingest(message('source', 'Please respond.'), scene, false)
  const state = runtime.states.get(scene.id)
  await runtime.run(scene, state.topics.keys().next().value, false)
  const turn = store.logs.findLast(
    (row) => row.stage === 'turn' && !['started', 'cancel_requested'].includes(row.status),
  )
  const rows = runtime.states.get(scene.id)?.messages ?? []
  const summary = {
    effects,
    warnings: store.logs
      .filter(
        (row) =>
          row.level === 'warn' &&
          row.module === 'runtime' &&
          row.code === 'planner_reason_omitted_over_limit',
      )
      .map((row) => row.code),
    models: modelStages,
    actions,
    reads,
    sent,
    transportCalls,
    botMessages: rows
      .filter((m) => m.senderId === 'bot')
      .map((m) => ({ parts: m.parts, replyToId: m.replyToId })),
    sourceStatus: rows.find((m) => m.id === 'source')?.status ?? null,
    status: turn?.status,
    reason: turn?.reason,
    counts: turn?.counts,
    active: runtime.activeTasks,
    registered: runtime.states.has(scene.id),
    persistentConfigDelta: store.db.prepare('SELECT count(*) AS n FROM config').get().n - before,
  }
  const topicID = state.topics.keys().next().value
  const normalize = (value) => {
    if (Array.isArray(value)) return value.map(normalize)
    if (value && typeof value === 'object')
      return Object.fromEntries(
        Object.entries(value).map(([key, v]) => [
          key,
          key === 'content' && typeof v === 'string' && v.startsWith('{')
            ? JSON.stringify(normalize(JSON.parse(v)))
            : normalize(v),
        ]),
      )
    return value === topicID ? 'fixture-topic' : value
  }
  cases.push({ name, expected: summary, requests: normalize(modelRequests) })
  runtime.dispose()
  store.close()
}
const recallCases = []
for (const name of ['prepareFailed', 'callbackFailed']) {
  const store = new Store(':memory:')
  store.set('settings', { ...store.settings, persona: 'Fixture persona' })
  const scene = {
    id: 'recall-scene',
    kind: 'group',
    adapterId: 'fixture',
    accountId: 'bot',
    platformSceneId: 'group',
  }
  store.saveScene({
    ...scene,
    name: 'Fixture',
    participationEnabled: true,
    learningEnabled: true,
    proactiveEnabled: false,
  })
  let start, release
  const entered = new Promise((resolve) => (start = resolve))
  let record = false
  const calls = []
  const runtime = new Runtime(store, () => ({
    complete: async () => {
      start()
      return await new Promise((resolve) => (release = resolve))
    },
  }))
  runtime.addExtension({
    tools: [],
    contextData(context) {
      context.validations.push(() => {
        if (record) calls.push('validate')
        return true
      })
      return undefined
    },
    prepareRecall() {
      calls.push('prepare')
      if (name === 'prepareFailed') throw new Error('prepare_failed')
      return () => {
        calls.push('callback')
        throw new Error('callback_failed')
      }
    },
    execute: async () => undefined,
  })
  const at = new Date().toISOString()
  runtime.ingest(
    {
      id: 'source',
      sceneId: scene.id,
      senderId: 'user',
      origin: 'live',
      status: 'available',
      sentAt: at,
      receivedAt: at,
      parts: [{ type: 'text', text: 'Synthetic recall' }],
    },
    scene,
    false,
  )
  const state = runtime.states.get(scene.id)
  const running = runtime.run(scene, state.topics.keys().next().value, false)
  await entered
  record = true
  let error
  try {
    runtime.recall(scene.id, 'source')
  } catch (e) {
    error = e.message
  }
  recallCases.push({
    name,
    expected: {
      calls: [...calls],
      error,
      sourceStatus: state.messages[0].status,
      aborted: [...state.tasks.values()][0].controller.signal.aborted,
    },
  })
  record = false
  runtime.cancel(scene.id)
  release(complete([call('participation.decide', { action: 'silent', reasonCode: 'fixture' })]))
  await running
  runtime.dispose()
  store.close()
}
write('backend/internal/botruntime/testdata/recall.json', recallCases)
const ingressCases = []
for (const senderId of ['bot', 'platform-bot', 'user'])
  for (const enabled of [false, true]) {
    const store = new Store(':memory:')
    store.set('settings', { ...store.settings, sourceEventRevisionsEnabled: enabled })
    const scene = {
      id: 'ingress-scene',
      kind: 'group',
      adapterId: 'fixture',
      accountId: 'bot',
      platformSceneId: 'group',
    }
    store.saveScene({
      ...scene,
      name: 'Fixture',
      participationEnabled: true,
      learningEnabled: true,
      proactiveEnabled: false,
    })
    const calls = [],
      events = []
    const runtime = new Runtime(store, () => {
      throw new Error('no_model_allowed')
    })
    runtime.addAdapter(
      'fixture',
      {
        send: async () => {
          throw new Error('no_send_allowed')
        },
        dispose() {},
      },
      'platform-bot',
    )
    runtime.on('message.received', (event) =>
      events.push({ type: event.type, message: event.message }),
    )
    runtime.addExtension({
      tools: [],
      received: (m) => calls.push({ kind: 'extension', id: m.id }),
      execute: async () => undefined,
    })
    const observe = store.sourceEvents.observe.bind(store.sourceEvents)
    store.sourceEvents.observe = (input) => {
      calls.push({ kind: 'observe', input })
      return observe(input)
    }
    const at = new Date().toISOString(),
      message = {
        id: 'source',
        sceneId: scene.id,
        senderId,
        origin: 'live',
        status: 'available',
        sentAt: at,
        receivedAt: at,
        parts: [{ type: 'text', text: 'Synthetic ingress' }],
        sourceEventRef: { eventId: 'caller-ref', revision: 99, deletionEpoch: 0 },
      }
    runtime.ingest(message, scene, false)
    await Promise.resolve()
    await Promise.resolve()
    const rows = runtime.states.get(scene.id).messages
    ingressCases.push({
      name: `${senderId}-${enabled}`,
      senderId,
      enabled,
      expected: {
        observeCalls: calls.filter((c) => c.kind === 'observe').length,
        extensionCalls: calls.filter((c) => c.kind === 'extension').length,
        senderId: rows[0].senderId,
        keptCallerRef: rows[0].sourceEventRef?.eventId === 'caller-ref',
        events: events.map((e) => e.type),
        topicCount: runtime.states.get(scene.id).topics.size,
      },
    })
    runtime.dispose()
    store.close()
  }
write('backend/internal/botruntime/testdata/ingress.json', ingressCases)

mock.timers.reset()
const schemaCases = []
const schemaCase = (kind, name, input) => {
  const result = (kind === 'send' ? SendSchema : DecisionSchema).safeParse(input)
  schemaCases.push({
    kind,
    name,
    input: JSON.stringify(input),
    expected: result.success ? JSON.stringify(result.data) : null,
  })
}
const validSend = (parts) => ({ operationId: 'operation', segments: [{ parts }] })
for (const part of [
  { type: 'text', text: 'valid' },
  { type: 'mention', userId: 'user' },
  { type: 'media', assetId: 'asset', mediaType: 'image' },
  { type: 'text', text: '' },
  { type: 'text' },
  { type: 'unknown', text: 'bad' },
  { type: 'media', mediaType: 'image' },
  { type: 'mention', userId: '' },
  { type: 'text', text: 'valid', extra: true },
  null,
  {},
  [],
  false,
])
  schemaCase('send', `part-${schemaCases.length}`, validSend([part]))
for (const count of [0, 1, 8, 9])
  schemaCase('send', `segments-${count}`, {
    operationId: 'operation',
    segments: Array.from({ length: count }, () => ({ parts: [{ type: 'text', text: 'x' }] })),
  })
for (const count of [0, 1, 20, 21])
  schemaCase(
    'send',
    `parts-${count}`,
    validSend(Array.from({ length: count }, () => ({ type: 'text', text: 'x' }))),
  )
for (const count of [0, 1, 100, 101])
  schemaCase('send', `operation-${count}`, {
    ...validSend([{ type: 'text', text: 'x' }]),
    operationId: 'x'.repeat(count),
  })
for (const text of [
  'x'.repeat(12000),
  'x'.repeat(12001),
  '😀'.repeat(12000),
  '😀'.repeat(12001),
  '\ud800'.repeat(12000),
  '\ud800'.repeat(12001),
])
  schemaCase('send', `text-length-${schemaCases.length}`, validSend([{ type: 'text', text }]))
for (const action of ['silent', 'wait', 'reply', 'other', null, 0])
  for (const wait of [undefined, -1, 0, 0.5, 60000, 60001, null])
    schemaCase('decision', `action-wait-${schemaCases.length}`, {
      action,
      reasonCode: 'fixture',
      ...(wait === undefined ? {} : { waitForMs: wait }),
    })
for (const count of [0, 20, 21])
  schemaCase('decision', `targets-${count}`, {
    action: 'reply',
    reasonCode: 'fixture',
    targetMessageIds: Array.from({ length: count }, (_, i) => `m${i}`),
  })
for (const count of [0, 4, 5])
  schemaCase('decision', `topics-${count}`, {
    action: 'reply',
    reasonCode: 'fixture',
    topics: Array.from({ length: count }, (_, i) => ({ topicId: `t${i}`, messageIds: [] })),
  })
for (const count of [0, 24, 25])
  schemaCase('decision', `topic-messages-${count}`, {
    action: 'reply',
    reasonCode: 'fixture',
    topics: [{ topicId: 't', messageIds: Array.from({ length: count }, (_, i) => `m${i}`) }],
  })
for (const input of [
  { action: 'reply', reasonCode: 'x'.repeat(200) },
  { action: 'reply', reasonCode: 'x'.repeat(201) },
  { action: 'reply', reasonCode: '😀'.repeat(200) },
  { action: 'reply', reasonCode: 'x', extra: 1 },
  { action: 'reply', reasonCode: 'x', topics: [{ topicId: '', messageIds: [] }] },
  { action: 'reply' },
  { reasonCode: 'x' },
  null,
])
  schemaCase('decision', `strict-${schemaCases.length}`, input)
write('backend/internal/botruntime/testdata/schema.json', schemaCases)
// Use the actual production protocol's visible JSON expression, rather than
// pretending raw in-process undefined is representable by the JSON bridge.
const modelSource = readFileSync('src/models.ts', 'utf8')
const visibleExpression = modelSource.match(
  /encoded = (JSON\.stringify\(\{ text: value\.text, toolCalls: value\.toolCalls, usage: value\.usage \}\))/,
)?.[1]
assert.ok(visibleExpression)
const visibleModel = new Function(
  'value',
  `const encoded = ${visibleExpression}; return JSON.parse(encoded)`,
)
const undefinedCases = []
for (const [kind, name, input] of [
  [
    'decision',
    'default-undefined',
    { action: 'reply', reasonCode: 'x', targetMessageIds: undefined },
  ],
  [
    'decision',
    'optional-undefined',
    {
      action: 'reply',
      reasonCode: 'x',
      waitForMs: undefined,
      topics: undefined,
      topicId: undefined,
    },
  ],
  ['decision', 'required-undefined', { action: undefined, reasonCode: 'x' }],
  ['decision', 'unknown-undefined', { action: 'reply', reasonCode: 'x', extra: undefined }],
  ['decision', 'null-default', { action: 'reply', reasonCode: 'x', targetMessageIds: null }],
  ['decision', 'null-optional', { action: 'reply', reasonCode: 'x', waitForMs: null }],
  [
    'send',
    'optional-undefined',
    {
      operationId: 'x',
      segments: [{ replyToId: undefined, parts: [{ type: 'text', text: 'x' }] }],
    },
  ],
  ['send', 'unknown-undefined', { ...validSend([{ type: 'text', text: 'x' }]), extra: undefined }],
  ['send', 'unknown-part-undefined', validSend([{ type: 'text', text: 'x', extra: undefined }])],
  ['send', 'required-undefined', validSend([{ type: 'text', text: undefined }])],
  [
    'send',
    'null-optional',
    { operationId: 'x', segments: [{ replyToId: null, parts: [{ type: 'text', text: 'x' }] }] },
  ],
  ['send', 'undefined-array', validSend([undefined])],
]) {
  const schema = kind === 'send' ? SendSchema : DecisionSchema,
    raw = schema.safeParse(input)
  const visible = visibleModel({
    text: '',
    toolCalls: [{ id: 'call', name: kind, arguments: input }],
    usage: {},
  }).toolCalls[0].arguments
  const normalized = schema.safeParse(visible)
  undefinedCases.push({
    kind,
    name,
    rawAccepted: raw.success,
    input: JSON.stringify(visible),
    expected: normalized.success ? JSON.stringify(normalized.data) : null,
  })
}
assert.equal(undefinedCases.find((c) => c.name === 'unknown-undefined').rawAccepted, false)
assert.ok(undefinedCases.find((c) => c.name === 'unknown-undefined').expected)
write('backend/internal/botruntime/testdata/undefined-wire.json', undefinedCases)

const sourceFiles = [
  'src/models.ts',
  'src/runtime.ts',
  'src/runtime/dispatch.ts',
  'src/runtime/reply-plan.ts',
  'src/runtime/reply-brief.ts',
  'src/runtime/reply-evidence-budget.ts',
  'tests/helpers/reply-plan.ts',
  'src/config-read-values.ts',
  'src/schema.ts',
]
const source = Object.fromEntries(
  sourceFiles.map((path) => [path, createHash('sha256').update(readFileSync(path)).digest('hex')]),
)
write('backend/internal/botruntime/testdata/contracts.json', contracts)
write('backend/internal/botruntime/testdata/runtime.json', { node: process.version, source, cases })
console.log(`Verified ${cases.length} actual Runtime turn traces`)
