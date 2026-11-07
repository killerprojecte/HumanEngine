import assert from 'node:assert/strict'
import test from 'node:test'
import { createHash } from 'node:crypto'
import { mkdtempSync, rmSync } from 'node:fs'
import { join } from 'node:path'
import { tmpdir } from 'node:os'
import {
  RecoveryAuthority,
  beginDurableRevocation,
  commitDurableRevocation,
  metadata,
  revoke,
} from '../src/memory/recovery/ledger.ts'
import { ConfigReadConnection } from '../src/server/config-read.ts'
import { Store } from '../src/store.ts'

function hashes(run: () => unknown) {
  const prototype = Object.getPrototypeOf(createHash('sha256'))
  const original = prototype.update
  let count = 0
  prototype.update = function (...args: unknown[]) {
    count++
    return original.apply(this, args)
  }
  try {
    run()
    return count
  } finally {
    prototype.update = original
  }
}
function authorityFixture() {
  const root = mkdtempSync(join(tmpdir(), 'he-readonly-integrity-'))
  const writer = new RecoveryAuthority(join(root, 'authority'), 'synthetic-instance')
  let head = writer.head()
  for (let i = 0; i < 4; i++)
    head = writer.append({ kind: 'source', sceneId: 's', messageId: `m${i}` }, head)
  const reader = new RecoveryAuthority(writer.path, undefined, true)
  const append = () =>
    writer.append({ kind: 'source', sceneId: 's', messageId: 'external' }, writer.head(), true)
  return {
    writer,
    reader,
    append,
    close() {
      for (const connection of [reader, writer]) {
        if (connection.db.inTransaction) connection.db.exec('ROLLBACK')
        connection.close()
      }
      rmSync(root, { recursive: true, force: true })
    },
  }
}

test('readonly caller snapshot caches integrity once and remains valid after read rollback', () => {
  const f = authorityFixture()
  try {
    const head = f.append()
    assert.equal(f.reader.db.readonly, true)
    f.reader.db.exec('BEGIN')
    assert.equal(
      hashes(() => assert.deepEqual(f.reader.head(), head)),
      head.sequence,
    )
    assert.equal(
      hashes(() => assert.deepEqual(f.reader.head(), head)),
      0,
    )
    f.reader.db.exec('ROLLBACK')
    assert.equal(
      hashes(() => assert.deepEqual(f.reader.head(), head)),
      0,
    )
    f.reader.db.exec('BEGIN')
    assert.deepEqual(f.reader.head(), head)
    f.reader.db.exec('COMMIT')
    assert.equal(
      hashes(() => assert.deepEqual(f.reader.head(), head)),
      0,
    )
  } finally {
    f.close()
  }
})

test('promoted readonly integrity detects a foreign old-row edit with unchanged final head', () => {
  const f = authorityFixture()
  try {
    const head = f.append()
    f.reader.db.exec('BEGIN')
    assert.deepEqual(f.reader.head(), head)
    f.reader.db.exec('ROLLBACK')
    f.writer.db
      .prepare('UPDATE main.revocations SET payload=? WHERE sequence=1')
      .run(JSON.stringify({ kind: 'source', sceneId: 's', messageId: 'tampered' }))
    assert.deepEqual(
      f.writer.db
        .prepare('SELECT sequence,hash FROM main.revocations ORDER BY sequence DESC LIMIT 1')
        .get(),
      { sequence: head.sequence, hash: head.hash },
    )
    assert.throws(() => f.reader.head(), /recovery_chain_invalid/)
    assert.throws(() => f.reader.head(), /recovery_chain_invalid/)
  } finally {
    f.close()
  }
})

test('readonly promotion still reaudits schema commits and rejects foreign identity mutation', () => {
  const f = authorityFixture()
  try {
    const head = f.append()
    f.reader.db.exec('BEGIN')
    assert.deepEqual(f.reader.head(), head)
    f.reader.db.exec('ROLLBACK')
    f.writer.db.exec('CREATE TABLE main.audit_metadata(value TEXT)')
    f.reader.db.exec('BEGIN')
    assert.equal(
      hashes(() => assert.deepEqual(f.reader.head(), head)),
      head.sequence,
    )
    assert.equal(
      hashes(() => assert.deepEqual(f.reader.head(), head)),
      0,
    )
    f.reader.db.exec('ROLLBACK')
    f.writer.db.prepare('UPDATE main.identity SET id=?').run('foreign-instance')
    assert.throws(() => f.reader.head(), /recovery_chain_invalid/)
  } finally {
    f.close()
  }
})

