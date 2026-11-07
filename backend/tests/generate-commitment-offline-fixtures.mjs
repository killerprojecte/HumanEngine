/** Actual older-file backup + source createRecoveryCommitmentPort maintenance.
 * This verifies this domain only; it deliberately does not call reconcileRecovery
 * or mark the restored database ready/ACK the durable authority head.
 */
import assert from 'node:assert/strict'
import Database from 'better-sqlite3'
import { createHash } from 'node:crypto'
import { mkdtempSync, mkdirSync, copyFileSync, rmSync, readFileSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { CommitmentService } from '../../src/memory/commitments/service.ts'
import {
  createRecoveryCommitmentPort,
  recordCommitmentMutation,
} from '../../src/memory/recovery/commitments.ts'
import {
  RecoveryAuthority,
  bindRecovery,
  saveGate,
  metadata,
  maintenanceGate,
  revoke,
} from '../../src/memory/recovery/ledger.ts'
assert.equal(process.version, 'v24.22.1')
const hash = (s) => createHash('sha256').update(s).digest('hex')
const scenarios = []
const source = (id) => ({ sceneId: 'scene', messageId: id, senderId: 'a' })
const proof = (id, parents = []) => ({
  proofId: id,
  sources: [source(id)],
  parents,
  parentReferences: parents.map((id) => ({ id, version: 1, deletionEpoch: 0 })),
  subjectWitness: { kind: 'self', personId: 'a', sourceMessageId: id },
})
const record = (id) => ({
  id,
  scope: { kind: 'scene', sceneId: 'scene', personId: 'a' },
  content: `Synthetic declaration ${id}`,
  kind: 'fact',
  epistemicKind: 'explicit',
  sources: [source(id)],
  importance: 'low',
  confidence: 0.5,
  learnedAt: '2026-10-28T00:00:00.000Z',
  status: 'active',
  locked: false,
  origin: 'learned',
  lineageStatus: 'verified',
})
function schema(db) {
  return db
    .prepare(
      "SELECT type,name,sql FROM sqlite_master WHERE name LIKE 'memory_commitment_%' AND sql IS NOT NULL ORDER BY rowid",
    )
    .all()
}
function tables(db) {
  return Object.fromEntries(
    db
      .prepare(
        "SELECT name FROM sqlite_master WHERE type='table' AND name LIKE 'memory_commitment_%' ORDER BY name",
      )
      .all()
      .map(({ name }) => {
        const n = db.prepare(`PRAGMA table_xinfo("${name}")`).all().length
        return [name, db.prepare(`SELECT * FROM "${name}" ORDER BY 1${n > 1 ? ',2' : ''}`).all()]
      }),
  )
}
const errorMessage = (e) =>
  e?.name === 'ZodError' ? 'commitment_schema_invalid' : (e?.message ?? null)
async function scenario(name, config = {}) {
  const root = mkdtempSync(join(process.cwd(), 'backend/.commitment-offline-oracle-'))
  let db, restored, ledger, external
  try {
    for (const dir of ['live', 'older', 'staging']) mkdirSync(join(root, dir))
    db = new Database(join(root, 'live', 'main.sqlite'))
    db.pragma('journal_mode=WAL')
    ledger = new RecoveryAuthority(join(root, 'authority'), '12345678-1234-4123-8123-123456789012')
    saveGate(db, { head: ledger.head(), authority: ledger.path, minimumReader: 1, state: 'ready' })
    bindRecovery(db, ledger)
    let live = false,
      clock = '2026-10-28T12:00:00.000Z',
      ids = 0
    const views = new Map()
    const authority = {
      enabled: () => true,
      learningEnabled: () => true,
      current: (_, r) => views.get(r.id),
      historical: (_, r) => views.get(r.id),
      people: () => ['a'],
      beforeWrite: (change) => {
        if (live) recordCommitmentMutation(db, change)
      },
    }
    let service, ref
    if (!config.empty) {
      service = new CommitmentService(db, authority, {
        id: () => `commitment-${++ids}`,
        now: () => clock,
      })
      const add = (id, proofs = [proof(id)]) => {
        views.set(id, { record: record(id), proofs })
        return { id, version: 1, deletionEpoch: 0 }
      }
      const ctx = (ref) => ({
        sceneId: 'scene',
        simulation: false,
        provenance: ref ? 'contextual' : 'source-only',
        exposedRefs: ref
          ? [{ ...ref, mutationVersion: service.storage.head(ref.id).mutationVersion }]
          : [],
      })
      const admit = (backing, command, target) =>
        db.transaction(() => service.admit(ctx(target), backing, command))().item.ref
      ref = admit(add('root', [proof('root', ['ancestor']), proof('independent')]), {
        kind: 'propose',
        terms: {
          kind: 'plan',
          title: 'Synthetic immutable plan',
          participantIds: ['a'],
          temporalScope: 'future',
          polarity: 'positive',
          modality: 'asserted',
          conditions: [],
        },
      })
      if (config.terminal) admit(add('private-completion'), { kind: 'complete', target: ref }, ref)
    }
    if (config.beforeBackup) db.exec(config.beforeBackup)
    const initialSchema = schema(db),
      initial = tables(db)
    await db.backup(join(root, 'older', 'main.sqlite'))
    live = true
    clock = '2026-10-28T13:00:00.000Z'
    const liveOps = config.live ?? []
    for (const op of liveOps)
      db.transaction(() => {
        if (op.kind === 'source') {
          revoke(db, { kind: 'source', sceneId: 'scene', messageId: op.message })
          service.purgeSource('scene', op.message)
        } else if (op.kind === 'assertion') {
          revoke(db, { kind: 'assertion', id: op.id, throughVersion: 1, terminal: true })
          service.purgeAssertion(op.id)
        } else if (op.kind === 'delete')
          service.erase({ sceneId: 'scene', simulation: false, provenance: 'source-only' }, ref)
      })()
    const liveRows = tables(db)
    external = new Database(join(ledger.path, 'authority.sqlite'), { readonly: true })
    const entries = external
      .prepare('SELECT sequence,previous_hash,payload,hash FROM revocations ORDER BY sequence')
      .all()
    const events = entries.map((e) => e.payload)
    copyFileSync(join(root, 'older', 'main.sqlite'), join(root, 'staging', 'main.sqlite'))
    restored = new Database(join(root, 'staging', 'main.sqlite'))
    saveGate(restored, { ...metadata(restored), state: 'quarantine' })
    maintenanceGate(restored, restored.name, ledger)
    let factoryCalls = 0,
      fault = '',
      owners = []
    const port = createRecoveryCommitmentPort({ db: restored, recoveryQuarantined: true }, () => {
      factoryCalls++
      return new CommitmentService(
        restored,
        {
          enabled: () => false,
          learningEnabled: () => false,
          current: () => undefined,
          people: () => [],
          beforeWrite: (change) => recordCommitmentMutation(restored, change),
          afterWrite: (head, next, prior) => {
            owners.push(['afterWrite', head, next, prior])
            if (fault === 'afterWrite') throw Error('synthetic_afterWrite')
          },
        },
        {
          now: () => {
            throw Error('offline_clock_called')
          },
          id: () => {
            throw Error('offline_id_called')
          },
        },
      )
    })
    const steps = []
    for (const input of typeof config.ops === 'function'
      ? config.ops(service?.snapshot(ref.id), events)
      : (config.ops ?? [])) {
      const op = structuredClone(input)
      fault = op.fault ?? ''
      owners = []
      if (op.sql) restored.exec(op.sql)
      if (op.outerRollback) restored.exec('BEGIN IMMEDIATE')
      let error = null,
        result = null
      try {
        if (op.kind === 'assertSupported') result = port.assertSupported()
        else if (op.kind === 'recall') port.recall(op.scene ?? 'scene', op.message)
        else if (op.kind === 'purgeAssertion') port.purgeAssertion(op.id)
        else if (op.kind === 'erase')
          port.erase(
            JSON.parse(
              op.raw ?? events.find((raw) => JSON.parse(raw).kind === 'commitment_delete'),
            ),
          )
        else if (op.kind === 'marker')
          port.replayMarker(
            JSON.parse(op.raw ?? events.find((raw) => JSON.parse(raw).kind === 'commitment_head')),
          )
        else if (op.kind === 'verify')
          port.verify((op.events ?? events).map((raw) => JSON.parse(raw)))
        else throw Error('unknown_offline_oracle_action')
      } catch (e) {
        error = errorMessage(e)
      } finally {
        if (op.outerRollback) restored.exec('ROLLBACK')
      }
      const gate = metadata(restored)
      steps.push({
        op: JSON.stringify(op),
        result,
        error,
        rows: JSON.stringify(tables(restored)),
        schema: JSON.stringify(schema(restored)),
        owners: JSON.stringify(owners),
        factoryCalls,
        gate: { head: gate.head, minimumReader: gate.minimumReader, state: gate.state },
        authority: JSON.stringify(
          external
            .prepare(
              'SELECT sequence,previous_hash,payload,hash FROM revocations ORDER BY sequence',
            )
            .all(),
        ),
      })
    }
    scenarios.push({
      name,
      initialSchema,
      initial: JSON.stringify(initial),
      liveOps,
      liveRows: JSON.stringify(liveRows),
      entries: JSON.stringify(entries),
      steps,
    })
  } finally {
    restored?.close()
    external?.close()
    db?.close()
    ledger?.close()
    rmSync(root, { recursive: true, force: true })
  }
}
await scenario('older-backup-source-marker-exact-qualification', {
  terminal: true,
  live: [{ kind: 'source', message: 'private-completion' }],
  ops: [
    { kind: 'assertSupported' },
    { kind: 'verify' },
    { kind: 'marker' },
    { kind: 'recall', message: 'private-completion' },
    { kind: 'verify' },
    { kind: 'marker' },
    { kind: 'verify' },
    { kind: 'recall', message: 'private-completion' },
    { kind: 'marker' },
    { kind: 'verify' },
  ],
})
await scenario('older-backup-ancestor-original-or', {
  live: [{ kind: 'assertion', id: 'ancestor' }],
  ops: [{ kind: 'purgeAssertion', id: 'ancestor' }, { kind: 'marker' }, { kind: 'verify' }],
})
await scenario('older-backup-terminal-erase-and-orphan-cleanup', {
  terminal: true,
  live: [{ kind: 'delete' }],
  ops: [
    { kind: 'erase' },
    { kind: 'verify' },
    {
      kind: 'verify',
      sql: "INSERT INTO memory_commitment_commands VALUES('orphan','commitment-1',99)",
    },
    { kind: 'erase' },
    { kind: 'verify' },
  ],
})
await scenario('missing-index-first-verify', {
  beforeBackup: 'DROP INDEX memory_commitment_scene',
  ops: [
    { kind: 'assertSupported' },
    { kind: 'verify' },
    { kind: 'recall', message: 'root', fault: 'afterWrite' },
  ],
})
await scenario('missing-index-first-recall-failure', {
  beforeBackup: 'DROP INDEX memory_commitment_scene',
  ops: [
    { kind: 'recall', message: 'root', fault: 'afterWrite' },
    { kind: 'assertSupported' },
    { kind: 'verify' },
  ],
})
await scenario('missing-index-outer-rollback-cached-constructor', {
  beforeBackup: 'DROP INDEX memory_commitment_scene',
  ops: [
    { kind: 'recall', message: 'root', fault: 'afterWrite', outerRollback: true },
    { kind: 'verify' },
  ],
})
const missingDelete = JSON.stringify({
  schemaVersion: 1,
  sceneId: 'scene',
  id: 'missing',
  deletionEpoch: 1,
  kind: 'commitment_delete',
})
const missingHead = JSON.stringify({
  schemaVersion: 1,
  sceneId: 'scene',
  id: 'missing',
  deletionEpoch: 0,
  kind: 'commitment_head',
  termsVersion: 1,
  mutationVersion: 1,
  qualificationHash: 'a'.repeat(64),
})
await scenario('zero-schema-never-initializes', {
  empty: true,
  ops: [
    { kind: 'assertSupported' },
    { kind: 'recall', message: 'root' },
    { kind: 'purgeAssertion', id: 'root' },
    { kind: 'erase', raw: missingDelete },
    { kind: 'marker', raw: missingHead },
    { kind: 'verify', events: [missingDelete] },
    { kind: 'verify', events: [missingHead] },
  ],
})
await scenario('legacy-no-timeline-purge-marker-reader3', {
  beforeBackup:
    'DROP TABLE memory_commitment_transitions;DROP TABLE memory_commitment_temporal_windows',
  live: [{ kind: 'source', message: 'root' }],
  ops: [{ kind: 'recall', message: 'root' }, { kind: 'marker' }, { kind: 'verify' }],
})
await scenario('three-table-legacy-reader3', {
  beforeBackup:
    'DROP TABLE memory_commitment_transitions;DROP TABLE memory_commitment_temporal_windows',
  ops: [{ kind: 'assertSupported' }, { kind: 'verify' }, { kind: 'recall', message: 'root' }],
})
for (const [name, sql] of [
  ['unknown-table', 'CREATE TABLE memory_commitment_future(id TEXT)'],
  ['unknown-view', 'CREATE VIEW memory_commitment_alias AS SELECT id FROM memory_commitment_heads'],
  ['unknown-column', 'ALTER TABLE memory_commitment_heads ADD COLUMN extra TEXT'],
  ['partial-temporal', 'DROP TABLE memory_commitment_temporal_windows'],
  ['corrupt-temporal', 'UPDATE memory_commitment_transitions SET value=CAST(value AS BLOB)'],
])
  await scenario(name, {
    beforeBackup: sql,
    ops: [{ kind: 'assertSupported' }, { kind: 'verify' }, { kind: 'recall', message: 'root' }],
  })
const headEvent = (
  snapshot,
  id = snapshot.head.id,
  qualificationHash = snapshot.qualificationHash,
) =>
  JSON.stringify({
    schemaVersion: snapshot.timeline ? 2 : 1,
    sceneId: snapshot.head.sceneId,
    id,
    deletionEpoch: snapshot.head.deletionEpoch,
    ...(snapshot.timeline ? { temporalVersion: 1 } : {}),
    kind: 'commitment_head',
    termsVersion: snapshot.head.latestVersion,
    mutationVersion: snapshot.head.mutationVersion,
    qualificationHash,
  })
await scenario('verify-map-order-and-last-head-value', {
  ops: (snapshot) => {
    const good = headEvent(snapshot),
      bad = headEvent(snapshot, snapshot.head.id, 'a'.repeat(64)),
      missing = headEvent(snapshot, 'missing')
    return [
      { kind: 'verify', events: [missing, bad] },
      { kind: 'verify', events: [bad, missing] },
      { kind: 'verify', events: [bad, good] },
      { kind: 'verify', events: [good, bad] },
    ]
  },
})
await scenario('verify-last-delete-and-head-suppression', {
  live: [{ kind: 'delete' }],
  ops: (snapshot, events) => {
    const original = events.find((raw) => JSON.parse(raw).kind === 'commitment_delete'),
      higher = JSON.stringify({ ...JSON.parse(original), deletionEpoch: 99 }),
      head = headEvent(snapshot)
    return [
      { kind: 'erase' },
      { kind: 'verify', events: [higher, original] },
      { kind: 'verify', events: [original, higher] },
      { kind: 'verify', events: [original, head] },
      { kind: 'verify', events: [head, original] },
    ]
  },
})
await scenario('marker-conflict-after-success', {
  live: [{ kind: 'source', message: 'root' }],
  ops: (_, events) => {
    const raw = events.find((raw) => JSON.parse(raw).kind === 'commitment_head'),
      event = JSON.parse(raw),
      changed = JSON.stringify({
        ...event,
        privacyMarker: { ...event.privacyMarker, recordedAt: '2000-01-01T00:00:00.000Z' },
      })
    return [
      { kind: 'recall', message: 'root' },
      { kind: 'marker' },
      { kind: 'marker', raw: changed },
      { kind: 'verify' },
    ]
  },
})
await scenario('invalid-offline-event-before-local-delete', {
  beforeBackup:
    "UPDATE memory_commitment_heads SET value=json_set(value,'$.mutationVersion',9007199254740991)",
  ops: [
    {
      kind: 'purgeAssertion',
      id: 'root',
      sql: "CREATE TRIGGER synthetic_delete_phase BEFORE DELETE ON memory_commitment_revisions BEGIN SELECT RAISE(ABORT,'synthetic_wrong_phase'); END",
    },
  ],
})
const files = [
  'src/memory/recovery/commitments.ts',
  'src/memory/recovery/commitment-contract.ts',
  'src/memory/recovery/ledger.ts',
  'src/memory/commitments/service.ts',
  'src/memory/commitments/storage.ts',
  'src/memory/commitments/temporal-store.ts',
  'src/memory/commitments/temporal-contracts.ts',
  'src/memory/domain/commitments.ts',
  'package-lock.json',
]
const output = {
  sourceNode: process.version,
  sourceLocale: new Intl.Collator().resolvedOptions().locale,
  sourceTimezone: new Intl.DateTimeFormat().resolvedOptions().timeZone,
  sources: Object.fromEntries(files.map((file) => [file, hash(readFileSync(file, 'utf8'))])),
  scenarios,
}
const path = 'backend/internal/commitments/testdata/offline-source-oracle.json',
  contents = JSON.stringify(output, null, 2) + '\n'
if (process.argv.includes('--check')) assert.equal(readFileSync(path, 'utf8'), contents)
else {
  mkdirSync('backend/internal/commitments/testdata', { recursive: true })
  writeFileSync(path, contents)
}
console.log(`commitment offline oracle: ${scenarios.length} actual-backup scenarios`)
