import assert from 'node:assert/strict'
import test from 'node:test'
import { Store } from '../src/store.ts'
import { Engine } from '../src/engine.ts'
import { createApp } from '../src/api.ts'
import type { MemoryRecord } from '../src/memory/domain/contracts.ts'
import {
  selectAdminMemoryPage,
  type AdminMemoryPageQuery,
} from '../src/memory/interfaces/admin-page.ts'

const defaults: AdminMemoryPageQuery = {
  page: 1,
  limit: 20,
  scene: 'all',
  person: '',
  kind: 'all',
  status: 'all',
  q: '',
  locale: 'en',
}
const record = (id: string, patch: Partial<MemoryRecord> = {}): MemoryRecord => ({
  id,
  scope: { kind: 'scene', sceneId: 'a', personId: id },
  content: id,
  kind: 'fact',
  importance: 'medium',
  confidence: 1,
  locked: false,
  sources: [],
  learnedAt: '2026-01-01T00:00:00.000Z',
  status: 'active',
  origin: 'manual',
  ...patch,
})

test('admin page uses stable explicit-locale search and complete same-scene person facets', () => {
  const rows = [
    record('first', { content: 'I' }),
    record('future', { validFrom: '2099-01-01T00:00:00Z' }),
    record('inactive', { status: 'invalidated' }),
    record('foreign', { scope: { kind: 'scene', sceneId: 'b', personId: 'foreign' } }),
    record('public', { scope: { kind: 'sharedPublicKnowledge' } }),
  ]
  const page = selectAdminMemoryPage(rows, {
    ...defaults,
    scene: 'scene:a',
    q: 'ı',
    locale: 'tr',
    limit: 1,
  })
  assert.deepEqual(
    page.items.map((r) => r.id),
    ['first'],
  )
  assert.deepEqual(page.personIds, ['first', 'future', 'inactive'])
  assert.equal(page.total, 5)
  assert.equal(page.matched, 1)
})

test('page concatenation matches stable newest-first order and out-of-range/empty pages clamp', () => {
  const rows = [
    record('tie-newer'),
    record('tie-older'),
    record('newest', { learnedAt: '2026-02-01T00:00:00.000Z' }),
  ]
  const ids = [1, 2, 3].flatMap((page) =>
    selectAdminMemoryPage(rows, { ...defaults, page, limit: 1 }).items.map((r) => r.id),
  )
  assert.deepEqual(ids, ['newest', 'tie-newer', 'tie-older'])
  assert.equal(selectAdminMemoryPage(rows, { ...defaults, page: 999, limit: 2 }).page, 2)
  assert.deepEqual(
    selectAdminMemoryPage(rows, { ...defaults, scene: 'public', person: 'tie-newer' }).items,
    [],
  )
  const empty = selectAdminMemoryPage(rows, { ...defaults, q: 'absent', page: 999 })
  assert.equal(empty.page, 1)
  assert.equal(empty.matched, 0)
  assert.deepEqual(
    selectAdminMemoryPage(rows, { ...defaults, locale: 'en', q: 'i' }).items.map((r) => r.id),
    ['tie-newer', 'tie-older'],
  )
})

function fixture() {
  const store = new Store(':memory:')
  const engine = new Engine(store)
  const app = createApp(store, { token: 'synthetic-admin-page', engine })
  const headers = {
    Authorization: 'Bearer synthetic-admin-page',
    'Content-Type': 'application/json',
  }
  const get = (url: string) => app.request(url, { headers })
  const add = (
    name: string,
    scope: MemoryRecord['scope'] = { kind: 'scene', sceneId: 'a', personId: name },
  ) => engine.memory.create({ scope, kind: 'fact', content: `Synthetic ${name}` })
  return {
    store,
    engine,
    app,
    headers,
    get,
    add,
    close: async () => {
      await engine.dispose()
      store.close()
    },
  }
}

