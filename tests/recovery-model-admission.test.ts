import assert from 'node:assert/strict'
import test from 'node:test'
import { randomUUID } from 'node:crypto'
import { createServer } from 'node:http'
import { access, mkdir, mkdtemp, rm } from 'node:fs/promises'
import { join } from 'node:path'
import { tmpdir } from 'node:os'
import { Engine } from '../src/engine.ts'
import { Store } from '../src/store.ts'
import { createApp } from '../src/api.ts'
import type { ChatMessage } from '../src/models.ts'
import type { Settings } from '../src/schema.ts'
import { metadata } from '../src/memory/recovery/ledger.ts'
import { RESTORATION_REVIEW_ACK, markRestoredModelHold } from '../src/memory/recovery/model-hold.ts'
import { modelScheduler } from '../src/scheduler.ts'
import { backup } from '../scripts/backup.ts'
import { restore } from '../scripts/restore.ts'

// Synthetic transport fixtures establish admission mechanics, not model quality or invoice accuracy.
const adminToken = 'synthetic-restoration-admin'
const credential = 'SYNTHETIC_RESTORATION_CREDENTIAL'
const prompt = 'SYNTHETIC_RESTORATION_PROMPT'
const messages: ChatMessage[] = [{ role: 'user', content: prompt }]
const scene = {
  id: 'restoration-admission-scene',
  kind: 'group' as const,
  adapterId: 'offline',
  accountId: 'synthetic-account',
  platformSceneId: 'synthetic-scene',
  name: 'Offline admission fixture',
  participationEnabled: false,
  learningEnabled: false,
  proactiveEnabled: false,
}
const held = (error: unknown) =>
  !!error &&
  typeof error === 'object' &&
  'code' in error &&
  error.code === 'budget_restore_accounting_hold'

