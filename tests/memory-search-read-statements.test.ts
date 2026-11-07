import assert from 'node:assert/strict'
import test from 'node:test'
import { Store } from '../src/store.ts'
import { MemoryService } from '../src/memory.ts'

// Real facade search, including refreshSupport, proof reads and ranking.
test('complete memory search reuses operation-owned compiled proof reads without retaining results', () => {
  const store = new Store(':memory:'),
    memory = new MemoryService(store)
  try {
    store.saveScene({
      id: 's',
      name: 'S',
      kind: 'group',
      adapterId: 'a',
      accountId: 'b',
      platformSceneId: 's',
      participationEnabled: false,
      learningEnabled: false,
      proactiveEnabled: false,
    })
    const put = store.db.prepare('INSERT INTO memories VALUES(?,?,?,?,?,?)')
    const proof = store.db.prepare('INSERT INTO memory_supports VALUES(?,?)')
    for (let i = 0; i <= 40; i++) {
      const source = { sceneId: 's', messageId: `source-${i}`, senderId: 'p' }
      const record = {
        id: `record-${i}`,
        scope: { kind: 'scene', sceneId: 's' },
        content: `Searchable ${i}`,
        kind: 'fact',
        importance: 'medium',
        confidence: 1,
        locked: false,
        sources: i ? [source] : [],
        learnedAt: '2026-01-01T00:00:00.000Z',
        status: 'active',
        origin: i ? 'learned' : 'manual',
        ...(i ? { lineageStatus: 'verified' } : {}),
      }
      put.run(record.id, 'scene:s', null, record.content, record.id, JSON.stringify(record))
      if (i) proof.run(record.id, JSON.stringify([{ sources: [source], parents: ['record-0'] }]))
    }
    const prepare = store.db.prepare.bind(store.db),
      counts = new Map<string, number>()
    store.db.prepare = ((sql: string) => {
      counts.set(sql, (counts.get(sql) ?? 0) + 1)
      return prepare(sql)
    }) as typeof store.db.prepare
    const ctx = { sceneId: 's', simulation: false }
    const first = memory.search(ctx, 'Searchable')
    assert.equal(first.length, 20)
    assert.equal(first[0]!.id, 'record-40')
    assert.equal(
      counts.get('SELECT * FROM memories WHERE id=?'),
      2,
      'one handle per refresh/read operation',
    )
    assert.equal(counts.get('SELECT value FROM memory_supports WHERE memory_id=?'), 2)
    first[0]!.content = 'caller mutation'
    assert.equal(memory.search(ctx, 'Searchable')[0]!.content, 'Searchable 40')
    store.recall('s', 'source-40')
    assert.equal(
      memory.search(ctx, 'Searchable').some((r) => r.id === 'record-40'),
      false,
    )
    prepare('UPDATE memory_supports SET value=? WHERE memory_id=?').run('[]', 'record-39')
    assert.equal(
      memory.search(ctx, 'Searchable').some((r) => r.id === 'record-39'),
      false,
    )
  } finally {
    memory.dispose()
    store.close()
  }
})
