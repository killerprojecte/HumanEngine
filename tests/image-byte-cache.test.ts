import sharp from 'sharp'
import assert from 'node:assert/strict'
import test from 'node:test'
import { ImageByteCache } from '../src/runtime/image-byte-cache.ts'
import { Runtime, type RuntimeAdapter, type TurnContext } from '../src/runtime.ts'
import type { Message } from '../src/onebot.ts'
import { Store } from '../src/store.ts'
import { readOneBotImage } from '../src/plugins/builtin/snowluma/media.ts'

const tick = () => new Promise((resolve) => setImmediate(resolve))
test('byte cache coalesces exact sources, isolates scenes/accounts/parts, expires and rejects oversized or failed data', async () => {
  let now = 0,
    reads = 0
  const cache = new ImageByteCache(() => now),
    adapter = {},
    signal = new AbortController().signal
  const load = async () => {
    reads++
    await tick()
    return Buffer.from('image-bytes')
  }
  const read = (scene = 'a', ref = 'first', target = adapter) =>
    cache.read(target, scene, ref, signal, () => true, load)
  await Promise.all([read(), read()])
  assert.equal(reads, 1)
  const bytes = await read()
  bytes.fill(0)
  assert.equal((await read()).toString(), 'image-bytes')
  await read('a', 'second')
  await read('b')
  await read('a', 'first', {})
  assert.equal(reads, 4)
  now = 300001
  await read()
  assert.equal(reads, 5)
  await assert.rejects(
    cache.read(
      adapter,
      'a',
      'big',
      signal,
      () => true,
      async () => Buffer.alloc(10 * 1024 * 1024 + 1),
    ),
    /media_size_limit/,
  )
  let errors = 0
  for (let n = 0; n < 2; n++)
    await assert.rejects(
      cache.read(
        adapter,
        'a',
        'failure',
        signal,
        () => true,
        async () => {
          errors++
          throw new Error('fixture')
        },
      ),
    )
  assert.equal(errors, 2)
  await assert.rejects(
    cache.read(adapter, 'a', 'first', signal, () => false, load),
    /image_source_unavailable/,
  )
})

test('one cancelled waiter cannot abort another; clearing prevents late fill and final waiter aborts the underlying read', async () => {
  const cache = new ImageByteCache(),
    adapter = {},
    first = new AbortController(),
    second = new AbortController()
  let release!: (bytes: Buffer) => void, operation!: AbortSignal
  const load = (signal: AbortSignal) => {
    operation = signal
    return new Promise<Buffer>((resolve) => {
      release = resolve
    })
  }
  const one = cache.read(adapter, 'a', 'ref', first.signal, () => true, load)
  const rejected = assert.rejects(one, /image_context_cancelled/)
  const two = cache.read(adapter, 'a', 'ref', second.signal, () => true, load)
  await tick()
  first.abort()
  await rejected
  assert.equal(operation.aborted, false)
  release(Buffer.from('ok'))
  assert.equal((await two).toString(), 'ok')
  const last = new AbortController()
  const pending = cache.read(adapter, 'a', 'late', last.signal, () => true, load)
  const gone = assert.rejects(pending, /image_context_cancelled/)
  await tick()
  last.abort()
  await gone
  assert.equal(operation.aborted, true)
  cache.clear('a')
  release(Buffer.from('late'))
  await tick()
  let reads = 0
  await cache.read(
    adapter,
    'a',
    'late',
    new AbortController().signal,
    () => true,
    async () => {
      reads++
      return Buffer.from('fresh')
    },
  )
  assert.equal(reads, 1)
})

async function fixture(
  local?: (
    sceneId: string,
    reference: string,
    messageId?: string,
  ) => { available: () => boolean; read: () => Promise<Buffer> } | undefined,
) {
  const store = new Store(':memory:')
  const scene = {
    id: 'scene',
    name: 'fixture',
    kind: 'group' as const,
    adapterId: 'adapter',
    accountId: '42',
    platformSceneId: '9',
    participationEnabled: true,
    learningEnabled: false,
    proactiveEnabled: false,
  }
  store.saveScene(scene)
  const runtime = new Runtime(store, undefined, undefined, local)
  const reference = (file: string) =>
    `ob:media:${Buffer.from(JSON.stringify({ sceneId: scene.id, type: 'image', file })).toString('base64url')}`
  const message: Message = {
    id: 'message',
    sceneId: scene.id,
    senderId: 'person',
    origin: 'live',
    status: 'available',
    sentAt: new Date().toISOString(),
    receivedAt: new Date().toISOString(),
    parts: [
      { type: 'media', mediaType: 'image', assetId: reference('file-one') },
      { type: 'media', mediaType: 'image', assetId: reference('file-two') },
    ],
  }
  const firstBytes = await sharp({
    create: { width: 3, height: 3, channels: 3, background: '#f00' },
  })
    .png()
    .toBuffer()
  const secondBytes = await sharp({
    create: { width: 3, height: 3, channels: 3, background: '#00f' },
  })
    .png()
    .toBuffer()
  let actions = 0,
    downloads = 0
  const adapter: RuntimeAdapter = {
    send: async () => ({ status: 'failed' }),
    history: async () => ({ items: [message], coverage: 'complete' }),
    getMessage: async () => ({ items: [message], coverage: 'complete' }),
    dispose() {},
    readImage: async (s, ref, signal) =>
      readOneBotImage(
        s,
        ref,
        async (_action, params) => {
          actions++
          return { url: `https://synthetic.invalid/${params.file}` }
        },
        signal,
        async (url) => {
          downloads++
          await tick()
          return url.endsWith('file-one') ? firstBytes : secondBytes
        },
      ),
  }
  runtime.addAdapter('adapter', adapter)
  runtime.ingest(message, scene, false)
  const context = (): TurnContext => ({
    scene,
    mode: 'real',
    turnId: 'turn',
    signal: new AbortController().signal,
    dependencies: new Set(),
    validations: [],
  })
  return {
    firstBytes,
    secondBytes,
    store,
    scene,
    runtime,
    message,
    adapter,
    reference,
    context,
    counts: () => ({ actions, downloads }),
    close: () => {
      runtime.dispose()
      store.close()
    },
  }
}

