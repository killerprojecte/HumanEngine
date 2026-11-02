import assert from 'node:assert/strict'
import test from 'node:test'
import { createHash } from 'node:crypto'
import { mkdtempSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import Database from 'better-sqlite3'
import { Store } from '../src/store.ts'
import type { RuntimeMessage } from '../src/runtime.ts'
import {
  RecoveryAuthority,
  metadata,
  revoke,
  validateSourceSnapshots,
} from '../src/memory/recovery/ledger.ts'

function fixture() {
  const root = mkdtempSync(join(tmpdir(), 'source-read-scope-'))
  const store = new Store(join(root, 'data', 'state.sqlite'))
  store.saveScene({
    id: 's',
    name: 's',
    kind: 'group',
    adapterId: 'fixture',
    accountId: 'bot',
    platformSceneId: 'group',
    participationEnabled: true,
    learningEnabled: false,
    proactiveEnabled: false,
  })
  store.db.transaction(() =>
    revoke(store.db, { kind: 'source', sceneId: 'other', messageId: 'synthetic' }),
  )()
  const authority = new RecoveryAuthority(metadata(store.db)!.authority)
  const messages: RuntimeMessage[] = Array.from({ length: 24 }, (_, i) => ({
    id: String(i),
    sceneId: 's',
    senderId: 'person',
    origin: 'live',
    status: 'available',
    sentAt: new Date().toISOString(),
    receivedAt: new Date().toISOString(),
    parts: [{ type: 'text', text: 'synthetic' }],
  }))
  return {
    store,
    authority,
    messages,
    close() {
      authority.close()
      store.close()
      rmSync(root, { recursive: true, force: true })
    },
  }
}
test('fixed snapshot batch verifies full chain bounded times and restores writable connections', () => {
  const f = fixture()
  const original = RecoveryAuthority.prototype.entries
  let scans = 0
  RecoveryAuthority.prototype.entries = function () {
    scans++
    return original.call(this)
  }
  try {
    assert.equal(validateSourceSnapshots(f.store, f.messages), true)
    assert.equal(scans, 3)
    assert.equal(f.store.db.inTransaction, false)
    assert.equal(f.store.db.pragma('query_only', { simple: true }), 0)
    f.store.saveScene({ ...f.store.scene('s')!, name: 'updated' })
    assert.equal(f.store.scene('s')!.name, 'updated')
  } finally {
    RecoveryAuthority.prototype.entries = original
    f.close()
  }
})
test('external writer is blocked during pinned batch and immediately detected after scope', () => {
  const f = fixture()
  const recalled = f.store.recalled.bind(f.store)
  let attempted = false
  f.authority.db.pragma('busy_timeout = 0')
  f.store.recalled = (scene, id) => {
    if (!attempted) {
      attempted = true
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
  try {
    assert.equal(validateSourceSnapshots(f.store, f.messages), true)
    assert.equal(attempted, true)
    f.authority.db.prepare('UPDATE revocations SET hash=? WHERE sequence=1').run('0'.repeat(64))
    assert.throws(() => validateSourceSnapshots(f.store, f.messages), /recovery_chain_invalid/)
    assert.equal(f.store.db.inTransaction, false)
    assert.equal(f.store.db.pragma('query_only', { simple: true }), 0)
  } finally {
    f.close()
  }
})
test('same-connection writes and durable revoke are denied inside source reads; exceptions restore state', () => {
  const f = fixture()
  const recalled = f.store.recalled.bind(f.store)
  let checked = false
  f.store.recalled = (scene, id) => {
    if (!checked) {
      checked = true
      assert.throws(
        () =>
          f.store.db
            .prepare('INSERT INTO recall_tombstones VALUES(?,?,?)')
            .run('s', '0', new Date().toISOString()),
        /readonly/,
      )
      assert.throws(
        () => revoke(f.store.db, { kind: 'source', sceneId: 's', messageId: '0' }),
        /recovery_read_scope_write_denied/,
      )
      throw new Error('synthetic_read_failure')
    }
    return recalled(scene, id)
  }
  try {
    const before = f.authority.head()
    assert.throws(() => validateSourceSnapshots(f.store, f.messages), /synthetic_read_failure/)
    assert.deepEqual(f.authority.head(), before)
    assert.equal(f.store.db.inTransaction, false)
    assert.equal(f.store.db.pragma('query_only', { simple: true }), 0)
    assert.equal(validateSourceSnapshots(f.store, f.messages), true)
    f.store.recall('s', '0')
    assert.equal(validateSourceSnapshots(f.store, f.messages), false)
  } finally {
    f.close()
  }
})
test('existing main transaction falls back to original checks without changing transaction ownership', () => {
  const f = fixture()
  try {
    f.store.db.transaction(() => {
      assert.equal(validateSourceSnapshots(f.store, f.messages), true)
      assert.equal(f.store.db.inTransaction, true)
      assert.equal(f.store.db.pragma('query_only', { simple: true }), 0)
      f.store.db
        .prepare('INSERT INTO recall_tombstones VALUES(?,?,?)')
        .run('s', '0', new Date().toISOString())
      assert.equal(validateSourceSnapshots(f.store, f.messages), false)
    })()
    assert.equal(f.store.recalled('s', '0'), true)
  } finally {
    f.close()
  }
})
test('external valid authority append after scope does not retain old permission', () => {
  const f = fixture()
  try {
    assert.equal(validateSourceSnapshots(f.store, f.messages), true)
    f.authority.append({ kind: 'source', sceneId: 's', messageId: '0' }, f.authority.head(), true)
    assert.throws(
      () => validateSourceSnapshots(f.store, f.messages),
      /recovery_authority_head_changed/,
    )
  } finally {
    f.close()
  }
})

test('the same batch fails if authority changes immediately after snapshot unlock', () => {
  const f = fixture()
  const pragma = f.store.db.pragma.bind(f.store.db)
  let changed = false
  f.store.db.pragma = ((source: string, options?: unknown) => {
    const value = (pragma as any)(source, options)
    if (source === 'query_only = OFF' && !changed) {
      changed = true
      f.authority.db.prepare('UPDATE revocations SET hash=? WHERE sequence=1').run('0'.repeat(64))
    }
    return value
  }) as typeof f.store.db.pragma
  try {
    assert.throws(() => validateSourceSnapshots(f.store, f.messages), /recovery_chain_invalid/)
    assert.equal(changed, true)
    assert.equal(f.store.db.inTransaction, false)
    assert.equal(pragma('query_only', { simple: true }), 0)
  } finally {
    f.store.db.pragma = pragma as typeof f.store.db.pragma
    f.close()
  }
})

test('preexisting authority writer contention fails within configured busy timeout without a leaked scope', () => {
  const f = fixture()
  const bound = (f.store as unknown as { recoveryAuthority: RecoveryAuthority }).recoveryAuthority
  bound.db.pragma('busy_timeout = 10')
  try {
    f.authority.db.exec('BEGIN EXCLUSIVE')
    assert.throws(() => validateSourceSnapshots(f.store, f.messages), /locked/)
    f.authority.db.exec('ROLLBACK')
    assert.equal(f.store.db.inTransaction, false)
    assert.equal(bound.db.inTransaction, false)
    assert.equal(f.store.db.pragma('query_only', { simple: true }), 0)
    assert.equal(bound.db.pragma('query_only', { simple: true }), 0)
    assert.equal(validateSourceSnapshots(f.store, f.messages), true)
  } finally {
    if (f.authority.db.inTransaction) f.authority.db.exec('ROLLBACK')
    f.close()
  }
})

test('exit invariants reject and roll back a main-only write after the final nested gate', () => {
  const f = fixture()
  const events = f.store.sourceEvents
  const hasEdits = events.hasEdits.bind(events)
  let calls = 0
  events.hasEdits = (identity) => {
    const result = hasEdits(identity)
    if (++calls === f.messages.length) {
      // Fault injection: production source reads never toggle query_only or write.
      f.store.db.pragma('query_only = OFF')
      f.store.db.prepare('INSERT INTO config(key,value) VALUES(?,?)').run('scope-canary', 'true')
      f.store.db.pragma('query_only = ON')
    }
    return result
  }
  try {
    assert.throws(() => validateSourceSnapshots(f.store, f.messages), /recovery_read_scope_changed/)
    assert.equal(f.store.get('scope-canary', false), false)
    assert.equal(f.store.db.inTransaction, false)
    assert.equal(f.store.db.pragma('query_only', { simple: true }), 0)
  } finally {
    events.hasEdits = hasEdits
    f.close()
  }
})

test('versioned snapshot batches preserve body-hash matching and recall rejection', () => {
  const f = fixture()
  try {
    const message = f.messages[0]!
    const bodyHash = createHash('sha256')
      .update(JSON.stringify([message.sentAt, message.replyToId ?? null, message.parts]))
      .digest('hex')
    const observed = f.store.sourceEvents.observe({
      kind: 'ingress',
      identity: {
        sceneId: 's',
        adapterId: 'fixture',
        accountId: 'bot',
        messageId: message.id,
        senderId: 'person',
      },
      bodyHash,
      occurredAt: message.sentAt,
    })
    assert.equal(observed.status, 'new')
    const versioned = { ...message, sourceEventRef: observed.ref! }
    assert.equal(validateSourceSnapshots(f.store, [versioned]), true)
    assert.equal(
      validateSourceSnapshots(f.store, [
        { ...versioned, parts: [{ type: 'text', text: 'changed' }] },
      ]),
      false,
    )
    assert.equal(validateSourceSnapshots(f.store, [message]), false)
    f.store.recall('s', message.id)
    assert.equal(validateSourceSnapshots(f.store, [versioned]), false)
  } finally {
    f.close()
  }
})
