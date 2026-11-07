/** Actual Runtime fault boundaries. No native implementation is used as oracle. */
import assert from 'node:assert/strict'
import { readFileSync, writeFileSync, mkdtempSync, rmSync } from 'node:fs'
import { join } from 'node:path'
import {
  RecoveryAuthority,
  bindRecovery,
  saveGate,
  metadata,
} from '../../src/memory/recovery/ledger.ts'
import { createHash } from 'node:crypto'
import { mock } from 'node:test'
import { Runtime } from '../../src/runtime.ts'
import { TurnWaitCoordinator } from '../../src/runtime/turn-wait.ts'
import { Store } from '../../src/store.ts'
import { outcomeError, failureFromOutcomes } from '../../src/diagnostics/error-context.ts'
import { fixtureReplyPlan } from '../../tests/helpers/reply-plan.ts'
assert.equal(process.version, 'v24.22.1')
process.env.TZ = 'UTC'
mock.timers.enable({ apis: ['Date'], now: new Date('2026-10-28T00:00:00Z') })
const call = (name, args) => ({ id: name, name, arguments: args })
const complete = (toolCalls) => ({
  text: '',
  toolCalls,
  modelId: 'fixture',
  usage: { inputTokens: 0, outputTokens: 0, totalTokens: 0 },
  assistant: { role: 'assistant', content: '', toolCalls },
})
const cases = []
for (const name of [
  'initial-read',
  'decision-read',
  'plan-read',
  'tool-read',
  'before-send',
  'before-send-once',
  'after-sent',
  'after-unknown',
  'readiness-after-decision',
  'scene-disabled-after-decision',
  'scene-deleted-after-decision',
  'scene-read-fault-after-decision',
]) {
  const store = new Store(':memory:')
  store.set('settings', {
    ...store.settings,
    persona: 'Fixture persona',
    timezone: 'UTC',
    debounceMs: 0,
    maxWaitMs: 100,
    groupTurnCooldownMs: 0,
    promptContextWindowEnabled: !name.startsWith('scene-'),
  })
  const scene = {
    id: 'scene',
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
    proactiveEnabled: true,
  })
  const originalRecalled = store.recalled.bind(store)
  const originalReady = store.assertRecoveryReady.bind(store)
  const failRead = () => {
    store.recalled = () => {
      throw Error('synthetic_source_read_failed')
    }
  }
  const effects = [],
    models = [],
    actions = [],
    reads = [],
    sent = []
  let planCalls = 0,
    transportCalls = 0
  const runtime = new Runtime(store, () => ({
    complete: async (role, messages, tools) => {
      const stage = tools.some((t) => t.name === 'participation.decide')
        ? 'decision'
        : tools.some((t) => t.name === 'reply.validate')
          ? 'validation'
          : role === 'replyer'
            ? 'replyer'
            : 'plan'
      if (stage === 'decision') {
        if (name === 'scene-disabled-after-decision')
          store.db
            .prepare('UPDATE scenes SET value=? WHERE id=?')
            .run(
              JSON.stringify({ ...store.scene(scene.id), participationEnabled: false }),
              scene.id,
            )
        if (name === 'scene-deleted-after-decision')
          store.db.prepare('DELETE FROM scenes WHERE id=?').run(scene.id)
        if (name === 'scene-read-fault-after-decision') store.db.exec('DROP TABLE scenes')
        if (name === 'decision-read') failRead()
        if (name === 'readiness-after-decision')
          store.assertRecoveryReady = () => {
            throw Error('recovery_synthetic_blocked')
          }
      }
      if (stage === 'plan' && name === 'plan-read') failRead()
      models.push(stage)
      effects.push({ kind: 'model', stage })
      if (stage === 'decision')
        return complete([
          call('participation.decide', {
            action: 'reply',
            reasonCode: 'fixture',
            targetMessageIds: ['source'],
          }),
        ])
      if (stage === 'validation') return complete([call('reply.validate', { valid: false })])
      if (stage === 'plan') {
        planCalls++
        if (name === 'tool-read' && planCalls === 1) return complete([call('fixture.read', {})])
        return complete([call('reply.plan', fixtureReplyPlan(messages))])
      }
      return complete([
        call('messages.send', {
          operationId: 'op',
          segments: [
            { replyToId: 'source', parts: [{ type: 'text', text: 'A concise response.' }] },
          ],
        }),
      ])
    },
  }))
  runtime.addAdapter('fixture', {
    send: async (_scene, segment, _signal, before) => {
      if (name === 'before-send') failRead()
      if (name === 'before-send-once')
        store.recalled = (...args) => {
          store.recalled = originalRecalled
          throw Error('synthetic_source_read_failed')
        }
      before?.()
      transportCalls++
      sent.push(segment)
      effects.push({ kind: 'send', segment })
      const receipt =
        name === 'after-unknown'
          ? { status: 'unknown', detail: 'delivery_unknown' }
          : { status: 'sent', messageId: `sent-${transportCalls}` }
      effects.push({ kind: 'receipt', receipt })
      if (name === 'after-sent' || name === 'after-unknown') failRead()
      return receipt
    },
    history: async () => ({ items: [], coverage: 'partial' }),
    getMessage: async () => ({ items: [], coverage: 'partial' }),
    dispose() {},
  })
  if (name === 'tool-read')
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
        reads.push(tool)
        failRead()
        return { items: ['synthetic evidence'] }
      },
    })
  const before = store.db.prepare('SELECT count(*) AS n FROM config').get().n
  runtime.ingest(
    {
      id: 'source',
      sceneId: scene.id,
      senderId: 'user',
      origin: 'live',
      status: 'available',
      sentAt: new Date().toISOString(),
      receivedAt: new Date().toISOString(),
      parts: [{ type: 'text', text: 'Please respond.' }],
    },
    scene,
    false,
  )
  const state = runtime.states.get(scene.id)
  if (name === 'initial-read') failRead()
  let runError
  try {
    await runtime.run(scene, state.topics.keys().next().value, false)
  } catch (e) {
    runError = e.message
  }
  const turn = store.logs.findLast(
    (row) => row.stage === 'turn' && !['started', 'cancel_requested'].includes(row.status),
  )
  const rows = runtime.states.get(scene.id)?.messages ?? []
  cases.push({
    name,
    expected: {
      ...(runError ? { runError } : {}),
      effects,
      warnings: [],
      models,
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
      receipts: [...runtime.operations.values()].flat(),
      ...(name === 'before-send-once'
        ? {
            outcomeCauseMessages: [...runtime.operations.values()]
              .flat()
              .map((receipt) => outcomeError(receipt)?.message ?? null),
            outcomeAggregate: failureFromOutcomes([...runtime.operations.values()].flat())?.message,
          }
        : {}),
    },
  })
  store.recalled = originalRecalled
  store.assertRecoveryReady = originalReady
  runtime.dispose()
  store.close()
}
const cleanupCases = []
for (const code of ['recovery_authority_head_changed', 'synthetic_cleanup_fault']) {
  const store = new Store(':memory:')
  store.set('settings', {
    ...store.settings,
    persona: 'Fixture persona',
    timezone: 'UTC',
    topicAdmissionEnabled: false,
  })
  const scene = {
    id: 'scene',
    kind: 'group',
    adapterId: 'fixture',
    accountId: 'bot',
    platformSceneId: 'group',
    name: 'Fixture',
    participationEnabled: true,
    learningEnabled: true,
    proactiveEnabled: true,
  }
  store.saveScene(scene)
  const runtime = new Runtime(store, () => ({
    complete: async () =>
      complete([
        call('participation.decide', {
          action: 'ignore',
          reasonCode: 'fixture',
          targetMessageIds: [],
        }),
      ]),
  }))
  runtime.ingest(
    {
      id: 'source',
      sceneId: scene.id,
      senderId: 'user',
      origin: 'live',
      status: 'available',
      sentAt: new Date().toISOString(),
      receivedAt: new Date().toISOString(),
      parts: [{ type: 'text', text: 'Synthetic.' }],
    },
    scene,
    false,
  )
  const state = runtime.states.get(scene.id),
    topicId = state.topics.keys().next().value
  const coordinator = new TurnWaitCoordinator(
    { quietMs: 0, maxWaitMs: 100, addressedQuietMs: 0, maxModelWaitMs: 100, maxPending: 8 },
    () => Date.now(),
  )
  coordinator.observe({ id: 'source', meaningful: true })
  const wake = coordinator.takeReady()
  assert.ok(wake)
  state.admissions.set(topicId, coordinator)
  const originalLog = store.log.bind(store),
    originalReady = store.assertRecoveryReady.bind(store)
  let timer,
    armed = false
  store.log = (level, module, event, context) => {
    const result = originalLog(level, module, event, context)
    if (
      !armed &&
      context?.stage === 'turn' &&
      !['started', 'cancel_requested'].includes(context.status)
    ) {
      armed = true
      state.pendingTurns.add(topicId)
      timer = setTimeout(() => {}, 60000)
      timer.unref()
      state.timers.set(topicId, timer)
      store.assertRecoveryReady = () => {
        throw Error(code)
      }
    }
    return result
  }
  let runError
  try {
    await runtime.run(scene, topicId, false, wake)
  } catch (error) {
    runError = error.message
  }
  cleanupCases.push({
    code,
    armed,
    runError: runError ?? null,
    tasks: state.tasks.size,
    pending: state.pendingTurns.size,
    timers: state.timers.size,
    wakeCurrent: coordinator.isCurrent(wake),
    retained: coordinator.retention(),
  })
  store.assertRecoveryReady = originalReady
  store.log = originalLog
  clearTimeout(timer)
  runtime.dispose()
  store.close()
}
// Validation callbacks may mutate dependencies or cancel the current turn.
const validationCases = []
for (const mode of ['add-recalled', 'abort', 'recall-local', 'valid']) {
  const store = new Store(':memory:')
  const scene = {
    id: 'scene',
    kind: 'group',
    adapterId: 'fixture',
    accountId: 'bot',
    platformSceneId: 'group',
    participationEnabled: true,
  }
  store.saveScene(scene)
  const runtime = new Runtime(store, () => ({ complete: async () => complete([]) }))
  runtime.ingest(
    {
      id: 'source',
      sceneId: scene.id,
      senderId: 'user',
      origin: 'live',
      status: 'available',
      sentAt: new Date().toISOString(),
      receivedAt: new Date().toISOString(),
      parts: [{ type: 'text', text: 'Synthetic.' }],
    },
    scene,
    false,
  )
  const state = runtime.states.get(scene.id)
  const controller = new AbortController()
  const dependencies = new Set(mode === 'recall-local' ? ['blocked'] : [])
  if (mode === 'add-recalled') store.recall(scene.id, 'blocked')
  const context = {
    scene,
    mode: 'real',
    signal: controller.signal,
    dependencies,
    validations: [
      () => {
        if (mode === 'add-recalled') dependencies.add('blocked')
        if (mode === 'abort') controller.abort('synthetic_abort')
        if (mode === 'recall-local') state.recalled.add('blocked')
        return true
      },
    ],
  }
  validationCases.push({ mode, eligible: runtime.eligible(context) })
  runtime.dispose()
  store.close()
}
const recallValidationRoot = mkdtempSync(join(process.cwd(), 'backend/.derived-recall-oracle-'))
let recallValidation
{
  const store = new Store(':memory:')
  const authority = new RecoveryAuthority(
    join(recallValidationRoot, 'authority'),
    '12345678-1234-4123-8123-123456789012',
  )
  saveGate(store.db, {
    head: authority.head(),
    authority: authority.path,
    minimumReader: 1,
    state: 'ready',
  })
  bindRecovery(store.db, authority)
  const scene = {
    id: 'scene',
    kind: 'group',
    adapterId: 'fixture',
    accountId: 'bot',
    platformSceneId: 'group',
    participationEnabled: true,
  }
  store.saveScene(scene)
  const runtime = new Runtime(store, () => ({ complete: async () => complete([]) }))
  runtime.ingest(
    {
      id: 'source',
      sceneId: scene.id,
      senderId: 'user',
      origin: 'live',
      status: 'available',
      sentAt: new Date().toISOString(),
      receivedAt: new Date().toISOString(),
      parts: [{ type: 'text', text: 'Synthetic.' }],
    },
    scene,
    false,
  )
  const state = runtime.states.get(scene.id)
  let sawTombstone = false,
    error = null
  state.tasks.set('synthetic-task', {
    dependencies: new Set(),
    controller: new AbortController(),
    validations: [
      () => {
        sawTombstone = store.recalled(scene.id, 'unrelated')
        throw Error('synthetic_derived_validation')
      },
    ],
  })
  try {
    runtime.recall(scene.id, 'unrelated')
  } catch (e) {
    error = e.message
  }
  recallValidation = {
    error,
    sawTombstone,
    tombstones: store.db.prepare('SELECT count(*) AS count FROM recall_tombstones').get().count,
    ack: metadata(store.db).head.sequence,
    authority: authority.head().sequence,
  }
  state.tasks.clear()
  runtime.dispose()
  store.close()
  authority.close()
  rmSync(recallValidationRoot, { recursive: true, force: true })
}
const sourceFiles = [
  'src/runtime.ts',
  'src/runtime/dispatch.ts',
  'src/runtime/reply-plan.ts',
  'src/store.ts',
  'src/memory/recovery/ledger.ts',
  'tests/helpers/reply-plan.ts',
]
const source = Object.fromEntries(
  sourceFiles.map((path) => [path, createHash('sha256').update(readFileSync(path)).digest('hex')]),
)
const path = 'backend/internal/sourceevents/testdata/runtime-authority-faults.json'
const raw =
  JSON.stringify(
    { node: process.version, source, cases, cleanupCases, validationCases, recallValidation },
    null,
    2,
  ) + '\n'
if (process.argv.includes('--check')) assert.equal(readFileSync(path, 'utf8'), raw)
else writeFileSync(path, raw)
console.log(`Verified ${cases.length} actual Runtime authority failure traces`)
