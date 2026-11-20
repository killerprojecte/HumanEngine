import assert from 'node:assert/strict'
import { createHash } from 'node:crypto'
import { readFileSync, writeFileSync } from 'node:fs'
import { EmbeddingIndex } from '../../src/embedding-index.ts'
import { MemoryService } from '../../src/memory.ts'
import { ModelProtocolRegistry } from '../../src/model-protocols.ts'
import { Router } from '../../src/models.ts'
import { Store } from '../../src/store.ts'

const clock = 1800000000000
let now = clock
const config = {
  providers: [{ id: 'p', name: 'Synthetic', protocol: 'fixture:embed', baseUrl: 'http://127.0.0.1/v1',
    credentialEnv: 'HUMANENGINE_EMBEDDING_STORAGE_FIXTURE' }],
  models: [{ id: 'm', displayName: 'Synthetic', providerId: 'p', apiModelName: 'local',
    capabilities: ['embedding'], embeddingBatchSize: 1, embeddingDimensions: 2 }],
  routes: [{ role: 'embedding', orderedModelIds: ['m'], timeoutMs: 10000 }],
}
process.env.HUMANENGINE_EMBEDDING_STORAGE_FIXTURE = 'synthetic-local-only'
const store = new Store(':memory:')
const memory = new MemoryService(store)
const protocols = new ModelProtocolRegistry()
const scene = { id: 's', adapterId: 'fixture', accountId: 'fixture', platformSceneId: 's',
  name: 'Synthetic', kind: 'group', learningEnabled: true, participationEnabled: false, proactiveEnabled: false }
store.saveScene(scene)
store.set('models', config)
const inputs = ['Synthetic older record', 'Synthetic newer record']
const records = inputs.map((content) => memory.create({ scope: { kind: 'scene', sceneId: 's' }, kind: 'fact', content }))
const aliases = new Map(records.map(({ id }, index) => [id, 'memory-' + index]))
const controls = []
const notifications = new EventTarget()
let calls = 0
const hash = (value) => createHash('sha256').update(JSON.stringify(value)).digest('hex')
protocols.register('fixture:embed', 'Synthetic', {
  complete: async () => { throw new Error('unexpected_chat_replay') },
  embeddings: async (_ctx, contents) => {
    const call = ++calls
    let resolve
    const waiting = new Promise((done) => { resolve = done })
    controls[call] = { resolve, contents }
    notifications.dispatchEvent(new Event('call-' + call))
    await waiting
    return { vectors: contents.map(() => [1, 0]), usage: { inputTokens: 1, outputTokens: 0, totalTokens: 1 } }
  },
}, new AbortController().signal)
const waitForCall = (call) => controls[call] ? Promise.resolve()
  : new Promise((done) => notifications.addEventListener('call-' + call, done, { once: true }))
const router = (cfg) => new Router(cfg, undefined, undefined, protocols)
const first = new EmbeddingIndex(store, memory, router, { now: () => now })
const { selected } = first.status()
assert.ok(selected)
const previous = {
  generationId: 'prior-generation', encoderRevision: selected.encoderRevision,
  modelId: 'm', dimensions: 2, spaceId: memory.registerSpace({
    providerId: 'p', modelId: 'm', apiModelName: 'local', deployment: config.providers[0].baseUrl,
    dimensions: 2, preprocessing: 'text-v1', normalized: false,
    generation: 'prior-generation', encoderRevision: selected.encoderRevision,
  }),
}
store.set('embedding.active', previous)
const writes = []
const set = store.set.bind(store)
store.set = (key, value) => {
  if (key === 'embedding.rebuild') writes.push(structuredClone(value))
  return set(key, value)
}
const job = () => store.get('embedding.rebuild', undefined)
const cases = []
const observations = []
let peer
const observe = (name) => observations.push({ name, now, calls, job: structuredClone(job()),
  checkpoints: store.db.prepare('SELECT job_id,memory_id,content_hash FROM embedding_rebuild_items ORDER BY memory_id').all(),
  active: store.get('embedding.active', undefined).generationId })
