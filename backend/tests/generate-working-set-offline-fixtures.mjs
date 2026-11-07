/** Actual old-file backup and quarantined working-set recovery domain.
 * No reconcile, ACK or ready transition is performed by this oracle. */
import assert from 'node:assert/strict'
import Database from 'better-sqlite3'
import { createHash } from 'node:crypto'
import { mkdtempSync, mkdirSync, copyFileSync, rmSync, readFileSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { WorkingSetStorage } from '../../src/memory/working-set/storage.ts'
import {
  createRecoveryWorkingSetPort,
  recordWorkingSetPin,
} from '../../src/memory/recovery/working-set.ts'
import {
  RecoveryAuthority,
  bindRecovery,
  saveGate,
  metadata,
  maintenanceGate,
  revoke,
} from '../../src/memory/recovery/ledger.ts'
assert.equal(process.version, 'v24.22.1')
const policy = (id = 'a', extra = {}) => ({
  schemaVersion: 1,
  sceneId: 'scene',
  selection: {
    ref: { kind: 'assertion', id, version: 1, deletionEpoch: 0 },
    dependencyKey: 'a'.repeat(64),
  },
  policyRevision: 1,
  pinned: true,
  terminal: false,
  ...extra,
})
const tables = (db) =>
  Object.fromEntries(
    db
      .prepare(
        "SELECT name FROM sqlite_master WHERE type='table' AND name GLOB 'memory_working_set_*' ORDER BY name",
      )
      .all()
      .map(({ name }) => [name, db.prepare(`SELECT * FROM "${name}" ORDER BY rowid`).all()]),
  )
const schema = (db) =>
  db
    .prepare(
      "SELECT type,name,sql FROM sqlite_master WHERE name GLOB 'memory_working_set_*' AND sql IS NOT NULL ORDER BY rowid",
    )
    .all()
const scenarios = []
async function scenario(name, config = {}) {
  const root = mkdtempSync(join(process.cwd(), 'backend/.working-set-offline-oracle-'))
  let db, restored, ledger, external
  try {
    for (const dir of ['live', 'older', 'staging']) mkdirSync(join(root, dir))
    db = new Database(join(root, 'live', 'main.sqlite'))
    db.pragma('journal_mode=WAL')
    ledger = new RecoveryAuthority(join(root, 'authority'), '12345678-1234-4123-8123-123456789012')
    saveGate(db, { head: ledger.head(), authority: ledger.path, minimumReader: 1, state: 'ready' })
    bindRecovery(db, ledger)
    const storage = new WorkingSetStorage(db)
    if (config.seed !== false) {
      db.transaction(() => storage.ensure())()
      for (const p of config.pins ?? [policy()]) db.transaction(() => storage.putPin(p))()
      if (config.soft !== false) {
        const snapshot = {
          ...policy().selection,
          sceneId: 'scene',
          recordedAt: 1,
          protection: null,
        }
        db.prepare('INSERT INTO memory_working_set_items VALUES(?,?,?,?)').run(
          'scene',
          'assertion',
          'a',
          JSON.stringify({
            schemaVersion: 1,
            snapshot,
            metadataRevision: 1,
            contextSelectedCount: 7,
            lastSelectedAt: 2,
            projectedAt: 2,
          }),
        )
        db.prepare('INSERT INTO memory_working_set_jobs VALUES(?,?,?,?)').run(
          'scene',
          'assertion',
          'a',
          JSON.stringify({
            schemaVersion: 1,
            snapshot,
            generation: '11111111-1111-4111-8111-111111111111',
            enqueuedAt: 2,
            lease: null,
          }),
        )
        db.prepare('INSERT INTO memory_working_set_receipts VALUES(?,?,?)').run(
          'receipt',
          2,
          JSON.stringify({
            schemaVersion: 1,
            sceneId: 'scene',
            turnId: 'turn',
            selection: policy().selection,
            selectedAt: 2,
          }),
        )
      }
    }
    if (config.beforeBackup) db.exec(config.beforeBackup)
    const initial = tables(db),
      initialSchema = schema(db)
    const initialUnrelatedSchema = db
      .prepare(
        "SELECT type,name,sql FROM sqlite_master WHERE name='memoryXworkingXsetXsurprise' ORDER BY rowid",
      )
      .all()
    await db.backup(join(root, 'older', 'main.sqlite'))
    const inputEvents = config.events ?? [
      { kind: 'working_set_pin', schemaVersion: 1, policy: policy() },
    ]
    for (const event of inputEvents)
      db.transaction(() =>
        event.kind === 'working_set_pin'
          ? recordWorkingSetPin(db, event.policy)
          : revoke(db, event),
      )()
    external = new Database(join(ledger.path, 'authority.sqlite'), { readonly: true })
    const events = external
      .prepare('SELECT payload FROM revocations ORDER BY sequence')
      .all()
      .map((row) => row.payload)
    copyFileSync(join(root, 'older', 'main.sqlite'), join(root, 'staging', 'main.sqlite'))
    restored = new Database(join(root, 'staging', 'main.sqlite'))
    saveGate(restored, { ...metadata(restored), state: 'quarantine' })
    maintenanceGate(restored, restored.name, ledger)
    const calls = []
    const port = createRecoveryWorkingSetPort(
      { db: restored, recoveryQuarantined: true },
      config.noResolver
        ? undefined
        : {
            resolvePinTarget(p) {
              calls.push(p)
              if (config.target === 'throw') throw Error('synthetic_target_failure')
              return config.target ?? 'current'
            },
          },
    )
    const trace = []
    for (const op of config.ops ?? [
      { kind: 'assert' },
      { kind: 'reset' },
      { kind: 'verify' },
      { kind: 'verify' },
    ]) {
      calls.length = 0
      let result, error
      try {
        const run = () => {
          if (op.sql) restored.exec(op.sql)
          if (op.kind === 'assert') return port.assertSupported()
          if (op.kind === 'reset') port.reset()
          else if (op.kind === 'resetVerify') {
            port.reset()
            port.verifyPins(events.map(JSON.parse))
          } else if (op.kind === 'verify') port.verifyPins(events.map(JSON.parse))
          if (op.rollback) throw Error('synthetic_outer_rollback')
        }
        result = op.outside ? run() : restored.transaction(run)()
      } catch (e) {
        error = e.name === 'ZodError' ? 'working_set_schema_invalid' : e.message
      }
      trace.push({
        ...(error ? { error } : result === undefined ? { undefined: true } : { result }),
        calls: structuredClone(calls),
        tables: tables(restored),
        schema: schema(restored),
      })
    }
    assert.equal(metadata(restored).state, 'quarantine')
    assert.deepEqual(
      external
        .prepare('SELECT payload FROM revocations ORDER BY sequence')
        .all()
        .map((row) => row.payload),
      events,
    )
    scenarios.push({ name, config, initial, initialSchema, initialUnrelatedSchema, events, trace })
  } finally {
    external?.close()
    restored?.close()
    db?.close()
    ledger?.close()
    rmSync(root, { recursive: true, force: true })
  }
}
await scenario('reset preserves pins and replay is idempotent')
await scenario('absent old schema lazy replay', { seed: false })
await scenario('absent empty authority remains absent', { seed: false, events: [] })
await scenario('backup only policy rejected', { events: [] })
await scenario('stale target accepted', { target: 'stale' })
await scenario('missing target rolls back reset and verify', { target: 'missing' })
await scenario('optional resolver missing rejects pinned', { noResolver: true })
await scenario('unpinned does not resolve', {
  noResolver: true,
  events: [
    {
      kind: 'working_set_pin',
      schemaVersion: 1,
      policy: policy('a', { pinned: false, policyRevision: 2 }),
    },
  ],
})
await scenario('terminal assertion suppresses pin', {
  events: [
    { kind: 'working_set_pin', schemaVersion: 1, policy: policy() },
    { kind: 'assertion', id: 'a', throughVersion: 1, terminal: true },
  ],
})
await scenario('nonterminal assertion preserves pin', {
  events: [
    { kind: 'assertion', id: 'a', throughVersion: 1, terminal: false },
    { kind: 'working_set_pin', schemaVersion: 1, policy: policy() },
  ],
})
await scenario('first insertion order last value wins', {
  seed: false,
  events: [
    ...['b', 'a', 'b'].map((id, i) => ({
      kind: 'working_set_pin',
      schemaVersion: 1,
      policy: policy(id, { policyRevision: i + 1 }),
    })),
  ],
})
await scenario('caller rollback preserves original metadata', {
  ops: [{ kind: 'reset', rollback: true }, { kind: 'verify', rollback: true }, { kind: 'assert' }],
})
await scenario('reset and replay require transaction', {
  ops: [
    { kind: 'reset', outside: true },
    { kind: 'verify', outside: true },
    { kind: 'assert', outside: true },
  ],
})
await scenario('pin column binding mismatch', {
  beforeBackup: "UPDATE memory_working_set_pins SET entity_id='other'",
})
await scenario('malformed item fails before reset', {
  beforeBackup: "UPDATE memory_working_set_items SET value='{}'",
})
await scenario('unknown extra table rejected', {
  beforeBackup: 'CREATE TABLE memory_working_set_surprise(value TEXT)',
})
await scenario('partial schema rejected', { beforeBackup: 'DROP TABLE memory_working_set_jobs' })
await scenario('glob does not treat underscore as wildcard', {
  beforeBackup: 'CREATE TABLE memoryXworkingXsetXsurprise(value TEXT)',
})
await scenario('reordered columns audit accepted lazy ensure rejects', {
  beforeBackup:
    'ALTER TABLE memory_working_set_pins RENAME TO temporary_pins; CREATE TABLE memory_working_set_pins(value TEXT,entity_id TEXT,kind TEXT,scene_id TEXT,PRIMARY KEY(scene_id,kind,entity_id)); INSERT INTO memory_working_set_pins SELECT value,entity_id,kind,scene_id FROM temporary_pins; DROP TABLE temporary_pins;',
})
const commitmentPolicy = policy('c', {
  selection: {
    ref: { kind: 'commitment', id: 'c', version: 1, deletionEpoch: 0, mutationVersion: 1 },
    dependencyKey: 'b'.repeat(64),
  },
})
await scenario('commitment delete is scene scoped', {
  pins: [commitmentPolicy],
  soft: false,
  events: [
    { kind: 'working_set_pin', schemaVersion: 1, policy: commitmentPolicy },
    { kind: 'commitment_delete', schemaVersion: 1, sceneId: 'other', id: 'c', deletionEpoch: 1 },
  ],
})
await scenario('commitment delete suppresses matching policy', {
  pins: [commitmentPolicy],
  soft: false,
  events: [
    { kind: 'working_set_pin', schemaVersion: 1, policy: commitmentPolicy },
    { kind: 'commitment_delete', schemaVersion: 1, sceneId: 'scene', id: 'c', deletionEpoch: 1 },
  ],
})
await scenario('combined reset replay failure rolls back soft tables', {
  target: 'missing',
  ops: [{ kind: 'resetVerify' }, { kind: 'assert' }],
})
await scenario('resolver error rolls back preceding policy', {
  seed: false,
  target: 'throw',
  events: [
    { kind: 'working_set_pin', schemaVersion: 1, policy: policy('b', { pinned: false }) },
    { kind: 'working_set_pin', schemaVersion: 1, policy: policy('a') },
  ],
})
const files = [
  'src/memory/recovery/working-set.ts',
  'src/memory/recovery/working-set-contract.ts',
  'src/memory/recovery/ledger.ts',
  'src/memory/working-set/contracts.ts',
  'src/memory/working-set/storage.ts',
  'package-lock.json',
]
const output = {
  sourceNode: process.version,
  sources: Object.fromEntries(
    files.map((file) => [file, createHash('sha256').update(readFileSync(file)).digest('hex')]),
  ),
  scenarios,
}
const path = 'backend/internal/workingset/testdata/offline-source-oracle.json',
  contents = JSON.stringify(output, null, 2) + '\n'
if (process.argv.includes('--check')) assert.equal(readFileSync(path, 'utf8'), contents)
else writeFileSync(path, contents)
console.log(`working-set offline oracle: ${scenarios.length} actual-backup scenarios`)
