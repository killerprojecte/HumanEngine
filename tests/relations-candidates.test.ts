import assert from 'node:assert/strict'
import test from 'node:test'
import Database from 'better-sqlite3'
import { RelationCandidates } from '../src/memory/relations/candidates.ts'
import type {
  RelationAssertionView,
  RelationAssertionReference,
  RelationContext,
  RelationProposal,
} from '../src/memory/domain/relations.ts'
const ctx: RelationContext = { sceneId: 'scene', simulation: false, provenance: 'source-only' }
const ref = (id = 'assertion'): RelationAssertionReference => ({ id, version: 1, deletionEpoch: 0 })
function fixture() {
  const db = new Database(':memory:')
  let enabled = false,
    auto = false,
    learning = true,
    sequence = 0
  const views = new Map<
    string,
    { reference: RelationAssertionReference; view: RelationAssertionView }
  >()
  const add = (id = 'assertion', personId = 'person', kind: 'self' | 'mentioned' = 'self') => {
    const source = {
      sceneId: 'scene',
      messageId: 'source-' + id,
      senderId: kind === 'self' ? personId : 'reporter',
    }
    const view: RelationAssertionView = {
      record: {
        id,
        scope: { kind: 'scene', sceneId: 'scene', personId },
        kind: kind === 'self' ? 'fact' : 'claim',
        content: 'Synthetic participation assertion',
        sources: [source],
        importance: 'low',
        confidence: 0.5,
        learnedAt: '2026-10-28T00:00:00.000Z',
        epistemicKind: kind === 'self' ? 'explicit' : 'reported',
        status: 'active',
        locked: false,
        origin: 'learned',
        lineageStatus: 'verified',
      },
      proofs: [
        {
          proofId: 'proof-' + id,
          sources: [source],
          parents: [],
          parentReferences: [],
          subjectWitness: { kind, personId, sourceMessageId: source.messageId },
        },
      ],
    }
    views.set(id, { reference: ref(id), view })
    return view
  }
  add()
  const service = new RelationCandidates(
    db,
    {
      enabled: () => enabled,
      autoAdmission: () => auto,
      learningEnabled: () => learning,
      current: (context, reference) => {
        const row = views.get(reference.id)
        return !context.simulation &&
          row &&
          row.view.record.scope.kind === 'scene' &&
          row.view.record.scope.sceneId === context.sceneId &&
          JSON.stringify(reference) === JSON.stringify(row.reference)
          ? structuredClone(row.view)
          : undefined
      },
    },
    { now: () => Date.parse('2026-10-28T00:00:00.000Z'), id: () => String(++sequence) },
  )
  const proposal = (patch: Partial<RelationProposal> = {}): RelationProposal => ({
    predicate: 'participates_in',
    modality: 'asserted',
    temporalScope: 'current',
    personId: 'person',
    target: { kind: 'new', entityKind: 'plan', title: 'Same title' },
    polarity: 'positive',
    conditions: [],
    ...patch,
  })
  return {
    db,
    service,
    views,
    add,
    proposal,
    enable: () => {
      enabled = true
    },
    auto: () => {
      auto = true
    },
    disable: () => {
      enabled = false
    },
    stopLearning: () => {
      learning = false
    },
    admit: (p: RelationProposal[] = [proposal()], reference = ref(), context = ctx) =>
      db.transaction(() => service.admit(context, reference, p))(),
  }
}
test('default OFF, learning OFF, simulation and orphan backing create nothing; active transaction required', () => {
  const h = fixture()
  try {
    assert.equal(h.admit().created.length, 0)
    assert.equal(
      (h.db.prepare('SELECT COUNT(*) AS n FROM memory_relation_entities').get() as { n: number }).n,
      0,
    )
    h.enable()
    assert.throws(() => h.service.admit(ctx, ref(), [h.proposal()]), /transaction_required/)
    assert.equal(h.admit([h.proposal()], ref('missing')).created.length, 0)
    assert.equal(h.admit([h.proposal()], ref(), { ...ctx, simulation: true }).created.length, 0)
    h.stopLearning()
    assert.equal(h.admit().created.length, 0)
  } finally {
    h.db.close()
  }
})
test('enabled explicit self auto acceptance is qualified indexing, mentioned/negative/conditional/time remain candidate', () => {
  const h = fixture()
  try {
    h.enable()
    h.auto()
    const a = h.admit().created[0]!
    assert.equal(a.status, 'accepted')
    assert.equal(a.autoReason, 'self_explicit_no_visible_conflict')
    assert.deepEqual(a.proofs[0]!.parentReferences, [])
    for (const patch of [
      { polarity: 'negative' as const },
      { modality: 'possible' as const },
      { temporalScope: 'past' as const },
      { temporalScope: 'unspecified' as const },
      { conditions: ['仅日期变更且并非无条件参加'] },
      { occurredAt: '2026-01-01T00:00:00Z' },
      { validFrom: '2027-01-01T00:00:00Z' },
    ])
      assert.equal(h.admit([h.proposal(patch)]).created[0]?.status, 'candidate')
    h.add('reported', 'person', 'mentioned')
    assert.equal(h.admit([h.proposal()], ref('reported')).created[0]?.status, 'candidate')
  } finally {
    h.db.close()
  }
})
test('same title never merges identity; exact retry is idempotent; existing entity requires contextual exact exposure', () => {
  const h = fixture()
  try {
    h.enable()
    const first = h.admit().created[0]!
    assert.equal(h.admit().created[0]!.id, first.id)
    h.add('second')
    const second = h.admit([h.proposal()], ref('second')).created[0]!
    assert.notEqual(first.target.id, second.target.id)
    const existing = h.proposal({ target: { kind: 'existing', entityId: first.target.id } })
    assert.equal(
      h.admit([existing], ref('second')).rejected[0]?.reason,
      'relation_target_not_exposed',
    )
    const context = {
      ...ctx,
      provenance: 'contextual' as const,
      exposedEntities: [{ entityId: first.target.id, backingRef: ref() }],
    }
    assert.equal(h.admit([existing], ref('second'), context).created.length, 1)
    assert.equal(
      h.admit([existing], ref('second')).rejected[0]?.reason,
      'relation_target_not_exposed',
    )
    assert.equal(
      h.admit([existing], ref('second'), { ...context, exposedEntities: [] }).rejected[0]?.reason,
      'relation_target_not_exposed',
    )
    assert.equal(
      h.admit([existing], ref('second'), { ...context, sceneId: 'foreign' }).created.length,
      0,
    )
    h.add('third')
    assert.equal(
      h.admit([existing], ref('third'), {
        ...context,
        exposedEntities: [{ entityId: first.target.id, backingRef: { ...ref(), version: 2 } }],
      }).created.length,
      0,
    )
  } finally {
    h.db.close()
  }
})
test('CAS review rechecks backing epoch and transaction; rollback leaves no relations or entities', () => {
  const h = fixture()
  try {
    h.enable()
    const a = h.admit().created[0]!
    const accepted = h.db.transaction(() => h.service.review(ctx, a.id, 1, 'accepted', 'admin'))()
    assert.equal(accepted.version, 2)
    assert.equal(accepted.admission, 'reviewed')
    assert.throws(
      () => h.db.transaction(() => h.service.review(ctx, a.id, 1, 'rejected'))(),
      /version_conflict/,
    )
    h.views.get('assertion')!.reference.deletionEpoch = 1
    assert.throws(
      () => h.db.transaction(() => h.service.review(ctx, a.id, 2, 'rejected'))(),
      /unavailable/,
    )
    assert.equal(h.service.list(ctx).items.length, 0)
    h.add('rollback')
    const count = (
      h.db.prepare('SELECT COUNT(*) AS n FROM memory_relation_candidates').get() as { n: number }
    ).n
    assert.throws(
      () =>
        h.db.transaction(() => {
          h.service.admit(ctx, ref('rollback'), [h.proposal()])
          throw Error('rollback')
        })(),
      /rollback/,
    )
    assert.equal(
      (h.db.prepare('SELECT COUNT(*) AS n FROM memory_relation_candidates').get() as { n: number })
        .n,
      count,
    )
  } finally {
    h.db.close()
  }
})
test('source and target invalidation physically clear private relation/entity bodies and reverse edges without deleting unrelated entities', () => {
  const h = fixture()
  try {
    h.enable()
    const a = h.admit([h.proposal({ conditions: ['PRIVATE_CONDITION'] })]).created[0]!
    h.add('second')
    const independent = h.admit([h.proposal()], ref('second')).created[0]!
    h.add('third')
    const linked = h.admit(
      [h.proposal({ target: { kind: 'existing', entityId: a.target.id } })],
      ref('third'),
      {
        ...ctx,
        provenance: 'contextual',
        exposedEntities: [{ entityId: a.target.id, backingRef: ref() }],
      },
    ).created[0]!
    const removed = h.db.transaction(() => h.service.assertionChanged('assertion'))()
    assert.deepEqual(new Set(removed), new Set([a.id, linked.id]))
    assert.equal(h.service.storage.entity(a.target.id), undefined)
    assert.ok(h.service.storage.entity(independent.target.id))
    assert.equal(h.service.storage.get(linked.id), undefined)
    assert.ok(
      !JSON.stringify(h.db.prepare('SELECT * FROM memory_relation_candidates').all()).includes(
        'PRIVATE_CONDITION',
      ),
    )
    assert.deepEqual(
      Object.keys(h.db.prepare('SELECT * FROM memory_relation_tombstones LIMIT 1').get()!),
      ['id', 'version'],
    )
  } finally {
    h.db.close()
  }
})
test('visible negative relation prevents auto acceptance for same exact entity; qualifiers never truncated', () => {
  const h = fixture()
  try {
    h.enable()
    h.auto()
    const target = h.admit([h.proposal({ polarity: 'negative' })]).created[0]!
    h.add('second')
    const context = {
      ...ctx,
      provenance: 'contextual' as const,
      exposedEntities: [{ entityId: target.target.id, backingRef: ref() }],
    }
    const positive = h.admit(
      [h.proposal({ target: { kind: 'existing', entityId: target.target.id } })],
      ref('second'),
      context,
    ).created[0]!
    assert.equal(positive.status, 'candidate')
    assert.equal(
      h.admit([h.proposal({ conditions: ['x'.repeat(2001)] })]).rejected[0]?.reason,
      'invalid_relation_proposal',
    )
    h.disable()
    assert.equal(h.service.list(ctx).items.length, 0)
  } finally {
    h.db.close()
  }
})

test('past or possible origin entity cannot authorize automatic current participation even after review', () => {
  for (const temporalScope of ['past', 'unspecified'] as const) {
    const h = fixture()
    try {
      h.enable()
      h.auto()
      const origin = h.admit([h.proposal({ temporalScope })]).created[0]!
      h.db.transaction(() => h.service.review(ctx, origin.id, 1, 'accepted'))()
      h.add('next')
      const next = h.admit(
        [h.proposal({ target: { kind: 'existing', entityId: origin.target.id } })],
        ref('next'),
        {
          ...ctx,
          provenance: 'contextual',
          exposedEntities: [{ entityId: origin.target.id, backingRef: ref() }],
        },
      ).created[0]!
      assert.equal(next.status, 'candidate')
    } finally {
      h.db.close()
    }
  }
})
