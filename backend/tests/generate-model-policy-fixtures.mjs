/** Actual frozen TypeScript policy oracle. No real network or paid providers.
 * node --import tsx backend/tests/generate-model-policy-fixtures.mjs [--check]
 */
import { createHash } from 'node:crypto'
import { mkdirSync, readFileSync, writeFileSync } from 'node:fs'
import { Router, ModelError } from '../../src/models.ts'
import { canonicalModelRole, normalizeRoleRoutes } from '../../src/model-roles.ts'
import {
  providerPolicy,
  providerOperationBudget,
  retryDelay,
  parseRetryAfter,
  providerAttemptsHandled,
} from '../../src/models/request-policy.ts'
import { progressDeadline, signalStreamProgress } from '../../src/models/stream-progress.ts'
import { withRoleBudget } from '../../src/models/role-budget.ts'
import { ModelProtocolRegistry } from '../../src/model-protocols.ts'
import { modelScheduler } from '../../src/scheduler.ts'
import { nativeStreamDispatcher } from '../../src/models/stream-dispatcher.ts'
import { budgetFailureCodes } from '../../src/budget/errors.ts'

const sources = {
  'src/models/stream-response.ts':
    '49e9742305b5a197ad6aeb98382057c31f2cbedeab1f3fd5a966b79d76173832',
  'src/models/stream-dispatcher.ts':
    'a45a3edd71a250035992af2cd9f76e503033f7bd666289f280ddaacb7f8e28f0',
  'src/models.ts': '61293616cd4cd978f44bc80ccfafe8ec95a1254d6e575ddb184bd10fcf91c8ca',
  'src/models/request-policy.ts':
    '55f0080b2cd64ba68fd207bfe94cab162f10cd3d9f34d819094a39a85890a63d',
  'src/models/role-budget.ts': 'a496b6d5f78baf5a690228c01290e798bdf8b4e570247a38358a4d780b5ec7fe',
  'src/model-roles.ts': 'd7014ef6614cc315df94c5773e54e5c990195ac98c2e7064833616a31412ab83',
  'src/model-protocols.ts': '95e45d005e786fcd2ef957354ffe7608cb5d827bc7e16cca5344a7905b193222',
  'src/models/stream-progress.ts':
    'ec2e74e2ebd6a07d2e2d79c380e7caec5d7d418c4301d82fec64e46af8b2a3e9',
  'src/budget/errors.ts': '327292e444f7b4347f27da14ea30980e090d68c6f2905cda3df3225bf14fecbd',
  'src/scheduler.ts': '3340a98e5545c146a2332dc335931efeb8c577806d08cb2d05f1c92d30b52d36',
  'src/diagnostics/flow.ts': '1285d8fd3b3ca8f23dd9d7f718022dd3aef6d64e2e07fad553ec63471633c338',
}
process.env.TZ = 'UTC'
if (process.version !== 'v24.22.1') throw Error('Use pinned Node 24.22.1')
if (process.argv.slice(2).some((arg) => arg !== '--check')) throw Error('Only --check is supported')
for (const [path, hash] of Object.entries(sources))
  if (
    createHash('sha256')
      .update(readFileSync(new URL('../../' + path, import.meta.url)))
      .digest('hex') !== hash
  )
    throw Error('Review changed oracle: ' + path)
const tag = (value) =>
  typeof value === 'number' && !Number.isFinite(value) ? { number: String(value) } : value
const captured = (task) => {
  try {
    return { value: task() }
  } catch (error) {
    return { error: error.message }
  }
}
const policies = [
  {},
  ...[-1, 0, 1, 2, 5, 6, 0.5, NaN, Infinity].map((retryCount) => ({ retryCount })),
  ...[99, 100, 30000, 300000, 300001, 100.5, NaN, Infinity].map((timeoutMs) => ({ timeoutMs })),
].map((input) => ({
  input,
  ...captured(() => ({
    ...providerPolicy(input),
    operationBudget: providerOperationBudget(input),
  })),
}))
const delays = []
for (const attempt of [-3, 0, 1, 1.9, 2, 6, 7, 100, NaN, Infinity])
  for (const after of [undefined, -20, 0, 300, 9999, 20000, NaN, Infinity])
    delays.push({
      attempt,
      ...(after === undefined ? {} : { after }),
      value: retryDelay(attempt, after),
    })
