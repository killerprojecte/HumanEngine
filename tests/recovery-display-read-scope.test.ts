import assert from 'node:assert/strict'
import test from 'node:test'
import { mkdtempSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { Store } from '../src/store.ts'
import { Runtime, type RuntimeMessage } from '../src/runtime.ts'
import {
  metadata,
  projectDisplayMessages,
  RecoveryAuthority,
  revoke,
} from '../src/memory/recovery/ledger.ts'

function fixture() {
  const root = mkdtempSync(join(tmpdir(), 'display-read-'))
  const store = new Store(join(root, 'data', 'state.sqlite'))
  const scene = {
    id:
      'ob:s:' +
      Buffer.from(JSON.stringify(['fixture', '123', 'group', '456'])).toString('base64url'),
    adapterId: 'fixture',
    accountId: '123',
    kind: 'group' as const,
    platformSceneId: '456',
  }
  store.saveScene({
    ...scene,
    name: 'synthetic',
    participationEnabled: false,
    learningEnabled: false,
    proactiveEnabled: false,
  })
  store.db.transaction(() =>
    revoke(store.db, { kind: 'source', sceneId: 'other', messageId: 'synthetic' }),
  )()
  const messages: RuntimeMessage[] = Array.from({ length: 30 }, (_, i) => ({
    id: 'm' + i,
    sceneId: scene.id,
    senderId: 'user',
    sentAt: new Date().toISOString(),
    receivedAt: new Date().toISOString(),
    origin: 'live',
    status: 'available',
    parts: [{ type: 'text', text: 'synthetic' }],
  }))
  const authority = new RecoveryAuthority(metadata(store.db)!.authority)
  return {
    store,
    scene,
    messages,
    authority,
    close() {
      authority.close()
      store.close()
      rmSync(root, { recursive: true, force: true })
    },
  }
}

test('real runtime preview and local history bound chain scans without model calls', async () => {
  const f = fixture(),
    runtime = new Runtime(f.store, () => {
      throw new Error('model forbidden')
    }),
    original = RecoveryAuthority.prototype.entries
  try {
    for (const message of f.messages)
      runtime.ingest(message as Parameters<Runtime['ingest']>[0], f.scene, false)
    let scans = 0
    RecoveryAuthority.prototype.entries = function (collect) {
      scans++
      return original.call(this, collect)
    }
    assert.equal(runtime.preview(f.scene.id).length, 30)
    assert.equal(scans, 4)
    scans = 0
    const history = await runtime.previewHistory(f.scene.id, 'local')
    assert.equal(history.items.length, 30)
    assert.equal(
      history.items.every((m) => m.origin === 'history'),
      true,
    )
    assert.equal(scans, 4)
  } finally {
    RecoveryAuthority.prototype.entries = original
    runtime.dispose()
    f.close()
  }
})

test('display reads use fresh identity and recall data, preserve simulation and copy nested input', () => {
  const f = fixture()
  try {
    const key = `identity:${f.scene.id}:user`
    f.store.set(key, { senderDisplayName: 'first' })
    const first = projectDisplayMessages(f.store, f.scene.id, f.messages, [])
    assert.equal(first[0]!.senderDisplayName, 'first')
    first[0]!.parts.length = 0
    assert.equal(f.messages[0]!.parts.length, 1)
    f.store.set(key, { senderDisplayName: 'second' })
    f.store.recall(f.scene.id, 'm0')
    const second = projectDisplayMessages(f.store, f.scene.id, f.messages, ['m1'])
    assert.equal(second[0]!.status, 'recalled')
    assert.deepEqual(second[0]!.parts, [])
    assert.equal(second[1]!.status, 'recalled')
    assert.equal(second[2]!.senderDisplayName, 'second')
    const sim = { ...f.messages[2]!, origin: 'simulation' as const }
    assert.equal(
      projectDisplayMessages(f.store, f.scene.id, [sim], [])[0]!.senderDisplayName,
      undefined,
    )
    assert.equal(projectDisplayMessages(f.store, f.scene.id, [sim], ['m2'])[0]!.status, 'recalled')
  } finally {
    f.close()
  }
})

test('display snapshots deny writes, detect subsequent tampering and restore state after exceptions', () => {
  const f = fixture(),
    recalled = f.store.recalled.bind(f.store)
  let tried = false
  f.authority.db.pragma('busy_timeout = 0')
  try {
    f.store.recalled = (scene, id) => {
      if (!tried) {
        tried = true
        assert.throws(
          () =>
            f.store.db
              .prepare('INSERT INTO recall_tombstones VALUES(?,?,?)')
              .run(scene, id, new Date().toISOString()),
          /readonly/,
        )
        assert.throws(
          () =>
            f.authority.db
              .prepare('UPDATE revocations SET hash=? WHERE sequence=1')
              .run('0'.repeat(64)),
          /locked/,
        )
      }
      return recalled(scene, id)
    }
    assert.equal(projectDisplayMessages(f.store, f.scene.id, f.messages, []).length, 30)
    assert.equal(tried, true)
    f.store.recalled = () => {
      throw new Error('synthetic')
    }
    assert.throws(() => projectDisplayMessages(f.store, f.scene.id, f.messages, []), /synthetic/)
    assert.equal(f.store.db.inTransaction, false)
    assert.equal(f.store.db.pragma('query_only', { simple: true }), 0)
    f.store.recalled = recalled
    f.authority.db.prepare('UPDATE revocations SET hash=? WHERE sequence=1').run('0'.repeat(64))
    assert.throws(
      () => projectDisplayMessages(f.store, f.scene.id, f.messages, []),
      /recovery_chain_invalid/,
    )
  } finally {
    f.close()
  }
})

test('display inputs materialize before snapshots and nested fallback sees fresh tombstones', () => {
  const f = fixture()
  try {
    const message = { ...f.messages[0]! }
    Object.defineProperty(message, 'id', {
      enumerable: true,
      get() {
        f.store.recall(f.scene.id, 'm0')
        return 'm0'
      },
    })
    assert.equal(projectDisplayMessages(f.store, f.scene.id, [message], [])[0]!.status, 'recalled')
    f.store.db.transaction(() => {
      assert.equal(
        projectDisplayMessages(f.store, f.scene.id, [f.messages[1]!], [])[0]!.status,
        'available',
      )
      f.store.db
        .prepare('INSERT INTO recall_tombstones VALUES(?,?,?)')
        .run(f.scene.id, 'm1', new Date().toISOString())
      assert.equal(
        projectDisplayMessages(f.store, f.scene.id, [f.messages[1]!], [])[0]!.status,
        'recalled',
      )
      assert.equal(f.store.db.inTransaction, true)
    })()
  } finally {
    f.close()
  }
})
