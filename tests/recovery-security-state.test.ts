import test from 'node:test'
import assert from 'node:assert/strict'
import { mkdtempSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import Database from 'better-sqlite3'
import { Store } from '../src/store.ts'
import { metadata, RecoveryAuthority } from '../src/memory/recovery/ledger.ts'
import { advanceSecurityState, assertSecurityState } from '../src/memory/recovery/security-state.ts'

function fixture() {
  const root = mkdtempSync(join(tmpdir(), 'he-security-revision-'))
  const store = new Store(join(root, 'live', 'humanengine.sqlite'))
  const authority = new RecoveryAuthority(metadata(store.db)!.authority)
  return {
    store,
    authority,
    close() {
      store.close()
      authority.close()
      rmSync(root, { recursive: true, force: true })
    },
  }
}

test('credential writes journal only opaque revisions; preserve and absent clear do not advance', () => {
  const f = fixture()
  try {
    const initial = f.authority.head().sequence
    f.store.secrets.apply('synthetic-provider', 'synthetic-name', { action: 'preserve' })
    f.store.secrets.apply('synthetic-provider', 'synthetic-name', { action: 'clear' })
    assert.equal(f.authority.head().sequence, initial)
    f.store.secrets.apply('synthetic-provider', 'synthetic-name', {
      action: 'set',
      value: 'synthetic-secret-one',
    })
    const first = f.authority.entries().at(-1)!.revocation
    assert.equal(first.kind, 'security_state')
    if (first.kind !== 'security_state') throw new Error('wrong event')
    assert.deepEqual(Object.keys(first).sort(), ['kind', 'revision'])
    assert.match(first.revision, /^[a-f0-9-]{36}$/)
    assertSecurityState(f.store.db, first)
    f.store.secrets.apply('synthetic-provider', 'synthetic-name', {
      action: 'set',
      value: 'synthetic-secret-two',
    })
    assert.throws(
      () => assertSecurityState(f.store.db, first),
      /recovery_security_state_unrecoverable/,
    )
    f.store.secrets.apply('synthetic-provider', 'synthetic-name', { action: 'clear' })
    assert.equal(f.store.secrets.has('synthetic-provider', 'synthetic-name'), false)
    const events = JSON.stringify(f.authority.entries())
    for (const forbidden of ['synthetic-secret', 'synthetic-provider', 'synthetic-name'])
      assert.equal(events.includes(forbidden), false)
  } finally {
    f.close()
  }
})

test('external security intent survives outer rollback and prevents stale local material being accepted', () => {
  const f = fixture()
  try {
    f.store.secrets.apply('model', 'synthetic', { action: 'set', value: 'synthetic-before' })
    const before = f.store.db.prepare('SELECT revision FROM recovery_security_state').get()
    assert.throws(
      () =>
        f.store.db.transaction(() => {
          f.store.secrets.apply('model', 'synthetic', { action: 'set', value: 'synthetic-after' })
          throw new Error('synthetic-rollback')
        })(),
      /synthetic-rollback/,
    )
    assert.deepEqual(
      f.store.db.prepare('SELECT revision FROM recovery_security_state').get(),
      before,
    )
    const latest = f.authority.entries().at(-1)!.revocation
    if (latest.kind !== 'security_state') throw new Error('wrong event')
    assert.throws(
      () => assertSecurityState(f.store.db, latest),
      /recovery_security_state_unrecoverable/,
    )
    assert.throws(() => f.store.assertRecoveryReady(), /recovery_authority_head_changed/)
  } finally {
    f.close()
  }
})

test('legacy absent marker is not fabricated; newer authority requires exact trusted material', () => {
  const db = new Database(':memory:')
  try {
    assertSecurityState(db, undefined)
    assert.throws(
      () => assertSecurityState(db, { kind: 'security_state', revision: 'newer' }),
      /recovery_security_state_unrecoverable/,
    )
    assert.equal(
      db.prepare("SELECT 1 FROM sqlite_master WHERE name='recovery_security_state'").get(),
      undefined,
    )
    assert.throws(() => advanceSecurityState(db), /recovery_transaction_required/)
    const revision = db.transaction(() => advanceSecurityState(db))()
    assertSecurityState(db, { kind: 'security_state', revision })
    assert.throws(() => assertSecurityState(db, undefined), /recovery_security_state_unrecoverable/)
  } finally {
    db.close()
  }
})

test('direct secret reads and mutations fail closed after external authority advancement', () => {
  const f = fixture()
  try {
    f.store.secrets.apply('model', 'synthetic', { action: 'set', value: 'synthetic-secret' })
    assert.equal(f.store.secrets.has('model', 'synthetic'), true)
    f.authority.append(
      { kind: 'source', sceneId: 'synthetic-scene', messageId: 'synthetic-recall' },
      f.authority.head(),
    )
    assert.throws(
      () => f.store.secrets.has('model', 'synthetic'),
      /recovery_authority_head_changed/,
    )
    assert.throws(
      () => f.store.secrets.get('model', 'synthetic'),
      /recovery_authority_head_changed/,
    )
    assert.throws(() => f.store.secrets.get('model', 'absent'), /recovery_authority_head_changed/)
    assert.throws(
      () => f.store.secrets.apply('model', 'synthetic', { action: 'preserve' }),
      /recovery_authority_head_changed/,
    )
    assert.throws(
      () => f.store.secrets.apply('model', 'synthetic', { action: 'clear' }),
      /recovery_authority_head_changed/,
    )
    assert.equal(
      f.store.db.prepare('SELECT COUNT(*) AS count FROM secrets').get() &&
        (f.store.db.prepare('SELECT COUNT(*) AS count FROM secrets').get() as { count: number })
          .count,
      1,
    )
  } finally {
    f.close()
  }
})

test('malformed security marker storage fails closed without rewriting the table', () => {
  const revision = '7a8a0944-2918-4bb3-9bf7-1decb4963441'
  const cases = [
    'CREATE TABLE recovery_security_state(id,revision,unexpected)',
    'CREATE TABLE recovery_security_state(id)',
    "CREATE VIEW recovery_security_state AS SELECT 1 AS id, 'bad' AS revision",
    "CREATE TABLE recovery_security_state(id,revision); INSERT INTO recovery_security_state VALUES(1,'bad')",
    `CREATE TABLE recovery_security_state(id,revision); INSERT INTO recovery_security_state VALUES(2,'${revision}')`,
    `CREATE TABLE recovery_security_state(id,revision); INSERT INTO recovery_security_state VALUES(1,'${revision}'),(1,'${revision}')`,
    'CREATE TABLE recovery_security_state(id,revision); INSERT INTO recovery_security_state VALUES(1,NULL)',
  ]
  for (const sql of cases) {
    const db = new Database(':memory:')
    try {
      db.exec(sql)
      const before = db
        .prepare("SELECT sql FROM sqlite_master WHERE name='recovery_security_state'")
        .get()
      assert.throws(() => assertSecurityState(db, undefined), /recovery_security_state_invalid/)
      assert.deepEqual(
        db.prepare("SELECT sql FROM sqlite_master WHERE name='recovery_security_state'").get(),
        before,
      )
    } finally {
      db.close()
    }
  }
  const empty = new Database(':memory:')
  try {
    empty.exec('CREATE TABLE recovery_security_state(id,revision)')
    assertSecurityState(empty, undefined)
    assert.throws(
      () => assertSecurityState(empty, { kind: 'security_state', revision }),
      /recovery_security_state_unrecoverable/,
    )
  } finally {
    empty.close()
  }
})
