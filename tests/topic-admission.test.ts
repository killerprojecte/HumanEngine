import assert from 'node:assert/strict'
import test from 'node:test'
import { assignTopic, parseTopicCue, type TopicSnapshot } from '../src/runtime/topic-admission.ts'
const topic = (id: string, extra: Partial<TopicSnapshot> = {}): TopicSnapshot => ({
  id,
  sceneId: 's',
  touchedAt: 1000,
  participants: ['A'],
  provisional: false,
  active: false,
  pending: false,
  waiting: false,
  sendState: 'none',
  ...extra,
})
const input = { sceneId: 's', senderId: 'A', now: 2000 }
test('same-minute independent plans and reply priority preserve stable identities', () => {
  const dinner = topic('dinner', { cue: { initiatorId: 'A', label: '晚饭' } }),
    run = topic('run', {
      participants: ['B'],
      cue: { initiatorId: 'B', label: '跑步' },
    })
  assert.equal(assignTopic({ ...input, cue: '晚饭' }, [dinner, run]).topicId, 'dinner')
  assert.equal(
    assignTopic({ ...input, anchors: [{ kind: 'reply', topicId: 'run' }], cue: '晚饭' }, [
      dinner,
      run,
    ]).topicId,
    'run',
  )
  assert.equal(assignTopic({ ...input, senderId: 'B', cue: '晚饭' }, [dinner, run]).kind, 'create')
  assert.equal(assignTopic({ ...input, senderId: 'C' }, [dinner]).reason, 'single_conversation')
})
test('same participant in two plans remains ambiguous, bucket reused without semantic guess', () => {
  const topics = [topic('one'), topic('two')]
  assert.deepEqual(assignTopic(input, topics), {
    kind: 'provisional',
    reason: 'ambiguous',
    candidateIds: ['one', 'two'],
  })
  assert.equal(
    assignTopic(input, [...topics, topic('bucket', { provisional: true })]).topicId,
    'bucket',
  )
  assert.equal(
    assignTopic({ ...input, unavailableReply: true }, topics).reason,
    'reply_anchor_unavailable',
  )
})
test('strict capacity never evicts active, pending, WAIT or partial/unknown sends', () => {
  for (const protectedState of [
    { active: true },
    { pending: true },
    { waiting: true },
    { sendState: 'partial' as const },
    { sendState: 'unknown' as const },
  ]) {
    const topics = Array.from({ length: 4 }, (_, i) => topic(String(i), protectedState))
    assert.equal(assignTopic({ ...input, cue: 'new' }, topics).kind, 'deferred')
    assert.deepEqual(
      topics,
      Array.from({ length: 4 }, (_, i) => topic(String(i), protectedState)),
    )
  }
  const topics = [
    topic('old', { active: true, touchedAt: 1 }),
    topic('idle', { touchedAt: 2 }),
    topic('p', { pending: true }),
    topic('u', { sendState: 'unknown' }),
  ]
  assert.equal(assignTopic({ ...input, cue: 'new' }, topics).evictTopicId, 'idle')
})
test('foreign, future and missing anchors never fabricate continuity', () => {
  assert.equal(
    assignTopic({ ...input, anchors: [{ kind: 'reply', topicId: 'foreign' }] }, [
      topic('foreign', { sceneId: 'other' }),
    ]).reason,
    'reply_anchor_unavailable',
  )
  assert.equal(assignTopic(input, [topic('future', { touchedAt: 3000 })]).reason, 'no_recent_topic')
  assert.equal(
    assignTopic(
      {
        ...input,
        anchors: [
          { kind: 'thread', topicId: 'one' },
          { kind: 'thread', topicId: 'two' },
        ],
      },
      [topic('one'), topic('two')],
    ).reason,
    'conflicting_anchors',
  )
})
test('cues require explicit bounded syntax; ordinary natural language remains unparsed', () => {
  assert.equal(parseTopicCue('[话题: 晚饭] 18点，只在双方有空时'), '晚饭')
  assert.equal(parseTopicCue('明天去吃饭'), undefined)
  assert.equal(parseTopicCue('引用 [topic: dinner]'), undefined)
  assert.equal(parseTopicCue('[topic: ' + 'a'.repeat(33) + ']'), undefined)
  assert.throws(() => assignTopic(input, [topic('x'), topic('x')]), /snapshot_invalid/)
})
