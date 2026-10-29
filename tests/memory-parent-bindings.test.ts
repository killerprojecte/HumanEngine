import assert from 'node:assert/strict'
import test from 'node:test'
import Database from 'better-sqlite3'
import {
  parentBindingState,
  validateNewProof,
  canonicalProofKey,
  type SupportProof,
} from '../src/memory/domain/proofs.ts'
import { evaluateProofParents, type ProofParentPorts } from '../src/memory/read/proof-parents.ts'
import { AssertionHistory } from '../src/memory/storage/assertion-history.ts'
import type { MemoryRecord } from '../src/memory/domain/contracts.ts'

const proof = (): SupportProof => ({
  sources: [],
  parents: ['p'],
  parentReferences: [{ id: 'p', version: 1, deletionEpoch: 0 }],
})
const ports = (): ProofParentPorts => ({
  current: () => ({ version: 1, deletionEpoch: 0, deleted: false }),
  revision: () => true,
})

test('new bindings require exact unique parent set and positive captured versions; legacy stays unknown', () => {
  assert.equal(parentBindingState({ sources: [], parents: ['p'] }), 'legacy_unknown')
  assert.throws(
    () => validateNewProof({ sources: [], parents: ['p'] }, 'contextual'),
    /binding_required/,
  )
  validateNewProof(proof(), 'contextual')
  validateNewProof({ sources: [], parents: [], parentReferences: [] }, 'source-only')
  assert.throws(() => validateNewProof(proof(), 'source-only'), /source_only/)
  for (const change of [
    { parentReferences: [] },
    { parents: ['p', 'p'] },
    { parentReferences: [{ id: 'other', version: 1, deletionEpoch: 0 }] },
    { parentReferences: [{ id: 'p', version: 0, deletionEpoch: 0 }] },
    { parentReferences: [{ id: 'p', version: 1, deletionEpoch: -1 }] },
    { parentReferences: [{ id: 'p', version: 1.5, deletionEpoch: 0 }] },
  ])
    assert.equal(parentBindingState({ ...proof(), ...change }), 'invalid')
})

test('canonical keys distinguish revisions, epochs, legacy and AND groups independent of ordering', () => {
  const a: SupportProof = {
    sources: [{ sceneId: 's', messageId: 'm', senderId: 'u' }],
    parents: ['p', 'q'],
    parentReferences: [
      { id: 'p', version: 1, deletionEpoch: 0 },
      { id: 'q', version: 2, deletionEpoch: 0 },
    ],
  }
  assert.equal(
    canonicalProofKey(a),
    canonicalProofKey({
      ...a,
      parents: [...a.parents].reverse(),
      parentReferences: [...a.parentReferences!].reverse(),
    }),
  )
  assert.notEqual(canonicalProofKey(a), canonicalProofKey({ ...a, parentReferences: undefined }))
  assert.notEqual(
    canonicalProofKey(proof()),
    canonicalProofKey({
      ...proof(),
      parentReferences: [{ id: 'p', version: 2, deletionEpoch: 0 }],
    }),
  )
  assert.notEqual(
    canonicalProofKey(proof()),
    canonicalProofKey({
      ...proof(),
      parentReferences: [{ id: 'p', version: 1, deletionEpoch: 1 }],
    }),
  )
})

test('current version drift is read-inapplicable while authorized same-epoch historical proof remains inspectable', () => {
  const p = ports()
  p.current = () => ({ version: 2, deletionEpoch: 0, deleted: false })
  assert.equal(evaluateProofParents(proof(), 'current', p).reason, 'parent_version_changed')
  assert.equal(evaluateProofParents(proof(), 'history', p).available, true)
  p.revision = () => false
  assert.equal(evaluateProofParents(proof(), 'history', p).reason, 'parent_revision_unavailable')
  p.current = () => ({ version: 2, deletionEpoch: 1, deleted: false })
  assert.equal(evaluateProofParents(proof(), 'history', p).reason, 'parent_epoch_changed')
})

test('current authorization and deletion precede historical lookup, including legacy proofs', () => {
  const p = ports()
  let lookups = 0
  p.revision = () => {
    lookups++
    return true
  }
  p.current = () => undefined
  for (const mode of ['current', 'history'] as const) {
    assert.equal(evaluateProofParents(proof(), mode, p).available, false)
    assert.equal(evaluateProofParents({ sources: [], parents: ['p'] }, mode, p).available, false)
  }
  assert.equal(lookups, 0)
  p.current = () => ({ version: 1, deletionEpoch: 0, deleted: true })
  assert.equal(evaluateProofParents(proof(), 'history', p).reason, 'parent_unavailable')
  assert.deepEqual(evaluateProofParents({ sources: [], parents: ['p'] }, 'current', ports()), {
    available: true,
    reason: 'legacy_unknown',
  })
  assert.equal(
    evaluateProofParents({ ...proof(), invalidatedBy: 'recalled' }, 'history', ports()).reason,
    'invalidated',
  )
})

