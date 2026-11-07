/** Actual CommitmentService/Storage/TemporalStore and dual-SQLite recovery.
 * TS admission creates seed material only; native interactive admission is NOT
 * claimed. Explicit recording assertion/workspace ports isolate this domain.
 */
import assert from 'node:assert/strict'
import { isDeepStrictEqual } from 'node:util'
import Database from 'better-sqlite3'
import { createHash } from 'node:crypto'
import { mkdtempSync, mkdirSync, rmSync, readFileSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { CommitmentService } from '../../src/memory/commitments/service.ts'
import {
  CommitmentStorage,
  CommitmentHeadSchema,
  CommitmentRevisionSchema,
  CommitmentEvidenceSchema,
} from '../../src/memory/commitments/storage.ts'
import { CommitmentTemporalStore } from '../../src/memory/commitments/temporal-store.ts'
import {
  CommitmentTransitionSchema,
  CommitmentWindowSchema,
  RecordedCommitmentStateSchema,
  TemporalMarkerSchema,
  temporalHash,
} from '../../src/memory/commitments/temporal-contracts.ts'
import {
  CommitmentTermsSchema,
  CommitmentRefSchema,
  commitmentQualificationHash,
} from '../../src/memory/domain/commitments.ts'
import { readCommitmentTimeline } from '../../src/memory/commitments/temporal-read.ts'
import {
  RecoveryAuthority,
  bindRecovery,
  saveGate,
  metadata,
} from '../../src/memory/recovery/ledger.ts'
import { recordCommitmentMutation } from '../../src/memory/recovery/commitments.ts'
assert.equal(process.version, 'v24.22.1')
const hash = (v) =>
  createHash('sha256')
    .update(typeof v === 'string' ? v : JSON.stringify(v))
    .digest('hex')
const source = (id) => ({ sceneId: 'scene', messageId: id, senderId: 'a' })
const proof = (id, message = id, extra = {}) => ({
  proofId: id,
  sources: [source(message)],
  parents: [],
  parentReferences: [],
  subjectWitness: { kind: 'self', personId: 'a', sourceMessageId: message },
  ...extra,
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
const terms = (title = 'Synthetic plan') => ({
  kind: 'plan',
  title,
  participantIds: ['a'],
  temporalScope: 'future',
  polarity: 'positive',
  modality: 'asserted',
  conditions: [],
  timeQualification: 'tomorrow',
})
const tableNames = [
  'memory_commitment_commands',
  'memory_commitment_heads',
  'memory_commitment_revisions',
  'memory_commitment_temporal_windows',
  'memory_commitment_transitions',
]
function tables(db) {
  const names = new Set(
    db
      .prepare("SELECT name FROM sqlite_master WHERE type='table'")
      .all()
      .map((r) => r.name),
  )
  return Object.fromEntries(
    tableNames
      .filter((n) => names.has(n))
      .map((n) => [n, db.prepare(`SELECT * FROM ${n} ORDER BY 1,2`).all()]),
  )
}
const traces = [],
  schemas = []
let sample
function trace(name, setup, operations) {
  const root = mkdtempSync(join(process.cwd(), 'backend/.commitment-privacy-oracle-'))
  let db, ledger, external
  try {
    mkdirSync(join(root, 'live'))
    db = new Database(join(root, 'live', 'main.sqlite'))
    db.pragma('journal_mode=WAL')
    ledger = new RecoveryAuthority(join(root, 'authority'), '12345678-1234-4123-8123-123456789012')
    saveGate(db, { head: ledger.head(), authority: ledger.path, minimumReader: 1, state: 'ready' })
    bindRecovery(db, ledger)
    let live = false,
      fault = '',
      tick = '2026-10-28T12:00:00.000Z',
      enabled = true,
      ids = 0,
      events = [],
      candidates = {},
      allowedCalls = 0
    const views = new Map()
    const authority = {
      enabled: () => enabled,
      learningEnabled: () => enabled,
      current: (_, ref) => views.get(ref.id),
      historical: (_, ref) => views.get(ref.id),
      people: () => ['a'],
      surviving: (_, e) => {
        events.push(['surviving', e.backing.id, e.qualificationRef ?? e.backing])
        return candidates[e.backing.id]
      },
      beforeWrite: (change) => {
        if (live) recordCommitmentMutation(db, change)
      },
      afterWrite: (head, next, prior) => {
        if (live) {
          events.push(['afterWrite', head, next, prior])
          if (fault === 'afterWrite') throw Error('synthetic_afterWrite')
        }
      },
    }
    const service = new CommitmentService(db, authority, {
      now: () => tick,
      id: () => `commitment-${++ids}`,
    })
    const ctx = (ref) => ({
      sceneId: 'scene',
      simulation: false,
      provenance: ref ? 'contextual' : 'source-only',
      exposedRefs: ref
        ? [{ ...ref, mutationVersion: service.storage.head(ref.id).mutationVersion }]
        : [],
    })
    const add = (id, proofs = [proof(id)]) => {
      views.set(id, { record: record(id), proofs })
      return { id, version: 1, deletionEpoch: 0 }
    }
    const admit = (backing, command, ref) =>
      db.transaction(() => service.admit(ctx(ref), backing, command))().item.ref
    const env = {
      db,
      service,
      views,
      add,
      admit,
      ctx,
      setTime: (value) => {
        tick = value
      },
    }
    const plan = setup(env)
    if (!sample) sample = structuredClone(service.snapshot(plan.id))
    const initial = tables(db)
    external = new Database(join(ledger.path, 'authority.sqlite'), { readonly: true })
    live = true
    const steps = []
    for (const raw of operations(plan, env)) {
      const op = structuredClone(raw)
      events = []
      allowedCalls = 0
      fault = op.fault ?? ''
      enabled = !(op.flagsOff ?? false)
      candidates = op.candidates ?? {}
      tick = op.now ?? '2026-10-28T13:00:00.000Z'
      if (op.sql) db.exec(op.sql)
      let error = null,
        result = null
      try {
        db.transaction(() => {
          const c = {
            sceneId: op.scene ?? 'scene',
            simulation: op.simulation ?? false,
            provenance: 'source-only',
          }
          if (op.kind === 'purgeSource') service.purgeSource(c.sceneId, op.message)
          else if (op.kind === 'purgeAssertion') service.purgeAssertion(op.id)
          else if (op.kind === 'prune')
            service.pruneUnavailableProof(c, (e, p) => {
              events.push(['allowed', e.backing.id, p.proofId])
              allowedCalls++
              if (op.failAt === allowedCalls) throw Error('synthetic_allowed')
              return (
                !p.sources.some((s) => (op.denyMessages ?? []).includes(s.messageId)) &&
                !p.parents.some((p) => (op.denyParents ?? []).includes(p))
              )
            })
          else if (op.kind === 'requalify') {
            const saved = authority.surviving
            if (op.noSurviving) delete authority.surviving
            try {
              service.requalify(c, op.transitions)
            } finally {
              authority.surviving = saved
            }
          } else if (op.kind === 'erase') service.erase(c, op.ref)
          else throw Error('unknown_oracle_action')
        })()
      } catch (e) {
        error = e.message
      }
      const rows = tables(db)
      const entries = external
        .prepare('SELECT sequence,previous_hash,payload,hash FROM revocations ORDER BY sequence')
        .all()
      const gate = metadata(db)
      const snapshots = []
      // After a failed main transaction, readiness correctly prevents publication.
      if (!error)
        for (const row of rows.memory_commitment_heads) snapshots.push(service.snapshot(row.id))
      steps.push({
        op: JSON.stringify(op),
        error,
        events: JSON.stringify(events),
        rows: JSON.stringify(rows),
        rowsHash: hash(rows),
        snapshots: JSON.stringify(snapshots),
        entries: JSON.stringify(entries),
        gateSequence: gate.head.sequence,
        gateHash: gate.head.hash,
        minimumReader: gate.minimumReader,
      })
    }
    traces.push({ name, initial: JSON.stringify(initial), steps })
  } finally {
    external?.close()
    db?.close()
    ledger?.close()
    rmSync(root, { recursive: true, force: true })
  }
}
const seed = (f) =>
  f.admit(f.add('root', [proof('root'), proof('survivor', 'independent')]), {
    kind: 'propose',
    terms: terms(),
  })
trace('original-or-survival-requalify-and-delete', seed, (ref, f) => [
  { kind: 'purgeSource', message: 'root', flagsOff: true },
  {
    kind: 'requalify',
    transitions: [
      {
        from: { id: 'root', version: 1, deletionEpoch: 0 },
        to: { id: 'root', version: 2, deletionEpoch: 1 },
      },
    ],
    candidates: {
      root: {
        ref: { id: 'root', version: 2, deletionEpoch: 1 },
        view: {
          record: record('root'),
          proofs: [proof('survivor', 'independent'), proof('new', 'new-source')],
        },
      },
    },
  },
  { kind: 'purgeSource', message: 'root' },
  { kind: 'erase', ref },
])
trace(
  'amendment-inherited-and-groups',
  (f) => {
    const ref = seed(f)
    return f.admit(
      f.add('amended'),
      { kind: 'amend', target: ref, terms: terms('Immutable amended terms') },
      ref,
    )
  },
  () => [
    { kind: 'purgeAssertion', id: 'root' },
    { kind: 'prune', denyMessages: ['amended'], flagsOff: true },
  ],
)
trace(
  'terminal-declaration-history-nulling',
  (f) => {
    const ref = seed(f)
    f.admit(f.add('completed'), { kind: 'complete', target: ref }, ref)
    return ref
  },
  () => [
    { kind: 'purgeSource', message: 'completed' },
    { kind: 'purgeSource', message: 'independent' },
    { kind: 'purgeSource', message: 'root' },
  ],
)
trace(
  'historical-parent-prune-and-call-order',
  (f) =>
    f.admit(
      f.add('parented', [
        proof('parented', 'parented', {
          parents: ['ancestor'],
          parentReferences: [{ id: 'ancestor', version: 1, deletionEpoch: 0 }],
        }),
        proof('survivor', 'independent'),
      ]),
      { kind: 'propose', terms: terms() },
    ),
  () => [
    { kind: 'prune', denyParents: ['ancestor'], flagsOff: true },
    { kind: 'prune', simulation: true, denyMessages: ['independent'] },
  ],
)
trace('no-latest-or-new-proof-resurrection', seed, () => [
  {
    kind: 'requalify',
    transitions: [
      {
        from: { id: 'root', version: 9, deletionEpoch: 0 },
        to: { id: 'root', version: 10, deletionEpoch: 0 },
      },
    ],
    candidates: {
      root: {
        ref: { id: 'root', version: 10, deletionEpoch: 0 },
        view: { record: record('root'), proofs: [proof('root')] },
      },
    },
  },
  {
    kind: 'requalify',
    transitions: [
      {
        from: { id: 'root', version: 1, deletionEpoch: 0 },
        to: { id: 'root', version: 2, deletionEpoch: 0 },
      },
    ],
    candidates: {
      root: {
        ref: { id: 'root', version: 2, deletionEpoch: 0 },
        view: { record: record('root'), proofs: [proof('new', 'independent')] },
      },
    },
  },
  {
    kind: 'requalify',
    transitions: [
      {
        from: { id: 'root', version: 1, deletionEpoch: 0 },
        to: { id: 'root', version: 2, deletionEpoch: 0 },
      },
    ],
    candidates: {
      root: {
        ref: { id: 'root', version: 2, deletionEpoch: 0 },
        view: {
          record: { ...record('root'), content: 'Changed terms backing' },
          proofs: [proof('root')],
        },
      },
    },
  },
])
trace(
  'legacy-no-history-first-privacy-marker',
  (f) => {
    const ref = seed(f)
    f.db.exec(
      'DROP TABLE memory_commitment_transitions;DROP TABLE memory_commitment_temporal_windows',
    )
    return ref
  },
  () => [{ kind: 'purgeSource', message: 'root' }],
)
trace(
  'clock-nonmonotonic-and-retention',
  (f) => {
    const ref = seed(f)
    let timeline = f.service.temporal.read(ref.id)
    for (let i = 2; i <= 66; i++) {
      const last = timeline.transitions.at(-1)
      timeline = f.service.temporal.append(timeline, {
        ...last,
        mutationVersion: i,
        head: { ...last.head, mutationVersion: i },
        states: null,
        reason: 'purge',
      })
    }
    f.db.transaction(() => {
      f.service.temporal.replace(ref.id, timeline)
      f.service.storage.put({ ...f.service.storage.head(ref.id), mutationVersion: 66 })
    })()
    return ref
  },
  () => [{ kind: 'purgeSource', message: 'root', now: '2026-10-27T13:00:00.000Z' }],
)
trace('owner-failure-durable-prefix', seed, () => [
  { kind: 'purgeSource', message: 'root', fault: 'afterWrite' },
])
trace('head-write-failure-durable-prefix', seed, () => [
  {
    kind: 'purgeSource',
    message: 'root',
    sql: "CREATE TRIGGER synthetic_head_failure BEFORE UPDATE ON memory_commitment_heads BEGIN SELECT RAISE(ABORT,'synthetic_head_failure'); END",
  },
])
trace('temporal-write-failure-durable-prefix', seed, () => [
  {
    kind: 'purgeSource',
    message: 'root',
    sql: "CREATE TRIGGER synthetic_temporal_failure BEFORE INSERT ON memory_commitment_transitions BEGIN SELECT RAISE(ABORT,'synthetic_temporal_failure'); END",
  },
])
trace('optional-surviving-absent-noop', seed, () => [
  {
    kind: 'requalify',
    noSurviving: true,
    transitions: [
      {
        from: { id: 'root', version: 1, deletionEpoch: 0 },
        to: { id: 'root', version: 2, deletionEpoch: 0 },
      },
    ],
  },
])
trace('analysis-failure-no-intent', seed, () => [
  { kind: 'prune', failAt: 2, denyMessages: ['root'] },
])

const schemaMap = {
  head: CommitmentHeadSchema,
  revision: CommitmentRevisionSchema,
  evidence: CommitmentEvidenceSchema,
  transition: CommitmentTransitionSchema,
  window: CommitmentWindowSchema,
  state: RecordedCommitmentStateSchema,
  marker: TemporalMarkerSchema,
  terms: CommitmentTermsSchema,
  ref: CommitmentRefSchema,
}
const addSchema = (kind, name, value) => {
  const raw = JSON.stringify(value),
    result = schemaMap[kind].safeParse(JSON.parse(raw))
  schemas.push({
    kind,
    name,
    input: raw,
    accepted: result.success,
    normalized: result.success ? JSON.stringify(result.data) : null,
  })
}
for (const [kind, value] of Object.entries({
  head: sample.head,
  revision: sample.revisions[0],
  evidence: sample.revisions[0].evidence[0],
  transition: sample.timeline.transitions[0],
  window: sample.timeline.window,
  state: sample.timeline.transitions[0].states[0],
  marker: { recordedAt: '2026-10-28T13:00:00.000Z', head: sample.head, reason: 'purge' },
  terms: sample.revisions[0].terms,
  ref: sample.revisions[0].ref,
})) {
  addSchema(kind, 'valid', value)
  addSchema(kind, 'reordered', Object.fromEntries(Object.entries(value).reverse()))
  addSchema(kind, 'unknown-key', { ...value, unknown: true })
  addSchema(kind, 'null', null)
}
for (const id of ['😀'.repeat(512), '😀'.repeat(513), '\ud800'.repeat(512), '\ud800'.repeat(513)])
  addSchema('ref', `id-length-${id.length}`, { id, version: 1, deletionEpoch: 0 })
for (const recordedAt of [
  '2026-02-29T00:00:00Z',
  '2024-02-29T00:00:00+23:59',
  '0000-02-29T00:00:00Z',
  '2026-10-28T12:00Z',
  '2026-10-28T12:00:00.123456789012Z',
  '2026-10-28T24:00:00Z',
])
  addSchema('revision', recordedAt, { ...sample.revisions[0], recordedAt })
addSchema('terms', 'offset-range', {
  ...sample.revisions[0].terms,
  validFrom: '2026-10-28T12:00:00+02:00',
  validUntil: '2026-10-28T11:00:00Z',
})
addSchema('terms', 'offset-reversed', {
  ...sample.revisions[0].terms,
  validFrom: '2026-10-28T12:00:00Z',
  validUntil: '2026-10-28T12:00:00+02:00',
})
addSchema('revision', 'empty-inherited-group', {
  ...sample.revisions[0],
  requiredProposalGroups: [[]],
})
addSchema('state', 'empty-inherited-group', {
  ...sample.timeline.transitions[0].states[0],
  requiredProposalGroups: [[]],
})
addSchema('evidence', 'invalid-parent-binding', {
  ...sample.revisions[0].evidence[0],
  proofs: [proof('x', 'x', { parents: ['p'], parentReferences: [] })],
})
addSchema('ref', 'unsafe-version', { id: 'id', version: 9007199254740992, deletionEpoch: 0 })
addSchema('ref', 'safe-version', { id: 'id', version: 9007199254740991, deletionEpoch: 0 })
for (const suffix of ['\n', '\r', '\u2028', '\u2029', '\r\n']) {
  addSchema('revision', `date-final-${JSON.stringify(suffix)}`, {
    ...sample.revisions[0],
    recordedAt: '2026-10-28T12:00:00Z' + suffix,
  })
  addSchema('evidence', `hash-final-${JSON.stringify(suffix)}`, {
    ...sample.revisions[0].evidence[0],
    backingFingerprint: 'a'.repeat(64) + suffix,
  })
}
for (const [kind, input] of [
  ['ref', '{"id":"zero","version":1,"deletionEpoch":-0}'],
  [
    'evidence',
    JSON.stringify(sample.revisions[0].evidence[0]).replace(
      '"deletionEpoch":0',
      '"deletionEpoch":-0',
    ),
  ],
]) {
  const result = schemaMap[kind].safeParse(JSON.parse(input))
  schemas.push({
    kind,
    name: 'signed-zero',
    input,
    accepted: result.success,
    normalized: result.success ? JSON.stringify(result.data) : null,
  })
}
const deepCases = [
  ['{"deletionEpoch":-0}', '{"deletionEpoch":0}'],
  ['{"a":1,"b":2}', '{"b":2,"a":1}'],
  ['[{"proofId":"p","sources":[]}]', '[{"sources":[],"proofId":"p"}]'],
].map(([left, right]) => ({
  left,
  right,
  equal: isDeepStrictEqual(JSON.parse(left), JSON.parse(right)),
}))
const schemaFaults = []
{
  const db = new Database(':memory:')
  db.exec('CREATE TABLE memory_commitment_heads(bad TEXT)')
  let error
  db.transaction(() => {
    try {
      new CommitmentStorage(db)
    } catch (e) {
      error = e.message
    }
  })()
  schemaFaults.push({
    name: 'base-atomic',
    error,
    tables: db
      .prepare("SELECT name FROM sqlite_master WHERE type='table' ORDER BY name")
      .all()
      .map((r) => r.name),
  })
  db.close()
}
{
  const db = new Database(':memory:')
  db.exec('CREATE TABLE memory_commitment_temporal_windows(bad TEXT)')
  let error
  db.transaction(() => {
    try {
      new CommitmentTemporalStore(db).replace(sample.head.id, sample.timeline)
    } catch (e) {
      error = e.message
    }
  })()
  schemaFaults.push({
    name: 'temporal-statement-prefix',
    error,
    tables: db
      .prepare("SELECT name FROM sqlite_master WHERE type='table' ORDER BY name")
      .all()
      .map((r) => r.name),
  })
  db.close()
}
const storageCases = []
for (const [method, sql] of [
  ['timeline', 'UPDATE memory_commitment_temporal_windows SET value=CAST(value AS BLOB)'],
  ['timeline', 'UPDATE memory_commitment_transitions SET value=CAST(value AS BLOB)'],
  [
    'timeline',
    'UPDATE memory_commitment_transitions SET mutation_version=CAST(mutation_version AS BLOB)',
  ],
  ['timeline', 'UPDATE memory_commitment_temporal_windows SET scene_id=CAST(scene_id AS BLOB)'],
  ['timeline', 'UPDATE memory_commitment_transitions SET scene_id=CAST(scene_id AS BLOB)'],
  [
    'ids',
    'UPDATE memory_commitment_temporal_windows SET id=CAST(id AS BLOB);UPDATE memory_commitment_transitions SET id=CAST(id AS BLOB)',
  ],
  ['head', 'UPDATE memory_commitment_heads SET scene_id=CAST(scene_id AS BLOB)'],
  ['head', 'UPDATE memory_commitment_heads SET value=CAST(value AS BLOB)'],
]) {
  const db = new Database(':memory:')
  const storage = new CommitmentStorage(db),
    temporal = new CommitmentTemporalStore(db)
  db.transaction(() => {
    for (const revision of sample.revisions) storage.put(sample.head, revision)
    temporal.replace(sample.head.id, sample.timeline)
  })()
  db.exec(sql)
  let error = null,
    value = null
  try {
    value =
      method === 'timeline'
        ? temporal.read(sample.head.id)
        : method === 'ids'
          ? temporal.allIds()
          : storage.head(sample.head.id)
  } catch (e) {
    error = e.message
  }
  storageCases.push({ method, sql, error, value: JSON.stringify(value) })
  db.close()
}
const compareKeys = new Set([
  'qualificationRef',
  'eventRef',
  'eventId',
  'revision',
  'subjectWitness',
  'sourceObservations',
  'sentAt',
  'receivedAt',
  'invalidatedBy',
  'personId',
  'sourceMessageId',
  'mentionPartIndex',
  'occurredAt',
  'validFrom',
  'validUntil',
])
function keys(v) {
  if (Array.isArray(v)) {
    for (const x of v) keys(x)
  } else if (v && typeof v === 'object') {
    for (const [k, x] of Object.entries(v)) {
      compareKeys.add(k)
      keys(x)
    }
  }
}
keys(sample)
for (const trace of traces)
  for (const step of trace.steps) {
    keys(JSON.parse(step.snapshots))
    for (const entry of JSON.parse(step.entries)) keys(JSON.parse(entry.payload))
  }
const comparisons = Object.fromEntries(
  [...compareKeys].map((a) => [
    a,
    Object.fromEntries([...compareKeys].map((b) => [b, a.localeCompare(b)])),
  ]),
)
const reads = []
for (const input of [
  {},
  { limit: 1 },
  { asOfRecorded: '2026-10-28T12:00:00.000Z' },
  { asOfRecorded: '2020-01-01T00:00:00Z' },
]) {
  const anchor = { ...sample.revisions[0].ref, mutationVersion: sample.head.mutationVersion }
  const value = readCommitmentTimeline(
    { sceneId: 'scene', simulation: false, provenance: 'source-only' },
    anchor,
    sample.timeline,
    input,
    {
      revision: (v) => sample.revisions.find((r) => r.ref.version === v),
      authority: {
        historical: () => ({
          record: record('root'),
          proofs: [proof('root'), proof('survivor', 'independent')],
        }),
      },
    },
  )
  reads.push({ anchor, input, value: JSON.stringify(value) })
}
{
  const anchor = { ...sample.revisions[0].ref, mutationVersion: sample.head.mutationVersion }
  const value = readCommitmentTimeline(
    { sceneId: 'scene', simulation: false, provenance: 'source-only' },
    anchor,
    sample.timeline,
    {},
    { revision: (v) => sample.revisions.find((r) => r.ref.version === v), authority: {} },
  )
  reads.push({ anchor, input: {}, noHistorical: true, value: JSON.stringify(value) })
}
const sourceFiles = [
  'src/memory/commitments/storage.ts',
  'src/memory/commitments/service.ts',
  'src/memory/commitments/temporal-store.ts',
  'src/memory/commitments/temporal-contracts.ts',
  'src/memory/commitments/transitions.ts',
  'src/memory/commitments/temporal-read.ts',
  'src/memory/domain/commitments.ts',
  'src/memory/domain/proofs.ts',
  'src/memory/domain/events.ts',
  'src/memory/lifecycle/commitment-privacy.ts',
  'src/memory/recovery/commitments.ts',
  'src/memory/recovery/commitment-contract.ts',
  'package-lock.json',
]
const output = {
  sourceNode: process.version,
  sourceLocale: new Intl.Collator().resolvedOptions().locale,
  sourceTimezone: new Intl.DateTimeFormat().resolvedOptions().timeZone,
  sources: Object.fromEntries(sourceFiles.map((path) => [path, hash(readFileSync(path, 'utf8'))])),
  traces,
  schemas,
  deepCases,
  schemaFaults,
  storageCases,
  comparisons,
  readSample: sample,
  reads,
}
const path = 'backend/internal/commitments/testdata/source-oracle.json'
const content = JSON.stringify(output, null, 2) + '\n'
if (process.argv.includes('--check')) assert.equal(readFileSync(path, 'utf8'), content)
else {
  mkdirSync('backend/internal/commitments/testdata', { recursive: true })
  writeFileSync(path, content)
}
console.log(
  `commitment privacy oracle: ${traces.length} traces, ${schemas.length} schemas, ${reads.length} reads`,
)
