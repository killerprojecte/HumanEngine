import assert from 'node:assert/strict'
import test from 'node:test'
import { Store } from '../src/store.ts'
import { Engine } from '../src/engine.ts'
import { HabitService, type HabitRecord } from '../src/habits.ts'
import type { TurnContext } from '../src/runtime.ts'

const DAY = 86_400_000,
  start = Date.parse('2026-11-02T00:00:00Z')
function fixture() {
  const store = new Store(':memory:')
  for (const id of ['a', 'b'])
    store.saveScene({
      id,
      kind: 'group',
      adapterId: 'offline',
      accountId: 'synthetic',
      platformSceneId: id,
      name: id,
      participationEnabled: false,
      learningEnabled: false,
      proactiveEnabled: false,
    })
  let now = start
  const habits = new HabitService(store, { now: () => now })
  const make = (
    id: string,
    sceneId: string | undefined,
    patch: Partial<HabitRecord> = {},
  ): HabitRecord => ({
    id,
    kind: 'botExpression',
    preference: 'emoji',
    ...(sceneId === undefined ? {} : { sceneId }),
    evidenceVersion: 2,
    sources: [
      { sceneId: sceneId ?? 'a', messageId: id, senderId: 'speaker', direction: 1, timestamp: now },
    ],
    weight: 0,
    confidence: 0,
    status: 'disputed',
    locked: false,
    learnedAt: new Date(now).toISOString(),
    ...patch,
  })
  const insert = (record: HabitRecord, sqlId = record.id, raw = JSON.stringify(record)) =>
    store.db.prepare('INSERT INTO habits VALUES(?,?,?)').run(sqlId, `fingerprint-${sqlId}`, raw)
  return {
    store,
    habits,
    make,
    insert,
    now: () => now,
    advance: (ms: number) => {
      now += ms
    },
  }
}
const context = { sceneId: 'a', simulation: false }

test('scoped evidence ignores foreign legacy shape errors but relevant/shared evidence still fails closed', () => {
  const f = fixture()
  try {
    const broken = f.make('foreign', 'b', { evidenceVersion: undefined, sources: null as never })
    f.insert(broken)
    assert.deepEqual(f.habits.list(context), [])
    assert.equal(f.habits.adminList().length, 1)
    f.store.db.prepare('UPDATE habits SET value=?').run(JSON.stringify({ ...broken, sceneId: 'a' }))
    assert.throws(() => f.habits.list(context), TypeError)
    f.store.db
      .prepare('UPDATE habits SET value=?')
      .run(JSON.stringify({ ...broken, sceneId: undefined }))
    assert.throws(() => f.habits.list(context), TypeError)
    f.store.db.prepare("UPDATE habits SET value='null'").run()
    assert.throws(() => f.habits.list(context), TypeError)
    f.store.db.prepare("UPDATE habits SET value='{bad'").run()
    assert.throws(() => f.habits.list(context), SyntaxError)
  } finally {
    f.store.close()
  }
})

test('foreign corrupt source metadata is isolated, while shared and local hints validate it fully', () => {
  const f = fixture()
  try {
    const identity = {
      sceneId: 'b',
      adapterId: 'offline',
      accountId: 'synthetic',
      messageId: 'foreign',
      senderId: 'speaker',
    }
    f.store.sourceEvents.observe({
      kind: 'ingress',
      identity,
      bodyHash: 'a'.repeat(64),
      occurredAt: null,
    })
    f.store.db.prepare("UPDATE source_event_heads SET value='{bad'").run()
    const record = f.make('foreign', 'b')
    f.insert(record)
    const before = f.store.db.prepare('SELECT value FROM habits').all()
    assert.deepEqual(f.habits.list(context), [])
    assert.deepEqual(f.store.db.prepare('SELECT value FROM habits').all(), before)
    for (const sceneId of ['a', undefined]) {
      f.store.db.prepare('UPDATE habits SET value=?').run(JSON.stringify({ ...record, sceneId }))
      assert.throws(() => f.habits.list(context), /source_event_corrupt/)
    }
  } finally {
    f.store.close()
  }
})

