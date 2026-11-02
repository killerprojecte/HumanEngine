import assert from 'node:assert/strict'
import test from 'node:test'
import {
  existsSync,
  mkdtempSync,
  mkdirSync,
  readFileSync,
  readdirSync,
  rmSync,
  writeFileSync,
} from 'node:fs'
import { tmpdir } from 'node:os'
import { dirname, join } from 'node:path'
import { Store } from '../src/store.ts'
import { withMemoryStatementBenchmarkFixture } from '../scripts/memory-support-statements-benchmark.ts'

async function sentinels(operation: () => Promise<void>) {
  const root = mkdtempSync(join(tmpdir(), 'he-benchmark-operator-sentinel-'))
  const originalAuthority = process.env.HUMANENGINE_RECOVERY_AUTHORITY_DIRECTORY
  const originalLogs = process.env.HUMANENGINE_LOG_DIR
  const authority = join(root, 'operator-authority'),
    logs = join(root, 'operator-logs')
  mkdirSync(authority)
  mkdirSync(logs)
  writeFileSync(join(authority, 'sentinel'), 'unchanged authority')
  writeFileSync(join(logs, 'sentinel'), 'unchanged logs')
  process.env.HUMANENGINE_RECOVERY_AUTHORITY_DIRECTORY = authority
  process.env.HUMANENGINE_LOG_DIR = logs
  try {
    await operation()
    assert.equal(process.env.HUMANENGINE_RECOVERY_AUTHORITY_DIRECTORY, authority)
    assert.equal(process.env.HUMANENGINE_LOG_DIR, logs)
    assert.deepEqual(readdirSync(authority), ['sentinel'])
    assert.deepEqual(readdirSync(logs), ['sentinel'])
    assert.equal(readFileSync(join(authority, 'sentinel'), 'utf8'), 'unchanged authority')
    assert.equal(readFileSync(join(logs, 'sentinel'), 'utf8'), 'unchanged logs')
  } finally {
    if (originalAuthority === undefined) delete process.env.HUMANENGINE_RECOVERY_AUTHORITY_DIRECTORY
    else process.env.HUMANENGINE_RECOVERY_AUTHORITY_DIRECTORY = originalAuthority
    if (originalLogs === undefined) delete process.env.HUMANENGINE_LOG_DIR
    else process.env.HUMANENGINE_LOG_DIR = originalLogs
    rmSync(root, { recursive: true, force: true })
  }
}

test('benchmark fixtures isolate inherited authority/log paths and clean up on success and workload failure', async () => {
  await sentinels(async () => {
    for (const fail of [false, true]) {
      let directory = '',
        database: Store['db'] | undefined,
        disposed = false
      const operation = withMemoryStatementBenchmarkFixture('file', (store, memory) => {
        directory = dirname(process.env.HUMANENGINE_RECOVERY_AUTHORITY_DIRECTORY!)
        assert.equal(process.env.HUMANENGINE_LOG_DIR, join(directory, 'logs'))
        assert.equal(store.db.name, join(directory, 'live', 'synthetic.sqlite'))
        database = store.db
        const dispose = memory.dispose.bind(memory)
        memory.dispose = () => {
          disposed = true
          dispose()
        }
        store.log('info', 'benchmark', 'synthetic_isolation_probe')
        if (fail) throw new Error('synthetic workload failure')
      })
      if (fail) await assert.rejects(operation, /synthetic workload failure/)
      else await operation
      assert.equal(disposed, true)
      assert.equal(database!.open, false)
      assert.equal(existsSync(directory), false)
    }
  })
})

test('benchmark fixture restores paths and removes temporary state when setup throws', async () => {
  await sentinels(async () => {
    let directory = ''
    await assert.rejects(
      withMemoryStatementBenchmarkFixture(
        'file',
        () => assert.fail('workload must not run'),
        () => {
          directory = dirname(process.env.HUMANENGINE_RECOVERY_AUTHORITY_DIRECTORY!)
          throw new Error('synthetic store setup failure')
        },
      ),
      /synthetic store setup failure/,
    )
    assert.equal(existsSync(directory), false)
    let database: Store['db'] | undefined
    await assert.rejects(
      withMemoryStatementBenchmarkFixture(
        'file',
        () => assert.fail('workload must not run'),
        (path) => {
          directory = dirname(process.env.HUMANENGINE_RECOVERY_AUTHORITY_DIRECTORY!)
          const store = new Store(path)
          database = store.db
          Object.defineProperty(store, 'sourceEvents', {
            get() {
              throw new Error('synthetic service setup failure')
            },
          })
          return store
        },
      ),
      /synthetic service setup failure/,
    )
    assert.equal(database!.open, false)
    assert.equal(existsSync(directory), false)
  })
})
