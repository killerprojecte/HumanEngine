import type { Message } from '../src/onebot.ts'
import assert from 'node:assert/strict'
import test from 'node:test'
import sharp from 'sharp'
import { Store } from '../src/store.ts'
import {
  Runtime,
  type RuntimeAdapter,
  type RuntimeMessage,
  type TurnContext,
} from '../src/runtime.ts'

async function fixture() {
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
  const runtime = new Runtime(store)
  const png = await sharp({ create: { width: 3, height: 3, channels: 3, background: '#08f' } })
    .png()
    .toBuffer()
  const message = (
    id: string,
    assetId: string,
    origin: 'live' | 'history' = 'history',
  ): Message => ({
    id,
    sceneId: scene.id,
    senderId: 'user',
    origin,
    status: 'available',
    sentAt: new Date().toISOString(),
    receivedAt: new Date().toISOString(),
    parts: [{ type: 'media', mediaType: 'image/png', assetId }],
  })
  const records = new Map<string, Message>()
  let reads = 0,
    fetches = 0
  const adapter: RuntimeAdapter = {
    send: async () => ({ status: 'failed' }),
    history: async () => ({ items: [...records.values()], coverage: 'complete' }),
    getMessage: async (_scene, id) => {
      fetches++
      return { items: records.has(id) ? [records.get(id)!] : [], coverage: 'partial' }
    },
    readImage: async (_scene, id) => {
      reads++
      if (id === 'expired') throw new Error('public_http_403')
      return png
    },
    dispose() {},
  }
  runtime.addAdapter('adapter', adapter)
  runtime.ingest(
    { ...message('seed', 'unused', 'live'), parts: [{ type: 'text', text: 'question' }] },
    scene,
    false,
  )
  const context = (): TurnContext => ({
    scene,
    mode: 'real',
    turnId: 'turn',
    signal: new AbortController().signal,
    dependencies: new Set(),
    validations: [],
  })
  const read = (ctx: TurnContext, ids: string[]) =>
    (
      runtime as unknown as {
        read(ctx: TurnContext, name: string, input: unknown): Promise<{ items: RuntimeMessage[] }>
      }
    ).read(ctx, 'messages.get', { ids })
  return {
    store,
    scene,
    runtime,
    png,
    records,
    adapter,
    message,
    context,
    read,
    counts: () => ({ reads, fetches }),
    close: () => {
      runtime.dispose()
      store.close()
    },
  }
}

test('historical pixels require trusted exact-message read, remain turn-scoped, and never enter live state', async () => {
  const f = await fixture()
  try {
    const old = f.message('old', 'historical-pixel')
    f.records.set(old.id, old)
    const ctx = f.context()
    await assert.rejects(
      f.runtime.readImage(ctx, 'historical-pixel', old),
      /image_source_unavailable/,
    )
    const result = await f.read(ctx, ['old'])
    const clone = { ...ctx, signal: new AbortController().signal }
    assert.equal(f.runtime.imageSourceAvailable(clone, old.id, 'historical-pixel'), true)
    assert.deepEqual(await f.runtime.readImage(clone, 'historical-pixel', result.items[0]), f.png)
    assert.deepEqual(
      f.runtime.preview(f.scene.id).map((m) => m.id),
      ['seed'],
    )
    await assert.rejects(
      f.runtime.readImage(f.context(), 'historical-pixel', old),
      /image_source_unavailable/,
    )
    await assert.rejects(
      f.runtime.readImage(ctx, 'historical-pixel', { ...old, sceneId: 'foreign' }),
      /image_source_unavailable/,
    )
    f.runtime.recall(f.scene.id, old.id)
    assert.equal(f.runtime.imageSourceAvailable(ctx, old.id, 'historical-pixel'), false)
    await assert.rejects(
      f.runtime.readImage(ctx, 'historical-pixel', old),
      /image_context_unavailable/,
    )
  } finally {
    f.close()
  }
})

test('live model read refreshes expired URL-only exact message and rejects extra history rows', async () => {
  const f = await fixture()
  try {
    const live = f.message('live', 'expired', 'live')
    f.runtime.ingest(live, f.scene, false)
    f.records.set('live', f.message('live', 'fresh'))
    assert.deepEqual(await f.runtime.readImage(f.context(), 'expired', live), f.png)
    assert.deepEqual(f.counts(), { reads: 2, fetches: 1 })
    const extra = f.message('extra', 'secret')
    f.adapter.getMessage = async () => ({ items: [extra], coverage: 'complete' })
    const ctx = f.context()
    assert.deepEqual((await f.read(ctx, ['requested'])).items, [])
    assert.equal(f.runtime.imageSourceAvailable(ctx, extra.id, 'secret'), false)
  } finally {
    f.close()
  }
})

test('historical authority rejects adapter replacement and late recall/cancellation', async () => {
  const f = await fixture()
  try {
    const old = f.message('old', 'pixel')
    f.records.set(old.id, old)
    const ctx = f.context()
    await f.read(ctx, ['old'])
    let release!: (bytes: Buffer) => void
    f.adapter.readImage = async () =>
      new Promise<Buffer>((resolve) => {
        release = resolve
      })
    const pending = f.runtime.readImage(ctx, 'pixel', old)
    while (!release) await new Promise((resolve) => setImmediate(resolve))
    f.runtime.recall(f.scene.id, old.id)
    release(f.png)
    await assert.rejects(pending, /image_context_cancelled/)
    const other = f.message('other', 'pixel2')
    f.records.set(other.id, other)
    const next = f.context()
    await f.read(next, ['other'])
    const aborted = f.context()
    const controller = new AbortController()
    aborted.signal = controller.signal
    await f.read(aborted, ['other'])
    release = undefined as unknown as typeof release
    const cancelled = f.runtime.readImage(aborted, 'pixel2', other)
    while (!release) await new Promise((resolve) => setImmediate(resolve))
    controller.abort()
    release(f.png)
    await assert.rejects(cancelled, /image_context_cancelled/)
    f.runtime.addAdapter('adapter', { ...f.adapter })
    assert.equal(f.runtime.imageSourceAvailable(next, other.id, 'pixel2'), false)
  } finally {
    f.close()
  }
})