const now = Date.UTC(2026, 0, 1)
const retryHeaders = [
  null,
  '',
  ' ',
  '0',
  '-1',
  '+.25',
  '1.5',
  '10',
  '100',
  '1e2',
  '\uFEFF1\uFEFF',
  '\u00851\u0085',
  'garbage',
  'Thu, 01 Jan 2026 00:00:03 GMT',
  'Wednesday, 31-Dec-25 23:59:59 GMT',
  'Sunday, 01-Jan-50 00:00:03 GMT',
  'Sunday, 01-Jan-68 00:00:03 GMT',
  'Thu Jan  1 00:00:20 2026',
].map((input) => ({ input, now, value: parseRetryAfter(input, now) ?? null }))
const roles = ['planner', 'replyer', 'thinker', 'learning', 'vision', 'embedding', 'unknown'].map(
  (input) => ({ input, value: canonicalModelRole(input) }),
)
const normalize = [
  ['thinker'],
  ['planner', 'thinker'],
  ['replyer', 'thinker'],
  ['planner', 'planner'],
].map((input) => ({
  input,
  ...captured(() => normalizeRoleRoutes(input.map((role) => ({ role }))).map((row) => row.role)),
}))
const config = (ids = ['a', 'b']) => ({
  providers: [
    {
      id: 'p',
      protocol: 'oracle:policy',
      baseUrl: 'http://unused.invalid',
      credentialEnv: '',
      retryCount: 2,
      timeoutMs: 100,
    },
  ],
  models: ids.map((id) => ({
    id,
    providerId: 'p',
    apiModelName: id,
    capabilities: ['text', 'tools', 'vision'],
  })),
  routes: [{ role: 'replyer', orderedModelIds: ids, timeoutMs: 300000 }],
})
const success = (modelId) => ({
  text: 'ok',
  toolCalls: [],
  usage: { inputTokens: 1, outputTokens: 1, totalTokens: 2 },
  modelId,
  assistant: { role: 'assistant', content: 'ok', toolCalls: [] },
})
const errorView = (error) => ({
  code: error instanceof ModelError ? error.code : error.message,
  retryable: error instanceof ModelError ? error.retryable : false,
  ...(error.status === undefined ? {} : { status: error.status }),
  handled: providerAttemptsHandled(error),
  failures: (error.failures ?? []).map((e) => (e instanceof ModelError ? e.code : 'plain')),
})
const cases = []
function add(name, outcomes, changes = {}, request = {}) {
  cases.push({
    name,
    config: { ...config(), ...changes },
    request: { role: 'thinker', toolNames: [], hasImages: false, ...request },
    outcomes,
  })
}
add('first_candidate_success', {})
add('retry_429_then_success', {
  a: [{ code: 'provider_http_429', status: 429, retryable: true, retryAfterMs: 800 }],
})
add('fallback_after_three_attempts', {
  a: Array(3).fill({ code: 'provider_http_503', status: 503, retryable: true }),
})
add('nonretryable_falls_back', {
  a: [{ code: 'provider_http_401', status: 401, retryable: false }],
})
add('timeout_falls_back_without_retry', { a: [{ code: 'provider_timeout', retryable: false }] })
add('arbitrary_error_never_retries', { a: [{ plain: true }] })
add('all_retryable', {
  a: Array(3).fill({ code: 'provider_http_429', status: 429, retryable: true }),
  b: Array(3).fill({ code: 'provider_http_503', status: 503, retryable: true }),
})
add('mixed_retryability_removes_final_status', {
  a: [{ plain: true }],
  b: Array(3).fill({ code: 'provider_http_503', status: 503, retryable: true }),
})
add(
  'six_failures_retains_last_four',
  Object.fromEntries(
    ['a', 'b', 'c', 'd', 'e', 'f'].map((id) => [id, [{ code: 'failure_' + id, retryable: false }]]),
  ),
  config(['a', 'b', 'c', 'd', 'e', 'f']),
)
for (const code of [
  ...budgetFailureCodes,
  'protocol_disabled',
  'provider_stream_interrupted',
  'provider_stream_invalid',
  'provider_stream_future',
  'memory_read_disabled',
  'memory_read_revoked',
  'personalization_disabled',
])
  add('terminal_' + code, { a: [{ code, retryable: false }] })
