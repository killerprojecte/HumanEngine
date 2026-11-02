import test from 'node:test'
import assert from 'node:assert/strict'
import { resolve } from 'node:path'
import { requestIdFor, requestOperation, sanitizeError } from '../src/diagnostics/errors.ts'
import { Store } from '../src/store.ts'
import { stdoutLog } from '../src/diagnostics/format.ts'
import { createApp } from '../src/api.ts'

function capturedSink(writes: string[]) {
  let closed = false
  const status = () => ({
    state: closed ? ('closed' as const) : ('ready' as const),
    backpressured: false,
    acceptedRecords: writes.length,
    writtenRecords: writes.length,
    rejectedRecords: 0,
    rejectedBytes: 0,
    failedRecords: 0,
    unconfirmedRecords: 0,
    pendingRecords: 0,
    pendingBytes: 0,
    maxPendingBytes: 65536,
    maxPendingRecords: 32,
    lastFailure: null,
  })
  const result = () => ({
    complete: true,
    durable: false,
    throughAcceptedRecords: writes.length,
    flushedAcceptedRecords: writes.length,
    status: status(),
  })
  return {
    enqueue(record: string) {
      assert(writes.length < 32 && Buffer.byteLength(writes.join('') + record) <= 65536)
      writes.push(record)
      return true
    },
    get status() {
      return status()
    },
    async close() {
      closed = true
      return result()
    },
    closeSync() {
      closed = true
      return result()
    },
  }
}

const privateText = 'fixture-private-token?access_token=fixture-private-token'
test('structural error diagnostics retain safe frames and bounded causes without messages or URLs', () => {
  const cause = Object.assign(
    new Error(`DNS failed for https://user:${privateText}@private.invalid`),
    { code: 'ENOTFOUND' },
  )
  cause.stack = `Error: ${privateText}\n    at lookup (${resolve('src/models.ts')}:40:12)\n    at leak (https://private.invalid/${privateText}:1:2)`
  const error = Object.assign(new TypeError(`Authorization: Bearer ${privateText}`, { cause }), {
    httpStatus: 502,
  })
  error.stack = `TypeError: ${privateText}\n    at send (${resolve('src/api.ts')}:80:10)\n    at ${privateText} (${resolve('src/not-a-real-private-file.ts')}:4:2)`
  const result = sanitizeError(error)
  assert.deepEqual(
    {
      errorType: result.errorType,
      httpStatus: result.httpStatus,
      stackFrames: result.stackFrames,
      causes: result.causes.map(({ errorType, systemCode, stackFrames }) => ({
        errorType,
        systemCode,
        stackFrames,
      })),
    },
    {
      errorType: 'TypeError',
      httpStatus: 502,
      stackFrames: ['src/api.ts:80:10'],
      causes: [
        { errorType: 'Error', systemCode: 'ENOTFOUND', stackFrames: ['src/models.ts:40:12'] },
      ],
    },
  )
  assert.doesNotMatch(
    JSON.stringify(result),
    /fixture-private|private\.invalid|Authorization|lookup|send/,
  )
  Object.defineProperty(cause, 'cause', { value: error })
  assert.equal(sanitizeError(error).causes.length, 1)
  let reads = 0
  const accessorResult = sanitizeError({
    get stack() {
      reads++
      throw Error(privateText)
    },
  })
  assert.equal(accessorResult.errorType, 'UnknownError')
  assert.deepEqual(accessorResult.stackFrames, [])
  assert.deepEqual(accessorResult.causes, [])
  assert.equal(result.rootCause?.errorCode, 'ENOTFOUND')
  assert.ok(result.rootCause?.adviceCode)
  const inheritedAccessor = new Error()
  Object.setPrototypeOf(
    inheritedAccessor,
    Object.create(Error.prototype, {
      name: {
        get() {
          reads++
          throw Error(privateText)
        },
      },
    }),
  )
  assert.deepEqual(sanitizeError(inheritedAccessor).stackFrames, [])
  assert.equal(reads, 0)
  assert.equal(requestOperation('PUT', '/api/plugins/:id/enabled'), 'PUT /api/plugins/:id/enabled')
  assert.equal(requestOperation('PUT', '/api/private?token=value'), 'PUT /api/*')
  const request = new Request('https://example.invalid/')
  assert.equal(requestIdFor(request), requestIdFor(request))
  assert.notEqual(requestIdFor(request), requestIdFor(new Request(request)))
})

test('API unhandled failure has matching response, stdout and diagnostics request ID plus safe operation and cause', async () => {
  const writes: string[] = []
  const store = new Store(':memory:', undefined, { stdout: capturedSink(writes) })
  const app = createApp(store, { token: 'fixture-admin-only' })
  app.get('/api/diagnostic-fixture/:id', () => {
    const cause = Object.assign(new Error(`https://${privateText}@private.invalid`), {
      code: 'ECONNREFUSED',
    })
    throw new TypeError(`Bearer ${privateText}`, { cause })
  })
  try {
    const response = await app.request(
      '/api/diagnostic-fixture/private-scene?access_token=private-query',
      { headers: { Authorization: 'Bearer fixture-admin-only' } },
    )
    assert.equal(response.status, 503)
    const body = await response.json()
    assert.equal(body.requestId, response.headers.get('X-Request-ID'))
    const entry = store.logs.find((row) => row.code === 'api_unhandled_error')!
    assert.equal(entry.requestId, body.requestId)
    assert.equal(entry.operation, 'GET /api/diagnostic-fixture/:id')
    assert.equal(entry.phase, 'request')
    assert.equal(entry.errorType, 'TypeError')
    assert.ok(entry.stackFrames?.some((frame) => frame.includes('tests/diagnostics.test.ts:')))
    assert.equal(entry.causes?.[0]?.systemCode, 'ECONNREFUSED')
    const stdout = writes
      .map((value) => JSON.parse(value.slice(value.indexOf(' | ') + 3)))
      .find((row) => row.requestId === body.requestId)
    assert.deepEqual(stdout, entry)
    assert.doesNotMatch(
      JSON.stringify({ body, entry, stdout }),
      /fixture-private|private-scene|private-query|private\.invalid|Bearer|fixture-admin/,
    )
  } finally {
    store.close()
  }
})

