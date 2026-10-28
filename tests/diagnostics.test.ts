import test from 'node:test'
import assert from 'node:assert/strict'
import { resolve } from 'node:path'
import { requestIdFor, requestOperation, sanitizeError } from '../src/diagnostics/errors.ts'
import { Store } from '../src/store.ts'
import { createApp } from '../src/api.ts'

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
  assert.deepEqual(result, {
    errorType: 'TypeError',
    httpStatus: 502,
    stackFrames: ['src/api.ts:80:10'],
    causes: [{ errorType: 'Error', systemCode: 'ENOTFOUND', stackFrames: ['src/models.ts:40:12'] }],
  })
  assert.doesNotMatch(
    JSON.stringify(result),
    /fixture-private|private\.invalid|Authorization|lookup|send/,
  )
  Object.defineProperty(cause, 'cause', { value: error })
  assert.equal(sanitizeError(error).causes.length, 1)
  let reads = 0
  assert.deepEqual(
    sanitizeError({
      get stack() {
        reads++
        throw Error(privateText)
      },
    }),
    { errorType: 'UnknownError', stackFrames: [], causes: [] },
  )
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
  const store = new Store(':memory:')
  const app = createApp(store, { token: 'fixture-admin-only' })
  app.get('/api/diagnostic-fixture/:id', () => {
    const cause = Object.assign(new Error(`https://${privateText}@private.invalid`), {
      code: 'ECONNREFUSED',
    })
    throw new TypeError(`Bearer ${privateText}`, { cause })
  })
  const writes: string[] = []
  const originalWrite = process.stdout.write
  process.stdout.write = ((chunk: string | Uint8Array) => {
    writes.push(String(chunk))
    return true
  }) as typeof process.stdout.write
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
    process.stdout.write = originalWrite
    store.close()
  }
})
