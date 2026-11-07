/** Actual TS relation service/storage oracle. Ports are explicit synthetic
 * callbacks; real assertion authority and lifecycle are separate integration tests. */
import assert from 'node:assert/strict'
import Database from 'better-sqlite3'
import { readFileSync, writeFileSync } from 'node:fs'
import { RelationCandidates } from '../../src/memory/relations/candidates.ts'
import { RelationProposalSchema } from '../../src/memory/domain/relations.ts'
assert.equal(process.version, 'v24.22.1')
const clone = structuredClone
const ref = { id: 'a', version: 1, deletionEpoch: 0 }
const context = { sceneId: 'scene', simulation: false, provenance: 'contextual' }
const proof = (id = 'proof-a', extra = {}) => ({
  proofId: id,
  sources: [{ sceneId: 'scene', messageId: 'message', senderId: 'someone-else' }],
  parents: [],
  parentReferences: [],
  subjectWitness: { kind: 'self', personId: 'person', sourceMessageId: 'message' },
  ...extra,
})
const view = (extra = {}, proofs = [proof()]) => ({
  record: {
    id: 'a',
    scope: { kind: 'scene', sceneId: 'scene', personId: 'person' },
    status: 'active',
    lineageStatus: 'verified',
    epistemicKind: 'explicit',
    ...extra,
  },
  proofs,
})
const proposal = (extra = {}) => ({
  predicate: 'participates_in',
  modality: 'asserted',
  temporalScope: 'current',
  personId: 'person',
  target: { kind: 'new', entityKind: 'plan', title: 'Synthetic plan' },
  polarity: 'positive',
  conditions: [],
  ...extra,
})
const entity = {
  id: 'entity',
  sceneId: 'scene',
  kind: 'plan',
  title: 'Synthetic plan',
  backing: ref,
}
const record = (id = 'edge', extra = {}) => ({
  id,
  version: 1,
  sceneId: 'scene',
  predicate: 'participates_in',
  modality: 'asserted',
  temporalScope: 'current',
  personId: 'person',
  target: entity,
  polarity: 'positive',
  conditions: [],
  epistemicKind: 'explicit',
  backing: ref,
  proofs: [proof()],
  status: 'accepted',
  admission: 'automatic',
  autoReason: 'self_explicit_no_visible_conflict',
  createdAt: '2026-11-01T00:00:00.000Z',
  ...extra,
})
const admit = (proposals = [proposal()], backing = ref) => ({ op: 'admit', proposals, backing })
const list = (options = {}) => ({ op: 'list', options })
const seed = (r = record()) => ({ op: 'seed', record: r, entity: r.target, key: r.id })
const review = (extra = {}) => ({
  op: 'review',
  id: 'edge',
  version: 1,
  status: 'accepted',
  ...extra,
})
const scenarios = []
const add = (name, steps, extra = {}) =>
  scenarios.push({
    name,
    context,
    views: { a: view() },
    flags: { enabled: true, learningEnabled: true, autoAdmission: true },
    steps,
    ...extra,
  })
