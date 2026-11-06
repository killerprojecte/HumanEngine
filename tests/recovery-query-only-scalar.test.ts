import assert from 'node:assert/strict'
import test from 'node:test'
import Database from 'better-sqlite3'
import { readRecoveryStatement } from '../src/memory/recovery/read-statements.ts'

test('query-only scalar handle executes fresh state inside transactions and stays connection-local', () => {
  const db = new Database(':memory:')
  const other = new Database(':memory:')
  const read = () => readRecoveryStatement(db, 'queryOnly')
  try {
    db.exec('CREATE TABLE probe(value TEXT)')
    assert.deepEqual(read(), { query_only: 0 })
    other.pragma('query_only = ON')
    assert.deepEqual(readRecoveryStatement(other, 'queryOnly'), { query_only: 1 })
    assert.deepEqual(read(), { query_only: 0 })
    assert.throws(
      () =>
        db.transaction(() => {
          db.pragma('query_only = ON')
          assert.deepEqual(read(), { query_only: 1 })
          assert.throws(() => db.exec("INSERT INTO probe VALUES('denied')"), /readonly/)
          db.pragma('query_only = OFF')
          assert.deepEqual(read(), { query_only: 0 })
          db.exec("INSERT INTO probe VALUES('rollback')")
          throw Error('rollback')
        })(),
      /rollback/,
    )
    assert.equal(db.inTransaction, false)
    assert.deepEqual(read(), { query_only: 0 })
    assert.deepEqual(db.prepare('SELECT * FROM probe').all(), [])
    // Returning a fresh object must not allow callers to affect a later invariant read.
    ;(read() as { query_only: number }).query_only = 1
    assert.deepEqual(read(), { query_only: 0 })
  } finally {
    other.close()
    db.close()
  }
  assert.throws(read, /recovery_connection_closed/)
})

test('query-only scalar avoids rebuilding object row factories without caching results', () => {
  const db = new Database(':memory:')
  const original = globalThis.Function
  let constructions = 0
  globalThis.Function = new Proxy(original, {
    apply(target, receiver, args) {
      constructions++
      return Reflect.apply(target, receiver, args)
    },
  })
  try {
    for (let i = 0; i < 100; i++)
      assert.deepEqual(readRecoveryStatement(db, 'queryOnly'), { query_only: 0 })
    assert.equal(constructions, 0)
    db.pragma('query_only = ON')
    assert.deepEqual(readRecoveryStatement(db, 'queryOnly'), { query_only: 1 })
    db.pragma('query_only = OFF')
    assert.deepEqual(readRecoveryStatement(db, 'queryOnly'), { query_only: 0 })
    assert.equal(constructions, 0)
  } finally {
    globalThis.Function = original
    db.close()
  }
})

test('query-only scalar rejects unexpected types and values, invalidates handles and propagates errors', () => {
  const db = new Database(':memory:')
  const prepare = db.prepare.bind(db)
  const sentinel = new Error('injected_sqlite_error')
  let invalid = false
  let next: unknown
  let compiled = 0
  db.prepare = ((sql: string) => {
    const statement = prepare(sql)
    if (sql === 'PRAGMA query_only') {
      compiled++
      const get = statement.get.bind(statement)
      statement.get = (() => {
        if (next === sentinel) throw sentinel
        return invalid ? next : get()
      }) as typeof statement.get
    }
    return statement
  }) as typeof db.prepare
  try {
    const read = () => readRecoveryStatement(db, 'queryOnly')
    assert.deepEqual(read(), { query_only: 0 })
    assert.deepEqual(read(), { query_only: 0 })
    assert.equal(compiled, 1)
    for (const value of [undefined, null, false, true, '0', '1', 0n, 1n, -1, 2, NaN, {}]) {
      next = value
      invalid = true
      assert.throws(read, /recovery_query_only_invalid/)
      const previous: number = compiled
      invalid = false
      assert.deepEqual(read(), { query_only: 0 })
      assert.equal(compiled, previous + 1)
    }
    next = sentinel
    assert.throws(read, (error) => error === sentinel)
    const previous: number = compiled
    next = undefined
    assert.deepEqual(read(), { query_only: 0 })
    assert.equal(compiled, previous + 1)
  } finally {
    db.close()
  }
})
