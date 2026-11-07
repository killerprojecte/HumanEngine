import test from 'node:test'
import assert from 'node:assert/strict'
import { mkdtemp, mkdir, rm } from 'node:fs/promises'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { randomUUID } from 'node:crypto'
import { Store } from '../src/store.ts'
import { MemoryService } from '../src/memory.ts'
import type { MemoryRecord } from '../src/memory/domain/contracts.ts'
import { AssertionHistory } from '../src/memory/storage/assertion-history.ts'
import { MemoryRevisionCoordinator } from '../src/memory/write/revision-coordinator.ts'
import { metadata, RecoveryAuthority, revoke } from '../src/memory/recovery/ledger.ts'
import {
  eligibilityIncarnation,
  eligibilityMaterial,
  parseAssertionEligibilityEvent,
} from '../src/memory/recovery/eligibility-contract.ts'
import {
  recordAssertionEligibilityBaseline,
  recordAssertionEligibilityChanges,
} from '../src/memory/recovery/eligibility.ts'
import { backup } from '../scripts/backup.ts'
import { restore } from '../scripts/restore.ts'

async function fixture(baseline = true) {
  const root = await mkdtemp(join(tmpdir(), 'he-eligibility-'))
  const database = join(root, 'live', 'db.sqlite'),
    mediaRoot = join(root, 'media')
  await mkdir(mediaRoot)
  const store = new Store(database),
    memory = new MemoryService(store)
  if (baseline) recordAssertionEligibilityBaseline(store.db)
  const opened: Store[] = []
  const current = (id: string, db = store.db): MemoryRecord | undefined => {
    const row = db.prepare('SELECT value FROM memories WHERE id=?').get(id) as
      { value: string } | undefined
    return row ? JSON.parse(row.value) : undefined
  }
  return {
    root,
    store,
    memory,
    current,
    create: (content = 'SYNTHETIC_ASSERTION') =>
      memory.create({
        scope: { kind: 'sharedPublicKnowledge' },
        kind: 'fact',
        content,
      }),
    events: () => {
      const authority = new RecoveryAuthority(metadata(store.db)!.authority)
      try {
        return authority.entries()
      } finally {
        authority.close()
      }
    },
    snapshot: async (name: string) => {
      const destination = join(root, name)
      await backup({ database, mediaRoot, destination })
      return destination
    },
    recover: async (backupRoot: string, name: string) => {
      const result = await restore({
        backupRoot,
        destination: join(root, name),
        authorityDirectory: metadata(store.db)!.authority,
      })
      const restored = new Store(result.database)
      opened.push(restored)
      return restored
    },
    close: async () => {
      for (const restored of opened) restored.close()
      store.close()
      await rm(root, { recursive: true, force: true })
    },
  }
}

const historyRows = (store: Store, id: string) =>
  store.db
    .prepare('SELECT * FROM memory_assertion_revisions WHERE assertion_id=? ORDER BY version')
    .all(id)

test('eligibility contract is body-free, strict, duplicate-free and requires grant material', () => {
  const event = {
    kind: 'assertion_eligibility',
    schemaVersion: 1,
    operationId: randomUUID(),
    mode: 'transition',
    changes: [
      {
        id: 'synthetic',
        incarnationHash: 'a'.repeat(64),
        state: { status: 'invalidated', lineageStatus: null },
        materialHash: null,
      },
    ],
  }
  assert.deepEqual(parseAssertionEligibilityEvent(event), event)
  for (const value of [
    { ...event, body: 'SYNTHETIC_BODY' },
    { ...event, changes: [...event.changes, ...event.changes] },
    { ...event, changes: [] },
    {
      ...event,
      changes: [{ ...event.changes[0], state: { status: 'active', lineageStatus: null } }],
    },
    { ...event, changes: [{ ...event.changes[0], materialHash: 'b'.repeat(64) }] },
    { ...event, operationId: 'not-a-monotonic-operation' },
  ])
    assert.throws(() => parseAssertionEligibilityEvent(value), /eligibility_event_invalid/)
  const record = {
    id: 'synthetic',
    learnedAt: '2026-01-01',
    status: 'active',
    content: 'material',
    scope: { kind: 'sharedPublicKnowledge' },
  } as MemoryRecord
  assert.equal(
    eligibilityMaterial(record, []),
    eligibilityMaterial({ ...record, status: 'invalidated' }, []),
  )
  assert.equal(
    eligibilityMaterial(record, []),
    eligibilityMaterial({ ...record, scope: { kind: 'sharedPublicKnowledge' } }, []),
  )
  assert.notEqual(
    eligibilityMaterial(record, []),
    eligibilityMaterial({ ...record, content: 'other' }, []),
  )
  assert.notEqual(
    eligibilityIncarnation(record),
    eligibilityIncarnation({ ...record, learnedAt: '2026-01-02' }),
  )
})