add('unknown_budget_prefix_can_fallback', {
  a: [{ code: 'budget_not_in_source', retryable: false }],
})
add(
  'skip_missing_model',
  {},
  { routes: [{ role: 'replyer', orderedModelIds: ['missing', 'a'], timeoutMs: 300000 }] },
)
add(
  'skip_missing_provider',
  {},
  { models: [{ id: 'a', providerId: 'missing', capabilities: ['text'] }, config().models[1]] },
)
add(
  'skip_text_mismatch',
  {},
  { models: [{ id: 'a', providerId: 'p', capabilities: ['embedding'] }, config().models[1]] },
)
add(
  'skip_tools_mismatch',
  {},
  { models: [{ id: 'a', providerId: 'p', capabilities: ['text', 'vision'] }, config().models[1]] },
  { toolNames: ['memory.read'] },
)
add(
  'skip_vision_mismatch',
  {},
  { models: [{ id: 'a', providerId: 'p', capabilities: ['text', 'tools'] }, config().models[1]] },
  { hasImages: true },
)
add('no_compatible_model', {}, { models: [] })
add('empty_route', {}, { routes: [{ role: 'replyer', orderedModelIds: [], timeoutMs: 300000 }] })
add('route_missing', {}, { routes: [] })
add(
  'duplicate_normalized_routes',
  {},
  {
    routes: [
      { role: 'replyer', orderedModelIds: ['a'] },
      { role: 'thinker', orderedModelIds: ['b'] },
    ],
  },
)
add('wire_name_collision', {}, {}, { toolNames: ['a.b', 'a__b'] })
for (const name of ['', 'a-b_C.9', 'x'.repeat(64), 'x'.repeat(65), 'a b', '工具'])
  add('tool_' + (name || 'empty'), {}, {}, { toolNames: [name] })
add(
  'invalid_stage_budget',
  {},
  { routes: [{ role: 'replyer', orderedModelIds: ['a'], timeoutMs: 0 }] },
)
add(
  'invalid_provider_policy_falls_back',
  {},
  { providers: [{ ...config().providers[0], retryCount: 6 }] },
)
add(
  'no_retry_configured',
  { a: [{ code: 'provider_http_503', status: 503, retryable: true }] },
  { providers: [{ ...config().providers[0], retryCount: 0 }] },
)
const originalTimer = globalThis.setTimeout,
  originalClear = globalThis.clearTimeout
for (const item of cases) {
  const events = [],
    calls = [],
    counts = new Map()
  let current = 0
  const writer = {
    log(_level, _module, code, entry) {
      const counts = entry?.counts ?? {}
      if (code === 'flow.model_attempt.started') current = counts.candidateIndex
      if (code === 'flow.model_attempt.skipped')
        events.push({
          kind: 'skip',
          modelId: entry.modelId,
          index: counts.candidateIndex,
          code: entry.reason,
        })
      if (code === 'flow.provider_attempt.started')
        events.push({
          kind: 'attempt',
          modelId: entry.modelId,
          index: current,
          attempt: counts.attempt,
          maxAttempts: counts.maxAttempts,
          timeoutMs: counts.timeoutMs,
        })
      if (code === 'flow.provider_retry_wait.started')
        events.push({
          kind: 'retry',
          modelId: entry.modelId,
          index: current,
          attempt: counts.attempt,
          maxAttempts: counts.maxAttempts,
          timeoutMs: counts.timeoutMs,
          delayMs: counts.retryDelayMs,
        })
    },
  }
  const log = (entry) => {
    if (entry.event === 'model.failed')
      events.push({ kind: 'failed', modelId: entry.modelId, index: current, code: entry.code })
  }
  // Only external invocation is replaced. Router selection, tool checks, role
  // normalization, attempt loops, fallback aggregation and flow all remain real.
  globalThis.setTimeout = (fn, ms) => {
    const timer = { active: true }
    if (ms <= 10000)
      queueMicrotask(() => {
        if (timer.active) fn()
      })
    return timer
  }
  globalThis.clearTimeout = (timer) => {
    if (timer) timer.active = false
  }
  try {
    const router = new Router(
      item.config,
      log,
      () => {},
      undefined,
      () => undefined,
      writer,
    )
    router.completeProtocol = async (_provider, model, role) => {
      const attempt = (counts.get(model.id) ?? 0) + 1
      counts.set(model.id, attempt)
      calls.push({ modelId: model.id, role, attempt })
      const outcome = item.outcomes[model.id]?.[attempt - 1]
      if (outcome?.plain)
        throw Object.assign(new Error('private error'), {
          status: 503,
          retryable: true,
          code: 'ECONNRESET',
        })
      if (outcome?.code)
        throw new ModelError(outcome.code, outcome.status, outcome.retryable, {
          retryAfterMs: outcome.retryAfterMs,
        })
      return success(model.id)
    }
    const response = await router.complete(
      item.request.role,
      [
        {
          role: 'user',
          content: 'synthetic',
          ...(item.request.hasImages ? { images: ['synthetic-image'] } : {}),
        },
      ],
      item.request.toolNames.map((name) => ({ name, description: 'synthetic', inputSchema: {} })),
    )
    item.expected = { modelId: response.modelId, events, calls }
  } catch (error) {
    item.expected = { error: errorView(error), events, calls }
  } finally {
    globalThis.setTimeout = originalTimer
    globalThis.clearTimeout = originalClear
  }
}