async function fixture(options: { retryCount?: number; failNative?: boolean } = {}) {
  const root = await mkdtemp(join(tmpdir(), 'he-recovery-model-admission-'))
  const database = join(root, 'live', 'humanengine.sqlite')
  const mediaRoot = join(root, 'live', 'media')
  await mkdir(mediaRoot, { recursive: true })
  const calls = { native: 0, customCompletion: 0, customEmbedding: 0, sends: 0 }
  const server = createServer(async (request, response) => {
    const chunks: Buffer[] = []
    for await (const chunk of request) chunks.push(Buffer.from(chunk))
    const body = JSON.parse(Buffer.concat(chunks).toString()) as { input?: string[] }
    calls.native++
    response.setHeader('Content-Type', 'application/json')
    if (options.failNative) {
      response.statusCode = 503
      response.end(JSON.stringify({ error: { message: 'Synthetic retryable fixture' } }))
      return
    }
    response.end(
      JSON.stringify(
        Array.isArray(body.input)
          ? {
              data: body.input.map((_input, index) => ({ index, embedding: [1, 0.5] })),
              usage: { prompt_tokens: 4, total_tokens: 4 },
            }
          : {
              choices: [
                { finish_reason: 'stop', message: { content: 'Synthetic local response.' } },
              ],
              usage: { prompt_tokens: 4, completion_tokens: 2, total_tokens: 6 },
            },
      ),
    )
  })
  await new Promise<void>((resolve) => server.listen(0, '127.0.0.1', resolve))
  const baseUrl = `http://127.0.0.1:${(server.address() as { port: number }).port}`
  const opened: Array<{ store: Store; engine: Engine; close(): Promise<void> }> = []
  const open = (path: string) => {
    const store = new Store(path)
    const engine = new Engine(store)
    const lifecycle = new AbortController()
    engine.protocols.register(
      'fixture:restoration',
      'Offline restoration fixture',
      {
        async complete() {
          calls.customCompletion++
          return {
            text: 'Synthetic custom response.',
            toolCalls: [],
            usage: { inputTokens: 4, outputTokens: 2, totalTokens: 6 },
          }
        },
        async embeddings(_context, inputs) {
          calls.customEmbedding++
          return {
            vectors: inputs.map(() => [1, 0.5]),
            usage: { inputTokens: 4, outputTokens: 0, totalTokens: 4 },
          }
        },
      },
      lifecycle.signal,
    )
    engine.runtime.addAdapter('offline', {
      send: async () => {
        calls.sends++
        throw new Error('Unexpected platform send')
      },
      history: async () => ({ items: [], coverage: 'complete' }),
      getMessage: async () => ({ items: [], coverage: 'complete' }),
      dispose() {},
    })
    const app = createApp(store, { engine, token: adminToken })
    let closed = false
    const result = {
      store,
      engine,
      app,
      database: path,
      request(route: string, body?: unknown, method = 'POST', authenticated = true) {
        return app.request(route, {
          method: body === undefined ? 'GET' : method,
          headers: {
            'Content-Type': 'application/json',
            ...(authenticated ? { Authorization: `Bearer ${adminToken}` } : {}),
          },
          ...(body === undefined ? {} : { body: JSON.stringify(body) }),
        })
      },
      async close() {
        if (closed) return
        closed = true
        try {
          await engine.dispose()
        } finally {
          lifecycle.abort()
          store.close()
        }
      },
    }
    opened.push(result)
    return result
  }
  const live = open(database)
  live.store.saveScene(scene)
  const authorityDirectory = metadata(live.store.db)!.authority
  const modelResponse = await live.request(
    '/api/models',
    {
      providers: [
        {
          id: 'native-provider',
          name: 'Local native',
          protocol: 'openai-chat',
          baseUrl,
          credentialEnv: '',
          retryCount: options.retryCount ?? 0,
          timeoutMs: 2000,
        },
        {
          id: 'custom-provider',
          name: 'Local custom',
          protocol: 'fixture:restoration',
          baseUrl,
          credentialEnv: '',
          retryCount: options.retryCount ?? 0,
          timeoutMs: 2000,
        },
      ],
      models: ['native', 'custom'].map((id) => ({
        id: `${id}-model`,
        displayName: `Synthetic ${id}`,
        providerId: `${id}-provider`,
        apiModelName: 'synthetic-local',
        capabilities: ['text', 'tools', 'embedding'],
        embeddingBatchSize: 1,
        embeddingDimensions: 2,
      })),
      routes: [
        { role: 'replyer', orderedModelIds: ['native-model'], timeoutMs: 5000 },
        { role: 'learning', orderedModelIds: ['custom-model'], timeoutMs: 5000 },
        { role: 'embedding', orderedModelIds: ['native-model'], timeoutMs: 5000 },
      ],
      credentials: {
        'native-provider': { action: 'set', value: credential },
        'custom-provider': { action: 'set', value: credential },
      },
    },
    'PUT',
  )
  assert.equal(modelResponse.status, 200)
  const memory = live.engine.memory.create({
    scope: { kind: 'scene', sceneId: scene.id },
    kind: 'fact',
    content: 'Retained lexical restoration fixture.',
  })
  return {
    root,
    database,
    mediaRoot,
    live,
    calls,
    memory,
    authorityDirectory,
    open,
    async snapshot(name = 'backup', source = { database, mediaRoot }) {
      const destination = join(root, name)
      await backup({ ...source, destination })
      return destination
    },
    async recover(backupRoot: string, name = 'restored') {
      const output = await restore({
        backupRoot,
        destination: join(root, name),
        authorityDirectory,
      })
      return { ...output, ...open(output.database) }
    },
    async close() {
      try {
        for (const entry of opened.reverse()) await entry.close()
      } finally {
        server.closeAllConnections()
        await new Promise<void>((resolve) => server.close(() => resolve()))
        await rm(root, { recursive: true, force: true })
      }
      assert.equal(calls.sends, 0)
    },
  }
}

type Opened = ReturnType<Awaited<ReturnType<typeof fixture>>['open']>
async function recovery(instance: Opened) {
  const response = await instance.request('/api/budget/recovery')
  assert.equal(response.status, 200)
  return response.json() as Promise<{
    restoreId?: string
    held: boolean
    restorationCoverage: string
  }>
}
async function acknowledge(instance: Opened, restoreId: string) {
  return instance.request('/api/budget/recovery/acknowledge', {
    restoreId,
    acknowledgment: RESTORATION_REVIEW_ACK,
  })
}
function accounting(store: Store) {
  return {
    attempts: store.db.prepare('SELECT * FROM budget_attempts ORDER BY rowid').all(),
    usage: store.db.prepare('SELECT * FROM model_usage ORDER BY rowid').all(),
  }
}
function noTransport(calls: Awaited<ReturnType<typeof fixture>>['calls']) {
  assert.deepEqual(calls, { native: 0, customCompletion: 0, customEmbedding: 0, sends: 0 })
}

