import assert from 'node:assert/strict'
import test from 'node:test'
import { createHash } from 'node:crypto'
import { Store } from '../src/store.ts'
import { MemoryService, type MemoryRecord } from '../src/memory.ts'
import { EmbeddingIndex, type RebuildJob } from '../src/embedding-index.ts'

const hash = (record: MemoryRecord) =>
  createHash('sha256')
    .update(JSON.stringify([record.id, record.content]))
    .digest('hex')
function fixture() {
  const store = new Store(':memory:'),
    memory = new MemoryService(store)
  const index = new EmbeddingIndex(store, memory, () => {
    throw Error('no model calls authorized in query fixture')
  })
  const create = (content: string) =>
    memory.create({ scope: { kind: 'sharedPublicKnowledge' }, kind: 'fact', content })
  return {
    store,
    memory,
    index,
    create,
    async close() {
      await index.dispose()
      memory.dispose()
      store.close()
    },
  }
}

test('indexable fixed-ID reads preserve existing non-temporal eligibility and repository order', async () => {
  const f = fixture()
  try {
    const first = f.create('first'),
      future = f.create('future'),
      expired = f.create('expired')
    f.memory.update(future.id, { validFrom: '2999-01-01T00:00:00Z' })
    f.memory.update(expired.id, { validUntil: '2000-01-01T00:00:00Z' })
    const wanted = [first.id, future.id, expired.id, first.id, 'missing']
    assert.deepEqual(
      f.memory.indexableByIds(wanted),
      f.memory.indexable().filter((row) => wanted.includes(row.id)),
    )
    assert.equal(f.memory.indexableByIds(wanted).length, 3)
    assert.deepEqual(f.memory.indexableByIds([]), [])
    f.memory.delete(first.id)
    assert.deepEqual(f.memory.indexableByIds([first.id]), [])
  } finally {
    await f.close()
  }
})

test('pending joins only eligible checkpoints/vectors with exact full counts and no N existence reads', async () => {
  const f = fixture()
  try {
    const complete = f.create('complete'),
      absent = f.create('missing vector'),
      changed = f.create('changed content')
    const spaceId = f.memory.registerSpace({
      providerId: 'synthetic',
      modelId: 'synthetic',
      apiModelName: 'synthetic',
      deployment: 'local',
      preprocessing: 'text-v1',
      dimensions: 2,
      normalized: false,
    })
    f.memory.index(complete.id, spaceId, [1, 0])
    f.memory.index(changed.id, spaceId, [1, 0])
    const job: RebuildJob = {
      id: 'synthetic-job',
      encoderRevision: 'synthetic',
      modelId: 'synthetic',
      state: 'running',
      processed: 0,
      total: 0,
      requiresRetryConfirmation: false,
      spaceId,
    }
    const insertDone = f.store.db.prepare('INSERT INTO embedding_rebuild_items VALUES(?,?,?)')
    for (const record of [complete, absent, changed])
      insertDone.run(job.id, record.id, record === changed ? 'old-hash' : hash(record))
    const insert = f.store.db.prepare(
      `INSERT INTO memories SELECT ?,scope_key,person_id,?,?,json_set(value,'$.id',?,'$.content',?,'$.status','invalidated') FROM memories WHERE id=?`,
    )
    f.store.db.transaction(() => {
      for (let i = 0; i < 10_000; i++) {
        const id = `historical-${i}`
        insert.run(id, id, `fingerprint-${id}`, id, id, complete.id)
        insertDone.run(job.id, id, `hash-${id}`)
      }
    })()
    const prepare = f.store.db.prepare.bind(f.store.db)
    const reads: { sql: string; rows: number; args: unknown[] }[] = []
    f.store.db.prepare = ((sql: string) => {
      assert.ok(
        !sql.includes('SELECT 1 FROM memory_vectors WHERE memory_id=? AND space_id=?'),
        'per-record vector existence query',
      )
      const statement = prepare(sql)
      if (!sql.includes('LEFT JOIN embedding_rebuild_items AS done')) return statement
      return new Proxy(statement, {
        get(object, key) {
          if (key === 'all')
            return (...args: unknown[]) => {
              const rows = object.all(...args)
              reads.push({ sql, rows: rows.length, args })
              return rows
            }
          const value = Reflect.get(object, key)
          return typeof value === 'function' ? value.bind(object) : value
        },
      })
    }) as typeof f.store.db.prepare
    const pending = (
      f.index as unknown as { pending(job: RebuildJob): MemoryRecord[] }
    ).pending.bind(f.index)
    assert.deepEqual(
      pending(job).map((record) => record.id),
      [changed.id, absent.id],
    )
    assert.equal(job.total, 3)
    assert.equal(job.processed, 1)
    assert.equal(reads.length, 1)
    assert.equal(reads[0]!.rows, 3)
    const plan = prepare(`EXPLAIN QUERY PLAN ${reads[0]!.sql}`).all(...reads[0]!.args) as {
      detail: string
    }[]
    assert.ok(
      plan.some(({ detail }) => /SEARCH done .*\(job_id=\? AND memory_id=\?\)/.test(detail)),
      JSON.stringify(plan),
    )
    assert.ok(
      plan.some(({ detail }) => /SEARCH vectors .*\(memory_id=\? AND space_id=\?\)/.test(detail)),
      JSON.stringify(plan),
    )
    delete job.spaceId
    assert.equal(pending(job).length, 3)
    assert.equal(job.processed, 0)
    job.spaceId = spaceId
    f.store.db
      .prepare('UPDATE embedding_rebuild_items SET content_hash=? WHERE job_id=? AND memory_id=?')
      .run(hash(changed), job.id, changed.id)
    assert.deepEqual(
      pending(job).map((record) => record.id),
      [absent.id],
    )
    assert.equal(job.processed, 2)
  } finally {
    await f.close()
  }
})
