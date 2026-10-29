import test from 'node:test'
import assert from 'node:assert/strict'
import { z } from '@hono/zod-openapi'
import { buildLearningCandidateSchema, CandidateSchema } from '../src/memory/learning/contracts.ts'
import {
  repairLearningCandidate,
  relationExtractionInstructions,
} from '../src/memory/learning/subject-input.ts'
const relation = {
  predicate: 'participates_in' as const,
  personId: 'person',
  target: { kind: 'new' as const, entityKind: 'event' as const, title: 'Synthetic event' },
  polarity: 'negative' as const,
  modality: 'asserted' as const,
  temporalScope: 'future' as const,
  conditions: ['Only if explicitly confirmed'],
  occurredAt: '2026-10-28T12:00:00Z',
}
const base = {
  content: 'Will not participate unless confirmed.',
  kind: 'claim' as const,
  subject: { kind: 'self' as const, sourceMessageId: 'source' },
  sourceMessageIds: ['source'],
  epistemicKind: 'explicit' as const,
}
test('OFF tool schema omits relations and strict parser refuses undeclared graph proposals', () => {
  const off = buildLearningCandidateSchema(false)
  assert.equal(off.safeParse(base).success, true)
  assert.equal(off.safeParse({ ...base, relations: [relation] }).success, false)
  const json = z.toJSONSchema(off)
  assert.equal(Object.hasOwn(json.properties ?? {}, 'relations'), false)
  assert.match(relationExtractionInstructions(false), /disabled/)
})
test('ON carries bounded complete relation fields, without supporting extra predicates or hidden authority', () => {
  const on = buildLearningCandidateSchema(true)
  assert.deepEqual(on.parse({ ...base, relations: [relation] }).relations, [relation])
  assert.equal(on.safeParse({ ...base, relations: Array(4).fill(relation) }).success, false)
  for (const patch of [
    { predicate: 'agrees_to' },
    { personId: '' },
    { observedAt: '2026-10-28T12:00:00Z' },
    { status: 'accepted' },
    { conditions: [''] },
    { occurredAt: '2026-10-28' },
    { target: { kind: 'existing', entityId: 'existing', version: 2 } },
  ])
    assert.equal(on.safeParse({ ...base, relations: [{ ...relation, ...patch }] }).success, false)
  assert.equal(
    on.safeParse({
      ...base,
      relations: [{ ...relation, target: { kind: 'existing', entityId: 'exact-exposed-ref' } }],
    }).success,
    true,
  )
  assert.match(
    relationExtractionInstructions(true),
    /source-only extraction must not use existing targets/,
  )
  assert.match(relationExtractionInstructions(true), /Preserve negative polarity, all conditions/)
})
test('new conditional schema retains prior subject time validation while legacy remains compatible', () => {
  for (const enabled of [false, true]) {
    assert.equal(
      buildLearningCandidateSchema(enabled).safeParse({ ...base, occurredAt: '2026-10-28' })
        .success,
      false,
    )
    assert.equal(
      buildLearningCandidateSchema(enabled).safeParse({
        ...base,
        subject: undefined,
        occurredAt: '2026-10-28',
      }).success,
      true,
    )
  }
})
test('any changed content repair discards old relations, unchanged content preserves every endpoint and condition', () => {
  const original = CandidateSchema.parse({ ...base, relations: [relation] })
  const unchanged = repairLearningCandidate(original, original.content)
  assert.equal(unchanged.relationsDiscarded, false)
  assert.deepEqual(unchanged.candidate.relations, original.relations)
  const changed = repairLearningCandidate(original, 'Different neutral source wording.')
  assert.equal(changed.relationsDiscarded, true)
  assert.equal(changed.candidate.relations, undefined)
  assert.deepEqual(changed.candidate.subject, original.subject)
  assert.deepEqual(changed.candidate.sourceMessageIds, original.sourceMessageIds)
  assert.deepEqual(original.relations, [relation])
})