test('all bound parents are AND, separate alternate proofs can survive one stale parent', () => {
  const both = {
    ...proof(),
    parents: ['p', 'q'],
    parentReferences: [...proof().parentReferences!, { id: 'q', version: 1, deletionEpoch: 0 }],
  }
  const p = ports()
  p.current = (id) => ({ version: id === 'q' ? 2 : 1, deletionEpoch: 0, deleted: false })
  assert.equal(evaluateProofParents(both, 'current', p).available, false)
  assert.equal(
    [both, proof()].some((group) => evaluateProofParents(group, 'current', p).available),
    true,
  )
})

test('exact revision storage preserves bindings and rejects wrong scope, purged bodies, terminal IDs', () => {
  const db = new Database(':memory:')
  const history = new AssertionHistory(db)
  try {
    const record: MemoryRecord = {
      id: 'child',
      scope: { kind: 'scene', sceneId: 's' },
      kind: 'claim',
      content: 'Synthetic assertion',
      sources: [],
      importance: 'medium',
      confidence: 0.7,
      learnedAt: '2026-01-01T00:00:00.000Z',
      status: 'active',
      locked: false,
      origin: 'manual',
    }
    db.transaction(() => history.importLegacy(record, [proof()]))()
    assert.deepEqual(history.revision('child', 1, 'scene:s')?.supports, [proof()])
    assert.equal(history.revision('child', 1, 'scene:foreign'), undefined)
    assert.equal(history.revision('child', 2, 'scene:s'), undefined)
    const copy = history.revision('child', 1, 'scene:s')!
    copy.supports[0]!.parentReferences![0]!.version = 99
    assert.equal(
      history.revision('child', 1, 'scene:s')!.supports[0]!.parentReferences![0]!.version,
      1,
    )
    db.transaction(() => history.purge('child'))()
    assert.equal(history.revision('child', 1, 'scene:s'), undefined)
    db.transaction(() => history.erase('child'))()
    assert.equal(history.revision('child', 1, 'scene:s'), undefined)
  } finally {
    db.close()
  }
})

test('matching current state cannot authorize a missing or redacted exact revision', () => {
  const p = ports()
  p.revision = () => false
  assert.equal(evaluateProofParents(proof(), 'current', p).reason, 'parent_revision_unavailable')
})

test('canonical tuples containing commas remain permutation invariant', () => {
  const a: SupportProof = {
    sources: [
      { sceneId: 's', messageId: 'm,x', senderId: 'y' },
      { sceneId: 's', messageId: 'm', senderId: 'x,y' },
    ],
    parents: [],
  }
  assert.equal(canonicalProofKey(a), canonicalProofKey({ ...a, sources: [...a.sources].reverse() }))
})
test('malformed persisted parent bindings fail closed without throwing', () => {
  for (const parentReferences of [null, {}, [null], ['p']]) {
    assert.equal(
      parentBindingState({ ...proof(), parentReferences } as unknown as SupportProof),
      'invalid',
    )
  }
  assert.equal(
    parentBindingState({ ...proof(), parents: null } as unknown as SupportProof),
    'invalid',
  )
})
test('legacy explicit validFrom survives import and history limit is opt-in', () => {
  const db = new Database(':memory:')
  const history = new AssertionHistory(db)
  try {
    const record: MemoryRecord = {
      id: 'timed',
      scope: { kind: 'scene', sceneId: 's' },
      kind: 'claim',
      content: 'Synthetic',
      sources: [],
      importance: 'low',
      confidence: 0.5,
      learnedAt: '2026-01-01T00:00:00.000Z',
      validFrom: '2026-02-01T00:00:00.000Z',
      status: 'active',
      locked: false,
      origin: 'manual',
    }
    db.transaction(() => history.importLegacy(record, []))()
    assert.equal(history.revision('timed', 1, 'scene:s')?.validFrom, record.validFrom)
    db.transaction(() =>
      history.append({
        record: { ...record, content: 'Revised synthetic' },
        supports: [],
        expectedVersion: 1,
        expectedDeletionEpoch: 0,
        idempotencyKey: 'v2',
        recordedAt: '2026-01-02T00:00:00.000Z',
        validFrom: record.validFrom!,
        validTo: null,
        eventKind: 'admin_assertion',
      }),
    )()
    assert.deepEqual(
      history.revisions('timed', 'scene:s').map((r) => r.version),
      [1, 2],
    )
    assert.deepEqual(
      history.revisions('timed', 'scene:s', 1).map((r) => r.version),
      [2],
    )
    assert.deepEqual(history.revisions('timed', 'foreign', 1), [])
    for (const limit of [0, -1, 1.5, 10001, NaN])
      assert.throws(() => history.revisions('timed', 'scene:s', limit), /limit_invalid/)
  } finally {
    db.close()
  }
})

