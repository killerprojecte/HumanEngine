import assert from 'node:assert/strict'
import test from 'node:test'
import { buildReplyBrief } from '../src/runtime/reply-brief.ts'
import type { MemoryRecord } from '../src/memory.ts'
import { fixtureReplyPlan } from './helpers/reply-plan.ts'
import { memoryScenarioHarness } from './helpers/memory-scenario-harness.ts'
import { memoryScenarioIds as ids } from './fixtures/memory-scenarios.ts'

const past = '2000-01-02T03:00:00+08:00'
const future = '2100-01-02T03:00:00+08:00'
const hasRecord = (records: MemoryRecord[], id: string) =>
  records.some((record) => record.id === id)

// These are supplied structured assertions, not a measurement of model temporal reasoning.
test('future parent validity excludes dependent and transitive records from current reads without permanently invalidating them', () => {
  const h = memoryScenarioHarness()
  try {
    const parent = h.saved(h.propose('Temporal parent preference.', [h.source('parent')]))
    const child = h.saved(
      h.propose('Temporal child preference.', [h.source('child')], {}, [parent.id]),
    )
    const grandchild = h.saved(
      h.propose('Temporal grandchild preference.', [h.source('grandchild')], {}, [child.id]),
    )
    const control = h.saved(h.propose('Temporal unrelated control.', [h.source('control')]))
    for (const record of [parent, child, grandchild]) {
      assert.ok(hasRecord(h.memory.list(h.context()), record.id))
      assert.ok(hasRecord(h.memory.search(h.context(), 'Temporal'), record.id))
    }
    h.memory.update(parent.id, { validFrom: future })
    for (const record of [parent, child, grandchild]) {
      assert.equal(hasRecord(h.memory.list(h.context()), record.id), false)
      assert.equal(hasRecord(h.memory.search(h.context(), 'Temporal'), record.id), false)
      assert.equal(
        hasRecord(h.memory.personProfile(h.context(), ids.lin).records, record.id),
        false,
      )
      assert.equal(h.memory.adminList().find((item) => item.id === record.id)?.status, 'active')
    }
    assert.ok(hasRecord(h.memory.list(h.context()), control.id))
    h.memory.update(parent.id, { validFrom: past })
    for (const record of [parent, child, grandchild]) {
      assert.ok(hasRecord(h.memory.list(h.context()), record.id))
      assert.ok(hasRecord(h.memory.search(h.context(), 'Temporal'), record.id))
      assert.equal(h.memory.adminList().find((item) => item.id === record.id)?.status, 'active')
    }
  } finally {
    h.close()
  }
})

test('current applicability requires every parent within an AND proof while an independent OR proof remains usable', () => {
  const h = memoryScenarioHarness()
  try {
    const first = h.saved(h.propose('Temporal first parent.', [h.source('first-parent')]))
    const second = h.saved(h.propose('Temporal second parent.', [h.source('second-parent')]))
    const required = h.saved(
      h.propose('Temporal requires both parents.', [h.source('both')], {}, [first.id, second.id]),
    )
    const alternative = h.saved(
      h.propose('Temporal alternative child.', [h.source('dependent')], {}, [first.id, second.id]),
    )
    h.saved(h.propose(alternative.content, [h.source('independent')]))
    h.memory.update(first.id, { validFrom: future })
    assert.equal(hasRecord(h.memory.list(h.context()), required.id), false)
    assert.equal(hasRecord(h.memory.search(h.context(), 'Temporal'), required.id), false)
    assert.ok(hasRecord(h.memory.list(h.context()), alternative.id))
    assert.ok(hasRecord(h.memory.search(h.context(), 'Temporal'), alternative.id))
    h.memory.update(second.id, { validFrom: future })
    assert.ok(hasRecord(h.memory.list(h.context()), alternative.id))
    h.memory.update(first.id, { validFrom: past })
    assert.equal(hasRecord(h.memory.list(h.context()), required.id), false)
    h.memory.update(second.id, { validFrom: past })
    assert.ok(hasRecord(h.memory.list(h.context()), required.id))
    assert.equal(h.memory.adminList().find((record) => record.id === required.id)?.status, 'active')
  } finally {
    h.close()
  }
})

