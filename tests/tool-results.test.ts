import assert from 'node:assert/strict'
import { test } from 'node:test'
import { readdir } from 'node:fs/promises'
import { ToolResultCache, type ResultOwner } from '../src/tool-results.ts'

const signal = () => new AbortController().signal
const owner = (scope = 'real:scene-a'): ResultOwner => ({
  scope,
  sceneId: 'scene-a',
  pluginId: 'fixture',
  dependencies: ['message-a'],
  validations: [],
})
const decode = (value: { content: string; encoding: string }) =>
  Buffer.from(value.content, value.encoding === 'base64' ? 'base64' : 'utf8')

test('large streams are fully cached before preview and repeated full/range reads reconstruct exact binary bytes', async () => {
  const cache = new ToolResultCache()
  const payload = Buffer.alloc(2 * 1024 * 1024 + 19)
  for (let i = 0; i < payload.length; i++) payload[i] = i % 256
  let consumed = 0
  async function* source() {
    for (let offset = 0; offset < payload.length; offset += 7001) {
      consumed++
      yield payload.subarray(offset, offset + 7001)
    }
  }
  try {
    const first = await cache.store(source(), owner(), signal(), { previewBytes: 64 })
    assert.equal(consumed, Math.ceil(payload.length / 7001))
    assert.equal(first.totalBytes, payload.length)
    assert.equal(first.complete, true)
    assert.equal(first.truncated, true)
    assert.equal(first.rangeUnit, 'bytes')
    assert.deepEqual(decode(first), payload.subarray(0, 64))
    const parts: Buffer[] = []
    let offset = 0
    do {
      const part = await cache.read(
        { resultId: first.resultId, full: true, offset },
        owner().scope,
        signal(),
      )
      assert.equal(part.requestedRange.length, payload.length - offset)
      assert.equal(part.returnedRange.offset, offset)
      assert.equal(part.requestComplete, part.nextOffset === null)
      parts.push(decode(part))
      assert.ok(part.returnedRange.length > 0)
      offset = part.returnedRange.end
    } while (offset < payload.length)
    assert.deepEqual(Buffer.concat(parts), payload)
    const range = { resultId: first.resultId, offset: 1048500, length: 251 }
    const again = await cache.read(range, owner().scope, signal())
    assert.deepEqual(decode(again), payload.subarray(range.offset, range.offset + range.length))
    assert.equal(again.requestComplete, true)
    assert.deepEqual(await cache.read(range, owner().scope, signal()), again)
    assert.equal(consumed, Math.ceil(payload.length / 7001), 'retrieval does not rerun the source')
  } finally {
    await cache.dispose()
  }
})

test('UTF-8 split ranges fall back losslessly to base64 and invalid ranges fail explicitly', async () => {
  const cache = new ToolResultCache()
  const text = 'a😀中z'
  try {
    const first = await cache.store(text, owner(), signal())
    assert.equal(first.content, text)
    assert.equal(first.encoding, 'utf8')
    const bom = await cache.store('\ufeffexact', owner(), signal())
    assert.deepEqual(decode(bom), Buffer.from('\ufeffexact'))
    const split = await cache.read(
      { resultId: first.resultId, offset: 2, length: 3, encoding: 'utf8' },
      owner().scope,
      signal(),
    )
    assert.equal(split.encoding, 'base64')
    assert.deepEqual(decode(split), Buffer.from(text).subarray(2, 5))
    const end = await cache.read(
      { resultId: first.resultId, offset: first.totalBytes },
      owner().scope,
      signal(),
    )
    assert.equal(end.content, '')
    assert.equal(end.nextOffset, null)
    for (const range of [
      { offset: -1 },
      { offset: 0.5 },
      { length: -1 },
      { offset: Number.MAX_SAFE_INTEGER + 1 },
      { full: true, length: 2 },
      { offset: first.totalBytes + 1 },
    ])
      await assert.rejects(
        cache.read({ resultId: first.resultId, ...range }, owner().scope, signal()),
        /tool_result_invalid_range/,
      )
  } finally {
    await cache.dispose()
  }
})

