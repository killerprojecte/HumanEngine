import assert from 'node:assert/strict'
import test from 'node:test'
import { PromptCacheBoundaries } from '../src/runtime/prompt-cache-boundaries.ts'

const rows = (count: number) =>
  Array.from({ length: count }, (_, id) => ({
    id: `source-${id}`,
    value: { text: `Synthetic message ${id}`, revision: 1 },
  }))
const scope = JSON.stringify(['real', 'scene-a', 'topic-a', 'planner'])

test('boundaries promote only successful requests, retain prior success after failure and deduplicate endpoints', () => {
  const cache = new PromptCacheBoundaries()
  const initial = cache.prepare(scope, rows(2), 'authority')
  assert.deepEqual(initial.contextCacheBoundaries, [1])
  initial.commit()
  const failed = cache.prepare(scope, rows(3), 'authority')
  assert.deepEqual(failed.contextCacheBoundaries, [1, 2])
  // A transport failure never calls commit and therefore never promotes endpoint 2.
  const next = cache.prepare(scope, rows(4), 'authority')
  assert.deepEqual(next.contextCacheBoundaries, [1, 3])
  next.commit()
  assert.deepEqual(cache.prepare(scope, rows(4), 'authority').contextCacheBoundaries, [3])
  assert.deepEqual(cache.prepare('empty', [], 'authority').contextCacheBoundaries, [])
})

test('newer preparation supersedes an older success even when newer request fails; disposal invalidates tickets', () => {
  const cache = new PromptCacheBoundaries()
  cache.prepare(scope, rows(2), 'authority').commit()
  const old = cache.prepare(scope, rows(3), 'authority')
  cache.prepare(scope, rows(4), 'authority') // Newer request fails.
  old.commit()
  assert.deepEqual(cache.prepare(scope, rows(5), 'authority').contextCacheBoundaries, [1, 4])
  for (const clear of [() => cache.delete(scope), () => cache.clear()]) {
    const pending = cache.prepare(scope, rows(3), 'authority')
    clear()
    pending.commit()
    assert.deepEqual(cache.prepare(scope, rows(4), 'authority').contextCacheBoundaries, [3])
  }
})

test('recall, edited hashes, changed identities and authority invalidate prior prefix without reviving it after failure', () => {
  for (const change of ['recall', 'edit', 'identity', 'authority'] as const) {
    const cache = new PromptCacheBoundaries()
    cache.prepare(scope, rows(3), 'authority').commit()
    const current = rows(4)
    if (change === 'recall') current.splice(1, 1)
    if (change === 'edit') current[1]!.value.revision = 2
    if (change === 'identity') current[1]!.id = 'different-source-same-content'
    const prepared = cache.prepare(scope, current, change === 'authority' ? 'revoked' : 'authority')
    assert.deepEqual(prepared.contextCacheBoundaries, [current.length - 1], change)
    // Even a failed changed request invalidates the previous prefix. A return to
    // the old source shape/authority cannot resurrect that discarded success.
    assert.deepEqual(cache.prepare(scope, rows(4), 'authority').contextCacheBoundaries, [3], change)
  }
})

test('mode, scene and topic keys remain isolated and captured hashes cannot be mutated through caller rows', () => {
  const cache = new PromptCacheBoundaries()
  const current = rows(2)
  const prepared = cache.prepare(scope, current, 'authority')
  current[0]!.value.text = 'changed after preparation'
  prepared.contextCacheBoundaries.push(100)
  prepared.commit()
  assert.deepEqual(cache.prepare(scope, rows(3), 'authority').contextCacheBoundaries, [1, 2])
  for (const key of [
    ['simulation', 'scene-a', 'topic-a', 'planner'],
    ['real', 'scene-b', 'topic-a', 'planner'],
    ['real', 'scene-a', 'topic-b', 'planner'],
  ])
    assert.deepEqual(
      cache.prepare(JSON.stringify(key), rows(3), 'authority').contextCacheBoundaries,
      [2],
    )
  assert.throws(
    () => cache.prepare(scope, [rows(1)[0]!, rows(1)[0]!], 'authority'),
    /duplicate_message/,
  )
})