test('active-invalidated-identical-active preserves bodies and epoch but each repeated transition advances authority', async () => {
  const f = await fixture()
  try {
    const record = f.create(),
      history = new AssertionHistory(f.store.db)
    // Normalize the same explicit optional lineage representation as a manual PATCH.
    f.memory.update(record.id, { status: 'active' })
    const state = history.state(record.id),
      bodies = historyRows(f.store, record.id)
    const start = metadata(f.store.db)!.head.sequence
    for (let i = 0; i < 2; i++) {
      f.memory.update(record.id, { status: 'invalidated' })
      assert.deepEqual(history.state(record.id), state)
      assert.deepEqual(historyRows(f.store, record.id), bodies)
      f.memory.update(record.id, { status: 'active' })
      assert.deepEqual(history.state(record.id), state)
    }
    assert.equal(metadata(f.store.db)!.head.sequence, start + 4)
    assert.equal(metadata(f.store.db)!.minimumReader, 11)
    const transitions = f
      .events()
      .flatMap(({ revocation }) =>
        revocation.kind === 'assertion_eligibility' && revocation.mode === 'transition'
          ? [revocation]
          : [],
      )
    assert.equal(new Set(transitions.map((event) => event.operationId)).size, 4)
    assert.equal(JSON.stringify(transitions).includes(record.content), false)
  } finally {
    await f.close()
  }
})

test('old snapshot replays reversible negative and exact ABA grant without erasing history', async () => {
  const f = await fixture()
  try {
    const record = f.create(),
      old = await f.snapshot('before'),
      rows = historyRows(f.store, record.id)
    const initial = new AssertionHistory(f.store.db).state(record.id)
    f.memory.update(record.id, { status: 'invalidated' })
    const denied = await f.recover(old, 'negative')
    assert.equal(f.current(record.id, denied.db)?.status, 'invalidated')
    assert.deepEqual(historyRows(denied, record.id), rows)
    assert.deepEqual(new AssertionHistory(denied.db).state(record.id), initial)
    f.memory.update(record.id, { status: 'active' })
    const granted = await f.recover(old, 'grant')
    assert.equal(f.current(record.id, granted.db)?.status, 'active')
    assert.deepEqual(historyRows(granted, record.id), rows)
    assert.deepEqual(new AssertionHistory(granted.db).state(record.id), initial)
  } finally {
    await f.close()
  }
})

test('missing changed grant material quarantines, but a later negative needs no intermediate grant material', async () => {
  const f = await fixture()
  try {
    const record = f.create(),
      old = await f.snapshot('old')
    f.memory.update(record.id, { status: 'invalidated' })
    f.memory.update(record.id, { status: 'active', content: 'SYNTHETIC_NEW_REVIEWED_CONTENT' })
    await assert.rejects(f.recover(old, 'missing-grant'), /eligibility_grant_material_missing/)
    f.memory.update(record.id, { status: 'invalidated' })
    const restored = await f.recover(old, 'later-negative')
    assert.equal(f.current(record.id, restored.db)?.status, 'invalidated')
    assert.equal(f.current(record.id, restored.db)?.content, record.content)
  } finally {
    await f.close()
  }
})

