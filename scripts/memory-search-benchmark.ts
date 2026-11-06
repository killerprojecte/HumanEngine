/** Offline synthetic complete-search fixture. An optional checkout path permits
 * matched baseline/candidate runs without modifying either source tree. */
import assert from 'node:assert/strict'
import { createHash } from 'node:crypto'
import { mkdtempSync, rmSync } from 'node:fs'
import { tmpdir, cpus } from 'node:os'
import { join, resolve } from 'node:path'
import { pathToFileURL } from 'node:url'
import type { MemoryRecord } from '../src/memory/domain/contracts.ts'

const checkout = resolve(process.argv[2] ?? '.')
const chainRows = Number(process.argv[3] ?? 1000)
const records = Number(process.argv[4] ?? 24)
const iterations = Number(process.argv[5] ?? 5)
if (
  ![0, 100, 1000].includes(chainRows) ||
  ![24, 100].includes(records) ||
  !Number.isInteger(iterations) ||
  iterations < 1 ||
  iterations > 7
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
const root = mkdtempSync(join(tmpdir(), 'he-search-benchmark-'))
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
    id: 'synthetic',
    name: 'Synthetic search fixture',
    kind: 'group',
    adapterId: 'offline',
    accountId: 'offline',
    platformSceneId: 'offline',
    participationEnabled: false,
    learningEnabled: false,
    proactiveEnabled: false,
  })
  // Direct fixture seeding only: no accounts, model calls, external data or network.
  store.db.transaction(() => {
    const insert = store!.db.prepare('INSERT INTO memories VALUES (?,?,?,?,?,?)')
    const proofs = store!.db.prepare('INSERT INTO memory_supports VALUES (?,?)')
    for (let i = 0; i <= records; i++) {
      const source = { sceneId: 'synthetic', messageId: `source-${i}`, senderId: 'person' }
      const record: MemoryRecord = {
        id: `record-${i}`,
        scope: { kind: 'scene', sceneId: 'synthetic' },
        content: `Synthetic searchable record ${i}`,
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
        record.id,
        JSON.stringify(record),
      )
      if (i) proofs.run(record.id, JSON.stringify([{ sources: [source], parents: ['record-0'] }]))
    }
  })()
  const gate = metadata(store.db)!
  authority = new RecoveryAuthority(gate.authority)
  let head = authority.head()
  // Seed a valid chain without timing the quadratic append setup path.
  authority.db.transaction(() => {
    const insert = authority!.db.prepare('INSERT INTO revocations VALUES(?,?,?,?)')
    for (let i = 0; i < chainRows; i++) {
      const payload = JSON.stringify({ kind: 'source', sceneId: 'other', messageId: `seed-${i}` })
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
    const start = performance.now()
    const beforeCpu = process.cpuUsage()
    const heartbeat = new Promise<number>((done) =>
      setTimeout(() => done(performance.now() - start), 0),
    )
    const result: MemoryRecord[] = memory.search(
      { sceneId: 'synthetic', simulation: false },
      'searchable',
    )
    const wallMs = performance.now() - start
    const cpu = process.cpuUsage(beforeCpu)
    assert.equal(result.length, 20)
    assert.deepEqual(
      result.map((record) => record.id),
      Array.from({ length: 20 }, (_, j) => `record-${records - j}`),
    )
    const heartbeatMs = await heartbeat
    if (i) samples.push({ wallMs, cpuMs: (cpu.user + cpu.system) / 1000, heartbeatMs, scans })
  }
  console.log(
    JSON.stringify({
      checkout,
      node: process.version,
      cpu: cpus()[0]?.model,
      synthetic: true,
      chainRows,
      records: records + 1,
      iterations,
      samples,
      note: 'Complete memory.search including unchanged refreshSupport; fixed shared-parent proof fixture, not production latency.',
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
