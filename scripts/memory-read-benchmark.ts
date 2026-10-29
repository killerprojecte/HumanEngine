import assert from 'node:assert/strict'
import { mkdtemp, open, rm, stat, writeFile } from 'node:fs/promises'
import { tmpdir, cpus } from 'node:os'
import { join, resolve } from 'node:path'
import { pathToFileURL } from 'node:url'
import { performance } from 'node:perf_hooks'
import { Store } from '../src/store.ts'
import { Engine } from '../src/engine.ts'
import type { RuntimeExtension, TurnContext } from '../src/runtime.ts'
import type { Router } from '../src/models.ts'

/** Offline bounded read-path sample, not a production latency or tokenizer benchmark. */
export async function memoryReadBenchmark(rows: number, iterations = 3, progressPath?: string) {
  if (
    ![1000, 2000].includes(rows) ||
    !Number.isInteger(iterations) ||
    iterations < 1 ||
    iterations > 5
  )
    throw new Error('invalid_bounded_benchmark_size')
  const root = await mkdtemp(join(tmpdir(), 'he-memory-read-benchmark-'))
  const store = new Store(join(root, 'data', 'humanengine.sqlite')),
    engine = new Engine(store)
  const originalFetch = globalThis.fetch
  let networkCalls = 0
  globalThis.fetch = async () => {
    networkCalls++
    throw new Error('benchmark_network_forbidden')
  }
  let modelCalls = 0
  engine.router = () =>
    ({
      complete: async () => {
        modelCalls++
        throw new Error('benchmark_network_forbidden')
      },
    }) as unknown as Router
  const scene = {
    id: 'synthetic-benchmark',
    adapterId: 'offline',
    accountId: 'offline',
    platformSceneId: 'synthetic',
    kind: 'group' as const,
  }
  store.saveScene({
    ...scene,
    name: 'Synthetic benchmark',
    participationEnabled: false,
    learningEnabled: false,
    proactiveEnabled: false,
  })
  const ctx = { sceneId: scene.id, simulation: false }
  const extension = (
    engine.runtime as unknown as { extensions: RuntimeExtension[] }
  ).extensions.find((e) => e.tools.some((t) => t.name === 'memory.search'))!
  const turn = (): TurnContext => ({
    scene,
    mode: 'real',
    turnId: crypto.randomUUID(),
    signal: new AbortController().signal,
    dependencies: new Set(),
    validations: [],
  })
  const sizes = async () => {
    let total = 0
    for (const suffix of ['', '-wal', '-shm'])
      total += await stat(store.db.name + suffix).then(
        (s) => s.size,
        () => 0,
      )
    return total
  }
  const measurements: Array<{
    operation: string
    milliseconds: number[]
    serializedUtf8Bytes: number[]
    resultCount: number[]
  }> = []
  const measure = async (
    operation: string,
    run: () => unknown | Promise<unknown>,
    count: (x: any) => number,
  ) => {
    const entry = {
      operation,
      milliseconds: [] as number[],
      serializedUtf8Bytes: [] as number[],
      resultCount: [] as number[],
    }
    for (let i = 0; i < iterations; i++) {
      const start = performance.now(),
        value = await run()
      entry.milliseconds.push(Math.round((performance.now() - start) * 1000) / 1000)
      entry.serializedUtf8Bytes.push(Buffer.byteLength(JSON.stringify(value), 'utf8'))
      entry.resultCount.push(count(value))
    }
    measurements.push(entry)
    if (progressPath)
      await writeFile(
        progressPath,
        JSON.stringify(
          {
            status: 'partial',
            rows,
            iterations,
            completedOperations: measurements,
            memory: process.memoryUsage(),
          },
          null,
          2,
        ) + '\n',
      )
  }
  try {
    const beforeBytes = await sizes(),
      start = performance.now()
    store.db
      .transaction(() => {
        for (let i = 0; i < rows; i++)
          engine.memory.create({
            scope: { kind: 'scene', sceneId: scene.id, personId: `synthetic-person-${i % 10}` },
            content: `Synthetic bounded memory sample ${i}; condition remains unconfirmed.`,
            kind: i % 10 === 0 ? 'experience' : 'claim',
            epistemicKind: 'explicit',
          })
      })
      .immediate()
    const seedMs = performance.now() - start,
      afterSeedBytes = await sizes()
    if (progressPath)
      await writeFile(
        progressPath,
        JSON.stringify(
          {
            status: 'seeded',
            rows,
            seedMs,
            beforeBytes,
            afterSeedBytes,
            memory: process.memoryUsage(),
          },
          null,
          2,
        ) + '\n',
      )
    await measure(
      'authorized-list',
      () => engine.memory.list(ctx),
      (x) => x.length,
    )
    await measure(
      'exact-person-profile',
      () => engine.memory.personProfile(ctx, 'synthetic-person-0'),
      (x) => x?.records?.length ?? x?.memories?.length ?? 0,
    )
    await measure(
      'lexical-search-graph-off',
      () => extension.execute('memory.search', { query: 'bounded memory sample' }, turn()),
      (x) => x.length,
    )
    store.set('settings', {
      ...store.settings,
      memoryGraphEnabled: true,
      memoryGraphShadowEnabled: true,
    })
    await measure(
      'lexical-search-empty-graph-shadow-on',
      () => extension.execute('memory.search', { query: 'bounded memory sample' }, turn()),
      (x) => x.length,
    )
    store.set('settings', {
      ...store.settings,
      memoryWorkingSetEnabled: true,
      memoryEpisodesEnabled: true,
    })
    await measure(
      'working-set-candidate-bound',
      () => engine.memory.workspace.workingSet(ctx, {}),
      (x) => x.items?.length ?? 0,
    )
    await measure(
      'independent-experience-view',
      () => engine.memory.workspace.episodes(ctx, {}),
      (x) => x.items?.length ?? 0,
    )
    assert.equal(modelCalls, 0)
    assert.equal(networkCalls, 0)
    return {
      schemaVersion: 1,
      synthetic: true,
      rows,
      iterations,
      environment: {
        node: process.version,
        platform: process.platform,
        arch: process.arch,
        cpu: cpus()[0]?.model,
        sqlite: store.db.prepare('SELECT sqlite_version() AS v').get(),
      },
      seedMs,
      beforeBytes,
      afterSeedBytes,
      afterReadsBytes: await sizes(),
      modelCalls,
      networkCalls,
      memoryUsage: process.memoryUsage(),
      maxRssKiB: process.resourceUsage().maxRSS,
      measurements,
      limits: [
        'Manual flat assertions across ten people; no deep proof DAG and no populated graph edges.',
        'Graph ON measures the actual shadow path with zero edges; populated graph bounds are tested separately, not performance-proven here.',
        'Three local samples are raw elapsed observations, not production p95 or guaranteed capacity.',
        'Bytes are JSON result serialization, not complete provider framing or tokens.',
        'No Engine.start, model, embedding, real platform or external network invocation.',
      ],
    }
  } finally {
    globalThis.fetch = originalFetch
    await engine.dispose()
    store.close()
    await rm(root, { recursive: true, force: true })
  }
}
if (process.argv[1] && import.meta.url === pathToFileURL(resolve(process.argv[1])).href) {
  const rows = Number(process.argv[2] ?? '1000'),
    destination = process.argv[3]
  // Reserve a unique destination before doing any work; never overwrite an earlier run.
  if (destination) {
    const reserved = await open(destination, 'wx')
    await reserved.close()
    const partial = await open(destination + '.partial', 'wx')
    await partial.close()
  }
  const result = await memoryReadBenchmark(
    rows,
    3,
    destination ? destination + '.partial' : undefined,
  )
  if (destination) await writeFile(destination, JSON.stringify(result, null, 2) + '\n')
  else console.log(JSON.stringify(result, null, 2))
}
