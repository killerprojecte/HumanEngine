/** Real WAL main + independent authority oracle for assertion transaction failures. */
import assert from 'node:assert/strict'
import Database from 'better-sqlite3'
import { mkdtempSync, rmSync, readFileSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { mock } from 'node:test'
import { createHash } from 'node:crypto'
import { initializeMemoryStorage } from '../../src/memory/storage/migrations.ts'
import { AssertionRepository } from '../../src/memory/storage/assertion-repository.ts'
import { MemoryRevisionCoordinator } from '../../src/memory/write/revision-coordinator.ts'
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
const record = {
  id: 'assertion',
  scope: { kind: 'scene', sceneId: 'scene' },
  content: 'Synthetic authority fixture',
  kind: 'claim',
  sources: [],
  importance: 'medium',
  confidence: 0.6,
  learnedAt: '2026-01-02T00:00:00.000Z',
  status: 'active',
  locked: false,
  origin: 'manual',
  lineageStatus: 'verified',
}
const cases = []
for (const stage of ['after-intent', 'after-commit', 'incarnation', 'nested-caught']) {
  const root = mkdtempSync(join(process.cwd(), 'backend/.assertion-recovery-oracle-'))
  let db, authority, external
  try {
    db = new Database(join(root, 'main.sqlite'))
    db.pragma('journal_mode=WAL')
    authority = new RecoveryAuthority(
      join(root, 'authority'),
      '12345678-1234-4123-8123-123456789012',
    )
    saveGate(db, {
      head: authority.head(),
      authority: authority.path,
      minimumReader: 1,
      state: 'ready',
    })
    bindRecovery(db, authority)
    initializeMemoryStorage({ db })
    let repo,
      armed = false
    const revisions = new MemoryRevisionCoordinator(
      db,
      (id) => repo.get(id),
      (id) => repo.supports(id),
      () => true,
      undefined,
      () => {
        if (armed && stage === 'after-commit') throw Error('synthetic_after_commit')
      },
      (changes) => {
        recordAssertionEligibilityChanges(db, changes)
        if (armed && stage === 'after-intent') throw Error('synthetic_after_intent')
      },
    )
    repo = new AssertionRepository(
      {
        db,
        assertRecoveryReady() {
          assertRecoveryReady(db)
        },
      },
      {
        beforeWrite: (id) => revisions.beforeWrite(id),
        afterWrite: (r) => revisions.afterWrite(r),
        removeProjection() {},
      },
    )
    revisions.transaction(() => {
      repo.write(record)
      repo.saveSupports(record.id, [])
    })()
    armed = true
    let error = null
    try {
      revisions.transaction(() => {
        if (stage === 'nested-caught') {
          try {
            revisions.transaction(() => {
              repo.write({ ...record, status: 'invalidated' })
              revisions.checkpoint()
              throw Error('nested_rollback')
            })()
          } catch (e) {
            assert.equal(e.message, 'nested_rollback')
          }
          repo.write({ ...record, content: 'accepted outer' })
          return
        }
        repo.write({
          ...record,
          status: 'invalidated',
          ...(stage === 'incarnation' ? { learnedAt: '2026-02-01T00:00:00.000Z' } : {}),
        })
      })()
    } catch (e) {
      error = e.message
    }
    external = new Database(join(root, 'authority', 'authority.sqlite'))
    const events = external
      .prepare('SELECT payload FROM revocations ORDER BY sequence')
      .all()
      .map((r) => {
        const v = JSON.parse(r.payload)
        if (v.kind === 'assertion_eligibility') delete v.operationId
        return v
      })
    let readyError = null
    try {
      assertRecoveryReady(db)
    } catch (e) {
      readyError = e.message
    }
    cases.push({
      stage,
      error,
      readyError,
      gateSequence: metadata(db).head.sequence,
      minimumReader: metadata(db).minimumReader,
      authoritySequence: authority.head().sequence,
      record: db.prepare('SELECT value FROM memories WHERE id=?').get(record.id).value,
      state: db
        .prepare(
          'SELECT version,deletion_epoch,deleted FROM memory_assertion_state WHERE assertion_id=?',
        )
        .get(record.id),
      events: JSON.stringify(events),
    })
  } finally {
    external?.close()
    db?.close()
    authority?.close()
    rmSync(root, { recursive: true, force: true })
  }
}
const paths = [
  'src/memory/storage/assertion-history.ts',
  'src/memory/storage/assertion-repository.ts',
  'src/memory/write/revision-coordinator.ts',
  'src/memory/recovery/eligibility.ts',
  'src/memory/recovery/ledger.ts',
]
const sources = Object.fromEntries(
  paths.map((p) => [p, createHash('sha256').update(readFileSync(p)).digest('hex')]),
)
const path = 'backend/internal/assertions/testdata/recovery.json',
  raw = JSON.stringify({ node: process.version, sources, cases }, null, 2) + '\n'
if (process.argv.includes('--check')) assert.equal(readFileSync(path, 'utf8'), raw)
else writeFileSync(path, raw)
console.log(`${cases.length} real dual-database assertion fault cases`)
