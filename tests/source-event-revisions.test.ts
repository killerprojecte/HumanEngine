import assert from 'node:assert/strict'
import test from 'node:test'
import Database from 'better-sqlite3'
import { SourceEventStore } from '../src/memory/storage/event-revisions.ts'
import { SourceEventIngest } from '../src/memory/events/ingest.ts'
import type {
  SourceEventIdentity,
  SourceEventRef,
  TrustedSourceChange,
} from '../src/memory/domain/events.ts'
const identity: SourceEventIdentity = {
  sceneId: 'scene',
  adapterId: 'adapter',
  accountId: 'account',
  messageId: 'message',
  senderId: 'person',
}
const first: TrustedSourceChange = {
  kind: 'ingress',
  identity,
  bodyHash: 'a'.repeat(64),
  occurredAt: null,
}
const edit = (base: SourceEventRef, editId = 'edit', hash = 'b'): TrustedSourceChange => ({
  kind: 'edit',
  identity,
  bodyHash: hash.repeat(64),
  occurredAt: null,
  editId,
  order: { kind: 'observed_cas', expectedBase: base },
})
function fixture(options: ConstructorParameters<typeof SourceEventStore>[1] = {}) {
  const db = new Database(':memory:')
  return {
    db,
    store: new SourceEventStore(db, {
      id: () => 'event',
      now: () => '2026-10-28T00:00:00.000Z',
      ...options,
    }),
  }
}
test('ordinary duplicates never infer edits; exact metadata snapshots distinguish current and history', () => {
  const { db, store } = fixture()
  const one = store.observe(first).ref!
  assert.equal(store.observe(first).status, 'duplicate')
  assert.equal(store.observe({ ...first, bodyHash: 'c'.repeat(64) }).status, 'conflict')
  const two = store.observe(edit(one))
  assert.equal(two.status, 'edited')
  assert.equal(store.current(one, identity).reason, 'revision_changed')
  assert.equal(store.historical(one, identity).reason, 'historical_metadata_only')
  assert.equal(store.current(two.ref!, identity).eligible, true)
  assert.equal(store.capture(identity, first.bodyHash, one), undefined)
  assert.equal(store.observe(edit(one)).status, 'duplicate')
  assert.equal(store.observe(edit(one, 'edit', 'c')).status, 'conflict')
  assert.equal(store.observe(edit(one, 'late')).status, 'conflict')
  assert.equal(store.observe(edit(two.ref!, 'revert', 'a')).status, 'edited')
  assert.equal(
    store.observe({ ...first, identity: { ...identity, senderId: 'other' } }).status,
    'conflict',
  )
  assert.equal(store.historical(one, { ...identity, sceneId: 'other' }).eligible, false)
  assert.equal(
    JSON.stringify(db.prepare('SELECT * FROM source_event_revisions').all()).includes(
      'raw message',
    ),
    false,
  )
  db.close()
})
test('bounded revisions and edit keys reject stale original CAS and sequence after eviction', () => {
  const { db, store } = fixture({ maxRevisions: 1, maxEditKeys: 1 })
  const one = store.observe(first).ref!
  const initial = edit(one)
  const two = store.observe(initial).ref!
  store.observe(edit(two, 'next'))
  assert.equal(store.historical(one, identity).reason, 'missing')
  assert.equal(store.observe(initial).status, 'conflict')
  const sequence = (n: number): TrustedSourceChange => ({
    kind: 'edit',
    identity,
    bodyHash: 'd'.repeat(64),
    occurredAt: null,
    editId: `seq-${n}`,
    order: { kind: 'platform_sequence', sequence: n },
  })
  assert.equal(store.observe(sequence(10)).status, 'unorderable')
  const otherIdentity = { ...identity, messageId: 'sequence-message' }
  const sequenceStore = new SourceEventStore(db, {
    id: () => 'sequence-event',
    maxRevisions: 1,
    maxEditKeys: 1,
  })
  sequenceStore.observe({ ...first, identity: otherIdentity })
  const ordered = (n: number) => ({ ...sequence(n), identity: otherIdentity })
  assert.equal(sequenceStore.observe(ordered(10)).status, 'edited')
  assert.equal(sequenceStore.observe(ordered(11)).status, 'edited')
  assert.equal(sequenceStore.observe(ordered(10)).status, 'conflict')
  assert.equal(
    (db.prepare('SELECT count(*) n FROM source_event_revisions').get() as { n: number }).n,
    2,
  )
  assert.equal(
    (db.prepare('SELECT count(*) n FROM source_event_edit_keys').get() as { n: number }).n,
    2,
  )
  db.close()
})
test('caller transaction and synchronous authority hook rollback all metadata', () => {
  const { db, store } = fixture({
    beforeWrite: () => {
      assert.equal(db.inTransaction, true)
    },
  })
  assert.throws(() =>
    db.transaction(() => {
      store.observe(first)
      throw Error('rollback')
    })(),
  )
  const one = store.observe(first).ref!
  assert.equal(one.revision, 1)
  const rejecting = new SourceEventStore(db, {
    beforeWrite: () => {
      throw Error('authority_denied')
    },
  })
  assert.throws(() => rejecting.observe(edit(one)), /authority_denied/)
  assert.equal(store.current(one, identity).eligible, true)
  const asyncHook = new SourceEventStore(db, {
    beforeWrite: (() => Promise.resolve()) as () => void,
  })
  assert.throws(() => asyncHook.observe(edit(one)), /async_authority/)
  db.close()
})
test('simulation lazily creates no tables; recall removes every metadata revision and blocks restart replay', () => {
  const { db, store } = fixture()
  const ingest = new SourceEventIngest(store)
  assert.equal(ingest.observe(first, { simulation: true }).status, 'simulation')
  assert.deepEqual(
    db.prepare("SELECT name FROM sqlite_master WHERE name LIKE 'source_event_%'").all(),
    [],
  )
  const one = store.observe(first).ref!
  store.observe(edit(one))
  store.revokeAll(identity.sceneId, identity.messageId)
  for (const table of ['source_event_heads', 'source_event_revisions', 'source_event_edit_keys'])
    assert.equal((db.prepare(`SELECT count(*) n FROM ${table}`).get() as { n: number }).n, 0)
  const restarted = new SourceEventStore(db)
  assert.equal(restarted.observe(first).status, 'recalled')
  assert.equal(restarted.historical(one, identity).reason, 'recalled')
  assert.equal(restarted.observe(edit(one)).status, 'recalled')
  restarted.revokeAll('other', 'never-observed')
  assert.equal(
    restarted.observe({
      ...first,
      identity: { ...identity, sceneId: 'other', messageId: 'never-observed' },
    }).status,
    'recalled',
  )
  db.close()
})
test('invalid input, malformed persistence and identity-mismatched revision fail closed', () => {
  const { db, store } = fixture()
  assert.throws(() => store.observe({ ...first, occurredAt: 'yesterday' }))
  assert.throws(() => store.observe({ ...first, rawBody: 'secret' } as TrustedSourceChange))
  const ref = store.observe(first).ref!
  db.prepare('UPDATE source_event_revisions SET value=?').run(
    JSON.stringify({
      identity,
      ref: { ...ref, eventId: 'wrong' },
      bodyHash: first.bodyHash,
      observedAt: '2026-10-28T00:00:00Z',
      occurredAt: null,
      bodyCoverage: 'unavailable',
    }),
  )
  assert.throws(() => store.current(ref, identity), /corrupt/)
  db.prepare('UPDATE source_event_heads SET value=?').run('{')
  assert.throws(() => store.capture(identity, first.bodyHash, ref), /corrupt/)
  db.close()
})

