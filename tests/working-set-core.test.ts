import test from 'node:test'
import assert from 'node:assert/strict'
import Database from 'better-sqlite3'
import { mkdtempSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { WorkingSetService } from '../src/memory/working-set/service.ts'
import { WorkingSetStorage } from '../src/memory/working-set/storage.ts'
import {
  WorkingSetSnapshotSchema,
  type WorkingSetSnapshot,
  type WorkingSetSelection,
  type WorkingSetPolicy,
  type WorkingSetPinPolicy,
} from '../src/memory/working-set/contracts.ts'
import { referenceKey } from '../src/memory/working-set/policy.ts'
const ctx = { sceneId: 's', simulation: false }
function fixture(policy: Partial<WorkingSetPolicy> = {}) {
  const root = mkdtempSync(join(tmpdir(), 'he-working-set-')),
    path = join(root, 'metadata.sqlite')
  let db = new Database(path)
  db.exec(
    "CREATE TABLE authoritative_canary(value TEXT); INSERT INTO authoritative_canary VALUES('body/proof/confidence untouched')",
  )
  let now = 1_000_000,
    enabled = true,
    blocked = false,
    batches = 0,
    writes = 0
  const journal: WorkingSetPinPolicy[] = []
  const current = new Map<string, WorkingSetSnapshot>()
  const ports = {
    assertReady() {
      if (blocked) throw new Error('recovery_blocked')
    },
    enabled: () => enabled,
    now: () => now,
    policy,
    beforePinWrite(policy: WorkingSetPinPolicy) {
      journal.push(structuredClone(policy))
    },
    beforeWrite() {
      writes++
    },
    resolveCurrent(context: typeof ctx, refs: readonly WorkingSetSnapshot['ref'][]) {
      batches++
      return refs.flatMap((ref) => {
        const value = current.get(referenceKey(ref))
        return value && value.sceneId === context.sceneId ? [structuredClone(value)] : []
      })
    },
  }
  let service = new WorkingSetService(db, ports)
  const add = (id: string, extra: Partial<WorkingSetSnapshot> = {}) => {
    const snapshot: WorkingSetSnapshot = {
      ref: { kind: 'assertion', id, version: 1, deletionEpoch: 0 },
      dependencyKey: 'a'.repeat(64),
      sceneId: 's',
      recordedAt: now,
      protection: null,
      ...extra,
    }
    current.set(referenceKey(snapshot.ref), snapshot)
    return { ref: snapshot.ref, dependencyKey: snapshot.dependencyKey }
  }
  return {
    get db() {
      return db
    },
    current,
    journal,
    ports,
    add,
    get service() {
      return service
    },
    restart() {
      db.close()
      db = new Database(path)
      service = new WorkingSetService(db, ports)
    },
    now: () => now,
    advance: (ms: number) => (now += ms),
    enabled: (value: boolean) => (enabled = value),
    block: () => (blocked = true),
    batches: () => batches,
    writes: () => writes,
    close() {
      db.close()
      rmSync(root, { recursive: true, force: true })
    },
  }
}

test('disabled/simulation/empty metadata is lazy; unknown rows remain available rather than disappearing', () => {
  const f = fixture()
  try {
    const a = f.add('a')
    f.enabled(false)
    assert.equal(
      f.service.recordSelected(ctx, { turnId: 't', selectedAt: f.now(), selections: [a] }).coverage,
      'disabled',
    )
    assert.equal(
      (
        f.db
          .prepare("SELECT count(*) n FROM sqlite_master WHERE name LIKE 'memory_working_set_%'")
          .get() as { n: number }
      ).n,
      0,
    )
    f.enabled(true)
    assert.equal(f.service.view(ctx, [a], { includeCold: false }).items[0]!.state, 'unknown')
    assert.equal(f.service.view({ ...ctx, simulation: true }, [a]).coverage, 'disabled')
    assert.equal(f.writes(), 0)
  } finally {
    f.close()
  }
})
test('context_selected is exact turn dedup across restart, not a proof or truth update', () => {
  const f = fixture()
  try {
    const a = f.add('a'),
      selection = { turnId: 't', selectedAt: f.now(), selections: [a] }
    assert.equal(f.service.recordSelected(ctx, selection).recorded, 1)
    assert.equal(f.service.recordSelected(ctx, selection).duplicates, 1)
    f.restart()
    assert.equal(f.service.recordSelected(ctx, selection).duplicates, 1)
    assert.equal(f.service.view(ctx, [a]).items[0]!.contextSelectedCount, 1)
    assert.deepEqual(f.db.prepare('SELECT * FROM authoritative_canary').all(), [
      { value: 'body/proof/confidence untouched' },
    ])
    const old = f.current.get(referenceKey(a.ref))!
    old.dependencyKey = 'b'.repeat(64)
    assert.equal(f.service.recordSelected(ctx, { ...selection, turnId: 'stale' }).unavailable, 1)
    assert.equal(
      f.service.recordSelected(ctx, {
        ...selection,
        selections: [{ ...a, dependencyKey: old.dependencyKey }],
      }).duplicates,
      1,
    )
    assert.equal(f.service.view(ctx, [a]).items.length, 0)
  } finally {
    f.close()
  }
})
test('pin CAS and conservative policy affect only accessibility; cold and review are explicit', () => {
  const f = fixture({ coldAfterMs: 100, reviewAfterMs: 50 })
  try {
    const a = f.add('a', { epistemicKind: 'hypothesis' })
    f.service.enqueue(ctx, [a])
    f.service.drain(ctx)
    f.advance(101)
    const cold = f.service.view(ctx, [a]).items[0]!
    assert.equal(cold.state, 'cold')
    assert.equal(cold.reviewDue, true)
    const hotOnly = f.service.view(ctx, [a], { includeCold: false })
    assert.equal(hotOnly.coverage, 'limited')
    assert.equal(hotOnly.omittedCold, 1)
    const pinned = f.service.setPinned(ctx, a, {
      expectedRevision: cold.pinRevision,
      pinned: true,
    })
    assert.equal(f.service.view(ctx, [a]).items[0]!.state, 'hot')
    assert.throws(
      () => f.service.setPinned(ctx, a, { expectedRevision: cold.pinRevision, pinned: false }),
      /policy_conflict/,
    )
    f.current.clear()
    assert.equal(f.service.view(ctx, [a]).items.length, 0)
    assert.equal(
      f.service.setPinned(ctx, a, { expectedRevision: pinned.policyRevision, pinned: false })
        .pinned,
      false,
    )
  } finally {
    f.close()
  }
})
test('typed open/needs-review and unresolved coverage remain hot but never confer source authority', () => {
  const f = fixture({ coldAfterMs: 10 })
  try {
    const a = f.add('a', { protection: 'unresolved_commitment_coverage' })
    f.service.enqueue(ctx, [a])
    f.service.drain(ctx)
    f.advance(11)
    assert.equal(f.service.view(ctx, [a]).items[0]!.state, 'hot')
    assert.equal(f.service.view(ctx, [a]).coverage, 'limited')
    const plan = f.add('plan', {
      ref: { kind: 'commitment', id: 'plan', version: 2, deletionEpoch: 0, mutationVersion: 4 },
      protection: 'open_commitment',
    })
    f.service.enqueue(ctx, [plan])
    f.service.drain(ctx)
    assert.equal(f.service.view(ctx, [plan]).items[0]!.state, 'hot')
    assert.throws(
      () => f.service.recordSelected(ctx, { turnId: 't', selectedAt: f.now(), selections: [plan] }),
      /plan_metrics_unsupported/,
    )
    f.current.clear()
    assert.equal(f.service.view(ctx, [a, plan]).items.length, 0)
  } finally {
    f.close()
  }
})
test('leases are bounded, restartable, exact-source qualified and invalidation cannot resurrect', () => {
  const f = fixture({ maxBatch: 2, leaseMs: 10 })
  try {
    const a = f.add('a'),
      b = f.add('b'),
      c = f.add('c')
    assert.equal(f.service.enqueue(ctx, [a, b, c]).coverage, 'limited')
    const leases = f.service.claim(ctx)
    assert.equal(leases.length, 2)
    assert.equal(f.service.claim(ctx).length, 0)
    f.restart()
    f.advance(11)
    const replacement = f.service.claim(ctx)
    assert.equal(f.service.complete(ctx, leases).completed, 0)
    f.service.invalidate('s', [a.ref])
    f.service.enqueue(ctx, [a])
    assert.equal(f.service.complete(ctx, replacement).completed, 1)
    assert.equal(f.service.view(ctx, [a]).items[0]!.state, 'unknown')
    assert.equal(f.service.drain(ctx).completed, 1)
    const before = f.batches()
    assert.equal(f.service.drain(ctx).completed, 0)
    assert.equal(f.batches(), before)
  } finally {
    f.close()
  }
})
test('same assertion ref with edited dependencies rejects old lease without upgrading its witness', () => {
  const f = fixture()
  try {
    const a = f.add('a')
    f.service.enqueue(ctx, [a])
    const old = f.service.claim(ctx)
    const changed = { ...f.current.get(referenceKey(a.ref))!, dependencyKey: 'c'.repeat(64) }
    f.current.set(referenceKey(a.ref), changed)
    assert.equal(f.service.complete(ctx, old).completed, 0)
    assert.equal(
      f.service.view(ctx, [{ ref: changed.ref, dependencyKey: changed.dependencyKey }]).items[0]!
        .state,
      'unknown',
    )
    f.service.enqueue(ctx, [{ ref: changed.ref, dependencyKey: changed.dependencyKey }])
    assert.equal(f.service.drain(ctx).completed, 1)
    assert.equal(f.service.view(ctx, [a]).items.length, 0)
  } finally {
    f.close()
  }
})
test('queue creation rolls back with authoritative transaction and OFF invalidation remains effective', () => {
  const f = fixture()
  try {
    const a = f.add('a')
    assert.throws(
      () =>
        f.db.transaction(() => {
          f.service.enqueue(ctx, [a])
          throw Error('rollback')
        })(),
      /rollback/,
    )
    assert.equal(f.service.storage.exists(), false)
    f.service.enqueue(ctx, [a])
    const lease = f.service.claim(ctx)
    f.enabled(false)
    f.service.cancelMaintenance()
    f.service.invalidate('s', [a.ref])
    f.enabled(true)
    assert.equal(f.service.complete(ctx, lease).completed, 0)
    assert.equal(f.service.view(ctx, [a]).items[0]!.state, 'unknown')
    f.block()
    assert.throws(() => f.service.view(ctx, [a]), /recovery_blocked/)
  } finally {
    f.close()
  }
})
test('selection age, capacity, scope and batch limits fail honestly without evicting live dedup', () => {
  const f = fixture({ maxBatch: 2, maxReceipts: 1 })
  try {
    const a = f.add('a'),
      b = f.add('b'),
      other = f.add('foreign', { sceneId: 'elsewhere' })
    assert.equal(
      f.service.recordSelected(ctx, { turnId: 't', selectedAt: f.now(), selections: [a, b] })
        .coverage,
      'limited',
    )
    assert.equal(f.service.view(ctx, [a, b, other]).coverage, 'limited')
    assert.equal(f.service.view(ctx, [a, b]).items.length, 2)
    assert.equal(f.service.view(ctx, [other]).unavailable, 1)
    assert.throws(
      () =>
        f.service.recordSelected(ctx, {
          turnId: 'future',
          selectedAt: f.now() + 1,
          selections: [a],
        }),
      /selection_invalid/,
    )
    assert.throws(
      () => f.service.recordSelected(ctx, { turnId: 'old', selectedAt: -1, selections: [a] }),
      /selection_invalid/,
    )
    assert.equal(
      f.service.recordSelected(ctx, { turnId: 't', selectedAt: f.now(), selections: [a] })
        .duplicates,
      1,
    )
  } finally {
    f.close()
  }
})
test('metadata schemas refuse copied body and future tables; recovery reset preserves explicit policy', () => {
  const f = fixture()
  try {
    const a = f.add('a')
    assert.equal(
      WorkingSetSnapshotSchema.safeParse({
        ...f.current.get(referenceKey(a.ref)),
        body: 'not metadata',
      }).success,
      false,
    )
    f.service.recordSelected(ctx, { turnId: 't', selectedAt: f.now(), selections: [a] })
    f.service.setPinned(ctx, a, { expectedRevision: 0, pinned: true })
    f.service.enqueue(ctx, [a])
    f.db.transaction(() => f.service.storage.reset())()
    assert.equal(f.service.view(ctx, [a]).items[0]!.state, 'hot')
    assert.equal(f.service.view(ctx, [a]).items[0]!.contextSelectedCount, 0)
    assert.equal(f.service.storage.pending('s'), 0)
    f.db.exec('CREATE TABLE memory_working_set_future(value TEXT)')
    assert.throws(() => new WorkingSetStorage(f.db).assertSupported(), /schema_unsupported/)
  } finally {
    f.close()
  }
})

test('policy changes take effect without restart or losing jobs; missing current authority is limited', () => {
  const f = fixture({ coldAfterMs: 100, reviewAfterMs: 100 })
  try {
    const a = f.add('a', { epistemicKind: 'hypothesis' }),
      b = f.add('b')
    f.service.enqueue(ctx, [a])
    f.service.drain(ctx)
    f.advance(50)
    f.service.enqueue(ctx, [b])
    const pending = f.service.storage.pending('s')
    assert.equal(f.service.view(ctx, [a]).items[0]!.state, 'hot')
    f.service.configurePolicy({ coldAfterMs: 20, reviewAfterMs: 20 })
    const changed = f.service.view(ctx, [a]).items[0]!
    assert.equal(changed.state, 'cold')
    assert.equal(changed.reviewDue, true)
    assert.equal(f.service.storage.pending('s'), pending)
    assert.throws(() => f.service.configurePolicy({ coldAfterMs: 0 }), /policy_invalid/)
    f.current.delete(referenceKey(a.ref))
    const missing = f.service.view(ctx, [a])
    assert.equal(missing.coverage, 'limited')
    assert.equal(missing.unavailable, 1)
  } finally {
    f.close()
  }
})

test('completed exact projection stays clean across repeated commit signals and database restart', () => {
  const f = fixture()
  try {
    const a = f.add('a')
    assert.equal(f.service.enqueue(ctx, [a]).queued, 1)
    assert.equal(f.service.enqueue(ctx, [a]).queued, 0)
    assert.equal(f.service.drain(ctx).completed, 1)
    f.restart()
    const writes = f.writes()
    assert.equal(f.service.enqueue(ctx, [a]).queued, 0)
    assert.equal(f.service.drain(ctx).completed, 0)
    assert.equal(f.writes(), writes)
  } finally {
    f.close()
  }
})

test('durable pin policy survives statistics, stale anchors remain unknown/hot, explicit unpin and terminal tombstones are ordered', () => {
  const f = fixture({ coldAfterMs: 10 })
  try {
    const a = f.add('a')
    const pinned = f.service.setPinned(ctx, a, { expectedRevision: 0, pinned: true })
    f.service.recordSelected(ctx, { turnId: 't', selectedAt: f.now(), selections: [a] })
    assert.equal(f.service.view(ctx, [a]).items[0]!.pinRevision, pinned.policyRevision)
    f.service.invalidate('s', [a.ref])
    f.advance(11)
    const updated = f.add('a', {
      ref: { kind: 'assertion', id: 'a', version: 2, deletionEpoch: 1 },
      dependencyKey: 'b'.repeat(64),
    })
    f.service.enqueue(ctx, [updated])
    f.service.drain(ctx)
    const stale = f.service.view(ctx, [updated])
    assert.equal(stale.coverage, 'limited')
    assert.equal(stale.items[0]!.state, 'hot')
    assert.equal(stale.items[0]!.pinned, false)
    assert.equal(stale.items[0]!.pinIntent, true)
    assert.equal(stale.items[0]!.pinStatus, 'stale')
    assert.equal(stale.items[0]!.policyCoverage, 'unknown')
    f.current.clear()
    f.enabled(false)
    const unpin = f.service.setPinned(ctx, a, { expectedRevision: 1, pinned: false })
    assert.equal(unpin.policyRevision, 2)
    f.enabled(true)
    f.current.set(referenceKey(updated.ref), {
      ref: updated.ref,
      dependencyKey: updated.dependencyKey,
      sceneId: 's',
      recordedAt: f.now(),
      protection: null,
    })
    const repin = f.service.setPinned(ctx, updated, { expectedRevision: 2, pinned: true })
    assert.equal(repin.policyRevision, 3)
    f.service.terminalInvalidate('s', [updated.ref])
    assert.deepEqual(
      f.journal.map((p) => [p.policyRevision, p.pinned, p.terminal]),
      [
        [1, true, false],
        [2, false, false],
        [3, true, false],
        [4, false, true],
      ],
    )
    assert.throws(
      () => f.service.setPinned(ctx, updated, { expectedRevision: 4, pinned: true }),
      /policy_terminal/,
    )
    f.restart()
    assert.equal(f.service.storage.pin('s', updated.ref)!.terminal, true)
  } finally {
    f.close()
  }
})

test('unknown plan recorded time is never replaced by epoch-zero or now; absent time stays unknown', () => {
  const f = fixture({ coldAfterMs: 10, reviewAfterMs: 10 })
  try {
    const plan = f.add('p', {
      ref: { kind: 'commitment', id: 'p', version: 1, deletionEpoch: 0, mutationVersion: 1 },
      recordedAt: null,
      epistemicKind: 'hypothesis',
    })
    f.service.enqueue(ctx, [plan])
    f.service.drain(ctx)
    f.advance(1000)
    const view = f.service.view(ctx, [plan]).items[0]!
    assert.equal(view.state, 'unknown')
    assert.equal(view.reviewDue, false)
    assert.equal(f.service.storage.item('s', plan.ref)!.snapshot.recordedAt, null)
  } finally {
    f.close()
  }
})

test('pin policy rejects SQLite BLOB and pins cannot become a route to stale body', () => {
  const f = fixture()
  try {
    const a = f.add('a')
    f.service.setPinned(ctx, a, { expectedRevision: 0, pinned: true })
    const row = f.db.prepare('SELECT value FROM memory_working_set_pins').get() as { value: string }
    f.db.prepare('UPDATE memory_working_set_pins SET value=?').run(Buffer.from(row.value))
    assert.throws(() => f.service.view(ctx, [a]), /storage_invalid/)
  } finally {
    f.close()
  }
})

test('pin intent is synchronous and local policy rolls back after a durable hook failure', () => {
  const f = fixture()
  try {
    const a = f.add('a')
    f.service.setPinned(ctx, a, { expectedRevision: 0, pinned: true })
    f.ports.beforePinWrite = (policy) => {
      f.journal.push(structuredClone(policy))
      throw new Error('synthetic_intent_pending')
    }
    assert.throws(
      () => f.service.setPinned(ctx, a, { expectedRevision: 1, pinned: false }),
      /intent_pending/,
    )
    assert.equal(f.service.storage.pin('s', a.ref)!.policyRevision, 1)
    assert.equal(f.service.storage.pin('s', a.ref)!.pinned, true)
    assert.equal(f.journal.at(-1)!.policyRevision, 2)
    f.ports.beforePinWrite = async () => {}
    assert.throws(
      () => f.service.setPinned(ctx, a, { expectedRevision: 1, pinned: false }),
      /async_hook_denied/,
    )
    assert.equal(f.service.storage.pin('s', a.ref)!.policyRevision, 1)
  } finally {
    f.close()
  }
})

test('policy management is bounded and scope checked while OFF, without resolving deleted source body', () => {
  const f = fixture()
  try {
    const a = f.add('a'),
      b = f.add('b')
    f.service.setPinned(ctx, a, { expectedRevision: 0, pinned: true })
    f.service.setPinned(ctx, b, { expectedRevision: 0, pinned: true })
    f.current.clear()
    f.enabled(false)
    const batches = f.batches()
    const page = f.service.listPinPolicies(ctx, { limit: 1 })
    assert.equal(page.coverage, 'limited')
    assert.equal(page.items.length, 1)
    assert.equal(f.batches(), batches)
    assert.equal(f.service.listPinPolicies({ sceneId: 'other', simulation: false }).items.length, 0)
    assert.equal(f.service.listPinPolicies({ ...ctx, simulation: true }).items.length, 0)
    f.service.terminalInvalidate('s', [a.ref])
    assert.equal(f.service.listPinPolicies(ctx).items.length, 1)
    f.db
      .prepare('UPDATE memory_working_set_pins SET entity_id=? WHERE entity_id=?')
      .run('wrong', 'b')
    assert.throws(() => f.service.listPinPolicies(ctx), /storage_invalid/)
  } finally {
    f.close()
  }
})

test('claim rejects inconsistent SQL job locators before any lease or cross-scene write', () => {
  for (const field of ['sceneId', 'kind', 'id'] as const) {
    const f = fixture()
    try {
      const a = f.add('a')
      f.service.enqueue(ctx, [a])
      const row = f.db.prepare('SELECT value FROM memory_working_set_jobs').get() as {
        value: string
      }
      const value = JSON.parse(row.value)
      if (field === 'sceneId') value.snapshot.sceneId = 'foreign'
      else value.snapshot.ref[field] = field === 'kind' ? 'commitment' : 'wrong'
      if (field === 'kind') value.snapshot.ref.mutationVersion = 1
      const corrupt = JSON.stringify(value)
      f.db.prepare('UPDATE memory_working_set_jobs SET value=?').run(corrupt)
      assert.throws(() => f.service.claim(ctx, 1), /storage_invalid/)
      assert.deepEqual(
        f.db.prepare('SELECT scene_id,kind,entity_id,value FROM memory_working_set_jobs').all(),
        [{ scene_id: 's', kind: 'assertion', entity_id: 'a', value: corrupt }],
      )
    } finally {
      f.close()
    }
  }
})
