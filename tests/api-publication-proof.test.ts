import test from 'node:test'
import assert from 'node:assert/strict'
import { mkdtempSync, rmSync } from 'node:fs'
import { join } from 'node:path'
import { tmpdir } from 'node:os'
import { IncomingMessage, ServerResponse } from 'node:http'
import { Socket } from 'node:net'
import { Worker } from 'node:worker_threads'
import { Store } from '../src/store.ts'
import { Engine } from '../src/engine.ts'
import { createApp, fetchWithPublicationProof } from '../src/api.ts'
import { metadata } from '../src/memory/recovery/ledger.ts'
import type { PublicationInfo } from '../src/server/config-read.ts'
import type { HttpResult } from '../src/server/http-result.ts'
import { PublicationGate } from '../src/server/publication-gate.ts'
import { WorkerRpc } from '../src/server/worker-rpc.ts'

async function fixture(
  run: (
    store: Store,
    app: ReturnType<typeof createApp>,
    publisher: PublicationGate,
  ) => Promise<void>,
) {
  const root = mkdtempSync(join(tmpdir(), 'he-api-proof-'))
  const store = new Store(join(root, 'data', 'live.sqlite'))
  const engine = new Engine(store)
  const app = createApp(store, { engine, token: 'synthetic-proof-token' })
  const reader = new WorkerRpc(
    new Worker(new URL('./fixtures/config-read-worker.mjs', import.meta.url), {
      workerData: { database: store.db.name },
    }),
  )
  await reader.ready
  const info = await reader.call<PublicationInfo>('publication-info', null)
  const publisher = new PublicationGate(store.db.name, info, reader)
  try {
    await run(store, app, publisher)
  } finally {
    await publisher.close()
    await reader.close()
    await engine.dispose()
    await store.closeLogs()
    store.close()
    rmSync(root, { recursive: true, force: true })
  }
}
const request = (path = '/api/settings', method = 'GET') =>
  new Request(`http://localhost${path}`, {
    method,
    headers: { Authorization: 'Bearer synthetic-proof-token' },
  })
const nodeResponse = () => new ServerResponse(new IncomingMessage(new Socket()))
const publish = (publisher: PublicationGate, result: HttpResult, output: ServerResponse) =>
  publisher.publish({ result }, output, new AbortController().signal)

test('API proof binds exact GET Response and the explicit HEAD null-body clone', async () => {
  await fixture(async (store, app, publisher) => {
    for (const method of ['GET', 'HEAD']) {
      const captured = await fetchWithPublicationProof(app, request('/api/settings', method))
      assert.equal(captured.response.status, 200)
      assert.deepEqual(captured.observedHead, metadata(store.db)!.head)
      if (method === 'HEAD') assert.equal(captured.response.body, null)
      const result: HttpResult = {
        status: captured.response.status,
        headers: [...captured.response.headers],
        body: new Uint8Array(await captured.response.arrayBuffer()),
        observedHead: captured.observedHead!,
      }
      const output = nodeResponse()
      await publish(publisher, result, output)
      assert.equal(output.statusCode, 200)
      assert.equal(output.writableEnded, true)
    }
    const unauthorized = await fetchWithPublicationProof(
      app,
      new Request('http://localhost/api/settings'),
    )
    assert.equal(unauthorized.response.status, 401)
    assert.equal(unauthorized.observedHead, undefined)
  })
})

test('a cloned or substituted ordinary response never inherits captured authority', async () => {
  await fixture(async (_store, app) => {
    const original = app.fetch.bind(app)
    app.fetch = async (...args: Parameters<typeof app.fetch>) => {
      const response = await original(...args)
      return new Response('SUBSTITUTED_BODY', {
        status: response.status,
        headers: response.headers,
      })
    }
    const captured = await fetchWithPublicationProof(app, request())
    assert.equal(await captured.response.text(), 'SUBSTITUTED_BODY')
    assert.equal(captured.observedHead, undefined)
  })
})

