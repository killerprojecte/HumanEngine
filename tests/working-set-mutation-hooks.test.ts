import assert from 'node:assert/strict'
import test from 'node:test'
import Database from 'better-sqlite3'
import { WorkingSetService } from '../src/memory/working-set/service.ts'
import { WorkingSetMutationHooks } from '../src/memory/working-set/mutation-hooks.ts'
import type {
  WorkingSetReference,
  WorkingSetSnapshot,
} from '../src/memory/working-set/contracts.ts'
const ref = (id = 'a'): WorkingSetReference => ({
  kind: 'assertion',
  id,
  version: 1,
  deletionEpoch: 0,
})
const snapshot = (sceneId = 's', id = 'a'): WorkingSetSnapshot => ({
  sceneId,
  ref: ref(id),
  dependencyKey: 'a'.repeat(64),
  recordedAt: 1,
  protection: null,
})
function fixture() {
  const db = new Database(':memory:')
  const values = new Map<string, WorkingSetSnapshot>()
  let enabled = true,
    resolves = 0,
    writes = 0
  const key = (scene: string, id: string) => JSON.stringify([scene, id])
  const resolve = (ctx: { sceneId: string }, refs: readonly WorkingSetReference[]) => {
    assert.equal(db.inTransaction, true)
    resolves++
    return refs.flatMap((r) => {
      const value = values.get(key(ctx.sceneId, r.id))
      return value ? [value] : []
    })
  }
  const access = new WorkingSetService(db, {
    assertReady: () => {},
    enabled: () => enabled,
    beforeWrite: () => {
      writes++
    },
    beforePinWrite: () => {},
    resolveCurrent: resolve,
    now: () => 1000,
  })
  const pages: Array<string | undefined> = []
  const hooks = new WorkingSetMutationHooks(db, {
    assertReady: () => {},
    enabled: () => enabled,
    beforeWrite: () => {
      writes++
    },
    storage: access.storage,
    resolveCommitted: resolve,
    invalidate: (scene, refs) => access.invalidate(scene, refs),
    terminalInvalidate: (scene, refs) => access.terminalInvalidate(scene, refs),
    affectedAssertionScenes: (id, after, limit) => {
      pages.push(after)
      if (!access.storage.exists()) return []
      return (
        db
          .prepare(
            `SELECT scene_id FROM (
        SELECT scene_id FROM memory_working_set_pins WHERE kind='assertion' AND entity_id=?
        UNION SELECT scene_id FROM memory_working_set_items WHERE kind='assertion' AND entity_id=?
        UNION SELECT scene_id FROM memory_working_set_jobs WHERE kind='assertion' AND entity_id=?
        UNION SELECT json_extract(value,'$.sceneId') AS scene_id FROM memory_working_set_receipts
          WHERE json_extract(value,'$.selection.ref.kind')='assertion' AND json_extract(value,'$.selection.ref.id')=?
      ) WHERE scene_id>? ORDER BY scene_id LIMIT ?`,
          )
          .all(id, id, id, id, after ?? '', limit) as { scene_id: string }[]
      ).map((r) => r.scene_id)
    },
    now: () => 1000,
  })
  return {
    db,
    access,
    hooks,
    pages,
    put: (s: WorkingSetSnapshot) => values.set(key(s.sceneId, s.ref.id), s),
    remove: (scene: string, id: string) => values.delete(key(scene, id)),
    enable: (on: boolean) => {
      enabled = on
    },
    counts: () => ({ resolves, writes }),
    tx: <T>(fn: () => T) => db.transaction(fn)(),
  }
}
const ctx = { sceneId: 's', simulation: false }
test('committed snapshots enqueue once in caller transaction; rollback, clean restart and precise source watermark are respected', () => {
  const f = fixture()
  try {
    f.put(snapshot())
    assert.throws(() => f.hooks.committed(ctx, [ref()]), /transaction_required/)
    assert.throws(
      () =>
        f.tx(() => {
          f.hooks.committed(ctx, [ref()])
          throw Error('rollback')
        }),
      /rollback/,
    )
    assert.equal(f.access.storage.exists(), false)
    assert.equal(f.tx(() => f.hooks.committed(ctx, [ref()])).queued, 1)
    const generation = f.access.storage.job('s', ref())!.generation
    assert.equal(f.tx(() => f.hooks.committed(ctx, [ref(), ref()])).queued, 0)
    assert.equal(f.access.storage.job('s', ref())!.generation, generation)
    assert.equal(f.access.drain(ctx).completed, 1)
    assert.equal(f.tx(() => f.hooks.committed(ctx, [ref()])).queued, 0)
    // The assertion version stays constant, but the real source dependency watermark changes.
    f.put({ ...snapshot(), dependencyKey: 'b'.repeat(64) })
    assert.equal(f.tx(() => f.hooks.committed(ctx, [ref()])).queued, 1)
    assert.equal(f.access.storage.job('s', ref())!.snapshot.ref.version, 1)
    assert.equal(f.access.storage.job('s', ref())!.snapshot.dependencyKey, 'b'.repeat(64))
    assert.throws(
      () =>
        f.tx(() =>
          f.hooks.committed(
            ctx,
            Array.from({ length: 41 }, () => ref()),
          ),
        ),
      /mutation_limit/,
    )
    f.put({ ...snapshot(), ref: { ...ref(), version: 2 } })
    assert.throws(() => f.tx(() => f.hooks.committed(ctx, [ref()])), /authority_invalid/)
  } finally {
    f.db.close()
  }
})
test('source edit invalidates leased old work while retaining pin; OFF cleanup does not resolve or enqueue', () => {
  const f = fixture()
  try {
    f.put(snapshot())
    const selected = { ref: ref(), dependencyKey: 'a'.repeat(64) }
    f.access.setPinned(ctx, selected, { expectedRevision: 0, pinned: true })
    f.tx(() => f.hooks.committed(ctx, [ref()]))
    const old = f.access.claim(ctx)
    f.put({ ...snapshot(), dependencyKey: 'b'.repeat(64) })
    const result = f.tx(() => f.hooks.sourceEdited(ctx, [ref()]))
    assert.equal(result.queued, 1)
    assert.equal(result.invalidatedTargets, 1)
    assert.equal(f.access.storage.pin('s', ref())!.pinned, true)
    assert.equal(f.access.storage.pin('s', ref())!.terminal, false)
    assert.equal(f.access.complete(ctx, old).completed, 0)
    assert.equal(f.access.drain(ctx).completed, 1)
    f.enable(false)
    const before = f.counts().resolves
    assert.equal(f.tx(() => f.hooks.sourceEdited(ctx, [ref()])).coverage, 'disabled')
    assert.equal(f.counts().resolves, before)
    assert.equal(f.access.storage.item('s', ref()), undefined)
    assert.equal(f.access.storage.pin('s', ref())!.pinned, true)
    f.enable(true)
    f.remove('s', 'a')
    const unavailable = f.tx(() => f.hooks.sourceEdited(ctx, [ref()]))
    assert.equal(unavailable.unavailable, 1)
    assert.equal(unavailable.queued, 0)
  } finally {
    f.db.close()
  }
})
test('terminal deletion clears every matching scene policy past page 40 but preserves other IDs; repeated cleanup is idempotent', () => {
  const f = fixture()
  try {
    for (let i = 0; i < 45; i++) {
      const s = snapshot(`scene-${String(i).padStart(3, '0')}`)
      f.put(s)
      f.access.setPinned(
        { sceneId: s.sceneId, simulation: false },
        { ref: s.ref, dependencyKey: s.dependencyKey },
        {
          expectedRevision: 0,
          pinned: true,
        },
      )
      f.tx(() => f.hooks.committed({ sceneId: s.sceneId, simulation: false }, [s.ref]))
    }
    f.put(snapshot('scene-000', 'independent'))
    f.access.setPinned(
      { sceneId: 'scene-000', simulation: false },
      { ref: ref('independent'), dependencyKey: 'a'.repeat(64) },
      { expectedRevision: 0, pinned: true },
    )
    f.enable(false)
    assert.equal(f.tx(() => f.hooks.terminalAssertionDeleted('a')).scenes, 45)
    assert.deepEqual(f.pages, [undefined, 'scene-039'])
    for (let i = 0; i < 45; i++) {
      const scene = `scene-${String(i).padStart(3, '0')}`,
        policy = f.access.storage.pin(scene, ref())!
      assert.equal(policy.terminal, true)
      assert.equal(policy.pinned, false)
      assert.equal(policy.policyRevision, 2)
      assert.equal(f.access.storage.job(scene, ref()), undefined)
    }
    assert.equal(f.access.storage.pin('scene-000', ref('independent'))!.pinned, true)
    f.tx(() => f.hooks.terminalAssertionDeleted('a'))
    assert.equal(f.access.storage.pin('scene-044', ref())!.policyRevision, 2)
  } finally {
    f.db.close()
  }
})
test('simulation creates no metadata schema and a failed outer transaction cannot leave a job', () => {
  const f = fixture()
  try {
    f.put(snapshot())
    assert.equal(
      f.tx(() => f.hooks.committed({ ...ctx, simulation: true }, [ref()])).coverage,
      'disabled',
    )
    assert.equal(
      f.tx(() => f.hooks.sourceEdited({ ...ctx, simulation: true }, [ref()])).invalidatedTargets,
      0,
    )
    assert.equal(f.access.storage.exists(), false)
    assert.deepEqual(f.counts(), { resolves: 0, writes: 0 })
  } finally {
    f.db.close()
  }
})