test('future manual records remain administratively visible but are absent from current lexical and vector retrieval', () => {
  const h = memoryScenarioHarness()
  try {
    const record = h.memory.create({
      scope: { kind: 'scene', sceneId: ids.group, personId: ids.lin },
      content: 'FUTURE_MANUAL_PREFERENCE_CANARY',
      kind: 'claim',
      validFrom: future,
      epistemicKind: 'explicit',
    })
    const control = h.memory.create({
      scope: { kind: 'scene', sceneId: ids.group, personId: ids.lin },
      content: 'Current manual control.',
      kind: 'claim',
      validFrom: past,
    })
    const spaceId = h.memory.registerSpace({
      providerId: 'fixture',
      modelId: 'fixture',
      apiModelName: 'fixture',
      deployment: 'offline',
      dimensions: 2,
      preprocessing: 'plain',
      normalized: true,
    })
    h.memory.index(record.id, spaceId, [1, 0])
    h.memory.index(control.id, spaceId, [1, 0])
    assert.equal(h.memory.adminList().find((item) => item.id === record.id)?.validFrom, future)
    assert.equal(hasRecord(h.memory.list(h.context()), record.id), false)
    assert.equal(
      hasRecord(h.memory.search(h.context(), 'FUTURE_MANUAL_PREFERENCE_CANARY'), record.id),
      false,
    )
    const vectorResults = h.memory.search(h.context(), '', ids.lin, {
      spaceId,
      vector: [1, 0],
      limit: 20,
    })
    assert.equal(hasRecord(vectorResults, record.id), false)
    assert.ok(hasRecord(vectorResults, control.id))
    assert.deepEqual(h.memory.people(h.context()), [{ personId: ids.lin, recordCount: 1 }])
    assert.equal(h.memory.adminList().find((item) => item.id === record.id)?.status, 'active')
  } finally {
    h.close()
  }
})

for (const epistemicKind of ['reported', 'inferred', 'hypothesis'] as const)
  test(`${epistemicKind} assertions require claim kind and cannot silently become facts through create, proposal or update`, () => {
    const h = memoryScenarioHarness()
    try {
      const scope = { kind: 'scene' as const, sceneId: ids.group, personId: ids.lin }
      const initial = h.snapshot()
      assert.throws(
        () =>
          h.memory.create({
            scope,
            content: 'Invalid asserted fact.',
            kind: 'fact',
            epistemicKind,
          }),
        /invalid_memory/,
      )
      assert.throws(
        () =>
          h.propose('Invalid proposed fact.', [h.source('invalid')], {
            kind: 'fact',
            epistemicKind,
          }),
        /invalid_memory/,
      )
      assert.equal(h.snapshot(), initial)
      const claim = h.saved(
        h.propose('Qualified claim remains uncertain.', [h.source('qualified')], {
          kind: 'claim',
          epistemicKind,
        }),
      )
      const before = h.snapshot()
      assert.throws(() => h.memory.update(claim.id, { kind: 'fact' }), /invalid_memory/)
      assert.equal(h.snapshot(), before)
      const current = h.memory.list(h.context()).find((record) => record.id === claim.id)!
      assert.equal(current.kind, 'claim')
      assert.equal(current.epistemicKind, epistemicKind)
      assert.equal(
        h.memory.historyFor(h.context(), claim.id)?.revisions.at(-1)?.record.epistemicKind,
        epistemicKind,
      )
    } finally {
      h.close()
    }
  })

test('ReplyBrief retains selected memory validity and epistemic qualifiers without promoting a hypothesis to fact', () => {
  const h = memoryScenarioHarness()
  try {
    const record = h.saved(
      h.propose('May prefer concise replies on weekdays only.', [h.source('qualified')], {
        kind: 'claim',
        epistemicKind: 'hypothesis',
        validFrom: past,
        occurredAt: '1999-12-31T20:00:00+08:00',
        validUntil: future,
      }),
    )
    const plan = fixtureReplyPlan(
      [
        {
          role: 'user',
          content: JSON.stringify({
            planContract: { targetMessageIds: ['question'], contextMessageIds: ['question'] },
          }),
        },
      ],
      {
        memorySelections: [{ evidenceId: 'memory-result', memoryIds: [record.id] }],
      },
    )!
    const brief = buildReplyBrief(
      plan,
      [
        {
          id: 'memory-result',
          toolName: 'memory.search',
          value: h.memory.search(h.context(), 'concise'),
        },
      ],
      [],
    )
    const receipt = brief.evidence[0] as unknown as {
      memories: Array<Partial<MemoryRecord>>
      caution: string
    }
    assert.equal(receipt.memories.length, 1)
    assert.equal(receipt.memories[0]!.content, record.content)
    assert.equal(receipt.memories[0]!.kind, 'claim')
    assert.equal(receipt.memories[0]!.epistemicKind, 'hypothesis')
    assert.equal(receipt.memories[0]!.validFrom, past)
    assert.equal(receipt.memories[0]!.validUntil, future)
    assert.equal(receipt.memories[0]!.occurredAt, record.occurredAt)
    assert.match(receipt.caution, /preserve uncertainty/)
    assert.equal(Object.hasOwn(receipt.memories[0]!, 'confidence'), false)
    assert.equal(Object.hasOwn(receipt.memories[0]!, 'score'), false)
    assert.deepEqual(brief.plan.facts, [])
  } finally {
    h.close()
  }
})

