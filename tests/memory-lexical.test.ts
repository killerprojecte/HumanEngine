import assert from 'node:assert/strict'
import test from 'node:test'
import { Store } from '../src/store.ts'
import { MemoryService } from '../src/memory.ts'

test('Chinese lexical fallback retrieves bounded meaningful words without requiring learning or embeddings', () => {
  const store = new Store(':memory:')
  for (const id of ['a', 'b'])
    store.saveScene({
      id,
      kind: 'group',
      adapterId: 'fixture',
      accountId: 'bot',
      platformSceneId: id,
      name: id,
      participationEnabled: true,
      learningEnabled: false,
      proactiveEnabled: false,
    })
  const memory = new MemoryService(store)
  const ctx = { sceneId: 'a', simulation: false }
  try {
    const tea = memory.create({
      scope: { kind: 'scene', sceneId: 'a', personId: 'person' },
      kind: 'fact',
      content: '用户喜欢喝乌龙茶，不喜欢咖啡',
    })
    memory.create({
      scope: { kind: 'scene', sceneId: 'b', personId: 'person' },
      kind: 'fact',
      content: '用户喜欢喝红茶',
    })
    memory.create({
      scope: { kind: 'scene', sceneId: 'a' },
      kind: 'fact',
      content: '部署 PostgreSQL 17，连接超时配置为五秒',
    })
    assert.deepEqual(
      memory.search(ctx, '他喜欢喝什么茶').map((item) => item.id),
      [tea.id],
    )
    assert.deepEqual(
      memory.search(ctx, '喜欢喝茶').map((item) => item.id),
      [tea.id],
    )
    assert.deepEqual(
      memory.search(ctx, '乌龙茶').map((item) => item.id),
      [tea.id],
    )
    assert.equal(memory.search(ctx, 'PostgreSQL').length, 1)
    assert.equal(memory.search(ctx, '上海天气怎么样').length, 0)
    assert.equal(memory.search(ctx, '什么 怎么 之前 这个 用户').length, 0)
    assert.equal(
      memory.search(ctx, '茶').length,
      0,
      'single Han character is not a broad retrieval term',
    )
    assert.equal(memory.search({ sceneId: 'simulation', simulation: true }, '喜欢喝茶').length, 0)
    assert.equal(memory.list(ctx, 'person').length, 1)
    assert.equal(store.scene('a')!.learningEnabled, false)
    assert.equal(
      memory.propose(
        {
          ...ctx,
          provenance: 'source-only',
          sourceEvidence: [
            { sceneId: 'a', messageId: 'new', senderId: 'person', available: true, isBot: false },
          ],
        },
        {
          kind: 'fact',
          content: 'New fact',
          sources: [{ sceneId: 'a', messageId: 'new', senderId: 'person' }],
        },
      ).status,
      'rejected',
    )
  } finally {
    store.close()
  }
})