test('query_only on a writable authority does not qualify caller snapshots for readonly promotion', () => {
  const f = authorityFixture()
  const writable = new RecoveryAuthority(f.writer.path)
  try {
    const head = f.append()
    assert.equal(writable.db.readonly, false)
    writable.db.pragma('query_only = ON')
    writable.db.exec('BEGIN')
    assert.equal(
      hashes(() => assert.deepEqual(writable.head(), head)),
      head.sequence,
    )
    assert.equal(
      hashes(() => assert.deepEqual(writable.head(), head)),
      head.sequence,
    )
    writable.db.exec('ROLLBACK')
    writable.db.pragma('query_only = OFF')
    assert.equal(
      hashes(() => assert.deepEqual(writable.head(), head)),
      head.sequence,
    )
    assert.equal(
      hashes(() => assert.deepEqual(writable.head(), head)),
      0,
    )
  } finally {
    if (writable.db.inTransaction) writable.db.exec('ROLLBACK')
    writable.close()
    f.close()
  }
})

async function withStore(run: (store: Store) => void) {
  const root = mkdtempSync(join(tmpdir(), 'he-readonly-publication-'))
  const store = new Store(join(root, 'data', 'live.sqlite'))
  try {
    run(store)
  } finally {
    await store.closeLogs()
    store.close()
    rmSync(root, { recursive: true, force: true })
  }
}

function verify(reader: ConfigReadConnection, ticket: number) {
  const lease = reader.beginPublication(ticket)
  try {
    return reader.verifyPublication({ ...lease, path: '/api/settings' })
  } finally {
    reader.abandonPublication(ticket)
  }
}

test('startup binds a fresh coherent main snapshot after a normal commit following constructor audit', async () => {
  await withStore((store) => {
    const original = RecoveryAuthority.prototype.entries
    let armed = true
    let reader: ConfigReadConnection | undefined
    RecoveryAuthority.prototype.entries = function (collect = true) {
      const result = original.call(this, collect)
      if (this.db.readonly && armed) {
        armed = false
        assert.equal(this.db.inTransaction, false)
        // The constructor's audit has ended; a real normal transaction commits
        // before its final readonly binding comparison. No direct SQL tampering.
        store.recall('synthetic', 'startup-write')
      }
      return result
    }
    try {
      reader = new ConfigReadConnection(store.db.name)
      assert.equal(armed, false)
      assert.deepEqual(verify(reader, 1).observedHead, metadata(store.db)!.head)
    } finally {
      RecoveryAuthority.prototype.entries = original
      reader?.close()
    }
  })
})

test('a durable async intent fails closed until exact main ACK completes', async () => {
  await withStore((store) => {
    const reader = new ConfigReadConnection(store.db.name)
    try {
      const ticket = beginDurableRevocation(store.db, {
        kind: 'source',
        sceneId: 'synthetic',
        messageId: 'pending-async',
      })
      assert(ticket)
      assert.throws(() => verify(reader, 1), /recovery_authority_head_changed/)
      commitDurableRevocation(store.db, ticket, () => {})
      assert.deepEqual(verify(reader, 2).observedHead, metadata(store.db)!.head)
    } finally {
      reader.close()
    }
  })
})

test('a multi-event main transaction is not waited on while its first durable authority event is pinned', async () => {
  await withStore((store) => {
    const reader = new ConfigReadConnection(store.db.name)
    try {
      store.db
        .transaction(() => {
          revoke(store.db, { kind: 'source', sceneId: 'synthetic', messageId: 'first' })
          assert.throws(() => verify(reader, 1), /recovery_authority_head_changed/)
          // Failure must release the authority read lock so the same normal main
          // transaction can append its second durable effect and commit.
          revoke(store.db, { kind: 'source', sceneId: 'synthetic', messageId: 'second' })
        })
        .immediate()
      assert.deepEqual(verify(reader, 2).observedHead, metadata(store.db)!.head)
    } finally {
      reader.close()
    }
  })
})
