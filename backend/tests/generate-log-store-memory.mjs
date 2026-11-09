/** Source Store oracle. The source class and real formatters execute; lease
 * status/results are explicit boundary INPUTS, not derived/fabricated delivery
 * counters. Physical delivery is covered by the independent channel25/native
 * descriptor tests. This fixture tests Store orchestration and byte projection.
 */
import assert from 'node:assert/strict'
import { readFileSync, writeFileSync } from 'node:fs'
import { createHash } from 'node:crypto'
import { sourceStore, sourceHashes, sliceHash, fixedID, fixedAt } from './log-store-memory-harness.mjs'
const root = new URL('../../', import.meta.url)
const channelFixture = readFileSync(new URL('backend/internal/observability/log-channel-memory-source.json', root))
const channelDefaults = JSON.parse(channelFixture).cases[0]
const clone = v => JSON.parse(JSON.stringify(v))
const digest = text => ({ bytes: Buffer.byteLength(text), sha256: createHash('sha256').update(text).digest('hex'), ...(text.length < 16384 ? { text } : {}) })
const sorted = v => Array.isArray(v) ? v.map(sorted) : v && typeof v === 'object' ? Object.fromEntries(Object.keys(v).sort().map(k => [k, sorted(v[k])])) : v
const emit = (fields = {}, level = 'info', extra = {}) => ({ kind: 'emit', level, fields, ...extra })
const status = name => ({ kind: 'status', name })
const close = { kind: 'close' }, sync = { kind: 'syncClose' }
const specs = [
 { name: 'unused-store-is-lazy', path: ':memory:', steps: [status('unused'), close, status('closed'), sync] },
 { name: 'lazy-file-default-directory', path: '/synthetic/data/main.sqlite', steps: [status('unused'), emit(), close, status('closed'), sync] },
 { name: 'environment-db-and-directory', env: { HUMANENGINE_DB: '/env/db.sqlite', HUMANENGINE_LOG_DIR: '/custom/logs' }, steps: [emit(), close, status('closed')] },
 { name: 'memory-ignores-log-directory', path: ':memory:', env: { HUMANENGINE_LOG_DIR: '/ignored' }, steps: [emit(), close, status('closed')] },
 { name: 'empty-environment-uses-defaults', env: { HUMANENGINE_DB: '', HUMANENGINE_LOG_DIR: '' }, steps: [status('unused'), close] },
 { name: 'exact-readable-dual-sink', path: '/synthetic/db.sqlite', steps: [emit({ batchId: 'b', counts: { sentSegments: 2 }, role: 'thinker', traceId: 't', turnId: 'u' }, 'warn'), close, status('closed')] },
 { name: 'context-overrides-and-original-info-priority', path: '/synthetic/db.sqlite', flow: { requestId: 'r', traceId: 'flow', stage: 'prepare' }, steps: [emit({ at: 'overridden', code: 'custom-code', level: 'error', message: 'discarded', module: 'custom-module', traceId: 'context' }), close, status('closed')] },
 { name: 'original-warn-priority-with-info-row', path: ':memory:', steps: [emit({ level: 'info' }, 'warn'), close, status('closed')] },
 { name: 'unicode-full-uppercase-prefix', path: ':memory:', steps: [emit({ level: 'straße', traceId: ['汉', '🙂'], turnId: false, batchId: 42 }), close, status('closed')] },
 { name: 'non-string-level-is-admission-failure', path: '/synthetic/db.sqlite', steps: [emit({ level: 7 }), status('failed'), close, status('closed')] },
 { name: 'nonfinite-and-surrogate', path: ':memory:', special: 'numbers', steps: [emit(), close, status('closed')] },
 { name: 'serialization-once-for-both-sinks', path: '/synthetic/db.sqlite', special: 'toJSON', steps: [emit(), close, status('closed')] },
 { name: 'unserializable-bigint-before-acquire', path: '/synthetic/db.sqlite', special: 'bigint', steps: [emit(), status('failed'), close, status('closed')] },
 { name: 'unserializable-cycle-before-acquire', path: '/synthetic/db.sqlite', special: 'cycle', steps: [emit(), status('failed'), close, status('closed')] },
 { name: 'file-acquire-failure-keeps-stdout-without-enqueue', path: '/synthetic/db.sqlite', behavior: { file: { acquireThrow: true } }, steps: [emit(), status('failed'), close, status('closed')] },
 { name: 'stdout-acquire-failure-skips-file', path: '/synthetic/db.sqlite', behavior: { stdout: { acquireThrow: true } }, steps: [emit(), status('failed'), close, status('closed')] },
 { name: 'file-enqueue-failure-keeps-stdout-admission', path: '/synthetic/db.sqlite', behavior: { file: { enqueueThrow: true } }, steps: [emit(), status('failed'), close, status('closed')] },
 { name: 'false-enqueue-is-not-initialization-failure', path: '/synthetic/db.sqlite', behavior: { stdout: { reject: true }, file: { reject: true } }, steps: [emit(), status('admitted'), close, status('closed')] },
 { name: 'closed-retention-and-sticky-shutdown', path: ':memory:', steps: [emit(), close, emit({}, 'warn'), status('late'), close, status('reclosed')] },
 { name: 'notice-throttled-minute-and-not-recursive', path: '/synthetic/db.sqlite', behavior: { stdout: { degraded: [3, 1] }, file: { degraded: [2, 4] } }, steps: [emit(), status('first'), {kind:'advance', millis:59999}, status('before-minute'), {kind:'advance', millis:1}, status('at-minute'), close, status('closed')] },
 { name: 'oversized-retention-alone-does-not-fail-close', path: ':memory:', special: 'oversized', steps: [emit(), status('oversized'), close, status('closed')] },
 { name: 'sync-close-shared-deadline-and-cleanup', path: '/synthetic/db.sqlite', behavior: { file: { syncAdvance: 3000 } }, steps: [emit(), sync, status('closed')] },
 { name: 'sync-close-error-does-not-skip-stdout-or-db', path: '/synthetic/db.sqlite', behavior: { file: { syncAdvance: 6000, syncThrow: true } }, steps: [emit(), sync, status('closed')] },
 { name: 'incomplete-close-remains-sticky', path: '/synthetic/db.sqlite', behavior: { file: { incomplete: true } }, steps: [emit(), close, status('closed'), sync, status('after-sync')] },
]
const cases = []
for (const spec of specs) {
 const operations = [], captures = [], checkpoints = [], results = [], serializationCalls = []
 let calls = 0, fixture
 const leases = new Map()
 const acquire = directory => {
  const kind = directory ? 'file' : 'stdout', behavior = spec.behavior?.[kind] ?? {}
  operations.push(`acquire-${kind}`)
  if (behavior.acquireThrow) throw new Error('synthetic acquire failure')
  const value = clone(channelDefaults.initial[kind === 'file' ? 1 : 0])
  if (behavior.degraded) { value.state = 'degraded'; [value.rejectedRecords, value.failedRecords] = behavior.degraded; value.lastFailure = 'sink_failed' }
  let released = false
  const result = { ...clone(channelDefaults.closed[kind === 'file' ? 1 : 0]), complete: !behavior.incomplete, status: value }
  const lease = {
   get status() { return value },
   enqueue(text, priority) {
    operations.push(`enqueue-${kind}`); captures.push({ sink: kind, priority, ...digest(text) })
    if (behavior.enqueueThrow) throw new Error('synthetic enqueue failure')
    return !behavior.reject
   },
   async close() { operations.push(`close-${kind}`); released = true; return result },
   closeSync(timeout) {
    operations.push(`sync-${kind}:${timeout}`)
    if (released) return undefined
    released = true
    fixture.advance(behavior.syncAdvance ?? 0)
    if (behavior.syncThrow) throw new Error('synthetic sync failure')
    return result
   },
  }
  leases.set(kind, lease); return lease
 }
 fixture = sourceStore({ acquire, path: spec.path, env: spec.env, flow: spec.flow, operations })
 const { store } = fixture
 const directory = fixture.directory()
 for (const step of spec.steps) {
  if (step.kind === 'emit') {
   let fields = sorted(step.fields)
   if (spec.special === 'numbers') fields = { nan: NaN, negativeInfinity: -Infinity, positiveInfinity: Infinity, surrogate: '\ud800' }
   if (spec.special === 'toJSON') fields = { custom: { toJSON() { calls++; return { owned: true } } } }
   if (spec.special === 'bigint') fields = { bad: 1n }
   if (spec.special === 'cycle') { const cycle = {}; cycle.self = cycle; fields = { bad: cycle } }
   if (spec.special === 'oversized') fields = { extra: 'x'.repeat(4 * 1024 * 1024) }
   store.log(step.level, 'synthetic', 'ordinary', fields)
   serializationCalls.push(calls)
  } else if (step.kind === 'advance') fixture.advance(step.millis)
  else if (step.kind === 'status') {
   const value = clone(store.loggingStatus)
   checkpoints.push({ name: step.name, status: value, retained: digest(JSON.stringify(store.logs.toReversed())), rowBytes: [...store.logBytes] })
  } else if (step.kind === 'close') results.push(clone(await store.closeLogs()))
  else if (step.kind === 'syncClose') store.close()
  else assert.fail(`unsupported step ${step.kind}`)
 }
 cases.push({ name: spec.name, spec, directory, operations, captures, checkpoints, results, serializationCalls })
}
// Both rejection directions are tested with actual Promise.allSettled behavior.
for (const failing of ['file','stdout']) {
 const operations = []; let release
 const gate = new Promise(resolve => { release = resolve })
 const fixture = sourceStore({ acquire() { throw new Error('unexpected acquisition') }, path: ':memory:', operations })
 for (const kind of ['file','stdout']) fixture.store[kind === 'file' ? 'fileLogs' : 'stdoutLogs'] = {
  async close() { operations.push(`${kind}-begin`); if (kind === failing) throw new Error('synthetic failure'); await gate; operations.push(`${kind}-end`); return { complete:true } }
 }
 let settled = false
 const closing = fixture.store.closeLogs().then(() => assert.fail('must reject'), () => { settled = true; operations.push('store-reject') })
 await Promise.resolve(); await Promise.resolve(); assert.equal(settled,false)
 release(); await closing
 cases.push({ name: `allsettled-${failing}-fails-other-held`, operations, shutdownComplete: fixture.store.loggingShutdownComplete })
}
assert.equal(cases.length, 26)
assert.equal(cases.find(c => c.name === 'serialization-once-for-both-sinks').serializationCalls[0],1)
assert.equal(cases.find(c => c.name === 'file-acquire-failure-keeps-stdout-without-enqueue').captures.length,0)
assert.equal(cases.find(c => c.name === 'oversized-retention-alone-does-not-fail-close').checkpoints.at(-1).status.shutdownComplete,true)
const output = JSON.stringify({ provenance:'Actual Store AST member bodies and actual formatters; explicit synthetic lease status/result inputs. No worker/fd proof.', sourceHashes, sliceHash, boundaryInputSHA256:createHash('sha256').update(channelFixture).digest('hex'), fixedID, fixedAt, cases },null,2)+'\n'
const destination = new URL('../internal/observability/log-store-memory-source.json',import.meta.url)
if (process.argv.includes('--check')) assert.equal(readFileSync(destination,'utf8'),output)
else writeFileSync(destination,output)
console.log(`PASS ${cases.length} actual-source Store boundary cases`)