test('ordering mode cannot switch; reverted bytes do not upgrade an old captured snapshot', () => {
  const { db, store } = fixture()
  const one = store.observe(first).ref!
  const two = store.observe(edit(one)).ref!
  const three = store.observe(edit(two, 'revert', 'a')).ref!
  assert.equal(store.capture(identity, first.bodyHash, one), undefined)
  assert.deepEqual(store.capture(identity, first.bodyHash, three), three)
  assert.equal(store.hasEdits(identity), true)
  const other = { ...identity, messageId: 'ordered' }
  const seqStore = new SourceEventStore(db, { id: () => 'seq-event' })
  seqStore.observe({ ...first, identity: other })
  const next = seqStore.observe({
    kind: 'edit',
    identity: other,
    bodyHash: 'c'.repeat(64),
    occurredAt: null,
    editId: 'seq10',
    order: { kind: 'platform_sequence', sequence: 10 },
  }).ref!
  assert.equal(seqStore.observe({ ...edit(next, 'cas'), identity: other }).status, 'unorderable')
  assert.equal(seqStore.current(next, other).eligible, true)
  db.close()
})

test('unenrolled read ports never initialize tables, including simulation reads', () => {
  const { db, store } = fixture()
  const ref = { eventId: 'missing', revision: 1, deletionEpoch: 0 }
  assert.equal(store.headMetadata(identity), undefined)
  assert.equal(store.registered(identity.sceneId, identity.messageId), undefined)
  assert.equal(store.hasEdits(identity), false)
  assert.equal(store.capture(identity, first.bodyHash, ref), undefined)
  assert.equal(store.current(ref, identity).reason, 'missing')
  assert.equal(store.historical(ref, identity).reason, 'missing')
  assert.deepEqual(
    db.prepare("SELECT name FROM sqlite_master WHERE name LIKE 'source_event_%'").all(),
    [],
  )
  const actual = store.observe(first).ref!
  assert.deepEqual(store.registered(identity.sceneId, identity.messageId)?.ref, actual)
  db.close()
})

test('new enrollment is bounded per scene and globally without evicting existing authority', () => {
  const db = new Database(':memory:')
  let ids = 0,
    writes = 0
  const store = new SourceEventStore(db, {
    id: () => `event-${++ids}`,
    maxHeadsPerScene: 1,
    maxHeadsTotal: 2,
    beforeWrite: () => {
      writes++
    },
  })
  const firstRef = store.observe(first).ref!
  assert.equal(
    store.observe({ ...first, identity: { ...identity, messageId: 'second' } }).status,
    'capacity',
  )
  assert.equal(writes, 1)
  assert.equal(store.observe(edit(firstRef)).status, 'edited')
  assert.equal(
    store.observe({ ...first, identity: { ...identity, sceneId: 'other' } }).status,
    'new',
  )
  assert.equal(
    store.observe({ ...first, identity: { ...identity, sceneId: 'third' } }).status,
    'capacity',
  )
  assert.equal(
    (db.prepare('SELECT count(*) n FROM source_event_heads').get() as { n: number }).n,
    2,
  )
  assert.equal(store.historical(firstRef, identity).eligible, true)
  store.revokeAll(identity.sceneId, identity.messageId)
  assert.equal(store.observe(first).status, 'recalled')
  assert.equal(
    store.observe({ ...first, identity: { ...identity, sceneId: 'third' } }).status,
    'new',
  )
  assert.equal(
    (db.prepare('SELECT count(*) n FROM source_event_tombstones').get() as { n: number }).n,
    1,
  )
  db.close()
})
