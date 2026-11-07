import test from 'node:test'
import assert from 'node:assert/strict'
import { mkdtempSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { createServer, IncomingMessage, ServerResponse } from 'node:http'
import { Socket } from 'node:net'
import { Engine } from '../src/engine.ts'
import {
  getEngine,
  initializeEngine,
  handleEngineRequest,
  publishEngineResponse,
  closeEngine,
} from '../server/utils/engine.ts'

test('persistent HTTP host owns no live Engine; config, status, HEAD and writes cross guarded worker boundary', async () => {
  const root = mkdtempSync(join(tmpdir(), 'he-http-owner-'))
  const saved = { ...process.env }
  const original = Engine.prototype.start
  let mainStarts = 0
  Engine.prototype.start = async function () {
    mainStarts++
    return original.call(this)
  }
  process.env.HUMANENGINE_DB = join(root, 'data', 'live.sqlite')
  process.env.HUMANENGINE_ADMIN_TOKEN = 'synthetic-worker-token'
  process.env.HUMANENGINE_PLUGINS = ''
  delete process.env.HUMANENGINE_RECOVERY_AUTHORITY_DIRECTORY
  process.env.HUMANENGINE_LOG_DIR = join(root, 'logs')
  const server = createServer(async (req, res) => {
    const chunks: Buffer[] = []
    for await (const chunk of req) chunks.push(chunk)
    const body = Buffer.concat(chunks)
    const request = new Request(`http://localhost${req.url}`, {
      method: req.method,
      headers: req.headers as Record<string, string>,
      ...(body.length ? { body } : {}),
    })
    const response = await handleEngineRequest(request)
    try {
      if (await publishEngineResponse(response, res)) return
    } catch {
      res.writeHead(503)
      res.end('safe')
      return
    }
    res.writeHead(response.status, Object.fromEntries(response.headers))
    res.end(Buffer.from(await response.arrayBuffer()))
  })
  try {
    await initializeEngine()
    const runtime = await getEngine()
    assert(runtime.owner.worker.threadId > 0)
    assert(runtime.reader.worker.threadId > 0)
    assert.notEqual(runtime.owner.worker.threadId, runtime.reader.worker.threadId)
    assert.equal(mainStarts, 0)
    await new Promise<void>((resolve) => server.listen(0, '127.0.0.1', resolve))
    const base = `http://127.0.0.1:${(server.address() as { port: number }).port}`
    const headers = { Authorization: 'Bearer synthetic-worker-token' }
    assert.equal((await fetch(`${base}/api/settings`)).status, 401)
    const settings = await fetch(`${base}/api/settings`, { headers })
    assert.equal(settings.status, 200)
    const value = await settings.json()
    const write = await fetch(`${base}/api/settings`, {
      method: 'PUT',
      headers: { ...headers, 'Content-Type': 'application/json' },
      body: JSON.stringify({ ...value, name: 'Worker changed' }),
    })
    assert.equal(write.status, 200)
    assert.equal(
      (await (await fetch(`${base}/api/settings`, { headers })).json()).name,
      'Worker changed',
    )
    const status = await fetch(`${base}/api/status`, { headers })
    assert.equal(status.status, 200)
    const statusValue = await status.json()
    assert.equal(statusValue.name, 'Worker changed')
    assert.equal(typeof statusValue.activeTasks, 'number')
    assert.equal(typeof statusValue.queuedModelRequests, 'number')
    const head = await fetch(`${base}/api/status`, { method: 'HEAD', headers })
    assert.equal(head.status, 200)
    assert.equal(await head.text(), '')
    assert.equal((await fetch(`${base}/api/not-a-route`, { headers })).status, 404)
    const controllers: AbortController[] = []
    const held: Response[] = []
    for (let index = 0; index < 64; index++) {
      const controller = new AbortController()
      controllers.push(controller)
      held.push(
        await handleEngineRequest(
          new Request(`${base}/api/settings`, { headers, signal: controller.signal }),
        ),
      )
    }
    assert(held.every((response) => response.status === 200))
    assert.equal((await fetch(`${base}/api/settings`, { headers })).status, 200)
    controllers[0]!.abort()
    const nodeResponse = () => new ServerResponse(new IncomingMessage(new Socket()))
    await assert.rejects(() => publishEngineResponse(held[0]!, nodeResponse()), /lease_expired/)
    const replacementController = new AbortController()
    const replacement = await handleEngineRequest(
      new Request(`${base}/api/settings`, { headers, signal: replacementController.signal }),
    )
    assert.equal(replacement.status, 200)
    const slowResponse = nodeResponse()
    assert.equal(await publishEngineResponse(replacement, slowResponse), true)
    assert.equal(slowResponse.writableEnded, true)
    assert.equal(slowResponse.writableFinished, false)
    // end() queued bytes without a drained socket. Cleanup stays attached, but no new admission cap is imposed.
    assert.equal((await fetch(`${base}/api/settings`, { headers })).status, 200)
    assert.equal(slowResponse.listenerCount('finish'), 1)
    slowResponse.emit('finish')
    assert.equal(slowResponse.listenerCount('finish'), 0)
    assert.equal(slowResponse.listenerCount('close'), 0)
    assert.equal(slowResponse.listenerCount('error'), 0)
    const closeController = new AbortController()
    const closeHeld = await handleEngineRequest(
      new Request(`${base}/api/settings`, { headers, signal: closeController.signal }),
    )
    assert.equal(closeHeld.status, 200)
    const closedResponse = nodeResponse()
    assert.equal(await publishEngineResponse(closeHeld, closedResponse), true)
    assert.equal((await fetch(`${base}/api/settings`, { headers })).status, 200)
    closedResponse.emit('close')
    assert.equal(closedResponse.listenerCount('finish'), 0)
    assert.equal(closedResponse.listenerCount('close'), 0)
    closeController.abort() // release remains idempotent after close
    const drainAbort = new AbortController()
    const abortHeld = await handleEngineRequest(
      new Request(`${base}/api/settings`, { headers, signal: drainAbort.signal }),
    )
    const abortedTransport = nodeResponse()
    assert.equal(await publishEngineResponse(abortHeld, abortedTransport), true)
    drainAbort.abort()
    assert.equal(abortedTransport.destroyed, true)
    assert.equal(abortedTransport.listenerCount('finish'), 0)
    assert.equal(abortedTransport.listenerCount('close'), 0)
    const rejected = await handleEngineRequest(new Request(`${base}/api/settings`, { headers }))
    const invalidTransport = nodeResponse()
    invalidTransport.destroy()
    await assert.rejects(() => publishEngineResponse(rejected, invalidTransport))
    assert.equal(invalidTransport.listenerCount('finish'), 0)
    assert.equal(invalidTransport.listenerCount('close'), 0)

    await assert.rejects(() => publishEngineResponse(replacement, nodeResponse()), /lease_expired/)
    for (const controller of controllers) controller.abort()
    const ownerControllers: AbortController[] = []
    for (let index = 0; index < 64; index++) {
      const controller = new AbortController()
      ownerControllers.push(controller)
      assert.equal(
        (
          await handleEngineRequest(
            new Request(`${base}/api/status`, { headers, signal: controller.signal }),
          )
        ).status,
        200,
      )
    }
    assert.equal((await fetch(`${base}/api/status`, { headers })).status, 200)
    const independent = await fetch(`${base}/api/settings`, { headers })
    assert.equal(independent.status, 200)
    assert.equal((await independent.json()).name, 'Worker changed')
    for (const controller of ownerControllers) controller.abort()
  } finally {
    await new Promise<void>((resolve) => server.close(() => resolve()))
    await closeEngine()
    Engine.prototype.start = original
    for (const key of Object.keys(process.env)) if (!(key in saved)) delete process.env[key]
    Object.assign(process.env, saved)
    rmSync(root, { recursive: true, force: true })
  }
})

test('worker startup failure stays cached until explicit shutdown/restart', async () => {
  const root = mkdtempSync(join(tmpdir(), 'he-worker-startup-'))
  const saved = { ...process.env }
  process.env.HUMANENGINE_DB = join(root, 'missing-parent', 'live.sqlite')
  process.env.HUMANENGINE_ADMIN_TOKEN = 'synthetic-worker-token'
  process.env.HUMANENGINE_RECOVERY_AUTHORITY_DIRECTORY = join(root, 'data', 'invalid')
  // Force an invalid existing database rather than allowing implicit initialization.
  const { mkdirSync, writeFileSync, unlinkSync } = await import('node:fs')
  mkdirSync(join(root, 'missing-parent'))
  writeFileSync(process.env.HUMANENGINE_DB, 'not a SQLite database')
  try {
    const first = getEngine()
    await assert.rejects(first)
    unlinkSync(process.env.HUMANENGINE_DB)
    delete process.env.HUMANENGINE_RECOVERY_AUTHORITY_DIRECTORY
    assert.equal(getEngine(), first)
    const request = () =>
      new Request('http://localhost/api/status', {
        headers: { Authorization: 'Bearer synthetic-worker-token' },
      })
    assert.equal((await handleEngineRequest(request())).status, 503)
    assert.equal(
      (await handleEngineRequest(new Request('http://localhost/api/status'))).status,
      401,
    )
    await closeEngine()
    const initializing = getEngine()
    const interrupted = assert.rejects(initializing, /engine_closing/)
    const closing = closeEngine()
    assert.equal(closeEngine(), closing)
    await assert.rejects(getEngine(), /engine_closing/)
    await closing
    await interrupted
    assert.equal((await handleEngineRequest(request())).status, 200)
  } finally {
    await closeEngine()
    for (const key of Object.keys(process.env)) if (!(key in saved)) delete process.env[key]
    Object.assign(process.env, saved)
    rmSync(root, { recursive: true, force: true })
  }
})