test('explicit legacy negative census protects pre-baseline backups, without inventing historical regrants', async () => {
  const f = await fixture(false)
  try {
    const record = f.create(),
      old = await f.snapshot('pre-upgrade')
    // Fixture represents an old reader's pre-upgrade main-only update.
    f.store.db
      .prepare('UPDATE memories SET value=? WHERE id=?')
      .run(JSON.stringify({ ...record, status: 'invalidated' }), record.id)
    recordAssertionEligibilityBaseline(f.store.db)
    const restored = await f.recover(old, 'baseline-negative')
    assert.equal(f.current(record.id, restored.db)?.status, 'invalidated')
    assert.equal(historyRows(restored, record.id).length, 1)
    assert.throws(() => recordAssertionEligibilityBaseline(f.store.db), /baseline_already_started/)
  } finally {
    await f.close()
  }
})

test('empty baseline preserves a legacy backup negative when current live state had an unjournaled regrant', async () => {
  const f = await fixture(false)
  try {
    const record = f.create()
    f.store.db
      .prepare('UPDATE memories SET value=? WHERE id=?')
      .run(JSON.stringify({ ...record, status: 'invalidated' }), record.id)
    const old = await f.snapshot('legacy-negative')
    f.store.db
      .prepare('UPDATE memories SET value=? WHERE id=?')
      .run(JSON.stringify(record), record.id)
    recordAssertionEligibilityBaseline(f.store.db)
    const restored = await f.recover(old, 'empty-baseline')
    assert.equal(f.current(record.id, restored.db)?.status, 'invalidated')
  } finally {
    await f.close()
  }
})

test('guarded retries do not append duplicate transitions, unrelated events never suppress one', async () => {
  const f = await fixture()
  try {
    const record = f.create(),
      state = new AssertionHistory(f.store.db).state(record.id)
    const guard = {
      expectedVersion: state.version,
      expectedDeletionEpoch: state.deletionEpoch,
      idempotencyKey: 'synthetic-idempotency',
    }
    f.memory.updateGuarded(record.id, { status: 'invalidated' }, guard)
    const after = metadata(f.store.db)!.head
    f.memory.updateGuarded(record.id, { status: 'invalidated' }, guard)
    assert.deepEqual(metadata(f.store.db)!.head, after)
    f.store.db.transaction(() => {
      revoke(f.store.db, { kind: 'source', sceneId: 'unrelated', messageId: 'unrelated' })
      f.memory.update(record.id, { status: 'active' })
    })()
    assert.equal(metadata(f.store.db)!.head.sequence, after.sequence + 2)
  } finally {
    await f.close()
  }
})

test('coordinator batches many final transitions across checkpoints and ignores rolled-back nested writes', async () => {
  const f = await fixture()
  try {
    const records = Array.from({ length: 16 }, (_, index) => f.create(`SYNTHETIC_${index}`))
    const coordinator = new MemoryRevisionCoordinator(
      f.store.db,
      f.current,
      () => [],
      () => false,
      undefined,
      undefined,
      (changes) => recordAssertionEligibilityChanges(f.store.db, changes),
    )
    const write = (record: MemoryRecord) => {
      coordinator.beforeWrite(record.id)
      f.store.db
        .prepare('UPDATE memories SET value=? WHERE id=?')
        .run(JSON.stringify(record), record.id)
      coordinator.afterWrite(record)
    }
    const start = metadata(f.store.db)!.head.sequence
    coordinator.transaction(() => {
      for (const record of records) {
        write({ ...record, status: 'invalidated' })
        coordinator.checkpoint()
      }
      assert.equal(
        metadata(f.store.db)!.head.sequence,
        start,
        'checkpoints do not independently append',
      )
      assert.throws(
        coordinator.transaction(() => {
          write({ ...records[0]!, status: 'active' })
          throw Error('nested rollback')
        }),
      )
    })()
    assert.equal(metadata(f.store.db)!.head.sequence, start + 1)
    const final = f.events().at(-1)!.revocation
    assert.equal(final.kind, 'assertion_eligibility')
    if (final.kind === 'assertion_eligibility') assert.equal(final.changes.length, records.length)
    assert.equal(f.current(records[0]!.id)?.status, 'invalidated')
  } finally {
    await f.close()
  }
})

