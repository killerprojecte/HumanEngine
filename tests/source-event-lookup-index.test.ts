import assert from 'node:assert/strict'
import test from 'node:test'
import Database from 'better-sqlite3'
import { mkdtempSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { Store } from '../src/store.ts'
import { createRecoverySourceEventPort } from '../src/memory/recovery/source-events.ts'
import {
  SourceEventStore,
  ensureSourceEventLookupIndex,
} from '../src/memory/storage/event-revisions.ts'
import { sourceIdentityKey } from '../src/memory/domain/events.ts'

const identity = {
  sceneId: 'scene',
  adapterId: 'adapter',
  accountId: 'account',
  messageId: 'message',
  senderId: 'speaker',
}
const ingress = { kind: 'ingress' as const, identity, bodyHash: 'a'.repeat(64), occurredAt: null }
const indexRow = (db: Database.Database) =>
  db
    .prepare(
      "SELECT sql FROM sqlite_master WHERE type='index' AND name='idx_source_event_heads_scene_message'",
    )
    .get()

test('fresh source-event initialization adds nonunique index; absent/partial schemas stay untouched on startup', () => {
  const db = new Database(':memory:')
  try {
    ensureSourceEventLookupIndex(db)
    assert.equal(
      db.prepare("SELECT 1 FROM sqlite_master WHERE name LIKE 'source_event_%'").get(),
      undefined,
    )
    db.exec(
      'CREATE TABLE source_event_heads(identity_key TEXT PRIMARY KEY,event_id TEXT UNIQUE NOT NULL,scene_id TEXT NOT NULL,message_id TEXT NOT NULL,value TEXT NOT NULL)',
    )
    ensureSourceEventLookupIndex(db)
    assert.equal(indexRow(db), undefined)
    assert.throws(
      () => new SourceEventStore(db).registered('scene', 'message'),
      /source_event_corrupt/,
    )
    db.exec('DROP TABLE source_event_heads')
    new SourceEventStore(db).observe(ingress)
    assert.ok(indexRow(db))
    assert.equal(
      (db.pragma('index_list(source_event_heads)') as { name: string; unique: number }[]).find(
        (row) => row.name === 'idx_source_event_heads_scene_message',
      )!.unique,
      0,
    )
  } finally {
    db.close()
  }
})

test('writable Store startup upgrades old source-event schema without rewriting raw heads', () => {
  const root = mkdtempSync(join(tmpdir(), 'he-event-lookup-')),
    path = join(root, 'data', 'live.sqlite')
  let store: Store | undefined
  try {
    store = new Store(path)
    store.sourceEvents.observe(ingress)
    store.db.exec('DROP INDEX idx_source_event_heads_scene_message')
    const before = store.db.prepare('SELECT * FROM source_event_heads').all()
    store.close()
    store = new Store(path)
    assert.ok(indexRow(store.db))
    assert.deepEqual(store.db.prepare('SELECT * FROM source_event_heads').all(), before)
    assert.ok(store.sourceEvents.registered('scene', 'message'))
  } finally {
    store?.close()
    rmSync(root, { recursive: true, force: true })
  }
})

test('readonly lookups never install an index and duplicate/malformed matches remain corrupt', () => {
  const db = new Database(':memory:'),
    events = new SourceEventStore(db)
  try {
    events.observe(ingress)
    db.exec('DROP INDEX idx_source_event_heads_scene_message')
    db.pragma('query_only = ON')
    assert.ok(events.registered('scene', 'message'))
    assert.equal(indexRow(db), undefined)
    db.pragma('query_only = OFF')
    db.transaction(() => ensureSourceEventLookupIndex(db))()
    db.pragma('query_only = ON')
    assert.ok(events.registered('scene', 'message'))
    db.pragma('query_only = OFF')
    const row = db.prepare('SELECT value FROM source_event_heads').get() as { value: string }
    const head = JSON.parse(row.value)
    head.metadata.identity.senderId = 'other-speaker'
    head.metadata.ref.eventId = 'other-event'
    db.prepare('INSERT INTO source_event_heads VALUES(?,?,?,?,?)').run(
      sourceIdentityKey(head.metadata.identity),
      'other-event',
      'scene',
      'message',
      JSON.stringify(head),
    )
    assert.throws(() => events.registered('scene', 'message'), /source_event_corrupt/)
    db.prepare('DELETE FROM source_event_heads WHERE event_id=?').run('other-event')
    db.prepare("UPDATE source_event_heads SET value='{bad'").run()
    assert.throws(() => events.registered('scene', 'message'), /source_event_corrupt/)
  } finally {
    db.close()
  }
})

test('large-head lookup and per-scene count use the scalar index without losing matches', () => {
  const db = new Database(':memory:'),
    events = new SourceEventStore(db)
  try {
    events.observe(ingress)
    const original = JSON.parse(
      (db.prepare('SELECT value FROM source_event_heads').get() as { value: string }).value,
    )
    const insert = db.prepare('INSERT INTO source_event_heads VALUES(?,?,?,?,?)')
    db.transaction(() => {
      for (let i = 0; i < 10_000; i++) {
        const head = structuredClone(original),
          id = `other-${i}`
        head.metadata.identity.sceneId = 'other-scene'
        head.metadata.identity.messageId = id
        head.metadata.ref.eventId = id
        insert.run(
          sourceIdentityKey(head.metadata.identity),
          id,
          'other-scene',
          id,
          JSON.stringify(head),
        )
      }
    })()
    for (const [sql, args] of [
      [
        'SELECT value FROM source_event_heads WHERE scene_id=? AND message_id=?',
        ['scene', 'message'],
      ],
      ['SELECT count(*) n FROM source_event_heads WHERE scene_id=?', ['other-scene']],
    ] as const) {
      const plan = db.prepare(`EXPLAIN QUERY PLAN ${sql}`).all(...args) as { detail: string }[]
      assert.ok(
        plan.some(({ detail }) =>
          /SEARCH source_event_heads USING (?:COVERING )?INDEX idx_source_event_heads_scene_message/.test(
            detail,
          ),
        ),
        JSON.stringify(plan),
      )
    }
    assert.ok(events.registered('scene', 'message'))
    assert.equal(events.registered('scene', 'missing'), undefined)
    assert.ok(events.registered('other-scene', 'other-9999'))
    assert.equal(
      (
        db
          .prepare('SELECT count(*) n FROM source_event_heads WHERE scene_id=?')
          .get('other-scene') as { n: number }
      ).n,
      10_000,
    )
  } finally {
    db.close()
  }
})

test('access index remains compatible with strict recovery table namespace; unknown objects stay rejected', () => {
  const store = new Store(':memory:')
  try {
    store.sourceEvents.observe(ingress)
    const port = createRecoverySourceEventPort(store)
    assert.equal(port.assertSupported(), true)
    store.db.exec('CREATE INDEX source_event_unknown ON source_event_heads(message_id)')
    assert.throws(() => port.assertSupported(), /recovery_unknown_source_event_schema/)
  } finally {
    store.close()
  }
})

test('index migration rolls back without changing original head bytes', () => {
  const db = new Database(':memory:')
  try {
    new SourceEventStore(db).observe(ingress)
    db.exec('DROP INDEX idx_source_event_heads_scene_message')
    const before = db.prepare('SELECT * FROM source_event_heads').all()
    assert.throws(
      () =>
        db.transaction(() => {
          ensureSourceEventLookupIndex(db)
          throw Error('synthetic migration failure')
        })(),
      /synthetic migration failure/,
    )
    assert.equal(indexRow(db), undefined)
    assert.deepEqual(db.prepare('SELECT * FROM source_event_heads').all(), before)
  } finally {
    db.close()
  }
})
