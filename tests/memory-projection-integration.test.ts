import assert from 'node:assert/strict'
import test from 'node:test'
import { mkdtempSync, rmSync } from 'node:fs'
import { join } from 'node:path'
import { tmpdir } from 'node:os'
import { Store } from '../src/store.ts'
import { MemoryService } from '../src/memory.ts'
import { ProjectionOutbox } from '../src/memory/storage/projection-outbox.ts'

const input = (content = 'SYNTHETIC_ALPHA') => ({
  scope: { kind: 'scene' as const, sceneId: 'a', personId: 'alice' },
  kind: 'fact' as const,
  content,
})
const enable = (store: Store) =>
  store.set('settings', { ...store.settings, memoryProjectionEnabled: true })
const fts = (store: Store) =>
  store.db
    .prepare(
      'SELECT assertion_id,scope_key,person_id,content FROM memory_fts ORDER BY assertion_id',
    )
    .all() as Array<{
    assertion_id: string
    scope_key: string
    person_id: string | null
    content: string
  }>
const jobs = (store: Store) => store.db.prepare('SELECT * FROM memory_projection_outbox').all()
const pendingAtCommit = (memory: MemoryService) => {
  // Emulate the process ending between durable commit and its best-effort postcommit drain.
  memory.flushProjections = () => undefined
}

test('real facade defaults OFF, never scans old records, and indexes only new ON commits', () => {
  const store = new Store(':memory:')
  try {
    const memory = new MemoryService(store)
    assert.equal(store.settings.memoryProjectionEnabled, false)
    const old = memory.create(input('SYNTHETIC_OLD'))
    assert.deepEqual(jobs(store), [])
    assert.deepEqual(fts(store), [])
    enable(store)
    memory.flushProjections()
    assert.deepEqual(jobs(store), [])
    const fresh = memory.create({
      ...input('SYNTHETIC_FUTURE'),
      validFrom: '2099-01-01T00:00:00.000Z',
    })
    assert.equal(fts(store).length, 1)
    assert.equal(fts(store)[0]!.assertion_id, fresh.id)
    assert.equal(fts(store)[0]!.content, 'SYNTHETIC_FUTURE')
    assert.equal(
      memory.list({ sceneId: 'a', simulation: false }).some((row) => row.id === fresh.id),
      false,
    )
    assert.equal(
      memory.list({ sceneId: 'a', simulation: false }).some((row) => row.id === old.id),
      true,
    )
    assert.deepEqual(memory.list({ sceneId: 'foreign', simulation: false }), [])
    const marker = store.db.prepare('SELECT * FROM memory_person_projection_state').get()
    assert.equal(JSON.stringify(marker).includes('SYNTHETIC_FUTURE'), false)
  } finally {
    store.close()
  }
})

test('real facade keeps same-version projections on no-op update and lock, updates changed content and deletes immediately', () => {
  const store = new Store(':memory:')
  try {
    enable(store)
    const memory = new MemoryService(store)
    const record = memory.create(input())
    assert.equal(fts(store)[0]!.content, 'SYNTHETIC_ALPHA')
    memory.lock(record.id, false)
    assert.equal(
      fts(store)[0]?.content,
      'SYNTHETIC_ALPHA',
      'no-op lock must not erase a done projection',
    )
    memory.update(record.id, { content: 'SYNTHETIC_ALPHA' })
    assert.equal(
      fts(store)[0]?.content,
      'SYNTHETIC_ALPHA',
      'same-content review must keep/rebuild projection',
    )
    memory.update(record.id, { content: 'SYNTHETIC_BETA' })
    assert.deepEqual(
      fts(store).map((row) => row.content),
      ['SYNTHETIC_BETA'],
    )
    memory.delete(record.id)
    assert.deepEqual(fts(store), [])
    assert.deepEqual(store.db.prepare('SELECT * FROM memory_person_projection_state').all(), [])
    memory.flushProjections()
    assert.deepEqual(fts(store), [])
  } finally {
    store.close()
  }
})