test('actual SnowLuma path reuses bytes for the same message but revalidates new-message sources', async () => {
  const f = await fixture()
  try {
    const ref = f.reference('file-one')
    const [background, turn] = await Promise.all([
      f.runtime.readSourceImage(f.scene.id, f.message.id, ref, new AbortController().signal),
      f.runtime.readImage(f.context(), ref, f.message),
    ])
    assert.deepEqual(background, turn)
    assert.deepEqual(turn, f.firstBytes)
    assert.deepEqual(f.counts(), { actions: 1, downloads: 1 })
    await f.runtime.readImage(f.context(), ref, f.message)
    const later = { ...f.message, id: 'later' }
    f.runtime.ingest(later, f.scene, false)
    await f.runtime.readImage(f.context(), ref, later)
    assert.deepEqual(f.counts(), { actions: 2, downloads: 2 })
    assert.deepEqual(
      await f.runtime.readImage(f.context(), f.reference('file-two'), f.message),
      f.secondBytes,
    )
    assert.deepEqual(f.counts(), { actions: 3, downloads: 3 })
    f.runtime.recall(f.scene.id, f.message.id)
    await assert.rejects(
      f.runtime.readImage(f.context(), ref, f.message),
      /image_source_unavailable/,
    )
    await f.runtime.readImage(f.context(), ref, later)
    assert.deepEqual(f.counts(), { actions: 4, downloads: 4 })
    f.runtime.addAdapter('adapter', { ...f.adapter })
    await f.runtime.readImage(f.context(), ref, later)
    assert.deepEqual(f.counts(), { actions: 5, downloads: 5 })
  } finally {
    f.close()
  }
})

test('authorized local reference resolves before network and revoked mapping cannot use cached bytes', async () => {
  let enabled = true,
    available = true,
    localReads = 0
  const f = await fixture((_scene, _ref, messageId) =>
    enabled && messageId === 'message'
      ? {
          available: () => available,
          read: async () => {
            localReads++
            return Buffer.from('local-known-image')
          },
        }
      : undefined,
  )
  try {
    const ref = f.reference('file-one')
    assert.equal(
      (await f.runtime.readImage(f.context(), ref, f.message)).toString(),
      'local-known-image',
    )
    assert.equal(localReads, 1)
    assert.deepEqual(f.counts(), { actions: 0, downloads: 0 })
    enabled = false
    await f.runtime.readImage(f.context(), ref, f.message)
    assert.equal(f.counts().downloads, 1)
    enabled = true
    available = false
    assert.equal(f.runtime.imageSourceAvailable(f.context(), f.message.id, ref), false)
    await assert.rejects(
      f.runtime.readImage(f.context(), ref, f.message),
      /image_source_unavailable/,
    )
    assert.equal(
      f.counts().downloads,
      1,
      'revoked local binding cannot fall through to warmbytes or remote',
    )
  } finally {
    f.close()
  }
})

test('background-first shared flight refreshes expired URL-only reference once for all readers', async () => {
  const f = await fixture()
  try {
    const ref = f.reference('file-one'),
      freshRef = f.reference('fresh-url')
    let reads = 0,
      refreshes = 0,
      release!: () => void
    f.adapter.readImage = async (_scene, asset) => {
      reads++
      if (asset === ref) {
        await new Promise<void>((resolve) => {
          release = resolve
        })
        throw new Error('public_http_403')
      }
      assert.equal(asset, freshRef)
      return f.firstBytes
    }
    f.adapter.getMessage = async () => {
      refreshes++
      return {
        coverage: 'complete',
        items: [
          { ...f.message, parts: [{ type: 'media', mediaType: 'image', assetId: freshRef }] },
        ],
      }
    }
    const background = f.runtime.readSourceImage(
      f.scene.id,
      f.message.id,
      ref,
      new AbortController().signal,
    )
    while (!release) await tick()
    const turn = f.runtime.readImage(f.context(), ref, f.message)
    await tick()
    release()
    const values = await Promise.all([background, turn])
    assert.deepEqual(values[0], f.firstBytes)
    assert.deepEqual(values[1], f.firstBytes)
    assert.equal(reads, 2)
    assert.equal(refreshes, 1)
    await f.runtime.readImage(f.context(), ref, f.message)
    assert.equal(reads, 2)
    assert.equal(refreshes, 1)
  } finally {
    f.close()
  }
})

test('same exact URL or file token in a new message may carry changed pixels and cannot reuse the previous source bytes', async () => {
  const f = await fixture()
  try {
    let pixels = f.firstBytes,
      reads = 0
    f.adapter.readImage = async () => {
      reads++
      return pixels
    }
    const ref = f.reference('file-one')
    assert.deepEqual(await f.runtime.readImage(f.context(), ref, f.message), f.firstBytes)
    pixels = f.secondBytes
    const next = { ...f.message, id: 'changed-source' }
    f.runtime.ingest(next, f.scene, false)
    assert.deepEqual(await f.runtime.readImage(f.context(), ref, next), f.secondBytes)
    assert.deepEqual(await f.runtime.readImage(f.context(), ref, f.message), f.firstBytes)
    assert.equal(reads, 2)
  } finally {
    f.close()
  }
})
