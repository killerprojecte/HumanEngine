import assert from 'node:assert/strict'
import test from 'node:test'
import Database from 'better-sqlite3'
import { CommitmentService } from '../src/memory/commitments/service.ts'
import type {
  CommitmentAssertion,
  CommitmentContext,
  CommitmentRef,
  CommitmentTerms,
  CommitmentMutation,
} from '../src/memory/domain/commitments.ts'
function fixture() {
  const db = new Database(':memory:')
  let ids = 0
  const mutations: CommitmentMutation[] = []
  let clock = Date.parse('2026-10-29T00:00:00Z')
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
      beforeWrite: (mutation) => {
        mutations.push(structuredClone(mutation))
        onWrite?.()
      },
      historical: (_, r) => views.get(r.id),
      people: () => ['a', 'b'],
    },
    { id: () => `c${++ids}`, now: () => new Date(clock).toISOString() },
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
  const anchor = (ref: CommitmentRef) => ({
    ...ref,
    mutationVersion: service.storage.head(ref.id)!.mutationVersion,
  })
  const history = (ref: CommitmentRef, asOfRecorded?: string) =>
    service.temporalHistory(ctx(ref), anchor(ref), { asOfRecorded })!
  return {
    anchor,
    history,
    mutations,
    tick: () => {
      clock += 1000
      return new Date(clock).toISOString()
    },
    time: () => new Date(clock).toISOString(),
    clock: (value: number) => (clock = value),
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
test('actual same-terms confirmation transitions record past declaration state, never today evidence applied retrospectively', () => {
  const f = fixture()
  try {
    const ref = f.admit(f.add('p18', 'a'), { kind: 'propose', terms: f.terms('18:00') }).item!.ref,
      t1 = f.time()
    const t2 = f.tick()
    f.admit(f.add('b18', 'b'), { kind: 'confirm', target: ref }, ref)
    const t3 = f.tick()
    const next = f.admit(
      f.add('p19', 'a'),
      { kind: 'amend', target: ref, terms: f.terms('19:00') },
      ref,
    ).item!.ref
    const t4 = f.tick()
    f.admit(f.add('b19', 'b'), { kind: 'confirm', target: next }, next)
    assert.equal(f.history(next, t1).recordedStates[0]!.states[0]!.confirmation, 'proposed')
    assert.deepEqual(f.history(next, t1).recordedStates[0]!.states[0]!.confirmedParticipantIds, [
      'a',
    ])
    assert.equal(f.history(next, t2).recordedStates[0]!.states[0]!.confirmation, 'confirmed')
    const pending = f.history(next, t3).recordedStates[0]!
    assert.equal(pending.activeVersion, 1)
    assert.equal(pending.states[0]!.terms!.timeQualification, '18:00')
    assert.equal(pending.states[1]!.confirmation, 'proposed')
    assert.equal(
      f.history(next, t4).recordedStates[0]!.states[0]!.terms!.timeQualification,
      '19:00',
    )
    assert.equal(f.history(next, t4).recordedStates[0]!.states[0]!.terms!.occurredAt, undefined)
    assert.equal(f.history(next).stateTime, 'server_recorded_not_occurred')
    f.tick()
    f.db.transaction(() => f.service.purgeSource('s', 'b19'))()
    const old = f.history(next, t4)
    assert.equal(old.recordedStates.length, 1)
    assert.equal(old.recordedStates[0]!.status, 'redacted')
    assert.deepEqual(old.recordedStates[0]!.states, [])
    assert.equal(f.service.current(f.ctx(next), next)!.confirmation, 'incomplete')
    assert.equal(JSON.stringify(f.service.temporal.read(next.id)).includes('b19'), false)
  } finally {
    f.db.close()
  }
})
test('legacy history is unknown and only a real later mutation starts recording; exact exposure blocks future mutation and newer terms', () => {
  const f = fixture()
  try {
    const ref = f.admit(f.add('a', 'a'), { kind: 'propose', terms: f.terms('18') }).item!.ref
    f.db.exec(
      'DROP TABLE memory_commitment_transitions; DROP TABLE memory_commitment_temporal_windows',
    )
    assert.deepEqual(f.history(ref).reasons, ['legacy_history_unknown'])
    const oldAnchor = f.anchor(ref),
      oldctx = f.ctx(ref)
    f.tick()
    f.admit(f.add('b', 'b'), { kind: 'confirm', target: ref }, ref)
    assert.equal(f.service.temporalHistory(oldctx, oldAnchor), undefined)
    assert.ok(f.history(ref).reasons.includes('legacy_history_unknown'))
    assert.equal(f.history(ref).window.firstMutationVersion, 2)
    f.tick()
    f.admit(f.add('a19', 'a'), { kind: 'amend', target: ref, terms: f.terms('19') }, ref)
    const bound = f.history(ref)
    assert.ok(bound.reasons.includes('anchor_version_bound'))
    assert.equal(bound.recordedStates.at(-1)!.status, 'redacted')
    assert.equal(JSON.stringify(bound).includes('"timeQualification":"19"'), false)
    assert.equal(
      f.service.temporalHistory({ ...f.ctx(ref), sceneId: 'other' }, f.anchor(ref)),
      undefined,
    )
  } finally {
    f.db.close()
  }
})
test('historical indirect privacy pruning physically redacts declaration status while an independent plan survives', () => {
  const f = fixture()
  try {
    const a = f.add('secret', 'a')
    f.views.get(a.id)!.proofs[0]!.parents = ['ancestor']
    f.views.get(a.id)!.proofs[0]!.parentReferences = [
      { id: 'ancestor', version: 1, deletionEpoch: 0 },
    ]
    const ref = f.admit(a, { kind: 'propose', terms: f.terms('PRIVATE_TEMPORAL_CANARY') }).item!.ref
    f.tick()
    f.admit(f.add('b', 'b'), { kind: 'confirm', target: ref }, ref)
    const independent = f.admit(f.add('public', 'a'), {
      kind: 'propose',
      terms: f.terms('independent'),
    }).item!.ref
    f.tick()
    f.db.transaction(() =>
      f.service.pruneUnavailableProof(f.ctx(), (_e, p) => !p.parents.includes('ancestor')),
    )()
    const value = JSON.stringify(f.service.temporal.read(ref.id))
    assert.equal(value.includes('ancestor'), false)
    assert.equal(value.includes('secret'), false)
    assert.ok(f.service.temporal.read(independent.id)!.transitions[0]!.states?.length)
    assert.equal(f.service.storage.revisions(ref.id).length, 0)
    f.db.transaction(() => f.service.erase(f.ctx(independent), independent))()
    assert.equal(f.service.temporal.read(independent.id), undefined)
    assert.equal(f.service.temporal.allIds().includes(independent.id), false)
  } finally {
    f.db.close()
  }
})
test('OR source pruning preserves captured state with original proof survivor and never recomputes its past confirmation', () => {
  const f = fixture()
  try {
    const a = f.add('a', 'a'),
      view = f.views.get('a')!,
      alternative = structuredClone(view.proofs[0]!)
    alternative.proofId = 'alternative'
    alternative.sources[0]!.messageId = 'alternate-source'
    alternative.subjectWitness!.sourceMessageId = 'alternate-source'
    view.proofs.push(alternative)
    const ref = f.admit(a, { kind: 'propose', terms: f.terms('18') }).item!.ref
    f.tick()
    f.admit(f.add('b', 'b'), { kind: 'confirm', target: ref }, ref)
    const confirmedAt = f.time()
    f.tick()
    view.proofs = [alternative]
    f.db.transaction(() => f.service.purgeSource('s', 'a'))()
    assert.equal(
      f.history(ref, confirmedAt).recordedStates[0]!.states[0]!.confirmation,
      'confirmed',
    )
    assert.equal(
      f.service.temporal.read(ref.id)!.transitions[1]!.states![0]!.evidence[0]!.proofs.length,
      1,
    )
  } finally {
    f.db.close()
  }
})
test('bounded retained window and server clock regression are explicit rather than false as-of answers', () => {
  const f = fixture()
  try {
    const ref = f.admit(f.add('a', 'a'), { kind: 'propose', terms: f.terms('18') }).item!.ref
    let timeline = f.service.temporal.read(ref.id)!,
      base = timeline.transitions[0]!
    for (let n = 2; n <= 66; n++)
      timeline = f.service.temporal.append(timeline, {
        ...base,
        mutationVersion: n,
        head: { ...base.head, mutationVersion: n },
        states: base.states!.map((s) => ({ ...s, mutationVersion: n })),
        recordedAt: new Date(Date.parse(base.recordedAt) + n * 1000).toISOString(),
      })
    f.db.transaction(() => f.service.temporal.replace(ref.id, timeline))()
    assert.equal(f.service.temporal.read(ref.id)!.transitions.length, 64)
    assert.equal(f.service.temporal.read(ref.id)!.window.prunedThroughMutationVersion, 2)
    // Independent actual command sequence for the regressing server clock case.
    const g = fixture()
    try {
      const r = g.admit(g.add('a', 'a'), { kind: 'propose', terms: g.terms('18') }).item!.ref
      g.clock(Date.parse('2026-10-28T23:00:00Z'))
      g.admit(g.add('b', 'b'), { kind: 'confirm', target: r }, r)
      const result = g.history(r, '2026-10-30T00:00:00Z')
      assert.deepEqual(result.recordedStates, [])
      assert.ok(result.reasons.includes('recorded_clock_nonmonotonic'))
    } finally {
      g.db.close()
    }
  } finally {
    f.db.close()
  }
})
test('precommit failures roll back both stores; malformed temporal rows fail closed and no simulation row is written', () => {
  const f = fixture()
  try {
    f.onWrite(() => {
      throw Error('test_authority_failure')
    })
    assert.throws(
      () => f.admit(f.add('a', 'a'), { kind: 'propose', terms: f.terms('18') }),
      /test_authority_failure/,
    )
    assert.equal(f.service.storage.allHeads().length, 0)
    assert.deepEqual(f.service.temporal.allIds(), [])
    f.onWrite(undefined)
    const ref = f.admit(f.add('ok', 'a'), { kind: 'propose', terms: f.terms('18') }).item!.ref
    const before = f.service.snapshot(ref.id)!.qualificationHash
    const no = f.db.transaction(() =>
      f.service.admit({ ...f.ctx(), simulation: true }, f.add('sim', 'a'), {
        kind: 'propose',
        terms: f.terms('new'),
      }),
    )()
    assert.equal(no.status, 'rejected')
    assert.equal(f.service.snapshot(ref.id)!.qualificationHash, before)
    f.db
      .prepare('UPDATE memory_commitment_transitions SET value=CAST(value AS BLOB) WHERE id=?')
      .run(ref.id)
    assert.throws(() => f.history(ref), /temporal_corrupt/)
  } finally {
    f.db.close()
  }
})
test('privacy transition records only the original journalable metadata marker, never a newly inferred confirmation', () => {
  const f = fixture()
  try {
    const ref = f.admit(f.add('private-source', 'a'), {
      kind: 'propose',
      terms: f.terms('PRIVATE_TERMS_CANARY'),
    }).item!.ref
    const time = f.tick()
    f.db.transaction(() => f.service.purgeSource('s', 'private-source'))()
    const mutation = f.mutations.at(-1)!,
      marker = mutation.privacyMarker!
    assert.equal(marker.recordedAt, time)
    assert.equal(marker.reason, 'purge')
    assert.equal(marker.head.mutationVersion, f.service.storage.head(ref.id)!.mutationVersion)
    assert.equal(JSON.stringify(marker).includes('PRIVATE_TERMS_CANARY'), false)
    assert.equal(JSON.stringify(marker).includes('private-source'), false)
    const row = f.service.temporal.read(ref.id)!.transitions.at(-1)!
    assert.equal(row.states, null)
    assert.equal(row.recordedAt, time)
    assert.equal(f.service.snapshot(ref.id)!.qualificationHash, mutation.qualificationHash)
  } finally {
    f.db.close()
  }
})
