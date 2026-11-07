/** Synthetic traces from the actual pinned TypeScript dispatch implementation.
 * node --import tsx backend/tests/generate-dispatch-fixtures.mjs [--check]
 * No networking, real messages, paid providers, database or copied state machine.
 */
import { createHash } from 'node:crypto'
import { readFileSync, writeFileSync, mkdirSync } from 'node:fs'
import { getEventListeners } from 'node:events'
import { execFileSync } from 'node:child_process'
import {
  awaitSendStage,
  dispatchSegment,
  abortContainedAdapter,
} from '../../src/runtime/dispatch.ts'
import {
  outcomeError,
  errorContext,
  preserveOutcomeError,
} from '../../src/diagnostics/error-context.ts'
import { diagnosticCodes } from '../../src/diagnostics/error-codes.ts'

const sources = {
  'src/runtime/dispatch.ts': 'f3f40c29491a6cc637327045537943d95784020a6fac4be13f1327ff6b92d9a2',
  'src/diagnostics/errors.ts': '57ffd77577d48c946f7df32d81d55c67617a9235e74607b956411c822e3b0955',
  'src/plugins/builtin/snowluma/protocol.ts':
    'dc842bdad6f893c291e195c323553494d8a7f9c4a2617440e8d56dd7fed0fe33',
  'src/diagnostics/flow.ts': '1285d8fd3b3ca8f23dd9d7f718022dd3aef6d64e2e07fad553ec63471633c338',
  'src/diagnostics/error-codes.ts':
    '6672d79271e8b65cc7e9ebf98baa8950eec1c7631d6913f79f7dc43098427529',
  'src/diagnostics/error-context.ts':
    '4319a3cedac5fe015e4cbdea37db23aad915a035edf4304a9abb3e4320540a25',
  'src/budget/errors.ts': '327292e444f7b4347f27da14ea30980e090d68c6f2905cda3df3225bf14fecbd',
}
if (process.version !== 'v24.22.1') throw Error('Use pinned Node 24.22.1')
for (const [path, hash] of Object.entries(sources)) {
  if (
    createHash('sha256')
      .update(readFileSync(new URL(`../../${path}`, import.meta.url)))
      .digest('hex') !== hash
  )
    throw Error(`Oracle changed: ${path}; review before replacing fixtures`)
}
if (process.argv.slice(2).some((arg) => arg !== '--check')) throw Error('Only --check is supported')
const tick = () => new Promise((resolve) => setImmediate(resolve))
const cause = (value) => (value instanceof Error ? { error: value.message } : { value })
const cancelReason = (kind) =>
  kind === 'timeout'
    ? new DOMException('fixture_timeout', 'TimeoutError')
    : kind === 'string'
      ? 'context_changed'
      : new Error('fixture_cancel')
const project = (result, error) => ({
  ...(result === undefined ? {} : { result }),
  ...(error === undefined ? {} : { error: cause(error) }),
})

const stageCases = []
for (const action of ['resolve', 'reject', 'throw', 'pending']) {
  for (const abort of [false, true])
    stageCases.push({ name: `${action}_invocation_abort_${abort}`, action, abort })
}
for (const order of [
  'resolve_abort',
  'abort_resolve',
  'reject_abort',
  'abort_reject',
  'abort_later_resolve',
  'abort_later_reject',
])
  stageCases.push({ name: order, action: 'pending', order })
stageCases.push({ name: 'pre_cancelled_never_invokes', action: 'resolve', preAbort: true })
async function executeStage(config) {
  const controller = new AbortController()
  let calls = 0,
    resolve,
    reject,
    result,
    error
  const gate = new Promise((done, fail) => {
    resolve = done
    reject = fail
  })
  if (config.preAbort) controller.abort(new Error('fixture_cancel'))
  const pending = awaitSendStage(() => {
    calls++
    if (config.abort) controller.abort(new Error('fixture_cancel'))
    if (config.action === 'resolve') return Promise.resolve('receipt')
    if (config.action === 'reject') return Promise.reject(new Error('fixture_error'))
    if (config.action === 'throw') throw new Error('fixture_error')
    return gate
  }, controller.signal).then(
    (value) => {
      result = value
    },
    (caught) => {
      error = caught
    },
  )
  if (config.action === 'pending' && !config.abort) {
    await tick()
    const order = config.order ?? 'resolve_abort'
    for (const op of order.split('_')) {
      if (op === 'abort') controller.abort(new Error('fixture_cancel'))
      if (op === 'resolve') resolve('receipt')
      if (op === 'reject') reject(new Error('fixture_error'))
      if (op === 'later') await Promise.resolve()
    }
  }
  await pending
  // Both late success and rejection are harmless after retirement.
  resolve('late')
  reject(new Error('late'))
  await tick()
  return {
    name: config.name,
    config,
    expected: {
      ...project(result, error),
      calls,
      listeners: getEventListeners(controller.signal, 'abort').length,
    },
  }
}