test('cache scopes, expiry, source invalidation and plugin disposal prevent stale result access', async () => {
  let now = 1000
  const cache = new ToolResultCache(10000, () => now)
  try {
    const first = await cache.store('private fixture', owner(), signal())
    for (const scope of ['real:scene-b', 'simulation:scene-a:run-a', 'real:scene-a:other-owner'])
      await assert.rejects(
        cache.read({ resultId: first.resultId }, scope, signal()),
        /tool_result_not_found_or_expired/,
      )
    const adopted: string[] = []
    const reused = await cache.read(
      { resultId: first.resultId },
      owner().scope,
      signal(),
      (entry) => adopted.push(...entry.dependencies),
    )
    assert.equal(
      reused.content,
      'private fixture',
      'same owner may read in a later turn without refetch',
    )
    assert.deepEqual(adopted, ['message-a'])
    now = 11000
    await assert.rejects(
      cache.read({ resultId: first.resultId }, owner().scope, signal()),
      /tool_result_expired/,
    )
    const recalled = await cache.store('recalled', owner(), signal())
    await cache.invalidate('scene-a', 'message-a')
    await assert.rejects(
      cache.read({ resultId: recalled.resultId }, owner().scope, signal()),
      /tool_result_not_found_or_expired/,
    )
    const disabled = await cache.store('disabled', owner(), signal())
    await cache.invalidatePlugin('fixture')
    await assert.rejects(
      cache.read({ resultId: disabled.resultId }, owner().scope, signal()),
      /tool_result_not_found_or_expired/,
    )
    let valid = true
    const invalid = await cache.store(
      'invalid',
      { ...owner(), validations: [() => valid] },
      signal(),
    )
    valid = false
    await assert.rejects(
      cache.read({ resultId: invalid.resultId }, owner().scope, signal()),
      /tool_result_invalidated/,
    )
  } finally {
    await cache.dispose()
  }
})

test('failed sources remove partial files and never publish a complete result', async () => {
  const cache = new ToolResultCache()
  try {
    async function* failed() {
      yield Buffer.from('partial')
      throw new Error('fixture failure')
    }
    await assert.rejects(cache.store(failed(), owner(), signal()), /tool_result_storage_failed/)
    const directory = (await Reflect.get(cache, 'directory')) as string
    assert.deepEqual(await readdir(directory), [])
    const recovered = await cache.store('recovered', owner(), signal())
    assert.equal(recovered.complete, true)
    assert.equal(recovered.content, 'recovered')
  } finally {
    await cache.dispose()
  }
})

test(
  'aborting or disposing a stalled source releases the pending write and removes staged files',
  { timeout: 2000 },
  async () => {
    for (const mode of ['abort', 'dispose']) {
      const cache = new ToolResultCache()
      const controller = new AbortController()
      let started!: () => void
      const ready = new Promise<void>((resolve) => {
        started = resolve
      })
      let returned = false
      const source: AsyncIterable<Uint8Array> = {
        [Symbol.asyncIterator]() {
          return {
            next() {
              started()
              return new Promise<IteratorResult<Uint8Array>>(() => {})
            },
            async return() {
              returned = true
              return { done: true as const, value: undefined }
            },
          }
        },
      }
      const pending = cache.store(source, owner(), controller.signal)
      const rejected = assert.rejects(pending, /tool_result_cancelled/)
      await ready
      if (mode === 'abort') controller.abort()
      else await cache.dispose()
      await rejected
      assert.equal(returned, true)
      await cache.dispose()
      await assert.rejects(cache.store('closed', owner(), signal()), /tool_result_cache_closed/)
    }
  },
)

test('recall and plugin disable revoke unpublished stalled writes without retaining generation maps', async () => {
  for (const mode of ['recall', 'plugin']) {
    const cache = new ToolResultCache()
    let started!: () => void
    const ready = new Promise<void>((resolve) => {
      started = resolve
    })
    const source: AsyncIterable<Uint8Array> = {
      [Symbol.asyncIterator]() {
        return {
          next() {
            started()
            return new Promise<IteratorResult<Uint8Array>>(() => {})
          },
          async return() {
            return { done: true as const, value: undefined }
          },
        }
      },
    }
    try {
      const pending = cache.store(source, owner(), signal())
      const rejected = assert.rejects(pending, /tool_result_invalidated/)
      await ready
      if (mode === 'recall') await cache.invalidate('scene-a', 'message-a')
      else await cache.invalidatePlugin('fixture')
      await rejected
      const directory = (await Reflect.get(cache, 'directory')) as string
      assert.deepEqual(await readdir(directory), [])
      const unrelated = await cache.store(
        'other',
        { ...owner('real:other'), sceneId: 'other', pluginId: 'other' },
        signal(),
      )
      assert.equal(unrelated.content, 'other')
    } finally {
      await cache.dispose()
    }
  }
})
