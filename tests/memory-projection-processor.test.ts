import assert from 'node:assert/strict'
import test from 'node:test'
import Database from 'better-sqlite3'
import { AssertionHistory } from '../src/memory/storage/assertion-history.ts'
import { ProjectionOutbox } from '../src/memory/storage/projection-outbox.ts'
import { MemoryProjectionProcessor } from '../src/memory/projections/processor.ts'
import type { MemoryRecord } from '../src/memory/domain/contracts.ts'

function fixture() {
  const db = new Database(':memory:')
  const history = new AssertionHistory(db)
  let now = 1000
  const outbox = new ProjectionOutbox(db, () => now)
  const records = new Map<string, MemoryRecord>()
  const processor = new MemoryProjectionProcessor(
    db,
    outbox,
    (id) => records.get(id),
    () => now,
  )
  const add = (
    id: string,
    scope: MemoryRecord['scope'] = { kind: 'scene', sceneId: 'a', personId: 'same-person-name-a' },
    future = false,
  ) => {
    const record: MemoryRecord = {
      id,
      scope,
      content: `SYNTHETIC_CONTENT_${id}`,
      sources: [],
      kind: 'fact',
      importance: 'medium',
      confidence: 1,
      learnedAt: '2026-01-01T00:00:00.000Z',
      origin: 'manual',
      status: 'active',
      locked: false,
      ...(future ? { validFrom: '2099-01-01T00:00:00.000Z' } : {}),
    }
    records.set(id, record)
    db.transaction(() => {
      const revision = history.append({
        record,
        supports: [],
        expectedVersion: 0,
        expectedDeletionEpoch: 0,
        idempotencyKey: 'create',
        recordedAt: record.learnedAt,
        validFrom: record.validFrom ?? null,
        validTo: null,
        eventKind: 'admin_assertion',
      })
      for (const kind of ['lexical', 'person'] as const)
        outbox.enqueue({ assertionId: id, version: revision.version, deletionEpoch: 0, kind })
    })()
    return record
  }
  return {
    db,
    history,
    outbox,
    processor,
    records,
    add,
    advance: () => {
      now += 40_000
    },
  }
}

test('bounded shadow projection builds FTS and body-free scoped person markers, including future facts', () => {
  const f = fixture()
  try {
    f.add('a')
    f.add('b', { kind: 'scene', sceneId: 'b', personId: 'same-person-name-b' }, true)
    f.add('shared', { kind: 'sharedPublicKnowledge' })
    assert.deepEqual(f.processor.drain(1), { claimed: 1, completed: 1, invalid: 0, failed: 0 })
    assert.deepEqual(f.processor.drain(16), { claimed: 5, completed: 5, invalid: 0, failed: 0 })
    assert.equal((f.db.prepare('SELECT count(*) AS n FROM memory_fts').get() as { n: number }).n, 3)
    const markers = f.db
      .prepare('SELECT * FROM memory_person_projection_state ORDER BY assertion_id')
      .all() as Array<{ scope_key: string; person_id: string | null }>
    assert.deepEqual(
      markers.map((x) => [x.scope_key, x.person_id]),
      [
        ['scene:a', 'same-person-name-a'],
        ['scene:b', 'same-person-name-b'],
        ['shared:public', null],
      ],
    )
    assert.equal(JSON.stringify(markers).includes('SYNTHETIC_CONTENT'), false)
    assert.deepEqual(f.processor.drain(), { claimed: 0, completed: 0, invalid: 0, failed: 0 })
    assert.throws(() => f.processor.drain(65), /invalid_projection_drain_limit/)
  } finally {
    f.db.close()
  }
})

test('source eligibility is rechecked inside completion and failure cannot undo committed authority', () => {
  const f = fixture()
  try {
    f.add('a')
    f.records.delete('a')
    assert.deepEqual(f.processor.drain(), { claimed: 2, completed: 0, invalid: 2, failed: 0 })
    assert.equal(f.history.state('a').version, 1)
    assert.deepEqual(f.db.prepare('SELECT * FROM memory_fts').all(), [])
    f.add('b')
    const throwing = new MemoryProjectionProcessor(f.db, f.outbox, () => {
      throw new Error('PRIVATE_ERROR_CANARY')
    })
    assert.deepEqual(throwing.drain(), { claimed: 2, completed: 0, invalid: 0, failed: 2 })
    assert.equal(f.history.state('b').version, 1)
    assert.equal(
      JSON.stringify(f.db.prepare('SELECT * FROM memory_projection_outbox').all()).includes(
        'PRIVATE_ERROR_CANARY',
      ),
      false,
    )
    f.advance()
    assert.equal(f.processor.drain().completed, 2)
  } finally {
    f.db.close()
  }
})

test('deletion clears shadow bodies and markers atomically; late epochs cannot recreate them', () => {
  const f = fixture()
  try {
    f.add('a')
    f.processor.drain()
    assert.throws(() => f.processor.remove('a'), /transaction_required/)
    assert.throws(
      () =>
        f.db.transaction(() => {
          f.processor.remove('a')
          throw new Error('rollback')
        })(),
      /rollback/,
    )
    assert.equal((f.db.prepare('SELECT count(*) AS n FROM memory_fts').get() as { n: number }).n, 1)
    f.db.transaction(() => {
      f.history.erase('a')
      f.records.delete('a')
      f.processor.remove('a')
    })()
    assert.deepEqual(f.db.prepare('SELECT * FROM memory_fts').all(), [])
    assert.deepEqual(f.db.prepare('SELECT * FROM memory_person_projection_state').all(), [])
    assert.equal(f.processor.drain().completed, 0)
    f.add('b')
    f.db.transaction(() => f.history.purge('b'))()
    assert.equal(f.processor.drain().completed, 0)
    assert.deepEqual(f.db.prepare('SELECT * FROM memory_fts').all(), [])
  } finally {
    f.db.close()
  }
})

test('constructor never enumerates old records or restarts learned/model work', () => {
  const f = fixture()
  try {
    let reads = 0
    const processor = new MemoryProjectionProcessor(f.db, f.outbox, () => {
      reads++
      throw new Error('unexpected')
    })
    assert.equal(reads, 0)
    assert.equal(processor.drain().claimed, 0)
    assert.equal(reads, 0)
    f.add('committed')
    const reopened = new MemoryProjectionProcessor(
      f.db,
      new ProjectionOutbox(f.db, () => 1000),
      (id) => f.records.get(id),
      () => 1000,
    )
    assert.equal(reopened.drain().completed, 2)
    assert.equal(reopened.drain().completed, 0)
  } finally {
    f.db.close()
  }
})
