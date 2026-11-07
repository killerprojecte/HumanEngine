/** Actual TypeScript metadata owners with explicit synthetic authority ports.
 * Native durable pin-intent/ACK composition is separately integration-tested. */
import assert from 'node:assert/strict'
import crypto from 'node:crypto'
import { syncBuiltinESMExports } from 'node:module'
import Database from 'better-sqlite3'
import { mkdtempSync, mkdirSync, readFileSync, writeFileSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { WorkingSetService } from '../../src/memory/working-set/service.ts'
import { WorkingSetStorage } from '../../src/memory/working-set/storage.ts'
import { WorkingSetMutationHooks } from '../../src/memory/working-set/mutation-hooks.ts'
import * as contracts from '../../src/memory/working-set/contracts.ts'
import { referenceKey } from '../../src/memory/working-set/policy.ts'
assert.equal(process.version, 'v24.22.1')
const clone = structuredClone
const now = 10_000
const ctx = { sceneId: 'scene', simulation: false }
const ref = (id = 'a', version = 1) => ({ kind: 'assertion', id, version, deletionEpoch: 0 })
const selection = (id = 'a', version = 1, dependencyKey = 'a'.repeat(64)) => ({
  ref: ref(id, version),
  dependencyKey,
})
const snapshot = (id = 'a', extra = {}) => ({
  ...selection(id),
  sceneId: 'scene',
  recordedAt: 9000,
  protection: null,
  ...extra,
})
const policy = {
  coldAfterMs: 1000,
  reviewAfterMs: 500,
  leaseMs: 100,
  maxBatch: 3,
  maxReceipts: 4,
  receiptRetentionMs: 1000,
  selectionMaxAgeMs: 200,
}
const scenarios = []
const add = (name, steps, extra = {}) =>
  scenarios.push({ name, policy, now, enabled: true, snapshots: [snapshot()], steps, ...extra })
const enqueue = (selections = [selection()]) => ({ op: 'enqueue', selections })
const claim = (limit = 3) => ({ op: 'claim', limit })
const complete = (indexes = [0]) => ({ op: 'complete', indexes })
const selected = (extra = {}) => ({
  op: 'recordSelected',
  input: { turnId: 'turn', selectedAt: now, selections: [selection()], ...extra },
})
const pin = (expectedRevision = 0, pinned = true, selected = selection()) => ({
  op: 'setPinned',
  selection: selected,
  input: { expectedRevision, pinned },
})
add(
  'constructor remains lazy and disabled paths avoid validation',
  [
    { op: 'view', selections: [selection()] },
    enqueue(),
    claim(),
    complete([]),
    selected(),
    { op: 'flag', value: true },
    { op: 'view', selections: [selection()] },
  ],
  { enabled: false },
)
add('enqueue duplicate and exact completed item are clean', [
  enqueue(),
  enqueue(),
  claim(),
  complete(),
  enqueue(),
  { op: 'view', selections: [selection()] },
])
add('expired lease can be stolen but old token cannot complete', [
  enqueue(),
  claim(),
  { op: 'clock', value: now + 100 },
  claim(),
  complete([0]),
  complete([1]),
])
add('wrong scene token generation and dependency remain stale', [
  enqueue(),
  claim(),
  { ...complete(), override: { sceneId: 'other' } },
  { ...complete(), override: { token: '00000000-0000-4000-8000-000000000099' } },
  { ...complete(), override: { generation: '00000000-0000-4000-8000-000000000098' } },
  { ...complete(), override: { dependencyKey: 'b'.repeat(64) } },
  complete(),
])
add('unavailable completion drops exact owned job without projecting', [
  enqueue(),
  claim(),
  { op: 'snapshots', value: [] },
  complete(),
  { op: 'view', selections: [selection()] },
])
add('changed watermark replaces pending generation', [
  enqueue(),
  claim(),
  { op: 'snapshots', value: [snapshot('a', { dependencyKey: 'b'.repeat(64) })] },
  enqueue([selection('a', 1, 'b'.repeat(64))]),
  complete(),
  claim(),
  complete([1]),
])
add('selection receipt deduplicates by turn and reference not dependency', [
  selected(),
  selected(),
  { op: 'snapshots', value: [snapshot('a', { dependencyKey: 'b'.repeat(64) })] },
  selected({ selections: [selection('a', 1, 'b'.repeat(64))] }),
])
add('selection age future and direct UTF16 turn width boundaries', [
  selected({ selectedAt: now - 200 }),
  selected({ turnId: 'old', selectedAt: now - 201 }),
  selected({ turnId: 'future', selectedAt: now + 1 }),
  selected({ turnId: '😀'.repeat(256) }),
  selected({ turnId: '😀'.repeat(257) }),
])
add(
  'cold and review classification does not mutate metadata',
  [
    enqueue(),
    { op: 'drain' },
    { op: 'view', selections: [selection()] },
    { op: 'view', selections: [selection()], options: { includeCold: false } },
    selected(),
    { op: 'view', selections: [selection()] },
  ],
  { snapshots: [snapshot('a', { epistemicKind: 'inferred' })] },
)
add('pin exact stale off unpin and terminal policy', [
  pin(),
  pin(1),
  { op: 'snapshots', value: [snapshot('a', { dependencyKey: 'b'.repeat(64) })] },
  { op: 'view', selections: [selection('a', 1, 'b'.repeat(64))] },
  { op: 'flag', value: false },
  pin(1, false),
  { op: 'terminalInvalidate', refs: [ref()] },
  pin(3),
  { op: 'listPinPolicies' },
])
add('pin revision conflict stale source and missing unpin', [
  pin(0, false),
  pin(1),
  { op: 'snapshots', value: [] },
  pin(),
  { op: 'flag', value: false },
  pin(),
])
add('ordinary invalidation while off preserves explicit pin', [
  pin(),
  selected(),
  enqueue(),
  { op: 'flag', value: false },
  { op: 'invalidate', refs: [ref()] },
  { op: 'listPinPolicies' },
  { op: 'cancelMaintenance' },
])
// The synthetic observer is not a recovery ledger: stop at this prefix. Native
// integration separately requires an ACK gap, never a forged ready continuation.
add('outer rollback removes metadata but not observed pin precommit', [
  { ...pin(), rollback: true },
])
add('ordinary derived rollback retains an already committed pin', [
  pin(),
  { ...enqueue(), rollback: true },
  { ...selected(), rollback: true },
  { op: 'listPinPolicies' },
])
add('named write and pin hook failures preserve source ordering', [
  { op: 'fault', hook: 'write', value: 'synthetic_write' },
  pin(),
  { op: 'fault', hook: 'write', value: null },
  { op: 'fault', hook: 'pin', value: 'synthetic_pin' },
  pin(),
  { op: 'fault', hook: 'pin', value: null },
  pin(),
])
add(
  'simulation denies pin but suppresses optional metadata work',
  [
    enqueue(),
    selected(),
    claim(),
    complete([]),
    { op: 'view', selections: [selection()] },
    { op: 'listPinPolicies' },
    pin(),
  ],
  { context: { ...ctx, simulation: true } },
)
add('restart preserves metadata and does not drain at construction', [
  enqueue(),
  { op: 'reopen' },
  { op: 'view', selections: [selection()] },
  { op: 'drain' },
  { op: 'reopen' },
  enqueue(),
])
add(
  'input prefix limit and duplicate selection conflict',
  [
    enqueue([selection('a'), selection('b'), selection('c'), selection('d')]),
    { op: 'view', selections: [selection('a'), selection('b'), selection('c'), selection('d')] },
    enqueue([selection(), selection('a', 1, 'b'.repeat(64))]),
  ],
  { snapshots: ['a', 'b', 'c', 'd'].map((id) => snapshot(id)) },
)
add('authority duplicate exact selection rejects transaction', [
  { op: 'resolveMode', value: 'duplicate' },
  enqueue(),
  { op: 'view', selections: [selection()] },
  { op: 'resolveMode', value: null },
  enqueue(),
])
add('invalid scalar options and policy configuration', [
  claim(0),
  { op: 'clock', value: -1 },
  claim(),
  { op: 'clock', value: now },
  { op: 'configurePolicy', value: { leaseMs: 1 } },
  { op: 'configurePolicy', value: { coldAfterMs: 0 } },
  { op: 'configurePolicy', value: { coldAfterMs: 2000 } },
  { op: 'listPinPolicies', options: { limit: 0 } },
])
add('mutation hooks require transaction and preserve disabled invalidation', [
  { op: 'committed', refs: [ref()], outside: true },
  { op: 'committed', refs: [ref()] },
  claim(),
  { op: 'flag', value: false },
  { op: 'sourceEdited', refs: [ref()] },
  complete(),
  { op: 'flag', value: true },
  { op: 'committed', refs: [ref()] },
])
add('mutation refs conflict and unavailable coverage', [
  { op: 'committed', refs: [ref(), ref('a', 2)] },
  { op: 'committed', refs: [ref(), ref('missing')] },
  { op: 'committed', refs: [ref()] },
  { op: 'invalidateAssertion', id: 'a' },
])

add('receipt capacity pruning uses strict retention boundary', [
  ...['one', 'two', 'three', 'four', 'five'].map((turnId) => selected({ turnId })),
  { op: 'clock', value: 11000 },
  selected({ turnId: 'at-boundary', selectedAt: 11000 }),
  { op: 'clock', value: 11001 },
  selected({ turnId: 'after-boundary', selectedAt: 11001 }),
])
add('selection count saturates without changing truth', [
  selected(),
  {
    op: 'sql',
    value:
      "UPDATE memory_working_set_items SET value=json_set(value,'$.contextSelectedCount',9007199254740991)",
  },
  selected({ turnId: 'second' }),
  { op: 'view', selections: [selection()] },
])
add('completion uses captured clock and does not invent postcallback expiry', [
  enqueue(),
  claim(),
  {
    op: 'resolveSQL',
    value: "UPDATE memory_working_set_jobs SET value=json_set(value,'$.lease.expiresAt',0)",
  },
  complete(),
])
add('changed callback generation prevents old completion', [
  enqueue(),
  claim(),
  {
    op: 'resolveSQL',
    value:
      "UPDATE memory_working_set_jobs SET value=json_set(value,'$.generation','00000000-0000-4000-8000-000000000099')",
  },
  complete(),
])
add('terminal assertion cleanup covers every metadata scene beyond one page', [
  { op: 'seedScenes', count: 42 },
  { op: 'invalidateAssertion', id: 'a' },
  { op: 'terminalAssertionDeleted', id: 'a' },
  { op: 'terminalAssertionDeleted', id: 'a' },
])

const tables = Object.keys(contracts.WORKING_SET_TABLES)
function run(input) {
  const directory = mkdtempSync(join(tmpdir(), 'humanengine-working-set-'))
  const databasePath = join(directory, 'main.sqlite')
  let db = new Database(databasePath),
    time = input.now,
    enabled = input.enabled
  let snapshots = clone(input.snapshots),
    calls = [],
    token = 0,
    mode = null,
    resolveSQL = null
  const faults = {},
    pins = [],
    leases = []
  const oldUUID = crypto.randomUUID
  crypto.randomUUID = () => {
    const value = `00000000-0000-4000-8000-${String(++token).padStart(12, '0')}`
    calls.push({ op: 'token', value })
    return value
  }
  syncBuiltinESMExports()
  const ready = () => {
    calls.push({ op: 'ready' })
    if (faults.ready) throw new Error(faults.ready)
  }
  const flag = () => {
    calls.push({ op: 'enabled', value: enabled })
    return enabled
  }
  const clock = () => {
    calls.push({ op: 'now', value: time })
    return time
  }
  const resolve = (context, refs) => {
    calls.push({ op: 'resolve', ctx: clone(context), refs: clone(refs) })
    if (faults.resolve) throw new Error(faults.resolve)
    if (resolveSQL) {
      const sql = resolveSQL
      resolveSQL = null
      db.exec(sql)
    }
    const wanted = new Set(refs.map(referenceKey))
    const found = snapshots
      .filter((value) => wanted.has(referenceKey(value.ref)))
      .map((value) => clone(value))
    return mode === 'duplicate' && found.length ? [found[0], found[0]] : found
  }
  const beforeWrite = () => {
    calls.push({ op: 'beforeWrite' })
    if (faults.write) throw new Error(faults.write)
  }
  let service, mutations
  const construct = () => {
    service = new WorkingSetService(db, {
      assertReady: ready,
      enabled: flag,
      now: clock,
      policy: input.policy,
      resolveCurrent: resolve,
      beforeWrite,
      beforePinWrite(value) {
        calls.push({ op: 'beforePinWrite', policy: clone(value) })
        if (faults.pin) throw new Error(faults.pin)
        pins.push(clone(value))
      },
    })
    mutations = new WorkingSetMutationHooks(db, {
      assertReady: ready,
      enabled: flag,
      now: clock,
      beforeWrite,
      storage: service.storage,
      resolveCommitted: resolve,
      invalidate: (scene, refs) => service.invalidate(scene, refs),
      terminalInvalidate: (scene, refs) => service.terminalInvalidate(scene, refs),
      affectedAssertionScenes(id, after, limit) {
        calls.push({ op: 'affectedScenes', id, after: after ?? null, limit })
        if (!service.storage.assertSupported()) return []
        return db
          .prepare(
            `SELECT scene_id FROM (
          SELECT scene_id FROM memory_working_set_pins WHERE kind='assertion' AND entity_id=?
          UNION SELECT scene_id FROM memory_working_set_items WHERE kind='assertion' AND entity_id=?
          UNION SELECT scene_id FROM memory_working_set_jobs WHERE kind='assertion' AND entity_id=?
          UNION SELECT json_extract(value,'$.sceneId') scene_id FROM memory_working_set_receipts WHERE json_extract(value,'$.selection.ref.kind')='assertion' AND json_extract(value,'$.selection.ref.id')=?
        ) WHERE (? IS NULL OR scene_id>?) ORDER BY scene_id COLLATE BINARY LIMIT ?`,
          )
          .all(id, id, id, id, after ?? null, after ?? null, limit)
          .map((row) => row.scene_id)
      },
    })
  }
  const inspect = () =>
    Object.fromEntries(
      tables.map((table) => [
        table,
        db.prepare("SELECT 1 FROM sqlite_master WHERE name=? AND type='table'").get(table)
          ? db.prepare(`SELECT * FROM ${table} ORDER BY rowid`).all()
          : null,
      ]),
    )
  const trace = []
  try {
    construct()
    for (const step of input.steps) {
      calls = []
      const context = step.ctx ?? input.context ?? ctx
      const act = () => {
        switch (step.op) {
          case 'view':
            return service.view(context, step.selections, step.options)
          case 'recordSelected':
            return service.recordSelected(context, step.input)
          case 'listPinPolicies':
            return service.listPinPolicies(context, step.options)
          case 'setPinned':
            return service.setPinned(context, step.selection, step.input)
          case 'enqueue':
            return service.enqueue(context, step.selections)
          case 'claim': {
            const result = service.claim(context, step.limit)
            leases.push(...clone(result))
            return result
          }
          case 'complete':
            return service.complete(
              context,
              step.leases ??
                step.indexes.map((i) => ({ ...clone(leases[i]), ...clone(step.override ?? {}) })),
            )
          case 'drain':
            return service.drain(context, step.limit)
          case 'cancelMaintenance':
            return service.cancelMaintenance(step.sceneId)
          case 'invalidate':
            return service.invalidate(step.sceneId ?? context.sceneId, step.refs)
          case 'terminalInvalidate':
            return service.terminalInvalidate(step.sceneId ?? context.sceneId, step.refs)
          case 'configurePolicy':
            return service.configurePolicy(step.value)
          case 'committed':
            return mutations.committed(context, step.refs)
          case 'sourceEdited':
            return mutations.sourceEdited(context, step.refs)
          case 'invalidateAssertion':
            return mutations.invalidateAssertion(step.id)
          case 'terminalAssertionDeleted':
            return mutations.terminalAssertionDeleted(step.id)
          case 'clock':
            time = step.value
            return
          case 'flag':
            enabled = step.value
            return
          case 'snapshots':
            snapshots = clone(step.value)
            return
          case 'fault':
            faults[step.hook] = step.value
            return
          case 'resolveMode':
            mode = step.value
            return
          case 'resolveSQL':
            resolveSQL = step.value
            return
          case 'seedScenes':
            return void db.transaction(() => {
              service.storage.ensure()
              for (let i = 0; i < step.count; i++) {
                const sceneId = `scene-${String(i).padStart(3, '0')}`
                service.storage.putPin({
                  schemaVersion: 1,
                  sceneId,
                  selection: selection(),
                  policyRevision: 1,
                  pinned: true,
                  terminal: false,
                })
                service.storage.putItem({
                  schemaVersion: 1,
                  snapshot: snapshot('a', { sceneId }),
                  metadataRevision: 1,
                  contextSelectedCount: 0,
                  lastSelectedAt: null,
                  projectedAt: time,
                })
              }
            })()
          case 'sql':
            return void db.exec(step.value)
          case 'reopen':
            db.close()
            db = new Database(databasePath)
            construct()
            return
          default:
            throw new Error(`unknown_fixture_op:${step.op}`)
        }
      }
      let result, error
      try {
        const mutation = [
          'committed',
          'sourceEdited',
          'invalidateAssertion',
          'terminalAssertionDeleted',
        ].includes(step.op)
        if (step.outer || step.rollback || (mutation && !step.outside)) {
          const rolled = new Error('fixture_outer_rollback')
          try {
            db.transaction(() => {
              result = act()
              if (step.rollback) throw rolled
            })()
          } catch (e) {
            if (e !== rolled) throw e
          }
        } else result = act()
      } catch (e) {
        error = e.message
      }
      trace.push({
        ...(error === undefined
          ? result === undefined
            ? { undefined: true }
            : { result: clone(result) }
          : { error }),
        calls: clone(calls),
        db: inspect(),
        pins: clone(pins),
      })
    }
    return { ...input, trace }
  } finally {
    crypto.randomUUID = oldUUID
    syncBuiltinESMExports()
    db.close()
    rmSync(directory, { recursive: true, force: true })
  }
}
const schemaFixtures = []
for (const [schema, valid] of [
  ['WorkingSetReferenceSchema', ref()],
  ['WorkingSetSelectionSchema', selection()],
  ['WorkingSetSnapshotSchema', snapshot()],
  [
    'WorkingSetPinPolicySchema',
    {
      schemaVersion: 1,
      sceneId: 'scene',
      selection: selection(),
      policyRevision: 1,
      pinned: true,
      terminal: false,
    },
  ],
  [
    'WorkingSetItemSchema',
    {
      schemaVersion: 1,
      snapshot: snapshot(),
      metadataRevision: 1,
      contextSelectedCount: 0,
      lastSelectedAt: null,
      projectedAt: now,
    },
  ],
  [
    'WorkingSetJobSchema',
    {
      schemaVersion: 1,
      snapshot: snapshot(),
      generation: '00000000-0000-4000-8000-000000000001',
      enqueuedAt: now,
      lease: null,
    },
  ],
  [
    'WorkingSetReceiptSchema',
    { schemaVersion: 1, sceneId: 'scene', turnId: 'turn', selection: selection(), selectedAt: now },
  ],
]) {
  for (const value of [
    valid,
    Object.fromEntries(Object.entries(valid).reverse()),
    { ...valid, extra: true },
    null,
    [],
    ...Object.keys(valid).map((key) => {
      const v = clone(valid)
      delete v[key]
      return v
    }),
  ]) {
    const parsed = contracts[schema].safeParse(value)
    schemaFixtures.push({
      schema,
      input: value,
      accepted: parsed.success,
      ...(parsed.success ? { canonical: JSON.stringify(parsed.data) } : {}),
    })
  }
}
for (const id of ['', 'x'.repeat(512), 'x'.repeat(513), '😀'.repeat(512), '😀'.repeat(513)]) {
  const input = ref(id),
    parsed = contracts.WorkingSetReferenceSchema.safeParse(input)
  schemaFixtures.push({
    schema: 'WorkingSetReferenceSchema',
    input,
    accepted: parsed.success,
    ...(parsed.success ? { canonical: JSON.stringify(parsed.data) } : {}),
  })
}
for (const input of [
  { ...ref(), kind: 'commitment', mutationVersion: 1 },
  { ...ref(), kind: 'commitment' },
  { ...ref(), mutationVersion: 1 },
  { ...ref(), version: 0 },
  { ...ref(), version: 9007199254740991 },
  { ...ref(), version: 9007199254740992 },
  { ...ref(), deletionEpoch: -1 },
  { ...ref(), version: 1.5 },
]) {
  const parsed = contracts.WorkingSetReferenceSchema.safeParse(input)
  schemaFixtures.push({
    schema: 'WorkingSetReferenceSchema',
    input,
    accepted: parsed.success,
    ...(parsed.success ? { canonical: JSON.stringify(parsed.data) } : {}),
  })
}
for (const generation of [
  '00000000-0000-0000-0000-000000000000',
  'ffffffff-ffff-ffff-ffff-ffffffffffff',
  '00000000-0000-8000-8000-000000000001',
  '00000000-0000-9000-8000-000000000001',
  'bad',
]) {
  const input = {
      schemaVersion: 1,
      snapshot: snapshot(),
      generation,
      enqueuedAt: now,
      lease: null,
    },
    parsed = contracts.WorkingSetJobSchema.safeParse(input)
  schemaFixtures.push({
    schema: 'WorkingSetJobSchema',
    input,
    accepted: parsed.success,
    ...(parsed.success ? { canonical: JSON.stringify(parsed.data) } : {}),
  })
}

const ddl = Object.entries(contracts.WORKING_SET_TABLES).map(
  ([name, columns]) => `CREATE TABLE ${name}(${columns.map((c) => `${c} TEXT`).join(',')});`,
)
const storageSchemas = [
  { name: 'absent', sql: '' },
  { name: 'all four source column layouts', sql: ddl.join('') },
  { name: 'partial three tables', sql: ddl.slice(0, 3).join('') },
  {
    name: 'extra matching table',
    sql: ddl.join('') + 'CREATE TABLE memory_working_set_extra(value);',
  },
  {
    name: 'view instead of table',
    sql:
      ddl.slice(1).join('') +
      'CREATE VIEW memory_working_set_pins AS SELECT 1 AS scene_id,2 AS kind,3 AS entity_id,4 AS value;',
  },
  {
    name: 'reordered columns',
    sql:
      ddl.slice(1).join('') +
      'CREATE TABLE memory_working_set_pins(kind,scene_id,entity_id,value);',
  },
  {
    name: 'extra column',
    sql:
      ddl.slice(1).join('') +
      'CREATE TABLE memory_working_set_pins(scene_id,kind,entity_id,value,extra);',
  },
  {
    name: 'LIKE underscores remain source wildcards',
    sql: 'CREATE TABLE memoryXworkingYsetZsurprise(value);',
  },
].map((item) => {
  const db = new Database(':memory:')
  try {
    db.exec(item.sql)
    return { ...item, supported: new WorkingSetStorage(db).assertSupported() }
  } catch (e) {
    return { ...item, error: e.message }
  } finally {
    db.close()
  }
})
const output =
  JSON.stringify(
    {
      source: 'actual TypeScript working-set owners with explicit synthetic authority ports',
      schemas: schemaFixtures,
      storageSchemas,
      scenarios: scenarios.map(run),
    },
    null,
    2,
  ) + '\n'
const destination = new URL('../internal/workingset/testdata/source-oracle.json', import.meta.url)
if (process.argv.includes('--check')) assert.equal(readFileSync(destination, 'utf8'), output)
else {
  mkdirSync(new URL('.', destination), { recursive: true })
  writeFileSync(destination, output)
}
console.log(`${scenarios.length} actual working-set traces; ${schemaFixtures.length} schemas`)
