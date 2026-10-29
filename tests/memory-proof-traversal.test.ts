import assert from 'node:assert/strict'
import test from 'node:test'
import { mkdtempSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { Store } from '../src/store.ts'
import { MemoryService, type MemoryRecord, type MemorySource } from '../src/memory.ts'
import type { ProofParentReference, SupportProof } from '../src/memory/domain/proofs.ts'
import {
  hasProvenance,
  ProofTraversalLimit,
  type ProvenancePorts,
} from '../src/memory/read/provenance.ts'
import { isCurrentlyApplicable } from '../src/memory/read/applicability.ts'

const sceneId = 'proof-traversal-scene'
const ctx = { sceneId, simulation: false }
const source = (messageId: string): MemorySource => ({
  sceneId,
  messageId,
  senderId: 'synthetic-person',
})
const record = (id: string, verified = true): MemoryRecord => ({
  id,
  scope: { kind: 'scene', sceneId },
  kind: 'claim',
  content: `Synthetic traversal record ${id}.`,
  sources: [source(id)],
  importance: 'medium',
  confidence: 0.5,
  learnedAt: '2026-10-28T00:00:00.000Z',
  status: 'active',
  locked: false,
  origin: verified ? 'learned' : 'manual',
  ...(verified ? { lineageStatus: 'verified' as const } : {}),
})
const proof = (id: string, parents: string[] = []): SupportProof => ({
  sources: [source(id)],
  parents,
})
function graph(records: MemoryRecord[], supports: Record<string, SupportProof[]>) {
  const byId = new Map(records.map((item) => [item.id, item]))
  let reads = 0
  const ports: ProvenancePorts = {
    record: (id) => byId.get(id),
    proofs: (id) => {
      reads++
      return supports[id] ?? []
    },
    recalled: () => false,
    parentAllowed: (child, parent) =>
      parent.scope.kind === 'sharedPublicKnowledge' ||
      (child.scope.kind === 'scene' &&
        parent.scope.sceneId === child.scope.sceneId &&
        (!parent.scope.personId || parent.scope.personId === child.scope.personId)),
  }
  return { ports, reads: () => reads }
}
const isLimit = (error: unknown) =>
  error instanceof ProofTraversalLimit && error.message === 'memory_proof_budget_exhausted'

test('provenance traversal preserves a same-scope diamond with bounded repeated work and rejects a foreign parent', () => {
  const records = ['A', 'B', 'C', 'D'].map((id) => record(id))
  const h = graph(records, {
    A: [proof('A', ['B', 'C'])],
    B: [proof('B', ['D'])],
    C: [proof('C', ['D'])],
    D: [proof('D')],
  })
  assert.equal(hasProvenance(records[0]!, h.ports), true)
  assert.equal(h.reads(), 4, 'the shared diamond premise is traversed only once')
  records[3]!.scope = { kind: 'scene', sceneId: 'foreign-scene' }
  assert.equal(hasProvenance(records[0]!, h.ports), false)
})

test('a cycle-dependent false result cannot poison a later complete OR alternative', () => {
  const a = record('A'),
    b = record('B')
  const h = graph([a, b], {
    A: [proof('A-cycle', ['B']), proof('A-independent')],
    B: [proof('B', ['A'])],
  })
  const cache = new Map<string, boolean>()
  const budget = { remaining: 4096 }
  assert.equal(hasProvenance(a, h.ports, new Set(), cache, budget), true)
  assert.equal(cache.has('B'), false, 'path-dependent false must not be memoized')
  assert.equal(hasProvenance(b, h.ports, new Set(), cache, budget), true)
  assert.ok([...cache.values()].every((value) => value === true))
})

test('applicability does not cache a temporary cycle failure across an independent OR anchor', () => {
  const a = record('A'),
    b = record('B')
  const h = graph([a, b], {
    A: [proof('A-cycle', ['B']), proof('A-independent')],
    B: [proof('B', ['A'])],
  })
  const ports = { ...h.ports, supported: () => true }
  const cache = new Map<string, boolean>()
  assert.equal(isCurrentlyApplicable(a, ports, Date.now(), new Set(), cache), true)
  assert.equal(cache.has('B'), false)
  assert.equal(isCurrentlyApplicable(b, ports, Date.now(), new Set(), cache), true)
  assert.equal(isCurrentlyApplicable(b, ports, Date.now()), true)
  assert.ok([...cache.values()].every((value) => value === true))
})

test('deep and broad provenance graphs throw a bounded error instead of claiming missing provenance', () => {
  const records = Array.from({ length: 66 }, (_, index) => record(`depth-${index}`))
  const supports = Object.fromEntries(
    records.map((item, index) => [
      item.id,
      [proof(item.id, index ? [records[index - 1]!.id] : [])],
    ]),
  )
  const deep = graph(records, supports)
  assert.throws(() => hasProvenance(records.at(-1)!, deep.ports), isLimit)
  const root = record('fanout-root')
  const parents = Array.from({ length: 2050 }, (_, index) => record(`leaf-${index}`, false))
  const wide = graph([root, ...parents], {
    [root.id]: [
      proof(
        root.id,
        parents.map((parent) => parent.id),
      ),
    ],
  })
  assert.throws(() => hasProvenance(root, wide.ports), isLimit)
})

test('shared traversal budget remains shared even on successful cache hits', () => {
  const item = record('cached')
  const h = graph([item], { [item.id]: [proof(item.id)] })
  const cache = new Map<string, boolean>(),
    budget = { remaining: 4 }
  assert.equal(hasProvenance(item, h.ports, new Set(), cache, budget), true)
  assert.equal(hasProvenance(item, h.ports, new Set(), cache, budget), true)
  assert.throws(() => hasProvenance(item, h.ports, new Set(), cache, budget), isLimit)
  assert.equal(h.reads(), 1)
  assert.equal(
    hasProvenance(item, h.ports),
    true,
    'a later authorized read has a fresh bounded traversal',
  )
})

function database() {
  const directory = mkdtempSync(join(tmpdir(), 'he-proof-traversal-'))
  const path = join(directory, 'synthetic.sqlite')
  let store: Store, memory: MemoryService
  const open = () => {
    store = new Store(path)
    if (!store.scene(sceneId))
      store.saveScene({
        id: sceneId,
        kind: 'group',
        adapterId: 'offline',
        accountId: 'offline-bot',
        platformSceneId: 'synthetic',
        name: 'Synthetic traversal',
        participationEnabled: false,
        learningEnabled: true,
        proactiveEnabled: false,
      })
    store.set('settings', { ...store.settings, memoryProjectionEnabled: true })
    memory = new MemoryService(store)
  }
  open()
  const add = (content: string, sourceId: string, refs: ProofParentReference[] = []) => {
    const evidence = source(sourceId)
    const result = memory.propose(
      {
        ...ctx,
        provenance: refs.length ? 'contextual' : 'source-only',
        dependencyMemoryIds: refs.map((ref) => ref.id),
        dependencyParentReferences: refs,
        sourceEvidence: [{ ...evidence, available: true, isBot: false }],
      },
      { content, kind: 'claim', sources: [evidence] },
    )
    assert.equal(result.status, 'saved', JSON.stringify(result))
    if (result.status !== 'saved') throw new Error('synthetic_fixture_failed')
    return result.memory
  }
  const capture = (item: MemoryRecord) => {
    const ref = memory.captureParentReference(ctx, item)
    assert.ok(ref)
    return ref
  }
  const snapshot = () =>
    JSON.stringify(
      (
        store.db
          .prepare(
            "SELECT name FROM sqlite_master WHERE type='table' AND (name LIKE 'memory_%' OR name IN ('memories','recall_tombstones')) ORDER BY name",
          )
          .all() as { name: string }[]
      ).map(({ name }) => [
        name,
        store.db
          .prepare(`SELECT * FROM ${name}`)
          .all()
          .map((row) => JSON.stringify(row))
          .sort(),
      ]),
    )
  // Build a prior-version fixture without using the new bound-write admission API.
  // Current proof, latest historical proof and reverse indexes remain aligned.
  const replaceStoredProof = (item: MemoryRecord, proofs: SupportProof[]) => {
    store.db.transaction(() => {
      const sources = [
        ...new Map(
          proofs.flatMap((value) => value.sources).map((value) => [value.messageId, value]),
        ).values(),
      ]
      const next = {
        ...item,
        sources,
        origin: 'learned' as const,
        lineageStatus: 'verified' as const,
      }
      store.db.prepare('UPDATE memories SET value=? WHERE id=?').run(JSON.stringify(next), item.id)
      store.db
        .prepare('INSERT OR REPLACE INTO memory_supports VALUES(?,?)')
        .run(item.id, JSON.stringify(proofs))
      store.db.prepare('DELETE FROM memory_sources WHERE memory_id=?').run(item.id)
      const insertSource = store.db.prepare('INSERT INTO memory_sources VALUES(?,?,?,?)')
      for (const value of sources)
        insertSource.run(item.id, value.sceneId, value.messageId, value.senderId)
      const version = memory.revisionState(item.id).version
      const row = store.db
        .prepare('SELECT value FROM memory_assertion_revisions WHERE assertion_id=? AND version=?')
        .get(item.id, version) as { value: string }
      const revision = JSON.parse(row.value)
      revision.record = next
      revision.supports = proofs
      store.db
        .prepare('UPDATE memory_assertion_revisions SET value=? WHERE assertion_id=? AND version=?')
        .run(JSON.stringify(revision), item.id, version)
      for (const table of [
        'memory_assertion_revision_parents',
        'memory_assertion_revision_sources',
      ])
        store.db
          .prepare(`DELETE FROM ${table} WHERE assertion_id=? AND version=?`)
          .run(item.id, version)
      const insertParent = store.db.prepare(
        'INSERT OR IGNORE INTO memory_assertion_revision_parents VALUES(?,?,?)',
      )
      for (const id of proofs.flatMap((value) => value.parents))
        insertParent.run(item.id, version, id)
      const insertRevisionSource = store.db.prepare(
        'INSERT OR IGNORE INTO memory_assertion_revision_sources VALUES(?,?,?,?)',
      )
      for (const value of sources)
        insertRevisionSource.run(item.id, version, value.sceneId, value.messageId)
    })()
  }
  const retained = (id: string) =>
    (
      store.db
        .prepare(
          'SELECT COUNT(*) AS n FROM memory_assertion_revisions WHERE assertion_id=? AND value IS NOT NULL',
        )
        .get(id) as { n: number }
    ).n
  const indexed = (id: string) =>
    (
      store.db.prepare('SELECT COUNT(*) AS n FROM memory_fts WHERE assertion_id=?').get(id) as {
        n: number
      }
    ).n
  return {
    get store() {
      return store
    },
    get memory() {
      return memory
    },
    add,
    capture,
    snapshot,
    replaceStoredProof,
    retained,
    indexed,
    reopen: () => {
      store.close()
      open()
    },
    close: () => {
      store.close()
      rmSync(directory, { recursive: true, force: true })
    },
  }
}

test('deep legacy traversal failure rolls back earlier invalidation, history purge, epochs and shadow indexes; reviewed repair restores reads', () => {
  const h = database()
  try {
    const nodes = Array.from({ length: 66 }, (_, index) =>
      h.add(`DEEP_LEGACY_CANARY_${index}`, `deep-source-${index}`),
    )
    const unavailable = h.add('ROLLBACK_BEFORE_LIMIT_CANARY', 'already-recalled-source')
    nodes.forEach((item, index) =>
      h.replaceStoredProof(item, [
        { sources: item.sources, parents: index ? [nodes[index - 1]!.id] : [] },
      ]),
    )
    h.store.recall(sceneId, 'already-recalled-source')
    const originalState = h.memory.revisionState(unavailable.id)
    assert.equal(h.indexed(unavailable.id), 1)
    const before = h.snapshot()
    for (const read of [
      () => h.memory.list(ctx),
      () => h.memory.search(ctx, 'DEEP_LEGACY_CANARY'),
      () => h.memory.historyFor(ctx, nodes.at(-1)!.id),
    ]) {
      assert.throws(read, isLimit)
      assert.equal(
        h.snapshot(),
        before,
        'unknown due to work limits must never become a destructive false',
      )
      assert.deepEqual(h.memory.revisionState(unavailable.id), originalState)
      assert.equal(h.indexed(unavailable.id), 1)
    }
    h.reopen()
    assert.throws(() => h.memory.list(ctx), isLimit)
    assert.equal(h.snapshot(), before)
    h.memory.update(nodes[32]!.id, { content: nodes[32]!.content })
    const visible = h.memory.list(ctx)
    assert.ok(visible.some((item) => item.id === nodes.at(-1)!.id))
    assert.equal(
      visible.some((item) => item.id === unavailable.id),
      false,
    )
    assert.equal(
      h.indexed(unavailable.id),
      0,
      'the genuine recall is applied only after a complete successful traversal',
    )
    assert.ok(h.retained(nodes.at(-1)!.id) > 0)
    assert.equal(h.memory.revisionState(nodes.at(-1)!.id).deletionEpoch, 0)
    assert.equal(
      h.memory.adminList().find((item) => item.id === nodes.at(-1)!.id)!.status,
      'active',
    )
    h.reopen()
    assert.ok(h.memory.list(ctx).some((item) => item.id === nodes.at(-1)!.id))
  } finally {
    h.close()
  }
})

test('high source fanout is bounded without purging retained history or invalidating a recoverable record', () => {
  const h = database()
  try {
    const item = h.add('HIGH_FANOUT_HISTORY_CANARY', 'fanout-root')
    h.replaceStoredProof(item, [
      {
        sources: Array.from({ length: 4200 }, (_, index) => source(`fanout-${index}`)),
        parents: [],
      },
    ])
    const before = h.snapshot(),
      state = h.memory.revisionState(item.id)
    assert.throws(() => h.memory.list(ctx), isLimit)
    assert.equal(h.snapshot(), before)
    assert.deepEqual(h.memory.revisionState(item.id), state)
    assert.equal(h.indexed(item.id), 1)
    h.reopen()
    assert.throws(() => h.memory.historyFor(ctx, item.id), isLimit)
    assert.equal(h.snapshot(), before)
    h.memory.update(item.id, { content: item.content })
    assert.ok(h.memory.list(ctx).some((record) => record.id === item.id))
    assert.equal(h.memory.revisionState(item.id).deletionEpoch, state.deletionEpoch)
    assert.ok(h.retained(item.id) > 0)
  } finally {
    h.close()
  }
})

test('historical exact-parent fanout shares its page budget and raises a limit rather than returning complete empty history', () => {
  const h = database()
  try {
    const parents = Array.from({ length: 64 }, (_, index) =>
      h.memory.create({
        scope: { kind: 'scene', sceneId },
        content: `Bound fanout parent ${index}.`,
        kind: 'claim',
      }),
    )
    const refs = parents.map((parent) => h.capture(parent))
    const item = h.add('BOUND_HISTORY_FANOUT_CANARY', 'bound-fanout-child', refs)
    for (let revision = 1; revision <= 35; revision++)
      h.memory.update(item.id, { confidence: revision / 100 })
    const before = h.snapshot(),
      state = h.memory.revisionState(item.id)
    assert.throws(() => h.memory.historyFor(ctx, item.id), isLimit)
    assert.equal(h.snapshot(), before)
    h.reopen()
    assert.throws(() => h.memory.historyFor(ctx, item.id), isLimit)
    assert.equal(h.snapshot(), before)
    h.memory.update(item.id, { content: item.content })
    assert.ok(h.memory.list(ctx).some((record) => record.id === item.id))
    assert.equal(h.memory.revisionState(item.id).deletionEpoch, state.deletionEpoch)
    assert.ok(
      h.retained(item.id) >= 36,
      'reviewing current content must not erase bounded-out history',
    )
    assert.throws(
      () => h.memory.historyFor(ctx, item.id),
      isLimit,
      'retained over-budget history remains explicitly unavailable, not falsely complete',
    )
    assert.equal(h.indexed(item.id), 1)
  } finally {
    h.close()
  }
})

test('an OR survivor gains a new epoch on recall but cannot resurrect a grandchild bound to its old epoch, including history and FTS after restart', () => {
  const h = database()
  try {
    const p = h.add('Recalled premise P.', 'p'),
      r = h.add('Independent premise R.', 'r')
    let child = h.add('OR_SURVIVOR_CANARY', 'c1', [h.capture(p)])
    child = h.add(child.content, 'c2', [h.capture(r)])
    const oldChild = h.capture(child)
    const grandchild = h.add('OLD_EPOCH_GRANDCHILD_CANARY', 'g', [oldChild])
    assert.ok(h.memory.list(ctx).some((record) => record.id === grandchild.id))
    assert.equal(h.indexed(grandchild.id), 1)
    h.memory.recall(sceneId, 'p')
    for (let read = 0; read < 2; read++) {
      assert.ok(h.memory.list(ctx).some((record) => record.id === child.id))
      assert.ok(h.memory.revisionState(child.id).deletionEpoch > oldChild.deletionEpoch)
      assert.equal(
        h.memory.list(ctx).some((record) => record.id === grandchild.id),
        false,
      )
      assert.equal(
        h.memory.adminList().find((record) => record.id === grandchild.id)!.status,
        'invalidated',
      )
      assert.equal(h.memory.historyFor(ctx, grandchild.id), undefined)
      assert.equal(h.indexed(grandchild.id), 0)
      assert.equal(h.retained(grandchild.id), 0)
      assert.equal(
        JSON.stringify(
          h.store.db.prepare('SELECT value FROM memory_assertion_revisions').all(),
        ).includes(grandchild.content),
        false,
      )
      const before = h.snapshot()
      h.memory.recall(sceneId, 'p')
      assert.equal(h.snapshot(), before, 'repeated recall must not change survivor epochs again')
      h.reopen()
    }
  } finally {
    h.close()
  }
})
