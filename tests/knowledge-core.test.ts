import assert from 'node:assert/strict'
import test from 'node:test'
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
test('finite registry retains qualifications and exact source roots/spans without raw chat persistence', () => {
  const f = fixture()
  try {
    const r = f.add('m', 'PRIVATE_SOURCE_CANARY')
    const result = f.run(() => f.service.admit(ctx, r, [f.proposal('m')]))
    assert.equal(result.created[0]!.status, 'accepted')
    const edge = result.created[0]!
    assert.equal(edge.spans[0]!.partIndex, 1)
    assert.equal(edge.spans[0]!.end, 'PRIVATE_SOURCE_CANARY'.length)
    assert.equal(
      JSON.stringify(f.db.prepare('SELECT * FROM memory_knowledge_edges').all()).includes(
        'PRIVATE_SOURCE_CANARY',
      ),
      false,
    )
    assert.equal(f.run(() => f.service.admit(ctx, r, [f.proposal('m')])).created[0]!.id, edge.id)
    assert.equal(
      f.service.query(ctx, {
        anchor: { kind: 'person', personId: 'person' },
        predicates: ['uses_tool'],
        depth: 1,
      }).edges.length,
      1,
    )
    assert.equal(
      KnowledgeProposalSchema.safeParse({ ...f.proposal('m'), registryVersion: 2 }).success,
      false,
    )
  } finally {
    f.db.close()
  }
})
test('negative possible conditional and quoted counterevidence stays in complete conflict groups', () => {
  const f = fixture()
  try {
    f.add('a')
    f.run(() => f.service.admit(ctx, ref('a'), [f.proposal('a')]))
    f.add('b', 'It might not work', 'person', 'reported')
    const p = f.proposal('b', {
      polarity: 'negative',
      modality: 'possible',
      temporalScope: 'past',
      conditions: ['Only when offline'],
      sourceSpans: [{ messageId: 'b', partIndex: 1, start: 0, end: 10, stance: 'quoted' }],
    })
    assert.equal(f.run(() => f.service.admit(ctx, ref('b'), [p])).created[0]!.status, 'candidate')
    const query = f.service.query(ctx, {
      anchor: { kind: 'person', personId: 'person' },
      predicates: ['uses_tool'],
      depth: 1,
    })
    assert.equal(query.edges.length, 2)
    assert.deepEqual(query.edges.find((e) => e.polarity === 'negative')!.conditions, [
      'Only when offline',
    ])
    assert.equal(query.edges.find((e) => e.polarity === 'negative')!.spans[0]!.stance, 'quoted')
    f.add('c')
    assert.equal(
      f.run(() =>
        f.service.admit(ctx, ref('c'), [
          f.proposal('c', {
            sourceSpans: [{ messageId: 'c', partIndex: 1, start: 0, end: 2, stance: 'quoted' }],
          }),
        ]),
      ).created.length,
      0,
    )
  } finally {
    f.db.close()
  }
})
test('typed property identity and units are explicit, never arbitrary numeric dependencies', () => {
  const f = fixture()
  try {
    f.add('p')
    const p = f.proposal('p', {
      predicate: 'has_property',
      property: 'duration',
      subject: { kind: 'new', entityKind: 'task', title: 'Task' },
      object: { kind: 'literal', valueType: 'number', value: 3, unit: 'hours' },
      sourceSpans: [{ messageId: 'p', partIndex: 1, start: 0, end: 2, stance: 'asserted' }],
    })
    const result = f.run(() => f.service.admit(ctx, ref('p'), [p]))
    assert.equal(result.created.length, 1)
    assert.equal(result.created[0]!.property, 'duration')
    assert.equal(
      f.run(() =>
        f.service.admit(ctx, ref('p'), [
          { ...p, object: { kind: 'literal', valueType: 'number', value: 3, unit: null } },
        ]),
      ).created.length,
      0,
    )
    assert.equal(
      f.run(() =>
        f.service.admit(ctx, ref('p'), [{ ...p, predicate: 'depends_on', property: undefined }]),
      ).created.length,
      0,
    )
  } finally {
    f.db.close()
  }
})
test('UTF16 boundaries, exact part indices, sources and all-structured-or-reject are enforced', () => {
  const f = fixture()
  try {
    f.add('emoji', 'A😀B')
    const valid = f.proposal('emoji')
    const bad = {
      ...valid,
      sourceSpans: [{ messageId: 'emoji', partIndex: 1, start: 1, end: 2, stance: 'self' }],
    }
    assert.equal(f.run(() => f.service.admit(ctx, ref('emoji'), [valid, bad])).created.length, 0)
    assert.equal(f.service.storage.rows().length, 0)
    assert.equal(
      f.run(() =>
        f.service.admit(ctx, ref('emoji'), [
          {
            ...valid,
            sourceSpans: [{ messageId: 'emoji', partIndex: 0, start: 0, end: 1, stance: 'self' }],
          },
        ]),
      ).created.length,
      0,
    )
    assert.equal(f.run(() => f.service.admit(ctx, ref('emoji'), [valid, valid])).created.length, 0)
    assert.equal(f.service.storage.rows().length, 0)
  } finally {
    f.db.close()
  }
})
test('same titles have opaque identities; exact exposed dependencies support two hops and privacy closure', () => {
  const f = fixture()
  try {
    f.add('a')
    const a = f.run(() =>
      f.service.admit(ctx, ref('a'), [
        f.proposal('a', {
          predicate: 'part_of',
          subject: { kind: 'new', entityKind: 'task', title: 'PRIVATE_TASK_CANARY' },
          object: { kind: 'new', entityKind: 'task', title: 'same' },
          sourceSpans: [{ messageId: 'a', partIndex: 1, start: 0, end: 2, stance: 'asserted' }],
        }),
      ]),
    ).created[0]!
    assert.equal(a.subject.kind, 'entity')
    assert.equal(a.object.kind, 'entity')
    if (a.subject.kind !== 'entity' || a.object.kind !== 'entity') throw new Error()
    const node = a.object.entity
    f.add('b')
    const p = f.proposal('b', {
      predicate: 'depends_on',
      subject: { kind: 'existing', entityId: node.id },
      object: { kind: 'new', entityKind: 'task', title: 'same' },
      sourceSpans: [{ messageId: 'b', partIndex: 1, start: 0, end: 2, stance: 'asserted' }],
    })
    const contextual: KnowledgeContext = {
      ...ctx,
      provenance: 'contextual',
      exposedEntities: [{ entityId: node.id, backingRef: node.backing }],
    }
    const b = f.run(() => f.service.admit(contextual, ref('b'), [p])).created[0]!
    assert.equal(b.dependencies.length, 1)
    assert.equal(f.run(() => f.service.admit(ctx, ref('b'), [p])).created.length, 0)
    const queried = f.service.query(
      { ...contextual, exposedEntities: [{ entityId: a.subject.entity.id, backingRef: ref('a') }] },
      {
        anchor: { kind: 'entity', entityId: a.subject.entity.id, backingRef: ref('a') },
        predicates: ['part_of', 'depends_on'],
        depth: 2,
      },
    )
    assert.equal(queried.edges.length, 2)
    assert.ok(queried.paths.some((path) => path.length === 2))
    f.run(() => f.service.purgeSource('s', 'a'))
    assert.equal(f.service.storage.rows().length, 0)
    assert.equal(
      JSON.stringify(f.db.prepare('SELECT * FROM memory_knowledge_edges').all()).includes(
        'PRIVATE_TASK_CANARY',
      ),
      false,
    )
  } finally {
    f.db.close()
  }
})
test('current OR projection is an exact original-proof intersection; source purge preserves independent OR', () => {
  const f = fixture()
  try {
    f.add('a')
    f.add('b')
    const first = f.views.get('a')!,
      second = f.views.get('b')!
    first.proofs.push(second.proofs[0]!)
    first.record.sources.push(...second.record.sources)
    const p = f.proposal('a', {
      sourceSpans: [
        { messageId: 'a', partIndex: 1, start: 0, end: 2, stance: 'self' },
        { messageId: 'b', partIndex: 1, start: 0, end: 2, stance: 'self' },
      ],
    })
    f.run(() => f.service.admit(ctx, ref('a'), [p]))
    first.proofs = first.proofs.filter((p) => p.proofId === 'proof-b')
    const view = f.service.query(ctx, {
      anchor: { kind: 'person', personId: 'person' },
      predicates: ['uses_tool'],
      depth: 1,
    }).edges[0]!
    assert.deepEqual(
      view.proofs.map((p) => p.proofId),
      ['proof-b'],
    )
    assert.deepEqual(
      view.spans.map((s) => s.source.messageId),
      ['b'],
    )
    f.run(() => f.service.purgeSource('s', 'a'))
    assert.equal(f.service.storage.rows().length, 1)
    assert.equal(f.service.storage.rows()[0]!.spans[0]!.source.messageId, 'b')
    f.views.delete('a')
    assert.equal(
      f.service.query(ctx, {
        anchor: { kind: 'person', personId: 'person' },
        predicates: ['uses_tool'],
        depth: 1,
      }).edges.length,
      0,
    )
  } finally {
    f.db.close()
  }
})
test('conflict expansion exceeds its budget atomically rather than returning a positive prefix', () => {
  const f = fixture()
  try {
    for (let i = 0; i < 13; i++) {
      f.add('m' + i)
      f.run(() =>
        f.service.admit(ctx, ref('m' + i), [
          f.proposal('m' + i, { polarity: i % 2 ? 'negative' : 'positive' }),
        ]),
      )
    }
    const result = f.service.query(ctx, {
      anchor: { kind: 'person', personId: 'person' },
      predicates: ['uses_tool'],
      depth: 1,
    })
    assert.equal(result.coverage, 'limited')
    assert.deepEqual(result.edges, [])
    assert.deepEqual(result.paths, [])
    f.ready(false)
    assert.throws(() => f.service.storage.rows(), /quarantined/)
  } finally {
    f.db.close()
  }
})

