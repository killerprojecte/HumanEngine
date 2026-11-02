import assert from 'node:assert/strict'
import test from 'node:test'
import { Store } from '../src/store.ts'
import { MemoryService } from '../src/memory.ts'

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
      learningEnabled: true,
      proactiveEnabled: false,
    })
  const memory = new MemoryService(store)
  const create = (
    id: string,
    scope: Parameters<MemoryService['create']>[0]['scope'] = { kind: 'scene', sceneId: 'a' },
  ) =>
    memory.create({
      scope,
      content: `Synthetic ${id}`,
      kind: 'claim',
      sources: [
        { sceneId: scope.kind === 'scene' ? scope.sceneId : 'a', messageId: id, senderId: 'p' },
      ],
    })
  return {
    store,
    memory,
    create,
    close() {
      memory.dispose()
      store.close()
    },
  }
}
const ctx = { sceneId: 'a', simulation: false }

test('exact-ID reads preserve scene/private/shared authority, duplicates, missing IDs and list order', () => {
  const f = fixture()
  try {
    const first = f.create('first'),
      other = f.create('other', { kind: 'scene', sceneId: 'b' }),
      person = f.create('person', { kind: 'scene', sceneId: 'a', personId: 'private-person' }),
      shared = f.create('shared', { kind: 'sharedPublicKnowledge' })
    const ids = [first.id, other.id, person.id, shared.id, first.id, 'missing']
    assert.deepEqual(
      f.memory.listByIds(ctx, ids),
      f.memory.list(ctx).filter((row) => ids.includes(row.id)),
    )
    assert.deepEqual(
      f.memory.listByIds({ ...ctx, simulation: true }, ids),
      f.memory.list({ ...ctx, simulation: true }),
    )
    assert.deepEqual(f.memory.listByIds(ctx, []), [])
    f.memory.recall('a', 'first')
    assert.equal(f.memory.listByIds(ctx, [first.id]).length, 0)
    f.memory.update(person.id, { validFrom: '2999-01-01T00:00:00Z' })
    assert.equal(f.memory.listByIds(ctx, [person.id]).length, 0)
  } finally {
    f.close()
  }
})

test('exact-ID candidate SQL uses primary-key lookups amid 5000 same-scene records', () => {
  const f = fixture()
  try {
    const target = f.create('target')
    const insert = f.store.db.prepare(
      `INSERT INTO memories SELECT ?,scope_key,person_id,?, ?,json_set(value,'$.id',?,'$.content',?) FROM memories WHERE id=?`,
    )
    f.store.db.transaction(() => {
      for (let i = 0; i < 5000; i++)
        insert.run(
          `bulk-${i}`,
          `Bulk ${i}`,
          `synthetic-fingerprint-${i}`,
          `bulk-${i}`,
          `Bulk ${i}`,
          target.id,
        )
    })()
    const prepare = f.store.db.prepare.bind(f.store.db)
    const reads: { sql: string; count: number; args: unknown[] }[] = []
    f.store.db.prepare = ((sql: string) => {
      const statement = prepare(sql)
      if (!sql.includes('json_each(?) AS wanted CROSS JOIN memories')) return statement
      return new Proxy(statement, {
        get(object, key) {
          if (key === 'all')
            return (...args: unknown[]) => {
              const result = object.all(...args)
              reads.push({ sql, count: result.length, args })
              return result
            }
          const value = Reflect.get(object, key)
          return typeof value === 'function' ? value.bind(object) : value
        },
      })
    }) as typeof f.store.db.prepare
    assert.equal(f.memory.listByIds(ctx, [target.id, target.id, 'missing']).length, 1)
    assert.equal(reads.length, 1)
    assert.equal(reads[0]!.count, 1)
    const plan = prepare(`EXPLAIN QUERY PLAN ${reads[0]!.sql}`).all(...reads[0]!.args) as {
      detail: string
    }[]
    assert.ok(
      plan.some(({ detail }) => /SEARCH memories USING INDEX .* \(id=\?\)/.test(detail)),
      JSON.stringify(plan),
    )
    assert.ok(!plan.some(({ detail }) => /^SCAN memories/.test(detail)))
    // Empty IDs still execute the fresh lifecycle boundary, not a cached permit.
    assert.deepEqual(f.memory.listByIds(ctx, []), [])
    assert.equal(reads.at(-1)!.count, 0)
  } finally {
    f.close()
  }
})

test('search scopes candidates without prematurely filtering future eligibility', () => {
  const f = fixture()
  try {
    const target = f.create('future')
    f.memory.update(target.id, { validFrom: '2999-01-01T00:00:00Z' })
    const prepare = f.store.db.prepare.bind(f.store.db)
    let changed = false
    f.store.db.prepare = ((sql: string) => {
      if (!changed && sql.includes('WITH scoped AS MATERIALIZED')) {
        changed = true
        // Deterministic boundary: equivalent to time eligibility changing between
        // candidate screening and the final statement, without timers/sleeps.
        prepare("UPDATE memories SET value=json_remove(value,'$.validFrom') WHERE id=?").run(
          target.id,
        )
      }
      return prepare(sql)
    }) as typeof f.store.db.prepare
    assert.deepEqual(f.memory.search(ctx, 'Synthetic future'), [])
    assert.equal(changed, true)
    assert.equal(f.memory.search(ctx, 'Synthetic future').length, 1)
  } finally {
    f.close()
  }
})
