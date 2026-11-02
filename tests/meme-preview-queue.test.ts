import assert from 'node:assert/strict'
import test from 'node:test'
import { PreviewQueue } from '../app/utils/preview-queue.ts'

const tick = () => new Promise<void>((resolve) => setImmediate(resolve))
test('viewport additions keep active previews and enforce a global three-request limit', async () => {
  const starts: string[] = []
  const signals: AbortSignal[] = []
  const releases: Array<() => void> = []
  let active = 0,
    maximum = 0
  const queue = new PreviewQueue(async (id, signal) => {
    starts.push(id)
    signals.push(signal)
    maximum = Math.max(maximum, ++active)
    await new Promise<void>((resolve) => releases.push(resolve))
    active--
  })
  queue.update(['a', 'b', 'c'])
  await tick()
  queue.update(['a', 'b', 'c', 'd'])
  queue.update(['a', 'b', 'c', 'd', 'e'])
  await tick()
  assert.deepEqual(starts, ['a', 'b', 'c'])
  assert.ok(signals.every((signal) => !signal.aborted))
  releases.splice(0).forEach((release) => release())
  await tick()
  assert.deepEqual(starts, ['a', 'b', 'c', 'd', 'e'])
  releases.splice(0).forEach((release) => release())
  await tick()
  queue.update(['a', 'b', 'c', 'd', 'e'])
  await tick()
  assert.equal(starts.length, 5)
  assert.equal(maximum, 3)
  queue.dispose()
})

test('page replacement aborts stale previews, waits for their slots and disposal stops queued work', async () => {
  const starts: string[] = []
  const signals: AbortSignal[] = []
  const releases: Array<() => void> = []
  const queue = new PreviewQueue(async (id, signal) => {
    starts.push(id)
    signals.push(signal)
    await new Promise<void>((resolve) => releases.push(resolve))
  })
  queue.update(['a', 'b', 'c', 'old-pending'])
  await tick()
  queue.update(['new-a', 'new-b', 'new-c', 'new-pending'])
  assert.ok(signals.every((signal) => signal.aborted))
  releases.splice(0).forEach((release) => release())
  await tick()
  assert.deepEqual(starts, ['a', 'b', 'c', 'new-a', 'new-b', 'new-c'])
  queue.dispose()
  releases.splice(0).forEach((release) => release())
  await tick()
  assert.equal(starts.length, 6)
  assert.ok(signals.every((signal) => signal.aborted))
})

test('overlapping replacements retain completed previews without reloading their blob URLs', async () => {
  const starts: string[] = []
  const queue = new PreviewQueue(async (id) => {
    starts.push(id)
  })
  queue.update(['a', 'b'])
  await tick()
  queue.update(['a', 'c'])
  await tick()
  assert.deepEqual(starts, ['a', 'b', 'c'])
  // A complete page reset permits a deliberate refresh/retry.
  queue.update([])
  queue.update(['a'])
  await tick()
  assert.deepEqual(starts, ['a', 'b', 'c', 'a'])
  queue.dispose()
})