const cases = []
const add = (name, config = {}) => cases.push({ name, ...config })
for (const status of ['sent', 'failed', 'unknown', 'cancelled']) {
  add(`receipt_${status}`, { status })
  add(`same_invocation_abort_${status}`, { status, sendAbort: true })
  add(`contained_same_invocation_abort_${status}`, { status, sendAbort: true, contained: true })
}
add('sent_without_message_id', { omitMessageId: true })
add('sent_empty_message_id', { messageId: '' })
add('adapter_microtask_abort_receipt', { send: 'microtask_abort_receipt' })
add('simulation_isolated', { mode: 'simulation' })
add('simulation_invalid_validation', { mode: 'simulation', validate: 'false' })
add('initial_ineligible', { initial: 'eligibility' })
add('initial_reply_recalled', { initial: 'reply' })
add('initial_aborted', { preAbort: true })
add('initial_grounding_invalid', { initial: 'grounding' })
add('typing_finished', { typing: 'resolve' })
add('typing_failure', { typing: 'reject' })
add('typing_sync_failure', { typing: 'throw' })
add('typing_abort_failure', { typing: 'abort_reject' })
for (const change of ['eligibility', 'reply', 'grounding'])
  add(`typing_changes_${change}`, { typing: 'resolve', typingChange: change })
for (const validation of [
  'false',
  'reject',
  'throw',
  'abort_false',
  'abort_reject',
  'pending_abort',
  'pending_string_abort',
  'later_self_abort',
])
  add(`validation_${validation}`, { validate: validation })
for (const change of ['eligibility', 'reply'])
  add(`validation_changes_${change}`, { validationChange: change })
for (const change of ['eligibility', 'adapter', 'reply', 'grounding', 'topic'])
  add(`authoritative_after_transmit_${change}`, { afterTransmitChange: change })
add('authoritative_after_transmit_abort', { afterTransmitAbort: true })
add('adapter_missing', { missing: true })
add('adapter_lookup_throws_before_invocation', { lookupThrow: true })
add('adapter_abort_before_invocation', { beforeInvokeAbort: true })
add('adapter_sync_throw', { send: 'throw' })
add('adapter_reject', { send: 'reject' })
add('adapter_self_abort_throw', { send: 'throw', sendAbort: true })
add('adapter_self_abort_reject', { send: 'reject', sendAbort: true })
add('adapter_pending_cancel', { send: 'pending', cancel: 'error' })
add('adapter_pending_timeout', { send: 'pending', cancel: 'timeout' })
add('adapter_pending_string_cancel', { send: 'pending', cancel: 'string' })
add('contained_waits_for_owned_receipt', { send: 'pending', cancel: 'error', contained: true })
for (const change of ['eligibility', 'adapter', 'reply', 'grounding', 'topic', 'none']) {
  add(`final_gate_${change}`, { send: 'held', transmitChange: change, reply: true })
  add(`final_gate_throws_${change}`, { send: 'held_throw', transmitChange: change, reply: true })
}
for (const detail of [
  'request_timeout',
  'send_invalidated',
  'provider_http_429',
  'budget_daily_limit',
  'private-canary',
  '',
  'provider_http_999',
])
  add(`detail_${detail || 'empty'}`, { status: 'failed', detail })
add('private_error_preserved', { status: 'unknown', privateError: true })
add('sent_next_segment', { index: 1, total: 3 })
add('sent_last_segment', { index: 2, total: 3 })
add('empty_reply_id_does_not_check_recall', { emptyReply: true, initial: 'reply' })
add('no_reply_does_not_check_recall', { noReply: true, initial: 'reply' })

