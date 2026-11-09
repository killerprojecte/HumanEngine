/** Supplemental actual Store/Engine/Hono header observations. SQLite is in-memory; log sinks are synthetic.
 * Candidate only: no generated fixture or pass claim until an authorized run.
 */
import assert from 'node:assert/strict'
import { readFileSync, writeFileSync } from 'node:fs'
import { createHash } from 'node:crypto'
import { Store } from '../../src/store.ts'
import { Engine } from '../../src/engine.ts'
import { createApp } from '../../src/api.ts'
import { memoryChannels, sourceHash } from './log-channel-memory-harness.mjs'

const root = new URL('../../', import.meta.url)
const paths = ['src/api.ts', 'src/store.ts', 'src/schema.ts', 'src/diagnostics/log-writer.ts',
  'src/diagnostics/format.ts', 'src/memory/recovery/ledger.ts', 'src/server/owner-runtime.ts']
const sourceHashes = Object.fromEntries(paths.map(p => [p,
  createHash('sha256').update(readFileSync(new URL(p, root))).digest('hex')]))
assert.equal(sourceHashes['src/diagnostics/log-writer.ts'], sourceHash)
const clone = value => JSON.parse(JSON.stringify(value))
const makeStore = channels => Object.assign(Object.create(Store.prototype), {
  logs: [], logBytes: [], retainedLogBytes: 0, evictedLogs: 0, oversizedLogs: 0,
  unserializableLogs: 0, loggingNoticeAt: -Infinity, loggingInitializationFailure: false,
  loggingClosed: false, loggingShutdownComplete: null, closedLogRejections: 0,
  ...channels,
})
const event = { id: '11111111-1111-4111-8111-111111111111', at: '2026-11-09T00:00:00.123Z' }
const interactions = []
{
  // No logger construction, no acquired stdout/file, and no asynchronous owner.
  const store = makeStore(), before = clone(store.loggingStatus)
  const result = await store.closeLogs(), after = clone(store.loggingStatus)
  assert.equal(before.stdout, null); assert.equal(before.file, null)
  assert.equal(after.shutdownComplete, true); assert.deepEqual(result, [])
  interactions.push({ name: 'unused-store-is-lazy-and-clean-close-is-noop', before, result, after })
}
for (const [name, original, override] of [['priority-original-info', 'info', 'error'], ['priority-original-warn', 'warn', 'info']]) {
  const f = memoryChannels(), c = new f.LogChannel({ maxBytes: 1024, maxRecords: 8 })
  const channel = { enqueue: (...args) => c.enqueue(...args), get status() { return c.status },
    close: () => c.flush(5000, true), closeSync: n => c.closeSync(n) }
  const store = makeStore({ stdoutLogs: channel })
  for (let i = 0; i < 7; i++) c.enqueue('x')
  store.log(original, 'synthetic', 'priority', { ...event, level: override })
  const before = clone(store.loggingStatus), result = await f.settle(store.closeLogs())
  assert.equal(before.stdout.acceptedRecords, original === 'warn' ? 8 : 7)
  interactions.push({ name, before, result, after: clone(store.loggingStatus) }); f.finishExits()
}
{
  const f = memoryChannels(), stdoutLogs = f.acquireLogChannel(), store = makeStore({ stdoutLogs })
  store.log('info', 'synthetic', 'ordinary', event)
  const firstClose = await f.settle(store.closeLogs())
  store.log('warn', 'synthetic', 'after-close', event)
  const afterLateLog = clone(store.loggingStatus)
  const secondClose = await f.settle(store.closeLogs())
  assert.equal(afterLateLog.closedRejectedRecords, 1)
  // The source updates shutdownComplete at close, not in its later log method.
  assert.equal(afterLateLog.shutdownComplete, true)
  assert.equal(store.loggingStatus.shutdownComplete, false)
  interactions.push({ name: 'late-log-retained-and-rechecked-shutdown', firstClose, afterLateLog, secondClose, after: clone(store.loggingStatus) })
  f.finishExits()
}
{
  const f = memoryChannels(), stdoutLogs = f.acquireLogChannel(), store = makeStore({ stdoutLogs })
  store.log('warn', 'synthetic', 'non-json', { ...event, counts: { synthetic: 1n } })
  const before = clone(store.loggingStatus), result = await f.settle(store.closeLogs())
  assert.equal(before.unserializableRecords, 1); assert.equal(before.stdout.acceptedRecords, 0)
  assert.equal(store.loggingStatus.shutdownComplete, false)
  interactions.push({ name: 'unserializable-is-not-admitted', before, result, after: clone(store.loggingStatus) }); f.finishExits()
}
{
  const f = memoryChannels(), stdoutLogs = f.acquireLogChannel()
  const store = makeStore({ stdoutLogs, fileLogs: { enqueue() { throw new Error('synthetic-init-failure') },
    status: stdoutLogs.status, close: async () => undefined } })
  store.log('info', 'synthetic', 'second-channel-throws', event)
  const before = clone(store.loggingStatus), result = await f.settle(store.closeLogs())
  assert.equal(before.initializationFailed, true); assert.equal(before.stdout.acceptedRecords, 1)
  assert.equal(store.loggingStatus.shutdownComplete, false)
  interactions.push({ name: 'initialization-failure-does-not-undo-stdout', before, result, after: clone(store.loggingStatus) }); f.finishExits()
}
{
  const events = []; let release
  const barrier = new Promise(resolve => { release = resolve })
  const store = makeStore({ fileLogs: { close: async () => { events.push('file-start'); await barrier; events.push('file-end'); return { complete: true } } },
    stdoutLogs: { close: async () => { events.push('stdout-reject'); throw new Error('synthetic-close-failure') } } })
  let settled = false
  const closing = store.closeLogs().then(() => { throw new Error('must reject') }, e => { settled = true; events.push('store-reject'); assert.equal(e.message, 'synthetic-close-failure') })
  await Promise.resolve(); await Promise.resolve(); assert.equal(settled, false)
  release(); await closing
  assert.equal(store.loggingShutdownComplete, false)
  assert.deepEqual(events, ['file-start', 'stdout-reject', 'file-end', 'store-reject'])
  interactions.push({ name: 'close-allsettled-drains-other-sink-before-error', events, shutdownComplete: store.loggingShutdownComplete })
}

