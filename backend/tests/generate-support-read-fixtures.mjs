/** Actual source AssertionRepository, including real prepares and SQL steps. */
import assert from 'node:assert/strict'
import Database from 'better-sqlite3'
import { createHash } from 'node:crypto'
import { readFileSync, writeFileSync } from 'node:fs'
import { AssertionRepository } from '../../src/memory/storage/assertion-repository.ts'

assert.equal(process.version, 'v24.22.1')
const recordSql = 'SELECT * FROM memories WHERE id=?'
const proofSql = 'SELECT value FROM memory_supports WHERE memory_id=?'
const first = '{"id":"a","content":"first","nested":{"value":"fresh"}}'
const proof = '[{"parents":["first"],"sources":[]}]'
const traces = []
function trace(name, operations) {
  const db = new Database(':memory:')
  db.exec(
    'CREATE TABLE memories(id TEXT PRIMARY KEY,value TEXT); CREATE TABLE memory_supports(memory_id TEXT PRIMARY KEY,value TEXT)',
  )
  const originalPrepare = db.prepare.bind(db)
  originalPrepare('INSERT INTO memories VALUES (?,?)').run('a', first)
  originalPrepare('INSERT INTO memory_supports VALUES (?,?)').run('a', proof)
  const counts = { record: 0, supports: 0 }
  let checks = 0,
    blocked = false,
    reenter = '',
    reentered = null
  db.prepare = (sql) => {
    if (sql === recordSql) counts.record++
    if (sql === proofSql) counts.supports++
    const statement = originalPrepare(sql)
    if (sql === recordSql) {
      const originalGet = statement.get.bind(statement)
      statement.get = (...args) => {
        const mode = reenter
        reenter = ''
        if (mode === 'read') reentered = repository.get('a')
        if (mode === 'failure') {
          db.exec('DROP TABLE memory_supports')
          assert.throws(() => repository.supports('a'), /no such table/)
        }
        return originalGet(...args)
      }
    }
    return statement
  }
  const repository = new AssertionRepository(
    {
      db,
      assertRecoveryReady() {
        checks++
        if (blocked) throw new Error('synthetic-recovery-block')
      },
    },
    { beforeWrite() {}, removeProjection() {} },
  )
  const steps = []
  function run(ops) {
    for (const op of ops) {
      let result = null,
        error = ''
      try {
        switch (op[0]) {
          case 'scope':
            repository.withSupportReadStatements(() => run(op[1]))
            break
          case 'rollback':
            db.transaction(() =>
              repository.withSupportReadStatements(() => {
                run(op[1])
                throw new Error('synthetic-rollback')
              }),
            )()
            break
          case 'panic':
            repository.withSupportReadStatements(() => {
              run(op[1])
              throw new Error('synthetic-exit')
            })
            break
          case 'get': {
            const value = repository.get(op[1])
            result = value === undefined ? { found: false } : { found: true, value }
            break
          }
          case 'supports':
            result = repository.supports(op[1])
            break
          case 'mutateRecord':
            repository.get('a').nested.value = 'caller mutation'
            break
          case 'mutateProof':
            repository.supports('a')[0].parents.push('caller mutation')
            break
          case 'record':
            originalPrepare('UPDATE memories SET value=? WHERE id=?').run(op[1], 'a')
            break
          case 'proof':
            originalPrepare('UPDATE memory_supports SET value=? WHERE memory_id=?').run(op[1], 'a')
            break
          case 'sql':
            db.exec(op[1])
            break
          case 'block':
            blocked = op[1]
            break
          case 'reenter':
            reenter = op[1]
            break
          case 'reentered':
            result = reentered
            break
          default:
            throw new Error(`unknown operation ${op[0]}`)
        }
      } catch (e) {
        error =
          e instanceof SyntaxError
            ? 'invalid-json'
            : e.message.includes('no such table')
              ? 'missing-table'
              : e.message
      }
      steps.push({
        op: JSON.stringify(op),
        result: JSON.stringify(result),
        error,
        counts: { ...counts },
        checks,
      })
    }
  }
  try {
    run(operations)
    traces.push({ name, operations, steps })
  } finally {
    db.close()
  }
}
trace('fresh reads and independent nested scopes', [
  ['get', 'a'],
  ['get', 'a'],
  ['supports', 'a'],
  [
    'scope',
    [
      ['mutateRecord'],
      ['get', 'a'],
      ['mutateProof'],
      ['supports', 'a'],
      ['record', '{"id":"a","content":"second"}'],
      ['proof', '[{"parents":["second"],"sources":[]}]'],
      ['get', 'a'],
      ['supports', 'a'],
      [
        'scope',
        [
          ['get', 'a'],
          ['supports', 'a'],
          ['get', 'a'],
        ],
      ],
      ['get', 'a'],
      ['supports', 'a'],
    ],
  ],
  ['get', 'a'],
  [
    'scope',
    [
      ['get', 'a'],
      ['get', 'missing'],
      ['supports', 'missing'],
    ],
  ],
])
trace('caught rollback and exceptional cleanup', [
  [
    'scope',
    [
      ['get', 'a'],
      ['supports', 'a'],
      [
        'rollback',
        [
          ['record', '{"id":"a","content":"rolled back"}'],
          ['proof', '["rolled back"]'],
          ['get', 'a'],
          ['supports', 'a'],
        ],
      ],
      ['get', 'a'],
      ['supports', 'a'],
      [
        'panic',
        [
          ['get', 'a'],
          ['supports', 'a'],
        ],
      ],
      ['get', 'a'],
      ['supports', 'a'],
    ],
  ],
  [
    'scope',
    [
      ['get', 'a'],
      ['supports', 'a'],
    ],
  ],
])
trace('lazy preparation SQL eviction and schema changes', [
  ['sql', 'DROP TABLE memories'],
  ['scope', []],
  [
    'scope',
    [
      ['supports', 'a'],
      ['get', 'a'],
      ['sql', 'CREATE TABLE memories(id TEXT PRIMARY KEY,value TEXT)'],
      ['sql', `INSERT INTO memories VALUES('a','${first}')`],
      ['get', 'a'],
      ['supports', 'a'],
      ['sql', 'ALTER TABLE memories ADD COLUMN extra TEXT'],
      ['get', 'a'],
      ['sql', 'DROP TABLE memories'],
      ['get', 'a'],
      ['sql', 'CREATE TABLE memories(id TEXT PRIMARY KEY,extra TEXT,value TEXT)'],
      ['sql', "INSERT INTO memories VALUES('a','new','{\"content\":\"new schema\"}')"],
      ['get', 'a'],
      ['supports', 'a'],
    ],
  ],
])
trace('readiness and raw JSON roots never cached', [
  [
    'scope',
    [
      ...[
        'null',
        'false',
        '0',
        '""',
        '[1,"raw"]',
        '"string"',
        '{"content":"first","content":"last"}',
        '{broken',
        first,
      ].flatMap((raw) => [
        ['record', raw],
        ['get', 'a'],
      ]),
      ...['null', 'false', '0', '""', '{"raw":true}', '[1]', '{broken', proof].flatMap((raw) => [
        ['proof', raw],
        ['supports', 'a'],
      ]),
      ['block', true],
      ['get', 'a'],
      ['supports', 'a'],
      ['block', false],
      ['get', 'a'],
    ],
  ],
])
trace('reentry uses ordinary preparation and retires failed handles after execution', [
  [
    'scope',
    [
      ['reenter', 'read'],
      ['get', 'a'],
      ['reentered'],
      ['get', 'a'],
      ['supports', 'a'],
      ['reenter', 'failure'],
      ['get', 'a'],
      ['get', 'a'],
      ['sql', 'CREATE TABLE memory_supports(memory_id TEXT PRIMARY KEY,value TEXT)'],
      ['supports', 'a'],
    ],
  ],
])
const sources = Object.fromEntries(
  ['src/memory/storage/assertion-repository.ts'].map((p) => [
    p,
    createHash('sha256').update(readFileSync(p)).digest('hex'),
  ]),
)
const path = 'backend/internal/assertions/testdata/support-reads.json'
const raw = JSON.stringify({ node: process.version, sources, traces }, null, 2) + '\n'
if (process.argv.includes('--check')) assert.equal(readFileSync(path, 'utf8'), raw)
else writeFileSync(path, raw)
console.log(
  `${traces.length} actual support-read traces, ${traces.reduce((n, trace) => n + trace.steps.length, 0)} steps`,
)