// Real Router, protocol registry, global scheduler, and real 100ms protocol
// deadlines. fetch is a local in-memory fake, never a network client.
const lifecycle = []
const tick = () => new Promise((resolve) => setImmediate(resolve))
const pause = (ms) => new Promise((resolve) => originalTimer(resolve, ms))
const nativeResponse = () =>
  new Response(
    JSON.stringify({
      choices: [{ message: { content: 'ok' }, finish_reason: 'stop' }],
      usage: { prompt_tokens: 1, completion_tokens: 1, total_tokens: 2 },
    }),
    { headers: { 'content-type': 'application/json' } },
  )
const originalFetch = globalThis.fetch
try {
  modelScheduler.configure(1, 0)
  for (const mode of [
    'custom_queue',
    'native_queue',
    'custom_uncooperative',
    'native_uncooperative',
  ]) {
    let release,
      entered = false,
      blockRelease
    let blocker
    if (mode.endsWith('_queue')) {
      blocker = modelScheduler.run(
        0,
        undefined,
        () =>
          new Promise((resolve) => {
            blockRelease = resolve
          }),
      )
      await tick()
    }
    const registry = new ModelProtocolRegistry()
    const controller = new AbortController()
    registry.register(
      'oracle:live',
      'Oracle',
      {
        complete: () => {
          entered = true
          return new Promise((resolve) => {
            release = () => resolve(success('a'))
          })
        },
      },
      controller.signal,
    )
    globalThis.fetch = async () => {
      entered = true
      return new Promise((resolve) => {
        release = () => resolve(nativeResponse())
      })
    }
    const native = mode.startsWith('native')
    const cfg = config(['a'])
    cfg.providers[0].protocol = native ? 'openai-chat' : 'oracle:live'
    cfg.providers[0].retryCount = 0
    cfg.models[0].streaming = false
    cfg.routes[0].timeoutMs = mode === 'native_uncooperative' ? 30 : 1000
    const router = new Router(
      cfg,
      () => {},
      () => {},
      registry,
    )
    const pending = router.complete('replyer', [{ role: 'user', content: 'synthetic' }]).then(
      (result) => ({ modelId: result.modelId }),
      (error) => ({ error: errorView(error) }),
    )
    if (mode === 'native_queue') {
      await pause(130)
      const before = { entered, status: modelScheduler.status }
      blockRelease()
      await blocker
      await tick()
      release()
      const result = await pending
      await tick()
      lifecycle.push({ mode, before, result, after: modelScheduler.status })
    } else {
      const result = await pending
      await tick()
      const before = { entered, status: modelScheduler.status }
      if (blockRelease) {
        blockRelease()
        await blocker
      }
      release?.()
      await tick()
      await tick()
      await tick()
      lifecycle.push({ mode, before, result, after: modelScheduler.status })
    }
    controller.abort()
  }
} finally {
  globalThis.fetch = originalFetch
  modelScheduler.configure(4, 0)
}

