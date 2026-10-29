import assert from 'node:assert/strict'
import test from 'node:test'
import {
  ProofInvalidation,
  type ProofInvalidationPorts,
} from '../src/memory/lifecycle/proof-invalidation.ts'

type Record = { id: string; status: string; lineageStatus: string }
type Proof = {
  parents: string[]
  sources: string[]
  invalidatedBy?: string
  parentReferences?: { id: string; version: number; deletionEpoch: number }[]
}
function fixture() {
  const records = new Map<string, Record>()
  const proofs = new Map<string, Proof[]>()
  const historical = new Map<string, Set<string>>()
  const epochs = new Map<string, number>()
  const events: string[] = []
  const ports: ProofInvalidationPorts<Record, Proof> = {
    records: () => [...records.values()],
    proofs: (id) => proofs.get(id) ?? [],
    historyDependents: (id) => historical.get(id) ?? [],
    exists: (id) => records.has(id),
    removeProjection: (id) => {
      events.push(`projection:${id}`)
    },
    purgeHistory: (id) => {
      events.push(`purge:${id}`)
      epochs.set(id, (epochs.get(id) ?? 0) + 1)
      historical.delete(id)
    },
    touch: (id) => {
      events.push(`touch:${id}`)
    },
    saveProofs: (id, value) => {
      proofs.set(id, value)
      events.push(`proofs:${id}`)
    },
    supported: (record, path, cache) => {
      if (!['active', 'disputed'].includes(record.status) || path.has(record.id)) return false
      if (cache.has(record.id)) return cache.get(record.id)!
      const next = new Set(path).add(record.id)
      const supported = (proofs.get(record.id) ?? []).some(
        (proof) =>
          !proof.invalidatedBy &&
          proof.sources.length > 0 &&
          proof.parents.every((id) => {
            const parent = records.get(id)
            return parent !== undefined && ports.supported(parent, next, cache)
          }),
      )
      cache.set(record.id, supported)
      return supported
    },
    invalidateRecord: (record) => {
      records.set(record.id, { ...record, status: 'invalidated' })
      events.push(`invalidate:${record.id}`)
    },
    removeVector: (id) => {
      events.push(`vector:${id}`)
    },
  }
  const add = (id: string, support: Proof[] = [{ sources: [id], parents: [] }]) => {
    records.set(id, { id, status: 'active', lineageStatus: 'verified' })
    proofs.set(id, support)
  }
  return {
    records,
    proofs,
    historical,
    epochs,
    events,
    ports,
    add,
    lifecycle: new ProofInvalidation(ports),
  }
}

test('dependency traversal handles transitive and cyclic proofs without flattening alternatives', () => {
  const f = fixture()
  f.add('a', [{ sources: ['a'], parents: ['b'] }])
  f.add('b', [{ sources: ['b'], parents: ['a', 'c'] }])
  f.add('c')
  assert.equal(f.lifecycle.dependsOn('a', 'c'), true)
  assert.equal(f.lifecycle.dependsOn('a', 'missing'), false)
  assert.equal(f.lifecycle.dependsOn('c', 'a'), false)
  assert.equal(f.lifecycle.dependsOn('c', 'c'), true)
})

test('purge follows historical-only and current dependencies before deleting links and touches OR survivors', () => {
  const f = fixture()
  f.add('parent')
  f.add('child', [{ sources: ['independent'], parents: [] }])
  f.add('grandchild', [{ sources: ['g'], parents: ['child'] }])
  f.historical.set('parent', new Set(['child']))
  f.lifecycle.purgeHistoryDependents('parent')
  assert.deepEqual(
    [...f.epochs],
    [
      ['parent', 1],
      ['child', 1],
      ['grandchild', 1],
    ],
  )
  assert.deepEqual(f.events, [
    'projection:parent',
    'purge:parent',
    'touch:parent',
    'projection:child',
    'purge:child',
    'touch:child',
    'projection:grandchild',
    'purge:grandchild',
    'touch:grandchild',
  ])
  assert.equal(f.records.get('child')!.status, 'active')
  assert.equal(f.ports.supported(f.records.get('child')!, new Set(), new Map()), true)
})

