import assert from 'node:assert/strict'
import test from 'node:test'
import { mkdtempSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { MemoryService } from '../src/memory.ts'
import { Store } from '../src/store.ts'

function fixture() {
  const directory = mkdtempSync(join(tmpdir(), 'memory-search-ranking-'))
  const store = new Store(join(directory, 'data', 'state.sqlite'))
  const memory = new MemoryService(store)
  const create = (content: string, personId?: string, publicRecord = false) =>
    memory.create({
      scope: publicRecord
        ? { kind: 'sharedPublicKnowledge' }
        : { kind: 'scene', sceneId: 'scene', personId },
      kind: 'fact',
      content,
      confidence: 1,
      importance: 'medium',
    })
  const spaceId = memory.registerSpace({
    providerId: 'offline',
    modelId: 'offline',
    apiModelName: 'offline',
    deployment: 'offline',
    dimensions: 2,
    normalized: true,
    preprocessing: 'plain',
  })
  return {
    store,
    memory,
    create,
    spaceId,
    close() {
      memory.dispose()
      store.close()
      rmSync(directory, { recursive: true, force: true })
    },
  }
}

test('durable fixed search preserves scope, vector/lexical ranking, limits and temporal exclusion against nested fallback', () => {
  const f = fixture()
  const ctx = { sceneId: 'scene', simulation: false }
  try {
    const first = f.create('alpha first', 'one')
    const second = f.create('beta second', 'one')
    const other = f.create('alpha other person', 'two')
    const shared = f.create('alpha shared', undefined, true)
    const future = f.create('alpha future', 'one')
    const expired = f.create('alpha expired', 'one')
    f.memory.update(future.id, { validFrom: '2100-01-01T00:00:00.000Z' })
    f.memory.update(expired.id, { validUntil: '2000-01-01T00:00:00.000Z' })
    for (const record of [first, other, shared, future, expired])
      f.memory.index(record.id, f.spaceId, [1, 0])
    f.memory.index(second.id, f.spaceId, [0, 1])
    const queries = [
      () => f.memory.search(ctx, 'alpha'),
      () => f.memory.search(ctx, 'alpha', 'one'),
      () => f.memory.search(ctx, '', 'one', { spaceId: f.spaceId, vector: [0, 1], limit: 1 }),
      () =>
        f.memory.search(ctx, 'alpha', 'one', { spaceId: f.spaceId, vector: [0, 1], limit: 100 }),
      () => f.memory.search({ ...ctx, simulation: true }, ''),
      () => f.memory.search({ ...ctx, simulation: true }, '', 'one'),
    ]
    for (const search of queries) {
      const fixed = search()
      assert.deepEqual(fixed, f.store.db.transaction(search)())
      assert.ok(fixed.every((record) => record.id !== future.id && record.id !== expired.id))
    }
    assert.deepEqual(
      queries[1]!().map((record) => record.id),
      [first.id],
    )
    assert.deepEqual(
      queries[2]!().map((record) => record.id),
      [second.id],
    )
    assert.deepEqual(
      queries[4]!().map((record) => record.id),
      [shared.id],
    )
    assert.deepEqual(queries[5]!(), [])
    assert.throws(
      () =>
        f.memory.search({ ...ctx, simulation: true }, '', 'one', {
          spaceId: 'missing',
          vector: [1, 0],
        }),
      /vector_space_not_found/,
      'invalid vector space is still checked even for an empty allowed scope',
    )
  } finally {
    f.close()
  }
})
