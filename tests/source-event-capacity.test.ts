import assert from 'node:assert/strict'
import test from 'node:test'
import Database from 'better-sqlite3'
import { SourceEventStore } from '../src/memory/storage/event-revisions.ts'
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
test('default persistent authority continues beyond former scene and instance quotas without evicting old proofs', () => {
  let id = 0
  const { db, store } = fixture({ id: () => `long-lived-event-${++id}` })
  try {
    const original = store.observe(first).ref!
    const originalMetadata = store.historical(original, identity).metadata
    db.transaction(() => {
      for (let i = 1; i <= 20001; i++) {
        const next: TrustedSourceChange = {
          ...first,
          identity: {
            ...identity,
            sceneId: i <= 5001 ? identity.sceneId : `other-${Math.floor(i / 4000)}`,
            messageId: `long-lived-message-${i}`,
          },
        }
        assert.equal(store.observe(next).status, 'new')
      }
    })()
    assert.equal(
      (db.prepare('SELECT count(*) AS n FROM source_event_heads').get() as { n: number }).n,
      20002,
    )
    assert.deepEqual(store.historical(original, identity).metadata, originalMetadata)
    assert.equal(store.current(original, identity).eligible, true)
    assert.equal(store.observe(first).status, 'duplicate')
    const changed = store.observe(edit(original)).ref!
    assert.equal(store.current(original, identity).reason, 'revision_changed')
    assert.deepEqual(store.historical(original, identity).metadata, originalMetadata)
    assert.equal(store.current(changed, identity).eligible, true)
    assert.equal(store.observe(edit(original, 'stale-edit')).status, 'conflict')
    store.revokeAll(identity.sceneId, identity.messageId)
    assert.equal(store.current(changed, identity).reason, 'recalled')
    assert.equal(store.historical(original, identity).reason, 'recalled')
    assert.equal(store.observe(first).status, 'recalled')
  } finally {
    db.close()
  }
})

test('explicit source quotas remain independent and reject new enrollment without blocking edits or recall', () => {
  for (const limit of [{ maxHeadsPerScene: 1 }, { maxHeadsTotal: 1 }]) {
    let id = 0
    const { db, store } = fixture({ ...limit, id: () => `limited-${++id}` })
    try {
      const ref: SourceEventRef = store.observe(first).ref!
      assert.equal(
        store.observe({ ...first, identity: { ...identity, messageId: 'second' } }).status,
        'capacity',
      )
      assert.equal(
        store.observe({ ...first, identity: { ...identity, sceneId: 'other-scene' } }).status,
        'maxHeadsPerScene' in limit ? 'new' : 'capacity',
      )
      const edited: SourceEventRef = store.observe(edit(ref)).ref!
      assert.equal(store.current(edited, identity).eligible, true)
      store.revokeAll(identity.sceneId, identity.messageId)
      assert.equal(store.historical(ref, identity).reason, 'recalled')
      assert.equal(
        store.observe({ ...first, identity: { ...identity, messageId: 'second' } }).status,
        'new',
      )
    } finally {
      db.close()
    }
  }
})

test('unlimited default enrollment preserves failed-write rollback and never downgrades authority', () => {
  let failWrite = false,
    id = 0
  const { db, store } = fixture({
    id: () => `atomic-${++id}`,
    beforeWrite: () => {
      if (failWrite) throw new Error('synthetic_write_failure')
    },
  })
  try {
    const ref: SourceEventRef = store.observe(first).ref!
    failWrite = true
    const next: TrustedSourceChange = {
      ...first,
      identity: { ...identity, messageId: 'failed-new' },
    }
    assert.throws(() => store.observe(next), /synthetic_write_failure/)
    assert.equal(store.registered(next.identity.sceneId, next.identity.messageId), undefined)
    assert.equal(store.current(ref, identity).eligible, true)
    assert.throws(() => store.observe(edit(ref)), /synthetic_write_failure/)
    assert.equal(store.current(ref, identity).eligible, true)
    failWrite = false
    assert.equal(store.observe(next).status, 'new')
    assert.equal(store.observe(edit(ref)).status, 'edited')
  } finally {
    db.close()
  }
})

test('reopening previously capacity-limited storage resumes new enrollment without replay or authority replacement', () => {
  let id = 0
  const { db, store } = fixture({ maxHeadsPerScene: 1, id: () => `before-upgrade-${++id}` })
  try {
    const ref: SourceEventRef = store.observe(first).ref!
    const next: TrustedSourceChange = {
      ...first,
      identity: { ...identity, messageId: 'new-after-upgrade' },
    }
    assert.equal(store.observe(next).status, 'capacity')
    const oldHead = db
      .prepare('SELECT value FROM source_event_heads WHERE event_id=?')
      .get(ref.eventId)
    const reopened = new SourceEventStore(db, { id: () => `after-upgrade-${++id}` })
    assert.equal(
      db
        .prepare('SELECT 1 FROM source_event_heads WHERE message_id=?')
        .get(next.identity.messageId),
      undefined,
    )
    assert.equal(reopened.current(ref, identity).eligible, true)
    assert.equal(reopened.observe(next).status, 'new')
    assert.deepEqual(
      db.prepare('SELECT value FROM source_event_heads WHERE event_id=?').get(ref.eventId),
      oldHead,
    )
    assert.equal(reopened.observe(first).status, 'duplicate')
  } finally {
    db.close()
  }
})