test('trusted recall requalification requires the exact pre-current ref and only original surviving proofs even while OFF', () => {
  const f = fixture()
  try {
    f.add('a')
    f.add('b')
    f.add('new')
    const view = f.views.get('a')!
    view.proofs.push(f.views.get('b')!.proofs[0]!)
    const p = f.proposal('a', {
      sourceSpans: [
        { messageId: 'a', partIndex: 1, start: 0, end: 2, stance: 'self' },
        { messageId: 'b', partIndex: 1, start: 0, end: 2, stance: 'self' },
      ],
    })
    f.run(() => f.service.admit(ctx, ref('a'), [p]))
    f.run(() => f.service.purgeSource('s', 'a'))
    view.proofs = [f.views.get('b')!.proofs[0]!, f.views.get('new')!.proofs[0]!]
    view.record.status = 'disputed'
    f.versions.set('a', 2)
    const after = { ...ref('a'), version: 2 }
    f.run(() => f.service.requalify(ctx, [{ before: { ...ref('a'), version: 99 }, after }]))
    assert.equal(f.service.storage.rows()[0]!.backing.version, 1)
    f.enabled(false)
    f.run(() => f.service.requalify(ctx, [{ before: ref('a'), after }]))
    f.enabled(true)
    const result = f.service.query(ctx, {
      anchor: { kind: 'person', personId: 'person' },
      predicates: ['uses_tool'],
      depth: 1,
    }).edges[0]!
    assert.equal(result.backing.version, 2)
    assert.equal(result.backingStatus, 'disputed')
    assert.deepEqual(
      result.proofs.map((p) => p.proofId),
      ['proof-b'],
    )
    assert.deepEqual(
      result.spans.map((s) => s.source.messageId),
      ['b'],
    )
  } finally {
    f.db.close()
  }
})

