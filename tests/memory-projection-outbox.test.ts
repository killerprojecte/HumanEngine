import assert from 'node:assert/strict'
import test from 'node:test'
import Database from 'better-sqlite3'
import { mkdtempSync, rmSync } from 'node:fs'
import { join } from 'node:path'
import { tmpdir } from 'node:os'
import { AssertionHistory } from '../src/memory/storage/assertion-history.ts'
import {
  ProjectionOutbox,
  type ProjectionSnapshot,
} from '../src/memory/storage/projection-outbox.ts'
import type { MemoryRecord } from '../src/memory/domain/contracts.ts'

function fixture(path = ':memory:') {
  const db = new Database(path)
  const history = new AssertionHistory(db)
  let clock = 10_000
  const outbox = new ProjectionOutbox(db, () => clock)
  db.exec('CREATE TABLE IF NOT EXISTS fixture_projection (id TEXT PRIMARY KEY,value TEXT)')
  const record: MemoryRecord = {
    id: 'a',
    scope: { kind: 'scene', sceneId: 'scene-a', personId: 'person-a' },
    content: 'PRIVATE_MEMORY_BODY_CANARY',
    kind: 'fact',
    sources: [],
    confidence: 1,
    importance: 'medium',
    learnedAt: '2026-01-01T00:00:00.000Z',
    origin: 'manual',
    locked: false,
    status: 'active',
  }
  const snapshot: ProjectionSnapshot = {
    assertionId: 'a',
    version: 1,
    deletionEpoch: 0,
    kind: 'person',
  }
  const append = () =>
    history.append({
      record,
      supports: [],
      expectedVersion: 0,
      expectedDeletionEpoch: 0,
      idempotencyKey: 'create',
      recordedAt: record.learnedAt,
      validFrom: null,
      validTo: null,
      eventKind: 'admin_assertion',
    })
  const commit = () =>
    db.transaction(() => {
      append()
      return outbox.enqueue(snapshot)
    })()
  return {
    db,
    history,
    outbox,
    snapshot,
    append,
    commit,
    advance: (ms: number) => {
      clock += ms
    },
  }
}

test('projection enqueue shares the assertion transaction, persists no body and has exact idempotency', () => {
  const f = fixture()
  try {
    assert.throws(() => f.outbox.enqueue(f.snapshot), /transaction_required/)
    assert.throws(() => f.db.transaction(() => f.outbox.enqueue(f.snapshot))(), /snapshot_stale/)
    assert.throws(
      () =>
        f.db.transaction(() => {
          f.append()
          f.outbox.enqueue(f.snapshot)
          throw new Error('rollback')
        })(),
      /rollback/,
    )
    assert.equal(f.history.state('a').version, 0)
    assert.equal(
      (f.db.prepare('SELECT count(*) AS n FROM memory_projection_outbox').get() as { n: number }).n,
      0,
    )
    const id = f.commit()
    assert.equal(f.db.transaction(() => f.outbox.enqueue(f.snapshot))(), id)
    assert.equal(
      JSON.stringify(f.db.prepare('SELECT * FROM memory_projection_outbox').all()).includes(
        'PRIVATE_MEMORY_BODY_CANARY',
      ),
      false,
    )
    assert.throws(
      () =>
        f.db.transaction(() =>
          f.outbox.enqueue({ ...f.snapshot, kind: 'embedding' as 'person' }),
        )(),
      /invalid_projection_snapshot/,
    )
    const lease = f.outbox.claim({ leaseMs: 100 })[0]!
    assert.equal(
      f.outbox.complete(lease, () => {
        f.db.prepare('INSERT INTO fixture_projection VALUES(?,?)').run('a', 'derived')
      }),
      true,
    )
    assert.deepEqual(f.outbox.claim({ leaseMs: 100 }), [])
    assert.equal(f.db.transaction(() => f.outbox.enqueue(f.snapshot))(), id)
    assert.deepEqual(f.outbox.claim({ leaseMs: 100 }), [])
  } finally {
    f.db.close()
  }
})

test('expired leases cannot commit after a different worker claims the job', () => {
  const f = fixture()
  try {
    f.commit()
    const old = f.outbox.claim({ leaseMs: 100 })[0]!
    f.advance(100)
    const current = f.outbox.claim({ leaseMs: 100 })[0]!
    assert.notEqual(old.leaseToken, current.leaseToken)
    assert.equal(current.attempts, 2)
    assert.equal(
      f.outbox.complete(old, () => {
        assert.fail('stale callback executed')
      }),
      false,
    )
    assert.equal(f.outbox.fail(old, 'projection_failed'), false)
    assert.equal(
      f.outbox.complete(current, () => {}),
      true,
    )
    assert.equal(
      f.outbox.complete(current, () => {
        assert.fail('completed callback replayed')
      }),
      false,
    )
  } finally {
    f.db.close()
  }
})

test('delete/epoch/version changes and explicit invalidation prevent late projection writes', () => {
  for (const change of ['delete', 'epoch', 'version', 'invalidate'] as const) {
    const f = fixture()
    try {
      f.commit()
      const lease = f.outbox.claim({ leaseMs: 100 })[0]!
      f.db.transaction(() => {
        if (change === 'delete') f.history.erase('a')
        if (change === 'epoch') f.history.purge('a')
        if (change === 'version')
          f.db
            .prepare('UPDATE memory_assertion_state SET version=version+1 WHERE assertion_id=?')
            .run('a')
        if (change === 'invalidate') f.outbox.invalidate('a')
      })()
      assert.equal(
        f.outbox.complete(lease, () => {
          assert.fail('invalid projection write')
        }),
        false,
      )
      assert.deepEqual(f.outbox.claim({ leaseMs: 100 }), [])
    } finally {
      f.db.close()
    }
  }
})

