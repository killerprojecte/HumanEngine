import assert from 'node:assert/strict'
import test from 'node:test'
import { mkdtemp, mkdir, rm } from 'node:fs/promises'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { Store } from '../src/store.ts'
import { MemoryService } from '../src/memory.ts'
import type { MemoryRevisionCoordinator } from '../src/memory/write/revision-coordinator.ts'
import type { AssertionRepository } from '../src/memory/storage/assertion-repository.ts'
import { metadata, RecoveryAuthority } from '../src/memory/recovery/ledger.ts'
import { recordAssertionEligibilityBaseline } from '../src/memory/recovery/eligibility.ts'
import { backup } from '../scripts/backup.ts'
import { restore } from '../scripts/restore.ts'

const context = { sceneId: 'review-scene', simulation: false }

async function fixture(baseline = true) {
  const root = await mkdtemp(join(tmpdir(), 'he-independent-eligibility-'))
  const database = join(root, 'live', 'humanengine.sqlite')
  const mediaRoot = join(root, 'live', 'media')
  await mkdir(mediaRoot, { recursive: true })
  const store = new Store(database)
  store.saveScene({
    id: context.sceneId,
    kind: 'group',
    adapterId: 'offline',
    accountId: 'synthetic-account',
    platformSceneId: 'synthetic-platform-scene',
    name: 'Independent eligibility review',
    learningEnabled: true,
    participationEnabled: true,
    proactiveEnabled: false,
  })
  const memory = new MemoryService(store)
  if (baseline) recordAssertionEligibilityBaseline(store.db)
  const opened: Array<{ store: Store; memory: MemoryService }> = []
  const authority = new RecoveryAuthority(metadata(store.db)!.authority)
  let nextSnapshot = 0
  let nextRestore = 0
  return {
    root,
    store,
    memory,
    authority,
    create(content: string) {
      return memory.create({
        scope: { kind: 'scene', sceneId: context.sceneId },
        content,
        kind: 'fact',
      })
    },
    snapshot: async () => {
      const destination = join(root, `backup-${++nextSnapshot}`)
      await backup({ database, mediaRoot, destination })
      return destination
    },
    recover: async (backupRoot: string) => {
      const result = await restore({
        backupRoot,
        destination: join(root, `restored-${++nextRestore}`),
        authorityDirectory: authority.path,
      })
      const restored = new Store(result.database)
      const service = new MemoryService(restored)
      const pair = { store: restored, memory: service }
      opened.push(pair)
      return pair
    },
    async close() {
      for (const pair of opened.reverse()) {
        pair.memory.dispose()
        pair.store.close()
      }
      memory.dispose()
      store.close()
      authority.close()
      await rm(root, { recursive: true, force: true })
    },
  }
}

function record(store: Store, id: string) {
  const row = store.db.prepare('SELECT value FROM memories WHERE id=?').get(id) as
    { value: string } | undefined
  return row ? JSON.parse(row.value) : undefined
}

function retained(store: Store, id: string) {
  return {
    state: store.db.prepare('SELECT * FROM memory_assertion_state WHERE assertion_id=?').get(id),
    revisions: store.db
      .prepare('SELECT * FROM memory_assertion_revisions WHERE assertion_id=? ORDER BY version')
      .all(id),
  }
}

function coordinator(memory: MemoryService) {
  return (memory as unknown as { revisions: MemoryRevisionCoordinator }).revisions
}

test('independent review: negative suffix preserves every retained history row and epoch', async () => {
  const f = await fixture()
  try {
    const item = f.create('Synthetic retained history')
    const original = retained(f.store, item.id)
    const old = await f.snapshot()
    const head = f.authority.head().sequence
    f.memory.update(item.id, { status: 'invalidated' })
    assert.equal(f.authority.head().sequence, head + 1)
    assert.deepEqual(retained(f.store, item.id), original)
    const restored = await f.recover(old)
    assert.equal(record(restored.store, item.id).status, 'invalidated')
    assert.deepEqual(retained(restored.store, item.id), original)
    assert.equal(restored.memory.historyFor(context, item.id), undefined)
  } finally {
    await f.close()
  }
})

test('independent review: identical repeated ABA advances authority without needing history movement', async () => {
  const f = await fixture()
  try {
    const item = f.create('Synthetic identical ABA')
    const original = retained(f.store, item.id)
    const old = await f.snapshot()
    const head = f.authority.head().sequence
    for (let cycle = 0; cycle < 2; cycle++) {
      f.memory.update(item.id, { status: 'invalidated' })
      f.memory.update(item.id, { status: 'active' })
    }
    assert.equal(f.authority.head().sequence, head + 4)
    assert.deepEqual(retained(f.store, item.id), original)
    const events = f.authority
      .entries()
      .slice(-4)
      .map((entry) => entry.revocation)
    assert.equal(new Set(events.map((event) => JSON.stringify(event))).size, 4)
    const restored = await f.recover(old)
    assert.equal(record(restored.store, item.id).status, 'active')
    assert.ok(restored.memory.historyFor(context, item.id))
  } finally {
    await f.close()
  }
})