test('recall invalidates source-bound projections; captured stale jobs cannot resurrect removed content', () => {
  const store = new Store(':memory:')
  try {
    enable(store)
    store.saveScene({
      id: 'a',
      kind: 'group',
      adapterId: 'fixture',
      accountId: 'account',
      platformSceneId: 'a',
      name: 'Synthetic scene',
      participationEnabled: false,
      learningEnabled: true,
      proactiveEnabled: false,
    })
    const memory = new MemoryService(store)
    const source = { sceneId: 'a', messageId: 'm1', senderId: 'alice' }
    const saved = memory.propose(
      {
        sceneId: 'a',
        simulation: false,
        provenance: 'source-only',
        sourceEvidence: [{ ...source, available: true, isBot: false }],
      },
      { ...input('SYNTHETIC_RECALLED'), personId: 'alice', sources: [source] },
    )
    assert.equal(saved.status, 'saved')
    if (saved.status !== 'saved') throw Error('fixture')
    assert.equal(fts(store).length, 1)
    memory.recall('a', 'm1')
    assert.deepEqual(fts(store), [])
    assert.deepEqual(store.db.prepare('SELECT * FROM memory_person_projection_state').all(), [])
    memory.flushProjections()
    assert.deepEqual(fts(store), [])
    pendingAtCommit(memory)
    const pending = memory.create(input('SYNTHETIC_PENDING_DELETE'))
    const outbox = new ProjectionOutbox(store.db)
    const lease = outbox.claim({ leaseMs: 30000 })[0]!
    assert.equal(lease.assertionId, pending.id)
    memory.delete(pending.id)
    assert.equal(
      outbox.complete(lease, () => {
        assert.fail('deleted lease callback executed')
      }),
      false,
    )
    assert.deepEqual(fts(store), [])
  } finally {
    store.close()
  }
})

test('assertion transaction rollback leaves neither record nor projection job', () => {
  const store = new Store(':memory:')
  try {
    enable(store)
    const memory = new MemoryService(store)
    store.db
      .exec(`CREATE TEMP TRIGGER fixture_reject_revision BEFORE INSERT ON memory_assertion_revisions
      BEGIN SELECT RAISE(ABORT,'synthetic_failure'); END;`)
    assert.throws(() => memory.create(input()), /synthetic_failure/)
    assert.deepEqual(memory.adminList(), [])
    assert.deepEqual(jobs(store), [])
    assert.deepEqual(fts(store), [])
  } finally {
    store.close()
  }
})

test('restart drains only committed queued projections, not OFF-period legacy records or old chats', () => {
  const root = mkdtempSync(join(tmpdir(), 'he-projection-integration-'))
  const path = join(root, 'fixture.sqlite')
  let store = new Store(path)
  try {
    let memory = new MemoryService(store)
    const old = memory.create(input('SYNTHETIC_OLD_OFF'))
    enable(store)
    pendingAtCommit(memory)
    const queued = memory.create(input('SYNTHETIC_COMMITTED'))
    assert.equal(jobs(store).length, 2)
    assert.deepEqual(fts(store), [])
    store.close()
    store = new Store(path)
    memory = new MemoryService(store)
    assert.deepEqual(fts(store), [], 'constructor must not itself scan or replay')
    memory.flushProjections()
    assert.deepEqual(
      fts(store).map((row) => row.assertion_id),
      [queued.id],
    )
    assert.equal(
      fts(store).some((row) => row.assertion_id === old.id),
      false,
    )
    memory.flushProjections()
    assert.equal(jobs(store).length, 2)
    assert.equal(memory.adminList().length, 2)
  } finally {
    store.close()
    rmSync(root, { recursive: true, force: true })
  }
})

test('projection failure remains retryable after authority commits and OFF disables new work while deletion stays effective', () => {
  const store = new Store(':memory:')
  try {
    enable(store)
    const memory = new MemoryService(store)
    store.db
      .exec(`CREATE TEMP TRIGGER fixture_projection_failure BEFORE INSERT ON memory_person_projection_state
      BEGIN SELECT RAISE(ABORT,'synthetic_projection_failure'); END;`)
    const record = memory.create(input('SYNTHETIC_DURABLE_AUTHORITY'))
    assert.equal(
      memory.adminList().find((row) => row.id === record.id)?.content,
      'SYNTHETIC_DURABLE_AUTHORITY',
    )
    assert.equal(
      (
        store.db
          .prepare("SELECT count(*) AS n FROM memory_projection_outbox WHERE state='pending'")
          .get() as { n: number }
      ).n,
      1,
    )
    store.db.exec('DROP TRIGGER fixture_projection_failure')
    // Fake the retry deadline without sleeping or invoking a model.
    store.db
      .prepare("UPDATE memory_projection_outbox SET available_at=0 WHERE state='pending'")
      .run()
    memory.flushProjections()
    assert.equal(
      (
        store.db
          .prepare("SELECT count(*) AS n FROM memory_projection_outbox WHERE state='done'")
          .get() as { n: number }
      ).n,
      2,
    )
    store.set('settings', { ...store.settings, memoryProjectionEnabled: false })
    memory.update(record.id, { content: 'SYNTHETIC_CHANGED_WHILE_OFF' })
    assert.deepEqual(fts(store), [])
    assert.deepEqual(store.db.prepare('SELECT * FROM memory_person_projection_state').all(), [])
    assert.equal(jobs(store).length, 2)
    memory.delete(record.id)
    enable(store)
    memory.flushProjections()
    assert.deepEqual(fts(store), [])
  } finally {
    store.close()
  }
})
