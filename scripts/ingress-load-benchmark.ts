import assert from 'node:assert/strict'
import { mkdtemp, rm, writeFile } from 'node:fs/promises'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'
import { createServer } from 'node:http'
import { performance, monitorEventLoopDelay } from 'node:perf_hooks'
import { Store } from '../src/store.ts'
import { Engine } from '../src/engine.ts'
import { createApp } from '../src/api.ts'
import type { Router } from '../src/models.ts'

// Real persistent Store + Engine hooks, no adapters/models/network providers.
const output = resolve(process.argv[2] ?? 'ingress-load-result.json')
const root = await mkdtemp(join(tmpdir(), 'he-ingress-load-'))
const previousAuthority = process.env.HUMANENGINE_RECOVERY_AUTHORITY_DIRECTORY
const previousLogDirectory = process.env.HUMANENGINE_LOG_DIR
process.env.HUMANENGINE_LOG_DIR = join(root, 'logs')
process.env.HUMANENGINE_RECOVERY_AUTHORITY_DIRECTORY = join(root, 'authority')
const cleanup: Array<() => void | Promise<void>> = []
try {
  const store = new Store(join(root, 'data', 'humanengine.sqlite'))
  cleanup.push(() => store.close())
  const engine = new Engine(store)
  cleanup.push(() => engine.dispose())
  let modelCalls = 0
  engine.router = () =>
    ({
      complete: async () => {
        modelCalls++
        throw new Error('benchmark_model_forbidden')
      },
    }) as unknown as Router
  const app = createApp(store, { engine, token: 'synthetic-local-benchmark' })
  const server = createServer(async (req, res) => {
    try {
      const response = await app.fetch(
        new Request(`http://localhost${req.url}`, {
          headers: { Authorization: 'Bearer synthetic-local-benchmark' },
        }),
      )
      res.writeHead(response.status, Object.fromEntries(response.headers))
      res.end(Buffer.from(await response.arrayBuffer()))
    } catch {
      res.writeHead(500)
      res.end()
    }
  })
  cleanup.push(async () => {
    server.closeAllConnections()
    if (server.listening) await new Promise<void>((resolve) => server.close(() => resolve()))
  })
  const stop = new AbortController()
  let failure: unknown
  let http: Promise<void> = Promise.resolve()
  cleanup.push(async () => {
    stop.abort()
    await http
  })
  const scene = (n: number) => ({
    id: `ob:s:${Buffer.from(JSON.stringify(['offline', '100001', 'group', String(200001 + n)])).toString('base64url')}`,
    name: 'Synthetic performance',
    kind: 'group' as const,
    adapterId: 'offline',
    accountId: '100001',
    platformSceneId: String(200001 + n),
    displayName: 'Synthetic performance',
    participationEnabled: false,
    learningEnabled: false,
    proactiveEnabled: false,
  })
  for (let n = 0; n < 100; n++) store.saveScene(scene(n))
  for (let n = 0; n < 250; n++) store.recall(scene(n % 100).id, `old-recalled-${n}`)
  const insert = store.db.prepare('INSERT INTO meme_assets(id,value) VALUES (?,?)')
  store.db.transaction(() => {
    for (let n = 0; n < 5000; n++) {
      const id = `synthetic-asset-${n}`
      insert.run(
        id,
        JSON.stringify({
          id,
          format: 'png',
          bytes: 100,
          width: 8,
          height: 8,
          frames: 1,
          sources: [],
          tags: {
            appearance: [],
            style: [],
            subject: [],
            emotion: [],
            intent: [],
            tone: [],
            intensity: [],
            memeMeaning: [],
            ocr: '',
          },
          analyzed: false,
          available: false,
          shared: false,
          privacyReviewed: false,
          favorite: false,
          disabled: false,
          preference: 0,
          usageCount: 0,
          createdAt: '2026-01-01T00:00:00Z',
        }),
      )
    }
  })()
  await new Promise<void>((resolve, reject) => {
    const failed = (error: Error) => reject(error)
    server.once('error', failed)
    server.listen(0, '127.0.0.1', () => {
      server.removeListener('error', failed)
      resolve()
    })
  })
  const address = server.address()
  assert.ok(address && typeof address !== 'string')
  const base = `http://127.0.0.1:${address.port}`
  const delay = monitorEventLoopDelay({ resolution: 10 })
  delay.enable()
  cleanup.push(() => {
    delay.disable()
  })
  const started = performance.now(),
    cpu = process.cpuUsage()
  let peakRss = process.memoryUsage().rss
  const watchdog = setInterval(() => {
    peakRss = Math.max(peakRss, process.memoryUsage().rss)
    if (peakRss > 768 * 1024 * 1024 || performance.now() - started > 120000) {
      failure ??= new Error('benchmark_resource_budget_exceeded')
      stop.abort()
    }
  }, 20)
  cleanup.push(() => clearInterval(watchdog))
  const samples: Record<string, number[]> = {}
  const record = (name: string, value: number) => (samples[name] ??= []).push(value)
  let ingestDone = false,
    completed = 0
  http = (async () => {
    while (!stop.signal.aborted && (!ingestDone || completed < 50))
      for (const path of [
        '/api/settings',
        '/api/scenes',
        '/api/status',
        '/api/memes?pageSize=12',
      ]) {
        if (stop.signal.aborted) return
        const start = performance.now()
        const response = await fetch(base + path, {
          signal: AbortSignal.any([stop.signal, AbortSignal.timeout(10000)]),
        })
        await response.arrayBuffer()
        assert.equal(response.status, 200)
        record(path, performance.now() - start)
        completed++
      }
  })().catch((error: unknown) => {
    failure ??= error
    stop.abort()
  })
  for (let n = 0; n < 2000; n++) {
    if (failure) throw failure
    const target = scene(n % 100),
      at = new Date().toISOString(),
      start = performance.now()
    engine.runtime.ingest(
      {
        id: `message-${n}`,
        sceneId: target.id,
        senderId: `person-${n % 20}`,
        senderPlatformId: String(300001 + (n % 20)),
        senderDisplayName: 'Synthetic sender',
        sentAt: at,
        receivedAt: at,
        origin: 'live',
        status: 'available',
        parts: [{ type: 'text', text: 'Synthetic offline ingress performance fixture.' }],
      },
      target,
    )
    record(n < 1000 ? 'ingress_first_1000' : 'ingress_last_1000', performance.now() - start)
    // Adapter events yield between arrivals; bursts of five stress the same event loop.
    if (n % 5 === 4) await new Promise((resolve) => setImmediate(resolve))
  }
  ingestDone = true
  await http
  if (failure) throw failure
  await new Promise((resolve) => setTimeout(resolve, 100))
  if (failure) throw failure
  assert.equal(modelCalls, 0)
  const summary = Object.fromEntries(
    Object.entries(samples).map(([key, values]) => {
      values.sort((a, b) => a - b)
      return [
        key,
        {
          n: values.length,
          p50Ms: values[Math.floor(values.length * 0.5)],
          p95Ms: values[Math.ceil(values.length * 0.95) - 1],
          maxMs: values.at(-1),
        },
      ]
    }),
  )
  await writeFile(
    output,
    JSON.stringify(
      {
        kind: 'persistent_engine_ingress_reply_learning_off',
        scenes: 100,
        catalogRows: 5000,
        messages: 2000,
        modelCalls,
        elapsedMs: performance.now() - started,
        cpu: process.cpuUsage(cpu),
        peakRssBytes: peakRss,
        eventLoopP95Ms: delay.percentile(95) / 1e6,
        eventLoopMaxMs: delay.max / 1e6,
        timings: summary,
      },
      null,
      2,
    ),
  )
} finally {
  const errors: unknown[] = []
  for (const close of cleanup.reverse()) {
    try {
      await close()
    } catch (error) {
      errors.push(error)
    }
  }
  if (previousLogDirectory === undefined) delete process.env.HUMANENGINE_LOG_DIR
  else process.env.HUMANENGINE_LOG_DIR = previousLogDirectory
  if (previousAuthority === undefined) delete process.env.HUMANENGINE_RECOVERY_AUTHORITY_DIRECTORY
  else process.env.HUMANENGINE_RECOVERY_AUTHORITY_DIRECTORY = previousAuthority
  await rm(root, { recursive: true, force: true })
  if (errors.length) throw new AggregateError(errors, 'benchmark_cleanup_failed')
}
