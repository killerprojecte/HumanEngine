import assert from 'node:assert/strict'
import test from 'node:test'
import { HabitService, type HabitContext, type HabitResult } from '../src/habits.ts'
import { Store } from '../src/store.ts'
function setup() {
  const store = new Store(':memory:')
  for (const id of ['a', 'b'])
    store.saveScene({
      id,
      kind: 'group',
      adapterId: 'snow',
      accountId: '42',
      platformSceneId: id,
      name: id,
      participationEnabled: true,
      learningEnabled: true,
      proactiveEnabled: false,
    })
  return { store, habits: new HabitService(store) }
}
function ctx(sceneId = 'a', messageId = 'm1', senderId = 'p1'): HabitContext {
  return {
    sceneId,
    simulation: false,
    sourceEvidence: [
      { sceneId, messageId, senderId, available: true, isBot: false, origin: 'live' },
    ],
  }
}
const sources = (c: HabitContext) =>
  c.sourceEvidence!.map(({ sceneId, messageId, senderId }) => ({ sceneId, messageId, senderId }))
const saved = (result: HabitResult) => {
  assert.equal(result.status, 'saved')
  if (result.status !== 'saved') throw Error('not saved')
  return result.habit
}
const snapshot = (store: Store) =>
  JSON.stringify(
    ['habits', 'habit_deleted', 'recall_tombstones', 'scenes', 'config'].map((t) =>
      store.db.prepare(`SELECT * FROM ${t}`).all(),
    ),
  )

test('Scene habits isolate facts; shared bot preferences are structured, sanitized, and simulation read-only', () => {
  const { store, habits } = setup()
  try {
    const a = ctx()
    saved(
      habits.propose(a, {
        kind: 'sceneRule',
        content: 'Use the local event abbreviation',
        sources: sources(a),
      }),
    )
    const shared = saved(
      habits.propose(a, {
        kind: 'botExpression',
        preference: 'emoji',
        direction: -1,
        sources: sources(a),
      }),
    )
    assert.equal(shared.sources.length, 0)
    assert.equal(habits.adminList().find((r) => r.id === shared.id)!.sources.length, 1)
    assert.equal(habits.list(ctx('b')).length, 1)
    assert.ok(!JSON.stringify(habits.list(ctx('b'))).includes('local event'))
    const sim = { ...a, simulation: true }
    const before = snapshot(store)
    assert.equal(habits.list(sim).length, 1)
    assert.equal(
      habits.propose(sim, { kind: 'sceneRule', content: 'No', sources: sources(a) }).status,
      'rejected',
    )
    assert.equal(
      habits.feedback(sim, shared.id, { direction: 1, sources: sources(a) }).status,
      'rejected',
    )
    assert.equal(snapshot(store), before)
    assert.equal(
      habits.propose(a, {
        kind: 'botExpression',
        preference: 'private name' as 'emoji',
        direction: 1,
        sources: sources(a),
      }).status,
      'rejected',
    )
  } finally {
    store.close()
  }
})

test('Repeated use cannot increase confidence; independent authors support and corrections weaken soft habits', () => {
  const { store, habits } = setup()
  try {
    const a = ctx()
    let habit = saved(
      habits.propose(a, {
        kind: 'sceneRule',
        content: 'Prefer short replies',
        sources: sources(a),
      }),
    )
    const initial = habit.confidence
    for (let i = 2; i < 5; i++) {
      const same = ctx('a', `m${i}`)
      habit = saved(habits.feedback(same, habit.id, { direction: 1, sources: sources(same) }))
      assert.equal(habit.confidence, initial)
    }
    const independent = ctx('a', 'm5', 'p2')
    habit = saved(
      habits.feedback(independent, habit.id, { direction: 1, sources: sources(independent) }),
    )
    assert.ok(habit.confidence > initial)
    const prior = habit.weight
    const correction = ctx('a', 'm6', 'p2')
    habit = saved(
      habits.feedback(correction, habit.id, { direction: -1, sources: sources(correction) }),
    )
    assert.ok(habit.weight < prior)
    const wrongScene = ctx('b')
    assert.equal(
      habits.feedback(wrongScene, habit.id, { direction: -1, sources: sources(wrongScene) }).status,
      'rejected',
    )
    habits.update(habit.id, { locked: true })
    assert.equal(
      habits.feedback(a, habit.id, { direction: 1, sources: sources(a) }).status,
      'rejected',
    )
    habits.delete(habit.id)
    assert.equal(
      habits.propose(a, {
        kind: 'sceneRule',
        content: 'Prefer   short replies',
        sources: sources(a),
      }).status,
      'rejected',
    )
  } finally {
    store.close()
  }
})

test('Recall retracts only its source, invalidates final-source records even locked, and prevents relearning', () => {
  const { store, habits } = setup()
  try {
    const a = ctx(),
      b = ctx('b', 'm2', 'p2')
    const habit = saved(
      habits.propose(a, {
        kind: 'botExpression',
        preference: 'concise',
        direction: 1,
        sources: sources(a),
      }),
    )
    saved(
      habits.propose(b, {
        kind: 'botExpression',
        preference: 'concise',
        direction: 1,
        sources: sources(b),
      }),
    )
    habits.update(habit.id, { locked: true })
    assert.equal(habits.recall({ sceneId: 'a', messageId: 'm1' }), 1)
    let record = habits.adminList()[0]!
    assert.equal(record.sources.length, 1)
    assert.equal(record.status, 'active')
    assert.equal(
      habits.propose(a, {
        kind: 'botExpression',
        preference: 'emoji',
        direction: 1,
        sources: sources(a),
      }).status,
      'rejected',
    )
    habits.recall({ sceneId: 'b', messageId: 'm2' })
    record = habits.adminList()[0]!
    assert.equal(record.status, 'invalidated')
    assert.equal(record.weight, 0)
    assert.equal(habits.list(a).length, 0)
  } finally {
    store.close()
  }
})

test('Learning validates current switch and live nonbot evidence at each write boundary', () => {
  const { store, habits } = setup()
  try {
    for (const change of [{ origin: 'history' as const }, { isBot: true }, { available: false }]) {
      const a = ctx()
      Object.assign(a.sourceEvidence![0]!, change)
      assert.equal(
        habits.propose(a, { kind: 'sceneRule', content: 'No', sources: sources(a) }).status,
        'rejected',
      )
    }
    const a = ctx()
    store.saveScene({ ...store.scene('a')!, learningEnabled: false })
    assert.equal(
      habits.propose(a, { kind: 'sceneRule', content: 'No', sources: sources(a) }).status,
      'rejected',
    )
    assert.equal(habits.adminList().length, 0)
  } finally {
    store.close()
  }
})
