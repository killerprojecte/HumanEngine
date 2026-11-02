/** Synthetic fixed global refresh, comparing identical reads with/without local compiled handles. */
import { mkdtempSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { pathToFileURL } from 'node:url'
import { Store } from '../src/store.ts'
import { MemoryService } from '../src/memory.ts'
import { AssertionRepository } from '../src/memory/storage/assertion-repository.ts'
import type { MemoryRecord } from '../src/memory/domain/contracts.ts'

/** Fixture scope also protects setup failures; inherited operator paths are never used.
 * The store factory permits a small deterministic setup-failure test without running
 * the benchmark workload. Fixtures run serially because process.env is process-wide.
 */
export async function withMemoryStatementBenchmarkFixture<T>(
  persistence: 'memory' | 'file',
  operation: (store: Store, memory: MemoryService) => T | Promise<T>,
  createStore: (path: string) => Store = (path) => new Store(path),
): Promise<T> {
  const directory = mkdtempSync(join(tmpdir(), 'he-support-statements-'))
  const previousAuthority = process.env.HUMANENGINE_RECOVERY_AUTHORITY_DIRECTORY
  const previousLogs = process.env.HUMANENGINE_LOG_DIR
  const previousScope = AssertionRepository.prototype.withSupportReadStatements
  let store: Store | undefined
  let memory: MemoryService | undefined
  try {
    process.env.HUMANENGINE_RECOVERY_AUTHORITY_DIRECTORY = join(directory, 'authority')
    process.env.HUMANENGINE_LOG_DIR = join(directory, 'logs')
    store = createStore(
      persistence === 'memory' ? ':memory:' : join(directory, 'live', 'synthetic.sqlite'),
    )
    memory = new MemoryService(store)
    return await operation(store, memory)
  } finally {
    AssertionRepository.prototype.withSupportReadStatements = previousScope
    try {
      memory?.dispose()
    } finally {
      try {
        if (store) await store.closeLogs()
      } finally {
        try {
          store?.close()
        } finally {
          if (previousAuthority === undefined)
            delete process.env.HUMANENGINE_RECOVERY_AUTHORITY_DIRECTORY
          else process.env.HUMANENGINE_RECOVERY_AUTHORITY_DIRECTORY = previousAuthority
          if (previousLogs === undefined) delete process.env.HUMANENGINE_LOG_DIR
          else process.env.HUMANENGINE_LOG_DIR = previousLogs
          rmSync(directory, { recursive: true, force: true })
        }
      }
    }
  }
}

async function benchmark() {
  const originalScope = AssertionRepository.prototype.withSupportReadStatements
  for (const persistence of ['memory', 'file'] as const)
    await withMemoryStatementBenchmarkFixture(persistence, (store, memory) => {
      const refresh = () =>
        (
          memory as unknown as { lifecycleCommands: { refreshSupport(): string[] } }
        ).lifecycleCommands.refreshSupport()
      const insert = store.db.prepare('INSERT INTO memories VALUES (?,?,?,?,?,?)')
      const proof = store.db.prepare('INSERT INTO memory_supports VALUES (?,?)')
      store.db.transaction(() => {
        for (let i = 0; i <= 1000; i++) {
          const source = {
            sceneId: 'synthetic',
            messageId: `source-${i}`,
            senderId: 'synthetic-person',
          }
          const record: MemoryRecord = {
            id: `synthetic-${i}`,
            scope: { kind: 'scene', sceneId: 'synthetic' },
            content: `Synthetic shared-parent fixture ${i} ${'x'.repeat(512)}`,
            kind: 'fact',
            importance: 'medium',
            confidence: 1,
            locked: false,
            sources: i ? [source] : [],
            learnedAt: '2026-01-01T00:00:00.000Z',
            status: 'active',
            origin: i ? 'learned' : 'manual',
            ...(i ? { lineageStatus: 'verified' as const } : {}),
          }
          insert.run(
            record.id,
            'scene:synthetic',
            null,
            record.content,
            `synthetic-${i}`,
            JSON.stringify(record),
          )
          if (i)
            proof.run(record.id, JSON.stringify([{ sources: [source], parents: ['synthetic-0'] }]))
        }
      })()
      const timings = { baseline: [] as number[], reuse: [] as number[] }
      for (let pair = 0; pair < 7; pair++)
        for (const mode of (pair % 2 ? ['reuse', 'baseline'] : ['baseline', 'reuse']) as (
          'baseline' | 'reuse'
        )[]) {
          AssertionRepository.prototype.withSupportReadStatements =
            mode === 'reuse'
              ? originalScope
              : function <T>(operation: () => T): T {
                  return operation()
                }
          const before = process.cpuUsage()
          const result = refresh()
          if (result.length) throw new Error('synthetic_fixture_unexpected_invalidation')
          const cpu = process.cpuUsage(before)
          timings[mode].push((cpu.user + cpu.system) / 1000)
        }
      for (const values of Object.values(timings)) values.sort((a, b) => a - b)
      const prepares = { baseline: { record: 0, proof: 0 }, reuse: { record: 0, proof: 0 } }
      const originalPrepare = store.db.prepare.bind(store.db)
      for (const mode of ['baseline', 'reuse'] as const) {
        AssertionRepository.prototype.withSupportReadStatements =
          mode === 'reuse'
            ? originalScope
            : function <T>(operation: () => T): T {
                return operation()
              }
        store.db.prepare = ((sql: string) => {
          if (sql === 'SELECT * FROM memories WHERE id=?') prepares[mode].record++
          if (sql === 'SELECT value FROM memory_supports WHERE memory_id=?') prepares[mode].proof++
          return originalPrepare(sql)
        }) as typeof store.db.prepare
        refresh()
      }
      store.db.prepare = originalPrepare
      console.log(
        JSON.stringify({
          persistence,
          records: 1001,
          parentEdges: 1000,
          cpuMedianMs: { baseline: timings.baseline[3], reuse: timings.reuse[3] },
          ratio: timings.baseline[3]! / timings.reuse[3]!,
          prepares,
          note: 'Actual complete refreshSupport; unchanged SQL executions, JSON parses, recovery gates, source checks and root order.',
        }),
      )
    })
}

if (process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href) await benchmark()
