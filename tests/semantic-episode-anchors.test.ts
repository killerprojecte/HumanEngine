import assert from 'node:assert/strict'
import test from 'node:test'
import { SemanticEpisodeAnchors } from '../src/memory/semantic/episode-anchors.ts'
import { semanticHash, type SemanticAssertion } from '../src/memory/semantic/contracts.ts'
import Database from 'better-sqlite3'
import { KnowledgeService } from '../src/memory/knowledge/service.ts'
import {
  KnowledgeProposalSchema,
  type KnowledgeContext,
  type KnowledgeProposal,
  type KnowledgeRef,
  type KnowledgeAuthority,
} from '../src/memory/knowledge/contracts.ts'
import type { MemoryRecord, MemorySource } from '../src/memory/domain/contracts.ts'
import type { RelationProof } from '../src/memory/domain/relations.ts'
const ctx: KnowledgeContext = { sceneId: 's', simulation: false, provenance: 'source-only' }
const ref = (id: string): KnowledgeRef => ({ id, version: 1, deletionEpoch: 0 })
function fixture() {
  const db = new Database(':memory:')
  const views = new Map<string, { record: MemoryRecord; proofs: RelationProof[] }>(),
    sources = new Map<
      string,
      { source: MemorySource; parts: ({ type: 'text'; text: string } | { type: 'other' })[] }
    >()
  const versions = new Map<string, number>()
  let sequence = 0,
    enabled = true,
    ready = true
  const authority: KnowledgeAuthority = {
    assertReady() {
      if (!ready) throw new Error('recovery_quarantined')
    },
    current(c, r) {
      const view = views.get(r.id)
      return c.sceneId === 's' && r.version === (versions.get(r.id) ?? 1) && r.deletionEpoch === 0
        ? view
        : undefined
    },
    source(_c, id) {
      return sources.get(id)
    },
    enabled: () => enabled,
    learningEnabled: () => true,
    autoAdmission: () => true,
  }
  const service = new KnowledgeService(db, authority, {
    id: () => `generated-${++sequence}`,
    now: () => Date.parse('2026-10-29T00:00:00Z'),
  })
  const add = (
    id: string,
    text = 'I use tool',
    personId = 'person',
    epistemicKind: MemoryRecord['epistemicKind'] = 'explicit',
  ) => {
    const source = { sceneId: 's', messageId: id, senderId: personId }
    sources.set(id, { source, parts: [{ type: 'other' }, { type: 'text', text }] })
    const proof: RelationProof = {
      proofId: 'proof-' + id,
      sources: [source],
      parents: [],
      parentReferences: [],
      subjectWitness: { kind: 'self', personId, sourceMessageId: id },
    }
    views.set(id, {
      record: {
        id,
        scope: { kind: 'scene', sceneId: 's', personId },
        kind: epistemicKind === 'explicit' ? 'fact' : 'claim',
        content: text,
        sources: [source],
        importance: 'medium',
        confidence: 1,
        learnedAt: '2026-10-29T00:00:00Z',
        status: 'active',
        locked: false,
        origin: 'manual',
        epistemicKind,
      },
      proofs: [proof],
    })
    return ref(id)
  }
  const proposal = (id: string, extra: Partial<KnowledgeProposal> = {}): KnowledgeProposal => ({
    registryVersion: 1,
    predicate: 'uses_tool',
    subject: { kind: 'self', personId: 'person' },
    object: { kind: 'new', entityKind: 'tool', title: 'same title' },
    polarity: 'positive',
    modality: 'asserted',
    temporalScope: 'current',
    conditions: [],
    sourceSpans: [
      {
        messageId: id,
        partIndex: 1,
        start: 0,
        end:
          sources.get(id)!.parts[1]!.type === 'text'
            ? (sources.get(id)!.parts[1] as { text: string }).text.length
            : 1,
        stance: 'self',
      },
    ],
    ...extra,
  })
  return {
    db,
    service,
    versions,
    views,
    sources,
    add,
    proposal,
    run: <T>(fn: () => T): T => db.transaction(fn)(),
    enabled: (v: boolean) => (enabled = v),
    ready: (v: boolean) => (ready = v),
  }
}
function episodeFixture(includeOrdinary = true) {
  const f = fixture()
  f.add('alice', 'Alice plans the event', 'alice')
  const origin = f.run(() =>
    f.service.admit(ctx, ref('alice'), [
      f.proposal('alice', {
        predicate: 'participates_in',
        subject: { kind: 'self', personId: 'alice' },
        object: { kind: 'new', entityKind: 'event', title: 'same title' },
      }),
    ]),
  ).created[0]!
  assert.equal(origin.object.kind, 'entity')
  if (origin.object.kind !== 'entity') throw Error('fixture')
  const entity = origin.object.entity
  f.add('bob-original', 'Bob may attend, only if weather permits', 'bob')
  f.add('bob-marker', 'trusted internal marker', 'bob')
  f.views.get('bob-marker')!.record.internalUse = 'knowledge_declaration'
  const marker = f.run(() =>
    f.service.admit(
      {
        ...ctx,
        provenance: 'contextual',
        exposedEntities: [{ entityId: entity.id, backingRef: entity.backing }],
        exposedAssertions: includeOrdinary ? [ref('bob-original')] : [],
      },
      ref('bob-marker'),
      [
        f.proposal('bob-marker', {
          predicate: 'participates_in',
          subject: { kind: 'self', personId: 'bob' },
          object: { kind: 'existing', entityId: entity.id },
          polarity: 'negative',
          modality: 'possible',
          temporalScope: 'past',
          conditions: ['Only if weather permits; not a confirmed attendance.'],
        }),
      ],
    ),
  ).created[0]!
  assert.ok(marker)
  const current = (
    _ctx: unknown,
    r: { id: string; version: number; deletionEpoch: number },
  ): SemanticAssertion | undefined => {
    const v = f.views.get(r.id)
    if (
      !v ||
      v.record.internalUse ||
      r.version !== (f.versions.get(r.id) ?? 1) ||
      r.deletionEpoch !== 0
    )
      return
    return {
      ...v,
      dependencyKey: semanticHash([r, v]),
      roots: v.proofs.flatMap((p) => p.sources.map((s) => semanticHash([s.sceneId, s.messageId]))),
      rootCoverage: 'complete',
      derived: false,
    }
  }
  const helper = new SemanticEpisodeAnchors({
    knowledge: f.service,
    current,
    anchorCurrent(_c, r) {
      return r.version === (f.versions.get(r.id) ?? 1) ? f.views.get(r.id) : undefined
    },
    triggerRecord(_c, r) {
      return r.version === (f.versions.get(r.id) ?? 1) ? f.views.get(r.id)?.record : undefined
    },
  })
  return { ...f, helper, origin, marker, entity, current }
}
test('actual knowledge existing entity marker discovers ordinary participant declarations with full qualifiers', () => {
  const f = episodeFixture()
  try {
    const result = f.helper.discover(ctx, ref('bob-marker'))
    assert.equal(result.coverage, 'complete')
    assert.equal(result.groups.length, 1)
    const group = result.groups[0]!
    assert.deepEqual(
      group.parents.map((p) => p.ref.id),
      ['alice', 'bob-original'],
    )
    const bob = group.edges.find((e) => e.id === f.marker.id)!
    assert.equal(bob.polarity, 'negative')
    assert.equal(bob.modality, 'possible')
    assert.equal(bob.temporalScope, 'past')
    assert.deepEqual(bob.conditions, ['Only if weather permits; not a confirmed attendance.'])
    const qualification = group.anchorQualifications.find((q) => q.edgeId === bob.id)!
    assert.equal(qualification.polarity, 'negative')
    assert.equal(qualification.modality, 'possible')
    assert.equal(qualification.temporalScope, 'past')
    assert.deepEqual(qualification.conditions, bob.conditions)
    assert.equal(qualification.status, bob.status)
    assert.equal(qualification.admission, bob.admission)
    assert.equal(qualification.occurredAt, null)
    assert.equal(qualification.interpretation, 'attributed_proposed_association_not_observed_event')
    assert.equal(JSON.stringify(qualification).includes('title'), false)
    assert.equal(group.interpretation, 'grouped_declarations_not_observed_event')
    assert.equal(
      f.helper.valid(
        ctx,
        group.anchor,
        group.parents.map((p) => p.ref),
        group.anchorMembershipKey,
      ),
      true,
    )
    assert.equal(
      group.parents.some((p) => p.ref.id === 'bob-marker'),
      false,
    )
    f.versions.set('bob-marker', 2)
    assert.equal(
      f.helper.valid(
        ctx,
        group.anchor,
        group.parents.map((p) => p.ref),
        group.anchorMembershipKey,
      ),
      false,
    )
  } finally {
    f.db.close()
  }
})
test('internal marker and source span alone cannot fabricate an original participant assertion', () => {
  const f = episodeFixture(false)
  try {
    const result = f.helper.discover(ctx, ref('bob-marker'))
    assert.equal(result.coverage, 'limited')
    assert.deepEqual(result.groups, [])
    assert.equal(result.reason, 'declaration_unavailable')
  } finally {
    f.db.close()
  }
})
test('same title is not an event join and complete-membership snapshots reject changed qualifications', () => {
  const f = episodeFixture()
  try {
    const before = f.helper.discover(ctx, ref('bob-marker')).groups[0]!
    f.add('other', 'Independent event', 'charlie')
    const other = f.run(() =>
      f.service.admit(ctx, ref('other'), [
        f.proposal('other', {
          predicate: 'participates_in',
          subject: { kind: 'self', personId: 'charlie' },
          object: { kind: 'new', entityKind: 'event', title: 'same title' },
        }),
      ]),
    ).created[0]!
    assert.equal(f.helper.discover(ctx, ref('other')).groups.length, 0)
    assert.equal(
      f.helper.valid(
        ctx,
        before.anchor,
        before.parents.map((p) => p.ref),
        before.anchorMembershipKey,
      ),
      true,
    )
    f.run(() => f.service.purgeSource('s', 'bob-original'))
    assert.equal(
      f.helper.valid(
        ctx,
        before.anchor,
        before.parents.map((p) => p.ref),
        before.anchorMembershipKey,
      ),
      false,
    )
    assert.ok(f.service.storage.get(other.id))
  } finally {
    f.db.close()
  }
})
test('limited graph pages remain explicitly limited rather than a partial episode', () => {
  const f = episodeFixture()
  try {
    const limited = new SemanticEpisodeAnchors({
      current: f.current,
      anchorCurrent(_c, r) {
        return f.views.get(r.id)
      },
      knowledge: {
        query() {
          return {
            edges: [],
            paths: [],
            supportClosure: { edges: [], records: [] },
            coverage: 'limited',
            reason: 'scan_limit',
            interpretation: 'attributed_claims_not_transitive_truth',
          }
        },
      },
    })
    assert.deepEqual(limited.discover(ctx, ref('alice')), {
      groups: [],
      coverage: 'limited',
      reason: 'knowledge_limited',
    })
  } finally {
    f.db.close()
  }
})
