import assert from 'node:assert/strict'
import test from 'node:test'
import Database from 'better-sqlite3'
import { CommitmentService } from '../src/memory/commitments/service.ts'
import type {
  CommitmentAssertion,
  CommitmentContext,
  CommitmentRef,
  CommitmentTerms,
} from '../src/memory/domain/commitments.ts'
function fixture() {
  const db = new Database(':memory:')
  let ids = 0
  let enabled = true
  const versions = new Map<string, CommitmentRef>()
  let onWrite: (() => void) | undefined
  const views = new Map<string, CommitmentAssertion>()
  const source = (id: string, person: string) => ({ sceneId: 's', messageId: id, senderId: person })
  const add = (id: string, person: string) => {
    views.set(id, {
      record: {
        id,
        scope: { kind: 'scene', sceneId: 's', personId: person },
        content: 'Synthetic plan declaration',
        kind: 'fact',
        epistemicKind: 'explicit',
        sources: [source(id, person)],
        importance: 'low',
        confidence: 0.5,
        learnedAt: '2026-10-28T00:00:00Z',
        status: 'active',
        locked: false,
        origin: 'learned',
        lineageStatus: 'verified',
      },
      proofs: [
        {
          proofId: id,
          sources: [source(id, person)],
          parents: [],
          parentReferences: [],
          subjectWitness: { kind: 'self', personId: person, sourceMessageId: id },
        },
      ],
    })
    return { id, version: 1, deletionEpoch: 0 }
  }
  const service = new CommitmentService(
    db,
    {
      enabled: () => enabled,
      learningEnabled: () => true,
      current: (_, r) =>
        !versions.has(r.id) || JSON.stringify(versions.get(r.id)) === JSON.stringify(r)
          ? views.get(r.id)
          : undefined,
      surviving: (_, e) => {
        const view = views.get(e.backing.id)
        return view ? { ref: versions.get(e.backing.id) ?? e.backing, view } : undefined
      },
      beforeWrite: () => onWrite?.(),
      historical: (_, r) => views.get(r.id),
      people: () => ['a', 'b'],
    },
    { id: () => `c${++ids}`, now: () => '2026-10-28T00:00:00Z' },
  )
  const ctx = (ref?: CommitmentRef): CommitmentContext => ({
    sceneId: 's',
    simulation: false,
    provenance: ref ? 'contextual' : 'source-only',
    exposedRefs: ref
      ? [{ ...ref, mutationVersion: service.storage.head(ref.id)!.mutationVersion }]
      : [],
  })
  const terms = (hour: string): CommitmentTerms => ({
    kind: 'plan',
    title: 'Meet',
    participantIds: ['a', 'b'],
    polarity: 'positive',
    modality: 'asserted',
    temporalScope: 'future',
    conditions: [],
    timeQualification: hour,
  })
  const admit = (backing: CommitmentRef, command: unknown, ref?: CommitmentRef) =>
    db.transaction(() => service.admit(ctx(ref), backing, command))()
  return {
    enabled: (value: boolean) => {
      enabled = value
    },
    db,
    service,
    views,
    versions,
    add,
    ctx,
    terms,
    admit,
    onWrite: (fn: (() => void) | undefined) => {
      onWrite = fn
    },
  }
}
test('two participants confirm same terms; amendment keeps old current until all confirm; revoked activation never rolls back', () => {
  const f = fixture()
  const first = f.admit(f.add('p18', 'a'), { kind: 'propose', terms: f.terms('18:00') })
  assert.equal(first.status, 'saved')
  const ref = first.item!.ref
  assert.deepEqual(first.item!.confirmedParticipantIds, ['a'])
  assert.equal(first.item!.confirmation, 'proposed')
  const confirmed = f.admit(f.add('b18', 'b'), { kind: 'confirm', target: ref }, ref)
  assert.equal(confirmed.item!.confirmation, 'confirmed')
  const amendment = f.admit(
    f.add('p19', 'a'),
    { kind: 'amend', target: ref, terms: f.terms('19:00') },
    ref,
  )
  const next = amendment.item!.ref
  assert.equal(next.version, 2)
  assert.equal(f.service.list(f.ctx()).items[0]!.terms!.timeQualification, '18:00')
  f.admit(f.add('b19', 'b'), { kind: 'confirm', target: next }, next)
  assert.equal(f.service.list(f.ctx()).items[0]!.terms!.timeQualification, '19:00')
  f.db.transaction(() => f.service.purgeSource('s', 'b19'))()
  f.views.delete('b19')
  const now = f.service.list(f.ctx()).items[0]!
  assert.equal(now.confirmation, 'incomplete')
  assert.equal(now.ref.version, 2)
  assert.notEqual(now.terms?.timeQualification, '18:00')
  assert.equal(f.service.storage.head(ref.id)!.everActivatedVersion, 2)
  assert.ok(
    f.service
      .history(f.ctx(next), next)!
      .revisions.some((r) => r.terms?.timeQualification === '18:00'),
  )
  f.db.close()
})
test('unexposed/stale/candidate conditions do not confirm; completion requires each self report, no implicit date/ACK', () => {
  const f = fixture()
  const initial = f.admit(f.add('p', 'a'), { kind: 'propose', terms: f.terms('unknown date') })
  const ref = initial.item!.ref
  assert.equal(f.admit(f.add('b', 'b'), { kind: 'confirm', target: ref }).status, 'rejected')
  f.admit(f.add('b2', 'b'), { kind: 'confirm', target: ref }, ref)
  const partial = f.admit(f.add('donea', 'a'), { kind: 'complete', target: ref }, ref)
  assert.equal(partial.item!.lifecycle, 'pending')
  const done = f.admit(f.add('doneb', 'b'), { kind: 'complete', target: ref }, ref)
  assert.equal(done.item!.lifecycle, 'done')
  assert.equal(done.item!.qualification, 'declared_not_verified')
  assert.equal(done.item!.terms!.occurredAt, undefined)
  const possible = f.admit(f.add('maybe', 'a'), {
    kind: 'propose',
    terms: { ...f.terms('maybe'), modality: 'possible' },
  })
  assert.equal(possible.item!.confirmation, 'proposed')
  assert.deepEqual(possible.item!.confirmedParticipantIds, [])
  assert.equal(
    f.admit(
      f.add('maybeb', 'b'),
      { kind: 'confirm', target: possible.item!.ref },
      possible.item!.ref,
    ).status,
    'rejected',
  )
  assert.throws(
    () => f.service.admit(f.ctx(), f.add('outside', 'a'), { kind: 'propose', terms: f.terms('x') }),
    /transaction/,
  )
  f.db.close()
})