test('shared seen prevents duplicate epochs across roots and cycles, alreadyPurged still removes projections and rebuilds', () => {
  const f = fixture()
  f.add('a', [{ sources: ['a'], parents: ['b'] }])
  f.add('b', [{ sources: ['b'], parents: ['a'] }])
  f.historical.set('a', new Set(['b', 'missing-row']))
  const seen = new Set<string>()
  f.lifecycle.purgeHistoryDependents('a', seen, new Set(['a']))
  f.lifecycle.purgeHistoryDependents('b', seen)
  assert.equal(f.epochs.has('a'), false)
  assert.equal(f.epochs.get('b'), 1)
  assert.equal(f.epochs.get('missing-row'), 1)
  assert.ok(f.events.includes('projection:a'))
  assert.ok(f.events.includes('touch:a'))
  assert.equal(f.events.includes('touch:missing-row'), false)
})

test('invalidation preserves independent OR proof and bound version metadata; AND proof requiring premise fails', () => {
  const f = fixture()
  f.add('parent')
  const references = [{ id: 'parent', version: 1, deletionEpoch: 0 }]
  const independent: Proof = { sources: ['own'], parents: [] }
  f.add('or', [
    { sources: ['dependent'], parents: ['parent'], parentReferences: references },
    independent,
  ])
  f.add('and', [{ sources: ['dependent'], parents: ['parent', 'other'] }])
  f.lifecycle.invalidateDependents('parent')
  assert.equal(f.proofs.get('or')![0]!.invalidatedBy, 'parent')
  assert.deepEqual(f.proofs.get('or')![0]!.parentReferences, references)
  assert.equal(f.proofs.get('or')![1], independent)
  assert.deepEqual(f.lifecycle.refreshSupport(), ['and'])
  assert.equal(f.records.get('or')!.status, 'active')
  assert.equal(f.epochs.has('or'), false)
})

test('refresh purges unsupported descendant once per transaction and repeated refresh is write-free', () => {
  const f = fixture()
  f.add('parent', [])
  f.add('child', [{ sources: ['child'], parents: ['parent'] }])
  f.add('legacy', [])
  f.records.get('legacy')!.lineageStatus = 'unverified'
  assert.deepEqual(f.lifecycle.refreshSupport(), ['parent', 'child'])
  assert.deepEqual(
    [...f.epochs],
    [
      ['parent', 1],
      ['child', 1],
    ],
  )
  assert.equal(f.epochs.has('legacy'), false)
  const before = [...f.events]
  assert.deepEqual(f.lifecycle.refreshSupport(), [])
  assert.deepEqual(f.events, before)
})

test('parent version drift is not interpreted as deletion or provenance loss', () => {
  const f = fixture()
  f.add('parent')
  f.add('child', [
    {
      sources: ['child'],
      parents: ['parent'],
      parentReferences: [{ id: 'parent', version: 1, deletionEpoch: 0 }],
    },
  ])
  // The current parent might now be version 2. This lifecycle layer deliberately
  // has no version-lookup port: current applicability is a separate read concern.
  assert.deepEqual(f.lifecycle.refreshSupport(), [])
  assert.deepEqual(f.events, [])
})

test('port failures propagate so caller transaction can roll back rather than falsely report success', () => {
  const f = fixture()
  f.add('parent', [])
  f.ports.purgeHistory = () => {
    throw new Error('synthetic_transaction_failure')
  }
  assert.throws(() => f.lifecycle.refreshSupport(), /synthetic_transaction_failure/)
  assert.equal(f.records.get('parent')!.status, 'active')
  assert.equal(f.events.includes('vector:parent'), false)
})

test('selective content-change invalidation preserves version-bound proofs while default deletion invalidates both', () => {
  const f = fixture()
  f.add('parent')
  const bound: Proof = {
    sources: ['bound-source'],
    parents: ['parent'],
    parentReferences: [{ id: 'parent', version: 1, deletionEpoch: 0 }],
  }
  const legacy: Proof = { sources: ['legacy-source'], parents: ['parent'] }
  f.add('mixed', [bound, legacy])
  f.add('bound-only', [bound])
  f.lifecycle.invalidateDependents('parent', (proof) => proof.parentReferences === undefined)
  assert.equal(f.proofs.get('mixed')![0], bound)
  assert.equal(f.proofs.get('mixed')![1]!.invalidatedBy, 'parent')
  assert.equal(f.events.includes('proofs:bound-only'), false)
  assert.deepEqual(f.lifecycle.refreshSupport(), [])
  assert.equal(f.epochs.size, 0)
  f.lifecycle.invalidateDependents('parent')
  assert.equal(f.proofs.get('mixed')![0]!.invalidatedBy, 'parent')
  assert.equal(f.proofs.get('bound-only')![0]!.invalidatedBy, 'parent')
  assert.deepEqual(f.proofs.get('bound-only')![0]!.parentReferences, bound.parentReferences)
})