test('owner mutation after final middleware cannot re-bless the older body at fetch completion', async () => {
  await fixture(async (store, app, publisher) => {
    const original = app.fetch.bind(app)
    const before = metadata(store.db)!.head
    app.fetch = async (...args: Parameters<typeof app.fetch>) => {
      const response = await original(...args)
      store.recall('synthetic', 'between-middleware-and-owner')
      return response
    }
    const captured = await fetchWithPublicationProof(app, request())
    assert.deepEqual(captured.observedHead, before)
    assert.notDeepEqual(captured.observedHead, metadata(store.db)!.head)
    const result: HttpResult = {
      status: captured.response.status,
      headers: [...captured.response.headers],
      body: new Uint8Array(await captured.response.arrayBuffer()),
      observedHead: captured.observedHead!,
    }
    await assert.rejects(
      publish(publisher, result, nodeResponse()),
      /configuration_read_result_stale/,
    )
  })
})

test('asynchronously serialized response keeps its original head through a later recall', async () => {
  await fixture(async (store, app, publisher) => {
    let finish!: () => void
    app.get('/api/synthetic-stream', (c) =>
      c.newResponse(
        new ReadableStream({
          start(controller) {
            finish = () => {
              controller.enqueue(new TextEncoder().encode('SYNTHETIC_PRE_RECALL_BYTES'))
              controller.close()
            }
          },
        }),
      ),
    )
    const captured = await fetchWithPublicationProof(app, request('/api/synthetic-stream'))
    const body = captured.response.arrayBuffer()
    store.recall('synthetic', 'while-body-is-awaiting')
    finish()
    const result: HttpResult = {
      status: captured.response.status,
      headers: [...captured.response.headers],
      body: new Uint8Array(await body),
      observedHead: captured.observedHead!,
    }
    await assert.rejects(
      publish(publisher, result, nodeResponse()),
      /configuration_read_result_stale/,
    )
  })
})

test('generic publication rejects shared mutable bytes and already-ended responses', async () => {
  await fixture(async (_store, app, publisher) => {
    const captured = await fetchWithPublicationProof(app, request())
    const result: HttpResult = {
      status: captured.response.status,
      headers: [...captured.response.headers],
      body: new Uint8Array(new SharedArrayBuffer(8)),
      observedHead: captured.observedHead!,
    }
    await assert.rejects(
      publish(publisher, result, nodeResponse()),
      /configuration_read_result_invalid/,
    )
    result.body = new TextEncoder().encode('{}')
    const ended = nodeResponse()
    ended.end('done')
    await assert.rejects(publish(publisher, result, ended), /configuration_read_result_invalid/)
  })
})

test('a recall microtask after body construction cannot attach a newer head to old bytes', async () => {
  await fixture(async (store, app, publisher) => {
    app.get('/api/synthetic-construction-race', (c) => {
      const response = c.json({ private: 'PRE_RECALL_BYTES' })
      queueMicrotask(() => store.recall('synthetic', 'before-middleware-resumes'))
      return response
    })
    const captured = await fetchWithPublicationProof(
      app,
      request('/api/synthetic-construction-race'),
    )
    assert.equal(captured.response.status, 503)
    assert.equal(captured.observedHead, undefined)
    assert(!(await captured.response.text()).includes('PRE_RECALL_BYTES'))
  })
})

test('a legitimate privacy mutation binds its new response after the committed head', async () => {
  await fixture(async (store, app, publisher) => {
    app.post('/api/synthetic-privacy-write', (c) => {
      store.recall('synthetic', 'privacy-write')
      return c.json({ applied: true })
    })
    const captured = await fetchWithPublicationProof(
      app,
      request('/api/synthetic-privacy-write', 'POST'),
    )
    assert.equal(captured.response.status, 200)
    assert.deepEqual(captured.observedHead, metadata(store.db)!.head)
    const output = nodeResponse()
    await publish(
      publisher,
      {
        status: captured.response.status,
        headers: [...captured.response.headers],
        body: new Uint8Array(await captured.response.arrayBuffer()),
        observedHead: captured.observedHead!,
      },
      output,
    )
    assert.equal(output.statusCode, 200)
  })
})

test('serialization side effects cannot move the body construction head forward', async () => {
  await fixture(async (store, app) => {
    app.get('/api/synthetic-serialization-race', (c) =>
      c.json({
        toJSON() {
          store.recall('synthetic', 'inside-json-serialization')
          return { private: 'PRE_RECALL_BYTES' }
        },
      }),
    )
    const captured = await fetchWithPublicationProof(
      app,
      request('/api/synthetic-serialization-race'),
    )
    assert.equal(captured.response.status, 503)
    assert.equal(captured.observedHead, undefined)
    assert(!(await captured.response.text()).includes('PRE_RECALL_BYTES'))
  })
})