test('trusted OR survivor requalification keeps original premise, rejects changed content and new proofs', () => {
  const f = fixture()
  const backing = f.add('root', 'a')
  const original = f.views.get('root')!
  const independent = structuredClone(original.proofs[0]!)
  independent.proofId = 'independent'
  independent.sources[0]!.messageId = 'independent'
  independent.subjectWitness!.sourceMessageId = 'independent'
  original.proofs.push(independent)
  const created = f.admit(backing, {
    kind: 'propose',
    terms: { ...f.terms('18'), participantIds: ['a'] },
  })
  const ref = created.item!.ref
  assert.equal(created.item!.confirmation, 'confirmed')
  f.db.transaction(() => f.service.purgeSource('s', 'root'))()
  original.proofs = [independent]
  f.versions.set('root', { ...backing, version: 2, deletionEpoch: 1 })
  assert.equal(f.service.current(f.ctx(), ref)!.confirmation, 'incomplete')
  f.db.transaction(() =>
    f.service.requalify(f.ctx(), [
      { from: { ...backing, version: 9 }, to: f.versions.get('root')! },
    ]),
  )()
  assert.equal(f.service.current(f.ctx(), ref)!.confirmation, 'incomplete')
  f.db.transaction(() => {
    const head = f.service.storage.head(ref.id)!
    for (let i = 0; i < 201; i++)
      f.service.storage.put({
        ...head,
        id: `padding-${i}`,
        activeVersion: null,
        everActivatedVersion: null,
        everTerminal: null,
      })
  })()
  assert.equal(
    f.service.storage.heads('s').some((head) => head.id === ref.id),
    false,
  )
  f.enabled(false)
  f.db.transaction(() =>
    f.service.requalify(f.ctx(), [{ from: backing, to: f.versions.get('root')! }]),
  )()
  assert.equal(f.service.current(f.ctx(), ref), undefined)
  f.enabled(true)
  const current = f.service.current(f.ctx(), ref)!
  assert.equal(current.confirmation, 'confirmed')
  assert.deepEqual(current.evidence[0]!.backing, backing)
  assert.equal(current.evidence[0]!.qualificationRef!.deletionEpoch, 1)
  original.record.content = 'Different meaning'
  f.versions.set('root', { ...backing, version: 3, deletionEpoch: 1 })
  f.db.transaction(() =>
    f.service.requalify(f.ctx(), [
      { from: { ...backing, version: 2, deletionEpoch: 1 }, to: f.versions.get('root')! },
    ]),
  )()
  assert.equal(f.service.current(f.ctx(), ref)!.confirmation, 'incomplete')
  f.db.close()
})
test('stale delivered mutation rejects, immutable terms enforced, failed authority rolls back and purge is idempotent', () => {
  const f = fixture()
  const first = f.admit(f.add('p', 'a'), { kind: 'propose', terms: f.terms('18') })
  const ref = first.item!.ref
  const oldContext = f.ctx(ref)
  f.admit(f.add('b', 'b'), { kind: 'confirm', target: ref }, ref)
  const stale = f.db.transaction(() =>
    f.service.admit(oldContext, f.add('stale', 'a'), { kind: 'cancel', target: ref }),
  )()
  assert.equal(stale.status, 'rejected')
  assert.equal(stale.reason, 'commitment_snapshot_changed')
  const snapshot = f.service.snapshot(ref.id)!
  assert.throws(
    () =>
      f.db.transaction(() =>
        f.service.storage.put(snapshot.head, {
          ...snapshot.revisions[0]!,
          terms: { ...snapshot.revisions[0]!.terms, title: 'mutated' },
        }),
      )(),
    /immutable_terms/,
  )
  f.onWrite(() => {
    assert.deepEqual(f.service.snapshot(ref.id), snapshot)
    throw Error('authority failure')
  })
  assert.throws(
    () => f.db.transaction(() => f.service.purgeSource('s', 'b'))(),
    /authority failure/,
  )
  assert.deepEqual(f.service.snapshot(ref.id), snapshot)
  f.onWrite(undefined)
  f.db.transaction(() => f.service.purgeSource('s', 'b'))()
  const mutation = f.service.snapshot(ref.id)!.head.mutationVersion
  f.db.transaction(() => f.service.purgeSource('s', 'b'))()
  assert.equal(f.service.snapshot(ref.id)!.head.mutationVersion, mutation)
  f.db
    .prepare('UPDATE memory_commitment_heads SET value=? WHERE id=?')
    .run(JSON.stringify({ ...f.service.snapshot(ref.id)!.head, futureField: true }), ref.id)
  assert.throws(() => f.service.storage.head(ref.id))
  f.db.close()
})

