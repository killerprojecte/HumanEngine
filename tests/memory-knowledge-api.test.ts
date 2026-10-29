import test from 'node:test'
import assert from 'node:assert/strict'
import { OpenAPIHono } from '@hono/zod-openapi'
import { Store } from '../src/store.ts'
import { KnowledgeService } from '../src/memory/knowledge/service.ts'
import { installKnowledgeRoutes } from '../src/memory/interfaces/knowledge-routes.ts'
import type { MemoryRecord } from '../src/memory/domain/contracts.ts'
import type { RelationProof } from '../src/memory/domain/relations.ts'
test('knowledge HTTP read retains complete qualifications and exact entity authority without exposing raw source hashes', async () => {
  const store = new Store(':memory:')
  let enabled = true,
    alive = true
  for (const id of ['scene', 'foreign'])
    store.saveScene({
      id,
      kind: 'group',
      adapterId: 'fixture',
      accountId: 'account',
      platformSceneId: id,
      name: 'Synthetic',
      learningEnabled: true,
      participationEnabled: false,
      proactiveEnabled: false,
    })
  const source = { sceneId: 'scene', messageId: 'message', senderId: 'person' }
  const record: MemoryRecord = {
    id: 'backing',
    scope: { kind: 'scene', sceneId: 'scene', personId: 'person' },
    kind: 'fact',
    content: 'Authorized qualified memory summary',
    sources: [source],
    importance: 'medium',
    confidence: 1,
    learnedAt: '2026-10-29T00:00:00Z',
    status: 'active',
    locked: false,
    origin: 'manual',
    epistemicKind: 'explicit',
  }
  const proofs: RelationProof[] = [
    {
      proofId: 'proof',
      sources: [source],
      parents: [],
      parentReferences: [],
      subjectWitness: { kind: 'self', personId: 'person', sourceMessageId: 'message' },
    },
  ]
  const records = new Map([['backing', record]])
  const knowledge = new KnowledgeService(store.db, {
    assertReady() {},
    current: (ctx, ref) =>
      alive &&
      ctx.sceneId === 'scene' &&
      records.has(ref.id) &&
      ref.version === 1 &&
      ref.deletionEpoch === 0
        ? { record: records.get(ref.id)!, proofs }
        : undefined,
    source: () => ({ source, parts: [{ type: 'text', text: 'I may not use this tool if busy' }] }),
    enabled: () => enabled,
    learningEnabled: () => true,
    autoAdmission: () => true,
  })
  const created = store.db.transaction(() =>
    knowledge.admit(
      { sceneId: 'scene', simulation: false, provenance: 'source-only' },
      { id: 'backing', version: 1, deletionEpoch: 0 },
      [
        {
          registryVersion: 1,
          predicate: 'uses_tool',
          subject: { kind: 'self', personId: 'person' },
          object: { kind: 'new', entityKind: 'tool', title: 'Synthetic tool' },
          polarity: 'negative',
          modality: 'possible',
          temporalScope: 'past',
          conditions: ['if busy'],
          sourceSpans: [{ messageId: 'message', partIndex: 0, start: 0, end: 30, stance: 'self' }],
        },
      ],
    ),
  )()
  assert.equal(created.created.length, 1)
  const app = new OpenAPIHono()
  app.use('/api/*', async (c, next) => {
    if (c.req.header('Authorization') !== 'Bearer synthetic')
      return c.json({ error: 'unauthorized' }, 401)
    await next()
  })
  installKnowledgeRoutes(app, { knowledge, store })
  const body = {
    sceneId: 'scene',
    personId: 'person',
    anchor: { kind: 'person', personId: 'person' },
    predicates: ['uses_tool'],
    depth: 2,
  }
  const query = (extra: object = {}, auth = true) =>
    app.request('/api/memory-knowledge/query', {
      method: 'POST',
      headers: {
        'Content-Type': 'application/json',
        ...(auth ? { Authorization: 'Bearer synthetic' } : {}),
      },
      body: JSON.stringify({ ...body, ...extra }),
    })
  try {
    assert.equal((await query({}, false)).status, 401)
    assert.equal((await query({ passed: true })).status, 400)
    assert.equal((await query({ sceneId: 'simulation' })).status, 404)
    const response = await query()
    assert.equal(response.status, 200)
    const value = await response.json()
    const edge = value.edges[0]
    assert.equal(edge.status, 'candidate')
    assert.equal(edge.polarity, 'negative')
    assert.equal(edge.modality, 'possible')
    assert.equal(edge.temporalScope, 'past')
    assert.deepEqual(edge.conditions, ['if busy'])
    assert.equal(edge.proofs[0].sources[0].messageId, 'message')
    assert.equal(value.sourceBodyCoverage, 'unavailable')
    assert.doesNotMatch(
      JSON.stringify(value),
      /I may not use this tool if busy|spanHash|backingFingerprint|rootId/,
    )
    const anchor = {
      kind: 'entity',
      entityId: edge.object.entity.id,
      backingRef: edge.object.entity.backing,
    }
    assert.equal((await query({ anchor })).status, 200)
    const backingRef = { id: 'backing', version: 1, deletionEpoch: 0 }
    const memoryResult = await query({
      personId: undefined,
      anchor: { kind: 'memory', backingRef },
    })
    assert.equal(memoryResult.status, 200)
    assert.equal((await memoryResult.json()).edges.length, 1)
    assert.equal(
      (await query({ personId: undefined, seedBackingRef: backingRef, anchor })).status,
      200,
    )
    assert.equal((await query({ personId: undefined, anchor })).status, 400)
    assert.equal(
      (
        await query({
          personId: undefined,
          anchor: { kind: 'memory', backingRef: { ...backingRef, version: 2 } },
        })
      ).status,
      409,
    )
    assert.equal(
      (
        await query({
          sceneId: 'foreign',
          personId: undefined,
          anchor: { kind: 'memory', backingRef },
        })
      ).status,
      409,
    )

    assert.equal(
      (await query({ anchor: { ...anchor, backingRef: { ...anchor.backingRef, version: 2 } } }))
        .status,
      404,
    )
    assert.equal((await query({ personId: 'other', anchor })).status, 404)
    assert.deepEqual((await (await query({ sceneId: 'foreign' })).json()).edges, [])
    records.set('ordinary', {
      ...record,
      id: 'ordinary',
      content: 'Do not use the tool when busy; only before 2027.',
      epistemicKind: 'reported',
      kind: 'claim',
      validUntil: '2027-01-01T00:00:00Z',
    })
    records.set('child', { ...record, id: 'child' })
    const parent = created.created[0]!
    const childResult = store.db.transaction(() =>
      knowledge.admit(
        {
          sceneId: 'scene',
          simulation: false,
          provenance: 'contextual',
          exposedAssertions: [{ id: 'ordinary', version: 1, deletionEpoch: 0 }],
          exposedEdges: [{ id: parent.id, version: parent.version, backing: parent.backing }],
        },
        { id: 'child', version: 1, deletionEpoch: 0 },
        [
          {
            registryVersion: 1,
            predicate: 'prefers_topic',
            subject: { kind: 'self', personId: 'person' },
            object: { kind: 'new', entityKind: 'topic', title: 'Derived topic' },
            polarity: 'positive',
            modality: 'asserted',
            temporalScope: 'current',
            conditions: [],
            sourceSpans: [
              { messageId: 'message', partIndex: 0, start: 0, end: 30, stance: 'self' },
            ],
          },
        ],
      ),
    )()
    assert.equal(childResult.created.length, 1)
    const inherited = await (await query({ predicates: ['prefers_topic'], depth: 1 })).json()
    assert.equal(inherited.edges.length, 1)
    assert.equal(inherited.edges[0].readDependencies.length, 2)
    assert.equal(inherited.edges[0].requiresPersonalization, true)
    assert.equal(inherited.supportClosure.edges[0].polarity, 'negative')
    assert.deepEqual(inherited.supportClosure.edges[0].conditions, ['if busy'])
    const ordinary = inherited.supportClosure.records.find(
      (item: { ref: { id: string } }) => item.ref.id === 'ordinary',
    )
    assert.equal(ordinary.record.content, 'Do not use the tool when busy; only before 2027.')
    assert.equal(ordinary.record.epistemicKind, 'reported')
    assert.equal(ordinary.record.validUntil, '2027-01-01T00:00:00Z')
    assert.doesNotMatch(JSON.stringify(inherited), /backingFingerprint|proofId|spanHash|rootId/)
    enabled = false
    assert.deepEqual((await (await query()).json()).edges, [])
    enabled = true
    alive = false
    assert.deepEqual((await (await query()).json()).edges, [])
  } finally {
    store.close()
  }
})
