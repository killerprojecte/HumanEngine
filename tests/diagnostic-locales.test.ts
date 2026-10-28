import test from 'node:test'
import assert from 'node:assert/strict'
import { flowCatalog, localizedError } from '../src/diagnostics/flow-catalog.ts'
import {
  readableLog,
  stdoutLog,
  diagnosticLines,
  type ReadableLog,
} from '../src/diagnostics/format.ts'
import type { Log } from '../src/schema.ts'

const failure: ReadableLog = {
  stage: 'turn',
  status: 'failed',
  reason: 'turn_failed',
  module: 'flow',
  code: 'flow.turn.failed',
  rootCause: {
    errorType: 'ModelError',
    errorCode: 'provider_http_401',
    errorMessage: 'provider_http_401',
    httpStatus: 401,
    adviceCode: 'provider_http_401',
    stackFrames: ['src/models.ts:100:2'],
    errorContext: {
      operation: 'model.complete',
      stage: 'model_attempt',
      role: 'planner',
      apiModel: 'fixture-planner',
      providerId: 'fixture-provider',
      attempt: 2,
      timeoutMs: 12000,
      remainingCandidates: 0,
      retryable: false,
    },
  },
}
test('final turn presentation includes root cause and corrective context in each selected locale', () => {
  const cn = readableLog(failure)
  for (const text of [
    '提供商拒绝认证',
    'HTTP=401',
    'fixture-planner',
    'fixture-provider',
    '尝试=2',
    '超时上限=12000ms',
    '处理建议',
    'API 密钥',
  ])
    assert.ok(cn.includes(text), text)
  assert.doesNotMatch(cn, /[錯誤請訊學習規劃嘗試鑰]/)
  const tw = readableLog(failure, 'zh-TW')
  assert.match(tw, /提供商拒絕認證/)
  assert.match(tw, /嘗試=2/)
  assert.match(tw, /處理建議/)
  const en = readableLog(failure, 'en')
  assert.match(en, /Provider rejected authentication/)
  assert.match(en, /Attempt=2/)
  assert.match(en, /Corrective action/)
  assert.doesNotMatch(en, /[\p{Script=Han}]/u)
})
test('stdout defaults to Simplified Chinese and ignores stale persisted presentation', () => {
  const log = {
    id: 'fixture',
    at: '2026-01-01T00:00:00.000Z',
    level: 'error',
    ...failure,
    message: '失敗：舊版繁體內容',
    traceId: 'trace',
    turnId: 'turn',
  } as Log
  const line = stdoutLog(log),
    prefix = line.split(' | ')[0]!
  assert.match(prefix, /追踪=trace/)
  assert.match(prefix, /提供商拒绝认证/)
  assert.doesNotMatch(prefix, /舊版繁體|失敗|追蹤/)
  assert.deepEqual(JSON.parse(line.split(' | ')[1]!), log)
})
test('locale catalogs cover identical flow keys and preserve simulation and cancellation truth', () => {
  const cn = flowCatalog('zh-CN')
  for (const lang of ['zh-TW', 'en'])
    for (const section of Object.keys(cn) as Array<keyof typeof cn>)
      assert.deepEqual(
        Object.keys(flowCatalog(lang)[section]).sort(),
        Object.keys(cn[section]).sort(),
      )
  for (const lang of ['zh-CN', 'zh-TW', 'en']) {
    const simulation = readableLog(
      {
        stage: 'send',
        status: 'completed',
        mode: 'simulation',
        reason: 'simulated_delivery',
        counts: { sentSegments: 1 },
      },
      lang,
    )
    assert.doesNotMatch(simulation, /平台.*确认|平台.*確認|Platform-acknowledged/)
    assert.ok(flowCatalog(lang).reasonLabels.cancellation_requested)
    assert.ok(flowCatalog(lang).nextLabels.wait_for_cleanup)
    assert.ok(localizedError('cause_not_captured', lang).summary)
  }
  assert.match(
    diagnosticLines({ errorType: 'UnknownError', errorCode: 'cause_not_captured' })[0]!,
    /未捕获底层原因/,
  )
})
test('diagnostic advice distinguishes rate limits, database writes, sends and malformed payloads', () => {
  assert.match(localizedError('provider_http_429').advice, /限流|额度/)
  assert.match(localizedError('SQLITE_BUSY').advice, /不要自动重放/)
  assert.match(localizedError('missing_send_receipt').advice, /避免重复/)
  assert.match(localizedError('invalid_provider_json').advice, /HTML|截断/)
  assert.match(localizedError('ECONNREFUSED').advice, /监听/)
  assert.match(localizedError('provider_http_503', 'en').summary, /server error/)
})

test('planner policy failures and stage budgets have specific advice in all locales', () => {
  for (const locale of ['zh-CN', 'zh-TW', 'en']) {
    for (const code of [
      'planner_invalid_plan',
      'planner_forbidden_tool',
      'planner_unknown_asset',
      'planner_duplicate_evidence',
      'planner_round_budget_exhausted',
      'planner_stale_action',
      'dialogue_forbidden_tool',
      'planner_timeout',
      'dialogue_timeout',
    ]) {
      const value = localizedError(code, locale)
      assert.notEqual(value.summary, localizedError('unknown_error', locale).summary, code)
      assert.ok(value.advice.length > 20, code)
    }
  }
  assert.match(localizedError('planner_timeout').advice, /不是用户取消/)
  assert.match(
    readableLog({ module: 'runtime', code: 'turn_failed', level: 'error' }),
    /未捕获底层原因/,
  )
})