test('actual restore holds native and custom completions/embeddings, API probes and rebuild even with budget OFF; lexical/admin reads remain usable', async () => {
  const h = await fixture()
  try {
    assert.deepEqual(await recovery(h.live), { held: false, restorationCoverage: 'none' })
    const restored = await h.recover(await h.snapshot())
    assert.equal(restored.store.settings.budgetExperimentEnabled, false)
    assert.equal((await recovery(restored)).held, true)
    assert.equal((await recovery(restored)).restorationCoverage, 'unknown')
    noTransport(h.calls)
    const router = restored.engine.router()
    for (const role of ['replyer', 'learning'] as const)
      await assert.rejects(router.complete(role, messages), held)
    for (const model of ['native-model', 'custom-model'])
      await assert.rejects(router.embeddings(model, ['Synthetic embedding input']), held)
    for (const model of ['native-model', 'custom-model']) {
      for (const capability of ['text', 'embedding']) {
        const response = await restored.request(`/api/models/${model}/probe`, { capability })
        assert.equal(response.status, 200)
        const result = await response.json()
        assert.equal(result.ok, false)
        assert.equal(result.detail, 'budget_restore_accounting_hold')
      }
    }
    const job = restored.engine.embeddingIndex.start()
    await restored.engine.embeddingIndex.settled()
    const status = restored.engine.embeddingIndex.status()
    assert.equal(status.job?.id, job.id)
    assert.equal(status.job?.state, 'failed')
    assert.equal(status.job?.error, 'budget_restore_accounting_hold')
    assert.equal(status.job?.processed, 0)
    assert.equal(status.active, undefined)
    const context = { sceneId: scene.id, learningEnabled: false, simulation: false }
    assert.equal(restored.engine.memory.search(context, 'lexical restoration')[0]?.id, h.memory.id)
    assert.ok(restored.engine.memory.adminList().some((memory) => memory.id === h.memory.id))
    assert.equal((await restored.request('/api/settings')).status, 200)
    assert.deepEqual(restored.store.db.prepare('SELECT * FROM budget_attempts').all(), [])
    noTransport(h.calls)
  } finally {
    await h.close()
  }
})

test('successful restore hold survives a date boundary, process reopen and a cold Engine start without automatic model or platform calls', async (t) => {
  t.mock.timers.enable({ apis: ['Date'], now: Date.UTC(2026, 9, 28, 23, 59) })
  const h = await fixture()
  try {
    const restored = await h.recover(await h.snapshot())
    const before = await recovery(restored)
    t.mock.timers.tick(2 * 24 * 60 * 60 * 1000)
    await restored.close()
    const reopened = h.open(restored.database)
    const previousPlugins = process.env.HUMANENGINE_PLUGINS
    process.env.HUMANENGINE_PLUGINS = ''
    try {
      await reopened.engine.start()
    } finally {
      if (previousPlugins === undefined) delete process.env.HUMANENGINE_PLUGINS
      else process.env.HUMANENGINE_PLUGINS = previousPlugins
    }
    assert.deepEqual(await recovery(reopened), before)
    await assert.rejects(reopened.engine.router().complete('replyer', messages), held)
    await assert.rejects(reopened.engine.router().embeddings('custom-model', ['Synthetic']), held)
    noTransport(h.calls)
  } finally {
    await h.close()
    t.mock.timers.reset()
  }
})

test('authenticated exact-ID acknowledgment releases admission only, not historical accounting; invalid requests never release HOLD', async () => {
  const h = await fixture()
  try {
    const restored = await h.recover(await h.snapshot())
    const before = await recovery(restored)
    const body = { restoreId: before.restoreId!, acknowledgment: RESTORATION_REVIEW_ACK }
    const rows = accounting(restored.store)
    assert.equal(
      (await restored.request('/api/budget/recovery/acknowledge', body, 'POST', false)).status,
      401,
    )
    for (const invalid of [
      { restoreId: body.restoreId },
      { ...body, acknowledgment: 'accept' },
      { ...body, reconciled: true },
      { ...body, restoreId: 'not-a-uuid' },
    ])
      assert.equal(
        (await restored.request('/api/budget/recovery/acknowledge', invalid)).status,
        400,
      )
    assert.equal((await acknowledge(restored, randomUUID())).status, 409)
    assert.deepEqual(await recovery(restored), before)
    await assert.rejects(restored.engine.router().complete('replyer', messages), held)
    noTransport(h.calls)
    // Capture rejected-call accounting separately: acknowledgment itself must not rewrite it.
    const beforeAck = accounting(restored.store)
    assert.deepEqual(beforeAck.attempts, rows.attempts)
    assert.equal((await acknowledge(restored, body.restoreId)).status, 200)
    assert.equal((await acknowledge(restored, body.restoreId)).status, 200)
    assert.deepEqual(accounting(restored.store), beforeAck)
    assert.deepEqual(await recovery(restored), {
      restoreId: body.restoreId,
      held: false,
      restorationCoverage: 'manually_acknowledged',
    })
    assert.equal(
      (await restored.engine.router().complete('replyer', messages)).text,
      'Synthetic local response.',
    )
    assert.equal(
      (await restored.engine.router().complete('learning', messages)).text,
      'Synthetic custom response.',
    )
    assert.equal(
      (await restored.engine.router().embeddings('native-model', ['After manual review'])).vectors
        .length,
      1,
    )
    assert.equal(
      (await restored.engine.router().embeddings('custom-model', ['After manual review'])).vectors
        .length,
      1,
    )
    assert.deepEqual(h.calls, { native: 2, customCompletion: 1, customEmbedding: 1, sends: 0 })
    const status = await restored.request('/api/budget')
    assert.equal(status.status, 200)
    const serialized = await status.text()
    assert.equal(serialized.includes(credential), false)
    assert.equal(serialized.includes(prompt), false)
    const coverage = JSON.parse(serialized)
    assert.equal(coverage.admissionHold, false)
    assert.equal(coverage.restorationId, body.restoreId)
    assert.equal(coverage.restorationCoverage, 'manually_acknowledged')
    // Current-ledger coverage may be settled. It does not erase the independent incomplete marker.
    await restored.close()
    assert.equal(
      (await recovery(h.open(restored.database))).restorationCoverage,
      'manually_acknowledged',
    )
  } finally {
    await h.close()
  }
})

