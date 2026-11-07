/** Synthetic live authority writes plus real HTTP config reads; no providers or deployment. */
import assert from 'node:assert/strict'
import { mkdtempSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { createServer } from 'node:http'
import { createHash } from 'node:crypto'
import workerThreads from 'node:worker_threads'
import { syncBuiltinESMExports } from 'node:module'
import { Worker, isMainThread, parentPort, workerData } from 'node:worker_threads'
import { register } from 'tsx/esm/api'
register()
const sleep = (ms) => new Promise((resolve) => setTimeout(resolve, ms))
const headers = { Authorization: 'Bearer synthetic-contention' }
if (!isMainThread && workerData.role === 'writer') {
  const Database = (await import('better-sqlite3')).default
  const { RecoveryAuthority, metadata, saveGate } = await import('../src/memory/recovery/ledger.ts')
  const db = new Database(workerData.database),
    authority = new RecoveryAuthority(metadata(db).authority)
  if (workerData.historyRows) {
    db.transaction(() => {
      const gate = metadata(db)
      let head = authority.head()
      authority.db.transaction(() => {
        const insert = authority.db.prepare('INSERT INTO main.revocations VALUES(?,?,?,?)')
        for (let i = 0; i < workerData.historyRows; i++) {
          const payload = JSON.stringify({ kind: 'source', sceneId: 'seed', messageId: `seed${i}` })
          const sequence = head.sequence + 1
          const hash = createHash('sha256')
            .update(`${head.instanceId}|${sequence}|${head.hash}|${payload}`)
            .digest('hex')
          insert.run(sequence, head.hash, payload, hash)
          head = { instanceId: head.instanceId, sequence, hash }
        }
      })()
      saveGate(db, { ...gate, head })
    }).immediate()
    authority.head()
  }
  parentPort.postMessage({ ready: true })
  parentPort.once('message', async () => {
    const samples = []
    try {
      for (let i = 0; i < 100; i++) {
        const start = performance.now()
        try {
          db.transaction(() => {
            const gate = metadata(db)
            const head = authority.append(
              { kind: 'source', sceneId: 'synthetic', messageId: `w${i}` },
              gate.head,
            )
            saveGate(db, { ...gate, head })
          }).immediate()
          samples.push({ ms: performance.now() - start, ok: true })
        } catch (error) {
          samples.push({
            ms: performance.now() - start,
            ok: false,
            code: error.code ?? error.message,
          })
        }
        await sleep(20)
      }
      parentPort.postMessage({ samples })
    } finally {
      authority.close()
      db.close()
    }
  })
} else if (!isMainThread) {
  const probe = { counts: {}, failures: [], hostFailures: [], active: 0, maxActive: 0 }
  // Wrap only this synthetic HTTP fixture's child workers. The shipped runtime has
  // no instrumentation hooks and the internal diagnostic messages contain no rows.
  workerThreads.Worker = class extends Worker {
    constructor(source, options) {
      const prefix = `await import(${JSON.stringify(new URL('./http-publication-probe.mjs', import.meta.url).href)}); `
      super(options?.eval ? prefix + source : source, options)
      this.on('message', (message) => {
        if (!message.publicationProbe) return
        const { event, detail } = message.publicationProbe
        probe.counts[event] = (probe.counts[event] ?? 0) + 1
        if (event === 'failure') probe.failures.push(detail)
      })
    }
  }
  syncBuiltinESMExports()
  process.env.HUMANENGINE_DB = workerData.database
  process.env.HUMANENGINE_ADMIN_TOKEN = 'synthetic-contention'
  process.env.HUMANENGINE_PLUGINS = ''
  process.env.HUMANENGINE_LOG_DIR = workerData.logs
  delete process.env.HUMANENGINE_RECOVERY_AUTHORITY_DIRECTORY
  const runtime = await import('../server/utils/engine.ts')
  await runtime.initializeEngine()
  const { gate } = await runtime.getEngine()
  const publish = gate.publish.bind(gate)
  gate.publish = (...args) => {
    probe.active++
    probe.maxActive = Math.max(probe.maxActive, probe.active)
    return publish(...args).finally(() => probe.active--)
  }
  const server = createServer(async (req, res) => {
    const controller = new AbortController()
    res.on('close', () => {
      if (!res.writableFinished) controller.abort()
    })
    try {
      const response = await runtime.handleEngineRequest(
        new Request(`http://localhost${req.url}`, {
          headers: req.headers,
          signal: controller.signal,
        }),
      )
      if (await runtime.publishEngineResponse(response, res)) return
      res.writeHead(response.status, Object.fromEntries(response.headers))
      res.end(Buffer.from(await response.arrayBuffer()))
    } catch (error) {
      probe.hostFailures.push(error.code ?? error.message)
      if (res.headersSent) res.destroy()
      else {
        res.writeHead(503)
        res.end('safe')
      }
    }
  })
  await new Promise((resolve) => server.listen(0, '127.0.0.1', resolve))
  parentPort.postMessage({ port: server.address().port })
  parentPort.on('message', async (message) => {
    if (message === 'stats') {
      parentPort.postMessage({ probe: structuredClone(probe) })
      return
    }
    if (message !== 'close') return
    await new Promise((resolve) => server.close(resolve))
    await runtime.closeEngine()
    parentPort.postMessage({ closed: true })
  })
} else {
  const root = mkdtempSync(join(tmpdir(), 'he-fence-pressure-')),
    database = join(root, 'data', 'live.sqlite')
  const started = (worker) =>
    new Promise((resolve, reject) => {
      worker.once('message', resolve)
      worker.once('error', reject)
    })
  const server = new Worker(new URL(import.meta.url), {
    workerData: { role: 'http', database, logs: join(root, 'logs') },
  })
  const historyRows = Number(process.argv[2] ?? 0)
  let writer
  try {
    const { port } = await started(server),
      base = `http://127.0.0.1:${port}`
    for (const path of ['/api/settings', '/api/scenes', '/api/models'])
      assert.equal((await fetch(base + path, { headers })).status, 200)
    writer = new Worker(new URL(import.meta.url), {
      workerData: { role: 'writer', database, historyRows },
    })
    await started(writer)
    const coldStarted = performance.now()
    assert.equal((await fetch(base + '/api/settings', { headers })).status, 200)
    const coldVerificationMs = performance.now() - coldStarted
    const completed = started(writer)
    const samples = [],
      pending = new Set(),
      paths = ['/api/settings', '/api/scenes', '/api/models']
    let skipped = 0,
      index = 0
    const tick = () => {
      if (pending.size >= 8) {
        skipped++
        return
      }
      const path = paths[index++ % paths.length],
        start = performance.now()
      const request = (async () => {
        try {
          const response = await fetch(base + path, { headers, signal: AbortSignal.timeout(30000) })
          const body = await response.text()
          if (response.status === 200) JSON.parse(body)
          samples.push({ path, status: response.status, ms: performance.now() - start })
        } catch (error) {
          samples.push({ path, status: 0, ms: performance.now() - start, error: error.message })
        }
      })().finally(() => pending.delete(request))
      pending.add(request)
    }
    const controls = [],
      controlPending = new Set()
    const controlTimer = setInterval(() => {
      if (controlPending.size) return
      const start = performance.now()
      const one = fetch(base + '/api/settings')
        .then(async (response) => {
          await response.text()
          controls.push({ status: response.status, ms: performance.now() - start })
        })
        .finally(() => controlPending.delete(one))
      controlPending.add(one)
    }, 20)
    const timer = setInterval(tick, 10)
    writer.postMessage('start')
    const writes = (await completed).samples
    await sleep(100)
    clearInterval(timer)
    clearInterval(controlTimer)
    await Promise.all([...pending, ...controlPending])
    const percentile = (values, p) =>
      [...values].sort((a, b) => a - b)[Math.ceil(values.length * p) - 1]
    const good = samples.filter((sample) => sample.status === 200)
    const statistics = started(server)
    server.postMessage('stats')
    const probe = (await statistics).probe
    console.log(
      JSON.stringify(
        {
          node: process.version,
          historyRows,
          coldVerificationMs,
          probe,
          control401: controls.length,
          controlErrors: controls.filter((sample) => sample.status !== 401),
          controlP95Ms: percentile(
            controls.map((sample) => sample.ms),
            0.95,
          ),
          notes:
            '100 actual durable authority+main-gate transactions every20ms, config HTTP every10ms/8 client in flight. Experiment limits only. Successful200 and errors separated.',
          successful200: good.length,
          requests: samples.length,
          errors: samples.filter((sample) => sample.status !== 200),
          skipped10msTicks: skipped,
          readP50Ms: percentile(
            good.map((sample) => sample.ms),
            0.5,
          ),
          readP95Ms: percentile(
            good.map((sample) => sample.ms),
            0.95,
          ),
          writeP50Ms: percentile(
            writes.map((sample) => sample.ms),
            0.5,
          ),
          writeP95Ms: percentile(
            writes.map((sample) => sample.ms),
            0.95,
          ),
          writeMaxMs: Math.max(...writes.map((sample) => sample.ms)),
          writes,
          samples,
        },
        null,
        2,
      ),
    )
  } finally {
    await writer?.terminate()
    const closed = started(server)
    server.postMessage('close')
    await closed
    await server.terminate()
    rmSync(root, { recursive: true, force: true })
  }
}
