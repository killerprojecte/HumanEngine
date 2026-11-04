import test from 'node:test'
import assert from 'node:assert/strict'
import { createHash, randomUUID } from 'node:crypto'
import { mkdtempSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { RecoveryAuthority } from '../src/memory/recovery/ledger.ts'

test('head-only verification discards event results but validates fresh full-chain contents', () => {
  const directory = mkdtempSync(join(tmpdir(), 'he-head-allocation-'))
  const authority = new RecoveryAuthority(directory, randomUUID())
  try {
    authority.append({ kind: 'source', sceneId: 'synthetic', messageId: 'first' }, authority.head())
    authority.append(
      { kind: 'source', sceneId: 'synthetic', messageId: 'second' },
      authority.head(),
    )
    assert.equal(authority.entries().length, 2)
    assert.deepEqual(authority.entries(false), [])
    const head = authority.head()
    assert.equal(head.sequence, 2)
    // Modify an old row, leaving latest head untouched. A head-only shortcut must not hide it.
    authority.db.prepare('UPDATE revocations SET payload=? WHERE sequence=1').run('{}')
    assert.throws(() => authority.head(), /recovery_chain_invalid/)
  } finally {
    authority.close()
    rmSync(directory, { recursive: true, force: true })
  }
})

test('head-only verification still parses event schemas even with a correct fresh row hash', () => {
  const directory = mkdtempSync(join(tmpdir(), 'he-head-schema-'))
  const identity = randomUUID()
  const authority = new RecoveryAuthority(directory, identity)
  try {
    const previous = '0'.repeat(64)
    const payload = '{"kind":"unknown"}'
    const hash = createHash('sha256').update(`${identity}|1|${previous}|${payload}`).digest('hex')
    authority.db.prepare('INSERT INTO revocations VALUES(?,?,?,?)').run(1, previous, payload, hash)
    assert.throws(() => authority.entries(false), /recovery_unknown_revocation/)
    assert.throws(() => authority.head(), /recovery_unknown_revocation/)
  } finally {
    authority.close()
    rmSync(directory, { recursive: true, force: true })
  }
})
