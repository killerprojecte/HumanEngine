/** Offline synthetic HTTP comparison. No network providers, deployment, or persistent data. */
import assert from 'node:assert/strict'
import { mkdtempSync, rmSync } from 'node:fs'
import { tmpdir, cpus } from 'node:os'
import { join } from 'node:path'
import { createHash } from 'node:crypto'
import { createServer } from 'node:http'
import { Worker, isMainThread, parentPort, workerData } from 'node:worker_threads'
import { register } from 'tsx/esm/api'
register()
const output = console.log.bind(console)
console.log = (...args) => console.error(...args)
const headers = { Authorization: 'Bearer synthetic-owner-load' }
const sleep = (ms) => new Promise((resolve) => setTimeout(resolve, ms))
const paths = ['/api/settings', '/api/scenes', '/api/models']
const percentile = (values, p) =>
  [...values].sort((a, b) => a - b)[Math.ceil(values.length * p) - 1] ?? null
if (!isMainThread) {
  process.env.HUMANENGINE_DB = workerData.database
  process.env.HUMANENGINE_ADMIN_TOKEN = 'synthetic-owner-load'
  process.env.HUMANENGINE_PLUGINS = ''
  process.env.HUMANENGINE_LOG_DIR = workerData.logs
  const runtime =
    workerData.mode === 'same-loop'
      ? await import('../src/server/owner-runtime.ts')
      : await import('../server/utils/engine.ts')
  await runtime.initializeEngine()
  const server = createServer(async (req, res) => {
    const controller = new AbortController()
    res.on('close', () => {
      if (!res.writableFinished) controller.abort()
    })
    try {
      const request = new Request(`http://localhost${req.url}`, {
        method: req.method,
        headers: req.headers,
        signal: controller.signal,
      })
      const response = await runtime.handleEngineRequest(request)
      if (await runtime.publishEngineResponse?.(response, res)) return
      res.writeHead(response.status, Object.fromEntries(response.headers))
      res.end(Buffer.from(await response.arrayBuffer()))
    } catch {
      if (res.headersSent) res.destroy()
      else {
        res.writeHead(503)
        res.end('safe')
      }
    }
  })
  await new Promise((resolve) => server.listen(0, '127.0.0.1', resolve))
  parentPort.postMessage({ ready: true, port: server.address().port })
  parentPort.on('message', async (message) => {
    if (message !== 'close') return
    await new Promise((resolve) => server.close(resolve))
    await runtime.closeEngine()
    parentPort.postMessage({ closed: true })
  })
} else {
  const { Store } = await import('../src/store.ts')
  const { Engine } = await import('../src/engine.ts')
  const { RecoveryAuthority, metadata, saveGate } = await import('../src/memory/recovery/ledger.ts')
  const root = mkdtempSync(join(tmpdir(), 'he-owner-load-'))
  const saved = { ...process.env }
  const results = []
  try {
    for (const mode of ['same-loop', 'isolated-owner']) {
      const caseRoot = join(root, mode),
        database = join(caseRoot, 'data', 'live.sqlite')
      process.env.HUMANENGINE_LOG_DIR = join(caseRoot, 'logs')
      delete process.env.HUMANENGINE_RECOVERY_AUTHORITY_DIRECTORY
      const store = new Store(database),
        engine = new Engine(store)
      const recordRows = 30000,
        chainRows = 1000
      store.db.transaction(() => {
        const insert = store.db.prepare('INSERT INTO memories VALUES (?,?,?,?,?,?)')
        for (let i = 0; i < recordRows; i++) {
          const record = {
            id: `m${i}`,
            scope: { kind: 'scene', sceneId: 'synthetic', personId: `p${i % 100}` },
            content: `Synthetic ${i} ${'x'.repeat(512)}`,
            kind: 'fact',
            importance: 'medium',
            confidence: 1,
            locked: false,
            sources: [],
            learnedAt: '2026-01-01T00:00:00.000Z',
            status: 'active',
            origin: 'manual',
          }
          insert.run(
            record.id,
            'scene:synthetic',
            `p${i % 100}`,
            record.content,
            record.id,
            JSON.stringify(record),
          )
        }
      })()
      const gate = metadata(store.db),
        authority = new RecoveryAuthority(gate.authority)
      let head = authority.head()
      authority.db.transaction(() => {
        const insert = authority.db.prepare('INSERT INTO revocations VALUES(?,?,?,?)')
        for (let i = 0; i < chainRows; i++) {
          const payload = JSON.stringify({ kind: 'source', sceneId: 'other', messageId: `s${i}` })
          const sequence = head.sequence + 1
          const hash = createHash('sha256')
            .update(`${head.instanceId}|${sequence}|${head.hash}|${payload}`)
            .digest('hex')
          insert.run(sequence, head.hash, payload, hash)
          head = { instanceId: head.instanceId, sequence, hash }
        }
      })()
      saveGate(store.db, { ...gate, head })
      authority.close()
      await engine.dispose()
      await store.closeLogs()
      store.close()
      const worker = new Worker(new URL(import.meta.url), {
        workerData: { mode, database, logs: join(caseRoot, 'logs') },
      })
      const ready = await new Promise((resolve, reject) => {
        worker.once('message', resolve)
        worker.once('error', reject)
      })
      const base = `http://127.0.0.1:${ready.port}`
      try {
        for (const path of paths) assert.equal((await fetch(base + path, { headers })).status, 200)
        const page = '/api/memories/page?scene=scene:synthetic&limit=20&locale=en'
        assert.equal((await fetch(base + page, { headers })).status, 200)
        const samples = [],
          waiting = new Set(),
          heavy = []
        let skipped = 0,
          next = 0
        const one = () => {
          if (waiting.size >= 8) {
            skipped++
            return
          }
          const path = paths[next++ % paths.length],
            started = performance.now()
          const promise = (async () => {
            try {
              const response = await fetch(base + path, {
                headers,
                signal: AbortSignal.timeout(30000),
              })
              const body = await response.json()
              if (response.status === 200) {
                if (path === '/api/settings') assert.equal(typeof body.name, 'string')
                else if (path === '/api/scenes') assert(Array.isArray(body))
                else
                  assert(
                    Array.isArray(body.providers) &&
                      Array.isArray(body.models) &&
                      Array.isArray(body.routes),
                  )
              }
              samples.push({ path, status: response.status, ms: performance.now() - started })
            } catch (error) {
              samples.push({
                path,
                status: 0,
                error: error.message,
                ms: performance.now() - started,
              })
            }
          })().finally(() => waiting.delete(promise))
          waiting.add(promise)
        }
        const start = performance.now(),
          timer = setInterval(one, 20)
        await sleep(100)
        for (let i = 0; i < 4; i++) {
          const started = performance.now()
          const response = await fetch(base + page, { headers, signal: AbortSignal.timeout(30000) })
          const text = await response.text()
          heavy.push({
            status: response.status,
            bytes: Buffer.byteLength(text),
            ms: performance.now() - started,
          })
        }
        await sleep(300)
        clearInterval(timer)
        await Promise.all([...waiting])
        const good = samples.filter((sample) => sample.status === 200)
        results.push({
          mode,
          recordRows,
          chainRows,
          elapsedMs: performance.now() - start,
          requests: samples.length,
          successful200: good.length,
          errors: samples.filter((s) => s.status !== 200),
          skipped20msTicks: skipped,
          p50Ms: percentile(
            good.map((s) => s.ms),
            0.5,
          ),
          p95Ms: percentile(
            good.map((s) => s.ms),
            0.95,
          ),
          maxMs: Math.max(...good.map((s) => s.ms)),
          heavy,
          samples,
        })
      } finally {
        const closed = new Promise((resolve) => worker.once('message', resolve))
        worker.postMessage('close')
        await closed
        await worker.terminate()
      }
    }
    output(
      JSON.stringify(
        {
          node: process.version,
          cpu: cpus()[0]?.model,
          notes:
            'Synthetic persistent DB; production supervisor and real owner memory-page endpoint. HTTP clients are outside server event loop. Four sequential CPU-heavy page requests, poll settings/scenes/models every20ms with8 in flight. No claim of natural scheduled-job rate or production SLA. No providers/adapters, deployment, or user data.',
          results,
        },
        null,
        2,
      ),
    )
  } finally {
    for (const key of Object.keys(process.env)) if (!(key in saved)) delete process.env[key]
    Object.assign(process.env, saved)
    rmSync(root, { recursive: true, force: true })
  }
}
