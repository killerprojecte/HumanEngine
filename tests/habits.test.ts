import assert from 'node:assert/strict'
import test from 'node:test'
import { HabitService, type HabitContext, type HabitResult } from '../src/habits.ts'
import { Store } from '../src/store.ts'
let clock = Date.now()
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
  return { store, habits: new HabitService(store, { now: () => clock }) }
}
function ctx(sceneId = 'a', messageId = 'm1', senderId = 'p1'): HabitContext {
  return {
    sceneId,
    simulation: false,
    sourceEvidence: [
      {
        sceneId,
        messageId,
        senderId,
        available: true,
        isBot: false,
        origin: 'live',
        text: 'Bot, from now on prefer concise replies and fewer emoji.',
        timestamp: ++clock,
      },
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
      assert.ok(Math.abs(habit.confidence - initial) < 0.00001)
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

test('Recorded single-image complaint cannot become global frequency, and local technical variants consolidate', () => {
  const { store, habits } = setup()
  try {
    const a = ctx()
    a.sourceEvidence![0]!.text =
      '机器人，你刚才发的那张表情我不喜欢，以后少用它。请记住这次反馈，回复一句文字就好，不要再发图片。'
    assert.equal(
      habits.propose(a, {
        kind: 'botExpression',
        preference: 'meme_frequency',
        direction: -1,
        sources: sources(a),
      }).status,
      'rejected',
    )
    for (const content of [
      '回答技术问题时给出更完整、详细的说明，包含关键步骤与原因',
      '该工程群成员普遍要求技术类回答详细、完整（分步骤讲清）',
    ]) {
      const c = ctx('a', content, 'p1')
      c.sourceEvidence![0]!.text = '本群以后回答技术问题请详细说明关键步骤。'
      saved(habits.propose(c, { kind: 'sceneRule', content, sources: sources(c) }))
    }
    assert.equal(habits.adminList().length, 1)
    assert.equal(habits.list(ctx('b')).length, 0)
    assert.equal(habits.adminList()[0]!.context, 'technical')
    const personal = ctx()
    personal.sourceEvidence![0]!.text = '机器人以后对我多发表情包'
    assert.equal(
      habits.propose(personal, {
        kind: 'botExpression',
        preference: 'meme_frequency',
        direction: 1,
        sources: sources(personal),
      }).status,
      'rejected',
    )
  } finally {
    store.close()
  }
})

test('Six authors reverse through one canonical axis; spam and same-source replay cannot create authority', () => {
  const { store, habits } = setup()
  try {
    const propose = (c: HabitContext, preference: 'concise' | 'detail') =>
      saved(
        habits.propose(c, { kind: 'botExpression', preference, direction: 1, sources: sources(c) }),
      )
    for (let i = 0; i < 6; i++) propose(ctx('a', `old${i}`, `p${i}`), 'concise')
    assert.ok(habits.adminList()[0]!.weight > 0.29)
    clock += 86_400_000
    for (let i = 0; i < 6; i++) propose(ctx('a', `new${i}`, `p${i}`), 'detail')
    assert.equal(habits.adminList().length, 1)
    assert.ok(habits.adminList()[0]!.weight < -0.29)
    for (let i = 0; i < 193; i++) propose(ctx('a', `spam${i}`, 'p0'), 'detail')
    assert.equal(habits.adminList()[0]!.sources.length, 6)
    // The same platform account in another group is still one author.
    propose(ctx('b', 'cross-group', 'p0'), 'detail')
    assert.equal(habits.adminList()[0]!.sources.length, 6)
    const original = ctx('a', 'replay', 'p0')
    propose(original, 'detail')
    const before = habits.adminList()[0]!.sources.find((s) => s.senderId === 'p0')!.timestamp
    clock += 86_400_000
    original.sourceEvidence![0]!.timestamp = clock
    propose(original, 'concise')
    assert.equal(habits.adminList()[0]!.sources.find((s) => s.senderId === 'p0')!.timestamp, before)
    assert.ok(habits.adminList()[0]!.weight < 0)
  } finally {
    store.close()
  }
})

test('Injected-clock phases decay, expire and renew; bounded compaction drops support and preserves recall', () => {
  const { store } = setup()
  const habits = new HabitService(store, { now: () => clock, policy: { maxSources: 8 } })
  try {
    const propose = (c: HabitContext) =>
      saved(
        habits.propose(c, {
          kind: 'botExpression',
          preference: 'concise',
          direction: 1,
          sources: sources(c),
        }),
      )
    const first = ctx()
    propose(first)
    const initial = habits.adminList()[0]!.weight
    clock += 30 * 86_400_000
    assert.ok(Math.abs(habits.adminList()[0]!.weight - initial / 2) < 1e-8)
    clock += 61 * 86_400_000
    assert.equal(habits.list(ctx()).length, 0)
    assert.equal(propose(ctx('a', 'fresh')).status, 'active')
    for (let i = 0; i < 12; i++) propose(ctx('a', `bounded${i}`, `author${i}`))
    let record = habits.adminList()[0]!
    assert.equal(record.sources.length, 8)
    assert.equal(
      habits.propose(first, {
        kind: 'botExpression',
        preference: 'concise',
        direction: 1,
        sources: sources(first),
      }).status,
      'rejected',
    )
    for (const source of record.sources) habits.recall(source)
    record = habits.adminList()[0]!
    assert.equal(record.status, 'invalidated')
    assert.equal(record.weight, 0)
    assert.equal(record.confidence, 0)
  } finally {
    store.close()
  }
})

test('Recall never resurrects an older author vote; clock skew and restart do not refresh evidence', () => {
  const { store, habits } = setup()
  try {
    const first = ctx('a', 'first', 'p1')
    const write = (c: HabitContext, direction: 1 | -1 = 1) =>
      habits.propose(c, {
        kind: 'botExpression',
        preference: 'concise',
        direction,
        sources: sources(c),
      })
    saved(write(first))
    const other = ctx('a', 'other', 'p2')
    saved(write(other))
    const correction = ctx('a', 'correction', 'p1')
    saved(write(correction, -1))
    habits.recall({ sceneId: 'a', messageId: 'correction' })
    saved(write(first))
    assert.equal(habits.adminList()[0]!.sources.length, 1)
    const future = ctx('a', 'future', 'p3')
    future.sourceEvidence![0]!.timestamp = clock + 300_001
    assert.equal(write(future).status, 'rejected')
    const weight = habits.adminList()[0]!.weight
    clock += 30 * 86_400_000
    const restarted = new HabitService(store, { now: () => clock })
    assert.ok(Math.abs(restarted.adminList()[0]!.weight - weight / 2) < 1e-8)
    const delayed = ctx('a', 'delayed', 'p3')
    delayed.sourceEvidence![0]!.timestamp = clock - 5 * 86_400_000
    saved(write(delayed))
    assert.equal(habits.adminList()[0]!.sources.length, 2)
  } finally {
    store.close()
  }
})

test('Legacy evidence is preserved but quarantined; old manual deletion still blocks canonical aliases', async () => {
  const { createHash } = await import('node:crypto')
  const { store, habits } = setup()
  try {
    const old = {
      id: 'legacy',
      kind: 'botExpression',
      preference: 'detail',
      sources: [{ sceneId: 'a', messageId: 'old', senderId: 'p1', direction: 1 }],
      weight: 0.05,
      confidence: 1 / 3,
      status: 'active',
      locked: false,
      learnedAt: new Date(clock).toISOString(),
    }
    const fp = createHash('sha256')
      .update(JSON.stringify(['bot', 'detail']))
      .digest('hex')
    store.db.prepare('INSERT INTO habits VALUES(?,?,?)').run(old.id, fp, JSON.stringify(old))
    assert.equal(habits.list(ctx()).length, 0)
    assert.equal(habits.adminList()[0]!.sources.length, 1)
    assert.equal(habits.adminList()[0]!.confidence, 0)
    assert.equal(
      JSON.parse((store.db.prepare('SELECT value FROM habits').get() as { value: string }).value)
        .status,
      'active',
    )
    habits.delete(old.id)
    const c = ctx('a', 'new')
    assert.equal(
      habits.propose(c, {
        kind: 'botExpression',
        preference: 'concise',
        direction: 1,
        sources: sources(c),
      }).status,
      'rejected',
    )
  } finally {
    store.close()
  }
})