test('history keeps recording, occurrence and validity times distinct and does not invent missing event or validity times', () => {
  const h = memoryScenarioHarness()
  try {
    const occurredAt = '1999-12-31T20:00:00+08:00'
    const known = h.saved(
      h.propose('Known timing.', [h.source('timed')], {
        validFrom: past,
        occurredAt,
        validUntil: future,
        epistemicKind: 'explicit',
      }),
    )
    const revision = h.memory.historyFor(h.context(), known.id)!.revisions.at(-1)!
    assert.equal(revision.record.occurredAt, occurredAt)
    assert.equal(revision.record.validFrom, past)
    assert.equal(revision.validFrom, new Date(past).toISOString())
    assert.equal(revision.validTo, new Date(future).toISOString())
    assert.ok(revision.recordedAt)
    assert.ok(Date.parse(revision.recordedAt) > Date.parse(past))
    assert.notEqual(revision.recordedAt, new Date(occurredAt).toISOString())
    const unknown = h.saved(
      h.propose('Recently; starting time is not specified.', [h.source('unknown')]),
    )
    const unknownRevision = h.memory.historyFor(h.context(), unknown.id)!.revisions.at(-1)!
    assert.equal(unknownRevision.validFrom, null)
    assert.equal(unknownRevision.validTo, null)
    assert.equal(Object.hasOwn(unknownRevision.record, 'validFrom'), false)
    assert.equal(Object.hasOwn(unknownRevision.record, 'occurredAt'), false)
    assert.equal(Object.hasOwn(unknownRevision.record, 'epistemicKind'), false)
    assert.ok(unknownRevision.recordedAt)
  } finally {
    h.close()
  }
})

test('invalid validity intervals and unknown epistemic categories fail atomically rather than entering current retrieval', () => {
  const h = memoryScenarioHarness()
  try {
    const record = h.saved(
      h.propose('Valid existing statement.', [h.source('valid')], {
        validFrom: past,
        validUntil: future,
      }),
    )
    const before = h.snapshot()
    assert.throws(() => h.memory.update(record.id, { validFrom: 'not-a-date' }), /invalid_memory/)
    assert.throws(
      () => h.memory.update(record.id, { validFrom: future, validUntil: past }),
      /invalid_memory/,
    )
    assert.throws(
      () => h.memory.update(record.id, { epistemicKind: 'certain-by-repetition' as never }),
      /invalid_memory/,
    )
    assert.equal(h.snapshot(), before)
    assert.ok(hasRecord(h.memory.list(h.context()), record.id))
  } finally {
    h.close()
  }
})

test('reconciliation metadata and supplied semantic hits cannot bypass a future parent, and recover when it becomes applicable', () => {
  const h = memoryScenarioHarness()
  try {
    const parent = h.saved(h.propose('Reconciliation temporal premise.', [h.source('parent')]))
    const child = h.saved(
      h.propose('Reconciliation temporal child.', [h.source('child')], {}, [parent.id]),
    )
    const source = h.source('current-candidate')
    const ctx = {
      ...h.context(),
      sourceEvidence: [{ ...source, available: true, isBot: false, origin: 'live' as const }],
    }
    const candidates = [
      {
        candidateIndex: 0,
        content: child.content,
        kind: 'claim' as const,
        personId: ids.lin,
        sourceMessageIds: [source.messageId],
      },
    ]
    const targets = (semantic: MemoryRecord[]) =>
      h.memory
        .reconciliationTargets(ctx, candidates, semantic)
        .targets.map((target) => target.memory)
    assert.ok(hasRecord(targets([]), child.id))
    h.memory.update(parent.id, { validFrom: future })
    for (const semantic of [[], [child]]) {
      assert.equal(hasRecord(targets(semantic), child.id), false)
      assert.equal(hasRecord(targets(semantic), parent.id), false)
    }
    assert.equal(h.memory.adminList().find((record) => record.id === child.id)?.status, 'active')
    h.memory.update(parent.id, { validFrom: past })
    for (const semantic of [[], [child]]) assert.ok(hasRecord(targets(semantic), child.id))
  } finally {
    h.close()
  }
})
