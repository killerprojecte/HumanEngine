/** Offline matched-checkout fixture for the profiled recall pre-state capture.
 * Seeds valid synthetic SQLite rows directly, without timing fixture construction. */
import assert from 'node:assert/strict'
import { createHash } from 'node:crypto'
import { mkdtempSync, rmSync } from 'node:fs'
import { join, resolve } from 'node:path'
import { tmpdir, cpus } from 'node:os'
import { pathToFileURL } from 'node:url'
import type { MemoryRecord } from '../src/memory/domain/contracts.ts'

const checkout = resolve(process.argv[2] ?? '.')
const chainRows = Number(process.argv[3] ?? 1000)
const records = Number(process.argv[4] ?? 24)
const iterations = Number(process.argv[5] ?? 3)
const phase = process.argv[6] ?? 'capture'
if (
  ![0, 100, 1000].includes(chainRows) ||
  ![24, 100].includes(records) ||
  !Number.isInteger(iterations) ||
  iterations < 1 ||
  iterations > 5 ||
  !['capture', 'recall', 'partition'].includes(phase)
)
  throw new Error('invalid_bounded_benchmark_size')
const { Store } = (await import(
  pathToFileURL(join(checkout, 'src/store.ts')).href
)) as typeof import('../src/store.ts')
const { MemoryService } = (await import(
  pathToFileURL(join(checkout, 'src/memory.ts')).href
)) as typeof import('../src/memory.ts')
const { RecoveryAuthority, metadata, saveGate } = (await import(
  pathToFileURL(join(checkout, 'src/memory/recovery/ledger.ts')).href
)) as typeof import('../src/memory/recovery/ledger.ts')
const root = mkdtempSync(join(tmpdir(), 'recall-capture-benchmark-'))
const previousAuthority = process.env.HUMANENGINE_RECOVERY_AUTHORITY_DIRECTORY
const previousLogs = process.env.HUMANENGINE_LOG_DIR
const originalEntries = RecoveryAuthority.prototype.entries
let store: InstanceType<typeof Store> | undefined
let memory: InstanceType<typeof MemoryService> | undefined
let authority: InstanceType<typeof RecoveryAuthority> | undefined
try {
  process.env.HUMANENGINE_RECOVERY_AUTHORITY_DIRECTORY = join(root, 'authority')
  process.env.HUMANENGINE_LOG_DIR = join(root, 'logs')
  store = new Store(join(root, 'data', 'synthetic.sqlite'))
  memory = new MemoryService(store)
  store.saveScene({
    id: 's',
    name: 'Synthetic',
    kind: 'group',
    adapterId: 'offline',
    accountId: 'offline',
    platformSceneId: 's',
    participationEnabled: false,
    learningEnabled: false,
    proactiveEnabled: false,
  })
  store.db.transaction(() => {
    const insert = store!.db.prepare('INSERT INTO memories VALUES (?,?,?,?,?,?)')
    const support = store!.db.prepare('INSERT INTO memory_supports VALUES (?,?)')
    const state = store!.db.prepare('INSERT INTO memory_assertion_state VALUES (?,?,?,?)')
    for (let i = 0; i < records; i++) {
      const source = { sceneId: 's', messageId: `source-${i}`, senderId: 'synthetic' }
      const record: MemoryRecord = {
        id: `record-${i}`,
        scope: { kind: 'scene', sceneId: 's' },
        content: `Synthetic ${i}`,
        kind: 'fact',
        importance: 'medium',
        confidence: 1,
        locked: false,
        sources: [source],
        learnedAt: '2026-01-01T00:00:00.000Z',
        status: 'active',
        origin: 'learned',
        lineageStatus: 'verified',
      }
      insert.run(record.id, 'scene:s', null, record.content, record.id, JSON.stringify(record))
      support.run(record.id, JSON.stringify([{ sources: [source], parents: [] }]))
      state.run(record.id, 1, 0, 0)
    }
  })()
  const gate = metadata(store.db)!
  authority = new RecoveryAuthority(gate.authority)
  let head = authority.head()
  authority.db.transaction(() => {
    const insert = authority!.db.prepare('INSERT INTO revocations VALUES(?,?,?,?)')
    for (let i = 0; i < chainRows; i++) {
      const payload = JSON.stringify(
        i % 2
          ? { kind: 'source', sceneId: 'other', messageId: `seed-${i}` }
          : {
              kind: 'source_edit_head',
              schemaVersion: 1,
              sceneId: 'other',
              messageId: `seed-${i}`,
              eventId: `event-${i}`,
              revision: 1,
              deletionEpoch: 0,
              identityHash: 'a'.repeat(64),
              headHash: 'b'.repeat(64),
            },
      )
      const sequence = head.sequence + 1
      const hash = createHash('sha256')
        .update(`${head.instanceId}|${sequence}|${head.hash}|${payload}`)
        .digest('hex')
      insert.run(sequence, head.hash, payload, hash)
      head = { instanceId: head.instanceId, sequence, hash }
    }
  })()
  saveGate(store.db, { ...gate, head })
  store.assertRecoveryReady()
  let scans = 0
  RecoveryAuthority.prototype.entries = function (collect) {
    scans++
    return originalEntries.call(this, collect)
  }
  const samples = []
  for (let i = 0; i <= iterations; i++) {
    scans = 0
    const start = performance.now(),
      beforeCpu = process.cpuUsage()
    const heartbeat = new Promise<number>((done) =>
      setTimeout(() => done(performance.now() - start), 0),
    )
    if (phase === 'capture')
      store.db.transaction(() =>
        assert.equal(typeof memory!.prepareRecall('s', 'unrelated'), 'function'),
      )()
    else if (phase === 'partition')
      assert.equal(
        memory.semanticNeighborhood.partitionKey({ sceneId: 's', simulation: false }, null),
        undefined,
      )
    else assert.deepEqual(memory.recall('s', `unrelated-${i}`), [])
    const wallMs = performance.now() - start,
      cpu = process.cpuUsage(beforeCpu)
    const heartbeatMs = await heartbeat
    assert.equal(store.db.inTransaction, false)
    assert.equal(store.db.pragma('query_only', { simple: true }), 0)
    assert.equal(
      (store.db.prepare('SELECT COUNT(*) n FROM memories').get() as { n: number }).n,
      records,
    )
    if (i) samples.push({ wallMs, cpuMs: (cpu.user + cpu.system) / 1000, heartbeatMs, scans })
  }
  console.log(
    JSON.stringify({
      checkout,
      node: process.version,
      cpu: cpus()[0]?.model,
      synthetic: true,
      phase,
      chainRows,
      records,
      iterations,
      samples,
      note: 'Synthetic sourced assertions; zero semantic candidates. Capture retains outer transaction. Recall phase includes full no-dependent recall; partition runs the semantic partition list path and rejects the >12 declaration fixture. Not production latency or websocket end-to-end.',
    }),
  )
} finally {
  RecoveryAuthority.prototype.entries = originalEntries
  try {
    memory?.dispose()
  } finally {
    try {
      authority?.close()
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
          rmSync(root, { recursive: true, force: true })
        }
      }
    }
  }
}