test('direct structured declarations use registered admission policy; time and quoted uncertainty stay qualified', () => {
  const f = fixture()
  try {
    f.add('p')
    const p = f.proposal('p', {
      predicate: 'part_of',
      subject: { kind: 'new', entityKind: 'task', title: 'Task' },
      object: { kind: 'new', entityKind: 'task', title: 'Project' },
      sourceSpans: [{ messageId: 'p', partIndex: 1, start: 0, end: 2, stance: 'asserted' }],
    })
    const edge = f.run(() => f.service.admit(ctx, ref('p'), [p])).created[0]!
    assert.equal(edge.status, 'accepted')
    assert.equal(edge.admission, 'direct_assertion_no_visible_conflict')
    assert.equal(edge.admissionPolicyVersion, 1)
    f.add('future')
    const future = f.run(() =>
      f.service.admit(ctx, ref('future'), [
        f.proposal('future', { temporalScope: 'future', validFrom: '2027-01-01T00:00:00Z' }),
      ]),
    ).created[0]!
    assert.equal(future.status, 'candidate')
    assert.equal(
      f.service.query(ctx, {
        anchor: { kind: 'person', personId: 'person' },
        predicates: ['uses_tool'],
        depth: 1,
      }).edges[0]!.applicability,
      'future',
    )
  } finally {
    f.db.close()
  }
})

