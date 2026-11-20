import assert from 'node:assert/strict'
import { createHash } from 'node:crypto'
import { readFileSync, writeFileSync } from 'node:fs'
import { createApp } from '../../src/api.ts'
import { Engine } from '../../src/engine.ts'
import { ModelProtocolError } from '../../src/model-protocols.ts'
import { Store } from '../../src/store.ts'

process.env.HUMANENGINE_EMBEDDING_HTTP_FIXTURE = 'synthetic-local-only'
const route = '/api/embedding-index'
const results = []
const scene = {
  id: 's', kind: 'group', adapterId: 'fixture', accountId: 'fixture', platformSceneId: 's',
  name: 'Synthetic', learningEnabled: true, participationEnabled: false, proactiveEnabled: false,
}
function models(apiModelName, dimensions = 2) {
  return {
    providers: [{ id: 'p', name: 'Synthetic', protocol: 'fixture:embed', baseUrl: 'http://127.0.0.1/v1',
      credentialEnv: 'HUMANENGINE_EMBEDDING_HTTP_FIXTURE' }],
    models: [{ id: 'm', displayName: 'Synthetic', providerId: 'p', apiModelName,
      capabilities: ['embedding'], embeddingBatchSize: 1,
      ...(dimensions === null ? {} : { embeddingDimensions: dimensions }) }],
    routes: [{ role: 'embedding', orderedModelIds: ['m'], timeoutMs: 10000 }],
  }
}
async function run(name, test) {
  const store = new Store(':memory:')
  store.saveScene(scene)
  store.set('models', models('one'))
  const engine = new Engine(store)
  const index = engine.embeddingIndex
  index.options.now = () => 1800000000000
  const app = createApp(store, { engine, token: 'synthetic-token' })
  const gates = [], events = new EventTarget(), calls = [], outcomes = [], aliases = new Map()
  let block = false, fault
  engine.protocols.register('fixture:embed', 'Synthetic', {
    complete: async () => { throw new Error('unexpected_replay') },
    embeddings: async (ctx, contents) => {
      const i = calls.length
      calls.push({ model: ctx.model.apiModelName, contents })
      if (fault) throw fault
      if (block) {
        await new Promise((resolve) => {
          gates[i] = resolve
          events.dispatchEvent(new Event('call-' + i))
        })
      }
      return { vectors: contents.map((text) => [text.length, 1]), usage: { inputTokens: contents.length, outputTokens: 0, totalTokens: contents.length } }
    },
  }, new AbortController().signal)
  const jobName = (id) => {
    if (id === null || id === undefined) return id
    if (!aliases.has(id)) aliases.set(id, 'generation-' + aliases.size)
    return aliases.get(id)
  }
  const state = (body) => {
    const { active, job } = body
    const knownGenerations = store.db.prepare("SELECT json_extract(value,'$.generation') AS id FROM vector_spaces WHERE json_extract(value,'$.generation') IS NOT NULL")
      .all().map(({ id }) => jobName(id)).sort()
    const priorSpace = store.get('embedding.previousSpace', '')
    return {
      state: body.state,
      active: active ? { generation: jobName(active.generationId), dimensions: active.dimensions } : null,
      job: job ? { id: jobName(job.id), state: job.state, error: job.error ?? null,
        uncertainty: job.requiresRetryConfirmation, processed: job.processed, total: job.total,
        expectedActive: jobName(job.expectedActive) ?? null } : null,
      coverage: body.coverage,
      retained: body.retained,
      prior: priorSpace ? jobName(JSON.parse(store.db.prepare('SELECT value FROM vector_spaces WHERE id=?').get(priorSpace).value).generation) : null,
      generations: knownGenerations,
      checkpoints: store.db.prepare('SELECT count(*) AS n FROM embedding_rebuild_items').get().n,
      calls: calls.length,
    }
  }
  async function request(label, action, input, expected = 200) {
    const response = await app.request('http://fixture.invalid' + route + (action ? '/' + action : ''), {
      method: action ? 'POST' : 'GET',
      headers: { Authorization: 'Bearer synthetic-token', 'X-Request-ID': 'synthetic-spoof',
        ...(action ? { 'Content-Type': 'application/json' } : {}) },
      ...(action ? { body: JSON.stringify(input) } : {}),
    })
    assert.equal(response.status, expected, label)
    assert.equal(response.headers.get('cache-control'), 'no-store')
    assert.match(response.headers.get('x-request-id'), /^[a-f0-9-]{36}$/u)
    const body = await response.json()
    outcomes.push({ label, action: action ?? 'status', status: response.status,
      body: response.status === 200 ? state(body) : body })
    return body
  }
  const status = (label) => request(label)
  const stored = () => store.get('embedding.rebuild', null)
  const expected = () => ({ expectedJobId: stored()?.id ?? null })
  const start = (label) => request(label, 'start', { ...expected(), expectedEncoderRevision: index.status().selected.encoderRevision })
  const add = (content, extra = {}) => engine.memory.create({ scope: { kind: 'scene', sceneId: 's' }, kind: 'fact', content, ...extra })
  const wait = (i) => gates[i] ? Promise.resolve() : new Promise((done) => events.addEventListener('call-' + i, done, { once: true }))
  const release = (i) => gates[i]()
  const settle = () => index.settled()
  try {
    await test({ store, engine, status, request, expected, start, add, wait, release, settle, calls,
      block: () => { block = true }, unblock: () => { block = false }, fail: (value) => { fault = value } })
    results.push({ name, outcomes, calls })
  } finally {
    for (const release of gates) release?.()
    await engine.dispose()
    store.close()
  }
}
await run('atomic-cutover-and-prune', async (f) => {
  f.add('older'); f.add('newer')
  await f.request('requires-rendered-encoder-revision', 'start', f.expected(), 400)
  await f.start('initial-start')
  await f.settle()
  const first = await f.status('initial-completed')
  assert.equal(first.job.state, 'completed')
  f.store.set('models', models('two', 2))
  const offset = f.calls.length
  f.block()
  const accepted = await f.start('replacement-start')
  assert.equal(accepted.job.state, 'running')
  await f.wait(offset)
  assert.equal((await f.status('old-active-before-first-provider')).active.generationId, first.active.generationId)
  f.release(offset)
  await f.wait(offset + 1)
  const staged = await f.status('old-active-with-staging-checkpoint')
  assert.equal(staged.active.generationId, first.active.generationId)
  assert.equal(staged.job.processed, 1)
  f.release(offset + 1)
  await f.settle()
  const next = await f.status('replacement-committed')
  assert.notEqual(next.active.generationId, first.active.generationId)
  assert.equal(next.job.state, 'completed')
  f.unblock()
  f.store.set('models', models('three', 2))
  await f.start('third-generation-start')
  await f.settle()
  const third = await f.status('previous-kept-oldest-pruned')
  assert.equal(third.retained.generations, 2)
  assert.equal(third.retained.vectors, 4)
  assert.equal(f.store.db.prepare('SELECT count(*) AS n FROM vector_spaces WHERE id=?').get(first.active.spaceId).n, 0)
})
await run('empty-and-unknown-width', async (f) => {
  f.store.set('models', models('native-width', null))
  await f.start('unknown-width-start')
  await f.settle()
  const unknown = await f.status('unknown-width-paused-no-probe')
  assert.equal(unknown.job.error, 'embedding_empty_native_dimension_unknown')
  assert.equal(unknown.active, undefined)
  f.store.set('models', models('declared-width', 2))
  await f.start('known-width-start')
  await f.settle()
  assert.equal((await f.status('known-empty-completed-no-probe')).active.dimensions, 2)
  assert.equal(f.calls.length, 0)
})
await run('pause-discards-late-result', async (f) => {
  f.add('paused-record')
  f.block()
  await f.start('start')
  await f.wait(0)
  await f.request('pause', 'pause', f.expected())
  f.release(0)
  await f.settle()
  const stopped = await f.status('late-result-cannot-write-or-publish')
  assert.equal(stopped.job.state, 'paused')
  assert.equal(stopped.retained.vectors, 0)
  assert.equal(stopped.active, undefined)
})
await run('edit-and-recall-after-first-request', async (f) => {
  f.add('will be recalled', { sources: [{ sceneId: 's', messageId: 'recall-source', senderId: 'other' }] })
  const edited = f.add('old edit body')
  f.block()
  await f.start('start')
  await f.wait(0)
  f.engine.memory.update(edited.id, { content: 'replacement edit body' })
  f.engine.memory.recall('s', 'recall-source')
  f.release(0)
  await f.wait(1)
  assert.deepEqual(f.calls.map((call) => call.contents), [['old edit body'], ['replacement edit body']])
  assert.equal((await f.status('old-vector-not-committed')).job.processed, 0)
  f.release(1)
  await f.settle()
  const completed = await f.status('only-requalified-edit-committed')
  assert.equal(completed.job.total, 1)
  assert.equal(completed.coverage.indexed, 1)
})
await run('policy-revoked-after-dispatch', async (f) => {
  f.add('protected-record')
  f.block()
  await f.start('start')
  await f.wait(0)
  f.store.saveScene({ ...scene, memoryReadEnabled: false, memoryReadRevision: 1 })
  f.release(0)
  await f.settle()
  const denied = await f.status('revocation-discards-late-result')
  assert.equal(denied.job.error, 'memory_read_revoked')
  assert.equal(denied.retained.vectors, 0)
})
await run('known-and-unknown-retry-confirmation', async (f) => {
  f.add('charged-record')
  f.fail(new ModelProtocolError('http', { status: 400 }))
  await f.start('known-start')
  await f.settle()
  const known = await f.status('known-response-clears-uncertainty')
  assert.equal(known.job.state, 'failed')
  assert.equal(known.job.requiresRetryConfirmation, false)
  f.fail(new Error('synthetic opaque failure'))
  await f.request('resume-known-without-confirmation', 'resume', f.expected())
  await f.settle()
  const unknown = await f.status('unknown-response-retains-uncertainty')
  assert.equal(unknown.job.requiresRetryConfirmation, true)
  await f.request('unconfirmed-retry-denied', 'resume', f.expected(), 400)
  assert.equal(f.calls.length, 2)
  f.fail(undefined)
  await f.request('confirmed-retry-admitted', 'resume', { ...f.expected(), confirmUnknownRequest: true })
  await f.settle()
  assert.equal((await f.status('confirmed-retry-completed')).job.state, 'completed')
})
await run('captured-credential-removal-fails-closed-or-uses-env', async (f) => {
  const noEnv = models('stored-only')
  noEnv.providers[0].protocol = 'openai-chat'
  delete noEnv.providers[0].credentialEnv
  f.store.saveModels(noEnv, { p: { action: 'set', value: 'synthetic-stored' } })
  f.add('credential protected record')
  const router = f.engine.router.bind(f.engine)
  // After the job's pinned Store.models snapshot, before credential lookup.
  f.engine.router = (config, admit) => {
    assert.equal(config.providers[0].credentialConfigured, true)
    f.store.secrets.apply('model', 'p', { action: 'clear' })
    return router(config, admit)
  }
  const fetch = globalThis.fetch
  globalThis.fetch = async (url, options) => {
    assert.equal(String(url), 'http://127.0.0.1/v1/embeddings')
    const input = JSON.parse(options.body)
    assert.equal(new Headers(options.headers).get('authorization'), 'Bearer ' + process.env.HUMANENGINE_EMBEDDING_HTTP_FIXTURE)
    f.calls.push({ model: input.model, contents: input.input })
    return new Response(JSON.stringify({ data: [{ index: 0, embedding: [1, 0] }],
      usage: { prompt_tokens: 1, total_tokens: 1 } }), { headers: { 'Content-Type': 'application/json' } })
  }
  try {
    await f.start('stored-only-start')
    await f.settle()
    const removed = await f.status('removed-stored-credential-no-anonymous-provider')
    assert.equal(removed.job.error, 'credential_not_configured')
    assert.equal(f.calls.length, 0)
    const fallback = models('env-fallback')
    fallback.providers[0].protocol = 'openai-chat'
    f.store.saveModels(fallback, { p: { action: 'set', value: 'synthetic-stored' } })
    await f.start('environment-fallback-start')
    await f.settle()
    assert.equal((await f.status('environment-fallback-completed')).job.state, 'completed')
    assert.equal(f.calls.length, 1)
  } finally {
    globalThis.fetch = fetch
  }
})
const sourceHashes = Object.fromEntries(['src/embedding-index.ts', 'src/admin.ts'].map((file) => [
  file, createHash('sha256').update(readFileSync(new URL('../../' + file, import.meta.url))).digest('hex'),
]))
const body = JSON.stringify({ node: process.version, sourceHashes, results }, null, 2) + '\n'
const target = new URL('../internal/app/testdata/embedding-rebuild-http-source.json', import.meta.url)
if (process.argv.includes('--check')) assert.equal(readFileSync(target, 'utf8'), body)
else writeFileSync(target, body)
console.log(`PASS ${results.length} embedding rebuild Hono/fake-provider scenarios`)
