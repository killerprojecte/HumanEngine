import assert from 'node:assert/strict'
import test from 'node:test'
import { mkdtempSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import Database from 'better-sqlite3'
import { Store } from '../src/store.ts'
import { metadata, RecoveryAuthority } from '../src/memory/recovery/ledger.ts'
import { readRecoveryStatement } from '../src/memory/recovery/read-statements.ts'

test('fixed recovery handles reuse compilation but execute fresh rows, rollback and schema errors', () => {
  const db = new Database(':memory:')
  try {
    db.exec(
      "CREATE TABLE recovery_gate(id INTEGER PRIMARY KEY,value TEXT); INSERT INTO recovery_gate VALUES(1,'first')",
    )
    const prepare = db.prepare.bind(db)
    let compiled = 0
    db.prepare = ((sql: string) => {
      compiled++
      return prepare(sql)
    }) as typeof db.prepare
    assert.deepEqual(readRecoveryStatement(db, 'gateValue'), { value: 'first' })
    assert.deepEqual(readRecoveryStatement(db, 'gateValue'), { value: 'first' })
    assert.equal(compiled, 1)
    assert.throws(
      () =>
        db.transaction(() => {
          db.exec("UPDATE recovery_gate SET value='inside'")
          assert.deepEqual(readRecoveryStatement(db, 'gateValue'), { value: 'inside' })
          throw Error('rollback')
        })(),
      /rollback/,
    )
    assert.deepEqual(readRecoveryStatement(db, 'gateValue'), { value: 'first' })
    db.exec('ALTER TABLE recovery_gate RENAME COLUMN value TO broken')
    assert.throws(() => readRecoveryStatement(db, 'gateValue'), /no such column/)
    db.exec('ALTER TABLE recovery_gate RENAME COLUMN broken TO value')
    assert.deepEqual(readRecoveryStatement(db, 'gateValue'), { value: 'first' })
    assert.equal(compiled, 2)
    db.exec('DROP TABLE recovery_gate')
    assert.equal(readRecoveryStatement(db, 'gateExists'), undefined)
    db.exec(
      "CREATE TABLE recovery_gate(id INTEGER PRIMARY KEY,value TEXT); INSERT INTO recovery_gate VALUES(1,'new')",
    )
    assert.ok(readRecoveryStatement(db, 'gateExists'))
    assert.deepEqual(readRecoveryStatement(db, 'gateValue'), { value: 'new' })
  } finally {
    db.close()
  }
  assert.throws(() => readRecoveryStatement(db, 'gateValue'), /recovery_connection_closed/)
  const another = new Database(':memory:')
  try {
    assert.equal(readRecoveryStatement(another, 'gateExists'), undefined)
  } finally {
    another.close()
  }
})

test('warm real gate never hides an append from another authority connection or a malformed chain', () => {
  const root = mkdtempSync(join(tmpdir(), 'he-recovery-handles-'))
  const store = new Store(join(root, 'live', 'data.sqlite'))
  const authority = new RecoveryAuthority(metadata(store.db)!.authority)
  try {
    for (let i = 0; i < 5; i++) store.assertRecoveryReady()
    const previous = authority.head()
    authority.append({ kind: 'source', sceneId: 's', messageId: 'm' }, previous)
    assert.equal(authority.head().sequence, previous.sequence + 1)
    assert.throws(() => store.assertRecoveryReady(), /recovery_authority_head_changed/)
    authority.append({ kind: 'source', sceneId: 's', messageId: 'm2' }, authority.head())
    assert.equal(authority.head().sequence, previous.sequence + 2)
    // Already-warm chain SELECT is executed again and validation is not bypassed.
    authority.db
      .prepare('UPDATE revocations SET payload=? WHERE sequence=?')
      .run('{"kind":"unknown"}', previous.sequence + 1)
    assert.throws(() => authority.head(), /recovery_/)
  } finally {
    authority.db.close()
    store.close()
    rmSync(root, { recursive: true, force: true })
  }
})

test('close and reopen uses a new connection, never handles or rows from the old instance', () => {
  const root = mkdtempSync(join(tmpdir(), 'he-recovery-reopen-')),
    path = join(root, 'live', 'data.sqlite')
  let store = new Store(path)
  try {
    const original = metadata(store.db)!
    store.assertRecoveryReady()
    store.close()
    store = new Store(path)
    store.assertRecoveryReady()
    assert.deepEqual(metadata(store.db), original)
    const raw = JSON.parse(
      (store.db.prepare('SELECT value FROM recovery_gate WHERE id=1').get() as { value: string })
        .value,
    )
    store.db
      .prepare('UPDATE recovery_gate SET value=? WHERE id=1')
      .run(JSON.stringify({ ...raw, minimumReader: 99999 }))
    assert.throws(() => store.assertRecoveryReady(), /recovery_format_unsupported/)
    store.db.prepare('UPDATE recovery_gate SET value=? WHERE id=1').run(JSON.stringify(raw))
    store.assertRecoveryReady()
    store.db
      .prepare('UPDATE recovery_gate SET value=? WHERE id=1')
      .run(JSON.stringify({ ...raw, state: 'invalid' }))
    assert.throws(() => store.assertRecoveryReady(), /recovery_/)
  } finally {
    store.close()
    rmSync(root, { recursive: true, force: true })
  }
})
