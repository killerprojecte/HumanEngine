import test from 'node:test'
import assert from 'node:assert/strict'
import { Store } from '../src/store.ts'
import { MemoryService } from '../src/memory.ts'
import { AssertionHistory } from '../src/memory/storage/assertion-history.ts'

test('facade commits final AND/OR proofs with revisions, not intermediate writes; reads remain scoped', () => {
  const store = new Store(':memory:')
  try {
    store.saveScene({
      id: 'a',
      kind: 'group',
      adapterId: 'fixture',
      accountId: 'fixture',
      platformSceneId: 'a',
      name: 'A',
      participationEnabled: false,
      learningEnabled: true,
      proactiveEnabled: false,
    })
    const memory = new MemoryService(store)
    const propose = (messageId: string) =>
      memory.propose(
        {
          sceneId: 'a',
          simulation: false,
          provenance: 'source-only',
          sourceEvidence: [
            { sceneId: 'a', messageId, senderId: 'alice', available: true, isBot: false },
          ],
        },
        {
          content: 'Synthetic preference',
          kind: 'fact',
          personId: 'alice',
          sources: [{ sceneId: 'a', messageId, senderId: 'alice' }],
        },
      )
    const first = propose('one')
    assert.equal(first.status, 'saved')
    if (first.status !== 'saved') throw Error('fixture')
    const id = first.memory.id
    assert.equal(
      memory.historyFor({ sceneId: 'a', simulation: false }, id)?.revisions[0]?.supports.length,
      1,
    )
    propose('two')
    const history = memory.historyFor({ sceneId: 'a', simulation: false }, id)!
    assert.equal(history.version, 2)
    assert.equal(history.revisions.at(-1)?.supports.length, 2)
    assert.equal(memory.historyFor({ sceneId: 'b', simulation: false }, id), undefined)
    assert.equal(memory.historyFor({ sceneId: 'a', simulation: true }, id), undefined)
    memory.recall('a', 'one')
    const after = memory.historyFor({ sceneId: 'a', simulation: false }, id)!
    assert.ok(after.deletionEpoch > 0)
    assert.ok(
      after.revisions.every((revision) =>
        revision.record.sources.every((source) => source.messageId !== 'one'),
      ),
    )
    memory.delete(id)
    assert.equal(memory.historyFor({ sceneId: 'a', simulation: false }, id), undefined)
    const raw = new AssertionHistory(store.db)
    assert.deepEqual(raw.revisions(id, 'scene:a'), [])
    assert.equal(raw.state(id).deleted, true)
  } finally {
    store.close()
  }
})

test('manual revision history records admin assertion without fabricating actor or occurrence/validity', () => {
  const store = new Store(':memory:')
  try {
    const memory = new MemoryService(store)
    const record = memory.create({
      scope: { kind: 'scene', sceneId: 'a', personId: 'alice' },
      kind: 'claim',
      content: 'Synthetic manual claim',
    })
    memory.update(record.id, { content: 'Corrected synthetic manual claim' })
    const history = memory.historyFor({ sceneId: 'a', simulation: false }, record.id)!
    assert.equal(history.version, 2)
    assert.equal(history.revisions[0]!.record.content, 'Synthetic manual claim')
    assert.equal(history.revisions[1]!.record.content, 'Corrected synthetic manual claim')
    for (const revision of history.revisions) {
      assert.equal(revision.event.kind, 'admin_assertion')
      assert.equal(revision.event.actorId, null)
      assert.equal(revision.validFrom, null)
      assert.equal(revision.record.occurredAt, undefined)
    }
  } finally {
    store.close()
  }
})

test('guarded corrections reject stale versions and epochs; exact retries do not duplicate revisions', () => {
  const store = new Store(':memory:')
  try {
    const memory = new MemoryService(store)
    const row = memory.create({
      scope: { kind: 'scene', sceneId: 'a' },
      kind: 'claim',
      content: 'Original',
    })
    const before = memory.revisionState(row.id)
    const guard = {
      expectedVersion: before.version,
      expectedDeletionEpoch: before.deletionEpoch,
      idempotencyKey: 'synthetic-correction',
    }
    const first = memory.updateGuarded(row.id, { content: 'Correction' }, guard)
    const after = memory.revisionState(row.id)
    assert.equal(after.version, before.version + 1)
    assert.deepEqual(memory.updateGuarded(row.id, { content: 'Correction' }, guard), first)
    assert.deepEqual(memory.revisionState(row.id), after)
    assert.throws(
      () => memory.updateGuarded(row.id, { content: 'Other' }, guard),
      /memory_command_conflict/,
    )
    assert.throws(
      () =>
        memory.updateGuarded(row.id, { content: 'Stale' }, { ...guard, idempotencyKey: 'new-key' }),
      /assertion_version_conflict/,
    )
    memory.delete(row.id)
    assert.throws(
      () => memory.updateGuarded(row.id, { content: 'Correction' }, guard),
      /memory_command_superseded/,
    )
    assert.equal(
      (
        store.db
          .prepare('SELECT COUNT(*) AS n FROM memory_assertion_revisions WHERE value IS NOT NULL')
          .get() as { n: number }
      ).n,
      0,
    )
  } finally {
    store.close()
  }
})

test('explicit validity start is distinct from recorded time and cannot become a current fact early', () => {
  const store = new Store(':memory:')
  try {
    const memory = new MemoryService(store)
    const row = memory.create({
      scope: { kind: 'scene', sceneId: 'a', personId: 'alice' },
      kind: 'claim',
      content: 'Future scoped hypothesis',
      validFrom: '2099-01-01T00:00:00Z',
      validUntil: '2100-01-01T00:00:00Z',
      epistemicKind: 'hypothesis',
    })
    const ctx = { sceneId: 'a', simulation: false }
    assert.deepEqual(memory.list(ctx), [])
    assert.deepEqual(memory.search(ctx, 'Future'), [])
    assert.equal(
      memory.adminList()[0]!.status,
      'active',
      'not-yet-applicable is not evidence invalidation',
    )
    assert.equal(memory.adminList()[0]!.epistemicKind, 'hypothesis')
    memory.update(row.id, { validFrom: '2020-01-01T00:00:00Z' })
    assert.equal(memory.list(ctx)[0]!.id, row.id)
    const history = memory.historyFor(ctx, row.id)!
    assert.equal(history.revisions[0]!.validFrom, '2099-01-01T00:00:00.000Z')
    assert.notEqual(history.revisions[0]!.recordedAt, history.revisions[0]!.validFrom)
    assert.equal(history.revisions[0]!.record.occurredAt, undefined)
  } finally {
    store.close()
  }
})