// Progress timers are captured by the actual helper, including propagation,
// parent cancellation after progress, and dispose without cancellation.
const progress = []
for (const mode of ['progress', 'expire', 'dispose', 'parent_abort']) {
  const timers = []
  globalThis.setTimeout = (fn, ms) => {
    const timer = { fn, ms, active: true }
    timers.push(timer)
    return timer
  }
  globalThis.clearTimeout = (timer) => {
    if (timer) timer.active = false
  }
  try {
    const parent = new AbortController()
    const stage = progressDeadline(parent.signal, 200, () => new Error('model_stage_timeout'))
    const provider = progressDeadline(stage.signal, 100, () => new Error('provider_timeout'))
    if (mode === 'progress' || mode === 'parent_abort') signalStreamProgress(provider.signal)
    if (mode === 'dispose') {
      provider.dispose()
      stage.dispose()
    }
    if (mode === 'expire') timers[1].fn()
    if (mode === 'parent_abort') parent.abort(new Error('caller'))
    progress.push({
      mode,
      active: timers.map((t) => t.active),
      stageAborted: stage.signal.aborted,
      providerAborted: provider.signal.aborted,
      reason: provider.signal.reason?.message ?? null,
    })
    provider.dispose()
    stage.dispose()
  } finally {
    globalThis.setTimeout = originalTimer
    globalThis.clearTimeout = originalClear
  }
}
// A managed role expiry rejects synchronously before a later parent abort.
// Conversely, parent cancellation must not synthesize task settlement. These
// traces use the real helper and capture only its timer, without a provider.
const roleBudgetRaces = []
for (const mode of ['timeout_then_parent_abort', 'parent_abort_then_timeout']) {
  let fire,
    rejectTask,
    settled = false
  globalThis.setTimeout = (fn) => {
    fire = fn
    return {}
  }
  globalThis.clearTimeout = () => {}
  try {
    const parent = new AbortController()
    const pending = withRoleBudget(
      50,
      parent.signal,
      () =>
        new Promise((_resolve, reject) => {
          rejectTask = reject
        }),
      () => new Error('model_stage_timeout'),
    ).then(
      () => {
        settled = true
        return { value: 'success' }
      },
      (error) => {
        settled = true
        return { error: error.message }
      },
    )
    if (mode === 'timeout_then_parent_abort') {
      fire()
      parent.abort(new Error('caller'))
    } else {
      parent.abort(new Error('caller'))
      fire()
    }
    await tick()
    const settledBeforeTask = settled
    rejectTask(new Error('request_cancelled'))
    roleBudgetRaces.push({ mode, settledBeforeTask, result: await pending })
  } finally {
    globalThis.setTimeout = originalTimer
    globalThis.clearTimeout = originalClear
  }
}
// Real native SSE parsing verifies that meaningful content stops managed
// deadlines and that late validation failure cannot replay the paid request.
const streams = []
await nativeStreamDispatcher()
const frame = (value) => `data: ${JSON.stringify(value)}\n\n`
const content = frame({ choices: [{ index: 0, delta: { content: 'ok' }, finish_reason: null }] })
const terminal =
  frame({ choices: [{ index: 0, delta: {}, finish_reason: 'stop' }] }) +
  frame({ choices: [], usage: { prompt_tokens: 1, completion_tokens: 1, total_tokens: 2 } }) +
  'data: [DONE]\n\n'
try {
  for (const mode of [
    'progress_survives_deadlines',
    'partial_is_terminal',
    'validation_after_content_is_terminal',
  ]) {
    let calls = 0
    const cfg = config()
    cfg.providers[0].protocol = 'openai-chat'
    cfg.routes[0].timeoutMs = 100
    globalThis.fetch = async () => {
      calls++
      const body = new ReadableStream({
        start(controller) {
          controller.enqueue(new TextEncoder().encode(content))
          if (mode === 'progress_survives_deadlines')
            originalTimer(() => {
              controller.enqueue(new TextEncoder().encode(terminal))
              controller.close()
            }, 140)
          else if (mode === 'partial_is_terminal') controller.close()
          else {
            controller.enqueue(
              new TextEncoder().encode(
                frame({ choices: [{ index: 0, delta: {}, finish_reason: 'length' }] }) +
                  'data: [DONE]\n\n',
              ),
            )
            controller.close()
          }
        },
      })
      return new Response(body, { headers: { 'content-type': 'text/event-stream' } })
    }
    const router = new Router(cfg)
    const result = await router.complete('replyer', [{ role: 'user', content: 'synthetic' }]).then(
      (result) => ({ modelId: result.modelId }),
      (error) => ({ error: errorView(error) }),
    )
    await tick()
    streams.push({ mode, calls, result, status: modelScheduler.status })
  }
} finally {
  globalThis.fetch = originalFetch
}

const output =
  JSON.stringify(
    {
      sources,
      policies,
      delays,
      retryHeaders,
      roles,
      normalize,
      cases,
      lifecycle,
      progress,
      roleBudgetRaces,
      streams,
    },
    (_key, value) => tag(value),
    2,
  ) + '\n'
const destination = new URL('../internal/modelservice/testdata/policy-node.json', import.meta.url)
if (process.argv.includes('--check')) {
  if (readFileSync(destination, 'utf8') !== output) throw Error('Model policy fixtures are stale')
} else {
  mkdirSync(new URL('.', destination), { recursive: true })
  writeFileSync(destination, output)
}
console.log(
  `Model policy oracle: ${cases.length} routing traces, ${policies.length} policies, ${delays.length} delays, ${lifecycle.length} scheduler lifecycles`,
)