test('additive API preserves legacy data, all-record presentation order, revisions, facets and validation', async () => {
  const f = fixture()
  try {
    const a = f.add('a'),
      future = f.add('future'),
      inactive = f.add('inactive'),
      internal = f.add('internal')
    f.add('foreign', { kind: 'scene', sceneId: 'b', personId: 'foreign' })
    f.add('public', { kind: 'sharedPublicKnowledge' })
    f.engine.memory.update(future.id, { validFrom: '2099-01-01T00:00:00.000Z' })
    f.engine.memory.update(inactive.id, { status: 'invalidated' })
    f.store.db
      .prepare('UPDATE memories SET value=? WHERE id=?')
      .run(JSON.stringify({ ...internal, internalUse: 'synthetic' }), internal.id)
    const calls: string[] = []
    const state = f.engine.memory.revisionState.bind(f.engine.memory)
    f.engine.memory.revisionState = (id) => {
      calls.push(id)
      return state(id)
    }
    const legacy = (await (await f.get('/api/memories')).json()) as MemoryRecord[]
    const originalCalls = [...calls]
    calls.length = 0
    const response = await f.get('/api/memories/page?scene=scene:a&limit=1&page=2&locale=en')
    assert.equal(response.status, 200)
    assert.equal(response.headers.get('Cache-Control'), 'no-store')
    const paged = await response.json()
    assert.deepEqual(calls, originalCalls)
    assert.equal(paged.items.length, 1)
    assert.equal(paged.total, legacy.length)
    assert.equal(paged.matched, 3)
    assert.deepEqual(new Set(paged.personIds), new Set(['a', 'future', 'inactive']))
    const expected = legacy
      .filter((r) => r.scope.kind === 'scene' && r.scope.sceneId === 'a')
      .sort((x, y) => y.learnedAt.localeCompare(x.learnedAt, 'en'))
    assert.deepEqual(paged.items, [expected[1]])
    assert.equal((await f.app.request('/api/memories/page')).status, 401)
    for (const query of [
      'limit=101',
      'limit=0',
      'page=0',
      'page=1.5',
      'scene=scene:',
      'locale=not_a_locale',
      'locale=zz',
      'status=unknown',
      'kind=unknown',
    ])
      assert.equal((await f.get(`/api/memories/page?${query}`)).status, 400, query)
    assert.deepEqual(await (await f.get('/api/memories')).json(), legacy)
    const current = (await (await f.get('/api/memories/page?person=a&scene=scene:a')).json())
      .items[0]
    f.engine.memory.update(a.id, { locked: true })
    const stale = await f.app.request(`/api/memories/${a.id}`, {
      method: 'PATCH',
      headers: f.headers,
      body: JSON.stringify({
        locked: false,
        guard: {
          expectedVersion: current.revision.version,
          expectedDeletionEpoch: current.revision.deletionEpoch,
          idempotencyKey: 'synthetic-stale-page',
        },
      }),
    })
    assert.equal(stale.status, 409)
  } finally {
    await f.close()
  }
})

test('off-page syntax, null and revision errors remain eager; admin page adds no lifecycle mutation', async () => {
  const f = fixture()
  try {
    const older = f.add('older'),
      newer = f.add('newer')
    const update = f.store.db.prepare('UPDATE memories SET value=? WHERE id=?')
    const state = f.engine.memory.revisionState.bind(f.engine.memory)
    const calls: string[] = []
    f.engine.memory.revisionState = (id) => {
      calls.push(id)
      if (id === older.id) throw new Error('synthetic-off-page-revision')
      return state(id)
    }
    for (const url of ['/api/memories', '/api/memories/page?limit=1'])
      assert.equal((await f.get(url)).status, 503)
    assert.deepEqual(calls, [newer.id, older.id, newer.id, older.id])
    f.engine.memory.revisionState = state
    update.run('null', newer.id)
    update.run('{broken', older.id)
    assert.throws(() => f.engine.memory.adminList(), SyntaxError)
    for (const url of ['/api/memories', '/api/memories/page?limit=1'])
      assert.equal((await f.get(url)).status, 503)
    update.run(JSON.stringify(older), older.id)
    for (const url of ['/api/memories', '/api/memories/page?limit=1'])
      assert.equal((await f.get(url)).status, 503)
    update.run(JSON.stringify(newer), newer.id)
    const before = f.store.db.prepare('SELECT total_changes() AS n').get()
    assert.equal((await f.get('/api/memories/page?limit=1')).status, 200)
    assert.deepEqual(f.store.db.prepare('SELECT total_changes() AS n').get(), before)
    const assertReady = f.store.assertRecoveryReady.bind(f.store)
    f.store.assertRecoveryReady = () => {
      throw new Error('synthetic-recovery-block')
    }
    try {
      assert.equal((await f.get('/api/memories/page')).status, 503)
    } finally {
      f.store.assertRecoveryReady = assertReady
    }
  } finally {
    await f.close()
  }
})

test('page preserves JS duplicate-key interpretation instead of SQL prefilter semantics', async () => {
  const f = fixture()
  try {
    const record = f.add('duplicate')
    const base = JSON.stringify(record).slice(0, -1)
    const write = f.store.db.prepare('UPDATE memories SET value=? WHERE id=?')
    write.run(
      `${base},"content":"first","content":"last","internalUse":true,"internalUse":false}`,
      record.id,
    )
    const legacy = await (await f.get('/api/memories')).json()
    const paged = await (await f.get('/api/memories/page?q=last')).json()
    assert.deepEqual(paged.items, legacy)
    assert.equal(paged.items[0].content, 'last')
    write.run(`${base},"internalUse":false,"internalUse":true}`, record.id)
    assert.deepEqual(await (await f.get('/api/memories')).json(), [])
    assert.deepEqual((await (await f.get('/api/memories/page')).json()).items, [])
  } finally {
    await f.close()
  }
})