test('restoring an already acknowledged backup generates a fresh HOLD and rejects the previous restore ID', async () => {
  const h = await fixture()
  try {
    const first = await h.recover(await h.snapshot(), 'first-restored')
    const firstId = (await recovery(first)).restoreId!
    assert.equal((await acknowledge(first, firstId)).status, 200)
    const acknowledgedSnapshot = await h.snapshot('acknowledged-backup', first)
    const second = await h.recover(acknowledgedSnapshot, 'second-restored')
    const state = await recovery(second)
    assert.notEqual(state.restoreId, firstId)
    assert.equal(state.held, true)
    assert.equal(state.restorationCoverage, 'unknown')
    assert.equal((await acknowledge(second, firstId)).status, 409)
    await assert.rejects(second.engine.router().complete('replyer', messages), held)
    noTransport(h.calls)
  } finally {
    await h.close()
  }
})

for (const [name, patch] of [
  ['enable strict admission', { budgetExperimentEnabled: true }],
  ['lower the per-turn cap', { budgetPerTurnUsd: 0.05, budgetPerTurnCalls: 2 }],
  ['change reserved lane allocation', { budgetRealtimeRatio: 0.3, budgetMaintenanceRatio: 0.05 }],
] as const satisfies ReadonlyArray<readonly [string, Partial<Settings>]>) {
  test(`budget policy security revision prevents old snapshots from undoing ${name}; current snapshots still restore held`, async () => {
    const h = await fixture()
    try {
      const oldSnapshot = await h.snapshot('old-policy')
      const response = await h.live.request(
        '/api/settings',
        { ...h.live.store.settings, ...patch },
        'PUT',
      )
      assert.equal(response.status, 200)
      await assert.rejects(
        h.recover(oldSnapshot, 'denied-old-policy'),
        /recovery_security_state_unrecoverable/,
      )
      await assert.rejects(access(join(h.root, 'denied-old-policy')), { code: 'ENOENT' })
      const current = await h.recover(await h.snapshot('current-policy'), 'current-restored')
      for (const [key, value] of Object.entries(patch))
        assert.equal(current.store.settings[key as keyof Settings], value)
      assert.equal((await recovery(current)).held, true)
      await assert.rejects(current.engine.router().complete('replyer', messages), held)
      noTransport(h.calls)
    } finally {
      await h.close()
    }
  })
}