test('conflict closure is not fabricated as an incident path and malformed storage is fail-closed', () => {
  const f = fixture()
  try {
    f.add('a')
    f.add('b')
    const a = f.run(() => f.service.admit(ctx, ref('a'), [f.proposal('a')])).created[0]!
    const b = f.run(() =>
      f.service.admit(ctx, ref('b'), [f.proposal('b', { polarity: 'negative' })]),
    ).created[0]!
    if (a.object.kind !== 'entity') throw new Error()
    const entity = a.object.entity
    const contextual = {
      ...ctx,
      provenance: 'contextual' as const,
      exposedEntities: [{ entityId: entity.id, backingRef: entity.backing }],
    }
    const result = f.service.query(contextual, {
      anchor: { kind: 'entity', entityId: entity.id, backingRef: entity.backing },
      predicates: ['uses_tool'],
      depth: 1,
    })
    assert.equal(result.edges.length, 2)
    assert.deepEqual(result.paths, [[a.id]])
    const changed = { ...b, registryVersion: 2 }
    f.db
      .prepare('UPDATE memory_knowledge_edges SET value=? WHERE id=?')
      .run(JSON.stringify(changed), b.id)
    assert.throws(() => f.service.storage.rows(), /corrupt/)
  } finally {
    f.db.close()
  }
})
test('write hook runs before storage and async hook refuses all local writes', () => {
  const db = new Database(':memory:')
  const f = fixture()
  try {
    f.add('a')
    const authority: KnowledgeAuthority = {
      assertReady() {},
      current: () => f.views.get('a'),
      source: () => f.sources.get('a'),
      enabled: () => true,
      learningEnabled: () => true,
      autoAdmission: () => true,
    }
    const service = new KnowledgeService(db, authority, { beforeWrite: () => Promise.resolve() })
    assert.throws(
      () => db.transaction(() => service.admit(ctx, ref('a'), [f.proposal('a')]))(),
      /async_write_hook/,
    )
    assert.equal(
      (
        db
          .prepare("SELECT count(*) n FROM sqlite_master WHERE name='memory_knowledge_edges'")
          .get() as { n: number }
      ).n,
      0,
    )
  } finally {
    db.close()
    f.db.close()
  }
})

