/** Real TypeScript SourceEventStore oracle. Synthetic metadata only. */
import assert from 'node:assert/strict'
import { readFileSync, writeFileSync, mkdirSync } from 'node:fs'
import Database from 'better-sqlite3'
import {
  SourceEventStore,
  SourceEventMetadataSchema,
  SourceEventHeadSchema,
  ensureSourceEventLookupIndex,
} from '../../src/memory/storage/event-revisions.ts'
import {
  SourceEventIdentitySchema,
  SourceEventRefSchema,
  TrustedSourceChangeSchema,
  sourceIdentityKey,
  sourceEventHeadHash,
} from '../../src/memory/domain/events.ts'
import { z } from 'zod'
assert.equal(process.version, 'v24.22.1')
const identity = {
  sceneId: 'scene',
  adapterId: 'synthetic',
  accountId: 'account',
  messageId: 'message',
  senderId: 'speaker',
}
const ingress = (i = identity) => ({
  kind: 'ingress',
  identity: i,
  bodyHash: 'a'.repeat(64),
  occurredAt: null,
})
const ref = (revision = 1) => ({ eventId: 'event-1', revision, deletionEpoch: 0 })
const edit = (sequence, extra = {}) => ({
  kind: 'edit',
  identity,
  bodyHash: (sequence % 2 ? 'b' : 'a').repeat(64),
  occurredAt: null,
  editId: `edit-${sequence}`,
  order: { kind: 'platform_sequence', sequence },
  ...extra,
})
const cas = (revision, extra = {}) =>
  edit(revision, { order: { kind: 'observed_cas', expectedBase: ref(revision) }, ...extra })
const traces = []
function trace(name, operations, options = {}) {
  const db = new Database(':memory:')
  let serial = 0
  const callbacks = []
  const store = new SourceEventStore(db, {
    id: () => `event-${++serial}`,
    now: () => '2026-10-28T00:00:00.000Z',
    ...options,
    beforeWrite: (m) => callbacks.push(['before', m]),
  })
  store.subscribeCommitted((m) => callbacks.push(['committed', m]))
  const steps = []
  for (const op of operations) {
    let result
    let error
    try {
      const [method, ...args] = op
      result = store[method](...args) ?? null
    } catch (e) {
      error = e.message
    }
    const tables = db
      .prepare(
        "SELECT name FROM sqlite_master WHERE type='table' AND name LIKE 'source_event_%' ORDER BY name",
      )
      .all()
      .map((r) => r.name)
    const rows = Object.fromEntries(
      tables.map((table) => [table, db.prepare(`SELECT * FROM ${table} ORDER BY 1,2`).all()]),
    )
    steps.push({
      op: JSON.stringify(op),
      result: JSON.stringify(result ?? null),
      ...(error ? { error } : {}),
      callbacks: JSON.stringify(callbacks.splice(0)),
      rows: JSON.stringify(rows),
    })
  }
  traces.push({ name, options, steps })
  db.close()
}
trace('enrollment-and-sequence', [
  ['registered', 'scene', 'message'],
  ['current', ref(), identity],
  ['observe', edit(1)],
  ['observe', ingress()],
  ['observe', ingress()],
  ['observe', { ...ingress(), bodyHash: 'c'.repeat(64) }],
  ['observe', ingress({ ...identity, senderId: 'collision' })],
  ['observe', edit(1)],
  ['observe', edit(1)],
  ['observe', edit(1, { bodyHash: 'c'.repeat(64) })],
  ['observe', edit(0)],
  ['observe', cas(2)],
  ['observe', edit(2)],
  ['current', ref(), identity],
  ['historical', ref(), identity],
  ['capture', identity, 'a'.repeat(64), ref()],
  ['capture', identity, 'a'.repeat(64), ref(3)],
  ['hasEdits', identity],
  ['revokeAll', 'scene', 'message'],
  ['observe', ingress()],
  ['historical', ref(), identity],
  ['registered', 'scene', 'message'],
  ['revokeAll', 'scene', 'message'],
])
trace('observed-cas', [
  ['observe', ingress()],
  ['observe', cas(1)],
  ['observe', cas(1)],
  ['observe', cas(1, { editId: 'stale' })],
  ['observe', cas(2)],
  ['observe', edit(3)],
  ['current', { ...ref(3), eventId: 'other' }, identity],
  ['current', { ...ref(3), deletionEpoch: 1 }, identity],
])
trace(
  'retention',
  [
    ['observe', ingress()],
    ...Array.from({ length: 6 }, (_, i) => ['observe', edit(i + 1)]),
    ['historical', ref(), identity],
    ['historical', ref(6), identity],
    ['observe', edit(1)],
    ['observe', edit(6)],
  ],
  { maxRevisions: 2, maxEditKeys: 3 },
)
trace(
  'embedding-capacity',
  [
    ['observe', ingress()],
    ['observe', ingress({ ...identity, messageId: 'second' })],
    ['revokeAll', 'scene', 'message'],
    ['observe', ingress({ ...identity, messageId: 'second' })],
  ],
  { maxHeadsPerScene: 1, maxHeadsTotal: 2 },
)
for (const text of ['😀', '\ud800', '\udfff', '<>&\u2028\u2029', 'zero\0tail']) {
  const i = { ...identity, sceneId: text, messageId: `message-${text}` }
  trace(`text-${JSON.stringify(text)}`, [
    ['observe', ingress(i)],
    ['headMetadata', i],
    ['registered', i.sceneId, i.messageId],
  ])
}
const hashes = [
  identity,
  ...['\ud800', '\udfff', '😀', '<>&\u2028'].map((sceneId) => ({ ...identity, sceneId })),
].map((i) => {
  const metadata = {
    identity: i,
    ref: ref(),
    bodyHash: 'a'.repeat(64),
    observedAt: '2026-10-28T00:00:00.000Z',
    occurredAt: null,
    bodyCoverage: 'unavailable',
  }
  return {
    identity: JSON.stringify(i),
    metadata: JSON.stringify(metadata),
    identityHash: sourceIdentityKey(i),
    headHash: sourceEventHeadHash(metadata, null, null),
  }
})
const faults = []
for (const stage of ['before', 'committed'])
  for (const outer of [false, true])
    for (const caught of [false, true]) {
      const db = new Database(':memory:')
      const calls = []
      let fail = false
      const store = new SourceEventStore(db, {
        id: () => 'event-1',
        now: () => '2026-10-28T00:00:00.000Z',
        beforeWrite: (m) => {
          calls.push(['before', m.kind])
          if (fail && stage === 'before') throw Error('synthetic-before')
        },
      })
      store.subscribeCommitted((m) => {
        calls.push(['committed', m.kind])
        if (fail && stage === 'committed') throw Error('synthetic-committed')
      })
      store.observe(ingress())
      calls.length = 0
      fail = true
      let error = null
      const action = () => {
        if (caught) {
          try {
            store.observe(edit(1))
          } catch (e) {
            calls.push(['caught', e.message])
          }
        } else store.observe(edit(1))
      }
      try {
        if (outer) db.transaction(action).immediate()
        else action()
      } catch (e) {
        error = e.message
      }
      faults.push({
        stage,
        outer,
        caught,
        error,
        calls,
        head: JSON.stringify(store.headMetadata(identity)),
        revisionCount: db.prepare('SELECT count(*) n FROM source_event_revisions').get().n,
      })
      db.close()
    }
