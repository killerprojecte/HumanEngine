import assert from 'node:assert/strict'
import test from 'node:test'
import { mkdtempSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import {
  MemoryService,
  type MemoryCandidate,
  type MemoryContext,
  type MemoryRecord,
} from '../src/memory.ts'
import { Store } from '../src/store.ts'

function setup(path = ':memory:') {
  const store = new Store(path)
  for (const id of ['a', 'b'])
    store.saveScene({
      id,
      kind: 'group',
      adapterId: 'local',
      accountId: 'bot',
      platformSceneId: id,
      name: id,
      participationEnabled: true,
      learningEnabled: true,
      proactiveEnabled: false,
    })
  const memory = new MemoryService(store)
  function propose(
    content: string,
    messageId: string,
    parents: string[] = [],
    patch: Partial<MemoryCandidate> = {},
    context: Partial<MemoryContext> = {},
  ) {
    const source = { sceneId: context.sceneId ?? 'a', messageId, senderId: 'alice' }
    return memory.propose(
      {
        sceneId: 'a',
        simulation: false,
        provenance: parents.length ? 'contextual' : 'source-only',
        dependencyMemoryIds: parents,
        sourceEvidence: [{ ...source, available: true, isBot: false }],
        ...context,
      },
      { content, kind: 'fact', sources: [source], ...patch },
    )
  }
  function saved(result: ReturnType<typeof propose>): MemoryRecord {
    assert.equal(result.status, 'saved', JSON.stringify(result))
    if (result.status !== 'saved') throw new Error('not_saved')
    return result.memory
  }
  const space = memory.registerSpace({
    providerId: 'local',
    modelId: 'local',
    apiModelName: 'local',
    deployment: 'local',
    dimensions: 2,
    preprocessing: 'plain',
    normalized: true,
  })
  return { store, memory, propose, saved, space }
}

test('Required premises cascade across multiple levels; partial direct recall preserves alternatives and locked descendants cannot bypass recall', () => {
  const { store, memory, propose, saved, space } = setup()
  try {
    const root = saved(propose('Release code is purple canoe', 'root1'))
    saved(propose(root.content, 'root2'))
    const child = saved(propose('The joke did not change purple canoe', 'joke', [root.id]))
    const grandchild = saved(propose('Purple canoe remains confirmed', 'confirmation', [child.id]))
    for (const record of [root, child, grandchild]) memory.index(record.id, space, [1, 0])
    memory.lock(child.id, true)
    memory.recall('a', 'root1')
    assert.equal(memory.search({ sceneId: 'a', simulation: false }, 'purple').length, 3)
    const affected = memory.recall('a', 'root2')
    assert.ok(affected.includes(child.id))
    assert.ok(affected.includes(grandchild.id))
    assert.equal(memory.search({ sceneId: 'a', simulation: false }, 'purple').length, 0)
    assert.equal(
      (store.db.prepare('SELECT count(*) AS n FROM memory_vectors').get() as { n: number }).n,
      0,
    )
    assert.throws(() => memory.index(child.id, space, [1, 0]), /memory_not_indexable/)
    assert.equal(memory.adminList().find((m) => m.id === child.id)?.sources[0]?.messageId, 'joke')
  } finally {
    store.close()
  }
})

test('Independent exact-content proof survives parent recall; contextual proof does not become independent by model omission or semantic merging', () => {
  const { store, memory, propose, saved } = setup()
  try {
    const parent = saved(propose('Code is purple canoe', 'original'))
    const child = saved(propose('Purple canoe is unchanged', 'joke', [parent.id]))
    saved(propose(child.content, 'explicit-independent'))
    assert.equal(
      propose('Still unchanged', 'ambiguous', [], { mergeIntoId: child.id }).status,
      'rejected',
    )
    assert.equal(
      propose(
        'Missing trusted context',
        'missing',
        [],
        {},
        { provenance: 'contextual', dependencyMemoryIds: undefined },
      ).status,
      'rejected',
    )
    memory.recall('a', 'original')
    assert.equal(memory.list({ sceneId: 'a', simulation: false })[0]?.id, child.id)
    memory.recall('a', 'explicit-independent')
    assert.equal(memory.list({ sceneId: 'a', simulation: false }).length, 0)
  } finally {
    store.close()
  }
})

test('Fresh source-only correction survives old preference recall; supersession, manual replacement and delete invalidate dependent proofs', () => {
  for (const action of ['supersede', 'edit', 'delete'] as const) {
    const { store, memory, propose, saved } = setup()
    try {
      const parent = saved(propose('Alice prefers tea', 'tea'))
      const child = saved(propose('Summary: Alice prefers tea', 'summary', [parent.id]))
      if (action === 'supersede') {
        assert.equal(
          propose('Alice prefers coffee', 'coffee', [parent.id], { supersedesId: parent.id })
            .status,
          'rejected',
        )
        const correction = saved(
          propose('Alice prefers coffee', 'coffee', [], { supersedesId: parent.id }),
        )
        memory.recall('a', 'tea')
        assert.ok(
          memory.list({ sceneId: 'a', simulation: false }).some((m) => m.id === correction.id),
        )
      } else if (action === 'edit') memory.update(parent.id, { content: 'Alice prefers coffee' })
      else memory.delete(parent.id)
      assert.equal(memory.adminList().find((m) => m.id === child.id)?.status, 'invalidated')
    } finally {
      store.close()
    }
  }
})

test('Dependency scope, missing parents and cycles are rejected atomically, including alternative-proof cycles', () => {
  const { store, memory, propose, saved } = setup()
  try {
    const parent = saved(propose('Private preference', 'p', [], { personId: 'alice' }))
    assert.equal(propose('Leak', 'leak', [parent.id]).status, 'rejected')
    assert.equal(
      propose('Other person', 'other', [parent.id], { personId: 'bob' }).status,
      'rejected',
    )
    assert.equal(
      propose('Other scene', 'scene', [parent.id], { personId: 'alice' }, { sceneId: 'b' }).status,
      'rejected',
    )
    assert.equal(propose('Missing', 'missing', ['missing']).status, 'rejected')
    const child = saved(propose('Child', 'child', [parent.id], { personId: 'alice' }))
    const before = JSON.stringify(memory.adminList())
    assert.equal(
      propose(parent.content, 'cycle', [child.id], { personId: 'alice' }).status,
      'rejected',
    )
    assert.equal(JSON.stringify(memory.adminList()), before)
    assert.equal(
      memory.promotePublic({ sceneId: 'a', simulation: false }, child.id).status,
      'rejected',
    )
  } finally {
    store.close()
  }
})

test('Legacy migration is restart-safe, nondestructive and conservatively quarantines only relevant unknown learned records', () => {
  const dir = mkdtempSync(join(tmpdir(), 'he-lineage-'))
  const path = join(dir, 'test.sqlite')
  let first = setup(path)
  const unknown = first.saved(first.propose('Unknown borrowed release code', 'joke', [], {}, {}))
  const other = first.saved(
    first.propose('Unrelated other scene', 'other', [], {}, { sceneId: 'b' }),
  )
  const manual = first.memory.create({
    scope: { kind: 'scene', sceneId: 'a' },
    content: 'Manual note',
    kind: 'fact',
    locked: true,
  })
  first.store.db
    .prepare("UPDATE memories SET value=json_remove(value,'$.lineageStatus') WHERE id IN (?,?)")
    .run(unknown.id, other.id)
  first.store.db
    .prepare('DELETE FROM memory_supports WHERE memory_id IN (?,?)')
    .run(unknown.id, other.id)
  first.store.close()
  const second = setup(path)
  try {
    assert.equal(
      second.memory.adminList().find((m) => m.id === unknown.id)?.lineageStatus,
      'unverified',
    )
    second.memory.recall('a', 'old-source-not-retained')
    const quarantined = second.memory.adminList().find((m) => m.id === unknown.id)!
    assert.equal(quarantined.lineageStatus, 'quarantined')
    assert.equal(quarantined.content, unknown.content)
    assert.deepEqual(quarantined.sources, unknown.sources)
    assert.equal(quarantined.status, 'active')
    assert.ok(
      second.memory.list({ sceneId: 'b', simulation: false }).some((m) => m.id === other.id),
    )
    assert.ok(
      second.memory.list({ sceneId: 'a', simulation: false }).some((m) => m.id === manual.id),
    )
    second.memory.lock(unknown.id, true)
    assert.equal(second.memory.search({ sceneId: 'a', simulation: false }, 'borrowed').length, 0)
    second.memory.update(unknown.id, { content: 'Manually revalidated fact' })
    assert.equal(second.memory.search({ sceneId: 'a', simulation: false }, 'revalidated').length, 1)
  } finally {
    second.store.close()
    rmSync(dir, { recursive: true, force: true })
  }
})

test('Support writes and supersession roll back atomically on failure', () => {
  const { store, memory, propose, saved } = setup()
  try {
    const parent = saved(propose('Old', 'old'))
    store.db.exec(
      "CREATE TRIGGER reject_support BEFORE INSERT ON memory_supports BEGIN SELECT RAISE(ABORT,'test_failure'); END",
    )
    assert.throws(() => propose('New', 'new', [], { supersedesId: parent.id }), /test_failure/)
    assert.equal(memory.adminList().length, 1)
    assert.equal(memory.adminList()[0]?.status, 'active')
  } finally {
    store.close()
  }
})

test('Persisted dependency proofs survive restart, and damaged missing/cyclic parents fail closed without writes in simulation', () => {
  for (const damage of ['none', 'missing', 'cycle'] as const) {
    const dir = mkdtempSync(join(tmpdir(), 'he-lineage-restart-'))
    const path = join(dir, 'test.sqlite')
    const first = setup(path)
    const parent = first.saved(first.propose('Restart parent', 'p'))
    const child = first.saved(first.propose('Restart child', 'c', [parent.id]))
    first.memory.index(child.id, first.space, [1, 0])
    first.store.close()
    const second = setup(path)
    try {
      if (damage === 'missing')
        second.store.db.prepare('DELETE FROM memories WHERE id=?').run(parent.id)
      if (damage === 'cycle')
        second.store.db
          .prepare('UPDATE memory_supports SET value=? WHERE memory_id=?')
          .run(JSON.stringify([{ sources: parent.sources, parents: [child.id] }]), parent.id)
      if (damage === 'none') {
        assert.ok(
          second.memory.list({ sceneId: 'a', simulation: false }).some((m) => m.id === child.id),
        )
        second.memory.recall('a', 'p')
      }
      assert.equal(
        second.memory.search({ sceneId: 'a', simulation: false }, 'Restart child').length,
        0,
      )
      assert.equal(second.memory.adminList().find((m) => m.id === child.id)?.status, 'invalidated')
      assert.throws(
        () => second.memory.index(child.id, second.space, [1, 0]),
        /memory_not_indexable/,
      )
    } finally {
      second.store.close()
      rmSync(dir, { recursive: true, force: true })
    }
  }
})

test('Expired premises cannot hide valid top-k simulation results or be revived by metadata changes', () => {
  const { store, memory, propose, saved, space } = setup()
  try {
    const parent = memory.create({
      scope: { kind: 'sharedPublicKnowledge' },
      content: 'expiry parent',
      kind: 'fact',
    })
    const child = saved(propose('expiry derived', 'child', [parent.id]))
    const result = memory.promotePublic({ sceneId: 'a', simulation: false }, child.id)
    assert.equal(result.status, 'saved')
    memory.index(child.id, space, [1, 0])
    const independent = memory.create({
      scope: { kind: 'sharedPublicKnowledge' },
      content: 'expiry independent',
      kind: 'fact',
      confidence: 0.01,
    })
    // A future expiry elapses without any write API triggering dependency cleanup.
    store.db
      .prepare(
        "UPDATE memories SET value=json_set(value,'$.validUntil','2000-01-01T00:00:00Z') WHERE id=?",
      )
      .run(parent.id)
    const before = JSON.stringify(store.db.prepare('SELECT * FROM memories').all())
    const vectors = JSON.stringify(store.db.prepare('SELECT * FROM memory_vectors').all())
    const hits = memory.search({ sceneId: 'simulation', simulation: true }, 'expiry', undefined, {
      spaceId: space,
      vector: [1, 0],
      limit: 1,
    })
    assert.deepEqual(
      hits.map((m) => m.id),
      [independent.id],
    )
    assert.equal(JSON.stringify(store.db.prepare('SELECT * FROM memories').all()), before)
    assert.equal(JSON.stringify(store.db.prepare('SELECT * FROM memory_vectors').all()), vectors)
    memory.update(child.id, { confidence: 1, status: 'active' })
    assert.equal(memory.adminList().find((m) => m.id === child.id)?.status, 'invalidated')
    assert.equal(
      (
        store.db
          .prepare('SELECT count(*) AS n FROM memory_vectors WHERE memory_id=?')
          .get(child.id) as { n: number }
      ).n,
      0,
    )
  } finally {
    store.close()
  }
})

test('Quarantine cannot be cleared by metadata or locking; unchanged explicitly reviewed content revalidates it', () => {
  const { store, memory, propose, saved } = setup()
  try {
    const record = saved(propose('Review me', 'r'))
    store.db
      .prepare(
        "UPDATE memories SET value=json_set(value,'$.lineageStatus','quarantined') WHERE id=?",
      )
      .run(record.id)
    memory.update(record.id, { confidence: 1, status: 'active', kind: 'term' })
    memory.lock(record.id, true)
    assert.equal(memory.search({ sceneId: 'a', simulation: false }, 'Review').length, 0)
    assert.equal(memory.adminList()[0]?.lineageStatus, 'quarantined')
    memory.update(record.id, { content: record.content })
    assert.equal(memory.search({ sceneId: 'a', simulation: false }, 'Review').length, 1)
    assert.equal(memory.adminList()[0]?.origin, 'manual')
    assert.equal(memory.adminList()[0]?.lineageStatus, undefined)
  } finally {
    store.close()
  }
})

test('Legacy shared/person quarantine follows actual source scenes, including metadata-protected unknown records', () => {
  const { store, memory, propose, saved } = setup()
  try {
    const shared = saved(propose('Public technical old fact', 'public'))
    assert.equal(
      memory.promotePublic({ sceneId: 'a', simulation: false }, shared.id).status,
      'saved',
    )
    const personal = saved(
      propose('Old personal preference', 'personal', [], { personId: 'alice' }),
    )
    for (const id of [shared.id, personal.id]) {
      store.db
        .prepare(
          "UPDATE memories SET value=json_set(value,'$.lineageStatus','unverified') WHERE id=?",
        )
        .run(id)
      store.db.prepare('DELETE FROM memory_supports WHERE memory_id=?').run(id)
    }
    memory.update(personal.id, { importance: 'high' })
    memory.recall('b', 'unrelated')
    assert.equal(memory.adminList().filter((m) => m.lineageStatus === 'quarantined').length, 0)
    memory.recall('a', 'old-source')
    assert.equal(memory.adminList().filter((m) => m.lineageStatus === 'quarantined').length, 2)
    assert.equal(memory.list({ sceneId: 'simulation', simulation: true }).length, 0)
    assert.equal(memory.list({ sceneId: 'a', simulation: false }, 'alice').length, 0)
  } finally {
    store.close()
  }
})

test('Migration provenance backfill and table creation roll back together on failure', () => {
  const { store, memory, propose, saved } = setup()
  try {
    const legacy = saved(propose('Legacy migration', 'm'))
    store.db.exec('DROP TABLE memory_supports')
    store.db
      .prepare("UPDATE memories SET value=json_remove(value,'$.lineageStatus') WHERE id=?")
      .run(legacy.id)
    const before = JSON.stringify(memory.adminList())
    store.db.exec(
      "CREATE TRIGGER fail_migration BEFORE UPDATE ON memories BEGIN SELECT RAISE(ABORT,'migration_failure'); END",
    )
    assert.throws(() => new MemoryService(store), /migration_failure/)
    assert.equal(JSON.stringify(memory.adminList()), before)
    assert.equal(
      store.db
        .prepare("SELECT name FROM sqlite_master WHERE type='table' AND name='memory_supports'")
        .get(),
      undefined,
    )
  } finally {
    store.close()
  }
})

test('A multi-source extraction requires all contributing messages unless a separate complete proof exists', () => {
  const { store, memory, propose, saved } = setup()
  try {
    const sources = [
      { sceneId: 'a', messageId: 'left', senderId: 'alice' },
      { sceneId: 'a', messageId: 'right', senderId: 'bob' },
    ]
    const record = saved(
      propose(
        'Alice likes tea and Bob likes coffee',
        'left',
        [],
        { sources },
        { sourceEvidence: sources.map((s) => ({ ...s, available: true, isBot: false })) },
      ),
    )
    memory.recall('a', 'left')
    assert.equal(memory.adminList().find((m) => m.id === record.id)?.status, 'invalidated')
    assert.equal(memory.search({ sceneId: 'a', simulation: false }, 'tea').length, 0)
  } finally {
    store.close()
  }
})

test('Repeated source confidence claims cannot inflate confidence or duplicate proofs; only new authors add a bounded increment', () => {
  const { store, memory, propose, saved } = setup()
  try {
    const initial = saved(
      propose('Stable independently corroborated fact', 'first', [], { confidence: 0.2 }),
    )
    for (let i = 0; i < 20; i++) {
      const repeated = saved(propose(initial.content, 'first', [], { confidence: 0.7 }))
      assert.equal(repeated.confidence, 0.2)
    }
    const proofs = () =>
      JSON.parse(
        (
          store.db
            .prepare('SELECT value FROM memory_supports WHERE memory_id=?')
            .get(initial.id) as { value: string }
        ).value,
      ) as unknown[]
    assert.equal(proofs().length, 1)
    const sameAuthor = saved(
      propose(initial.content, 'same-author-new-message', [], { confidence: 0.99 }),
    )
    assert.equal(sameAuthor.confidence, 0.2)
    assert.equal(proofs().length, 2)
    function corroborate(author: string) {
      const source = { sceneId: 'a', messageId: `corroboration-${author}`, senderId: author }
      return saved(
        propose(
          initial.content,
          source.messageId,
          [],
          { confidence: 1, sources: [source] },
          { sourceEvidence: [{ ...source, available: true, isBot: false }] },
        ),
      )
    }
    assert.equal(corroborate('bob').confidence, 0.25)
    assert.equal(corroborate('bob').confidence, 0.25)
    for (let i = 0; i < 20; i++) assert.ok(corroborate(`independent-${i}`).confidence <= 0.85)
    assert.equal(memory.adminList().find((m) => m.id === initial.id)?.confidence, 0.85)
    assert.equal(memory.update(initial.id, { confidence: 0.95 }).confidence, 0.95)
    assert.equal(propose(initial.content, 'first', [], { confidence: 1 }).status, 'rejected')
  } finally {
    store.close()
  }
})

test('Proof deduplication is order-independent but preserves distinct dependency alternatives', () => {
  const { store, memory, propose, saved } = setup()
  try {
    const a = saved(propose('Premise A', 'a'))
    const b = saved(propose('Premise B', 'b'))
    const sources = [
      { sceneId: 'a', messageId: 'left', senderId: 'alice' },
      { sceneId: 'a', messageId: 'right', senderId: 'bob' },
    ]
    const context = {
      sourceEvidence: sources.map((s) => ({ ...s, available: true, isBot: false })),
    }
    const child = saved(propose('Both premises hold', 'left', [a.id, b.id], { sources }, context))
    saved(
      propose(child.content, 'left', [b.id, a.id], { sources: [...sources].reverse() }, context),
    )
    const proofCount = () =>
      (
        JSON.parse(
          (
            store.db
              .prepare('SELECT value FROM memory_supports WHERE memory_id=?')
              .get(child.id) as { value: string }
          ).value,
        ) as unknown[]
      ).length
    assert.equal(proofCount(), 1)
    saved(propose(child.content, 'left', [a.id], { sources }, context))
    assert.equal(proofCount(), 2)
  } finally {
    store.close()
  }
})