test('exact revision rejects corrupt payload identity, version, scope and malformed JSON', () => {
  const db = new Database(':memory:')
  const history = new AssertionHistory(db)
  try {
    const record: MemoryRecord = {
      id: 'exact',
      scope: { kind: 'scene', sceneId: 's' },
      kind: 'claim',
      content: 'Synthetic',
      sources: [],
      importance: 'low',
      confidence: 0.5,
      learnedAt: '2026-01-01T00:00:00.000Z',
      status: 'active',
      locked: false,
      origin: 'manual',
    }
    db.transaction(() => history.importLegacy(record, []))()
    const original = history.revision('exact', 1, 'scene:s')!
    for (const faulty of [
      { ...original, assertionId: 'other' },
      { ...original, version: 2 },
      { ...original, record: { ...record, id: 'other' } },
      { ...original, record: { ...record, scope: { kind: 'scene', sceneId: 'other' } } },
      { ...original, record: { ...record, scope: { kind: 'not-a-scope' } } },
      { ...original, record: null },
      null,
    ]) {
      db.prepare('UPDATE memory_assertion_revisions SET value=? WHERE assertion_id=?').run(
        JSON.stringify(faulty),
        'exact',
      )
      assert.equal(history.revision('exact', 1, 'scene:s'), undefined)
    }
    db.prepare('UPDATE memory_assertion_revisions SET value=? WHERE assertion_id=?').run(
      '{broken',
      'exact',
    )
    assert.equal(history.revision('exact', 1, 'scene:s'), undefined)
    db.prepare('UPDATE memory_assertion_revisions SET value=? WHERE assertion_id=?').run(
      JSON.stringify(original),
      'exact',
    )
    assert.equal(history.revision('exact', 1, 'scene:s')?.record.id, 'exact')
  } finally {
    db.close()
  }
})

test('revision list omits corrupt rows without widening the bounded SQL window', () => {
  const db = new Database(':memory:')
  const history = new AssertionHistory(db)
  try {
    const record: MemoryRecord = {
      id: 'list',
      scope: { kind: 'scene', sceneId: 's' },
      kind: 'claim',
      content: 'Synthetic',
      sources: [],
      importance: 'low',
      confidence: 0.5,
      learnedAt: '2026-01-01T00:00:00.000Z',
      status: 'active',
      locked: false,
      origin: 'manual',
    }
    db.transaction(() => {
      history.importLegacy(record, [])
      for (let version = 2; version <= 4; version++)
        history.append({
          record: { ...record, content: 'Synthetic ' + version },
          supports: [],
          expectedVersion: version - 1,
          expectedDeletionEpoch: 0,
          idempotencyKey: 'v' + version,
          recordedAt: record.learnedAt,
          validFrom: null,
          validTo: null,
          eventKind: 'admin_assertion',
        })
    })()
    const wrong = history.revision('list', 2, 'scene:s')!
    db.prepare(
      'UPDATE memory_assertion_revisions SET value=? WHERE assertion_id=? AND version=2',
    ).run(JSON.stringify({ ...wrong, record: { ...wrong.record, id: 'foreign' } }), 'list')
    db.prepare(
      'UPDATE memory_assertion_revisions SET value=? WHERE assertion_id=? AND version=4',
    ).run('{broken', 'list')
    assert.deepEqual(
      history.revisions('list', 'scene:s').map((r) => r.version),
      [1, 3],
    )
    assert.deepEqual(
      history.revisions('list', 'scene:s', 2).map((r) => r.version),
      [3],
    )
    assert.deepEqual(history.revisions('list', 'scene:s', 1), [])
    assert.deepEqual(history.revisions('list', 'scene:other'), [])
  } finally {
    db.close()
  }
})
