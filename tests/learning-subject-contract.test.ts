import test from 'node:test'
import assert from 'node:assert/strict'
import { CandidateSchema } from '../src/memory/learning/contracts.ts'
import {
  resolveLearningSubject,
  subjectLearningMessages,
  subjectExtractionInstructions,
} from '../src/memory/learning/subject-input.ts'
import { trustedSourceEvidence } from '../src/memory/learning/source-evidence.ts'
import { ContextProjection } from '../src/runtime/context-projection.ts'
import type { RuntimeMessage } from '../src/runtime.ts'
const message: RuntimeMessage = {
  id: 'message-1',
  sceneId: 'scene',
  senderId: 'speaker',
  sentAt: '2026-10-28T12:00:00Z',
  receivedAt: '2026-10-28T12:00:01Z',
  origin: 'live',
  status: 'available',
  parts: [
    { type: 'text', text: 'Synthetic third-party report' },
    { type: 'mention', userId: 'subject' },
    { type: 'mention', userId: 'all' },
    { type: 'mention', userId: 'bot' },
  ],
}
const candidate = {
  content: 'A sourced claim',
  kind: 'claim' as const,
  sourceMessageIds: ['message-1'],
}
const ctx = () => ({
  sceneId: 'scene',
  simulation: false,
  botUserId: 'bot',
  sourceEvidence: trustedSourceEvidence([message], () => false),
})
test('subject contract accepts exact shapes, rejects model observation clocks and trusted source forgery', () => {
  for (const subject of [
    { kind: 'self', sourceMessageId: 'message-1' },
    { kind: 'mentioned', personId: 'subject', sourceMessageId: 'message-1', mentionPartIndex: 1 },
    { kind: 'unresolved' },
  ])
    assert.equal(CandidateSchema.safeParse({ ...candidate, subject }).success, true)
  for (const patch of [
    { observedAt: '2026-10-28T12:00:00Z' },
    { receivedAt: '2026-10-28T12:00:00Z' },
    { sourceEvidence: [] },
    {
      subject: {
        kind: 'mentioned',
        personId: 'subject',
        sourceMessageId: 'message-1',
        mentionPartIndex: -1,
      },
    },
    { subject: { kind: 'unresolved', personId: 'speaker' } },
  ])
    assert.equal(CandidateSchema.safeParse({ ...candidate, ...patch }).success, false)
})
test('repair attribution shares authoritative mention/legacy/self/unresolved validation', () => {
  const mentioned = resolveLearningSubject(ctx(), {
    ...candidate,
    kind: 'fact',
    subject: {
      kind: 'mentioned',
      personId: 'subject',
      sourceMessageId: 'message-1',
      mentionPartIndex: 1,
    },
  })
  assert.equal(mentioned.ok, true)
  if (mentioned.ok) {
    assert.equal(mentioned.personId, 'subject')
    assert.equal(mentioned.kind, 'claim')
    assert.equal(mentioned.epistemicKind, 'reported')
    assert.equal(mentioned.sourceObservations[0]!.senderId, 'speaker')
  }
  const self = resolveLearningSubject(ctx(), {
    ...candidate,
    subject: { kind: 'self', sourceMessageId: 'message-1' },
  })
  assert.ok(self.ok && self.personId === 'speaker')
  const unresolved = resolveLearningSubject(ctx(), {
    ...candidate,
    subject: { kind: 'unresolved' },
  })
  assert.ok(unresolved.ok && unresolved.personId === undefined)
  assert.equal(resolveLearningSubject(ctx(), { ...candidate, personId: 'speaker' }).ok, true)
  assert.equal(resolveLearningSubject(ctx(), { ...candidate, personId: 'subject' }).ok, false)
  for (const [personId, mentionPartIndex] of [
    ['subject', 0],
    ['all', 2],
    ['bot', 3],
    ['same-nickname-other-id', 1],
  ] as const)
    assert.equal(
      resolveLearningSubject(ctx(), {
        ...candidate,
        subject: { kind: 'mentioned', personId, sourceMessageId: 'message-1', mentionPartIndex },
      }).ok,
      false,
    )
  assert.equal(resolveLearningSubject({ ...ctx(), simulation: true }, candidate).ok, false)
  assert.equal(
    resolveLearningSubject(
      { ...ctx(), sourceEvidence: trustedSourceEvidence([message], () => true) },
      candidate,
    ).ok,
    false,
  )
  assert.equal(resolveLearningSubject({ ...ctx(), sceneId: 'other' }, candidate).ok, false)
})
test('source prompt preserves original mention indices and projection roundtrip without nickname identity or clock defaults', () => {
  const projected = subjectLearningMessages([message])
  assert.deepEqual(projected[0]!.subjectMentions, [{ personId: 'subject', mentionPartIndex: 1 }])
  const projection = new ContextProjection('scene')
  const wire = JSON.parse(projection.stringify({ messages: projected }))
  const subject = {
    kind: 'mentioned',
    personId: wire.messages[0].subjectMentions[0].personId,
    sourceMessageId: wire.messages[0].id,
    mentionPartIndex: 1,
  }
  assert.deepEqual(projection.restoreArguments({ subject }), {
    subject: {
      kind: 'mentioned',
      personId: 'subject',
      sourceMessageId: 'message-1',
      mentionPartIndex: 1,
    },
  })
  assert.equal(CandidateSchema.parse(candidate).occurredAt, undefined)
  assert.match(
    subjectExtractionInstructions,
    /Quoted speech, jokes, roleplay and hypotheses are not self-facts/,
  )
  assert.match(
    subjectExtractionInstructions,
    /Never use the speaker as an unresolved-subject fallback/,
  )
  assert.match(subjectExtractionInstructions, /do not replace unknown dates with now/)
})

test('new explicit subject protocol rejects ambiguous event dates without rewriting legacy inputs', () => {
  const subject = { kind: 'self', sourceMessageId: 'message-1' }
  for (const key of ['occurredAt', 'validUntil'] as const) {
    for (const value of ['', '2026-10-28', '2026-10-28T12:00:00', 'not-a-date'])
      assert.equal(
        CandidateSchema.safeParse({ ...candidate, subject, [key]: value }).success,
        false,
      )
    for (const value of ['2026-10-28T12:00:00Z', '2026-10-28T20:00:00+08:00'])
      assert.equal(CandidateSchema.safeParse({ ...candidate, subject, [key]: value }).success, true)
    assert.equal(CandidateSchema.safeParse({ ...candidate, [key]: '2026-10-28' }).success, true)
  }
  const parsed = CandidateSchema.parse({ ...candidate, subject })
  assert.equal(parsed.occurredAt, undefined)
  assert.equal(parsed.validUntil, undefined)
})
