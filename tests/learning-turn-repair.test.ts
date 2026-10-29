import { ContextProjection } from '../src/runtime/context-projection.ts'
import { fixturePlanCompletion } from './helpers/reply-plan.ts'
import assert from 'node:assert/strict'
import test from 'node:test'
import { HabitService } from '../src/habits.ts'
import { ModelError } from '../src/models.ts'
import {
  call,
  completed,
  harness,
  identifier,
  message,
  otherSceneId,
  proposal,
  sceneId,
  scope,
  subjectId,
} from './helpers/learning-technical-fixture.ts'

const pause = (milliseconds: number) => new Promise((resolve) => setTimeout(resolve, milliseconds))

for (const change of ['unchanged', 'retract', 'lock'] as const) {
  test(
    `asynchronous Planner preserves a decaying hint unless its evidence changes: ${change}`,
    { timeout: 120000 },
    async (t) => {
      let clock = 1000
      t.mock.method(Date, 'now', () => clock)
      const h = harness()
      const source = { sceneId, messageId: identifier(110), senderId: subjectId }
      let release!: () => void, entered!: () => void
      const pending = new Promise<void>((resolve) => {
        release = resolve
      })
      const started = new Promise<void>((resolve) => {
        entered = resolve
      })
      try {
        h.store.saveScene({ ...scope(), participationEnabled: true })
        const inserted = h.engine.habits.propose(
          {
            sceneId,
            simulation: false,
            sourceEvidence: [
              {
                ...source,
                origin: 'live',
                available: true,
                isBot: false,
                timestamp: 0,
                text: 'Bot, always use concise diagnostics.',
              },
            ],
          },
          { kind: 'botExpression', preference: 'concise', direction: 1, sources: [source] },
        )
        assert.equal(inserted.status, 'saved')
        if (inserted.status !== 'saved') throw Error('fixture_hint_rejected')
        assert.ok(inserted.habit.weight > 0)
        let thinker = 0,
          sent = 0
        h.install(async (role, messages) => {
          const fixturePlan = fixturePlanCompletion(role, messages)
          if (fixturePlan) return fixturePlan
          if (role === 'planner') {
            entered()
            await pending
            return completed([
              call('participation.decide', { action: 'reply', reasonCode: 'addressed' }),
            ])
          }
          thinker++
          return completed(
            thinker === 1
              ? [
                  call('messages.send', {
                    operationId: identifier(111),
                    segments: [{ parts: [{ type: 'text', text: 'Operation complete.' }] }],
                  }),
                ]
              : [],
          )
        })
        h.engine.runtime.addAdapter(scope().adapterId, {
          send: async () => {
            sent++
            return { status: 'sent' }
          },
          getMessage: async () => ({ items: [], coverage: 'complete' }),
          history: async () => ({ items: [], coverage: 'complete' }),
          dispose() {},
        })
        h.engine.runtime.ingest(message(112, 'Report the diagnostic result.'), scope())
        await started
        const weight = h.engine.habits.list({ sceneId, simulation: false })[0]!.weight
        await pause(30)
        clock += 30
        assert.notEqual(h.engine.habits.list({ sceneId, simulation: false })[0]!.weight, weight)
        if (change === 'retract') h.engine.habits.recall(source)
        if (change === 'lock') h.engine.habits.update(inserted.habit.id, { locked: true })
        release()
        while (h.engine.runtime.activeTasks) await pause(5)
        assert.equal(sent, change === 'unchanged' ? 1 : 0)
        assert.equal(thinker > 0, change === 'unchanged')
      } finally {
        release()
        await h.close()
      }
    },
  )
}

test('fixed-clock hint identity tracks expiry and policy but not elapsed decay', async () => {
  const h = harness()
  let clock = 0
  const habits = new HabitService(h.store, { now: () => clock })
  const ctx = { sceneId, simulation: false }
  const source = { sceneId, messageId: identifier(120), senderId: subjectId }
  try {
    const saved = habits.propose(
      {
        ...ctx,
        sourceEvidence: [
          {
            ...source,
            origin: 'live',
            available: true,
            isBot: false,
            timestamp: 0,
            text: 'Bot, always use concise diagnostics.',
          },
        ],
      },
      { kind: 'botExpression', preference: 'concise', direction: 1, sources: [source] },
    )
    assert.equal(saved.status, 'saved')
    if (saved.status !== 'saved') throw Error('fixture_hint_rejected')
    const identity = habits.validationIdentity(ctx, saved.habit.id)
    clock += 2000
    assert.equal(habits.validationIdentity(ctx, saved.habit.id), identity)
    assert.notEqual(
      new HabitService(h.store, {
        now: () => clock,
        policy: { halfLifeDays: 15 },
      }).validationIdentity(ctx, saved.habit.id),
      identity,
    )
    clock += 90 * 86_400_000
    assert.equal(habits.validationIdentity(ctx, saved.habit.id), undefined)
  } finally {
    await h.close()
  }
})

