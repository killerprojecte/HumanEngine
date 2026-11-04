import assert from 'node:assert/strict'
import test from 'node:test'
import { createModelCallPager, type CallPage } from '../app/utils/model-call-pager.ts'
const page = (items: number[], nextCursor: number | null = null): CallPage<number> => ({
  items,
  nextCursor,
  retentionDays: 30,
  maxRecords: 100000,
})

test('call pagination preserves stable cursors, returns to newer pages, and resets filters', async () => {
  const queries: unknown[] = []
  const pager = createModelCallPager<number>(async (query) => {
    queries.push(query)
    return query.beforeId === 90 ? page([89], 89) : page([100, 90], 90)
  })
  await pager.reset({ modelId: 'fixture' })
  await pager.next()
  assert.equal(pager.state.page, 2)
  assert.deepEqual(pager.state.items, [89])
  await pager.previous()
  assert.equal(pager.state.page, 1)
  assert.deepEqual(queries, [
    { modelId: 'fixture', limit: 50 },
    { modelId: 'fixture', limit: 50, beforeId: 90 },
    { modelId: 'fixture', limit: 50 },
  ])
  await pager.reset({ status: 'failed' })
  assert.deepEqual(queries.at(-1), { status: 'failed', limit: 50 })
  pager.dispose()
})

test('failed navigation retains rows and page; next can be retried', async () => {
  let fail = true
  const pager = createModelCallPager<number>(async (query) => {
    if (!query.beforeId) return page([10], 10)
    if (fail) throw Error('private provider error must not reach state')
    return page([9])
  })
  await pager.reset()
  await pager.next()
  assert.equal(pager.state.failed, true)
  assert.equal(pager.state.page, 1)
  assert.deepEqual(pager.state.items, [10])
  fail = false
  await pager.retry()
  assert.equal(pager.state.page, 2)
  assert.equal(pager.state.failed, false)
  await pager.next()
  assert.equal(pager.state.page, 2)
  pager.dispose()
})

test('new filters supersede older requests even when transport ignores abort; unmount discards responses', async () => {
  const pending: Array<{ resolve: (value: CallPage<number>) => void; signal: AbortSignal }> = []
  const pager = createModelCallPager<number>(
    (_, signal) => new Promise((resolve) => pending.push({ resolve, signal })),
  )
  const first = pager.reset()
  const second = pager.reset({ role: 'planner' })
  assert.equal(pending[0]!.signal.aborted, true)
  pending[1]!.resolve(page([2]))
  await second
  pending[0]!.resolve(page([1]))
  await first
  assert.deepEqual(pager.state.items, [2])
  assert.equal(pager.state.busy, false)
  const third = pager.reset()
  pager.dispose()
  pending[2]!.resolve(page([3]))
  await third
  assert.deepEqual(pager.state.items, [])
})
