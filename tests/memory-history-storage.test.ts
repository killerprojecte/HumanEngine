import assert from 'node:assert/strict'
import test from 'node:test'
import Database from 'better-sqlite3'
import { mkdtempSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import {
  AssertionHistory,
  type AppendAssertionRevision,
} from '../src/memory/storage/assertion-history.ts'
import type { MemoryRecord } from '../src/memory/domain/contracts.ts'

const source = { sceneId: 'scene-a', messageId: 'message-a', senderId: 'person-a' }
const record = (): MemoryRecord => ({
  id: 'assertion-a',
  scope: { kind: 'scene', sceneId: 'scene-a', personId: 'person-a' },
  content: 'Synthetic preference',
  kind: 'claim',
  sources: [source],
  importance: 'medium',
  confidence: 0.6,
  learnedAt: '2026-01-02T00:00:00.000Z',
  occurredAt: '2025-12-01T00:00:00.000Z',
  status: 'active',
  locked: false,
  origin: 'learned',
  lineageStatus: 'unverified',
})
const input = (patch: Partial<AppendAssertionRevision> = {}): AppendAssertionRevision => ({
  record: record(),
  supports: [
    { sources: [source], parents: ['parent-a'] },
    { sources: [{ ...source, messageId: 'independent-b' }], parents: [] },
  ],
  expectedVersion: 0,
  expectedDeletionEpoch: 0,
  idempotencyKey: 'command-a',
  recordedAt: '2026-01-02T00:00:00.000Z',
  validFrom: null,
  validTo: null,
  eventKind: 'learned_assertion',
  ...patch,
})
function setup() {
  const db = new Database(':memory:')
  db.pragma('foreign_keys=ON')
  return { db, history: new AssertionHistory(db) }
}

test('history appends only within caller transaction, preserves proof alternatives and rolls back atomically', () => {
  const { db, history } = setup()
  try {
    assert.throws(() => history.append(input()), /assertion_transaction_required/)
    const first = db.transaction(() => history.append(input()))()
    assert.deepEqual(first.supports, input().supports)
    assert.equal(first.record.lineageStatus, 'unverified')
    assert.equal(first.validFrom, null)
    first.record.content = 'caller mutation'
    assert.equal(
      history.revisions('assertion-a', 'scene:scene-a')[0]!.record.content,
      'Synthetic preference',
    )
    assert.deepEqual(history.revisions('assertion-a', 'scene:foreign'), [])
    assert.throws(
      () =>
        db.transaction(() => {
          history.append(
            input({
              expectedVersion: 1,
              idempotencyKey: 'rollback',
              record: { ...record(), content: 'ROLLBACK_CANARY' },
            }),
          )
          throw new Error('rollback')
        })(),
      /rollback/,
    )
    assert.equal(history.state('assertion-a').version, 1)
    assert.equal(history.revisions('assertion-a', 'scene:scene-a').length, 1)
  } finally {
    db.close()
  }
})

test('idempotent retries preserve version, conflicts and stale versions fail rather than overwrite', () => {
  const { db, history } = setup()
  try {
    db.transaction(() => history.append(input()))()
    assert.equal(db.transaction(() => history.append(input()))().version, 1)
    assert.throws(
      () =>
        db.transaction(() =>
          history.append(input({ record: { ...record(), content: 'different' } })),
        )(),
      /idempotency_conflict/,
    )
    assert.throws(
      () => db.transaction(() => history.append(input({ idempotencyKey: 'new' })))(),
      /version_conflict/,
    )
    const second = db.transaction(() =>
      history.append(
        input({
          expectedVersion: 1,
          idempotencyKey: 'new',
          recordedAt: '2026-01-03T00:00:00.000Z',
          validFrom: '2026-01-04T00:00:00.000Z',
          record: { ...record(), content: 'Corrected preference' },
        }),
      ),
    )()
    assert.equal(second.version, 2)
    assert.equal(
      history.revisions('assertion-a', 'scene:scene-a')[0]!.record.content,
      'Synthetic preference',
    )
    assert.equal(second.validFrom, '2026-01-04T00:00:00.000Z')
    assert.throws(
      () =>
        db.transaction(() =>
          history.append(
            input({
              expectedVersion: 2,
              idempotencyKey: 'foreign',
              record: { ...record(), scope: { kind: 'scene', sceneId: 'foreign' } },
            }),
          ),
        )(),
      /scope_conflict/,
    )
    assert.throws(
      () =>
        db.transaction(() => history.append(input({ validFrom: '2026-02-30T00:00:00.000Z' })))(),
      /invalid_assertion_revision/,
    )
  } finally {
    db.close()
  }
})

test('source redaction purges historical bodies and stales captured epochs even with alternative proof', () => {
  const { db, history } = setup()
  try {
    db.transaction(() =>
      history.append(input({ record: { ...record(), content: 'REVOKED_BODY_CANARY' } })),
    )()
    assert.deepEqual(db.transaction(() => history.redactSource('scene-a', 'message-a'))(), [
      'assertion-a',
    ])
    assert.deepEqual(history.revisions('assertion-a', 'scene:scene-a'), [])
    assert.equal(history.state('assertion-a').version, 1)
    assert.equal(history.state('assertion-a').deletionEpoch, 1)
    assert.equal(history.state('assertion-a').deleted, false)
    assert.equal(
      JSON.stringify(db.prepare('SELECT * FROM memory_assertion_revisions').all()).includes(
        'REVOKED_BODY_CANARY',
      ),
      false,
    )
    assert.throws(
      () => db.transaction(() => history.append(input({ expectedVersion: 1 })))(),
      /stale_epoch/,
    )
    const surviving = { ...record(), sources: [{ ...source, messageId: 'independent-b' }] }
    const next = db.transaction(() =>
      history.append(
        input({
          record: surviving,
          supports: [{ sources: surviving.sources, parents: [] }],
          expectedVersion: 1,
          expectedDeletionEpoch: 1,
          idempotencyKey: 'surviving',
        }),
      ),
    )()
    assert.equal(next.version, 2)
    assert.deepEqual(db.transaction(() => history.redactSource('scene-a', 'message-a'))(), [])
  } finally {
    db.close()
  }
})

test('terminal deletion erases all bodies and prevents import, retry or fresh command from resurrecting ID', () => {
  const { db, history } = setup()
  try {
    db.transaction(() => history.append(input()))()
    const erased = db.transaction(() => history.erase('assertion-a'))()
    assert.deepEqual(erased, { version: 1, deletionEpoch: 1, deleted: true })
    assert.deepEqual(db.transaction(() => history.erase('assertion-a'))(), erased)
    assert.deepEqual(db.prepare('SELECT * FROM memory_assertion_revisions').all(), [])
    assert.deepEqual(db.prepare('SELECT * FROM memory_assertion_revision_sources').all(), [])
    assert.throws(() => db.transaction(() => history.importLegacy(record(), []))(), /stale_epoch/)
    assert.throws(
      () =>
        db.transaction(() =>
          history.append(input({ expectedVersion: 1, expectedDeletionEpoch: 1 })),
        )(),
      /stale_epoch/,
    )
  } finally {
    db.close()
  }
})

test('legacy manual migration and reopen are idempotent; missing identity/time is never fabricated', () => {
  const root = mkdtempSync(join(tmpdir(), 'he-history-'))
  const path = join(root, 'fixture.sqlite')
  let db = new Database(path)
  try {
    const manual = {
      ...record(),
      origin: 'manual' as const,
      sources: [],
      learnedAt: 'legacy-invalid-date',
    }
    let history = new AssertionHistory(db)
    db.transaction(() => history.importLegacy(manual, []))()
    const imported = history.revisions(manual.id, 'scene:scene-a')[0]!
    assert.equal(imported.recordedAt, null)
    assert.equal(imported.validFrom, null)
    assert.deepEqual(imported.event, { kind: 'admin_assertion', actorId: null, legacyImport: true })
    assert.deepEqual(imported.record.sources, [])
    db.close()
    db = new Database(path)
    history = new AssertionHistory(db)
    db.transaction(() => history.importLegacy(manual, []))()
    assert.deepEqual(history.revisions(manual.id, 'scene:scene-a'), [imported])
    assert.equal(history.state(manual.id).version, 1)
  } finally {
    db.close()
    rmSync(root, { recursive: true, force: true })
  }
})

test('scope transitions require an explicit prior purge and cannot expose private historical bodies', () => {
  const { db, history } = setup()
  try {
    db.transaction(() =>
      history.append(input({ record: { ...record(), content: 'PRIVATE_HISTORY_CANARY' } })),
    )()
    const promoted = input({
      record: {
        ...record(),
        scope: { kind: 'sharedPublicKnowledge' },
        content: 'Reviewed public knowledge',
      },
      expectedVersion: 1,
      idempotencyKey: 'authorized-promotion',
    })
    assert.throws(() => db.transaction(() => history.append(promoted))(), /scope_conflict/)
    const next = db.transaction(() => {
      history.purge('assertion-a')
      return history.append({ ...promoted, expectedDeletionEpoch: 1 })
    })()
    assert.equal(next.version, 2)
    assert.deepEqual(history.revisions('assertion-a', 'scene:scene-a'), [])
    assert.equal(
      history.revisions('assertion-a', 'shared:public')[0]!.record.content,
      'Reviewed public knowledge',
    )
    assert.equal(
      JSON.stringify(db.prepare('SELECT * FROM memory_assertion_revisions').all()).includes(
        'PRIVATE_HISTORY_CANARY',
      ),
      false,
    )
    const empty = db.transaction(() => history.purge('not-yet-created'))()
    assert.deepEqual(empty, { version: 0, deletionEpoch: 1, deleted: false })
    assert.throws(
      () =>
        db.transaction(() =>
          history.append(input({ record: { ...record(), id: 'not-yet-created' } })),
        )(),
      /stale_epoch/,
    )
  } finally {
    db.close()
  }
})

test('historical parent references survive a safe current rewrite until history is purged', () => {
  const { db, history } = setup()
  try {
    db.transaction(() => history.append(input()))()
    db.transaction(() =>
      history.append(
        input({
          expectedVersion: 1,
          idempotencyKey: 'safe-rewrite',
          supports: [],
          record: {
            ...record(),
            content: 'Independent manual replacement',
            sources: [],
            origin: 'manual',
          },
          eventKind: 'admin_assertion',
        }),
      ),
    )()
    assert.deepEqual(history.dependents('parent-a'), ['assertion-a'])
    assert.deepEqual(history.dependents('foreign-parent'), [])
    // Simulate a database saved before the additive parent-index migration.
    db.exec('DROP TABLE memory_assertion_revision_parents')
    const reopened = new AssertionHistory(db)
    assert.deepEqual(reopened.dependents('parent-a'), ['assertion-a'])
    assert.equal(
      (
        db.prepare('SELECT COUNT(*) AS n FROM memory_assertion_revision_parents').get() as {
          n: number
        }
      ).n,
      1,
    )
    new AssertionHistory(db)
    assert.equal(
      (
        db.prepare('SELECT COUNT(*) AS n FROM memory_assertion_revision_parents').get() as {
          n: number
        }
      ).n,
      1,
    )
    db.transaction(() => reopened.purge('assertion-a'))()
    assert.deepEqual(reopened.dependents('parent-a'), [])
    assert.deepEqual(reopened.revisions('assertion-a', 'scene:scene-a'), [])
    db.transaction(() =>
      reopened.append(
        input({ expectedVersion: 2, expectedDeletionEpoch: 1, idempotencyKey: 'post-purge' }),
      ),
    )()
    assert.deepEqual(reopened.dependents('parent-a'), ['assertion-a'])
    db.transaction(() => reopened.erase('assertion-a'))()
    assert.deepEqual(reopened.dependents('parent-a'), [])
    assert.deepEqual(db.prepare('SELECT * FROM memory_assertion_revision_parents').all(), [])
  } finally {
    db.close()
  }
})