test('contextual entity exposure is immutable, keyed and revalidated before queued commit', async () => {
  const { ContextualProposals } = await import('../src/memory/learning/contextual-proposals.ts')
  type Store = import('../src/store.ts').Store
  type TurnContext = import('../src/runtime.ts').TurnContext
  type RuntimeMessage = import('../src/runtime.ts').RuntimeMessage
  type MemoryRecord = import('../src/memory/domain/contracts.ts').MemoryRecord
  let enabled = true,
    revision = 1,
    deleted = false,
    visible = true
  const store = {
    settings: {
      memoryGraphAnswerEnabled: true,
      get memoryGraphEnabled() {
        return enabled
      },
    },
    scene: () => ({
      id: 'scene',
      adapterId: 'fixture',
      accountId: 'account',
      learningEnabled: true,
    }),
    sourceEvents: { registered: () => undefined, hasEdits: () => false },
    recalled: () => false,
    log: () => {},
  } as unknown as Store
  const source = {
    id: 'source',
    sceneId: 'scene',
    senderId: 'person',
    status: 'available',
    origin: 'live',
    sentAt: '2026-10-28T00:00:00Z',
    receivedAt: '2026-10-28T00:00:01Z',
    parts: [{ type: 'text', text: 'Synthetic source' }],
  } as RuntimeMessage
  const context = {
    mode: 'real',
    scene: { id: 'scene' },
    signal: new AbortController().signal,
  } as TurnContext
  const calls: unknown[] = []
  const proposals = new ContextualProposals(store, {
    memory: {
      list: () => (visible ? [{ id: 'assertion' } as MemoryRecord] : []),
      revisionState: () => ({ version: revision, deletionEpoch: 0, deleted }),
    },
    preview: () => [source],
    foregroundBusy: () => false,
    async commit(_scene, _candidate, _sources, _parents, signal, entities) {
      assert.equal(signal.aborted, false)
      calls.push(structuredClone(entities))
      return { status: 'saved' }
    },
  })
  try {
    const exposed = [
      { entityId: 'entity', backingRef: { id: 'assertion', version: 1, deletionEpoch: 0 } },
    ]
    assert.equal(
      proposals.submit(context, { ...base, relations: [relation] }, [], exposed, () => true).status,
      'queued',
    )
    exposed[0]!.entityId = 'mutated-outside'
    exposed[0]!.backingRef.version = 99
    await proposals.queue.whenIdle()
    assert.deepEqual(calls, [
      [{ entityId: 'entity', backingRef: { id: 'assertion', version: 1, deletionEpoch: 0 } }],
    ])
    // Change exact entity exposure: same assertion candidate is not falsely deduplicated.
    assert.equal(
      proposals.submit(
        context,
        { ...base, relations: [relation] },
        [],
        [{ entityId: 'other', backingRef: { id: 'assertion', version: 1, deletionEpoch: 0 } }],
        () => true,
      ).status,
      'queued',
    )
    revision = 2
    await proposals.queue.whenIdle()
    assert.equal(calls.length, 1)
    revision = 1
    assert.equal(
      proposals.submit(
        context,
        { ...base, content: 'Another qualified relation', relations: [relation] },
        [],
        [{ entityId: 'third', backingRef: { id: 'assertion', version: 1, deletionEpoch: 0 } }],
        () => true,
      ).status,
      'queued',
    )
    enabled = false
    await proposals.queue.whenIdle()
    assert.equal(calls.length, 1)
    assert.throws(() => proposals.submit(context, { ...base, relations: [relation] }, []))
    enabled = true
    visible = false
    deleted = true
    assert.equal(
      proposals.submit(
        context,
        { ...base, content: 'Must not use deleted entity', relations: [relation] },
        [],
        [{ entityId: 'deleted', backingRef: { id: 'assertion', version: 1, deletionEpoch: 0 } }],
        () => true,
      ).status,
      'rejected',
    )
    visible = true
    deleted = false
    const independent = [
      { entityId: 'reviewed', backingRef: { id: 'assertion', version: 1, deletionEpoch: 0 } },
    ]
    assert.deepEqual(proposals.submit(context, base, [], independent), {
      status: 'rejected',
      reason: 'entity_authority_unavailable',
    })
    for (const guard of [
      () => false,
      () => {
        throw new Error('revoked')
      },
    ]) {
      assert.equal(
        proposals.submit(context, { ...base, content: 'No authority downgrade' }, [], [], guard)
          .status,
        'rejected',
      )
      await proposals.queue.whenIdle()
      assert.equal(calls.length, 1)
    }
    let reviewAccepted = true
    assert.equal(
      proposals.submit(
        context,
        { ...base, content: 'Review may be withdrawn' },
        [],
        independent,
        () => reviewAccepted,
      ).status,
      'queued',
    )
    reviewAccepted = false
    await proposals.queue.whenIdle()
    assert.equal(calls.length, 1)
    const controller = new AbortController()
    const independentContext = { ...context, signal: controller.signal }
    assert.equal(
      proposals.submit(
        independentContext,
        { ...base, content: 'Independent trusted exposure remains valid' },
        [],
        independent,
        () => true,
      ).status,
      'queued',
    )
    controller.abort('turn_finished')
    await proposals.queue.whenIdle()
    assert.equal(calls.length, 2)
  } finally {
    proposals.queue.dispose()
  }
})

test('past participation with an unknown date and possible participation retain explicit qualifiers', () => {
  const { occurredAt: _unused, ...unknownTime } = relation
  const past = CandidateSchema.parse({
    ...base,
    relations: [{ ...unknownTime, temporalScope: 'past' }],
  }).relations![0]!
  assert.equal(past.temporalScope, 'past')
  assert.equal(past.occurredAt, undefined)
  assert.equal(past.validFrom, undefined)
  const possible = CandidateSchema.parse({
    ...base,
    relations: [{ ...unknownTime, modality: 'possible', temporalScope: 'unspecified' }],
  }).relations![0]!
  assert.equal(possible.modality, 'possible')
  assert.equal(possible.temporalScope, 'unspecified')
  for (const key of ['modality', 'temporalScope'] as const) {
    const missing = { ...unknownTime } as Record<string, unknown>
    delete missing[key]
    assert.equal(CandidateSchema.safeParse({ ...base, relations: [missing] }).success, false)
  }
  assert.match(
    relationExtractionInstructions(true),
    /Unknown temporal applicability is unspecified, not current/,
  )
  assert.match(relationExtractionInstructions(true), /even accepted does not mean verified truth/)
})