test('concept discovery starts only from exact actual backing origins with one bounded shared scene scan', () => {
  const f = fixture()
  try {
    f.add('a')
    f.add('same-title')
    const proposal = (id: string) =>
      f.proposal(id, {
        predicate: 'part_of',
        subject: { kind: 'new', entityKind: 'task', title: 'Same task' },
        object: { kind: 'new', entityKind: 'task', title: 'Same project' },
        sourceSpans: [{ messageId: id, partIndex: 1, start: 0, end: 2, stance: 'asserted' }],
      })
    const a = f.run(() => f.service.admit(ctx, ref('a'), [proposal('a')])).created[0]!
    f.run(() => f.service.admit(ctx, ref('same-title'), [proposal('same-title')]))
    let scans = 0
    const original = f.service.storage.rows.bind(f.service.storage)
    f.service.storage.rows = (...args) => {
      scans++
      return original(...args)
    }
    const result = f.service.discover(ctx, ref('a'), { predicates: ['part_of'], depth: 2 })
    assert.equal(scans, 1)
    assert.deepEqual(
      result.edges.map((e) => e.id),
      [a.id],
    )
    assert.deepEqual(result.paths, [[a.id]])
    assert.equal(ctx.exposedEntities, undefined)
    assert.throws(
      () =>
        f.service.discover(ctx, { ...ref('a'), version: 2 }, { predicates: ['part_of'], depth: 1 }),
      /backing_unavailable/,
    )
    assert.throws(
      () =>
        f.service.discover({ ...ctx, sceneId: 'foreign' }, ref('a'), {
          predicates: ['part_of'],
          depth: 1,
        }),
      /backing_unavailable/,
    )
    assert.deepEqual(
      f.service.discover({ ...ctx, simulation: true }, ref('a'), {
        predicates: ['part_of'],
        depth: 1,
      }).edges,
      [],
    )
  } finally {
    f.db.close()
  }
})
test('discovery root overflow returns no partial positives and entity queries cannot upgrade old backing refs', () => {
  const f = fixture()
  try {
    f.add('a')
    for (let i = 0; i < 7; i++)
      f.run(() =>
        f.service.admit(ctx, ref('a'), [
          f.proposal('a', {
            predicate: 'part_of',
            subject: { kind: 'new', entityKind: 'task', title: 'Task' + i },
            object: { kind: 'new', entityKind: 'task', title: 'Project' + i },
            sourceSpans: [{ messageId: 'a', partIndex: 1, start: 0, end: 2, stance: 'asserted' }],
          }),
        ]),
      )
    const result = f.service.discover(ctx, ref('a'), { predicates: ['part_of'], depth: 1 })
    assert.equal(result.coverage, 'limited')
    assert.deepEqual(result.edges, [])
    const first = f.service.storage.rows()[0]!
    if (first.subject.kind !== 'entity') throw new Error()
    const entity = first.subject.entity
    f.versions.set('a', 2)
    f.run(() =>
      f.service.requalify(ctx, [{ before: ref('a'), after: { ...ref('a'), version: 2 } }]),
    )
    const stale = {
      ...ctx,
      provenance: 'contextual' as const,
      exposedEntities: [{ entityId: entity.id, backingRef: ref('a') }],
    }
    assert.throws(
      () =>
        f.service.query(stale, {
          anchor: { kind: 'entity', entityId: entity.id, backingRef: ref('a') },
          predicates: ['part_of'],
          depth: 1,
        }),
      /anchor_unavailable/,
    )
  } finally {
    f.db.close()
  }
})