test('privacy unavailability clears items, jobs and receipts across every scene but preserves pin intent', () => {
  const f = fixture()
  try {
    for (let i = 0; i < 45; i++) {
      const sceneId = `privacy-${String(i).padStart(3, '0')}`
      const s = snapshot(sceneId)
      f.put(s)
      const selection = { ref: s.ref, dependencyKey: s.dependencyKey }
      f.access.setPinned({ sceneId, simulation: false }, selection, {
        expectedRevision: 0,
        pinned: true,
      })
      f.tx(() => f.hooks.committed({ sceneId, simulation: false }, [s.ref]))
      f.access.drain({ sceneId, simulation: false })
      f.put({ ...s, dependencyKey: 'b'.repeat(64) })
      f.tx(() => {
        f.hooks.committed({ sceneId, simulation: false }, [s.ref])
        f.access.storage.putReceipt(`receipt-${i}`, {
          schemaVersion: 1,
          sceneId,
          turnId: `turn-${i}`,
          selection,
          selectedAt: 1000,
        })
      })
      assert.ok(f.access.storage.item(sceneId, s.ref))
      assert.ok(f.access.storage.job(sceneId, s.ref))
    }
    f.enable(false)
    assert.throws(() => f.hooks.invalidateAssertion('a'), /transaction_required/)
    assert.equal(f.tx(() => f.hooks.invalidateAssertion('a')).scenes, 45)
    assert.deepEqual(f.pages, [undefined, 'privacy-039'])
    assert.equal(f.access.storage.receiptCount(), 0)
    for (let i = 0; i < 45; i++) {
      const sceneId = `privacy-${String(i).padStart(3, '0')}`
      assert.equal(f.access.storage.item(sceneId, ref()), undefined)
      assert.equal(f.access.storage.job(sceneId, ref()), undefined)
      const policy = f.access.storage.pin(sceneId, ref())!
      assert.equal(policy.pinned, true)
      assert.equal(policy.terminal, false)
      assert.equal(policy.policyRevision, 1)
    }
  } finally {
    f.db.close()
  }
})
