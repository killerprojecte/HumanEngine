import assert from 'node:assert/strict'
import test from 'node:test'
import { mkdtempSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { memoryScenarioHarness } from './helpers/memory-scenario-harness.ts'
import { memoryScenarioIds as ids } from './fixtures/memory-scenarios.ts'

/** Persistent synthetic DB only; no model, adapter, external send or real chat. */
function persistentFixture() {
  const directory = mkdtempSync(join(tmpdir(), 'he-history-lifecycle-'))
  const path = join(directory, 'fixture.sqlite')
  let current = memoryScenarioHarness(path)
  return {
    get current() {
      return current
    },
    reopen() {
      current.close()
      current = memoryScenarioHarness(path)
      return current
    },
    close() {
      current.close()
      rmSync(directory, { recursive: true, force: true })
    },
  }
}
type Harness = ReturnType<typeof memoryScenarioHarness>

/** Recreate pre-companion rows for migration coverage, not a supported product mutation. */
function makeLegacy(h: Harness, id: string, unverified = false) {
  h.store.db.transaction(() => {
    if (unverified) {
      h.store.db
        .prepare(
          "UPDATE memories SET value=json_set(value,'$.lineageStatus','unverified') WHERE id=?",
        )
        .run(id)
      h.store.db.prepare('DELETE FROM memory_supports WHERE memory_id=?').run(id)
    }
    for (const table of [
      'memory_assertion_revision_parents',
      'memory_assertion_revision_sources',
      'memory_assertion_revisions',
      'memory_assertion_state',
      'memory_command_receipts',
    ])
      if (
        h.store.db.prepare("SELECT 1 FROM sqlite_master WHERE type='table' AND name=?").get(table)
      )
        h.store.db.prepare(`DELETE FROM ${table} WHERE assertion_id=?`).run(id)
  })()
}
function revisionRows(h: Harness) {
  return h.store.db
    .prepare('SELECT * FROM memory_assertion_revisions ORDER BY assertion_id,version')
    .all()
}
function assertNoHistoricalCanaries(h: Harness, ...canaries: string[]) {
  const raw = JSON.stringify([
    revisionRows(h),
    h.store.db.prepare('SELECT * FROM memory_assertion_revision_sources').all(),
  ])
  for (const canary of canaries)
    assert.equal(raw.includes(canary), false, `revoked revision data retained in SQL: ${canary}`)
}
function snapshot(h: Harness) {
  return JSON.stringify([
    h.snapshot(),
    ...[
      'memory_assertion_state',
      'memory_assertion_revisions',
      'memory_assertion_revision_sources',
      'memory_assertion_revision_parents',
      'memory_command_receipts',
    ].map((table) => [table, h.store.db.prepare(`SELECT * FROM ${table} ORDER BY rowid`).all()]),
  ])
}

test('legacy quarantine purges imported history before safe manual review and cannot revive its body after restart', () => {
  const f = persistentFixture()
  let h = f.current
  try {
    const old = h.saved(
      h.propose('LEGACY_UNVERIFIED_BODY_CANARY', [h.source('retained-placeholder')]),
    )
    makeLegacy(h, old.id, true)
    h = f.reopen()
    assert.equal(h.memory.revisionState(old.id).version, 0)
    assert.ok(JSON.stringify(h.memory.historyFor(h.context(), old.id)).includes(old.content))
    h.memory.recall(ids.group, 'original-source-no-longer-retained')
    assert.equal(h.memory.historyFor(h.context(), old.id), undefined)
    assertNoHistoricalCanaries(h, old.content)
    h.memory.update(old.id, { content: 'Explicitly reviewed safe replacement.' })
    const history = h.memory.historyFor(h.context(), old.id)
    assert.ok(history)
    assert.equal(JSON.stringify(history).includes(old.content), false)
    assertNoHistoricalCanaries(h, old.content)
    h = f.reopen()
    assert.equal(
      JSON.stringify(h.memory.historyFor(h.context(), old.id)).includes(old.content),
      false,
    )
    assertNoHistoricalCanaries(h, old.content)
    assert.equal(
      h.memory.personProfile(h.context(), ids.lin).records[0]?.content,
      'Explicitly reviewed safe replacement.',
    )
  } finally {
    f.close()
  }
})

test('deleting a parent purges historical-only descendant bodies even after a manual replacement cleared current supports', () => {
  const f = persistentFixture()
  let h = f.current
  try {
    const parent = h.saved(
      h.propose('PARENT_PRIVATE_BODY_CANARY', [h.source('parent-source-canary')]),
    )
    const child = h.saved(
      h.propose('CHILD_PRIVATE_BODY_CANARY', [h.source('child-source')], {}, [parent.id]),
    )
    h.memory.update(child.id, { content: 'Reviewed safe replacement.' })
    assert.equal(
      h.store.db.prepare('SELECT 1 FROM memory_supports WHERE memory_id=?').get(child.id),
      undefined,
    )
    assert.ok(JSON.stringify(revisionRows(h)).includes(child.content))
    h.memory.delete(parent.id)
    assertNoHistoricalCanaries(h, parent.content, child.content, 'parent-source-canary')
    assert.equal(h.memory.historyFor(h.context(), parent.id), undefined)
    assert.equal(
      JSON.stringify(h.memory.historyFor(h.context(), child.id)).includes(child.content),
      false,
    )
    assert.equal(
      h.memory.personProfile(h.context(), ids.lin).records[0]?.content,
      'Reviewed safe replacement.',
    )
    h = f.reopen()
    assertNoHistoricalCanaries(h, parent.content, child.content, 'parent-source-canary')
    assert.equal(
      JSON.stringify(h.memory.historyFor(h.context(), child.id)).includes(child.content),
      false,
    )
  } finally {
    f.close()
  }
})

test('first reviewed update of a legacy verified record captures its exact pre-mutation proof before clearing current supports', () => {
  const f = persistentFixture()
  let h = f.current
  try {
    const parent = h.saved(h.propose('Legacy premise.', [h.source('parent-proof')]))
    const old = h.saved(
      h.propose('Legacy exact proof body.', [h.source('required-a'), h.source('required-b')], {}, [
        parent.id,
      ]),
    )
    h.saved(h.propose(old.content, [h.source('alternative-proof')]))
    const exactSupports = JSON.parse(
      (
        h.store.db.prepare('SELECT value FROM memory_supports WHERE memory_id=?').get(old.id) as {
          value: string
        }
      ).value,
    )
    makeLegacy(h, old.id)
    h = f.reopen()
    assert.deepEqual(h.memory.revisionState(old.id), {
      version: 0,
      deletionEpoch: 0,
      deleted: false,
    })
    h.memory.update(old.id, { content: 'Reviewed replacement.' })
    const imported = h.memory
      .historyFor(h.context(), old.id)
      ?.revisions.find((revision) => revision.event.legacyImport)
    assert.ok(imported, 'pre-mutation legacy revision must remain observable')
    assert.equal(imported.record.content, old.content)
    assert.deepEqual(imported.supports, exactSupports)
    assert.equal(
      h.store.db.prepare('SELECT 1 FROM memory_supports WHERE memory_id=?').get(old.id),
      undefined,
    )
    h = f.reopen()
    const reopened = h.memory
      .historyFor(h.context(), old.id)
      ?.revisions.find((revision) => revision.event.legacyImport)
    assert.deepEqual(reopened?.supports, exactSupports)
  } finally {
    f.close()
  }
})

test('alternative-proof recall omits revoked source metadata from newly retained history and repeated recall is idempotent', () => {
  const f = persistentFixture()
  let h = f.current
  try {
    const old = h.saved(
      h.propose('Independently supported body.', [h.source('REVOKED_SOURCE_CANARY')]),
    )
    h.saved(h.propose(old.content, [h.source('surviving-source')]))
    h.memory.recall(ids.group, 'REVOKED_SOURCE_CANARY')
    const first = h.memory.historyFor(h.context(), old.id)!
    assert.ok(first.revisions.length > 0, 'surviving proof must retain usable current history')
    assert.equal(JSON.stringify(first).includes('REVOKED_SOURCE_CANARY'), false)
    assertNoHistoricalCanaries(h, 'REVOKED_SOURCE_CANARY')
    h.memory.recall(ids.group, 'REVOKED_SOURCE_CANARY')
    assert.deepEqual(h.memory.historyFor(h.context(), old.id), first)
    h = f.reopen()
    assertNoHistoricalCanaries(h, 'REVOKED_SOURCE_CANARY')
    assert.deepEqual(h.memory.historyFor(h.context(), old.id), first)
    h.memory.recall(ids.group, 'REVOKED_SOURCE_CANARY')
    assert.deepEqual(h.memory.historyFor(h.context(), old.id), first)
  } finally {
    f.close()
  }
})

test('history authorization rejects other scenes and simulation before lazy legacy migration', () => {
  const f = persistentFixture()
  let h = f.current
  try {
    const record = h.saved(h.propose('PRIVATE_HISTORY_BODY_CANARY', [h.source('private-source')]))
    makeLegacy(h, record.id)
    h = f.reopen()
    const before = snapshot(h)
    for (const ctx of [
      h.context(ids.otherGroup),
      h.context(ids.private),
      h.context(ids.otherAccount),
      { ...h.context(), simulation: true },
    ])
      assert.equal(h.memory.historyFor(ctx, record.id), undefined)
    assert.equal(snapshot(h), before, 'unauthorized history reads must not even import legacy data')
    const allowed = h.memory.historyFor(h.context(), record.id)
    assert.equal(allowed?.revisions[0]?.record.content, record.content)
    const after = snapshot(h)
    const detached = h.memory.historyFor(h.context(), record.id)!
    detached.revisions[0]!.record.content = 'Caller mutation.'
    detached.revisions[0]!.supports[0]!.sources[0]!.messageId = 'caller-source'
    assert.equal(snapshot(h), after)
    h = f.reopen()
    assert.deepEqual(h.memory.historyFor(h.context(), record.id), allowed)
  } finally {
    f.close()
  }
})

test('guarded service commands preserve retry identity and reject stale versions or changed payloads without mutation', () => {
  const f = persistentFixture()
  let h = f.current
  try {
    const record = h.memory.create({
      scope: { kind: 'scene', sceneId: ids.group, personId: ids.lin },
      content: 'Original manual preference.',
      kind: 'claim',
    })
    const state = h.memory.revisionState(record.id)
    const firstGuard = {
      expectedVersion: state.version,
      expectedDeletionEpoch: state.deletionEpoch,
      idempotencyKey: 'first-edit',
    }
    const patch = { content: 'First reviewed preference.' }
    const result = h.memory.updateGuarded(record.id, patch, firstGuard)
    const first = snapshot(h)
    assert.equal(h.memory.revisionState(record.id).version, state.version + 1)
    assert.deepEqual(h.memory.updateGuarded(record.id, patch, firstGuard), result)
    assert.equal(snapshot(h), first)
    assert.throws(
      () => h.memory.updateGuarded(record.id, { content: 'Different request.' }, firstGuard),
      /memory_command_conflict/,
    )
    assert.throws(
      () =>
        h.memory.updateGuarded(record.id, patch, {
          ...firstGuard,
          idempotencyKey: 'stale-distinct-command',
        }),
      /assertion_version_conflict/,
    )
    assert.equal(snapshot(h), first)
    h = f.reopen()
    assert.deepEqual(h.memory.updateGuarded(record.id, patch, firstGuard), result)
    assert.equal(snapshot(h), first)
    const current = h.memory.revisionState(record.id)
    h.memory.updateGuarded(
      record.id,
      { content: 'Second reviewed preference.' },
      {
        expectedVersion: current.version,
        expectedDeletionEpoch: current.deletionEpoch,
        idempotencyKey: 'second-edit',
      },
    )
    const second = snapshot(h)
    assert.throws(
      () => h.memory.updateGuarded(record.id, patch, firstGuard),
      /memory_command_superseded/,
    )
    assert.equal(snapshot(h), second)
  } finally {
    f.close()
  }
})

test('guarded writes reject a stale deletion epoch independently of version, and terminal deletion blocks retry after restart', () => {
  const f = persistentFixture()
  let h = f.current
  try {
    const record = h.saved(h.propose('EPOCH_GUARD_BODY_CANARY', [h.source('old-proof')]))
    h.saved(h.propose(record.content, [h.source('independent-proof')]))
    const oldEpoch = h.memory.revisionState(record.id).deletionEpoch
    h.memory.recall(ids.group, 'old-proof')
    const live = h.memory.revisionState(record.id)
    assert.ok(live.deletionEpoch > oldEpoch)
    const before = snapshot(h)
    assert.throws(
      () =>
        h.memory.updateGuarded(
          record.id,
          { content: 'Stale write.' },
          {
            expectedVersion: live.version,
            expectedDeletionEpoch: oldEpoch,
            idempotencyKey: 'epoch-stale',
          },
        ),
      /assertion_deleted_or_stale_epoch/,
    )
    assert.equal(snapshot(h), before)
    const guard = {
      expectedVersion: live.version,
      expectedDeletionEpoch: live.deletionEpoch,
      idempotencyKey: 'epoch-current',
    }
    const patch = { content: 'TERMINALLY_DELETED_BODY_CANARY' }
    h.memory.updateGuarded(record.id, patch, guard)
    h.memory.delete(record.id)
    assertNoHistoricalCanaries(h, record.content, patch.content)
    h = f.reopen()
    const deleted = h.memory.revisionState(record.id)
    assert.equal(deleted.deleted, true)
    assert.ok(deleted.deletionEpoch > live.deletionEpoch)
    const after = snapshot(h)
    assert.throws(
      () => h.memory.updateGuarded(record.id, patch, guard),
      /memory_command_superseded/,
    )
    assert.throws(
      () =>
        h.memory.updateGuarded(record.id, patch, {
          expectedVersion: deleted.version,
          expectedDeletionEpoch: deleted.deletionEpoch,
          idempotencyKey: 'fresh-command-after-delete',
        }),
      /assertion_deleted_or_stale_epoch/,
    )
    assert.equal(snapshot(h), after)
    assert.equal(h.memory.historyFor(h.context(), record.id), undefined)
    assert.equal(
      JSON.stringify(h.store.db.prepare('SELECT * FROM memory_command_receipts').all()).includes(
        patch.content,
      ),
      false,
      'retry receipts must not retain payload bodies',
    )
  } finally {
    f.close()
  }
})

for (const stage of ['history', 'receipt'] as const)
  test(`guarded ${stage} persistence failure rolls back current record, support, tombstone, revision and receipt together`, () => {
    const f = persistentFixture()
    let h = f.current
    try {
      const record = h.saved(h.propose('Atomic original preference.', [h.source('atomic-proof')]))
      const state = h.memory.revisionState(record.id)
      const guard = {
        expectedVersion: state.version,
        expectedDeletionEpoch: state.deletionEpoch,
        idempotencyKey: `retry-${stage}`,
      }
      const table = stage === 'history' ? 'memory_assertion_revisions' : 'memory_command_receipts'
      const patch = { content: `ROLLBACK_${stage.toUpperCase()}_BODY_CANARY` }
      h.store.db.exec(
        `CREATE TRIGGER reject_lifecycle_write BEFORE INSERT ON ${table} BEGIN SELECT RAISE(ABORT, 'forced_lifecycle_failure'); END`,
      )
      const before = snapshot(h)
      assert.throws(
        () => h.memory.updateGuarded(record.id, patch, guard),
        /forced_lifecycle_failure/,
      )
      assert.equal(snapshot(h), before)
      assertNoHistoricalCanaries(h, patch.content)
      h.store.db.exec('DROP TRIGGER reject_lifecycle_write')
      h = f.reopen()
      assert.equal(snapshot(h), before)
      const result = h.memory.updateGuarded(record.id, patch, guard)
      assert.equal(result.content, patch.content)
      assert.equal(h.memory.revisionState(record.id).version, state.version + 1)
      const receipts = h.store.db
        .prepare('SELECT count(*) AS n FROM memory_command_receipts WHERE command_key=?')
        .get(guard.idempotencyKey) as { n: number }
      assert.equal(receipts.n, 1)
      h = f.reopen()
      assert.deepEqual(h.memory.updateGuarded(record.id, patch, guard), result)
    } finally {
      f.close()
    }
  })

for (const entry of ['read', 'write'] as const)
  test(`legacy ${entry} migration filters already revoked proof alternatives before retaining history`, () => {
    const f = persistentFixture()
    let h = f.current
    try {
      const record = h.saved(
        h.propose('Legacy independently supported body.', [
          h.source('REVOKED_LEGACY_SOURCE_CANARY'),
        ]),
      )
      h.saved(h.propose(record.content, [h.source('surviving-legacy-source')]))
      h.memory.recall(ids.group, 'REVOKED_LEGACY_SOURCE_CANARY')
      makeLegacy(h, record.id)
      h = f.reopen()
      assert.equal(h.memory.revisionState(record.id).version, 0)
      if (entry === 'write') h.memory.lock(record.id, true)
      const imported = h.memory.historyFor(h.context(), record.id)!
      assert.ok(imported.revisions.length > 0)
      assert.ok(imported.revisions.some((revision) => revision.event.legacyImport))
      assert.equal(JSON.stringify(imported).includes('REVOKED_LEGACY_SOURCE_CANARY'), false)
      assertNoHistoricalCanaries(h, 'REVOKED_LEGACY_SOURCE_CANARY')
      h.memory.recall(ids.group, 'REVOKED_LEGACY_SOURCE_CANARY')
      assert.deepEqual(h.memory.historyFor(h.context(), record.id), imported)
      h = f.reopen()
      assert.deepEqual(h.memory.historyFor(h.context(), record.id), imported)
      assertNoHistoricalCanaries(h, 'REVOKED_LEGACY_SOURCE_CANARY')
    } finally {
      f.close()
    }
  })

test('indirect parent recall rebuilds surviving independent child history without revoked parent evidence', () => {
  const f = persistentFixture()
  let h = f.current
  try {
    const parent = h.saved(
      h.propose('INDIRECT_PARENT_BODY_CANARY', [h.source('INDIRECT_REVOKED_SOURCE_CANARY')]),
    )
    const child = h.saved(
      h.propose('Independently surviving child.', [h.source('contextual-child-source')], {}, [
        parent.id,
      ]),
    )
    h.saved(h.propose(child.content, [h.source('independent-child-source')]))
    h.memory.recall(ids.group, 'INDIRECT_REVOKED_SOURCE_CANARY')
    assert.ok(h.memory.list(h.context()).some((record) => record.id === child.id))
    const surviving = h.memory.historyFor(h.context(), child.id)!
    assert.ok(
      surviving.revisions.length > 0,
      'live child with an independent proof needs a safe current history snapshot',
    )
    assert.equal(surviving.revisions.at(-1)?.record.content, child.content)
    for (const revision of surviving.revisions)
      assert.ok(revision.supports.every((proof) => !proof.parents.includes(parent.id)))
    assert.equal(JSON.stringify(surviving).includes('INDIRECT_REVOKED_SOURCE_CANARY'), false)
    assertNoHistoricalCanaries(h, parent.content, 'INDIRECT_REVOKED_SOURCE_CANARY')
    h = f.reopen()
    assert.deepEqual(h.memory.historyFor(h.context(), child.id), surviving)
    assertNoHistoricalCanaries(h, parent.content, 'INDIRECT_REVOKED_SOURCE_CANARY')
    h.memory.recall(ids.group, 'INDIRECT_REVOKED_SOURCE_CANARY')
    assert.deepEqual(h.memory.historyFor(h.context(), child.id), surviving)
  } finally {
    f.close()
  }
})