test('a restored unresolved attempt is never forgiven by manual admission acknowledgment, next day or restart', async (t) => {
  t.mock.timers.enable({ apis: ['Date'], now: Date.UTC(2026, 9, 28, 23, 59) })
  const h = await fixture()
  try {
    // Synthetic ledger-only fault injection: transport was started but no receipt is known.
    // These fixture bounds are not a claim about a real tokenizer, price or provider contract.
    const ledger = h.live.engine.budget.ledger
    ledger.reserve(
      {
        id: 'synthetic-unknown-attempt',
        requestId: 'synthetic-unknown-request',
        attempt: 1,
        providerId: 'native-provider',
        modelId: 'native-model',
        operation: 'complete',
        lane: 'foreground',
        turnId: 'synthetic-old-turn',
        estimate: {
          source: 'verified_contract',
          estimatorVersion: 'offline-fixture',
          contractVersion: 'offline-fixture',
          enforceable: true,
          inputUpperTokens: 10,
          outputUpperTokens: 2,
          inputCategories: ['inputUsdPerMillion'],
        },
        prices: { inputUsdPerMillion: 1, outputUsdPerMillion: 1 },
      },
      { dailyNanoUsd: 1_000_000_000, maxUnresolvedNanoUsd: 100_000_000 },
    )
    assert.equal(ledger.start('synthetic-unknown-attempt'), true)
    ledger.unknown('synthetic-unknown-attempt')
    const snapshot = await h.snapshot()
    const restored = await h.recover(snapshot)
    const rows = accounting(restored.store)
    assert.equal(restored.engine.budget.coverage().unknownAttempts, 1)
    assert.ok(restored.engine.budget.coverage().unresolvedNanoUsd > 0)
    assert.equal((await acknowledge(restored, (await recovery(restored)).restoreId!)).status, 200)
    assert.deepEqual(accounting(restored.store), rows)
    t.mock.timers.tick(2 * 24 * 60 * 60 * 1000)
    await restored.close()
    const reopened = h.open(restored.database)
    assert.deepEqual(accounting(reopened.store), rows)
    const coverage = reopened.engine.budget.coverage()
    assert.equal(coverage.unknownAttempts, 1)
    assert.ok(coverage.carriedNanoUsd > 0)
    assert.equal(coverage.restorationCoverage, 'manually_acknowledged')
    assert.equal(coverage.coverage, 'pending')
    noTransport(h.calls)
  } finally {
    await h.close()
    t.mock.timers.reset()
  }
})

test('already queued Engine native/custom calls recheck a newly persisted HOLD at the physical execution boundary', async () => {
  const h = await fixture()
  let release = () => {}
  let blockers: Promise<void>[] = []
  let outcomes: Promise<PromiseSettledResult<unknown>[]> | undefined
  try {
    const restored = await h.recover(await h.snapshot())
    assert.equal((await acknowledge(restored, (await recovery(restored)).restoreId!)).status, 200)
    const wait = new Promise<void>((resolve) => {
      release = resolve
    })
    blockers = Array.from({ length: modelScheduler.status.concurrency }, () =>
      modelScheduler.run(0, undefined, () => wait),
    )
    await new Promise<void>((resolve) => setImmediate(resolve))
    const router = restored.engine.router()
    outcomes = Promise.allSettled([
      router.complete('replyer', messages),
      router.complete('learning', messages),
      router.embeddings('native-model', ['Queued native']),
      router.embeddings('custom-model', ['Queued custom']),
    ])
    await new Promise<void>((resolve) => setImmediate(resolve))
    assert.equal(modelScheduler.status.queued, 4)
    noTransport(h.calls)
    // Controlled last-boundary fault injection uses the same atomic marker as restore finalization.
    // This does not pretend the restore CLI overwrites an open database.
    restored.store.db.transaction(() => markRestoredModelHold(restored.store.db))()
    release()
    await Promise.all(blockers)
    const results = await outcomes
    for (const result of results) {
      assert.equal(result.status, 'rejected')
      assert.ok(result.status === 'rejected' && held(result.reason))
    }
    noTransport(h.calls)
  } finally {
    release()
    await Promise.all(blockers)
    await outcomes
    await h.close()
  }
})

test('HOLD established during a real native retry wait prevents later attempts without pretending the first request was unsent', async () => {
  const h = await fixture({ retryCount: 2, failNative: true })
  try {
    const restored = await h.recover(await h.snapshot())
    assert.equal((await acknowledge(restored, (await recovery(restored)).restoreId!)).status, 200)
    const originalLog = restored.store.log.bind(restored.store)
    let retryWaitObserved = false
    restored.store.log = (...args: Parameters<Store['log']>) => {
      const result = originalLog(...args)
      if (args[2] === 'flow.provider_retry_wait.started' && !retryWaitObserved) {
        retryWaitObserved = true
        restored.store.db.transaction(() => markRestoredModelHold(restored.store.db))()
      }
      return result
    }
    try {
      await assert.rejects(restored.engine.router().complete('replyer', messages), held)
    } finally {
      restored.store.log = originalLog
    }
    assert.equal(retryWaitObserved, true)
    assert.equal(
      h.calls.native,
      1,
      'the already issued request is not reversible; no second physical attempt is permitted',
    )
    assert.equal(h.calls.customCompletion, 0)
    assert.equal(h.calls.customEmbedding, 0)
    assert.equal((await recovery(restored)).held, true)
  } finally {
    await h.close()
  }
})
