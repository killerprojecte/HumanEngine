import assert from 'node:assert/strict'
import test from 'node:test'
import Database from 'better-sqlite3'
import { FixedReadStatements } from '../src/storage/fixed-read-statements.ts'

const queries = {
  one: { sql: 'SELECT value FROM values_table WHERE id=?', all: false },
  all: { sql: 'SELECT id,value FROM values_table ORDER BY id', all: true },
} as const

test('fixed reads reuse compilation only, observing writes, rollback and caller object mutation', () => {
  const db = new Database(':memory:')
  db.exec(
    "CREATE TABLE values_table(id INTEGER PRIMARY KEY,value TEXT); INSERT INTO values_table VALUES(1,'first')",
  )
  const prepare = db.prepare.bind(db)
  let compiled = 0
  db.prepare = ((sql: string) => {
    compiled++
    return prepare(sql)
  }) as typeof db.prepare
  const reads = new FixedReadStatements(db, queries)
  try {
    const first = reads.read('one', 1) as { value: string }
    first.value = 'caller mutation'
    assert.deepEqual(reads.read('one', 1), { value: 'first' })
    assert.throws(
      () =>
        db.transaction(() => {
          prepare('UPDATE values_table SET value=?').run('inside')
          assert.deepEqual(reads.read('one', 1), { value: 'inside' })
          throw new Error('rollback')
        })(),
      /rollback/,
    )
    assert.deepEqual(reads.read('one', 1), { value: 'first' })
    prepare('UPDATE values_table SET value=?').run('committed')
    assert.deepEqual(reads.read('one', 1), { value: 'committed' })
    assert.deepEqual(reads.read('all'), [{ id: 1, value: 'committed' }])
    assert.equal(compiled, 2)
  } finally {
    db.close()
  }
})

test('schema changes recompile and SQL errors clear all private handles', () => {
  const db = new Database(':memory:')
  const reads = new FixedReadStatements(db, queries)
  try {
    assert.throws(() => reads.read('one', 1), /no such table/)
    db.exec(
      "CREATE TABLE values_table(id INTEGER PRIMARY KEY,value TEXT); INSERT INTO values_table VALUES(1,'old')",
    )
    assert.deepEqual(reads.read('one', 1), { value: 'old' })
    reads.read('all')
    db.exec(
      "DROP TABLE values_table; CREATE TABLE values_table(id INTEGER PRIMARY KEY,value TEXT); INSERT INTO values_table VALUES(1,'new')",
    )
    assert.deepEqual(reads.read('one', 1), { value: 'new' })
    db.exec('DROP TABLE values_table')
    assert.throws(() => reads.read('one', 1), /no such table/)
    db.exec(
      "CREATE TABLE values_table(id INTEGER PRIMARY KEY,value TEXT); INSERT INTO values_table VALUES(1,'recreated')",
    )
    assert.deepEqual(reads.read('all'), [{ id: 1, value: 'recreated' }])
    db.close()
    assert.throws(() => reads.read('one', 1), /not open/)
  } finally {
    if (db.open) db.close()
  }
})

test('reentrant SQLite callback retains the original busy-connection rejection', () => {
  const db = new Database(':memory:')
  const reads = new FixedReadStatements(db, queries)
  try {
    db.exec(
      "CREATE TABLE backing(id INTEGER PRIMARY KEY,value TEXT); INSERT INTO backing VALUES(1,'outer'),(2,'inner')",
    )
    db.function('read_inner', (id: number, value: string) => {
      if (id === 1) reads.read('one', 2)
      return value
    })
    db.exec('CREATE VIEW values_table AS SELECT id,read_inner(id,value) AS value FROM backing')
    const busy = {
      name: 'TypeError',
      message: 'This database connection is busy executing a query',
    }
    assert.throws(() => db.prepare(queries.one.sql).get(1), busy)
    assert.throws(() => reads.read('one', 1), busy)
    db.exec('DROP VIEW values_table; ALTER TABLE backing RENAME TO values_table')
    assert.deepEqual(reads.read('one', 1), { value: 'outer' })
  } finally {
    db.close()
  }
})
