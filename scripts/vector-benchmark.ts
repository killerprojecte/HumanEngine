import assert from 'node:assert/strict'
import { mkdtemp, rm } from 'node:fs/promises'
import { tmpdir, cpus } from 'node:os'
import { join, resolve } from 'node:path'
import { pathToFileURL } from 'node:url'
import { performance } from 'node:perf_hooks'
import { createRequire } from 'node:module'
import { Worker } from 'node:worker_threads'
import { Store } from '../src/store.ts'
import { MemoryService } from '../src/memory.ts'

const require = createRequire(import.meta.url)
const stats = (samples: number[]) => {
  const values = [...samples].sort((a, b) => a - b)
  return {
    samples: values.length,
    p50Ms: values[Math.floor((values.length - 1) * 0.5)],
    p95Ms: values[Math.ceil((values.length - 1) * 0.95)],
  }
}
export async function benchmark(rows = 1000, iterations = 30) {
  if (
    !Number.isSafeInteger(rows) ||
    rows < 20 ||
    rows > 10000 ||
    !Number.isSafeInteger(iterations) ||
    iterations < 3 ||
    iterations > 100
  )
    throw new Error('invalid_benchmark_size')
  const directory = await mkdtemp(join(tmpdir(), 'humanengine-vector-'))
  const database = join(directory, 'benchmark.sqlite'),
    store = new Store(database),
    memory = new MemoryService(store)
  let writer: Worker | undefined
  try {
    const spaceId = memory.registerSpace({
      providerId: 'synthetic',
      modelId: 'synthetic-32',
      apiModelName: 'not-a-real-model',
      deployment: 'local-benchmark-v1',
      dimensions: 32,
      preprocessing: 'deterministic-v1',
      normalized: true,
    })
    const expected = new Map<string, string>(),
      counts = new Map<string, number>()
    const query = Array.from({ length: 32 }, (_, i) => (i === 0 ? 1 : 0))
    let state = 42
    const random = () => {
      state = (Math.imul(state, 1664525) + 1013904223) >>> 0
      return state / 4294967296 - 0.5
    }
    const seedStart = performance.now()
    store.db.transaction(() => {
      for (let i = 0; i < rows; i++) {
        const sceneId = i < Math.floor(rows / 10) ? 'small' : 'large'
        const first = !expected.has(sceneId)
        const record = memory.create({
          scope: { kind: 'scene', sceneId },
          content: `Synthetic sample ${i}`,
          kind: 'fact',
        })
        memory.index(
          record.id,
          spaceId,
          first ? query : Array.from({ length: 32 }, (_, d) => (d === 0 ? 0 : random())),
        )
        if (first) expected.set(sceneId, record.id)
        counts.set(sceneId, (counts.get(sceneId) ?? 0) + 1)
      }
    })()
    const seedMs = performance.now() - seedStart
    const measure = (sceneId: string, service = memory) => {
      const start = performance.now()
      const result = service.search({ sceneId, simulation: false }, '', undefined, {
        spaceId,
        vector: query,
        limit: 5,
      })
      const elapsed = performance.now() - start
      assert.equal(result[0]?.id, expected.get(sceneId), 'expected same-scope exact neighbor')
      assert.ok(
        result.every((r) => r.scope.kind === 'scene' && r.scope.sceneId === sceneId),
        'unrelated scenes excluded before ranking',
      )
      return elapsed
    }
    const scopes = []
    for (const sceneId of ['small', 'large']) {
      const firstQueryMs = measure(sceneId)
      const warm = stats(Array.from({ length: iterations }, () => measure(sceneId)))
      const connectionCold = stats(
        Array.from({ length: 5 }, () => {
          const fresh = new Store(database)
          try {
            return measure(sceneId, new MemoryService(fresh))
          } finally {
            fresh.close()
          }
        }),
      )
      scopes.push({ sceneId, rows: counts.get(sceneId), firstQueryMs, connectionCold, warm })
    }
    writer = new Worker(
      `
      const {parentPort,workerData}=require('node:worker_threads');
      const Database=require(workerData.module);
      const db=new Database(workerData.database); db.pragma('busy_timeout=1000');
      let writes=0, failures=0;
      const statement=db.prepare('INSERT INTO config(key,value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value');
      const write=()=>{try {statement.run('benchmark-writer',JSON.stringify(++writes))} catch {failures++}};
      write(); const timer=setInterval(write,2); parentPort.postMessage({ready:true});
      parentPort.on('message',()=>{clearInterval(timer); db.close(); parentPort.postMessage({writes,failures})});
    `,
      { eval: true, workerData: { database, module: require.resolve('better-sqlite3') } },
    )
    await new Promise<void>((resolve, reject) => {
      writer!.once('message', () => resolve())
      writer!.once('error', reject)
    })
    const writeInterference = scopes.map((scope) => ({
      sceneId: scope.sceneId,
      ...stats(Array.from({ length: iterations }, () => measure(scope.sceneId))),
    }))
    const writeStats = await new Promise<{ writes: number; failures: number }>(
      (resolve, reject) => {
        writer!.once('message', resolve)
        writer!.once('error', reject)
        writer!.postMessage('stop')
      },
    )
    await writer.terminate()
    writer = undefined
    return {
      scriptVersion: 1,
      environment: {
        node: process.version,
        platform: process.platform,
        arch: process.arch,
        cpu: cpus()[0]?.model,
        sqlite: (
          store.db.prepare('SELECT sqlite_version() AS version').get() as { version: string }
        ).version,
        sqliteVec: store.vectorVersion,
      },
      synthetic: true,
      dimensions: 32,
      rows,
      iterations,
      seedMs,
      scopes,
      writeInterference,
      writeStats,
      notes: [
        'First query is query-cold after seeding; connectionCold uses five fresh connections. Neither purges OS cache.',
        'Exact scoped search includes JavaScript ranking and JSON decoding.',
        'Concurrent writer updates one config row every 2ms; not a production workload.',
        'Synthetic vectors and local machine timings do not establish real embedding quality or deployment capacity.',
      ],
    }
  } finally {
    if (writer) await writer.terminate()
    store.close()
    await rm(directory, { recursive: true, force: true })
  }
}
if (process.argv[1] && import.meta.url === pathToFileURL(resolve(process.argv[1])).href) {
  const sizes = process.argv.slice(2).length ? process.argv.slice(2).map(Number) : [1000, 10000]
  try {
    for (const size of sizes) console.log(JSON.stringify(await benchmark(size), null, 2))
  } catch {
    console.error('Vector benchmark failed validation or execution.')
    process.exitCode = 1
  }
}