test('independent review: missing grant material blocks until later terminal deletion dominates', async () => {
  const f = await fixture()
  try {
    const item = f.create('Synthetic old grant material')
    const old = await f.snapshot()
    f.memory.update(item.id, { status: 'invalidated' })
    f.memory.update(item.id, { status: 'active', content: 'Synthetic unavailable new material' })
    await assert.rejects(f.recover(old), /recovery_eligibility/)
    f.memory.delete(item.id)
    const restored = await f.recover(old)
    assert.equal(record(restored.store, item.id), undefined)
    assert.equal(restored.memory.historyFor(context, item.id), undefined)
  } finally {
    await f.close()
  }
})

test('independent review: acknowledged grant permits a later ordinary content edit in the backup', async () => {
  const f = await fixture()
  try {
    const item = f.create('Synthetic original prefix material')
    f.memory.update(item.id, { status: 'invalidated' })
    f.memory.update(item.id, { status: 'active' })
    f.memory.update(item.id, { content: 'Synthetic legitimate later edit' })
    const current = await f.snapshot()
    const restored = await f.recover(current)
    assert.equal(record(restored.store, item.id).content, 'Synthetic legitimate later edit')
    assert.ok(restored.memory.historyFor(context, item.id))
  } finally {
    await f.close()
  }
})

test('independent review: caught nested rollback excludes rolled-back records from aggregate', async () => {
  const f = await fixture()
  try {
    const first = f.create('Synthetic rolled-back eligibility')
    const second = f.create('Synthetic committed eligibility')
    const initial = f.authority.head().sequence
    const revisions = coordinator(f.memory)
    revisions.transaction(() => {
      assert.throws(
        revisions.transaction(() => {
          f.memory.update(first.id, { status: 'invalidated' })
          throw new Error('synthetic_nested_rollback')
        }),
        /synthetic_nested_rollback/,
      )
      f.memory.update(second.id, { status: 'invalidated' })
    })()
    assert.equal(record(f.store, first.id).status, 'active')
    assert.equal(record(f.store, second.id).status, 'invalidated')
    assert.equal(f.authority.head().sequence, initial + 1)
    const event = f.authority.entries().at(-1)!.revocation
    assert.equal(event.kind, 'assertion_eligibility')
    if (event.kind !== 'assertion_eligibility') throw new Error('wrong_event')
    assert.deepEqual(
      event.changes.map((change) => change.id),
      [second.id],
    )
  } finally {
    await f.close()
  }
})

test('independent review: revision checkpoints do not split one eligibility aggregate', async () => {
  const f = await fixture()
  try {
    const first = f.create('Synthetic checkpoint first')
    const second = f.create('Synthetic checkpoint second')
    const initial = f.authority.head().sequence
    const revisions = coordinator(f.memory)
    revisions.transaction(() => {
      f.memory.update(first.id, { status: 'invalidated' })
      revisions.checkpoint()
      f.memory.update(second.id, { status: 'invalidated' })
      revisions.checkpoint()
      assert.equal(f.authority.head().sequence, initial)
    })()
    assert.equal(f.authority.head().sequence, initial + 1)
    const event = f.authority.entries().at(-1)!.revocation
    if (event.kind !== 'assertion_eligibility') throw new Error('wrong_event')
    assert.deepEqual(event.changes.map((change) => change.id).sort(), [first.id, second.id].sort())
  } finally {
    await f.close()
  }
})

test('independent review: outer SQLite rollback preserves durable negative intent and blocks live reads', async () => {
  const f = await fixture()
  try {
    const item = f.create('Synthetic outer transaction rollback')
    const old = await f.snapshot()
    assert.throws(
      f.store.db.transaction(() => {
        f.memory.update(item.id, { status: 'invalidated' })
        throw new Error('synthetic_outer_rollback')
      }),
      /synthetic_outer_rollback/,
    )
    assert.equal(record(f.store, item.id).status, 'active')
    assert.throws(() => f.store.assertRecoveryReady(), /recovery_authority_head_changed/)
    const restored = await f.recover(old)
    assert.equal(record(restored.store, item.id).status, 'invalidated')
  } finally {
    await f.close()
  }
})

test('independent review: later source intent survives rollback and outranks an earlier regrant', async () => {
  const f = await fixture()
  try {
    const item = f.memory.create({
      scope: { kind: 'scene', sceneId: context.sceneId },
      content: 'Synthetic recalled grant material',
      kind: 'fact',
      sources: [
        { sceneId: context.sceneId, messageId: 'synthetic-message', senderId: 'synthetic-user' },
      ],
    })
    const old = await f.snapshot()
    f.memory.update(item.id, { status: 'invalidated' })
    f.memory.update(item.id, { status: 'active' })
    assert.throws(
      f.store.db.transaction(() => {
        f.store.recall(context.sceneId, 'synthetic-message')
        throw new Error('synthetic_source_rollback')
      }),
      /synthetic_source_rollback/,
    )
    const restored = await f.recover(old)
    assert.equal(restored.store.recalled(context.sceneId, 'synthetic-message'), true)
    assert.equal(record(restored.store, item.id).status, 'invalidated')
    assert.equal(restored.memory.historyFor(context, item.id), undefined)
  } finally {
    await f.close()
  }
})

