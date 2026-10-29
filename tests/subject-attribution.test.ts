import test from 'node:test'
import assert from 'node:assert/strict'
import { resolveSubjectAttribution } from '../src/memory/write/subject-attribution.ts'
import { MemoryService } from '../src/memory.ts'
import { Store } from '../src/store.ts'
import type { MemoryCandidate, MemoryContext } from '../src/memory/domain/contracts.ts'
const source = { sceneId: 's', messageId: 'm', senderId: 'speaker' }
const ctx = (): MemoryContext => ({
  sceneId: 's',
  simulation: false,
  provenance: 'source-only',
  botUserId: 'bot',
  sourceEvidence: [
    {
      ...source,
      available: true,
      isBot: false,
      mentions: [{ partIndex: 2, userId: 'subject' }],
      sentAt: '2026-10-01T00:00:00Z',
      receivedAt: '2026-10-02T00:00:00Z',
    },
  ],
})
const candidate = (): MemoryCandidate => ({
  content: 'Synthetic preference',
  kind: 'fact',
  sources: [source],
})
const setup = () => {
  const store = new Store(':memory:')
  store.saveScene({
    id: 's',
    kind: 'group',
    adapterId: 'fixture',
    accountId: 'a',
    platformSceneId: 's',
    name: 'Synthetic',
    participationEnabled: true,
    learningEnabled: true,
    proactiveEnabled: false,
  })
  return { store, memory: new MemoryService(store) }
}
test('self derives source speaker; mentioned derives witnessed distinct person and reported claim', () => {
  const self = resolveSubjectAttribution(ctx(), {
    ...candidate(),
    subject: { kind: 'self', sourceMessageId: 'm' },
  })
  assert.equal(self.ok && self.personId, 'speaker')
  const third = resolveSubjectAttribution(ctx(), {
    ...candidate(),
    subject: { kind: 'mentioned', personId: 'subject', sourceMessageId: 'm', mentionPartIndex: 2 },
    epistemicKind: 'explicit',
  })
  assert.equal(third.ok && third.personId, 'subject')
  assert.equal(third.ok && third.kind, 'claim')
  assert.equal(third.ok && third.epistemicKind, 'reported')
  assert.equal(third.ok && third.sourceObservations[0]?.senderId, 'speaker')
})
test('same name cannot replace canonical ID; unseen/all/bot/outside-source and legacy contextual UUID reject', () => {
  for (const personId of ['other', 'all', 'bot'])
    assert.equal(
      resolveSubjectAttribution(ctx(), {
        ...candidate(),
        subject: { kind: 'mentioned', personId, sourceMessageId: 'm', mentionPartIndex: 2 },
      }).ok,
      false,
    )
  assert.equal(
    resolveSubjectAttribution(ctx(), {
      ...candidate(),
      subject: { kind: 'self', sourceMessageId: 'uncited' },
    }).ok,
    false,
  )
  assert.equal(
    resolveSubjectAttribution(
      { ...ctx(), provenance: 'contextual' },
      { ...candidate(), personId: 'subject' },
    ).ok,
    false,
  )
  assert.equal(
    resolveSubjectAttribution(ctx(), {
      ...candidate(),
      personId: 'speaker',
      subject: { kind: 'unresolved' },
    }).ok,
    false,
  )
  assert.equal(
    resolveSubjectAttribution(ctx(), { ...candidate(), subject: { kind: 'unresolved' } }).ok,
    true,
  )
  const foreign = ctx()
  foreign.sourceEvidence![0]!.sceneId = 'foreign'
  assert.equal(resolveSubjectAttribution(foreign, candidate()).ok, false)
})
test('unknown transport timestamps stay absent and do not infer occurredAt or subject', () => {
  const context = ctx()
  context.sourceEvidence![0]!.receivedAt = '2026-10-02'
  context.sourceEvidence![0]!.sentAt = undefined
  const result = resolveSubjectAttribution(context, candidate())
  assert.equal(result.ok, true)
  if (result.ok) {
    assert.equal(result.personId, undefined)
    assert.equal(result.sourceObservations[0]?.receivedAt, undefined)
    assert.equal(result.sourceObservations[0]?.sentAt, undefined)
  }
})
test('real authority persists mentioned witness, exposes trusted observation and removes recalled alternative clock', () => {
  const { store, memory } = setup()
  try {
    const input = {
      ...candidate(),
      subject: {
        kind: 'mentioned' as const,
        personId: 'subject',
        sourceMessageId: 'm',
        mentionPartIndex: 2,
      },
    }
    const first = memory.propose(ctx(), input)
    assert.equal(first.status, 'saved')
    if (first.status !== 'saved') return
    assert.equal(first.memory.scope.kind === 'scene' && first.memory.scope.personId, 'subject')
    assert.equal(first.memory.kind, 'claim')
    assert.equal(first.memory.epistemicKind, 'reported')
    assert.equal(first.memory.observedAt, '2026-10-02T00:00:00Z')
    assert.equal(first.memory.occurredAt, undefined)
    assert.equal(
      memory.subjectEvidence(ctx(), first.memory.id)?.witnesses[0]?.sources[0]?.senderId,
      'speaker',
    )
    const secondContext = ctx()
    secondContext.sourceEvidence![0] = {
      ...secondContext.sourceEvidence![0]!,
      messageId: 'm2',
      receivedAt: '2026-10-03T00:00:00Z',
    }
    const second = memory.propose(secondContext, {
      ...input,
      sources: [{ ...source, messageId: 'm2' }],
      subject: { ...input.subject, sourceMessageId: 'm2' },
    })
    assert.equal(second.status, 'saved')
    assert.equal(memory.subjectEvidence(ctx(), first.memory.id)?.witnesses.length, 2)
    memory.recall('s', 'm')
    const after = memory.list(ctx(), 'subject')[0]!
    assert.ok(after)
    assert.equal(after.observedAt, '2026-10-03T00:00:00Z')
    const witnesses = memory.subjectEvidence(ctx(), first.memory.id)!.witnesses
    assert.equal(witnesses.length, 1)
    assert.equal(witnesses[0]!.sourceMessageId, 'm2')
    assert.equal(
      memory.subjectEvidence({ ...ctx(), sceneId: 'foreign' }, first.memory.id),
      undefined,
    )
    assert.equal(memory.subjectEvidence({ ...ctx(), simulation: true }, first.memory.id), undefined)
  } finally {
    store.close()
  }
})
test('third-party later report cannot supersede or merge into self proof; learning/simulation gate remains', () => {
  const { store, memory } = setup()
  try {
    const selfContext = ctx()
    selfContext.sourceEvidence![0]!.senderId = 'subject'
    const self = memory.propose(selfContext, {
      ...candidate(),
      sources: [{ ...source, senderId: 'subject' }],
      subject: { kind: 'self', sourceMessageId: 'm' },
    })
    assert.equal(self.status, 'saved')
    if (self.status !== 'saved') return
    const report = {
      ...candidate(),
      subject: {
        kind: 'mentioned' as const,
        personId: 'subject',
        sourceMessageId: 'm',
        mentionPartIndex: 2,
      },
    }
    assert.equal(
      memory.propose(ctx(), {
        ...report,
        content: 'Different report',
        supersedesId: self.memory.id,
      }).status,
      'rejected',
    )
    assert.equal(memory.propose(ctx(), report).status, 'rejected')
    assert.equal(memory.list(ctx(), 'subject')[0]?.id, self.memory.id)
    assert.equal(memory.propose({ ...ctx(), simulation: true }, report).status, 'rejected')
  } finally {
    store.close()
  }
})

