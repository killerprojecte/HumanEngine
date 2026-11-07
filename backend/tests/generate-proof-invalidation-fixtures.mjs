/** Real source ProofInvalidation + repository/history/coordinator/authority.
 * Derived-domain hooks are explicitly recording test ports, not implementations
 * of relations, projections, working sets, knowledge or semantic privacy.
 */
import assert from 'node:assert/strict'
import Database from 'better-sqlite3'
import { mock } from 'node:test'
import { createHash } from 'node:crypto'
import { mkdtempSync, mkdirSync, rmSync, readFileSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { initializeMemoryStorage } from '../../src/memory/storage/migrations.ts'
import { AssertionRepository } from '../../src/memory/storage/assertion-repository.ts'
import { MemoryRevisionCoordinator } from '../../src/memory/write/revision-coordinator.ts'
import { AssertionAuthority } from '../../src/memory/read/assertion-authority.ts'
import { ProofInvalidation } from '../../src/memory/lifecycle/proof-invalidation.ts'
import {
  RecoveryAuthority,
  bindRecovery,
  saveGate,
  metadata,
  assertRecoveryReady,
} from '../../src/memory/recovery/ledger.ts'
import { recordAssertionEligibilityChanges } from '../../src/memory/recovery/eligibility.ts'
assert.equal(process.version, 'v24.22.1')
mock.timers.enable({ apis: ['Date'], now: new Date('2026-10-28T00:00:00.000Z') })
const source = (id) => ({ sceneId: 'scene', messageId: id, senderId: 'person' })
const record = (id, extra = {}) => ({
  id,
  scope: { kind: 'scene', sceneId: 'scene', personId: 'person' },
  content: `Synthetic ${id}`,
  kind: 'claim',
  sources: [source(id)],
  importance: 'medium',
  confidence: 0.6,
  learnedAt: '2026-01-02T00:00:00.000Z',
  status: 'active',
  locked: false,
  origin: 'learned',
  lineageStatus: 'verified',
  ...extra,
})
const proof = (id, parents = [], extra = {}) => ({ sources: [source(id)], parents, ...extra })
const put = (id, proofs = [proof(id)], extra = {}) => ['put', record(id, extra), proofs]
const tx = (...actions) => ['transaction', actions]
const traces = []
function trace(name, operations) {
  const root = mkdtempSync(join(process.cwd(), 'backend/.proof-invalidation-oracle-'))
  let db, ledger, external
  try {
    mkdirSync(join(root, 'live'))
    db = new Database(join(root, 'live', 'main.sqlite'))
    db.pragma('journal_mode=WAL')
    db.pragma('foreign_keys=ON')
    ledger = new RecoveryAuthority(join(root, 'authority'), '12345678-1234-4123-8123-123456789012')
    saveGate(db, { head: ledger.head(), authority: ledger.path, minimumReader: 1, state: 'ready' })
    bindRecovery(db, ledger)
    initializeMemoryStorage({ db })
    db.exec(
      'CREATE TABLE recall_tombstones(scene_id TEXT NOT NULL,message_id TEXT NOT NULL,recalled_at TEXT NOT NULL,PRIMARY KEY(scene_id,message_id))',
    )
    let repository,
      authority,
      events = [],
      fault = ''
    const recalled = (scene, message) =>
      !!db
        .prepare('SELECT 1 FROM recall_tombstones WHERE scene_id=? AND message_id=?')
        .get(scene, message)
    const owner = (kind, id) => {
      events.push([kind, id])
      if (fault === kind) throw Error(`synthetic_${kind}`)
    }
    const revisions = new MemoryRevisionCoordinator(
      db,
      (id) => repository.get(id),
      (id) => authority.historySupports(id),
      (r) =>
        r.status !== 'invalidated' &&
        r.lineageStatus !== 'quarantined' &&
        !r.sources.some((s) => recalled(s.sceneId, s.messageId)),
      (id, version, epoch) => events.push(['committed', id, version, epoch]),
      undefined,
      (changes) => recordAssertionEligibilityChanges(db, changes),
    )
    repository = new AssertionRepository(
      { db, assertRecoveryReady: () => assertRecoveryReady(db) },
      {
        beforeWrite: (id) => revisions.beforeWrite(id),
        afterWrite: (r) => revisions.afterWrite(r),
        removeProjection: (id) => owner('repositoryProjection', id),
      },
    )
    authority = new AssertionAuthority(
      { assertRecoveryReady: () => assertRecoveryReady(db), recalled },
      repository,
      revisions,
    )
    const lifecycle = new ProofInvalidation({
      records: () => repository.all(),
      proofs: (id) => repository.supports(id),
      historyDependents: (id) => revisions.history.dependents(id),
      exists: (id) => !!repository.get(id),
      removeProjection: (id) => owner('projection', id),
      purgeHistory: (id) => {
        owner('beforePurge', id)
        revisions.history.purge(id)
      },
      touch: (id) => revisions.touch(id),
      saveProofs: (id, proofs) => repository.saveSupports(id, proofs),
      supported: (r, path, cache) => authority.supported(r, path, cache),
      invalidateRecord: (r) => {
        owner('beforeInvalidate', r.id)
        repository.write({ ...r, status: 'invalidated' })
      },
      removeVector: (id) => db.prepare('DELETE FROM memory_vectors WHERE memory_id=?').run(id),
    })
    const actions = (ops, results) => {
      const seen = new Set()
      for (const [method, ...args] of ops) {
        if (method === 'put') {
          repository.write(args[0])
          repository.saveSupports(args[0].id, args[1])
        } else if (method === 'supports') repository.saveSupports(args[0], args[1])
        else if (method === 'raw') db.exec(args[0])
        else if (method === 'fault') fault = args[0]
        else if (method === 'throw') throw Error(args[0])
        else if (method === 'checkpoint') revisions.checkpoint()
        else if (method === 'purge')
          lifecycle.purgeHistoryDependents(args[0], seen, new Set(args[1] ?? []))
        else if (method === 'invalidate')
          lifecycle.invalidateDependents(
            args[0],
            (p) => !args[1] || p.parentReferences === undefined,
          )
        else if (method === 'refresh') results.push(lifecycle.refreshSupport())
        else if (method === 'depends') results.push(lifecycle.dependsOn(args[0], args[1]))
        else if (method === 'nested') {
          try {
            revisions.transaction(() => actions(args[0], results))()
          } catch (e) {
            if (!args[1]) throw e
            events.push(['caught', e.message])
          }
        } else throw Error(`unknown_action:${method}`)
      }
    }
    const steps = []
    external = new Database(join(root, 'authority', 'authority.sqlite'))
    for (const original of operations) {
      events = []
      fault = ''
      const results = []
      let error = null
      try {
        revisions.transaction(() => actions(structuredClone(original[1]), results))()
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
      const intents = external
        .prepare('SELECT payload FROM revocations ORDER BY sequence')
        .all()
        .map((r) => {
          const event = JSON.parse(r.payload)
          delete event.operationId
          return event
        })
      steps.push({
        op: JSON.stringify(original),
        result: JSON.stringify(results),
        error,
        events: JSON.stringify(events),
        rowsHash: createHash('sha256').update(JSON.stringify(rows)).digest('hex'),
        intents: JSON.stringify(intents),
        gateSequence: metadata(db).head.sequence,
        minimumReader: metadata(db).minimumReader,
      })
    }
    traces.push({ name, steps })
  } finally {
    external?.close()
    db?.close()
    ledger?.close()
    rmSync(root, { recursive: true, force: true })
  }
}
trace('historical-only-safe-replacement-and-dfs', [
  tx(
    put('root'),
    put('a-history', [proof('old-child', ['root'])]),
    put('b-current', [proof('b', ['root'])]),
    put('c-current', [proof('c', ['root'])]),
    put('shared', [proof('shared', ['a-history', 'b-current'])]),
  ),
  tx(
    put('a-history', [], {
      content: 'Reviewed safe replacement',
      sources: [],
      origin: 'manual',
      lineageStatus: 'unverified',
    }),
  ),
  tx(['depends', 'shared', 'root'], ['purge', 'root'], ['purge', 'shared']),
])
trace('independent-or-and-selective-invalidation', [
  tx(
    put('parent'),
    ['checkpoint'],
    put('or', [
      proof('bound', ['parent'], {
        parentReferences: [{ id: 'parent', version: 1, deletionEpoch: 0 }],
        subjectWitness: { marker: 'preserved' },
      }),
      proof('independent'),
    ]),
    put('and', [proof('and', ['parent', 'missing'])]),
    put('legacy', [proof('legacy', ['parent'])]),
  ),
  tx(['invalidate', 'parent', true], ['refresh']),
  tx(['invalidate', 'parent', false], ['refresh']),
  tx(['refresh']),
])
trace('refresh-shared-purge-and-vector-removal', [
  tx(
    put('parent'),
    put('child', [proof('child', ['parent'])]),
    put('unverified', [], { lineageStatus: 'unverified' }),
    [
      'raw',
      "INSERT INTO vector_spaces VALUES('space','{}');INSERT INTO memory_vectors VALUES('parent','space','embedding');INSERT INTO memory_vectors VALUES('child','space','embedding')",
    ],
  ),
  tx(['supports', 'parent', []], ['refresh']),
  tx(['refresh']),
])
trace('historical-missing-row-and-already-purged', [
  tx(put('root'), put('historical-only', [proof('h', ['root'])])),
  tx(
    ['raw', "DELETE FROM memories WHERE id='historical-only'"],
    ['purge', 'root', ['root']],
    ['purge', 'root'],
  ),
])
trace('ordinary-caught-nested-rollback', [
  tx(put('root')),
  tx(
    [
      'nested',
      [
        ['fault', 'projection'],
        ['purge', 'root'],
      ],
      true,
    ],
    ['fault', ''],
    put('accepted'),
  ),
])
trace('durable-intent-survives-application-failure', [
  tx(put('root')),
  tx(['supports', 'root', []], ['fault', 'beforeInvalidate'], ['refresh']),
])
trace('fresh-analysis-after-mutation', [
  tx(put('a'), put('b', [proof('b', ['a'])])),
  tx(['refresh'], ['supports', 'a', []], ['refresh']),
])
trace('cycle-failures-do-not-poison-or-alternatives', [
  tx(put('a'), put('b')),
  tx(
    ['supports', 'a', [proof('a', ['b'])]],
    ['supports', 'b', [proof('b-cycle', ['a']), proof('b-independent')]],
    ['refresh'],
  ),
])
trace('short-circuit-before-malformed-parent-tail', [
  tx(put('wanted'), put('child', [proof('child', ['wanted', null])])),
  tx(['depends', 'child', 'wanted'], ['invalidate', 'wanted', false], ['purge', 'wanted']),
])
trace('analyze-every-root-before-intents', [
  tx(put('budget'), put('unsupported')),
  tx(
    ['supports', 'unsupported', []],
    [
      'supports',
      'budget',
      [proof('budget', [], { sources: Array.from({ length: 4095 }, () => source('budget')) })],
    ],
    ['refresh'],
  ),
])
const paths = [
  'src/memory/lifecycle/proof-invalidation.ts',
  'src/memory/read/assertion-authority.ts',
  'src/memory/read/provenance.ts',
  'src/memory/read/bound-parents.ts',
  'src/memory/read/proof-parents.ts',
  'src/memory/storage/assertion-history.ts',
  'src/memory/storage/assertion-repository.ts',
  'src/memory/storage/migrations.ts',
  'src/memory/write/revision-coordinator.ts',
  'src/memory/domain/proofs.ts',
  'src/memory/domain/record-values.ts',
  'src/memory/recovery/eligibility.ts',
  'src/memory/recovery/ledger.ts',
]
const sources = Object.fromEntries(
  paths.map((p) => [p, createHash('sha256').update(readFileSync(p)).digest('hex')]),
)
const path = 'backend/internal/assertionlifecycle/testdata/proof-invalidation.json'
const raw = JSON.stringify({ node: process.version, sources, traces }, null, 2) + '\n'
if (process.argv.includes('--check')) assert.equal(readFileSync(path, 'utf8'), raw)
else writeFileSync(path, raw)
console.log(
  `${traces.length} real source lifecycle traces, ${traces.reduce((n, t) => n + t.steps.length, 0)} transitions`,
)
