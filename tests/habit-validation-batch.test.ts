import test from 'node:test'
import assert from 'node:assert/strict'
import { createHash } from 'node:crypto'
import { mkdtempSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { Store } from '../src/store.ts'
import { HabitService, DEFAULT_HABIT_POLICY, type HabitRecord } from '../src/habits.ts'
import { RecoveryAuthority } from '../src/memory/recovery/ledger.ts'

test('habit batch preserves identities and freshly observes raw changes, recalls, scope and expiry', () => {
  const directory = mkdtempSync(join(tmpdir(), 'he-habit-batch-'))
  const store = new Store(join(directory, 'db.sqlite'))
  let now = Date.now()
  const habits = new HabitService(store, { now: () => now })
  const records: HabitRecord[] = Array.from({ length: 24 }, (_, i) => ({
    id: `habit-${i}`,
    kind: 'sceneRule',
    sceneId: 'synthetic',
    content: `Rule ${i}`,
    evidenceVersion: 2,
    sources: Array.from({ length: 3 }, (_, j) => ({
      sceneId: 'synthetic',
      messageId: `source-${i}-${j}`,
      senderId: `sender-${j}`,
      direction: 1 as const,
      timestamp: now,
    })),
    weight: 0,
    confidence: 0,
    status: 'active',
    locked: false,
    learnedAt: new Date(now).toISOString(),
  }))
  const put = store.db.prepare('INSERT INTO habits VALUES(?,?,?)')
  for (const record of records) put.run(record.id, record.id, JSON.stringify(record))
  const ctx = { sceneId: 'synthetic', simulation: false }
  const ids = records.map((r) => r.id)
  const originalEntries = RecoveryAuthority.prototype.entries
  let scans = 0
  RecoveryAuthority.prototype.entries = function (...args) {
    scans++
    return originalEntries.apply(this, args)
  }
  try {
    const first = habits.validationIdentities(ctx, ids)
    assert.ok(scans <= 8, `fixed number of full-chain scans, got ${scans}`)
    for (const raw of records) {
      const { weight: _weight, confidence: _confidence, ...stable } = raw
      const expected = createHash('sha256')
        .update(JSON.stringify({ raw, effective: stable, policy: DEFAULT_HABIT_POLICY }))
        .digest('hex')
      assert.equal(
        first.get(raw.id),
        expected,
        'same raw/effective/policy identity as individual validation',
      )
    }
    now += 2000
    assert.deepEqual(
      habits.validationIdentities(ctx, ids),
      first,
      'elapsed decay is not a revision',
    )
    habits.update(ids[0]!, { locked: true })
    assert.notEqual(habits.validationIdentities(ctx, ids).get(ids[0]!), first.get(ids[0]!))
    store.recall('synthetic', 'source-1-0')
    assert.notEqual(habits.validationIdentities(ctx, ids).get(ids[1]!), first.get(ids[1]!))
    assert.equal(
      habits.validationIdentities({ ...ctx, sceneId: 'foreign' }, ids).get(ids[2]!),
      undefined,
    )
    assert.equal(
      habits.validationIdentities({ ...ctx, simulation: true }, ids).get(ids[2]!),
      undefined,
    )
    assert.equal(habits.validationIdentities(ctx, ['missing']).get('missing'), undefined)
    now += 90 * 86400000
    assert.ok(
      [...habits.validationIdentities(ctx, ids).values()].every(
        (identity) => identity === undefined,
      ),
    )
  } finally {
    RecoveryAuthority.prototype.entries = originalEntries
    store.close()
    rmSync(directory, { recursive: true, force: true })
  }
})

test('clock callbacks cannot reuse pre-callback source qualification', () => {
  const store = new Store(':memory:')
  const now = Date.now()
  let armed = false
  const habits = new HabitService(store, {
    now: () => {
      if (armed) {
        armed = false
        store.recall('synthetic', 'source')
      }
      return now
    },
  })
  const record: HabitRecord = {
    id: 'habit',
    kind: 'sceneRule',
    sceneId: 'synthetic',
    content: 'Synthetic',
    evidenceVersion: 2,
    sources: [
      {
        sceneId: 'synthetic',
        messageId: 'source',
        senderId: 'speaker',
        direction: 1,
        timestamp: now,
      },
    ],
    weight: 0,
    confidence: 0,
    status: 'active',
    locked: false,
    learnedAt: new Date(now).toISOString(),
  }
  try {
    store.db
      .prepare('INSERT INTO habits VALUES(?,?,?)')
      .run(record.id, record.id, JSON.stringify(record))
    const ctx = { sceneId: 'synthetic', simulation: false }
    assert.ok(habits.validationIdentity(ctx, record.id))
    armed = true
    assert.equal(habits.validationIdentities(ctx, [record.id]).get(record.id), undefined)
  } finally {
    store.close()
  }
})

test('clock callbacks cannot bless prefetched later raw rows and falsy stored rows stay unavailable', () => {
  const store = new Store(':memory:')
  const now = Date.now()
  let mutation: (() => void) | undefined
  const habits = new HabitService(store, {
    now: () => {
      const run = mutation
      mutation = undefined
      run?.()
      return now
    },
  })
  const record: HabitRecord = {
    id: 'duplicate-json-id',
    kind: 'sceneRule',
    sceneId: 's',
    content: 'old',
    evidenceVersion: 2,
    sources: [
      { sceneId: 's', messageId: 'source', senderId: 'speaker', direction: 1, timestamp: now },
    ],
    weight: 0,
    confidence: 0,
    status: 'active',
    locked: false,
    learnedAt: new Date(now).toISOString(),
  }
  const ctx = { sceneId: 's', simulation: false }
  try {
    const put = store.db.prepare('INSERT OR REPLACE INTO habits VALUES(?,?,?)')
    for (const id of ['first', 'second']) put.run(id, id, JSON.stringify(record))
    const before = habits.validationIdentities(ctx, ['first', 'second'])
    assert.ok(before.get('second'))
    for (const change of ['update', 'delete']) {
      put.run('second', 'second', JSON.stringify(record))
      mutation = () =>
        change === 'delete'
          ? store.db.prepare('DELETE FROM habits WHERE id=?').run('second')
          : store.db
              .prepare('UPDATE habits SET value=? WHERE id=?')
              .run(JSON.stringify({ ...record, content: 'changed' }), 'second')
      const after = habits.validationIdentities(ctx, ['first', 'second'])
      assert.equal(after.get('first'), before.get('first'))
      assert.equal(after.get('second'), undefined)
    }
    for (const value of ['null', 'false', '0', '""']) {
      put.run('falsy', 'falsy', value)
      assert.equal(habits.validationIdentity(ctx, 'falsy'), undefined)
    }
  } finally {
    store.close()
  }
})