test('explicit subject dates reject ambiguous and impossible instants but legacy unknown stays absent', () => {
  for (const occurredAt of ['', '2026-10-01', '2026-02-30T00:00:00Z'])
    assert.equal(
      resolveSubjectAttribution(ctx(), {
        ...candidate(),
        subject: { kind: 'self', sourceMessageId: 'm' },
        occurredAt,
      }).ok,
      false,
    )
  assert.equal(
    resolveSubjectAttribution(ctx(), {
      ...candidate(),
      subject: { kind: 'self', sourceMessageId: 'm' },
      occurredAt: '2026-10-01T12:30:00+08:00',
    }).ok,
    true,
  )
})
test('authority rejects legacy arbitrary person in source-only and contextual paths', () => {
  const { store, memory } = setup()
  try {
    for (const provenance of ['source-only', 'contextual'] as const)
      assert.equal(
        memory.propose(
          { ...ctx(), provenance, dependencyMemoryIds: [], dependencyParentReferences: [] },
          { ...candidate(), personId: 'not-the-speaker' },
        ).status,
        'rejected',
      )
  } finally {
    store.close()
  }
})
test('repeat same proof cannot replace trusted observation clock or add witnesses', () => {
  const { store, memory } = setup()
  try {
    const input = { ...candidate(), subject: { kind: 'self' as const, sourceMessageId: 'm' } }
    const first = memory.propose(ctx(), input)
    assert.equal(first.status, 'saved')
    if (first.status !== 'saved') return
    const changed = ctx()
    changed.sourceEvidence![0]!.receivedAt = '2020-01-01T00:00:00Z'
    assert.equal(memory.propose(changed, input).status, 'saved')
    assert.equal(memory.subjectEvidence(ctx(), first.memory.id)?.observedAt, '2026-10-02T00:00:00Z')
    assert.equal(memory.subjectEvidence(ctx(), first.memory.id)?.witnesses.length, 1)
    const history = memory.historyFor(ctx(), first.memory.id)!
    assert.equal(history.revisions[0]!.record.observedAt, '2026-10-02T00:00:00Z')
  } finally {
    store.close()
  }
})