test('reported and conditional proposals remain candidates; individual cancellation does not claim everybody cancelled', () => {
  const f = fixture()
  const reported = f.add('reported', 'a')
  f.views.get('reported')!.record.epistemicKind = 'reported'
  f.views.get('reported')!.proofs[0]!.subjectWitness!.kind = 'mentioned'
  const candidate = f.admit(reported, { kind: 'propose', terms: f.terms('19') })
  assert.equal(candidate.item!.confirmation, 'proposed')
  assert.deepEqual(candidate.item!.confirmedParticipantIds, [])
  const conditional = f.admit(f.add('condition', 'a'), {
    kind: 'propose',
    terms: { ...f.terms('19'), conditions: ['Only if both can attend'] },
  })
  assert.deepEqual(conditional.item!.confirmedParticipantIds, [])
  const real = f.admit(f.add('a', 'a'), { kind: 'propose', terms: f.terms('18') })
  const ref = real.item!.ref
  f.admit(f.add('b', 'b'), { kind: 'confirm', target: ref }, ref)
  const cancelled = f.admit(f.add('cancel-a', 'a'), { kind: 'cancel', target: ref }, ref)
  assert.equal(cancelled.item!.lifecycle, 'cancelled')
  assert.deepEqual(cancelled.item!.cancelledParticipantIds, ['a'])
  assert.equal(cancelled.item!.completedParticipantIds.length, 0)
  assert.equal(f.service.list({ ...f.ctx(), sceneId: 'foreign' }).items.length, 0)
  assert.equal(f.service.list({ ...f.ctx(), simulation: true }).items.length, 0)
  f.db.close()
})