test('independent review: nonterminal purge remains erased across exact-material regrant', async () => {
  const f = await fixture()
  try {
    const item = f.create('Synthetic retained current material after privacy purge')
    const original = retained(f.store, item.id).state as { version: number; deletion_epoch: number }
    const old = await f.snapshot()
    const revisions = coordinator(f.memory)
    revisions.transaction(() => {
      revisions.history.purge(item.id)
      f.memory.update(item.id, { status: 'invalidated' })
    })()
    f.memory.update(item.id, { status: 'active' })
    const restored = await f.recover(old)
    const current = retained(restored.store, item.id)
    // Existing privacy replay may advance the redaction counter more than once;
    // the eligibility grant must never rewind it or recover a purged version body.
    assert.ok(
      (current.state as { deletion_epoch: number }).deletion_epoch > original.deletion_epoch,
    )
    assert.equal(record(restored.store, item.id).status, 'active')
    for (const revision of current.revisions as Array<{ version: number; value: string | null }>)
      if (revision.version <= original.version) assert.equal(revision.value, null)
    assert.ok(restored.memory.historyFor(context, item.id))
  } finally {
    await f.close()
  }
})

test('independent review: a post-backup assertion grant leaves the missing root absent', async () => {
  const f = await fixture()
  try {
    const old = await f.snapshot()
    const item = f.create('Synthetic assertion born after the backup')
    f.memory.update(item.id, { status: 'invalidated' })
    f.memory.update(item.id, { status: 'active' })
    const restored = await f.recover(old)
    assert.equal(record(restored.store, item.id), undefined)
    assert.equal(restored.memory.historyFor(context, item.id), undefined)
    assert.deepEqual(retained(restored.store, item.id).revisions, [])
  } finally {
    await f.close()
  }
})

test('independent review: explicit legacy negative census withdraws an older active backup', async () => {
  const f = await fixture(false)
  try {
    const item = f.create('Synthetic pre-upgrade current-live census')
    const original = retained(f.store, item.id)
    const old = await f.snapshot()
    // Simulate the historical supported main-only invalidation, before the new writer existed.
    f.store.db
      .prepare("UPDATE memories SET value=json_set(value,'$.status','invalidated') WHERE id=?")
      .run(item.id)
    recordAssertionEligibilityBaseline(f.store.db)
    const restored = await f.recover(old)
    assert.equal(record(restored.store, item.id).status, 'invalidated')
    assert.deepEqual(retained(restored.store, item.id), original)
    assert.equal(restored.memory.historyFor(context, item.id), undefined)
  } finally {
    await f.close()
  }
})

test('independent review: empty legacy census does not invent an unrecorded old regrant', async () => {
  const f = await fixture(false)
  try {
    const item = f.create('Synthetic pre-upgrade negative snapshot')
    f.store.db
      .prepare("UPDATE memories SET value=json_set(value,'$.status','invalidated') WHERE id=?")
      .run(item.id)
    const old = await f.snapshot()
    f.store.db
      .prepare("UPDATE memories SET value=json_set(value,'$.status','active') WHERE id=?")
      .run(item.id)
    recordAssertionEligibilityBaseline(f.store.db)
    const restored = await f.recover(old)
    assert.equal(record(restored.store, item.id).status, 'invalidated')
    assert.equal(restored.memory.historyFor(context, item.id), undefined)
  } finally {
    await f.close()
  }
})

test('independent review: negative dimensions never clear a stricter snapshot withdrawal', async () => {
  const f = await fixture()
  try {
    const item = f.create('Synthetic independent status and lineage withdrawals')
    f.memory.update(item.id, { status: 'invalidated' })
    const old = await f.snapshot()
    const repository = (f.memory as unknown as { repository: AssertionRepository }).repository
    coordinator(f.memory).transaction(() => {
      repository.write({
        ...record(f.store, item.id),
        status: 'active',
        lineageStatus: 'quarantined',
      })
    })()
    const restored = await f.recover(old)
    assert.equal(record(restored.store, item.id).status, 'invalidated')
    assert.equal(record(restored.store, item.id).lineageStatus, 'quarantined')
    assert.equal(restored.memory.historyFor(context, item.id), undefined)
  } finally {
    await f.close()
  }
})

test('independent review: allowed applicability status changes do not add eligibility obligations', async () => {
  const f = await fixture()
  try {
    const item = f.create('Synthetic allowed historical applicability')
    const start = f.authority.head()
    f.memory.update(item.id, { status: 'disputed' })
    f.memory.update(item.id, { status: 'superseded' })
    f.memory.update(item.id, { status: 'active' })
    assert.deepEqual(f.authority.head(), start)
    assert.ok(f.memory.historyFor(context, item.id))
  } finally {
    await f.close()
  }
})