class ReadinessStore extends Store {
  fail = false
  failStatus = false
  assertRecoveryReady() { if (this.fail) throw new Error('synthetic-readiness-failure'); return super.assertRecoveryReady() }
  get loggingStatus() { if (this.failStatus) throw new Error('synthetic-status-failure'); return super.loggingStatus }
}
const specs = [
  { name: 'ready' }, { name: 'head', method: 'HEAD' },
  { name: 'query-ignored', path: '/api/logs/status?clear=true&limit=1' },
  { name: 'bad-auth', badAuth: true }, { name: 'no-auth', noAuth: true },
  { name: 'head-bad-auth', method: 'HEAD', badAuth: true },
  { name: 'missing-token', noToken: true }, { name: 'head-missing-token', method: 'HEAD', noToken: true },
  { name: 'post', method: 'POST' }, { name: 'put', method: 'PUT' },
  { name: 'delete', method: 'DELETE' }, { name: 'options', method: 'OPTIONS' },
  { name: 'trailing', path: '/api/logs/status/' },
  { name: 'child', path: '/api/logs/status/clear' },
  { name: 'encoded', path: '/api/logs/%73tatus' },
  { name: 'readiness-unavailable', failure: true },
  { name: 'head-unavailable', failure: true, method: 'HEAD' },
  { name: 'getter-unavailable', getterFailure: true },
  { name: 'head-getter-unavailable', getterFailure: true, method: 'HEAD' },
  { name: 'degraded-notice-on-read', degraded: true },
  { name: 'degraded-notice-on-head', degraded: true, method: 'HEAD' },
  { name: 'degraded-auth-does-not-read', degraded: true, badAuth: true },
  { name: 'closed', closed: true },
  { name: 'independent-file-and-stdout', file: true },
]
const cases = []
for (const spec of specs) {
  const f = memoryChannels(), stdout = f.acquireLogChannel()
  const store = new ReadinessStore(':memory:', undefined, { stdout })
  if (spec.file) store.fileLogs = f.acquireLogChannel('/synthetic-log-directory')
  const engine = new Engine(store)
  try {
    if (spec.degraded) stdout.enqueue('x'.repeat(1024 * 1024 + 1), true)
    if (spec.closed) await f.settle(store.closeLogs())
    const before = store.logs.length
    const app = createApp(store, { engine, token: spec.noToken ? '' : 'synthetic-token' })
    const headers = { 'X-Request-ID': 'synthetic-request' }
    if (!spec.noAuth) headers.Authorization = 'Bearer ' + (spec.badAuth ? 'wrong' : 'synthetic-token')
    const path = spec.path ?? '/api/logs/status', method = spec.method ?? 'GET'
    store.fail = !!spec.failure; store.failStatus = !!spec.getterFailure
    const response = await app.request(path, { method, headers })
    const rawText = await response.text()
    const text = rawText.replace(/"requestId":"[^"]+"/g, '"requestId":"<request-id>"')
    const requestId = response.headers.get('x-request-id')
    assert.match(requestId, /^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/)
    assert.notEqual(requestId, headers['X-Request-ID'])
    const timing = response.headers.get('server-timing')
    let timingShape = null
    if (timing !== null) {
      assert.match(timing, /^recovery;dur=\d+\.\d{2}, handler;dur=\d+\.\d{2}, total;dur=\d+\.\d{2}$/)
      for (const match of timing.matchAll(/;dur=(\d+\.\d{2})/g)) assert.ok(Number.isFinite(Number(match[1])) && Number(match[1]) >= 0)
      timingShape = timing.replace(/;dur=\d+\.\d{2}/g, ';dur=<duration>')
    }
    const errorRequestId = /"requestId":"([^"]+)"/.exec(rawText)?.[1] ?? null
    cases.push({ name: spec.name, path, method, status: response.status,
      requestIdShape: '<uuid-v4>', callerRequestIdIgnored: requestId !== headers['X-Request-ID'],
      errorRequestIdMatchesHeader: errorRequestId === null ? null : errorRequestId === requestId,
      serverTiming: timingShape,
      responseHeaders: Object.fromEntries(['content-type', 'cache-control', 'x-content-type-options'].map(k => [k, response.headers.get(k)])) })
  } finally {
    store.fail = false; store.failStatus = false
    await engine.dispose(); await f.settle(store.closeLogs()); store.close(); f.finishExits()
  }
}
const output = JSON.stringify({ sourceHashes, cases }, null, 2) + '\n'
const file = new URL('../internal/httpapi/testdata/log-status-headers-source.json', import.meta.url)
if (process.argv.includes('--check')) assert.equal(readFileSync(file, 'utf8'), output)
else writeFileSync(file, output)
console.log(`PASS ${cases.length} supplemental actual in-memory log status header cases`)