test('new entities never merge by title; transaction rollback leaves no proposed plan', () => {
  const f = fixture()
  const one = f.admit(f.add('one', 'a'), { kind: 'propose', terms: f.terms('same') })
  const two = f.admit(f.add('two', 'a'), { kind: 'propose', terms: f.terms('same') })
  assert.notEqual(one.item!.ref.id, two.item!.ref.id)
  const before = f.service.storage.allHeads().length
  assert.throws(() =>
    f.db.transaction(() => {
      f.service.admit(f.ctx(), f.add('rollback', 'a'), { kind: 'propose', terms: f.terms('same') })
      throw Error('rollback')
    })(),
  )
  assert.equal(f.service.storage.allHeads().length, before)
  f.db.close()
})

test('withdrawn terminal declaration becomes needs_review, not a restored definite pending arrangement', () => {
  for (const operation of ['complete', 'cancel'] as const) {
    const f = fixture()
    const initial = f.admit(f.add('p', 'a'), {
      kind: 'propose',
      terms: { ...f.terms('18'), participantIds: ['a'] },
    })
    const ref = initial.item!.ref
    const terminal = f.admit(f.add('terminal', 'a'), { kind: operation, target: ref }, ref)
    assert.equal(terminal.item!.lifecycle, operation === 'complete' ? 'done' : 'cancelled')
    f.db.transaction(() => f.service.purgeSource('s', 'terminal'))()
    f.views.delete('terminal')
    const current = f.service.current(f.ctx(), ref)!
    assert.equal(current.lifecycle, 'needs_review')
    assert.equal(current.confirmation, 'incomplete')
    assert.ok(f.service.storage.head(ref.id)!.everTerminal)
    f.db.close()
  }
})

test('privacy-only proof pruning clears history-only premise and preserves independent complete alternatives', () => {
  const f = fixture()
  const backing = f.add('p', 'a')
  const view = f.views.get('p')!
  const independent = structuredClone(view.proofs[0]!)
  independent.proofId = 'other'
  independent.sources[0]!.messageId = 'other'
  independent.subjectWitness!.sourceMessageId = 'other'
  view.proofs[0]!.parents = ['old-parent']
  view.proofs[0]!.parentReferences = [{ id: 'old-parent', version: 1, deletionEpoch: 0 }]
  view.proofs.push(independent)
  const created = f.admit(backing, {
    kind: 'propose',
    terms: { ...f.terms('18'), participantIds: ['a'] },
  })
  const ref = created.item!.ref
  f.db.transaction(() =>
    f.service.pruneUnavailableProof(f.ctx(), (_, proof) => !proof.parents.includes('old-parent')),
  )()
  const snapshot = f.service.snapshot(ref.id)!
  assert.equal(JSON.stringify(snapshot.revisions).includes('old-parent'), false)
  assert.ok(
    snapshot.revisions[0]!.evidence.every((e) => e.proofs.every((p) => p.proofId === 'other')),
  )
  f.db.transaction(() => f.service.pruneUnavailableProof(f.ctx(), () => false))()
  const empty = f.service.snapshot(ref.id)!
  assert.deepEqual(empty.revisions, [])
  assert.equal(empty.head.everActivatedVersion, 1)
  assert.equal(f.service.current(f.ctx(), ref)!.confirmation, 'incomplete')
  assert.equal(f.service.current(f.ctx(), ref)!.terms, undefined)
  f.db.close()
})

test('amended terms inherit proposal privacy dependencies; recall/delete/pruning remove copied private terms but preserve an independent plan', () => {
  for (const operation of ['source', 'assertion', 'privacy'] as const) {
    const f = fixture()
    const privateTitle = 'PRIVATE_ORIGINAL_TERMS_CANARY'
    const initial = f.admit(f.add('original', 'a'), {
      kind: 'propose',
      terms: { ...f.terms('18'), title: privateTitle, participantIds: ['a'] },
    })
    const ref = initial.item!.ref
    const amended = f.admit(
      f.add('amender', 'a'),
      {
        kind: 'amend',
        target: ref,
        terms: { ...f.terms('19'), title: privateTitle, participantIds: ['a'] },
      },
      ref,
    )
    const newRef = amended.item!.ref
    assert.equal(amended.item!.confirmation, 'confirmed')
    assert.equal(f.service.storage.revision(ref.id, 2)!.requiredProposalGroups.length, 1)
    const independent = f.admit(f.add('independent', 'a'), {
      kind: 'propose',
      terms: { ...f.terms('20'), title: 'Independent plan', participantIds: ['a'] },
    }).item!.ref
    f.db.transaction(() => {
      if (operation === 'source') f.service.purgeSource('s', 'original')
      else if (operation === 'assertion') f.service.purgeAssertion('original')
      else f.service.pruneUnavailableProof(f.ctx(), (e) => e.backing.id !== 'original')
    })()
    f.views.delete('original')
    const persisted = JSON.stringify(
      f.db.prepare('SELECT value FROM memory_commitment_revisions').all(),
    )
    assert.equal(persisted.includes(privateTitle), false)
    assert.equal(persisted.includes('original'), false)
    assert.equal(f.service.current(f.ctx(), newRef)!.confirmation, 'incomplete')
    assert.equal(f.service.current(f.ctx(), newRef)!.terms, undefined)
    assert.equal(f.service.storage.head(ref.id)!.everActivatedVersion, 2)
    assert.equal(f.service.current(f.ctx(), independent)!.confirmation, 'confirmed')
    assert.equal(f.service.current(f.ctx(), independent)!.terms!.title, 'Independent plan')
    f.db.close()
  }
})

