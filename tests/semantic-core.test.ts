import assert from 'node:assert/strict'
import test from 'node:test'
import Database from 'better-sqlite3'
import { SemanticService } from '../src/memory/semantic/candidates.ts'
import {
  semanticHash,
  semanticFingerprint,
  type SemanticAnchorQualification,
  type SemanticAssertion,
  type SemanticAuthority,
  type SemanticContext,
  type SemanticMutation,
  type SemanticSelection,
} from '../src/memory/semantic/contracts.ts'
const ctx: SemanticContext = { sceneId: 'scene', simulation: false }
const trustedEpisode = {
  anchor: { id: 'trusted-event', version: 1 },
  membershipKey: semanticHash('membership'),
  anchorQualifications: [
    {
      edgeId: 'edge',
      edgeVersion: 1,
      registryVersion: 1,
      predicate: 'participates_in',
      subject: { kind: 'person', personId: 'person' },
      object: {
        kind: 'entity',
        entityId: 'trusted-event',
        entityKind: 'event',
        backing: { id: 'a', version: 1, deletionEpoch: 0 },
        originEdgeId: 'edge',
      },
      polarity: 'negative',
      modality: 'possible',
      temporalScope: 'past',
      conditions: ['Only if safe'],
      occurredAt: null,
      validFrom: null,
      validUntil: null,
      status: 'candidate',
      admission: 'qualified_candidate',
      interpretation: 'attributed_proposed_association_not_observed_event',
    },
  ] satisfies SemanticAnchorQualification[],
}
function fixture() {
  const db = new Database(':memory:')
  const views = new Map<string, SemanticAssertion>()
  let enabled = true,
    now = Date.parse('2026-10-29T00:00:00Z'),
    seq = 0
  let partition = semanticHash('partition'),
    partitionReads = 0,
    membership = trustedEpisode.membershipKey
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
    anchorCurrent(c, r) {
      return c.sceneId === ctx.sceneId &&
        r.version === (versions.get(r.id) ?? 1) &&
        r.deletionEpoch === 0
        ? views.get(r.id)
        : undefined
    },
    current(c, r) {
      return c.sceneId === ctx.sceneId &&
        r.version === (versions.get(r.id) ?? 1) &&
        r.deletionEpoch === 0
        ? views.get(r.id)
        : undefined
    },
    episodeAnchor: (_c, a, _parents, key) =>
      a.id === 'trusted-event' && a.version === 1 && key === membership,
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
    db,
    views,
    versions,
    service,
    mutations,
    episode: () => ({
      ...trustedEpisode,
      anchorDependencies: [
        {
          ref: { id: 'a', version: 1, deletionEpoch: 0 },
          fingerprint: semanticFingerprint(views.get('a')!.record),
          proofs: views.get('a')!.proofs,
        },
      ],
    }),
    partition: (value: string) => {
      partition = value
    },
    partitionReads: () => partitionReads,
    membership: (value: string) => {
      membership = value
    },
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
test('automatic attributed view preserves full original conditions, root dedup and all visible unselected parents', () => {
  const f = fixture()
  try {
    const parents = [f.add('a', 'same-root'), f.add('b', 'same-root'), f.add('c')]
    const result = f.run(() => f.service.admit(ctx, parents, [f.proposal(['a', 'b'])]))
    assert.equal(result.rejected.length, 0)
    const item = result.created[0]!
    assert.equal(item.status, 'admitted')
    assert.equal(item.parents.length, 3)
    const view = f.service.current(ctx, item.id)!
    assert.equal(view.roots.length, 2)
    assert.equal(view.declarations.length, 3)
    assert.ok(view.declarations.every((d) => d.record.content.endsWith('never on Sunday')))
    assert.equal(
      f.run(() => f.service.admit(ctx, parents, [f.proposal(['a', 'b'])])).created.length,
      0,
    )
    f.run(() => f.service.lifecycle.purgeSource('scene', 'c'))
    assert.equal(f.service.storage.get(item.id), undefined)
    const row = f.db
      .prepare('SELECT value FROM memory_semantic_candidates WHERE id=?')
      .get(item.id) as { value: unknown }
    assert.equal(row.value, null)
  } finally {
    f.db.close()
  }
})
test('exact dirty snapshot, subject partition and derived inputs reject without downgrade', () => {
  const f = fixture()
  try {
    const parents = [f.add('a'), f.add('b')]
    f.versions.set('a', 2)
    assert.equal(
      f.run(() => f.service.admit(ctx, parents, [f.proposal(['a', 'b'])])).rejected[0]!.reason,
      'semantic_parent_unavailable',
    )
    f.versions.set('a', 1)
    f.views.get('b')!.record.scope = { kind: 'scene', sceneId: 'scene', personId: 'other' }
    assert.equal(
      f.run(() => f.service.admit(ctx, parents, [f.proposal(['a', 'b'])])).rejected[0]!.reason,
      'semantic_subject_partition',
    )
    f.views.get('b')!.record.scope = { kind: 'scene', sceneId: 'scene', personId: 'person' }
    f.views.get('a')!.derived = true
    assert.equal(
      f.run(() => f.service.admit(ctx, parents, [f.proposal(['a', 'b'])])).created.length,
      0,
    )
    assert.equal(f.mutations.length, 0)
  } finally {
    f.db.close()
  }
})
test('episode anchor is exact and grouping never asserts an observed shared event; hypothesis expiry keeps declaration', () => {
  const f = fixture()
  try {
    const parents = [f.add('a'), f.add('b')]
    const proposal = {
      ...f.proposal(['a', 'b']),
      kind: 'episode_candidate',
      episodeAnchor: { id: 'guessed', version: 1 },
      hypothesis: true,
    }
    assert.equal(f.run(() => f.service.admit(ctx, parents, [proposal])).created.length, 0)
    proposal.episodeAnchor.id = 'trusted-event'
    const item = f.run(() => f.service.admit(ctx, parents, [proposal], f.episode())).created[0]!
    assert.equal(item.status, 'candidate')
    f.time(Date.parse(item.applicableUntil!) + 1)
    const view = f.service.current(ctx, item.id)!
    assert.equal(view.applicability, 'review_needed')
    assert.equal(view.interpretation, 'grouped_declarations_not_observed_event')
    assert.equal(view.candidate.text, item.text)
  } finally {
    f.db.close()
  }
})
test('indirect parent privacy pruning physically removes candidate but ordinary version drift does not', () => {
  const f = fixture()
  try {
    const parents = [f.add('a'), f.add('b')]
    f.views.get('a')!.proofs[0]!.parents = ['ancestor']
    f.views.get('a')!.proofs[0]!.parentReferences = [
      { id: 'ancestor', version: 1, deletionEpoch: 0 },
    ]
    const item = f.run(() =>
      f.service.admit(ctx, parents, [
        { ...f.proposal(['a', 'b']), text: 'PRIVATE_DERIVED_CANARY' },
      ]),
    ).created[0]!
    f.versions.set('a', 2)
    assert.equal(f.service.current(ctx, item.id), undefined)
    assert.ok(f.service.storage.get(item.id))
    assert.throws(
      () =>
        f.run(() =>
          f.service.lifecycle.pruneUnavailableProof(ctx, () => {
            throw Error('unknown')
          }),
        ),
      /unknown/,
    )
    assert.ok(f.service.storage.get(item.id))
    f.enabled(false)
    f.run(() =>
      f.service.lifecycle.pruneUnavailableProof(ctx, (_ref, p) => !p.parents.includes('ancestor')),
    )
    assert.equal(f.service.storage.get(item.id), undefined)
    assert.equal(
      JSON.stringify(f.db.prepare('SELECT * FROM memory_semantic_candidates').all()).includes(
        'PRIVATE_DERIVED_CANARY',
      ),
      false,
    )
  } finally {
    f.db.close()
  }
})
test('original independent OR survives only exact recall requalification; newer proof cannot replace original', () => {
  const f = fixture()
  try {
    const parents = [f.add('a'), f.add('b')]
    f.add('alternate')
    f.views.get('a')!.proofs.push(f.views.get('alternate')!.proofs[0]!)
    f.views.get('a')!.roots.push(...f.views.get('alternate')!.roots)
    const item = f.run(() => f.service.admit(ctx, parents, [f.proposal(['a', 'b'])])).created[0]!
    f.run(() => f.service.lifecycle.purgeSource('scene', 'a'))
    f.views.get('a')!.proofs = [f.views.get('alternate')!.proofs[0]!]
    f.views.get('a')!.roots = f.views.get('alternate')!.roots
    f.versions.set('a', 2)
    f.views.get('a')!.dependencyKey = semanticHash('changed')
    f.run(() =>
      f.service.lifecycle.requalify(ctx, [
        { before: { ...parents[0]!.ref, version: 99 }, after: { ...parents[0]!.ref, version: 2 } },
      ]),
    )
    assert.equal(f.service.current(ctx, item.id), undefined)
    f.enabled(false)
    f.run(() =>
      f.service.lifecycle.requalify(ctx, [
        { before: parents[0]!.ref, after: { ...parents[0]!.ref, version: 2 } },
      ]),
    )
    f.enabled(true)
    assert.equal(
      f.service.current(ctx, item.id)!.candidate.parents[0]!.proofs[0]!.proofId,
      'proof-alternate',
    )
  } finally {
    f.db.close()
  }
})
test('attempt receipts consume unchanged and unknown keys, clean locators and never restart model work', () => {
  const f = fixture()
  try {
    const parents = [f.add('a'), f.add('b')],
      inputKey = semanticHash(['algorithm1', 'prompt1', parents])
    assert.equal(
      f.run(() => f.service.beginAttempt(ctx, { inputKey, parents })),
      true,
    )
    assert.equal(
      f.run(() => f.service.beginAttempt(ctx, { inputKey, parents })),
      false,
    )
    f.run(() => f.service.finishAttempt(ctx, inputKey, 'abandoned'))
    f.run(() => f.service.lifecycle.purgeAssertion('a'))
    assert.deepEqual(f.service.storage.attempts()[0]!.parents, [])
    assert.equal(
      f.run(() => f.service.beginAttempt(ctx, { inputKey, parents })),
      false,
    )
  } finally {
    f.db.close()
  }
})
test('review CAS and rollback preserve committed status; simulation makes no tables', () => {
  const f = fixture()
  try {
    const parents = [f.add('a'), f.add('b')]
    assert.equal(
      f.run(() => f.service.admit({ ...ctx, simulation: true }, parents, [f.proposal(['a', 'b'])]))
        .created.length,
      0,
    )
    assert.equal(
      f.db.prepare("SELECT name FROM sqlite_master WHERE name LIKE 'memory_semantic_%'").all()
        .length,
      0,
    )
    const item = f.run(() => f.service.admit(ctx, parents, [f.proposal(['a', 'b'])])).created[0]!
    assert.throws(
      () => f.run(() => f.service.review(ctx, item.id, 99, 'rejected')),
      /version_conflict/,
    )
    assert.throws(
      () =>
        f.run(() => {
          f.service.review(ctx, item.id, 1, 'rejected')
          throw Error('rollback')
        }),
      /rollback/,
    )
    assert.equal(f.service.current(ctx, item.id)!.candidate.version, 1)
    f.run(() => f.service.review(ctx, item.id, 1, 'rejected'))
    assert.equal(f.service.current(ctx, item.id), undefined)
  } finally {
    f.db.close()
  }
})

test('same-scene explicit event anchor permits distinct participant declarations but not a summary merge', () => {
  const f = fixture()
  try {
    const parents = [f.add('a'), f.add('b')]
    f.views.get('b')!.record.scope = { kind: 'scene', sceneId: 'scene', personId: 'other' }
    assert.equal(
      f.run(() => f.service.admit(ctx, parents, [f.proposal(['a', 'b'])])).created.length,
      0,
    )
    const item = f.run(() =>
      f.service.admit(
        ctx,
        parents,
        [
          {
            ...f.proposal(['a', 'b']),
            kind: 'episode_candidate',
            episodeAnchor: { id: 'trusted-event', version: 1 },
          },
        ],
        f.episode(),
      ),
    ).created[0]!
    assert.equal(item.personId, null)
    assert.equal(item.status, 'candidate')
    assert.deepEqual(
      f.service.current(ctx, item.id)!.declarations.map((d) => d.record.scope),
      [
        { kind: 'scene', sceneId: 'scene', personId: 'person' },
        { kind: 'scene', sceneId: 'scene', personId: 'other' },
      ],
    )
  } finally {
    f.db.close()
  }
})

test('capacity stops grants but never terminal privacy or review; persisted malformed bodies fail closed', () => {
  const f = fixture()
  try {
    const parents = [f.add('a'), f.add('b')]
    const first = f.run(() => f.service.admit(ctx, parents, [f.proposal(['a', 'b'])])).created[0]!
    // Fill only synthetic tombstone metadata; no copied records or extra model work.
    f.run(() => {
      const put = f.db.prepare(
        'INSERT INTO memory_semantic_candidates(id,scene_id,input_key,version,value) VALUES(?,?,?,?,NULL)',
      )
      for (let i = 1; i < 40; i++) put.run(`tombstone-${i}`, 'scene', semanticHash(i), 1)
    })
    const before = f.mutations.length
    assert.equal(
      f.run(() =>
        f.service.admit(ctx, parents, [{ ...f.proposal(['a', 'b']), kind: 'duplicate_candidate' }]),
      ).rejected[0]!.reason,
      'semantic_capacity',
    )
    assert.equal(f.mutations.length, before)
    f.run(() => f.service.review(ctx, first.id, 1, 'rejected'))
    f.run(() => f.service.lifecycle.erase(ctx, first.id))
    assert.equal(f.service.storage.get(first.id), undefined)
    f.db
      .prepare('UPDATE memory_semantic_candidates SET value=? WHERE id=?')
      .run(Buffer.from('{}'), 'tombstone-1')
    assert.throws(() => f.service.storage.rows(), /semantic_storage_invalid/)
  } finally {
    f.db.close()
  }
})

test('unknown authority failures and asynchronous prewrite hooks fail closed before local effects', () => {
  const f = fixture()
  try {
    const parents = [f.add('a'), f.add('b')]
    const base: SemanticAuthority = {
      assertReady() {},
      enabled: () => true,
      learningEnabled: () => true,
      autoSummary: () => true,
      partitionKey: () => semanticHash('partition'),
      episodeAnchor: () => false,
      anchorCurrent() {
        throw Error('provenance_budget_unknown')
      },
      current() {
        throw Error('provenance_budget_unknown')
      },
    }
    const unknown = new SemanticService(f.db, base)
    assert.throws(
      () => f.run(() => unknown.admit(ctx, parents, [f.proposal(['a', 'b'])])),
      /provenance_budget_unknown/,
    )
    const asynchronous = new SemanticService(
      f.db,
      {
        ...base,
        current(_c, r) {
          return f.views.get(r.id)
        },
      },
      { beforeWrite: () => Promise.resolve() },
    )
    assert.throws(
      () => f.run(() => asynchronous.admit(ctx, parents, [f.proposal(['a', 'b'])])),
      /semantic_async_hook_denied/,
    )
    assert.equal(
      f.db.prepare("SELECT name FROM sqlite_master WHERE name LIKE 'memory_semantic_%'").all()
        .length,
      0,
    )
  } finally {
    f.db.close()
  }
})

test('attempt capacity reserves cleanup and terminal completion, not unlimited model retries', () => {
  const f = fixture()
  try {
    const parents = [f.add('a'), f.add('b')],
      inputKey = semanticHash('first-attempt')
    assert.equal(
      f.run(() => f.service.beginAttempt(ctx, { inputKey, parents })),
      true,
    )
    const template = f.service.storage.attempts()[0]!
    f.run(() => {
      const put = f.db.prepare(
        'INSERT INTO memory_semantic_attempts(scene_id,input_key,value) VALUES(?,?,?)',
      )
      for (let i = 1; i < 4096; i++) {
        const key = semanticHash(['attempt', i])
        put.run(
          'scene',
          key,
          JSON.stringify({
            ...template,
            inputKey: key,
            parents: [],
            roots: [],
            status: 'abandoned',
          }),
        )
      }
    })
    const before = f.mutations.length
    assert.equal(
      f.run(() => f.service.beginAttempt(ctx, { inputKey: semanticHash('overflow'), parents })),
      false,
    )
    assert.equal(f.mutations.length, before)
    f.run(() => f.service.finishAttempt(ctx, inputKey, 'finished'))
    f.run(() => f.service.lifecycle.purgeAssertion('a'))
    const result = f.service.storage.attempts().find((a) => a.inputKey === inputKey)!
    assert.equal(result.status, 'abandoned')
    assert.deepEqual(result.parents, [])
    assert.equal(
      f.run(() => f.service.beginAttempt(ctx, { inputKey, parents })),
      false,
    )
  } finally {
    f.db.close()
  }
})

test('person partition changes invalidate current summaries without deleting history; one key read per list partition', () => {
  const f = fixture()
  try {
    const parents = [f.add('a'), f.add('b')]
    const items = f.run(() =>
      f.service.admit(ctx, parents, [
        f.proposal(['a', 'b']),
        { ...f.proposal(['a', 'b']), kind: 'duplicate_candidate' },
      ]),
    ).created
    assert.equal(items.length, 2)
    const reads = f.partitionReads()
    assert.equal(f.service.list(ctx).items.length, 2)
    assert.equal(f.partitionReads() - reads, 1)
    f.partition(semanticHash('new-member-or-source-head'))
    assert.equal(f.service.current(ctx, items[0]!.id), undefined)
    assert.equal(f.service.list(ctx).items.length, 0)
    assert.equal(f.service.storage.get(items[0]!.id)!.text, items[0]!.text)
    const fresh = f.run(() => f.service.admit(ctx, parents, [f.proposal(['a', 'b'])])).created[0]!
    assert.notEqual(fresh.inputKey, items[0]!.inputKey)
    f.run(() => f.service.review(ctx, fresh.id, 1, 'rejected'))
    assert.equal(f.service.current(ctx, fresh.id), undefined)
    f.run(() => f.service.review(ctx, fresh.id, 2, 'admitted'))
    assert.equal(f.service.current(ctx, fresh.id)!.candidate.version, 3)
    f.run(() => f.service.review(ctx, fresh.id, 3, 'rejected'))
    f.partition(semanticHash('changed-again'))
    assert.throws(
      () => f.run(() => f.service.review(ctx, fresh.id, 4, 'admitted')),
      /parent_unavailable/,
    )
    assert.equal(f.service.storage.get(fresh.id)!.status, 'rejected')
  } finally {
    f.db.close()
  }
})

test('recall updates partition only for pre-current candidates, preserving OR without reviving an already stale summary', () => {
  const f = fixture()
  try {
    const parents = [f.add('a'), f.add('b')]
    f.add('alternate')
    f.views.get('a')!.proofs.push(f.views.get('alternate')!.proofs[0]!)
    f.views.get('a')!.roots.push(...f.views.get('alternate')!.roots)
    const old = f.run(() => f.service.admit(ctx, parents, [f.proposal(['a', 'b'])])).created[0]!
    f.partition(semanticHash('membership-after-new-declaration'))
    const current = f.run(() => f.service.admit(ctx, parents, [f.proposal(['a', 'b'])])).created[0]!
    f.enabled(false)
    const captured = f.service.captureQualification(ctx)
    assert.deepEqual(
      captured.map((c) => c.id),
      [current.id],
    )
    f.run(() => f.service.lifecycle.purgeSource('scene', 'a'))
    f.views.get('a')!.proofs = [f.views.get('alternate')!.proofs[0]!]
    f.views.get('a')!.roots = f.views.get('alternate')!.roots
    f.versions.set('a', 2)
    f.views.get('a')!.dependencyKey = semanticHash('recall-survivor')
    f.partition(semanticHash('partition-after-recall'))
    f.run(() =>
      f.service.lifecycle.requalify(
        ctx,
        [{ before: parents[0]!.ref, after: { ...parents[0]!.ref, version: 2 } }],
        captured,
      ),
    )
    f.enabled(true)
    assert.ok(f.service.current(ctx, current.id))
    assert.equal(f.service.current(ctx, old.id), undefined)
    assert.equal(f.service.storage.get(old.id)!.partitionKey, old.partitionKey)
    assert.equal(
      f.service.storage.get(current.id)!.partitionKey,
      semanticHash('partition-after-recall'),
    )
  } finally {
    f.db.close()
  }
})

test('admin view shows freshly qualified rejected candidates but stale diagnostics never reveal body', () => {
  const f = fixture()
  try {
    const parents = [f.add('a'), f.add('b')]
    const item = f.run(() =>
      f.service.admit(ctx, parents, [{ ...f.proposal(['a', 'b']), text: 'ADMIN_PRIVATE_CANARY' }]),
    ).created[0]!
    f.run(() => f.service.review(ctx, item.id, 1, 'rejected'))
    assert.equal(f.service.list(ctx).items.length, 0)
    assert.equal(f.service.adminList(ctx).items[0]!.candidate.status, 'rejected')
    f.partition(semanticHash('changed-membership'))
    const stale = f.service.adminList(ctx)
    assert.equal(stale.items.length, 0)
    assert.deepEqual(stale.unavailable, [
      { id: item.id, version: 2, status: 'rejected', reason: 'qualification_unavailable' },
    ])
    assert.equal(JSON.stringify(stale).includes('ADMIN_PRIVATE_CANARY'), false)
    f.enabled(false)
    assert.deepEqual(f.service.adminList(ctx), { items: [], unavailable: [], coverage: 'complete' })
  } finally {
    f.db.close()
  }
})

test('episode membership is server captured and persisted; changed edge qualifiers cannot renew old candidate', () => {
  const f = fixture()
  try {
    const parents = [f.add('a'), f.add('b')]
    const proposal = {
      ...f.proposal(['a', 'b']),
      kind: 'episode_candidate',
      episodeAnchor: trustedEpisode.anchor,
    }
    assert.equal(
      f.run(() => f.service.admit(ctx, parents, [proposal])).rejected[0]!.reason,
      'semantic_episode_membership_required',
    )
    assert.equal(
      f.run(() =>
        f.service.admit(
          ctx,
          parents,
          [{ ...proposal, episodeMembershipKey: trustedEpisode.membershipKey }],
          f.episode(),
        ),
      ).created.length,
      0,
    )
    const item = f.run(() => f.service.admit(ctx, parents, [proposal], f.episode())).created[0]!
    assert.equal(item.episodeMembershipKey, trustedEpisode.membershipKey)
    assert.deepEqual(item.anchorQualifications, trustedEpisode.anchorQualifications)
    assert.deepEqual(
      f.service.current(ctx, item.id)!.candidate.anchorQualifications,
      trustedEpisode.anchorQualifications,
    )
    assert.ok(f.service.current(ctx, item.id))
    f.membership(semanticHash('same-parents-new-edge-conditions'))
    assert.equal(f.service.current(ctx, item.id), undefined)
    assert.throws(
      () => f.run(() => f.service.review(ctx, item.id, 1, 'admitted')),
      /parent_unavailable/,
    )
    assert.equal(f.service.storage.get(item.id)!.episodeMembershipKey, trustedEpisode.membershipKey)
  } finally {
    f.db.close()
  }
})

for (const operation of ['source', 'assertion', 'historical_parent'] as const)
  test(`episode anchor ${operation} privacy removes derived text while ordinary parents and independent summary survive`, () => {
    const f = fixture()
    try {
      const parents = [f.add('a'), f.add('b')]
      f.add('marker')
      const marker = f.views.get('marker')!
      marker.record.internalUse = 'knowledge_declaration'
      marker.proofs[0]!.parents = ['private-ancestor']
      marker.proofs[0]!.parentReferences = [
        { id: 'private-ancestor', version: 1, deletionEpoch: 0 },
      ]
      const trusted = {
        ...trustedEpisode,
        anchorDependencies: [
          {
            ref: { id: 'marker', version: 1, deletionEpoch: 0 },
            fingerprint: semanticFingerprint(marker.record),
            proofs: structuredClone(marker.proofs),
          },
        ],
      }
      const episode = f.run(() =>
        f.service.admit(
          ctx,
          parents,
          [
            {
              ...f.proposal(['a', 'b']),
              kind: 'episode_candidate',
              episodeAnchor: trusted.anchor,
              text: 'PRIVATE_ANCHOR_EPISODE_CANARY',
            },
          ],
          trusted,
        ),
      ).created[0]!
      assert.ok(episode)
      const summary = f.run(() => f.service.admit(ctx, parents, [f.proposal(['a', 'b'])]))
        .created[0]!
      f.enabled(false)
      f.run(() => {
        if (operation === 'source') f.service.lifecycle.purgeSource('scene', 'marker')
        else if (operation === 'assertion') f.service.lifecycle.purgeAssertion('marker')
        else
          f.service.lifecycle.pruneUnavailableProof(
            ctx,
            (_r, p) => !p.parents.includes('private-ancestor'),
          )
      })
      assert.equal(f.service.storage.get(episode.id), undefined)
      assert.ok(f.service.storage.get(summary.id))
      assert.ok(f.views.get('a'))
      assert.ok(f.views.get('b'))
      assert.equal(
        JSON.stringify(f.db.prepare('SELECT * FROM memory_semantic_candidates').all()).includes(
          'PRIVATE_ANCHOR_EPISODE_CANARY',
        ),
        false,
      )
    } finally {
      f.db.close()
    }
  })
