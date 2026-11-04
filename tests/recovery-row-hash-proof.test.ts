import assert from 'node:assert/strict'
import test from 'node:test'
import { createHash } from 'node:crypto'
import { mkdtempSync, rmSync, readFileSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { RecoveryAuthority } from '../src/memory/recovery/ledger.ts'

const hash = (value: string) => createHash('sha256').update(value).digest('hex')
const payload = JSON.stringify({ kind: 'fingerprint', value: 'a'.repeat(64) })
function fixture(count = 1, body = payload) {
  const root = mkdtempSync(join(tmpdir(), 'row-hash-proof-'))
  const path = join(root, 'authority')
  const authority = new RecoveryAuthority(path, 'fixture')
  let previous = '0'.repeat(64)
  authority.db.transaction(() => {
    const insert = authority.db.prepare('INSERT INTO revocations VALUES(?,?,?,?)')
    for (let sequence = 1; sequence <= count; sequence++) {
      const next = hash(`fixture|${sequence}|${previous}|${body}`)
      insert.run(sequence, previous, body, next)
      previous = next
    }
  })()
  return {
    authority,
    path,
    close() {
      if (authority.db.open) authority.close()
      rmSync(root, { recursive: true, force: true })
    },
  }
}
function hashes(action: () => unknown) {
  const prototype = Object.getPrototypeOf(createHash('sha256'))
  const original = prototype.update
  let calls = 0
  prototype.update = function (...args: unknown[]) {
    calls++
    return original.apply(this, args)
  }
  try {
    action()
    return calls
  } finally {
    prototype.update = original
  }
}

test('proof reuse skips SHA only; every call rereads rows and produces independent parsed events', () => {
  const f = fixture()
  try {
    assert.equal(
      hashes(() => f.authority.head()),
      1,
    )
    assert.equal(
      hashes(() => f.authority.head()),
      0,
    )
    const first = f.authority.entries()
    ;(first[0]!.revocation as { value: string }).value = 'poison'
    assert.equal((f.authority.entries()[0]!.revocation as { value: string }).value, 'a'.repeat(64))
    f.authority.db
      .prepare('UPDATE revocations SET payload=?')
      .run(payload.replace('a'.repeat(64), 'b'.repeat(64)))
    assert.throws(() => f.authority.head(), /recovery_chain_invalid/)
  } finally {
    f.close()
  }
})

for (const [field, value] of [
  ['sequence', 2],
  ['previous_hash', 'b'.repeat(64)],
  ['payload', '{}'],
  ['hash', 'b'.repeat(64)],
] as const)
  test(`fresh ${field} mutation never inherits another exact row proof`, () => {
    const f = fixture()
    try {
      f.authority.head()
      f.authority.db.prepare(`UPDATE revocations SET ${field}=?`).run(value)
      assert.throws(() => f.authority.head(), /recovery_chain_invalid/)
    } finally {
      f.close()
    }
  })

test('identity changes, deletion and ordering are evaluated on every traversal', () => {
  const f = fixture(2)
  try {
    f.authority.head()
    f.authority.db.prepare('UPDATE identity SET id=?').run('changed')
    assert.throws(() => f.authority.head(), /recovery_chain_invalid/)
    f.authority.db.prepare('UPDATE identity SET id=?').run('fixture')
    f.authority.db.prepare('DELETE FROM revocations WHERE sequence=2').run()
    assert.equal(f.authority.head().sequence, 1, 'fresh prefix head is not memoized')
    f.authority.db.prepare('UPDATE revocations SET sequence=2').run()
    assert.throws(() => f.authority.head(), /recovery_chain_invalid/)
  } finally {
    f.close()
  }
})

test('malformed events with correct SHA never enter the proof cache and schema checks remain fresh', () => {
  const f = fixture(1, '{"kind":"unknown"}')
  try {
    for (let i = 0; i < 2; i++) {
      assert.equal(
        hashes(() => assert.throws(() => f.authority.head(), /recovery_unknown_revocation/)),
        1,
      )
    }
  } finally {
    f.close()
  }
})

test('rollback and reopening cannot retain a mutable or connection-wide permission', () => {
  const f = fixture()
  try {
    f.authority.head()
    f.authority.db.exec('BEGIN')
    f.authority.db.prepare('UPDATE revocations SET hash=?').run('b'.repeat(64))
    assert.throws(() => f.authority.head(), /recovery_chain_invalid/)
    f.authority.db.exec('ROLLBACK')
    assert.equal(f.authority.head().sequence, 1)
    f.authority.close()
    let reopened!: RecoveryAuthority
    assert.equal(
      hashes(() => {
        reopened = new RecoveryAuthority(f.path)
      }),
      1,
    )
    reopened.close()
  } finally {
    f.close()
  }
})

test('same-token raw-file corruption still fails cryptographic validation after page cache release', () => {
  const f = fixture()
  const a = f.authority
  const token = () => [
    a.db.pragma('data_version', { simple: true }),
    a.db.prepare('SELECT total_changes() n').get(),
    a.db.pragma('schema_version', { simple: true }),
  ]
  try {
    a.head()
    const before = token()
    const file = join(f.path, 'authority.sqlite')
    const bytes = readFileSync(file),
      at = bytes.indexOf(Buffer.from(payload))
    assert(at >= 0)
    Buffer.from(payload.replace('a'.repeat(64), 'b'.repeat(64))).copy(bytes, at)
    writeFileSync(file, bytes)
    a.db.pragma('shrink_memory')
    assert.deepEqual(token(), before)
    assert.throws(() => a.head(), /recovery_chain_invalid/)
  } finally {
    f.close()
  }
})

test('entry cap retains only a bounded prefix; uncached tail is always hashed', () => {
  const f = fixture(4097)
  try {
    assert.equal(
      hashes(() => f.authority.head()),
      4097,
    )
    assert.equal(
      hashes(() => f.authority.head()),
      1,
    )
  } finally {
    f.close()
  }
})

test('oversized valid payloads bypass admission without truncating their exact bytes', () => {
  const f = fixture(1, payload + ' '.repeat(70000))
  try {
    assert.equal(
      hashes(() => f.authority.head()),
      1,
    )
    assert.equal(
      hashes(() => f.authority.head()),
      1,
    )
  } finally {
    f.close()
  }
})

test('byte cap stops admission before entry cap for larger valid rows', () => {
  const f = fixture(300, payload + ' '.repeat(15000))
  try {
    assert.equal(
      hashes(() => f.authority.head()),
      300,
    )
    const misses = hashes(() => f.authority.head())
    assert(misses > 0 && misses < 300)
    assert.equal(
      hashes(() => f.authority.head()),
      misses,
    )
  } finally {
    f.close()
  }
})

test('same-sequence valid replacement and rollback require each exact proof again', () => {
  const f = fixture()
  const a = f.authority
  const alternate = JSON.stringify({ kind: 'fingerprint', value: 'b'.repeat(64) })
  const originalHash = hash(`fixture|1|${'0'.repeat(64)}|${payload}`)
  const alternateHash = hash(`fixture|1|${'0'.repeat(64)}|${alternate}`)
  try {
    a.head()
    a.db.exec('BEGIN')
    a.db
      .prepare('UPDATE revocations SET payload=?,hash=? WHERE sequence=1')
      .run(alternate, alternateHash)
    assert.equal(
      hashes(() => a.head()),
      1,
    )
    assert.equal(
      hashes(() => a.head()),
      0,
    )
    a.db.exec('ROLLBACK')
    assert.equal(
      hashes(() => assert.equal(a.head().hash, originalHash)),
      1,
    )
    assert.equal(
      hashes(() => a.head()),
      0,
    )
    a.db.prepare('UPDATE revocations SET payload=? WHERE sequence=1').run(alternate)
    assert.throws(() => a.head(), /recovery_chain_invalid/)
  } finally {
    if (a.db.inTransaction) a.db.exec('ROLLBACK')
    f.close()
  }
})

test('repeated same-sequence replacements conservatively exhaust the byte admission budget', () => {
  const f = fixture()
  const a = f.authority
  try {
    a.head()
    for (let i = 0; i < 320; i++) {
      const body =
        JSON.stringify({ kind: 'source', sceneId: 's', messageId: `m-${i}` }) + ' '.repeat(15000)
      const digest = hash(`fixture|1|${'0'.repeat(64)}|${body}`)
      a.db.prepare('UPDATE revocations SET payload=?,hash=? WHERE sequence=1').run(body, digest)
      a.head()
    }
    assert.equal(
      hashes(() => a.head()),
      1,
      'an unadmitted replacement must still be hashed',
    )
    assert.equal(
      hashes(() => a.head()),
      1,
      'a full budget never silently admits another proof',
    )
  } finally {
    f.close()
  }
})
