/** Direct AssertionHistory oracle; synthetic storage only, never model authorization. */
import assert from 'node:assert/strict'
import Database from 'better-sqlite3'
import { createHash } from 'node:crypto'
import { readFileSync, writeFileSync, mkdirSync } from 'node:fs'
import {
  AssertionHistory,
  AssertionHistoryReader,
} from '../../src/memory/storage/assertion-history.ts'
assert.equal(process.version, 'v24.22.1')
const record = (extra = {}) => ({
  id: 'assertion',
  scope: { kind: 'scene', sceneId: 'scene', personId: 'person' },
  content: 'Synthetic preference',
  kind: 'claim',
  sources: [{ sceneId: 'scene', messageId: 'message', senderId: 'person' }],
  importance: 'medium',
  confidence: 0.6,
  learnedAt: '2026-01-02T00:00:00.000Z',
  status: 'active',
  locked: false,
  origin: 'learned',
  lineageStatus: 'unverified',
  ...extra,
})
const input = (extra = {}) => ({
  record: record(),
  supports: [
    { sources: record().sources, parents: ['parent'] },
    { sources: [{ sceneId: 'scene', messageId: 'independent', senderId: 'person' }], parents: [] },
  ],
  expectedVersion: 0,
  expectedDeletionEpoch: 0,
  idempotencyKey: 'first',
  recordedAt: '2026-01-02T00:00:00.000Z',
  validFrom: null,
  validTo: null,
  eventKind: 'learned_assertion',
  ...extra,
})
const traces = []
const tables = [
  'memory_assertion_state',
  'memory_assertion_revisions',
  'memory_assertion_revision_sources',
  'memory_assertion_revision_parents',
]
function trace(name, ops) {
  const db = new Database(':memory:')
  db.pragma('foreign_keys=ON')
  new AssertionHistoryReader(db)
  assert.equal(db.prepare("SELECT count(*) n FROM sqlite_master WHERE type='table'").get().n, 0)
  const history = new AssertionHistory(db),
    steps = []
  for (const op of ops) {
    let result = null,
      error
    const encodedOp = JSON.stringify(op)
    const [method, ...args] = op
    const invoke = () => {
      if (method === 'latest') return history.revisions(...args).at(-1) ?? null
      if (method === 'raw') {
        db.exec(args[0])
        return null
      }
      if (method === 'bulk') {
        for (let i = 0; i < args[0]; i++)
          history.append(
            input({
              expectedVersion: i,
              idempotencyKey: `bulk:${i}`,
              recordedAt: `2026-01-${String(i < 5 ? 2 : 3).padStart(2, '0')}T00:00:00.000Z`,
              record: record({ content: `Synthetic ${i}` }),
            }),
          )
        return null
      }
      if (method === 'rollback') {
        history.append(args[0])
        throw Error('synthetic_rollback')
      }
      if (method === 'appendDetached') {
        args[0].record.extra = { nan: NaN, minusZero: -0, nested: { value: 'original' } }
        const value = history.append(args[0])
        const aliases =
          value.record === args[0].record ||
          value.supports === args[0].supports ||
          value.supports[0].sources[0] === args[0].supports[0].sources[0]
        value.record.extra.nested.value = 'return mutation'
        const inputNested = args[0].record.extra.nested.value
        args[0].record.content = 'input mutation'
        return {
          aliases,
          inputNested,
          returnedContent: value.record.content,
          nanIsNull: value.record.extra.nan === null,
          minusZero: Object.is(value.record.extra.minusZero, -0),
        }
      }
      if (method === 'mutateReturned') {
        const value = history.append(args[0])
        value.record.content = 'CALLER_MUTATION'
        return null
      }
      if (method === 'noTransaction') return history[args[0]](...args.slice(1))
      return history[method](...args)
    }
    try {
      result = method === 'noTransaction' ? invoke() : db.transaction(invoke)()
    } catch (e) {
      error = e.message
    }
    const rows = Object.fromEntries(
      tables.map((table) => [table, db.prepare(`SELECT * FROM ${table} ORDER BY 1,2`).all()]),
    )
    steps.push({
      op: encodedOp,
      result: JSON.stringify(result ?? null),
      ...(error ? { error } : {}),
      rowsHash: createHash('sha256').update(JSON.stringify(rows)).digest('hex'),
      rowCounts: Object.fromEntries(
        Object.entries(rows).map(([name, values]) => [name, values.length]),
      ),
    })
  }
  traces.push({ name, steps })
  db.close()
}
trace('append-idempotency-and-scope', [
  ['state', 'assertion'],
  ['noTransaction', 'append', input()],
  ['mutateReturned', input()],
  ['append', input({ expectedVersion: 99 })],
  ['append', input({ record: record({ content: 'changed' }) })],
  ['append', input({ idempotencyKey: 'new' })],
  ['rollback', input({ expectedVersion: 1, idempotencyKey: 'rollback' })],
  [
    'append',
    input({ expectedVersion: 1, idempotencyKey: 'second', record: record({ content: 'second' }) }),
  ],
  [
    'append',
    input({
      expectedVersion: 2,
      idempotencyKey: 'foreign',
      record: record({ scope: { kind: 'scene', sceneId: 'foreign' } }),
    }),
  ],
  ['revision', 'assertion', 1, 'scene:scene'],
  ['revision', 'assertion', 1, 'scene:foreign'],
  ['revisions', 'assertion', 'scene:scene'],
  ['revisions', 'assertion', 'scene:scene', 1],
  ['retainedCount', 'assertion', 'scene:scene'],
  ['latest', 'assertion', 'scene:scene'],
  ['dependents', 'parent'],
])
trace('detached-normalized-return', [['appendDetached', input()]])
trace('validation-order', [
  ...[
    {},
    { expectedVersion: -1 },
    { expectedVersion: 0.5 },
    { expectedVersion: 9007199254740992 },
    { expectedDeletionEpoch: -1 },
    { idempotencyKey: '' },
    { idempotencyKey: 'x'.repeat(257) },
    { recordedAt: '2026-02-30T00:00:00.000Z' },
    { recordedAt: '2026-01-02T00:00:00Z' },
    { validFrom: '2026-01-03T00:00:00.000Z', validTo: '2026-01-02T00:00:00.000Z' },
  ].map((p) => ['append', input(p)]),
  ['revisions', 'assertion', 'scene:scene', 0],
  ['revisions', 'assertion', 'scene:scene', 10001],
  ['queryRetained', 'assertion', 'scene:scene', { throughVersion: 1, limit: 21 }],
  [
    'queryRetained',
    'assertion',
    'scene:scene',
    { throughVersion: 1, limit: 1, validAt: 'invalid' },
  ],
])
trace('privacy-epochs-and-terminal', [
  ['append', input()],
  ['redactSource', 'scene', 'message'],
  ['state', 'assertion'],
  ['append', input()],
  [
    'append',
    input({
      expectedVersion: 1,
      expectedDeletionEpoch: 1,
      idempotencyKey: 'replacement',
      record: record({ content: 'replacement', sources: [] }),
      supports: [],
    }),
  ],
  ['redactSource', 'scene', 'message'],
  ['purge', 'assertion'],
  ['purge', 'assertion'],
  ['state', 'assertion'],
  ['erase', 'assertion'],
  ['erase', 'assertion'],
  ['purge', 'assertion'],
  ['append', input({ expectedVersion: 2, expectedDeletionEpoch: 4 })],
  ['queryRetained', 'assertion', 'scene:scene', { throughVersion: 2, limit: 1 }],
  ['dependents', 'parent'],
])
trace('legacy-and-expanded-dates', [
  ['importLegacy', record({ learnedAt: 'unknown', validFrom: 'bad' }), []],
  ['importLegacy', record({ content: 'ignored' }), []],
  ['revisions', 'assertion', 'scene:scene'],
  [
    'append',
    input({
      expectedVersion: 1,
      idempotencyKey: 'expanded',
      recordedAt: '+010000-01-01T00:00:00.000Z',
    }),
  ],
  [
    'append',
    input({
      expectedVersion: 2,
      idempotencyKey: 'negative',
      recordedAt: '-000001-01-01T00:00:00.000Z',
    }),
  ],
  [
    'append',
    input({
      expectedVersion: 3,
      idempotencyKey: 'minus-zero',
      recordedAt: '-000000-01-01T00:00:00.000Z',
    }),
  ],
  ['revisions', 'assertion', 'scene:scene'],
])
trace('query-predicates-before-limit', [
  ['bulk', 205],
  ['latest', 'assertion', 'scene:scene'],
  [
    'queryRetained',
    'assertion',
    'scene:scene',
    { throughVersion: 205, limit: 2, asOfRecorded: '2026-01-02T00:00:00.000Z' },
  ],
  ['queryRetained', 'assertion', 'scene:scene', { throughVersion: 205, limit: 20 }],
  [
    'raw',
    "UPDATE memory_assertion_revisions SET value='{invalid' WHERE version=205; UPDATE memory_assertion_revisions SET recorded_at=NULL WHERE version=204;",
  ],
  ['queryRetained', 'assertion', 'scene:scene', { throughVersion: 205, limit: 2 }],
  [
    'raw',
    "UPDATE memory_assertion_revisions SET value=json_set(value,'$.assertionId','wrong') WHERE version=203;",
  ],
  ['queryRetained', 'assertion', 'scene:scene', { throughVersion: 203, limit: 1 }],
  ['latest', 'assertion', 'scene:scene'],
])
trace('validity-and-retention', [
  ['append', input({ validFrom: '2026-01-03T00:00:00.000Z', validTo: '2026-01-04T00:00:00.000Z' })],
  [
    'queryRetained',
    'assertion',
    'scene:scene',
    { throughVersion: 1, limit: 1, validAt: '2026-01-03T00:00:00.000Z' },
  ],
  [
    'queryRetained',
    'assertion',
    'scene:scene',
    { throughVersion: 1, limit: 1, validAt: '2026-01-04T00:00:00.000Z' },
  ],
  ['raw', "UPDATE memory_assertion_revisions SET recorded_at='2026-01-03T00:00:00.000Z'"],
  ['queryRetained', 'assertion', 'scene:scene', { throughVersion: 1, limit: 1 }],
  ['purge', 'assertion'],
  ['queryRetained', 'assertion', 'scene:scene', { throughVersion: 1, limit: 1 }],
])
const sources = Object.fromEntries(
  ['src/memory/storage/assertion-history.ts'].map((p) => [
    p,
    createHash('sha256').update(readFileSync(p)).digest('hex'),
  ]),
)
const path = 'backend/internal/assertions/testdata/history.json',
  raw = JSON.stringify({ node: process.version, sources, traces }, null, 2) + '\n'
if (process.argv.includes('--check')) assert.equal(readFileSync(path, 'utf8'), raw)
else {
  mkdirSync('backend/internal/assertions/testdata', { recursive: true })
  writeFileSync(path, raw)
}
console.log(
  `${traces.length} history traces, ${traces.reduce((n, t) => n + t.steps.length, 0)} transitions`,
)