test('all delivered graph edges are inherited AND dependencies even for all-new endpoints, and privacy clears copied terms', () => {
  const f = fixture()
  try {
    for (const id of ['a', 'b', 'c', 'independent']) f.add(id)
    const a = f.run(() => f.service.admit(ctx, ref('a'), [f.proposal('a')])).created[0]!
    const b = f.run(() =>
      f.service.admit(ctx, ref('b'), [
        f.proposal('b', { object: { kind: 'new', entityKind: 'tool', title: 'PRIVATE_PAGE_B' } }),
      ]),
    ).created[0]!
    const captured = {
      ...ctx,
      provenance: 'contextual' as const,
      exposedEdges: [a, b].map((e) => ({ id: e.id, version: e.version, backing: e.backing })),
    }
    const child = f.run(() =>
      f.service.admit(captured, ref('c'), [
        f.proposal('c', {
          object: { kind: 'new', entityKind: 'tool', title: 'PRIVATE_PAGE_B copied' },
        }),
      ]),
    ).created[0]!
    assert.equal(child.readDependencies.length, 2)
    assert.equal(child.dependencies.length, 0)
    const independent = f.run(() =>
      f.service.admit(ctx, ref('independent'), [f.proposal('independent')]),
    ).created[0]!
    f.run(() => f.service.purgeSource('s', 'b'))
    assert.equal(f.service.storage.get(child.id), undefined)
    assert.ok(f.service.storage.get(a.id))
    assert.ok(f.service.storage.get(independent.id))
    assert.equal(
      JSON.stringify(f.db.prepare('SELECT value FROM memory_knowledge_edges').all()).includes(
        'PRIVATE_PAGE_B',
      ),
      false,
    )
  } finally {
    f.db.close()
  }
})
test('ordinary actual assertion reads are inherited too; stale snapshots reject and original independent supports survive', () => {
  const f = fixture()
  try {
    f.add('memory')
    f.add('new')
    const captured = {
      ...ctx,
      provenance: 'contextual' as const,
      exposedAssertions: [ref('memory')],
    }
    const child = f.run(() =>
      f.service.admit(captured, ref('new'), [
        f.proposal('new', {
          object: { kind: 'new', entityKind: 'tool', title: 'PRIVATE_MEMORY_COPY' },
        }),
      ]),
    ).created[0]!
    assert.equal(child.readDependencies[0]!.backing.id, 'memory')
    assert.equal(child.readDependencies[0]!.edgeId, undefined)
    f.versions.set('memory', 2)
    assert.equal(
      f.service.query(ctx, {
        anchor: { kind: 'person', personId: 'person' },
        predicates: ['uses_tool'],
        depth: 1,
      }).edges.length,
      0,
    )
    assert.equal(
      f.run(() => f.service.admit(captured, ref('new'), [f.proposal('new')])).created.length,
      0,
    )
    f.run(() => f.service.purgeAssertion('memory'))
    assert.equal(f.service.storage.rows().length, 0)
  } finally {
    f.db.close()
  }
})
test('inherited graph OR survives only this exact privacy version transition and never substitutes new proof', () => {
  const f = fixture()
  try {
    for (const id of ['a', 'b', 'c', 'd', 'unseen']) f.add(id)
    const originView = f.views.get('a')!
    originView.proofs.push(f.views.get('b')!.proofs[0]!)
    const origin = f.run(() =>
      f.service.admit(ctx, ref('a'), [
        f.proposal('a', {
          sourceSpans: [
            { messageId: 'a', partIndex: 1, start: 0, end: 2, stance: 'self' },
            { messageId: 'b', partIndex: 1, start: 0, end: 2, stance: 'self' },
          ],
        }),
      ]),
    ).created[0]!
    const contextual = {
      ...ctx,
      provenance: 'contextual' as const,
      exposedEdges: [{ id: origin.id, version: origin.version, backing: origin.backing }],
    }
    const child = f.run(() => f.service.admit(contextual, ref('c'), [f.proposal('c')])).created[0]!
    const grandchild = f.run(() =>
      f.service.admit(
        {
          ...ctx,
          provenance: 'contextual',
          exposedEdges: [{ id: child.id, version: child.version, backing: child.backing }],
        },
        ref('d'),
        [f.proposal('d')],
      ),
    ).created[0]!
    originView.proofs = [f.views.get('b')!.proofs[0]!, f.views.get('unseen')!.proofs[0]!]
    f.run(() => f.service.purgeSource('s', 'a'))
    let updated = f.service.storage.get(child.id)!
    assert.deepEqual(
      updated.readDependencies[0]!.proofs.map((p) => p.proofId),
      ['proof-b'],
    )
    assert.equal(
      updated.readDependencies[0]!.edgeVersion,
      f.service.storage.get(origin.id)!.version,
    )
    f.versions.set('a', 2)
    f.run(() =>
      f.service.requalify(ctx, [{ before: ref('a'), after: { ...ref('a'), version: 2 } }]),
    )
    updated = f.service.storage.get(child.id)!
    assert.equal(
      f.service.storage.get(grandchild.id)!.readDependencies[0]!.edgeVersion,
      updated.version,
    )
    assert.equal(updated.readDependencies[0]!.backing.version, 2)
    assert.equal(
      updated.readDependencies[0]!.edgeVersion,
      f.service.storage.get(origin.id)!.version,
    )
    assert.ok(
      f.service
        .query(ctx, {
          anchor: { kind: 'person', personId: 'person' },
          predicates: ['uses_tool'],
          depth: 1,
        })
        .edges.some((e) => e.id === grandchild.id),
    )
  } finally {
    f.db.close()
  }
})