async function executeDispatch(config) {
  const controller = new AbortController(),
    rows = [],
    spans = new Map(),
    calls = []
  let eligible = true,
    recalled = false,
    grounding = false,
    version = 1,
    sends = 0,
    physical = 0
  let validationFinish, sendFinish, release, entered
  const held = new Promise((resolve) => {
    release = resolve
  })
  const started = new Promise((resolve) => {
    entered = resolve
  })
  const context = {
    scene: { id: 's', kind: 'group', adapterId: 'a', accountId: 'b', platformSceneId: 'g' },
    mode: config.mode ?? 'real',
    turnId: 't',
    signal: controller.signal,
    dependencies: new Set(),
    validations: [],
  }
  const segment = {
    parts: [
      { type: 'text', text: 'fixture 😀' },
      { type: 'media', assetId: 'opaque:asset', mediaType: 'image' },
    ],
    ...(config.noReply ? {} : { replyToId: config.emptyReply ? '' : 'reply' }),
  }
  const receipt = () => {
    const value = {
      status: config.status ?? 'sent',
      ...(config.omitMessageId || (config.status && config.status !== 'sent')
        ? {}
        : { messageId: config.messageId ?? 'receipt' }),
      ...('detail' in config ? { detail: config.detail } : {}),
    }
    return config.privateError
      ? preserveOutcomeError(value, new Error('private-error-canary'), {
          operation: 'inner.send',
          stage: 'inner',
        })
      : value
  }
  let adapter = {
    send: (_scene, _segment, signal, beforeTransmit) => {
      sends++
      calls.push('send')
      if (_scene !== context.scene || _segment !== segment || signal !== controller.signal)
        throw Error('fixture_argument_identity')
      if (config.sendAbort) controller.abort(cancelReason('error'))
      if (config.send === 'throw') throw new Error('fixture_send')
      if (config.send === 'reject') return Promise.reject(new Error('fixture_send'))
      if (config.send === 'microtask_abort_receipt')
        return Promise.resolve().then(() => {
          controller.abort(cancelReason('error'))
          return receipt()
        })
      if (config.send === 'pending') {
        entered()
        return new Promise((resolve) => {
          sendFinish = resolve
        })
      }
      if (config.send === 'held' || config.send === 'held_throw') {
        entered()
        return held.then(() => {
          calls.push('transmit')
          if (config.send === 'held_throw') beforeTransmit()
          else
            try {
              beforeTransmit()
            } catch {
              return { status: 'failed', detail: 'send_invalidated' }
            }
          physical++
          return receipt()
        })
      }
      physical++
      change(config.afterTransmitChange)
      if (config.afterTransmitAbort) controller.abort(cancelReason('error'))
      return Promise.resolve(receipt())
    },
  }
  if (config.contained) abortContainedAdapter(adapter)
  let current = adapter
  const change = (what) => {
    if (what === 'eligibility') eligible = false
    if (what === 'reply') recalled = true
    if (what === 'grounding') grounding = true
    if (what === 'topic') version++
    if (what === 'adapter') current = { ...adapter }
  }
  change(config.initial)
  if (config.preAbort) controller.abort(cancelReason('error'))
  const services = {
    store: {
      log: (_level, _module, _code, row) => {
        spans.set(row.spanId, row.stage)
        rows.push({
          stage: row.stage,
          status: row.status,
          ...(row.reason ? { reason: row.reason } : {}),
          ...(row.nextStep ? { nextStep: row.nextStep } : {}),
          ...(row.counts ? { counts: row.counts } : {}),
          ...(row.receiptState ? { receiptState: row.receiptState } : {}),
          ...(row.parentSpanId ? { parentStage: spans.get(row.parentSpanId) } : {}),
          ...(row.errorContext?.operation ? { errorOperation: row.errorContext.operation } : {}),
          ...(row.errorContext?.stage ? { errorStage: row.errorContext.stage } : {}),
        })
      },
    },
    eligible: () => {
      calls.push('eligible')
      return eligible && !controller.signal.aborted
    },
    isRecalled: (scene, id) => {
      if (scene !== 's' || id !== 'reply') throw Error('fixture_recall_scope')
      calls.push('recall')
      return recalled
    },
    adapter: (id) => {
      if (id !== 'a') throw Error('fixture_adapter_scope')
      calls.push('adapter')
      if (config.lookupThrow) throw new Error('fixture_lookup')
      if (config.beforeInvokeAbort) controller.abort(cancelReason('error'))
      return config.missing ? undefined : current
    },
  }
  const guard = {
    grounding: {
      check: (segments) => {
        if (segments.length !== 1 || segments[0] !== segment) throw Error('fixture_grounding_scope')
        calls.push('grounding')
        return grounding ? 'changed' : undefined
      },
    },
    captureTransmitGuard: () => {
      calls.push('capture')
      const captured = version
      return () => {
        calls.push('topic')
        return version === captured
      }
    },
    validate: (request) => {
      calls.push('validate')
      if (
        request.operationId !== 'operation' ||
        request.segments.length !== 1 ||
        request.segments[0] !== segment
      )
        throw Error('fixture_validation_scope')
      change(config.validationChange)
      if (config.validate?.startsWith('abort_')) controller.abort(cancelReason('error'))
      if (config.validate === 'throw') throw new Error('fixture_validation')
      if (config.validate === 'reject' || config.validate === 'abort_reject')
        return Promise.reject(new Error('fixture_validation'))
      if (config.validate?.startsWith('pending_')) {
        entered()
        return new Promise((resolve) => {
          validationFinish = resolve
        })
      }
      if (config.validate === 'later_self_abort')
        return Promise.resolve().then(() => {
          controller.abort('context_changed')
          return false
        })
      return Promise.resolve(config.validate !== 'false' && config.validate !== 'abort_false')
    },
  }
  const typing = config.typing
    ? {
        wait: () => {
          calls.push('typing')
          change(config.typingChange)
          if (config.typing === 'abort_reject') controller.abort(cancelReason('error'))
          if (config.typing === 'throw') throw new Error('fixture_typing')
          return config.typing.includes('reject')
            ? Promise.reject(new Error('fixture_typing'))
            : Promise.resolve()
        },
      }
    : undefined
  let result, error
  const pending = dispatchSegment(
    services,
    context,
    segment,
    'operation',
    guard,
    typing,
    config.index ?? 0,
    config.total ?? 1,
  ).then(
    (value) => {
      result = value
    },
    (caught) => {
      error = caught
    },
  )
  if (
    config.validate?.startsWith('pending_') ||
    ['pending', 'held', 'held_throw'].includes(config.send)
  ) {
    await started
    // Let invocation return and its promise observers attach before the event.
    await tick()
    if (config.validate?.startsWith('pending_'))
      controller.abort(
        cancelReason(config.validate === 'pending_string_abort' ? 'string' : 'error'),
      )
    else if (config.send === 'pending') {
      controller.abort(cancelReason(config.cancel))
      if (config.contained) sendFinish(receipt())
    } else {
      change(config.transmitChange)
      release()
    }
  }
  await pending
  if (validationFinish) validationFinish(true)
  if (sendFinish) sendFinish({ status: 'sent', messageId: 'late' })
  await tick()
  const privateCause = result && outcomeError(result)
  if (result?.status === 'sent' && config.mode === 'simulation') {
    if (
      !/^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/.test(
        result.messageId,
      )
    )
      throw Error('fixture_invalid_uuid')
    result = { ...result, messageId: '<uuid>' }
  }
  return {
    name: config.name,
    config,
    expected: {
      ...project(result, error),
      ...(privateCause === undefined
        ? {}
        : { outcomeError: cause(privateCause), outcomeContext: errorContext(privateCause) ?? {} }),
      sends,
      physical,
      calls,
      flows: rows,
      listeners: getEventListeners(controller.signal, 'abort').length,
    },
  }
}

