import test from 'node:test'
import assert from 'node:assert/strict'
import { mkdtempSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { Worker } from 'node:worker_threads'
import { createServer, type ServerResponse } from 'node:http'
import { randomUUID } from 'node:crypto'
import { Store } from '../src/store.ts'
import { Engine } from '../src/engine.ts'
import { recordAssertionEligibilityBaseline } from '../src/memory/recovery/eligibility.ts'
import { WorkerRpc } from '../src/server/worker-rpc.ts'
import { PublicationGate } from '../src/server/publication-gate.ts'
import type { PublicationInfo } from '../src/server/config-read.ts'
import type { HttpResult } from '../src/server/http-result.ts'

const token = 'synthetic-eligibility-fence'
const secret = 'SYNTHETIC_HISTORY_WITHDRAWAL_REGRESSION'
async function fixture(
  run: (value: {
    owner: WorkerRpc
    reader: WorkerRpc
    info: PublicationInfo
    database: string
    id: string
    history: string
    attempted: Int32Array
    request: (path: string, method?: string, body?: object) => Promise<HttpResult>
  }) => Promise<void>,
) {
  const root = mkdtempSync(join(tmpdir(), 'he-eligibility-http-'))
  const database = join(root, 'data', 'live.sqlite')
  const saved = { ...process.env }
  delete process.env.HUMANENGINE_RECOVERY_AUTHORITY_DIRECTORY
  process.env.HUMANENGINE_LOG_DIR = join(root, 'logs')
  const store = new Store(database),
    engine = new Engine(store)
  recordAssertionEligibilityBaseline(store.db)
  store.saveScene({
    id: 's',
    name: 's',
    kind: 'group',
    adapterId: 'fixture',
    accountId: 'bot',
    platformSceneId: 's',
    participationEnabled: true,
    learningEnabled: true,
    proactiveEnabled: false,
  })
  const memory = engine.memory.create({
    scope: { kind: 'scene', sceneId: 's' },
    content: secret,
    kind: 'fact',
  })
  await engine.dispose()
  await store.closeLogs()
  store.close()
  const attempted = new Int32Array(new SharedArrayBuffer(4))
  const source = new URL('../src/server/owner-worker.ts', import.meta.url)
  // Scheduling observation only: the production owner/SQLite path is unchanged.
  const owner = new WorkerRpc(
    new Worker(
      `
    import { register } from 'tsx/esm/api'; register();
    import { workerData } from 'node:worker_threads';
    import Database from 'better-sqlite3';
    const state = new Int32Array(workerData.attempted);
    const probe = new Database(':memory:');
    const prototype = Object.getPrototypeOf(probe.prepare('SELECT 1'));
    probe.close();
    const run = prototype.run;
    prototype.run = function(...args) {
      // Observe the real reservation attempt, before SQLite waits. A publication
      // now excludes the writer before INSERT, not merely at its COMMIT.
      if (this.source === 'BEGIN IMMEDIATE' && this.database.name.endsWith('/authority.sqlite'))
        Atomics.store(state, 0, 1);
      return run.apply(this, args);
    };
    await import(${JSON.stringify(source.href)});
  `,
      {
        eval: true,
        workerData: { database, attempted: attempted.buffer },
        env: {
          ...process.env,
          HUMANENGINE_DB: database,
          HUMANENGINE_ADMIN_TOKEN: token,
          HUMANENGINE_PLUGINS: '',
        },
      },
    ),
  )
  let reader: WorkerRpc | undefined
  try {
    await owner.ready
    reader = new WorkerRpc(
      new Worker(new URL('./fixtures/config-read-worker.mjs', import.meta.url), {
        workerData: { database },
      }),
    )
    await reader.ready
    const info = await reader.call<PublicationInfo>('publication-info', null)
    const request = (path: string, method = 'GET', body?: object) =>
      owner.call<HttpResult>('fetch', {
        url: `http://localhost${path}`,
        method,
        requestId: randomUUID(),
        headers: [
          ['Authorization', `Bearer ${token}`],
          ...(body ? [['Content-Type', 'application/json']] : []),
        ],
        body: new TextEncoder().encode(body ? JSON.stringify(body) : ''),
      })
    await run({
      owner,
      reader,
      info,
      database,
      id: memory.id,
      history: `/api/memory-history?sceneId=s&id=${encodeURIComponent(memory.id)}`,
      attempted,
      request,
    })
  } finally {
    await reader?.close()
    await owner.close()
    for (const key of Object.keys(process.env)) if (!(key in saved)) delete process.env[key]
    Object.assign(process.env, saved)
    rmSync(root, { recursive: true, force: true })
  }
}
async function httpResponse(publish: (response: ServerResponse) => Promise<void>) {
  let published = false
  const server = createServer(async (_request, response) => {
    try {
      await publish(response)
      published = true
    } catch {
      if (response.headersSent) response.destroy()
      else {
        response.writeHead(503)
        response.end('withheld')
      }
    }
  })
  await new Promise<void>((resolve) => server.listen(0, '127.0.0.1', resolve))
  try {
    const response = await fetch(`http://127.0.0.1:${(server.address() as { port: number }).port}`)
    const body = await response.text()
    return { status: response.status, body, published }
  } finally {
    await new Promise<void>((resolve) => server.close(() => resolve()))
  }
}

test('successful invalidation between capture and publication withholds the captured HTTP history', async () => {
  await fixture(async ({ reader, info, database, id, history, request }) => {
    const result = await request(history)
    assert.equal(result.status, 200)
    assert(new TextDecoder().decode(result.body).includes(secret))
    assert.equal(
      (await request(`/api/memories/${id}`, 'PATCH', { status: 'invalidated' })).status,
      200,
    )
    assert.equal((await request(history)).status, 404)
    const gate = new PublicationGate(database, info, reader)
    try {
      const emitted = await httpResponse((output) =>
        gate.publish({ result }, output, new AbortController().signal),
      )
      assert.equal(emitted.status, 503)
      assert.equal(emitted.body.includes(secret), false)
    } finally {
      await gate.close()
    }
  })
})

test(
  'invalidation attempted while the host is pinned cannot acknowledge before older bytes are queued',
  { timeout: 15000 },
  async () => {
    await fixture(async ({ reader, info, database, id, history, attempted, request }) => {
      const result = await request(history)
      assert.equal(result.status, 200)
      Atomics.store(attempted, 0, 0)
      let patch: Promise<HttpResult> | undefined,
        patchAcknowledged = false,
        acknowledgedBeforeEnqueue = false,
        enqueued = false
      const proxy = {
        call: async (
          operation: Parameters<WorkerRpc['call']>[0],
          payload: unknown,
          options?: Parameters<WorkerRpc['call']>[2],
        ) => {
          if (operation === 'publication-verify' && !patch) {
            patch = request(`/api/memories/${id}`, 'PATCH', { status: 'invalidated' }).then(
              (reply) => {
                patchAcknowledged = true
                acknowledgedBeforeEnqueue = !enqueued
                return reply
              },
            )
            // A watchdog is only a test failure condition, never a production limit.
            const deadline = Date.now() + 3000
            while (!Atomics.load(attempted, 0) && !patchAcknowledged && Date.now() < deadline)
              await new Promise((resolve) => setTimeout(resolve, 1))
            assert.equal(
              Atomics.load(attempted, 0),
              1,
              'the real owner must attempt a durable authority write',
            )
            assert.equal(patchAcknowledged, false)
          }
          return reader.call(operation, payload, options)
        },
        releaseResult: (value: object) => reader.releaseResult(value),
      } as WorkerRpc
      const gate = new PublicationGate(database, info, proxy)
      try {
        const emitted = await httpResponse(async (output) => {
          const original = output.end
          output.end = function (this: ServerResponse, ...args: Parameters<ServerResponse['end']>) {
            assert.equal(patchAcknowledged, false)
            enqueued = true
            return original.apply(this, args)
          } as ServerResponse['end']
          await gate.publish({ result }, output, new AbortController().signal)
        })
        assert.equal(emitted.status, 200)
        assert(emitted.body.includes(secret))
        assert(patch)
        assert.equal((await patch).status, 200)
        assert.equal(acknowledgedBeforeEnqueue, false)
        assert.equal((await request(history)).status, 404)
        const stale = await httpResponse((output) =>
          gate.publish({ result }, output, new AbortController().signal),
        )
        assert.equal(stale.status, 503)
        assert.equal(stale.body.includes(secret), false)
      } finally {
        await gate.close()
        await patch?.catch(() => {})
      }
    })
  },
)

test('active-invalidated-active ABA cannot reuse a prior captured history permission', async () => {
  await fixture(async ({ reader, info, database, id, history, request }) => {
    const old = await request(history)
    assert.equal(old.status, 200)
    assert.equal(
      (await request(`/api/memories/${id}`, 'PATCH', { status: 'invalidated' })).status,
      200,
    )
    assert.equal((await request(history)).status, 404)
    assert.equal((await request(`/api/memories/${id}`, 'PATCH', { status: 'active' })).status, 200)
    const current = await request(history)
    assert.equal(current.status, 200)
    const gate = new PublicationGate(database, info, reader)
    try {
      assert.equal(
        (
          await httpResponse((output) =>
            gate.publish({ result: old }, output, new AbortController().signal),
          )
        ).status,
        503,
      )
      const emitted = await httpResponse((output) =>
        gate.publish({ result: current }, output, new AbortController().signal),
      )
      assert.equal(emitted.status, 200)
      assert(emitted.body.includes(secret))
    } finally {
      await gate.close()
    }
  })
})