function rejects(name, code, fn) {
  assert.throws(fn, { message: code })
  cases.push({ name, error: code })
}
try {
  rejects('wrong-latest-before-start', 'embedding_job_changed', () => first.start('not-latest', selected.encoderRevision))
  rejects('wrong-encoder-before-start', 'embedding_config_changed', () => first.start(null, 'not-current'))
  first.start(null, selected.encoderRevision)
  await waitForCall(1)
  observe('submitted-paid-request-uncertain')
  const id = job().id
  const original = structuredClone(job())
  rejects('busy-local-owner', 'embedding_rebuild_busy', () => first.start(id, selected.encoderRevision))
  rejects('pause-stale-job', 'embedding_job_changed', () => first.pause('not-latest'))
  now += 30_001
  peer = new EmbeddingIndex(store, memory, router, { now: () => now })
  observe('expired-restart-no-replay')
  rejects('unconfirmed-resume', 'embedding_retry_confirmation_required', () => peer.resume(false, id))
  peer.resume(true, id)
  await waitForCall(2)
  observe('resumed-attempt-uncertain')
  assert.notEqual(job().attemptId, original.attemptId)
  assert.notEqual(job().ownerId, original.ownerId)
  controls[1].resolve()
  await first.settled()
  await first.dispose()
  observe('stale-attempt-cannot-advance-or-pause-peer')
  assert.equal(observations.at(-1).checkpoints.length, 0)
  now += 1000 // The committed checkpoint renews the lease at the new clock.
  controls[2].resolve()
  await waitForCall(3)
  observe('committed-checkpoint-next-request-uncertain')
  assert.equal(observations.at(-1).checkpoints.length, 1)
  peer.pause(id)
  observe('paused-owned-attempt')
  controls[3].resolve()
  await peer.settled()
  rejects('cancel-stale-job', 'embedding_job_changed', () => peer.cancel('not-latest'))
  peer.cancel(id)
  observe('cancelled-retains-committed-checkpoint')
  rejects('resume-cancelled-job', 'embedding_rebuild_not_resumable', () => peer.resume(true, id))
  assert.deepEqual(writes.slice(0, 7).map((w) => [w.state, w.requiresRetryConfirmation, w.processed]), [
    ['running', false, 0], ['running', true, 0], ['paused', true, 0], ['running', false, 0],
    ['running', true, 0], ['running', false, 1], ['running', true, 1],
  ])
  assert.ok(observations.every((o) => o.active === 'prior-generation'))
  const stableJob = (value) => Object.fromEntries(Object.entries(value).map(([key, value]) => {
    if (key === 'id') value = 'job-0'
    if (key === 'ownerId') value = value === original.ownerId ? 'owner-0' : 'owner-1'
    if (key === 'attemptId') value = value === original.attemptId ? 'attempt-0' : 'attempt-1'
    if (key === 'spaceId') value = 'staging-space'
    return [key, value]
  }))
  const snapshots = observations.map((o) => ({ ...o, job: stableJob(o.job), checkpoints: o.checkpoints.map(({ memory_id, content_hash }) => {
    const row = records.find(({ id }) => id === memory_id)
    assert.equal(content_hash, hash([row.id, row.content]))
    const id = aliases.get(row.id)
    return { job_id: 'job-0', memory_id: id, content_hash: hash([id, row.content]) }
  }) }))
  const tables = Object.fromEntries(['embedding_rebuild_items'].map((table) => [table,
    store.db.prepare("SELECT sql FROM sqlite_master WHERE type='table' AND name=?").get(table).sql]))
  const sourceHashes = Object.fromEntries(['src/embedding-index.ts'].map((file) => [
    file, createHash('sha256').update(readFileSync(new URL('../../' + file, import.meta.url))).digest('hex'),
  ]))
  const wire = JSON.stringify({ node: process.version, sourceHashes, clock, config, inputs, previous,
    tables, cases, snapshots, writes: writes.map(stableJob) }, null, 2) + '\n'
  const target = new URL('../internal/learningowner/testdata/embedding-rebuild-storage-source.json', import.meta.url)
  if (process.argv.includes('--check')) assert.equal(readFileSync(target, 'utf8'), wire)
  else writeFileSync(target, wire)
  console.log(`PASS ${snapshots.length} embedding rebuild source snapshots and ${cases.length} rejected transitions`)
} finally {
  for (const gate of controls) gate?.resolve()
  await peer?.dispose()
  await first.dispose()
  store.close()
}
