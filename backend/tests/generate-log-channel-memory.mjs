/** Frozen candidate, NOT executed at creation. Synthetic boundary differential. */
import assert from 'node:assert/strict'
import { readFileSync, writeFileSync } from 'node:fs'
import { memoryChannels, sourceHash } from './log-channel-memory-harness.mjs'

const cases = []
async function scenario(name, run) {
  const f = memoryChannels()
  const value = await run(f)
  f.finishExits()
  cases.push({ name, ...value })
}
await scenario('default-limits-and-option-validation', async f => {
  const out = new f.LogChannel(), file = new f.LogChannel({ directory: '/logs' })
  const initial = [f.status(out), f.status(file)]
  for (const options of [{ maxBytes: 1023 }, { maxBytes: 67108865 }, { maxBytes: 1024.5 },
    { maxRecords: 7 }, { maxRecords: 65537 }, { maxRecords: NaN }]) {
    assert.throws(() => new f.LogChannel(options), /invalid_log_queue_limits/)
  }
  assert.equal(f.workers.length, 2)
  const closed = await Promise.all([f.settle(out.flush(5000, true)), f.settle(file.flush(5000, true))])
  assert.equal(closed[0].durable, false); assert.equal(closed[1].durable, true)
  return { initial, closed }
})
await scenario('record-reserve-sticky-rejection-whole-fifo', async f => {
  const c = new f.LogChannel({ directory: '/logs', maxBytes: 1024, maxRecords: 8 })
  const admission = []
  for (let i = 0; i < 8; i++) admission.push(c.enqueue(`${i}\n`))
  admission.push(c.enqueue('priority\n', true), c.enqueue('x'.repeat(1025), true))
  const pending = f.status(c), result = await f.settle(c.flush(5000, true))
  assert.deepEqual(admission, [true, true, true, true, true, true, true, false, true, false])
  assert.equal(result.complete, false); assert.equal(result.status.writtenRecords, 8)
  return { admission, pending, result, bytes: f.bytes('/logs/runtime.jsonl').toString() }
})
await scenario('exact-byte-reserve-unicode-empty-and-allocation-failure', async f => {
  const c = new f.LogChannel({ maxBytes: 1024, maxRecords: 8 })
  const admission = [c.enqueue('🚀'.repeat(224)), c.enqueue('a'), c.enqueue('x'.repeat(128), true)]
  admission.push(c.enqueue('', true))
  const full = f.status(c)
  await f.settle(c.flush())
  f.allocationFailure = true; admission.push(c.enqueue('漢字🚀\ud800\n', true))
  f.allocationFailure = false
  admission.push(c.enqueue('漢字🚀\ud800\n', true))
  const result = await f.settle(c.flush(5000, true))
  assert.equal(full.pendingBytes, 1024); assert.equal(result.status.lastFailure, 'encode_failed')
  assert.equal(result.status.writtenRecords, 4)
  return { admission, full, result, bytesHex: f.bytes().toString('hex') }
})
await scenario('stdout-partial-eagain-retries-only-unwritten-suffix', async f => {
  const c = new f.LogChannel()
  f.plans.set('<stdout>', [{ n: 2 }, { code: 'EAGAIN' }, { code: 'EWOULDBLOCK' }, { n: 3 }, { code: 'EAGAIN' }])
  const during = []; f.onBackoff = () => during.push(f.status(c))
  c.enqueue('prefix:🚀\n')
  const result = await f.settle(c.flush(5000, true))
  assert.equal(f.bytes().toString(), 'prefix:🚀\n')
  assert.deepEqual(f.backoffs, [2, 4, 2])
  assert(during.every(s => s.backpressured && s.pendingRecords === 1 && s.failedRecords === 0))
  return { during, backoffs: f.backoffs, result, bytes: f.bytes().toString() }
})
await scenario('ambiguous-partial-error-is-terminal-no-replay', async f => {
  const c = new f.LogChannel()
  f.plans.set('<stdout>', [{ n: 3 }, { code: 'EIO' }])
  c.enqueue('abcdef\n'); c.enqueue('second\n')
  await Promise.all(f.workers.map(w => w.ready)); await Promise.resolve(); f.pumpOne()
  const beforeReject = f.status(c), admitted = c.enqueue('after\n')
  const result = await f.settle(c.flush(5000, true))
  assert.equal(admitted, false); assert.equal(f.bytes().toString(), 'abc')
  assert.equal(result.status.failedRecords, 2); assert.equal(result.status.unconfirmedRecords, 0)
  return { beforeReject, admitted, result, operations: f.operations }
})
await scenario('fsync-failure-does-not-invent-failed-record', async f => {
  const c = new f.LogChannel({ directory: '/logs' })
  f.fsyncFailure = '/logs/runtime.jsonl'; c.enqueue('one\n')
  const result = await f.settle(c.flush(5000, true))
  assert.equal(result.status.writtenRecords, 1); assert.equal(result.status.failedRecords, 0)
  assert.equal(result.status.lastFailure, 'sink_write_failed'); assert.equal(result.complete, false)
  return { result }
})
await scenario('zero-write-is-terminal', async f => {
  const c = new f.LogChannel(); f.plans.set('<stdout>', [{ n: 0 }]); c.enqueue('one\n')
  const result = await f.settle(c.flush(5000, true))
  assert.equal(result.status.failedRecords, 1); return { result }
})
await scenario('file-open-failure-is-sink-failure-not-construction-failure', async f => {
  const c = new f.LogChannel({ directory: '/logs' }); f.openFailure = '/logs/runtime.jsonl'
  c.enqueue('one\n'); c.enqueue('two\n')
  const result = await f.settle(c.flush(5000, true))
  assert.equal(result.status.failedRecords, 2); assert.equal(result.status.lastFailure, 'sink_write_failed')
  return { result, operations: f.operations }
})
await scenario('sync-close-drains-existing-batch-and-fsync', async f => {
  const c = new f.LogChannel({ directory: '/logs' }); c.enqueue('one\n'); c.enqueue('two\n')
  const result = c.closeSync(5000)
  assert.equal(result.complete, true); assert.equal(result.durable, true)
  assert.equal(result.status.writtenRecords, 2)
  return { result, operations: f.operations }
})
for (const event of ['error', 'exit']) await scenario(`worker-${event}-is-unconfirmed-and-sanitized`, async f => {
  const c = new f.LogChannel(); c.enqueue('one\n')
  if (event === 'exit') f.workers[0].confirmExit(7)
  else f.workers[0].emit('error', new Error('synthetic-private-path-must-not-leak'))
  const result = await f.settle(c.flush())
  assert.equal(result.status.unconfirmedRecords, 1)
  assert.equal(result.status.lastFailure, event === 'error' ? 'worker_failed' : 'worker_exited')
  assert(!JSON.stringify(result).includes('private'))
  return { result }
})
await scenario('stop-during-backpressure-never-replays-written-prefix', async f => {
  const c = new f.LogChannel(); c.enqueue('abcdef\n')
  f.plans.set('<stdout>', [{ n: 2 }, { code: 'EAGAIN' }])
  let timeoutResult
  f.onBackoff = () => { timeoutResult = c.flushSync(0, true) }
  await Promise.all(f.workers.map(w => w.ready)); await Promise.resolve(); f.pumpOne()
  const after = f.status(c)
  assert.equal(f.bytes().toString(), 'ab'); assert.equal(after.failedRecords, 1)
  assert.equal(timeoutResult.status.unconfirmedRecords, 1); assert.equal(after.unconfirmedRecords, 0)
  return { timeoutResult, after, bytes: f.bytes().toString() }
})
for (const kind of ['records', 'flush']) await scenario(`${kind}-handoff-failure-is-unconfirmed`, async f => {
  const c = new f.LogChannel(); f.workers[0].failKind = kind
  c.enqueue('one\n'); c.enqueue('two\n')
  const result = await f.settle(c.flush(5000, kind === 'records'))
  assert.equal(result.status.unconfirmedRecords, 2); assert.equal(result.status.pendingRecords, 0)
  assert.equal(result.status.lastFailure, kind === 'records' ? 'enqueue_failed' : 'flush_failed')
  return { result, closedBeforeExit: c.closed }
})
await scenario('concurrent-flush-targets-own-later-admissions', async f => {
  const c = new f.LogChannel({ directory: '/logs' }); c.enqueue('one\n')
  const first = c.flush(); c.enqueue('two\n'); const second = c.flush()
  const results = await f.settle(Promise.all([first, second]))
  assert.equal(results[0].throughAcceptedRecords, 1); assert.equal(results[1].throughAcceptedRecords, 2)
  assert.equal(results[1].flushedAcceptedRecords, 2)
  const close = await f.settle(c.flush(5000, true)); return { results, close }
})
await scenario('shorter-concurrent-deadline-abandons-unconfirmed', async f => {
  const c = new f.LogChannel(); c.enqueue('held\n')
  const first = c.flush(1000), short = c.flush(1, true)
  // Do not deliver any worker messages. These are real source timer branches.
  const results = await Promise.all([first, short])
  assert(results.every(r => !r.complete && r.status.lastFailure === 'flush_timeout'))
  return { results }
})
await scenario('file-drains-independently-of-held-stdout', async f => {
  const out = new f.LogChannel(), file = new f.LogChannel({ directory: '/logs' })
  out.enqueue('held\n'); file.enqueue('durable\n')
  const fileResult = await f.settle(file.flush(5000, true), w => !!w.directory)
  const held = f.status(out), outResult = out.flushSync(0, true)
  assert.equal(fileResult.durable, true); assert.equal(held.pendingRecords, 1)
  assert.equal(outResult.status.unconfirmedRecords, 1)
  return { fileResult, held, outResult }
})
await scenario('released-lease-is-local-and-retains-generation-view', async f => {
  const a = f.acquireLogChannel('/logs'), b = f.acquireLogChannel('/logs/.')
  a.enqueue('one\n'); const aClosed = await f.settle(a.close())
  const rejected = a.enqueue('after-release\n'); b.enqueue('two\n')
  const bClosed = await f.settle(b.close())
  assert.equal(rejected, false); assert.equal(bClosed.complete, true)
  assert.equal(a.closeSync(), undefined)
  const repeated = await f.settle(a.close())
  assert.equal(repeated.complete, false); assert.equal(a.status.lastFailure, 'lease_released')
  return { aClosed, bClosed, aStatus: f.status(a), repeated, workers: f.workers.length }
})
await scenario('retiring-owner-blocks-replacement-until-close-ack', async f => {
  const a = f.acquireLogChannel('/logs'); a.enqueue('one\n'); const closing = a.close()
  const b = f.acquireLogChannel('/logs/.'); const whileClosing = b.enqueue('rejected\n')
  assert.equal(f.workers.length, 1)
  const firstClose = await f.settle(closing)
  // Closed ack has closed fd, but synthetic exit has intentionally not fired.
  const next = b.enqueue('three\n'); assert.equal(f.workers.length, 2)
  const result = await f.settle(b.close())
  assert.equal(whileClosing, false); assert.equal(next, true)
  assert.equal(a.status.acceptedRecords, 1); assert.equal(result.status.acceptedRecords, 2)
  assert.equal(result.complete, false)
  return { whileClosing, next, firstClose, result, retired: f.status(a), operations: f.operations }
})
await scenario('unconfirmed-owner-stays-registered-until-exit', async f => {
  const a = f.acquireLogChannel('/logs'); a.enqueue('one\n')
  const stopped = a.closeSync(0)
  const b = f.acquireLogChannel('/logs'); assert.equal(b.enqueue('blocked\n'), false)
  assert.equal(f.workers.length, 1); f.workers[0].confirmExit(1)
  assert.equal(b.enqueue('new\n'), true)
  const result = await f.settle(b.close())
  assert.equal(result.status.unconfirmedRecords, 1); assert.equal(result.complete, false)
  return { stopped, result, retired: f.status(a), workers: f.workers.length }
})
await scenario('zero-reference-confirmed-exit-creates-clean-owner', async f => {
  const a = f.acquireLogChannel('/logs'); a.enqueue('old\n'); a.closeSync(0)
  f.workers[0].confirmExit(1)
  const b = f.acquireLogChannel('/logs'); b.enqueue('new\n')
  const result = await f.settle(b.close())
  assert.equal(result.complete, true); assert.equal(result.status.acceptedRecords, 1)
  return { result, retired: f.status(a) }
})
await scenario('canonical-existing-parent-shares-not-lexical-path', async f => {
  f.aliases.set('/alias', '/real'); f.directories.add('/real')
  const a = f.acquireLogChannel('/alias/new'), b = f.acquireLogChannel('/real/new/.')
  assert.equal(f.workers.length, 1)
  a.enqueue('shared\n'); await f.settle(a.close()); const result = await f.settle(b.close())
  return { result, workers: f.workers.length, operations: f.operations }
})
await scenario('rotation-strict-threshold-and-interrupted-tail', async f => {
  f.files.set('/logs/runtime.jsonl', Buffer.from('interrupted:'))
  const c = new f.LogChannel({ directory: '/logs', rotateBytes: 4 })
  c.enqueue('123\n'); c.enqueue('x\n'); c.enqueue('z\n')
  const result = await f.settle(c.flush(5000, true))
  const files = Object.fromEntries([...f.files].map(([p, b]) => [p, b.toString()]))
  assert.equal(files['/logs/runtime.jsonl.2'], 'interrupted:')
  assert.equal(files['/logs/runtime.jsonl.1'], '123\nx\n')
  assert.equal(files['/logs/runtime.jsonl'], 'z\n')
  return { result, files, operations: f.operations }
})
await scenario('clean-close-then-enqueue-is-degraded', async f => {
  const c = new f.LogChannel(); c.enqueue('one\n')
  const closed = await f.settle(c.flush(5000, true)); assert.equal(c.enqueue('no\n'), false)
  const again = await f.settle(c.flush())
  assert.equal(again.status.lastFailure, 'sink_closed'); assert.equal(again.complete, false)
  return { closed, again }
})
await scenario('concurrent-flush-resamples-after-wait', async f => {
  const c = new f.LogChannel({ directory: '/logs' })
  c.enqueue('one\n'); const first = c.flush()
  c.enqueue('two\n'); const second = c.flush()
  c.enqueue('three\n')
  const results = await f.settle(Promise.all([first, second]))
  assert.equal(results[0].throughAcceptedRecords, 1)
  assert.equal(results[1].throughAcceptedRecords, 2)
  assert.equal(results[1].flushedAcceptedRecords, 3)
  const close = await f.settle(c.flush(5000, true))
  return { results, close, operations: f.operations }
})
const output = JSON.stringify({ sourceHash, provenance: 'actual LogChannel and workerSource; synthetic OS boundaries', cases }, null, 2) + '\n'
const outputURL = new URL('../internal/observability/log-channel-memory-source.json', import.meta.url)
if (process.argv.includes('--check')) assert.equal(readFileSync(outputURL, 'utf8'), output)
else writeFileSync(outputURL, output)
console.log(`PASS ${cases.length} actual-source synthetic log channel cases`)