test('flat support closure preserves indirect personal origin qualifications and complete exact records', () => {
  const f = fixture()
  try {
    for (const id of ['a', 'b', 'c']) f.add(id)
    const a = f.run(() =>
      f.service.admit(ctx, ref('a'), [
        f.proposal('a', {
          polarity: 'negative',
          modality: 'possible',
          conditions: ['Only after explicit review'],
        }),
      ]),
    ).created[0]!
    const general = (id: string) => {
      f.views.get(id)!.record.scope = { kind: 'scene', sceneId: 's' }
      return f.proposal(id, {
        predicate: 'part_of',
        subject: { kind: 'new', entityKind: 'task', title: id },
        object: { kind: 'new', entityKind: 'task', title: id + ' target' },
        sourceSpans: [{ messageId: id, partIndex: 1, start: 0, end: 2, stance: 'asserted' }],
      })
    }
    const b = f.run(() =>
      f.service.admit(
        {
          ...ctx,
          provenance: 'contextual',
          exposedEdges: [{ id: a.id, version: a.version, backing: a.backing }],
        },
        ref('b'),
        [general('b')],
      ),
    ).created[0]!
    const c = f.run(() =>
      f.service.admit(
        {
          ...ctx,
          provenance: 'contextual',
          exposedEdges: [{ id: b.id, version: b.version, backing: b.backing }],
        },
        ref('c'),
        [general('c')],
      ),
    ).created[0]!
    const result = f.service.discover(ctx, ref('c'), { predicates: ['part_of'], depth: 1 })
    assert.deepEqual(
      result.edges.map((e) => e.id),
      [c.id],
    )
    assert.equal(result.edges[0]!.requiresPersonalization, true)
    assert.deepEqual(new Set(result.supportClosure.edges.map((e) => e.id)), new Set([a.id, b.id]))
    const origin = result.supportClosure.edges.find((e) => e.id === a.id)!
    assert.equal(origin.polarity, 'negative')
    assert.equal(origin.modality, 'possible')
    assert.deepEqual(origin.conditions, ['Only after explicit review'])
    assert.deepEqual(
      new Set(result.supportClosure.records.map((r) => r.ref.id)),
      new Set(['a', 'b', 'c']),
    )
    assert.equal(
      result.supportClosure.edges.some((e) => 'supportClosure' in e),
      false,
    )
    f.run(() => f.service.purgeAssertion('a'))
    assert.equal(f.service.storage.get(c.id), undefined)
  } finally {
    f.db.close()
  }
})
test('oversized inherited support closure returns an empty limited page, not clipped source conditions', () => {
  const f = fixture()
  try {
    let previous: ReturnType<typeof f.service.admit>['created'][number] | undefined
    for (let i = 0; i < 41; i++) {
      const id = 'm' + i
      f.add(id)
      f.views.get(id)!.record.scope = { kind: 'scene', sceneId: 's' }
      const inherited: KnowledgeContext = previous
        ? {
            ...ctx,
            provenance: 'contextual',
            exposedEdges: [
              { id: previous.id, version: previous.version, backing: previous.backing },
            ],
          }
        : ctx
      previous = f.run(() =>
        f.service.admit(inherited, ref(id), [
          f.proposal(id, {
            predicate: 'part_of',
            subject: { kind: 'new', entityKind: 'task', title: id },
            object: { kind: 'new', entityKind: 'task', title: id + ' target' },
            sourceSpans: [{ messageId: id, partIndex: 1, start: 0, end: 2, stance: 'asserted' }],
          }),
        ]),
      ).created[0]!
      assert.ok(previous)
    }
    const result = f.service.discover(ctx, ref('m40'), { predicates: ['part_of'], depth: 1 })
    assert.equal(result.coverage, 'limited')
    assert.equal(result.reason, 'dependency_limit')
    assert.deepEqual(result.edges, [])
    assert.deepEqual(result.supportClosure, { edges: [], records: [] })
  } finally {
    f.db.close()
  }
})