add('automatic duplicate consumes entity id and review', [
  admit(),
  admit(),
  list(),
  review({ id: 'id-2', actorId: 'reviewer' }),
  review({ id: 'id-2', version: 2, status: 'rejected', actorId: '' }),
  list(),
  { op: 'purge', id: 'a' },
  list(),
])
add(
  'disabled ordering and invalid reference',
  [
    admit([], { id: '', version: 0, deletionEpoch: 0 }),
    admit(),
    list(),
    { op: 'flags', value: { enabled: true, learningEnabled: false } },
    admit(),
    { op: 'flags', value: { learningEnabled: true } },
    admit([proposal(), proposal(), proposal(), proposal()]),
  ],
  { flags: { enabled: false, learningEnabled: true, autoAdmission: true } },
)
add('simulation short circuits flags', [admit(), list()], {
  context: { ...context, simulation: true },
})
add('strict proposal rejection and candidate semantics', [
  admit([
    proposal({ unknown: true }),
    proposal({ modality: 'possible' }),
    proposal({ polarity: 'negative', conditions: ['Only if invited'] }),
  ]),
  list(),
  list({ status: 'candidate', limit: 1 }),
])
add('time bounds prevent automatic and equality allowed', [
  admit([
    proposal({ validFrom: '2026-11-01T00:00:00Z', validUntil: '2026-11-01T00:00:00Z' }),
    proposal({ temporalScope: 'future' }),
    proposal({ occurredAt: '2026-11-01T00:00:00+02:00' }),
  ]),
  list(),
])
add('backing time callback order', [admit(), list()], {
  views: { a: view({ validFrom: '2026-10-31T00:00:00Z', validUntil: '2026-11-02T00:00:00Z' }) },
})
add('expired backing remains visible candidate', [admit(), list()], {
  views: { a: view({ validUntil: '2026-11-01T00:00:00Z' }) },
})
add('disputed nonquarantined lineage remains candidate', [admit(), list()], {
  views: { a: view({ status: 'disputed', lineageStatus: 'legacy_unknown' }) },
})
add('proof witness requires message but not sender identity', [
  admit(),
  list(),
  {
    op: 'setView',
    id: 'a',
    view: view({}, [
      proof('proof-a', {
        subjectWitness: { kind: 'self', personId: 'person', sourceMessageId: 'missing' },
      }),
    ]),
  },
  list(),
  review({ id: 'id-2' }),
])
add('new proof cannot authorize old edge', [
  admit(),
  { op: 'setView', id: 'a', view: view({}, [proof('new')]) },
  list(),
  review({ id: 'id-2', version: 99 }),
])
add(
  'proof intersection retained original material',
  [
    seed(record('edge', { proofs: [proof(), proof('other')] })),
    list(),
    review(),
    list(),
    { op: 'setView', id: 'a', view: view({}, [proof('other')]) },
    list(),
  ],
  { views: { a: view() } },
)
add('proof object key order ignored', [
  seed(
    record('edge', {
      proofs: [
        {
          subjectWitness: proof().subjectWitness,
          parentReferences: [],
          parents: [],
          sources: proof().sources,
          proofId: 'proof-a',
        },
      ],
    }),
  ),
  list(),
])
add('existing entity requires exposure before duplicate', [
  seed(),
  admit([proposal({ target: { kind: 'existing', entityId: 'entity' } })]),
  { op: 'context', value: { exposedEntities: [{ entityId: 'entity', backingRef: ref }] } },
  admit([proposal({ target: { kind: 'existing', entityId: 'entity' } })]),
  { op: 'context', value: { exposedEntities: [] } },
  admit([proposal({ target: { kind: 'existing', entityId: 'entity' } })]),
])
add(
  'existing target conflict and origin',
  [
    seed(record('edge', { modality: 'possible' })),
    admit([proposal({ target: { kind: 'existing', entityId: 'entity' } })]),
    list(),
  ],
  { context: { ...context, exposedEntities: [{ entityId: 'entity', backingRef: ref }] } },
)
add(
  'review without learning preserves unknown fields and actor',
  [
    seed(record('edge', { actorId: 'old', extension: { retained: true } })),
    review({ actorId: '' }),
    review({ version: 1 }),
    list(),
  ],
  { flags: { enabled: true, learningEnabled: false, autoAdmission: false } },
)
add('review write succeeds but second projection disappears', [
  seed(),
  { op: 'setCurrentScript', responses: [view(), view(), null] },
  review(),
  { op: 'setCurrentScript', responses: [] },
  list(),
])
add('list projects all eligible before limit and projects twice', [
  { op: 'seedBulk', records: [record('a'), record('b'), record('c')] },
  list({ limit: 1 }),
  {
    op: 'setCurrentScript',
    responses: [view(), view(), view(), view(), view(), view(), null, null, null],
  },
  list({ limit: 1 }),
])
add('raw 201 rows impose limited coverage despite filtering', [
  {
    op: 'seedBulk',
    records: Array.from({ length: 201 }, (_, i) =>
      record(`edge-${String(i).padStart(3, '0')}`, { personId: 'another' }),
    ),
  },
  list({ personId: 'person' }),
  admit(),
  list({ personId: 'person' }),
])
add('embedded target remains usable after entity deletion', [
  seed(),
  { op: 'sql', statement: 'DELETE FROM memory_relation_entities' },
  list(),
])
add('monotonic tombstone prevents resurrection and orphan retained', [
  seed(),
  {
    op: 'sql',
    statement: "INSERT INTO memory_relation_entities VALUES('orphan','scene','other','{}')",
  },
  { op: 'purge', id: 'a' },
  { op: 'storagePut', record: record(), key: 'again', entity },
  { op: 'changed', id: 'a' },
])
add('caught candidate failure retains inserted entity prefix', [
  {
    op: 'sql',
    statement:
      "CREATE TRIGGER deny_candidate BEFORE INSERT ON memory_relation_candidates BEGIN SELECT RAISE(ABORT,'candidate_denied'); END",
  },
  admit(),
  { op: 'sql', statement: 'DROP TRIGGER deny_candidate' },
  admit(),
  list(),
])
add('caller rollback removes entity and candidate', [
  { ...admit(), rollback: true },
  list(),
  admit(),
  list(),
])
add('purge failure retains tombstone prefix when caught', [
  seed(),
  {
    op: 'sql',
    statement:
      "CREATE TRIGGER deny_delete BEFORE DELETE ON memory_relation_candidates BEGIN SELECT RAISE(ABORT,'delete_denied'); END",
  },
  { op: 'purge', id: 'a' },
  { op: 'sql', statement: 'DROP TRIGGER deny_delete' },
  { op: 'purge', id: 'a' },
])
add('storage CAS conflict', [
  seed(),
  { op: 'storageUpdate', record: record('edge', { version: 2 }), version: 7 },
  list(),
])
add(
  'read limit validation precedes disabled',
  [list({ limit: 0 }), list({ limit: 41 }), list({ limit: 1.5 })],
  { flags: { enabled: false, learningEnabled: false, autoAdmission: false } },
)
// Backing is not schema-normalized: its JSON property order changes admission keys.
add('backing property insertion order participates in admission key', [
  admit(),
  admit([proposal()], { deletionEpoch: 0, version: 1, id: 'a' }),
  list(),
])
add('unique key failure preserves fresh entity prefix', [
  seed(),
  {
    op: 'storagePut',
    record: record('second', { target: { ...entity, id: 'fresh-entity' } }),
    key: 'edge',
    entity: { ...entity, id: 'fresh-entity' },
  },
])
add('target-backed purge and monotonic tombstone', [
  seed(record('edge', { backing: { id: 'b', version: 1, deletionEpoch: 0 } })),
  { op: 'sql', statement: "INSERT INTO memory_relation_tombstones VALUES('edge',99)" },
  { op: 'purge', id: 'a' },
])
const twoSources = [
  { sceneId: 'scene', messageId: 'message', senderId: 'someone-else' },
  { sceneId: 'scene', messageId: 'message2', senderId: 'someone-else' },
]
add(
  'proof source array order differs despite canonical key',
  [
    seed(record('edge', { proofs: [proof('proof-a', { sources: twoSources })] })),
    list(),
    {
      op: 'setView',
      id: 'a',
      view: view({}, [proof('proof-a', { sources: [...twoSources].reverse() })]),
    },
    list(),
  ],
  { views: { a: view({}, [proof('proof-a', { sources: twoSources })]) } },
)
add('Date TimeClip errors preserve rows and boundary ISO', [
  { op: 'clock', value: 8640000000000001 },
  admit(),
  seed(),
  review(),
  { op: 'clock', value: -8640000000000000 },
  review(),
  list(),
])
const schemaInputs = [
  proposal(),
  proposal({ target: { kind: 'new', entityKind: 'event', title: '😀'.repeat(250) } }),
  proposal({ target: { kind: 'new', entityKind: 'event', title: '😀'.repeat(251) } }),
  proposal({ personId: 'x'.repeat(512) }),
  proposal({ personId: 'x'.repeat(513) }),
  proposal({ conditions: Array(8).fill('x') }),
  proposal({ conditions: Array(9).fill('x') }),
  proposal({ validFrom: '2026-11-02T00:00:00Z', validUntil: '2026-11-01T00:00:00Z' }),
  proposal({ occurredAt: '2026-02-30T00:00:00Z' }),
  proposal({ occurredAt: '2026-11-01T00:00:00' }),
  proposal({ occurredAt: '2026-11-01T00:00:00+08:00' }),
  proposal({ extra: null }),
  proposal({ target: { kind: 'existing', entityId: 'entity', extra: true } }),
]
function run(input) {
  const db = new Database(':memory:')
  let calls = [],
    nextID = 0,
    now = Date.parse('2026-11-01T00:00:00Z'),
    flags = clone(input.flags),
    views = clone(input.views),
    ctx = clone(input.context),
    script = []
  const authority = Object.fromEntries(
    ['enabled', 'learningEnabled', 'autoAdmission'].map((name) => [
      name,
      (value) => {
        calls.push({ op: name, context: clone(value) })
        return flags[name]
      },
    ]),
  )
  authority.current = (value, reference) => {
    calls.push({ op: 'current', context: clone(value), reference: clone(reference) })
    return clone(script.length ? (script.shift() ?? undefined) : (views[reference.id] ?? undefined))
  }
  const service = new RelationCandidates(db, authority, {
    now: () => {
      calls.push({ op: 'now', value: now })
      return now
    },
    id: () => {
      const value = `id-${++nextID}`
      calls.push({ op: 'id', value })
      return value
    },
  })
  const state = () =>
    Object.fromEntries(
      [
        ['entities', 'memory_relation_entities'],
        ['candidates', 'memory_relation_candidates'],
        ['tombstones', 'memory_relation_tombstones'],
      ].map(([key, table]) => [key, db.prepare(`SELECT * FROM ${table}`).all()]),
    )
  const insert = (r, e = r.target, key = r.id) => {
    if (e && !service.storage.entity(e.id))
      db.prepare('INSERT INTO memory_relation_entities VALUES(?,?,?,?)').run(
        e.id,
        e.sceneId,
        e.backing.id,
        JSON.stringify(e),
      )
    service.storage.put(r, key)
  }
  const trace = input.steps.map((step) => {
    calls = []
    let result, error
    const action = () => {
      switch (step.op) {
        case 'admit':
          return service.admit(ctx, step.backing, step.proposals)
        case 'list':
          return service.list(ctx, step.options)
        case 'review':
          return service.review(ctx, step.id, step.version, step.status, step.actorId)
        case 'purge':
          return service.purgeAssertion(step.id)
        case 'changed':
          return service.assertionChanged(step.id)
        case 'seed':
          return insert(step.record, step.entity, step.key)
        case 'seedBulk':
          for (const r of step.records) insert(r)
          return
        case 'storagePut':
          return service.storage.put(step.record, step.key, step.entity)
        case 'storageUpdate':
          return service.storage.update(step.record, step.version)
        case 'sql':
          db.exec(step.statement)
          return
        case 'setView':
          views[step.id] = clone(step.view)
          return
        case 'setCurrentScript':
          script = clone(step.responses)
          return
        case 'flags':
          Object.assign(flags, step.value)
          return
        case 'context':
          Object.assign(ctx, step.value)
          return
        case 'clock':
          now = step.value
          return
        default:
          throw new Error(`unknown fixture action ${step.op}`)
      }
    }
    db.exec('BEGIN')
    try {
      result = action()
    } catch (e) {
      error = e.message
    }
    if (db.inTransaction) db.exec(step.rollback ? 'ROLLBACK' : 'COMMIT')
    return {
      ...(error === undefined
        ? result === undefined
          ? { undefined: true }
          : { result }
        : { error }),
      calls,
      db: state(),
    }
  })
  db.close()
  return { ...input, trace }
}
for (const value of [
  '2026-11-01T00:00Z',
  '2026-11-01T00:00:00.123456Z',
  '2024-02-29T00:00:00Z',
  '2023-02-29T00:00:00Z',
  '2026-11-01T00:00:00+23:59',
  '2026-11-01T00:00:00+24:00',
  '2026-11-01T24:00:00Z',
  '2026-11-01T00:00:60Z',
  '0000-01-01T00:00:00Z',
])
  schemaInputs.push(proposal({ occurredAt: value }))
schemaInputs.push(
  proposal({ target: { kind: 'new', entityKind: 'plan', title: '😀'.repeat(500) } }),
  proposal({ target: { kind: 'new', entityKind: 'plan', title: '😀'.repeat(501) } }),
  proposal({ target: { kind: 'new', entityKind: 'plan', title: '\ud800' } }),
)
const output = {
  source: 'Actual TypeScript RelationCandidates/RelationStorage with explicit synthetic ports',
  scenarios: scenarios.map(run),
  schemas: schemaInputs.map((input) => {
    const r = RelationProposalSchema.safeParse(input)
    return { input, success: r.success, ...(r.success ? { data: r.data } : {}) }
  }),
}
const text = JSON.stringify(output, null, 2) + '\n'
const destination = new URL('../internal/relations/testdata/source-oracle.json', import.meta.url)
if (process.argv.includes('--check')) assert.equal(readFileSync(destination, 'utf8'), text)
else writeFileSync(destination, text)
console.log(
  `${output.scenarios.length} actual relation traces; ${output.schemas.length} proposal schemas`,
)
