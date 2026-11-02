import assert from 'node:assert/strict'
import test from 'node:test'
import Database from 'better-sqlite3'
import { mkdtempSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { Store } from '../src/store.ts'
import { metadata, RecoveryAuthority } from '../src/memory/recovery/ledger.ts'
import { AssertionRepository } from '../src/memory/storage/assertion-repository.ts'

const recordSql = 'SELECT * FROM memories WHERE id=?'
const proofSql = 'SELECT value FROM memory_supports WHERE memory_id=?'
function fixture() {
  const db = new Database(':memory:')
  db.exec(
    'CREATE TABLE memories(id TEXT PRIMARY KEY,value TEXT); CREATE TABLE memory_supports(memory_id TEXT PRIMARY KEY,value TEXT)',
  )
  const originalPrepare = db.prepare.bind(db)
  const counts = new Map<string, number>()
  db.prepare = ((sql: string) => {
    counts.set(sql, (counts.get(sql) ?? 0) + 1)
    return originalPrepare(sql)
  }) as typeof db.prepare
  let checks = 0,
    blocked = false
  const repository = new AssertionRepository(
    {
      db,
      assertRecoveryReady: () => {
        checks++
        if (blocked) throw new Error('synthetic-recovery-block')
      },
    },
    { beforeWrite: () => {}, removeProjection: () => {} },
  )
  const put = (value: unknown) =>
    originalPrepare('INSERT OR REPLACE INTO memories VALUES (?,?)').run('a', JSON.stringify(value))
  put({ id: 'a', content: 'first', status: 'active' })
  originalPrepare('INSERT INTO memory_supports VALUES (?,?)').run(
    'a',
    '[{"parents":["first"],"sources":[]}]',
  )
  return {
    db,
    repository,
    counts,
    put,
    originalPrepare,
    checks: () => checks,
    block: () => {
      blocked = true
    },
  }
}

test('refresh-local handles reuse preparation only: reads, objects, recovery checks and mutations stay fresh', () => {
  const f = fixture()
  try {
    f.db.transaction(() =>
      f.repository.withSupportReadStatements(() => {
        const a = f.repository.get('a')!
        a.content = 'mutated caller object'
        const proof = f.repository.supports('a')
        proof[0]!.parents.push('mutated caller proof')
        assert.equal(f.repository.get('a')!.content, 'first')
        assert.deepEqual(f.repository.supports('a')[0]!.parents, ['first'])
        f.put({ id: 'a', content: 'second', status: 'active' })
        f.originalPrepare('UPDATE memory_supports SET value=? WHERE memory_id=?').run(
          '[{"parents":["second"],"sources":[]}]',
          'a',
        )
        assert.equal(f.repository.get('a')!.content, 'second')
        assert.deepEqual(f.repository.supports('a')[0]!.parents, ['second'])
      }),
    )()
    assert.equal(
      Reflect.get(f.repository, 'supportReads'),
      undefined,
      'successful scope releases handles',
    )
    assert.equal(f.counts.get(recordSql), 1)
    assert.equal(f.counts.get(proofSql), 1)
    assert.equal(f.checks(), 3)
    f.repository.get('a')
    f.repository.get('a')
    assert.equal(
      f.counts.get(recordSql),
      3,
      'outside refresh retains original preparation behavior',
    )
    f.repository.withSupportReadStatements(() => f.repository.get('a'))
    assert.equal(f.counts.get(recordSql), 4, 'next operation prepares a new handle')
  } finally {
    f.db.close()
  }
})

test('nested scopes restore outer handles, including rollback and thrown callbacks', () => {
  const f = fixture()
  try {
    f.db.transaction(() =>
      f.repository.withSupportReadStatements(() => {
        f.repository.get('a')
        assert.throws(
          () =>
            f.db.transaction(() =>
              f.repository.withSupportReadStatements(() => {
                f.put({ id: 'a', content: 'rolled back' })
                assert.equal(f.repository.get('a')!.content, 'rolled back')
                throw new Error('synthetic-rollback')
              }),
            )(),
          /synthetic-rollback/,
        )
        assert.equal(f.repository.get('a')!.content, 'first')
        assert.equal(f.counts.get(recordSql), 2)
      }),
    )()
    assert.throws(
      () =>
        f.repository.withSupportReadStatements(() => {
          f.repository.get('a')
          throw new Error('synthetic-exit')
        }),
      /synthetic-exit/,
    )
    assert.equal(
      Reflect.get(f.repository, 'supportReads'),
      undefined,
      'thrown scope releases handles',
    )
    f.repository.withSupportReadStatements(() => f.repository.get('a'))
    assert.equal(f.counts.get(recordSql), 4)
  } finally {
    f.db.close()
  }
})

test('schema changes recompile or fail normally, errors discard handles, closed/reopened connections cannot reuse them', () => {
  const f = fixture()
  try {
    f.repository.withSupportReadStatements(() => {
      assert.equal(f.repository.get('a')!.content, 'first')
      f.db.exec(
        'DROP TABLE memories; CREATE TABLE memories(id TEXT PRIMARY KEY,extra TEXT,value TEXT)',
      )
      f.originalPrepare('INSERT INTO memories VALUES (?,?,?)').run(
        'a',
        'new column',
        '{"id":"a","content":"new schema"}',
      )
      assert.equal(f.repository.get('a')!.content, 'new schema')
      f.db.exec('DROP TABLE memories')
      assert.throws(() => f.repository.get('a'), /no such table/)
      f.db.exec('CREATE TABLE memories(id TEXT PRIMARY KEY,value TEXT)')
      f.put({ id: 'a', content: 'recreated' })
      assert.equal(f.repository.get('a')!.content, 'recreated')
      assert.equal(f.counts.get(recordSql), 2)
      f.db.close()
      assert.throws(() => f.repository.get('a'), /not open/)
    })
    const reopened = fixture()
    try {
      assert.equal(
        reopened.repository.withSupportReadStatements(() => reopened.repository.get('a'))!.content,
        'first',
      )
    } finally {
      reopened.db.close()
    }
  } finally {
    if (f.db.open) f.db.close()
  }
})

test('recovery gate remains before each record read, while null/duplicate/malformed JSON is freshly decoded', () => {
  const f = fixture()
  try {
    f.repository.withSupportReadStatements(() => {
      f.originalPrepare('UPDATE memories SET value=? WHERE id=?').run(
        '{"id":"a","content":"first","content":"last"}',
        'a',
      )
      assert.equal(f.repository.get('a')!.content, 'last')
      f.put(null)
      assert.equal(f.repository.get('a'), null)
      f.originalPrepare('UPDATE memories SET value=? WHERE id=?').run('{broken', 'a')
      assert.throws(() => f.repository.get('a'), SyntaxError)
      f.put({ id: 'a', content: 'fixed' })
      assert.equal(f.repository.get('a')!.content, 'fixed')
      f.block()
      assert.throws(() => f.repository.get('a'), /synthetic-recovery-block/)
      assert.equal(f.checks(), 5)
      assert.equal(f.counts.get(recordSql), 1)
    })
  } finally {
    f.db.close()
  }
})

test('reentrant SQLite reads preserve the original busy-connection failure', () => {
  const f = fixture()
  try {
    const inner: string[] = []
    f.db.function('read_inner', (id: string, value: string) => {
      if (id === 'a') inner.push(f.repository.get('b')!.content)
      return value
    })
    f.db.exec(
      'ALTER TABLE memories RENAME TO backing; CREATE VIEW memories AS SELECT id,read_inner(id,value) AS value FROM backing',
    )
    f.originalPrepare('INSERT INTO backing VALUES (?,?)').run('b', '{"id":"b","content":"inner"}')
    const busy = {
      name: 'TypeError',
      message: 'This database connection is busy executing a query',
    }
    assert.throws(() => f.repository.get('a'), busy)
    assert.throws(() => f.repository.withSupportReadStatements(() => f.repository.get('a')), busy)
    assert.deepEqual(inner, [])
    assert.equal(f.checks(), 4)
    f.db.exec('DROP VIEW memories; ALTER TABLE backing RENAME TO memories')
    assert.equal(
      f.repository.withSupportReadStatements(() => f.repository.get('a'))!.content,
      'first',
    )
  } finally {
    f.db.close()
  }
})

test('warm operation handles do not hide an external recovery-authority advance', () => {
  const root = mkdtempSync(join(tmpdir(), 'he-support-read-authority-'))
  const store = new Store(join(root, 'live', 'synthetic.sqlite'))
  const authority = new RecoveryAuthority(metadata(store.db)!.authority)
  store.db.exec('CREATE TABLE memories(id TEXT PRIMARY KEY, value TEXT)')
  const repository = new AssertionRepository(store, {
    beforeWrite: () => {},
    removeProjection: () => {},
  })
  try {
    store.db.transaction(() =>
      repository.withSupportReadStatements(() => {
        assert.equal(repository.get('absent'), undefined)
        authority.append(
          { kind: 'source', sceneId: 'synthetic', messageId: 'revoked' },
          authority.head(),
        )
        assert.throws(() => repository.get('absent'), /recovery_authority_head_changed/)
      }),
    )()
    assert.equal(Reflect.get(repository, 'supportReads'), undefined)
  } finally {
    authority.db.close()
    store.close()
    rmSync(root, { recursive: true, force: true })
  }
})