test('historical privacy pruning removes indirect parent proofs and derived bodies without treating current drift as revocation', () => {
  const f = fixture()
  try {
    for (const id of ['a', 'b', 'child', 'independent']) f.add(id)
    const inherited = f.views.get('a')!.proofs[0]!
    inherited.parents = ['private-parent']
    inherited.parentReferences = [ref('private-parent')]
    const origin = f.run(() =>
      f.service.admit(ctx, ref('a'), [
        f.proposal('a', {
          object: { kind: 'new', entityKind: 'tool', title: 'INDIRECT_PRIVATE_CANARY' },
        }),
      ]),
    ).created[0]!
    const child = f.run(() =>
      f.service.admit(
        {
          ...ctx,
          provenance: 'contextual',
          exposedEdges: [{ id: origin.id, version: origin.version, backing: origin.backing }],
        },
        ref('child'),
        [
          f.proposal('child', {
            object: { kind: 'new', entityKind: 'tool', title: 'INDIRECT_PRIVATE_CANARY_COPY' },
          }),
        ],
      ),
    ).created[0]!
    const independent = f.run(() =>
      f.service.admit(ctx, ref('independent'), [f.proposal('independent')]),
    ).created[0]!
    // A survives through a later independent alternative, which must not upgrade old E.
    f.views.get('a')!.proofs = [f.views.get('b')!.proofs[0]!]
    f.versions.set('a', 2)
    f.run(() => f.service.pruneUnavailableProof(ctx, () => true))
    assert.ok(
      f.service.storage.get(origin.id),
      'ordinary version drift alone is not privacy deletion',
    )
    const before = f.service.storage.rows()
    assert.throws(
      () =>
        f.run(() =>
          f.service.pruneUnavailableProof(ctx, () => {
            throw new Error('history_unknown')
          }),
        ),
      /history_unknown/,
    )
    assert.deepEqual(f.service.storage.rows(), before)
    f.enabled(false)
    const inspected: string[] = []
    f.run(() =>
      f.service.pruneUnavailableProof(ctx, (backing, proof) => {
        inspected.push(backing.id)
        return !proof.parents.includes('private-parent')
      }),
    )
    assert.ok(inspected.includes('a'))
    assert.equal(f.service.storage.get(origin.id), undefined)
    assert.equal(f.service.storage.get(child.id), undefined)
    assert.ok(f.service.storage.get(independent.id))
    assert.equal(
      JSON.stringify(f.service.storage.rows()).includes('INDIRECT_PRIVATE_CANARY'),
      false,
    )
  } finally {
    f.db.close()
  }
})