test('reviewed quarantine release is journaled even when status stays active and proof mismatch cannot be guessed away', async () => {
  const f = await fixture(false)
  try {
    const record = f.create()
    const quarantined: MemoryRecord = { ...record, lineageStatus: 'quarantined' }
    f.store.db
      .prepare('UPDATE memories SET value=? WHERE id=?')
      .run(JSON.stringify(quarantined), record.id)
    // Synthetic trusted legacy proof is removed only by explicit reviewed content.
    f.store.db
      .prepare('INSERT INTO memory_supports VALUES(?,?)')
      .run(
        record.id,
        JSON.stringify([{ sources: [], parents: [], syntheticWitness: 'PRIVATE_PROOF' }]),
      )
    recordAssertionEligibilityBaseline(f.store.db)
    const old = await f.snapshot('quarantined-with-proof')
    f.memory.update(record.id, { content: record.content })
    assert.equal(f.current(record.id)?.status, 'active')
    assert.equal(f.current(record.id)?.lineageStatus, undefined)
    const event = f.events().at(-1)!.revocation
    assert.equal(event.kind, 'assertion_eligibility')
    if (event.kind === 'assertion_eligibility') {
      assert.equal(event.changes[0]?.state.status, 'active')
      assert.equal(event.changes[0]?.state.lineageStatus, null)
      assert.equal(typeof event.changes[0]?.materialHash, 'string')
      assert.equal(JSON.stringify(event).includes('PRIVATE_PROOF'), false)
    }
    await assert.rejects(
      f.recover(old, 'missing-reviewed-proofs'),
      /eligibility_grant_material_missing/,
    )
    const current = await f.snapshot('reviewed-current')
    const restored = await f.recover(current, 'reviewed-restored')
    assert.equal(f.current(record.id, restored.db)?.lineageStatus, undefined)
  } finally {
    await f.close()
  }
})

test('exact-material reviewed quarantine release can restore without replacing content or proofs', async () => {
  const f = await fixture(false)
  try {
    const record = f.create()
    f.store.db
      .prepare('UPDATE memories SET value=? WHERE id=?')
      .run(JSON.stringify({ ...record, lineageStatus: 'quarantined' }), record.id)
    recordAssertionEligibilityBaseline(f.store.db)
    const old = await f.snapshot('quarantined-no-proof')
    f.memory.update(record.id, { content: record.content })
    const restored = await f.recover(old, 'reviewed-replay')
    assert.equal(f.current(record.id, restored.db)?.lineageStatus, undefined)
    assert.equal(f.current(record.id, restored.db)?.content, record.content)
  } finally {
    await f.close()
  }
})

test('warm ordinary and support-only writes add no eligibility row reads or authority commits', async () => {
  const f = await fixture()
  try {
    const record = f.create()
    let reads = 0,
      candidates = 0
    const coordinator = new MemoryRevisionCoordinator(
      f.store.db,
      (id) => {
        reads++
        return f.current(id)
      },
      () => [],
      () => false,
      undefined,
      undefined,
      (changes) => {
        candidates += changes.length
        recordAssertionEligibilityChanges(f.store.db, changes)
      },
    )
    const update = f.store.db.prepare('UPDATE memories SET value=? WHERE id=?')
    const start = metadata(f.store.db)!.head
    for (let transaction = 0; transaction < 64; transaction++) {
      coordinator.transaction(() => {
        for (let write = 0; write < 8; write++) {
          coordinator.beforeWrite(record.id)
          const next = { ...record, locked: write % 2 === 0 }
          update.run(JSON.stringify(next), record.id)
          coordinator.afterWrite(next)
        }
      })()
    }
    assert.equal(
      reads,
      64 * 2,
      'only existing first-before and revision-flush reads, not per repeated write or eligibility flush',
    )
    assert.equal(candidates, 0)
    assert.deepEqual(metadata(f.store.db)!.head, start)
    reads = 0
    coordinator.transaction(() => {
      for (let write = 0; write < 8; write++) coordinator.beforeWrite(record.id)
    })()
    assert.equal(reads, 2, 'support-only touches also do not fetch an eligibility final row')
    assert.equal(candidates, 0)
  } finally {
    await f.close()
  }
})