test('mentioned correction cannot supersede pre-attribution legacy proof without witness', () => {
  const { store, memory } = setup()
  try {
    const context = ctx()
    context.sourceEvidence![0]!.senderId = 'subject'
    const first = memory.propose(context, {
      ...candidate(),
      sources: [{ ...source, senderId: 'subject' }],
      subject: { kind: 'self', sourceMessageId: 'm' },
      epistemicKind: 'explicit',
    })
    assert.equal(first.status, 'saved')
    if (first.status !== 'saved') return
    const row = store.db
      .prepare('SELECT value FROM memory_supports WHERE memory_id=?')
      .get(first.memory.id) as { value: string }
    const legacy = JSON.parse(row.value).map(
      ({ subjectWitness: _w, sourceObservations: _o, ...proof }: Record<string, unknown>) => proof,
    )
    store.db
      .prepare('UPDATE memory_supports SET value=? WHERE memory_id=?')
      .run(JSON.stringify(legacy), first.memory.id)
    const report = {
      ...candidate(),
      content: 'Different later reported preference',
      subject: {
        kind: 'mentioned' as const,
        personId: 'subject',
        sourceMessageId: 'm',
        mentionPartIndex: 2,
      },
      supersedesId: first.memory.id,
    }
    assert.equal(memory.propose(ctx(), report).status, 'rejected')
    assert.equal(memory.adminList().find((item) => item.id === first.memory.id)?.status, 'active')
    assert.equal(memory.propose(ctx(), { ...report, supersedesId: undefined }).status, 'saved')
  } finally {
    store.close()
  }
})

test('unresolved subject is a scene claim and batch evidence performs one authorized list', () => {
  const resolved = resolveSubjectAttribution(ctx(), {
    ...candidate(),
    subject: { kind: 'unresolved' },
    epistemicKind: 'explicit',
  })
  assert.equal(resolved.ok && resolved.kind, 'claim')
  assert.equal(resolved.ok && resolved.personId, undefined)
  assert.equal(resolved.ok && resolved.epistemicKind, 'explicit')
  const { store, memory } = setup()
  try {
    const saved = memory.propose(ctx(), {
      ...candidate(),
      subject: { kind: 'self', sourceMessageId: 'm' },
    })
    assert.equal(saved.status, 'saved')
    if (saved.status !== 'saved') return
    const original = memory.list.bind(memory)
    let calls = 0
    memory.list = (...args) => {
      calls++
      return original(...args)
    }
    const batch = memory.subjectEvidenceBatch(ctx(), [saved.memory.id, 'missing', saved.memory.id])
    assert.equal(calls, 1)
    assert.equal(batch.items.size, 1)
    assert.equal(batch.coverage, 'complete')
    assert.equal(
      memory.subjectEvidenceBatch(
        ctx(),
        Array.from({ length: 201 }, (_, i) => 'missing' + i),
      ).coverage,
      'limited',
    )
    assert.equal(
      memory.subjectEvidenceBatch({ ...ctx(), simulation: true }, [saved.memory.id]).items.size,
      0,
    )
  } finally {
    store.close()
  }
})

test('legacy unqualified protocol does not silently repair invalid fact epistemics', () => {
  const { store, memory } = setup()
  try {
    for (const epistemicKind of ['reported', 'inferred', 'hypothesis'] as const) {
      const value = { ...candidate(), epistemicKind }
      const resolved = resolveSubjectAttribution(ctx(), value)
      assert.equal(resolved.ok && resolved.kind, 'fact')
      assert.throws(() => memory.propose(ctx(), value), /invalid_memory/)
    }
  } finally {
    store.close()
  }
})