test('inherited proposal groups are AND but retain independent OR proofs and reject current edited ancestry', () => {
  const f = fixture()
  const backing = f.add('origin', 'a')
  const view = f.views.get('origin')!
  const b = structuredClone(view.proofs[0]!)
  b.proofId = 'survivor'
  b.sources[0]!.messageId = 'survivor'
  b.subjectWitness!.sourceMessageId = 'survivor'
  view.proofs.push(b)
  const first = f.admit(backing, {
    kind: 'propose',
    terms: { ...f.terms('18'), participantIds: ['a'] },
  }).item!.ref
  const next = f.admit(
    f.add('next', 'a'),
    { kind: 'amend', target: first, terms: { ...f.terms('19'), participantIds: ['a'] } },
    first,
  ).item!.ref
  f.db.transaction(() => f.service.purgeSource('s', 'origin'))()
  view.proofs = [b]
  assert.equal(f.service.current(f.ctx(), next)!.confirmation, 'confirmed')
  assert.equal(
    f.service.storage.revision(next.id, next.version)!.requiredProposalGroups[0]![0]!.proofs.length,
    1,
  )
  f.versions.set('origin', { ...backing, version: 2, deletionEpoch: 0 })
  assert.equal(f.service.current(f.ctx(), next)!.terms, undefined)
  // Ordinary semantic edits have no trusted privacy transition and cannot revive inherited terms.
  f.db.transaction(() => f.service.requalify(f.ctx(), []))()
  assert.equal(f.service.current(f.ctx(), next)!.confirmation, 'incomplete')
  f.db.close()
})

test('lifecycle filter applies after authorization and before result limit; terminal plans do not crowd open plans', () => {
  const f = fixture()
  for (let i = 0; i < 14; i++) {
    const proposed = f.admit(f.add(`terminal-p-${i}`, 'a'), {
      kind: 'propose',
      terms: { ...f.terms('18:00'), participantIds: ['a'] },
    })
    const ref = proposed.item!.ref
    f.admit(
      f.add(`terminal-c-${i}`, 'a'),
      { kind: i % 2 ? 'cancel' : 'complete', target: ref },
      ref,
    )
  }
  for (let i = 0; i < 12; i++) {
    f.admit(f.add(`open-${i}`, 'a'), { kind: 'propose', terms: f.terms('19:00') })
  }
  const open = f.service.list(f.ctx(), { lifecycle: 'open' })
  assert.equal(open.items.length, 12)
  assert.equal(open.coverage, 'complete')
  assert.ok(open.items.every((item) => item.lifecycle === 'pending'))
  const terminal = f.service.list(f.ctx(), { lifecycle: 'terminal' })
  assert.equal(terminal.items.length, 12)
  assert.equal(terminal.coverage, 'limited')
  assert.ok(terminal.items.every((item) => ['done', 'cancelled'].includes(item.lifecycle)))
  assert.deepEqual(f.service.list(f.ctx()), f.service.list(f.ctx(), { lifecycle: 'all' }))
  // Revoked terminal declarations do not become definite pending; needs_review is open.
  f.db.transaction(() => f.service.purgeSource('s', 'terminal-c-0'))()
  const review = f.service.list(f.ctx(), { lifecycle: 'open' })
  assert.equal(review.coverage, 'limited')
  assert.equal(
    f.service.current(f.ctx(), { id: 'c1', version: 1, deletionEpoch: 0 })!.lifecycle,
    'needs_review',
  )
  assert.throws(
    () => f.service.list(f.ctx(), { lifecycle: 'bogus' as 'all' }),
    /commitment_read_lifecycle/,
  )
  f.db.close()
})