test('async callbacks are rejected before invocation; thrown/thenable writes roll back', () => {
  const f = fixture()
  try {
    f.commit()
    const lease = f.outbox.claim({ leaseMs: 100 })[0]!
    let invoked = false
    assert.throws(
      () =>
        f.outbox.complete(lease, async () => {
          invoked = true
        }),
      /must_be_sync/,
    )
    assert.equal(invoked, false)
    assert.throws(
      () =>
        f.outbox.complete(lease, () => {
          f.db.prepare('INSERT INTO fixture_projection VALUES(?,?)').run('a', 'should rollback')
          throw new Error('write failed')
        }),
      /write failed/,
    )
    assert.deepEqual(f.db.prepare('SELECT * FROM fixture_projection').all(), [])
    assert.throws(
      () =>
        f.outbox.complete(lease, () => {
          f.db.prepare('INSERT INTO fixture_projection VALUES(?,?)').run('a', 'thenable rollback')
          return Promise.resolve()
        }),
      /must_be_sync/,
    )
    assert.deepEqual(f.db.prepare('SELECT * FROM fixture_projection').all(), [])
    assert.throws(
      () =>
        f.outbox.complete(lease, () => {
          f.history.purge('a')
        }),
      /changed_during_commit/,
    )
    assert.equal(f.history.state('a').deletionEpoch, 0)
    assert.equal(
      f.outbox.complete(lease, () => {}),
      true,
    )
  } finally {
    f.db.close()
  }
})

test('restart recovers committed deterministic projections only, with bounded retries and no timers', () => {
  const root = mkdtempSync(join(tmpdir(), 'he-projection-outbox-'))
  const path = join(root, 'fixture.sqlite')
  let f = fixture(path)
  try {
    f.commit()
    f.db.close()
    f = fixture(path)
    assert.equal(
      (f.db.prepare('SELECT count(*) AS n FROM memory_projection_outbox').get() as { n: number }).n,
      1,
    )
    for (let attempt = 1; attempt <= 3; attempt++) {
      const lease = f.outbox.claim({ leaseMs: 100 })[0]!
      assert.equal(lease.attempts, attempt)
      assert.equal(f.outbox.fail(lease, 'projection_failed'), true)
      assert.deepEqual(f.outbox.claim({ leaseMs: 100 }), [])
      f.advance(30_000)
    }
    assert.deepEqual(f.outbox.claim({ leaseMs: 100 }), [])
    assert.equal(
      (f.db.prepare('SELECT state FROM memory_projection_outbox').get() as { state: string }).state,
      'failed',
    )
  } finally {
    f.db.close()
    rmSync(root, { recursive: true, force: true })
  }
})

test('an expired worker crash cannot create unbounded retries; unavailable sources are terminal', () => {
  for (const unavailable of [true, false]) {
    const f = fixture()
    try {
      f.commit()
      for (let attempt = 1; attempt <= (unavailable ? 1 : 3); attempt++) {
        const lease = f.outbox.claim({ leaseMs: 10 })[0]!
        if (unavailable) assert.equal(f.outbox.fail(lease, 'source_unavailable'), true)
        f.advance(11)
      }
      assert.deepEqual(f.outbox.claim({ leaseMs: 10 }), [])
    } finally {
      f.db.close()
    }
  }
})

test('bounded terminal pruning preserves current idempotency and never removes pending or leased jobs', () => {
  const f = fixture()
  try {
    const id = f.commit()
    const lease = f.outbox.claim({ leaseMs: 100 })[0]!
    f.outbox.complete(lease, () => {})
    const insert = f.db.prepare(`INSERT INTO memory_projection_outbox
      (assertion_id,version,deletion_epoch,kind,state,available_at,updated_at)
      VALUES(?,?,0,'person',?,0,0)`)
    f.db.transaction(() => {
      for (let version = 1; version <= 4100; version++)
        insert.run('obsolete', version, ['done', 'invalid', 'failed'][version % 3])
      insert.run('uncommitted-work', 1, 'pending')
      insert.run('uncommitted-work', 2, 'leased')
    })()
    assert.equal(f.outbox.pruneTerminal(2), 2)
    assert.equal(f.outbox.pruneTerminal(2), 2)
    assert.equal(f.outbox.pruneTerminal(2), 0)
    assert.equal(
      (
        f.db
          .prepare(
            "SELECT count(*) AS n FROM memory_projection_outbox WHERE assertion_id='obsolete'",
          )
          .get() as { n: number }
      ).n,
      4096,
    )
    assert.equal(
      (
        f.db
          .prepare(
            "SELECT count(*) AS n FROM memory_projection_outbox WHERE state IN ('pending','leased')",
          )
          .get() as { n: number }
      ).n,
      2,
    )
    assert.equal(f.db.transaction(() => f.outbox.enqueue(f.snapshot))(), id)
    assert.equal(
      (
        f.db.prepare('SELECT state FROM memory_projection_outbox WHERE id=?').get(id) as {
          state: string
        }
      ).state,
      'done',
    )
    assert.throws(() => f.outbox.pruneTerminal(1001), /invalid_projection_prune_limit/)
  } finally {
    f.db.close()
  }
})