for (const operation of ['correct-setting', 'confirm-setting'] as const) {
  test(`neutral source repair preserves technical evidence before reconciliation: ${operation}`, async () => {
    const h = harness()
    try {
      const correcting = operation === 'correct-setting'
      const previousContent = correcting
        ? 'Serializer emits text packets.'
        : 'Fixed-width frame headers are enabled.'
      const previousSource = message(210, 'Protocol baseline.', identifier(3))
      if (correcting) previousSource.senderId = subjectId
      const previous = h.seed(previousContent, previousSource, {
        kind: correcting ? 'fact' : 'claim',
        ...(correcting ? { personId: subjectId } : {}),
      })
      const source = message(
        211,
        correcting
          ? 'I am changing the serializer from text packets to binary packets.'
          : 'I independently verified that fixed-width frame headers are enabled.',
        correcting ? subjectId : identifier(4),
      )
      const sideSource = message(212, 'The retry budget is four attempts.')
      const ungrounded = proposal(
        correcting
          ? 'She requests binary packet serialization.'
          : 'She verified fixed-width frame headers.',
        source,
        correcting,
      )
      ungrounded.arguments.kind = 'claim'
      const sibling = proposal('Retry budget is four attempts.', sideSource)
      const repaired = correcting
        ? 'Serializer emits binary packets.'
        : 'Frame headers have a fixed width.'
      let repairs = 0
      h.engine.searchMemory = async () => [previous]
      h.install(async (_role, prompts, tools) => {
        if (tools?.some((item) => item.name === 'learning.repair')) {
          repairs++
          assert.equal(h.engine.memory.adminList().length, 1)
          const payload = JSON.parse(prompts[1]!.content)
          assert.equal(payload.candidates.length, 1)
          assert.deepEqual(
            payload.candidates[0].messages,
            new ContextProjection(sceneId).project([source]),
          )
          assert.equal(Object.hasOwn(payload, 'relatedMemories'), false)
          assert.equal(
            JSON.stringify(payload).includes(ungrounded.arguments.content as string),
            false,
          )
          assert.equal(JSON.stringify(payload).includes(previousContent), false)
          return completed([call('learning.repair', { candidateIndex: 0, content: repaired })])
        }
        if (tools?.some((item) => item.name === 'learning.reconcile'))
          return completed([
            call('learning.reconcile', {
              candidateIndex: 0,
              targetId: JSON.parse(prompts[1]!.content).relatedMemories.find(
                (memory: { content: string }) => memory.content === previous.content,
              ).id,
              action: correcting ? 'supersede' : 'merge',
            }),
          ])
        return completed(correcting ? [ungrounded, sibling] : [ungrounded])
      })
      await h.run([source, sideSource])
      assert.equal(repairs, 1)
      assert.equal(h.engine.learningStatus().failed, 0)
      const rows = h.engine.memory.adminList()
      const current = rows.find((item) => item.content === repaired && item.status === 'active')!
      assert.ok(current)
      assert.equal(current.kind, 'claim')
      assert.equal(current.lineageStatus, 'verified')
      assert.equal(
        rows.find((item) => item.id === previous.id)!.status,
        correcting ? 'superseded' : 'active',
      )
      if (correcting) {
        assert.deepEqual(
          current.sources.map((item) => item.messageId),
          [source.id],
        )
        assert.ok(rows.some((item) => item.content === sibling.arguments.content))
      } else {
        h.engine.memory.recall(sceneId, previousSource.id)
        const surviving = h.engine.memory.list({ sceneId, simulation: false })
        assert.equal(surviving.length, 1)
        assert.deepEqual(
          surviving[0]!.sources.map((item) => item.messageId),
          [source.id],
        )
        assert.equal(h.engine.memory.list({ sceneId: otherSceneId, simulation: false }).length, 0)
      }
      assert.equal(
        h.store.logs.filter((entry) => entry.code === 'learning_source_repair_started').length,
        1,
      )
    } finally {
      await h.close()
    }
  })
}

for (const mode of [
  'invalid-content',
  'changed-identity',
  'retracted-input',
  'temporary-error',
  'partial-write',
] as const) {
  test(`neutral repair keeps valid technical siblings and obeys commit boundaries: ${mode}`, async () => {
    const h = harness()
    try {
      const source = message(310, 'The serializer must emit binary packets.')
      const sideSource = message(311, 'The connection retry limit is four.')
      const bad = proposal('She requires binary packet serialization.', source, true)
      const sibling = proposal('Connection retry limit is four.', sideSource)
      let repairs = 0
      h.install(async (_role, _prompts, tools) => {
        if (tools?.some((item) => item.name === 'learning.repair')) {
          repairs++
          assert.equal(h.engine.memory.adminList().length, 0)
          if (mode === 'temporary-error') throw new ModelError('fixture_unavailable', 503)
          if (mode === 'retracted-input') h.store.recall(sceneId, source.id)
          return completed([
            call('learning.repair', {
              candidateIndex: 0,
              content:
                mode === 'invalid-content'
                  ? bad.arguments.content
                  : 'Serializer emits binary packets.',
              ...(mode === 'changed-identity'
                ? { personId: identifier(7), kind: 'summary', sourceMessageIds: [identifier(999)] }
                : {}),
            }),
          ])
        }
        return completed([bad, sibling])
      })
      if (mode === 'partial-write')
        h.engine.indexMemory = async () => {
          throw new ModelError('fixture_unavailable', 503)
        }
      await h.run([source, sideSource])
      assert.equal(repairs, 1)
      const rows = h.engine.memory.adminList()
      if (mode === 'temporary-error') {
        assert.equal(rows.length, 0)
        assert.equal(h.engine.learningStatus().retried, 1)
        assert.equal(h.access.learningRetries.size, 1)
      } else if (mode === 'partial-write') {
        assert.equal(rows.length, 1)
        assert.equal(h.engine.learningStatus().retried, 0)
        assert.equal(h.access.learningRetries.size, 0)
        const failure = h.engine.learningStatus().failures[0]!
        assert.equal(failure.partial, true)
        assert.equal(failure.canRetry, false)
      } else {
        assert.equal(rows.length, 1)
        assert.equal(rows[0]!.content, sibling.arguments.content)
      }
    } finally {
      await h.close()
    }
  })
}