const fixture = {
  version: 1,
  baseline: '24fe8a773d87f2f09996e259fca46524c2c02928',
  node: process.version,
  sources,
  stages: [],
  dispatch: [],
}
for (const config of stageCases) fixture.stages.push(await executeStage(config))
for (const config of cases) fixture.dispatch.push(await executeDispatch(config))
const write = (path, value) => {
  const destination = new URL(path, import.meta.url)
  if (process.argv.includes('--check')) {
    if (readFileSync(destination, 'utf8') !== value) throw Error(`Stale fixture: ${path}`)
  } else writeFileSync(destination, value)
}
mkdirSync(new URL('../internal/dispatch/testdata/', import.meta.url), { recursive: true })
write('../internal/dispatch/testdata/traces-node.json', JSON.stringify(fixture, null, 2) + '\n')
write(
  '../internal/dispatch/diagnostic_codes_generated.go',
  execFileSync('gofmt', {
    input: `// Code generated by backend/tests/generate-dispatch-fixtures.mjs; DO NOT EDIT.\npackage dispatch\n\nimport "regexp"\n\nvar providerHTTPCode = regexp.MustCompile(\x60^provider_http_[1-5][0-9][0-9]$\x60)\nvar diagnosticCodes = map[string]struct{}{\n${[
      ...diagnosticCodes,
    ]
      .sort()
      .map((code) => `\t${JSON.stringify(code)}: {},`)
      .join(
        '\n',
      )}\n}\n\nfunc knownDiagnosticCode(code string) bool {\n\t_, found := diagnosticCodes[code]\n\treturn found || providerHTTPCode.MatchString(code)\n}\n`,
    encoding: 'utf8',
  }),
)
console.log(
  `${fixture.stages.length} settlement traces; ${fixture.dispatch.length} dispatch traces`,
)
