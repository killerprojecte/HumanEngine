import assert from 'node:assert/strict'
import test from 'node:test'
import { Store } from '../src/store.ts'
import { OpenAPIHono } from '@hono/zod-openapi'
import { installSemanticRoutes } from '../src/memory/interfaces/semantic-routes.ts'
import { SemanticService } from '../src/memory/semantic/candidates.ts'
import {
  semanticHash,
  type SemanticAssertion,
  type SemanticAuthority,
  type SemanticContext,
  type SemanticMutation,
  type SemanticSelection,
} from '../src/memory/semantic/contracts.ts'
const ctx: SemanticContext = { sceneId: 'scene', simulation: false }
function fixture() {
  const store = new Store(':memory:')
  const db = store.db
  const views = new Map<string, SemanticAssertion>()
  let enabled = true,
    now = Date.parse('2026-10-29T00:00:00Z'),
    seq = 0
  let partition = semanticHash('partition'),
    partitionReads = 0
  const mutations: SemanticMutation[] = []
  const versions = new Map<string, number>()
  const authority: SemanticAuthority = {
    assertReady() {},
    enabled: () => enabled,
    learningEnabled: () => true,
    autoSummary: () => true,
    partitionKey: () => {
      partitionReads++
      return partition
    },
    current(c, r) {
      return c.sceneId === ctx.sceneId &&
        r.version === (versions.get(r.id) ?? 1) &&
        r.deletionEpoch === 0
        ? views.get(r.id)
        : undefined
    },
    anchorCurrent(c, r) {
      return this.current(c, r)
    },
    episodeAnchor: (_c, a) => a.id === 'trusted-event' && a.version === 1,
  }
  const service = new SemanticService(db, authority, {
    now: () => now,
    id: () => `candidate-${++seq}`,
    beforeWrite: (m) => {
      mutations.push(m)
    },
  })
  const add = (id: string, root = id): SemanticSelection => {
    const source = { sceneId: ctx.sceneId, messageId: root, senderId: 'person' }
    views.set(id, {
      record: {
        id,
        scope: { kind: 'scene', sceneId: ctx.sceneId, personId: 'person' },
        content: `${id} only if not raining; never on Sunday`,
        kind: 'fact',
        epistemicKind: 'explicit',
        sources: [source],
        confidence: 1,
        importance: 'medium',
        learnedAt: '2026-10-29T00:00:00Z',
        status: 'active',
        locked: false,
        origin: 'manual',
      },
      proofs: [{ proofId: `proof-${id}`, sources: [source], parents: [], parentReferences: [] }],
      dependencyKey: semanticHash(id),
      roots: [semanticHash([ctx.sceneId, root])],
      rootCoverage: 'complete',
      derived: false,
    })
    return { ref: { id, version: 1, deletionEpoch: 0 }, dependencyKey: semanticHash(id) }
  }
  const proposal = (ids: string[]) => ({
    kind: 'summary_candidate',
    selectedParentIds: ids,
    text: 'Attributed summary; original conditions remain attached.',
    hypothesis: false,
  })
  return {
    store,
    db,
    views,
    versions,
    service,
    mutations,
    partition: (value: string) => {
      partition = value
    },
    partitionReads: () => partitionReads,
    add,
    proposal,
    run: <T>(fn: () => T) => db.transaction(fn)(),
    enabled: (v: boolean) => {
      enabled = v
    },
    time: (v: number) => {
      now = v
    },
  }
}

test('semantic administration preserves full originals, rejected review CAS, expiry and no source authority bypass', async () => {
  const f = fixture()
  for (const id of ['scene', 'foreign'])
    f.store.saveScene({
      id,
      kind: 'group',
      adapterId: 'fixture',
      accountId: 'account',
      platformSceneId: id,
      name: 'Synthetic',
      learningEnabled: false,
      participationEnabled: false,
      proactiveEnabled: false,
    })
  const parents = [f.add('a'), f.add('b'), f.add('unselected')]
  const created = f.run(() =>
    f.service.admit(ctx, parents, [{ ...f.proposal(['a', 'b']), hypothesis: true }]),
  ).created[0]!
  const app = new OpenAPIHono()
  app.use('/api/*', async (c, next) => {
    if (c.req.header('Authorization') !== 'Bearer synthetic')
      return c.json({ error: 'unauthorized' }, 401)
    await next()
  })
  installSemanticRoutes(app, { semantic: f.service, store: f.store })
  const headers = { Authorization: 'Bearer synthetic', 'Content-Type': 'application/json' }
  const get = async (sceneId = 'scene') =>
    (
      await app.request('/api/memory-semantics?' + new URLSearchParams({ sceneId }), { headers })
    ).json()
  const review = (expectedVersion: number, status: string, extra = {}) =>
    app.request('/api/memory-semantics/' + created.id + '/review', {
      method: 'POST',
      headers,
      body: JSON.stringify({
        sceneId: 'scene',
        expectedVersion,
        status,
        confirmed: true,
        ...extra,
      }),
    })
  try {
    assert.equal((await app.request('/api/memory-semantics?sceneId=scene')).status, 401)
    assert.equal(
      (await app.request('/api/memory-semantics?sceneId=simulation', { headers })).status,
      404,
    )
    assert.equal((await get('foreign')).items.length, 0)
    const page = await get()
    assert.equal(page.items.length, 1)
    assert.equal(page.items[0].declarations.length, 3)
    assert.ok(
      page.items[0].declarations.every((d: { record: { content: string } }) =>
        d.record.content.includes('never on Sunday'),
      ),
    )
    assert.equal(page.items[0].parents.length, 3)
    assert.equal(page.items[0].rootCount, 3)
    assert.doesNotMatch(
      JSON.stringify(page),
      /proofId|fingerprint|dependencyKey|partitionKey|inputKey|[a-f0-9]{64}/,
    )
    assert.equal((await review(1, 'rejected', { passed: true })).status, 400)
    assert.equal((await review(1, 'rejected')).status, 200)
    assert.equal(f.service.list(ctx).items.length, 0)
    assert.equal((await get()).items[0].status, 'rejected')
    assert.equal((await review(1, 'admitted')).status, 409)
    assert.equal((await review(2, 'admitted')).status, 200)
    f.time(Date.parse('2026-11-10T00:00:00Z'))
    const expired = await get()
    assert.equal(expired.items[0].applicability, 'review_needed')
    assert.equal(expired.items[0].declarations[0].record.status, 'active')
    f.versions.set('unselected', 2)
    const stale = await get()
    assert.equal(stale.items.length, 0)
    assert.equal(stale.unavailable[0].id, created.id)
    assert.deepEqual(Object.keys(stale.unavailable[0]).sort(), [
      'id',
      'reason',
      'status',
      'version',
    ])
    assert.doesNotMatch(JSON.stringify(stale), /never on Sunday|Attributed summary/)
    assert.equal((await review(3, 'admitted')).status, 409)
    f.enabled(false)
    assert.equal((await get()).items.length, 0)
  } finally {
    f.store.close()
  }
})
