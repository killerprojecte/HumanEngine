import assert from 'node:assert/strict'
import test from 'node:test'
import Database from 'better-sqlite3'
import { SourceEventStore } from '../src/memory/storage/event-revisions.ts'
import { Store } from '../src/store.ts'

const identity = { sceneId: 's', adapterId: 'a', accountId: 'b', messageId: 'm', senderId: 'p' }
const ingress = { kind: 'ingress' as const, identity, bodyHash: 'a'.repeat(64), occurredAt: null }

test('source reads retain fresh metadata, tombstones and authority callbacks with fixed preparations', () => {
  const db = new Database(':memory:')
  let checks = 0,
    mutate: (() => void) | undefined
  const source = new SourceEventStore(db, {
    assertReady: () => {
      checks++
      const callback = mutate
      mutate = undefined
      callback?.()
    },
  })
  try {
    const one = new SourceEventStore(db).observe(ingress).ref!
    const prepare = db.prepare.bind(db)
    let prepares = 0
    db.prepare = ((sql: string) => {
      prepares++
      return prepare(sql)
    }) as typeof db.prepare
    const read = () => {
      assert.equal(source.registered('s', 'm')?.ref.revision, 1)
      assert.equal(source.current(one, identity).eligible, true)
      assert.equal(source.hasEdits(identity), false)
    }
    checks = 0
    read()
    const firstChecks = checks
    assert.equal(prepares, 5)
    for (let i = 0; i < 20; i++) read()
    assert.equal(prepares, 5, 'no SQL compilation proportional to validation count')
    assert.equal(checks, firstChecks * 21, 'every existing authority callback still executes')
    const caller = source.registered('s', 'm')!
    caller.ref.revision = 999
    assert.equal(source.registered('s', 'm')?.ref.revision, 1)
    mutate = () => prepare('INSERT INTO source_event_tombstones VALUES(?,?)').run('s', 'm')
    assert.equal(source.current(one, identity).reason, 'recalled')
    prepare('DELETE FROM source_event_tombstones').run()
    const two = source.observe({
      kind: 'edit',
      identity,
      bodyHash: 'b'.repeat(64),
      occurredAt: null,
      editId: 'e',
      order: { kind: 'observed_cas', expectedBase: one },
    }).ref!
    assert.equal(source.current(one, identity).reason, 'revision_changed')
    assert.equal(source.current(two, identity).eligible, true)
    assert.equal(source.hasEdits(identity), true)
    prepare("UPDATE source_event_heads SET value='{broken'").run()
    assert.throws(() => source.registered('s', 'm'), /source_event_corrupt/)
  } finally {
    db.close()
  }
})

test('missing and partial schemas remain live after cached existence reads', () => {
  const db = new Database(':memory:'),
    source = new SourceEventStore(db)
  try {
    assert.equal(source.registered('s', 'm'), undefined)
    source.observe(ingress)
    assert.equal(source.registered('s', 'm')?.identity.senderId, 'p')
    db.exec('DROP TABLE source_event_edit_keys')
    assert.throws(() => source.registered('s', 'm'), /source_event_corrupt/)
    db.close()
    assert.throws(() => source.registered('s', 'm'), /not open/)
  } finally {
    if (db.open) db.close()
  }
})

test('Store source getters preserve fresh scene permissions and recall state', () => {
  const store = new Store(':memory:')
  try {
    const prepare = store.db.prepare.bind(store.db)
    const counts = new Map<string, number>()
    store.db.prepare = ((sql: string) => {
      counts.set(sql, (counts.get(sql) ?? 0) + 1)
      return prepare(sql)
    }) as typeof store.db.prepare
    store.saveScene({
      ...identity,
      id: 's',
      name: 'S',
      kind: 'group',
      platformSceneId: 's',
      participationEnabled: true,
      learningEnabled: true,
      proactiveEnabled: false,
    })
    for (let i = 0; i < 20; i++) {
      assert.equal(store.scene('s')?.participationEnabled, true)
      assert.equal(store.recalled('s', 'm'), false)
    }
    assert.equal(counts.get('SELECT value FROM scenes WHERE id=?'), 1)
    assert.equal(counts.get('SELECT 1 FROM recall_tombstones WHERE scene_id=? AND message_id=?'), 1)
    const scene = store.scene('s')!
    scene.participationEnabled = false
    assert.equal(store.scene('s')?.participationEnabled, true)
    store.saveScene({ ...scene, participationEnabled: false })
    assert.equal(store.scene('s')?.participationEnabled, false)
    store.recall('s', 'm')
    assert.equal(store.recalled('s', 'm'), true)
    assert.equal(store.recalled('other', 'm'), false)
  } finally {
    store.close()
  }
})