const subscriptions = []
for (const mode of [
  'remove-next-add-new',
  'delete-readd-current',
  'same-function-twice',
  'nested-observe',
  'mutate-listener-copy',
]) {
  const db = new Database(':memory:')
  let serial = 0,
    changed = false
  const calls = []
  const store = new SourceEventStore(db, {
    id: () => `event-${++serial}`,
    now: () => '2026-10-28T00:00:00.000Z',
  })
  let stopA, stopB
  const record = (name, m) =>
    calls.push({
      name,
      messageId: m.identity.messageId,
      revision: m.ref.revision,
      headRevision: store.headMetadata(m.identity)?.ref.revision ?? null,
    })
  const c = (m) => record('C', m)
  const a = (m) => {
    record('A', m)
    if (!changed) {
      changed = true
      if (mode === 'remove-next-add-new') {
        stopB()
        store.subscribeCommitted(c)
      }
      if (mode === 'delete-readd-current') {
        stopA()
        store.subscribeCommitted(a)
      }
      if (mode === 'nested-observe') {
        store.subscribeCommitted(c)
        store.observe(ingress({ ...identity, messageId: 'nested' }))
      }
    }
    if (mode === 'mutate-listener-copy') {
      m.ref.revision = 999
      m.identity.messageId = 'mutated'
      m.metadata.bodyHash = 'c'.repeat(64)
    }
  }
  stopA = store.subscribeCommitted(a)
  stopB = store.subscribeCommitted((m) => record('B', m))
  if (mode === 'same-function-twice') store.subscribeCommitted(a)
  const result = store.observe(ingress())
  subscriptions.push({ mode, calls, result, head: store.headMetadata(identity) })
  db.close()
}
const schemaTransitions = []
{
  const db = new Database(':memory:')
  const store = new SourceEventStore(db)
  const inspect = (name, fn) => {
    let result = null,
      error = null
    try {
      result = fn() ?? null
    } catch (e) {
      error = e.message
    }
    schemaTransitions.push({
      name,
      result: JSON.stringify(result),
      error,
      tables: db
        .prepare(
          "SELECT name FROM sqlite_master WHERE name LIKE 'source_event_%' OR name='idx_source_event_heads_scene_message' ORDER BY name",
        )
        .all()
        .map((r) => r.name),
    })
  }
  inspect('empty-read', () => store.registered('scene', 'message'))
  inspect('empty-index', () => ensureSourceEventLookupIndex(db))
  db.exec(
    'CREATE TABLE source_event_heads(identity_key TEXT PRIMARY KEY,event_id TEXT UNIQUE NOT NULL,scene_id TEXT NOT NULL,message_id TEXT NOT NULL,value TEXT NOT NULL)',
  )
  inspect('partial-index', () => ensureSourceEventLookupIndex(db))
  inspect('partial-read', () => store.registered('scene', 'message'))
  db.exec('DROP TABLE source_event_heads; PRAGMA query_only=ON')
  inspect('readonly-empty-read', () => store.registered('scene', 'message'))
  inspect('readonly-observe', () => store.observe(ingress()))
  db.exec('PRAGMA query_only=OFF')
  new SourceEventStore(db, { id: () => 'event-1', now: () => '2026-10-28T00:00:00.000Z' }).observe(
    ingress(),
  )
  inspect('enrolled-read', () => store.registered('scene', 'message'))
  db.exec('DROP INDEX idx_source_event_heads_scene_message')
  inspect('complete-index', () => ensureSourceEventLookupIndex(db))
  db.exec('DROP TABLE source_event_edit_keys')
  inspect('dropped-table-read', () => store.registered('scene', 'message'))
  db.close()
}
const validations = []
const validate = (schema, values) => {
  const schemas = {
    identity: SourceEventIdentitySchema,
    ref: SourceEventRefSchema,
    change: TrustedSourceChangeSchema,
    metadata: SourceEventMetadataSchema,
    head: SourceEventHeadSchema,
  }
  for (const value of values) {
    const result = schemas[schema].safeParse(value)
    validations.push({
      schema,
      input: JSON.stringify(value),
      valid: result.success,
      ...(result.success ? { output: JSON.stringify(result.data) } : {}),
    })
  }
}
validate('identity', [
  identity,
  { ...identity, extra: true },
  { ...identity, sceneId: '' },
  ...['😀', 'a', '\ud800'].flatMap((x) =>
    [512, 513].map((n) => ({ ...identity, sceneId: x.repeat(n) })),
  ),
])
validate('ref', [
  ref(),
  ...[-1, 0, 1.5, 9007199254740991, 9007199254740992].map((revision) => ({ ...ref(), revision })),
  { ...ref(), deletionEpoch: -1 },
])
validate('change', [
  ingress(),
  edit(1),
  cas(1),
  { ...ingress(), order: null },
  { ...edit(1), order: { kind: 'platform_sequence', sequence: -1 } },
  ...[
    '0000-02-29T00:00:00Z',
    '2026-02-29T00:00:00Z',
    '2024-02-29T23:59:59.123456789123+23:59',
    '2026-10-28T00:00Z',
    '2026-10-28T00:00:60Z',
    '2026-10-28T00:00:00+24:00',
  ].map((occurredAt) => ({ ...ingress(), occurredAt })),
])
const output =
  JSON.stringify(
    {
      node: process.version,
      faults,
      subscriptions,
      schemaTransitions,
      validations,
      schemas: {
        identity: z.toJSONSchema(SourceEventIdentitySchema),
        ref: z.toJSONSchema(SourceEventRefSchema),
        change: z.toJSONSchema(TrustedSourceChangeSchema),
        metadata: z.toJSONSchema(SourceEventMetadataSchema),
        head: z.toJSONSchema(SourceEventHeadSchema),
      },
      hashes,
      traces,
    },
    null,
    2,
  ) + '\n'
const path = 'backend/internal/sourceevents/testdata/source-events.json'
if (process.argv.includes('--check')) assert.equal(readFileSync(path, 'utf8'), output)
else {
  mkdirSync('backend/internal/sourceevents/testdata', { recursive: true })
  writeFileSync(path, output)
}
console.log(
  `SourceEventStore oracle: ${traces.length} traces, ${traces.reduce((n, t) => n + t.steps.length, 0)} transitions, ${hashes.length} hash cases`,
)
