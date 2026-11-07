/** Actual repository/revision coordinator; callbacks below are explicit recording test ports. */
import assert from 'node:assert/strict'
import Database from 'better-sqlite3'
import { mock } from 'node:test'
import { createHash } from 'node:crypto'
import { readFileSync, writeFileSync } from 'node:fs'
import { initializeMemoryStorage } from '../../src/memory/storage/migrations.ts'
import { AssertionRepository } from '../../src/memory/storage/assertion-repository.ts'
import { MemoryRevisionCoordinator } from '../../src/memory/write/revision-coordinator.ts'
import { fingerprint, historicalFingerprint } from '../../src/memory/domain/record-values.ts'
import { canonicalProofKey } from '../../src/memory/domain/proofs.ts'
import {
  eligibilityIncarnation,
  eligibilityMaterial,
  eligibilityState,
  eligibilityDenied,
} from '../../src/memory/recovery/eligibility-contract.ts'
import { recordAssertionEligibilityChanges } from '../../src/memory/recovery/eligibility.ts'
assert.equal(process.version, 'v24.22.1')
mock.timers.enable({ apis: ['Date'], now: new Date('2026-10-28T00:00:00.000Z') })
const record = (patch = {}) => ({
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
  lineageStatus: 'verified',
  ...patch,
})
const proof = (patch = {}) => ({ sources: record().sources, parents: [], ...patch })
const traces = []
function trace(name, operations, { retain = true } = {}) {
  const db = new Database(':memory:')
  db.pragma('foreign_keys=ON')
  initializeMemoryStorage({ db })
  let repository,
    events = [],
    fault = ''
  const revisions = new MemoryRevisionCoordinator(
    db,
    (id) => repository.get(id),
    (id) => repository.supports(id),
    () => retain,
    (id, version, epoch) => {
      events.push(['committed', id, version, epoch])
      if (fault === 'committed') throw Error('synthetic_committed')
    },
    () => {
      events.push(['afterCommit'])
      if (fault === 'afterCommit') throw Error('synthetic_afterCommit')
    },
    (changes) => {
      events.push([
        'eligibility',
        changes.map(({ before, after }) => ({
          before: eligibilityIncarnation(before),
          after: eligibilityIncarnation(after),
          state: eligibilityState(after),
          material: eligibilityDenied(eligibilityState(after))
            ? null
            : eligibilityMaterial(after, repository.supports(after.id)),
        })),
      ])
      recordAssertionEligibilityChanges(db, changes)
    },
  )
  repository = new AssertionRepository(
    { db, assertRecoveryReady() {} },
    {
      beforeWrite: (id) => revisions.beforeWrite(id),
      afterWrite: (r) => revisions.afterWrite(r),
      removeProjection: (id) => events.push(['removeProjection', id]),
    },
  )
  const steps = []
  function actions(ops) {
    for (const [method, ...args] of ops) {
      if (method === 'raw') db.exec(args[0])
      else if (method === 'write') repository.write(args[0])
      else if (method === 'supports') repository.saveSupports(args[0], args[1])
      else if (method === 'checkpoint') revisions.checkpoint()
      else if (method === 'touch') revisions.touch(args[0])
      else if (method === 'throw') throw Error(args[0])
      else if (method === 'fault') fault = args[0]
      else if (method === 'writeThenMutate') {
        repository.write(args[0])
        args[0].status = 'active'
        args[0].lineageStatus = 'verified'
      } else if (method === 'nested') {
        try {
          revisions.transaction(() => actions(args[0]))()
        } catch (e) {
          if (!args[1]) throw e
          events.push(['caught', e.message])
        }
      } else throw Error('unknown_action')
    }
  }
  for (const original of operations) {
    const op = structuredClone(original)
    let error,
      result = null
    fault = ''
    events = []
    try {
      if (op[0] === 'seed') {
        const r = op[1]
        db.prepare('INSERT INTO memories VALUES(?,?,?,?,?,?)').run(
          r.id,
          'scene:scene',
          r.scope.personId,
          r.content,
          fingerprint(r.scope, r.content),
          JSON.stringify(r),
        )
        db.prepare('INSERT INTO memory_supports VALUES(?,?)').run(r.id, JSON.stringify(op[2]))
      } else if (op[0] === 'outside') actions(op[1])
      else revisions.transaction(() => actions(op[1]))()
    } catch (e) {
      error = e.message
    }
    const names = db
      .prepare(
        "SELECT name FROM sqlite_master WHERE type='table' AND (name LIKE 'memory_%' OR name='memories' OR name='vector_spaces') ORDER BY name",
      )
      .all()
      .map((r) => r.name)
    const rows = Object.fromEntries(
      names.map((name) => [name, db.prepare(`SELECT * FROM ${name} ORDER BY 1,2`).all()]),
    )
    steps.push({
      op: JSON.stringify(original),
      result: JSON.stringify(result),
      ...(error ? { error } : {}),
      events: JSON.stringify(events),
      rowsHash: createHash('sha256').update(JSON.stringify(rows)).digest('hex'),
    })
  }
  traces.push({ name, retain, steps })
  db.close()
}
trace('write-checkpoint-final-eligibility', [
  ['outside', [['write', record()]]],
  [
    'transaction',
    [
      ['write', record()],
      ['supports', 'assertion', [proof()]],
    ],
  ],
  [
    'transaction',
    [
      ['write', record()],
      ['supports', 'assertion', [proof()]],
    ],
  ],
  [
    'transaction',
    [['write', record({ status: 'invalidated' })], ['checkpoint'], ['write', record()]],
  ],
  ['transaction', [['write', record({ lineageStatus: 'quarantined' })]]],
  ['transaction', [['write', record({ lineageStatus: 'verified' })]]],
  ['transaction', [['writeThenMutate', record({ status: 'invalidated' })]]],
  ['transaction', [['write', record({ status: 'active', learnedAt: '2026-02-01T00:00:00.000Z' })]]],
])
trace('nested-rollback-and-proof-dedup', [
  [
    'transaction',
    [
      ['write', record()],
      ['supports', 'assertion', [proof()]],
      [
        'nested',
        [
          ['write', record({ content: 'ROLLBACK_CANARY' })],
          ['checkpoint'],
          ['throw', 'nested_rollback'],
        ],
        true,
      ],
      [
        'supports',
        'assertion',
        [
          proof({
            sourceObservations: [
              { sceneId: 'scene', messageId: 'message', senderId: 'person', note: 'first' },
            ],
          }),
          proof({
            sourceObservations: [
              { sceneId: 'scene', messageId: 'message', senderId: 'person', note: 'second' },
              { sceneId: 'scene', messageId: 'new', senderId: 'person', note: 'new' },
            ],
          }),
        ],
      ],
    ],
  ],
  ['transaction', [['nested', [['write', record({ content: 'accepted child' })]], false]]],
  [
    'transaction',
    [
      ['write', record({ content: 'OUTER_ROLLBACK_CANARY' })],
      ['throw', 'outer_rollback'],
    ],
  ],
  ['transaction', [['touch', 'assertion'], ['checkpoint']]],
])
trace('legacy-preimage-import', [
  ['seed', record({ lineageStatus: 'unverified' }), [proof()]],
  [
    'transaction',
    [
      ['supports', 'assertion', []],
      ['write', record({ content: 'updated', lineageStatus: 'verified' })],
    ],
  ],
  [
    'transaction',
    [
      ['fault', 'committed'],
      ['write', record({ content: 'FAILED_COMMIT' })],
    ],
  ],
  [
    'transaction',
    [
      ['fault', 'afterCommit'],
      ['write', record({ content: 'COMMITTED_BEFORE_CALLBACK_ERROR' })],
    ],
  ],
])
trace(
  'retention-disabled',
  [
    [
      'transaction',
      [
        ['write', record()],
        ['supports', 'assertion', [proof()]],
      ],
    ],
    ['transaction', [['write', record({ status: 'invalidated' })]]],
    ['transaction', [['write', record({ status: 'active' })]]],
  ],
  { retain: false },
)
trace('foreign-key-and-cascades', [
  ['transaction', [['supports', 'missing', [proof()]]]],
  [
    'transaction',
    [
      ['write', record()],
      ['supports', 'assertion', [proof()]],
    ],
  ],
  [
    'transaction',
    [
      [
        'raw',
        "INSERT INTO vector_spaces VALUES('space','{}'); INSERT INTO memory_vectors VALUES('assertion','space',x'0102'); DELETE FROM memories WHERE id='assertion';",
      ],
    ],
  ],
])
trace('falsy-preimage-repair', [
  [
    'transaction',
    [
      ['write', record()],
      ['supports', 'assertion', [proof()]],
    ],
  ],
  ...['null', 'false', '0', '""'].flatMap((value) => [
    ['transaction', [['raw', `UPDATE memories SET value='${value}' WHERE id='assertion'`]]],
    ['transaction', [['write', record({ content: `repaired ${value}` })]]],
  ]),
])
const rawReadCases = [null, false, 0, '', 42, 'truthy', [], {}].map((value) => {
  const db = new Database(':memory:')
  initializeMemoryStorage({ db })
  const repo = new AssertionRepository(
    { db, assertRecoveryReady() {} },
    { beforeWrite() {}, removeProjection() {} },
  )
  const r = record()
  db.prepare('INSERT INTO memories VALUES(?,?,?,?,?,?)').run(
    r.id,
    'scene:scene',
    'person',
    r.content,
    fingerprint(r.scope, r.content),
    JSON.stringify(value),
  )
  db.prepare('INSERT INTO memory_supports VALUES(?,?)').run(r.id, JSON.stringify(value))
  const result = {
    input: JSON.stringify(value),
    get: JSON.stringify(repo.get(r.id)),
    all: JSON.stringify(repo.all()),
    supports: JSON.stringify(repo.supports(r.id)),
  }
  db.close()
  return result
})
const hashVectors = [
  record(),
  record({ content: '  ΟΣ ΟΣΑ İ \u0085 \ufeff  ' }),
  record({
    id: 'assertion\ud800',
    content: 'A\ud800B\udfff',
    extra: { '\ue000': 1, '𐀀': 2, 10: 3, 2: 4 },
  }),
  record({ scope: { kind: 'sharedPublicKnowledge' }, content: 'Shared' }),
  record({
    status: 'invalidated',
    lineageStatus: 'quarantined',
    arbitrary: [null, true, -0, 1e20],
  }),
].map((r, i) => {
  const proofs = [
    proof({
      parents: ['𐀀', '\ue000'],
      parentReferences: [
        { id: '\ue000', version: 2, deletionEpoch: 0 },
        { id: '𐀀', version: 1, deletionEpoch: 1 },
      ],
      sourceObservations: [
        { sceneId: 'scene', messageId: 'message', senderId: 'person', arbitrary: i },
      ],
      subjectWitness: { kind: 'direct', personId: 'person' },
      extra: { z: 1, a: 2 },
    }),
  ]
  return {
    record: JSON.stringify(r),
    proofs: JSON.stringify(proofs),
    fingerprint: fingerprint(r.scope, r.content),
    historical: historicalFingerprint(r),
    incarnation: eligibilityIncarnation(r),
    material: eligibilityMaterial(r, proofs),
    proofKeys: proofs.map(canonicalProofKey),
  }
})
const paths = [
  'src/memory/storage/assertion-history.ts',
  'src/memory/storage/assertion-repository.ts',
  'src/memory/storage/migrations.ts',
  'src/memory/write/revision-coordinator.ts',
  'src/memory/domain/proofs.ts',
  'src/memory/domain/record-values.ts',
  'src/memory/recovery/eligibility-contract.ts',
  'src/memory/recovery/eligibility.ts',
]
const sources = Object.fromEntries(
  paths.map((p) => [p, createHash('sha256').update(readFileSync(p)).digest('hex')]),
)
const path = 'backend/internal/assertions/testdata/coordinator.json',
  raw =
    JSON.stringify({ node: process.version, sources, traces, hashVectors, rawReadCases }, null, 2) +
    '\n'
if (process.argv.includes('--check')) assert.equal(readFileSync(path, 'utf8'), raw)
else writeFileSync(path, raw)
console.log(
  `${traces.length} coordinator traces, ${traces.reduce((n, t) => n + t.steps.length, 0)} transitions`,
)