test('logging health is authenticated, reports bounded whole-row retention and rejects post-close delivery', async () => {
  const writes: string[] = []
  const sink = capturedSink(writes)
  // Keep the delivery fixture bounded without retaining every synthetic record.
  const store = new Store(':memory:', undefined, {
    stdout: {
      ...sink,
      enqueue() {
        return true
      },
    },
  })
  const app = createApp(store, { token: 'fixture-log-health' })
  try {
    for (let i = 0; i < 510; i++)
      store.log('info', 'fixture', 'synthetic', { reason: 'x'.repeat(10000) })
    const huge = store.log('warn', 'fixture', 'oversized', { reason: 'x'.repeat(5 * 1024 * 1024) })
    assert.equal(huge.reason?.length, 5 * 1024 * 1024, 'log return value is not truncated')
    assert(
      !store.logs.some((row) => row.id === huge.id),
      'oversized rows are not partially retained',
    )
    const unauthorized = await app.request('/api/logs/status')
    assert.equal(unauthorized.status, 401)
    const response = await app.request('/api/logs/status', {
      headers: { Authorization: 'Bearer fixture-log-health' },
    })
    assert.equal(response.status, 200)
    const status = await response.json()
    assert(status.memory.retainedBytes <= 4 * 1024 * 1024)
    assert(status.memory.retainedRecords <= 500)
    assert(status.memory.evictedRecords > 0)
    assert.equal(status.memory.oversizedRecords, 1)
    assert.equal(store.logs.filter((row) => row.code === 'log_delivery_degraded').length, 1)
    await store.closeLogs()
    store.log('info', 'fixture', 'after_close')
    assert.equal(store.loggingStatus.closedRejectedRecords, 1)
    assert.equal(store.loggingStatus.closed, true)
  } finally {
    store.close()
  }
})

test('unserializable diagnostic payload cannot throw into runtime or enter the log view', () => {
  const writes: string[] = [],
    store = new Store(':memory:', undefined, { stdout: capturedSink(writes) })
  try {
    const row = store.log('warn', 'fixture', 'non_json', {
      counts: { invalid: 1n as unknown as number },
    })
    assert.equal(row.code, 'non_json')
    assert.equal(writes.length, 0)
    assert.equal(store.loggingStatus.unserializableRecords, 1)
    assert.deepEqual(
      store.logs.map((entry) => entry.code),
      ['log_delivery_degraded'],
    )
  } finally {
    store.close()
  }
})

test('reusing serialized diagnostic JSON preserves exact Unicode, escaped and correlated stdout bytes', () => {
  const row = {
    id: 'fixture',
    at: '2026-11-02T00:00:00.000Z',
    level: 'error' as const,
    module: 'fixture',
    code: 'escaped',
    reason: 'Unicode 漢字 🚀 \" \\ \n',
    traceId: 'trace',
    turnId: 'turn',
    errorType: 'TypeError',
    stackFrames: ['src/engine.ts:12:3'],
    counts: { records: 3 },
  }
  assert.equal(stdoutLog(row, JSON.stringify(row)), stdoutLog(row))
  assert.deepEqual(JSON.parse(stdoutLog(row, JSON.stringify(row)).split(' | ')[1]!), row)
})

test('failed async and synchronous logging cleanup cannot prevent Store database closure', async () => {
  const sink = capturedSink([])
  sink.close = async () => {
    throw new Error('synthetic_async_close_failure')
  }
  sink.closeSync = () => {
    throw new Error('synthetic_sync_close_failure')
  }
  const store = new Store(':memory:', undefined, { stdout: sink })
  await assert.rejects(store.closeLogs(), /synthetic_async_close_failure/)
  assert.equal(store.loggingStatus.shutdownComplete, false)
  assert.doesNotThrow(() => store.close())
  assert.equal(store.db.open, false)
  assert.equal(store.loggingStatus.shutdownComplete, false)
})

test('serialization loss before channel creation cannot claim complete asynchronous or synchronous shutdown', async () => {
  for (const asynchronous of [true, false]) {
    const store = new Store(':memory:')
    store.log('warn', 'fixture', 'non_json', { counts: { invalid: 1n as unknown as number } })
    assert.equal(store.loggingStatus.stdout, null)
    assert.equal(store.loggingStatus.file, null)
    if (asynchronous) {
      assert.deepEqual(await store.closeLogs(), [])
      assert.equal(store.loggingStatus.shutdownComplete, false)
    }
    store.close()
    assert.equal(store.db.open, false)
    assert.equal(store.loggingStatus.shutdownComplete, false)
  }
})