test('JS scope preserves duplicate-key last value, falsy shared scopes, ordering and fresh recall', () => {
  const f = fixture()
  try {
    const local = f.make('local', 'a')
    f.insert(local, local.id, '{"sceneId":"b",' + JSON.stringify(local).slice(1))
    f.insert(f.make('shared', undefined))
    f.insert(f.make('other', 'b'))
    assert.deepEqual(
      f.habits.list(context).map((row) => row.id),
      ['local', 'shared'],
    )
    assert.deepEqual(
      f.habits.list({ ...context, simulation: true }).map((row) => row.id),
      ['shared'],
    )
    f.store.recall('a', 'local')
    assert.deepEqual(
      f.habits.list(context).map((row) => row.id),
      ['shared'],
    )
  } finally {
    f.store.close()
  }
})

test('engine scopes private identities but retains first-original cross-scope duplicate binding', async () => {
  const f = fixture(),
    engine = new Engine(f.store)
  try {
    const first = f.make('duplicate', 'b', {
      sources: [
        {
          sceneId: 'a',
          messageId: 'first-foreign-source',
          senderId: 'speaker',
          direction: 1,
          timestamp: Date.now(),
        },
      ],
    })
    const second = f.make('duplicate', 'a', {
      sources: [
        {
          sceneId: 'a',
          messageId: 'second-local-source',
          senderId: 'speaker',
          direction: 1,
          timestamp: Date.now(),
        },
      ],
    })
    f.insert(first)
    f.insert(second, 'second-sql-id')
    f.insert(f.make('foreign-only', 'b', { evidenceVersion: undefined, sources: null as never }))
    const identityCalls: string[] = [],
      identity = engine.habits.validationIdentities.bind(engine.habits)
    engine.habits.validationIdentities = (ctx, ids) => {
      identityCalls.push(...ids)
      return identity(ctx, ids)
    }
    const ctx: TurnContext = {
      scene: f.store.scene('a')!,
      mode: 'real',
      turnId: 'synthetic',
      signal: new AbortController().signal,
      dependencies: new Set(),
      validations: [],
    }
    const hints = (
      engine as unknown as { learnedHints(ctx: TurnContext): HabitRecord[] }
    ).learnedHints(ctx)
    assert.deepEqual(
      hints.map((row) => row.id),
      ['duplicate'],
    )
    assert.deepEqual(identityCalls, ['duplicate'])
    assert.deepEqual([...ctx.dependencies], ['first-foreign-source'])
    assert.equal(
      ctx.validations.every((validate) => validate()),
      false,
      'foreign SQL identity cannot authorize local duplicate JSON ID',
    )
  } finally {
    await engine.dispose()
    f.store.close()
  }
})

test('scope filtering never skips disputed hints that reactivate between identity capture and exposure', async () => {
  const f = fixture(),
    engine = new Engine(f.store)
  try {
    ;(engine.habits as unknown as { now: () => number }).now = f.now
    const sources: HabitRecord['sources'] = Array.from({ length: 8 }, (_, i) => ({
      sceneId: 'a',
      messageId: `old-${i}`,
      senderId: `old-${i}`,
      direction: 1,
      timestamp: start - 90 * DAY + 1,
    }))
    sources.push({
      sceneId: 'a',
      messageId: 'negative',
      senderId: 'negative',
      direction: -1,
      timestamp: start,
    })
    f.insert(f.make('boundary', 'a', { sources }))
    assert.equal(engine.habits.adminList()[0]!.status, 'disputed')
    const list = engine.habits.list.bind(engine.habits)
    engine.habits.list = (ctx) => {
      f.advance(2)
      return list(ctx)
    }
    const ctx: TurnContext = {
      scene: f.store.scene('a')!,
      mode: 'real',
      turnId: 'boundary',
      signal: new AbortController().signal,
      dependencies: new Set(),
      validations: [],
    }
    const hints = (
      engine as unknown as { learnedHints(ctx: TurnContext): HabitRecord[] }
    ).learnedHints(ctx)
    assert.equal(hints.length, 1)
    assert.equal(hints[0]!.status, 'active')
    assert.equal(
      ctx.validations.every((validate) => validate()),
      false,
      'newly reactivated hint cannot upgrade the earlier private identity',
    )
  } finally {
    await engine.dispose()
    f.store.close()
  }
})
