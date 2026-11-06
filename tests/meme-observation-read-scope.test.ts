import assert from 'node:assert/strict'
import test from 'node:test'
import { Engine } from '../src/engine.ts'
import { Store } from '../src/store.ts'
import { Runtime, type RuntimeMessage } from '../src/runtime.ts'
import type { MemeSelectionContext } from '../src/memes.ts'
import { imageParts } from '../src/plugins/builtin/snowluma/image-segments.ts'
import sharp from 'sharp'

function fixture() {
  const store = new Store(':memory:')
  const engine = new Engine(store)
  // Use real retained runtime state without scheduling unrelated background intake.
  engine.runtime.dispose()
  const runtime = new Runtime(store, () => {
    throw new Error('model forbidden')
  })
  Object.defineProperty(engine, 'runtime', { value: runtime })
  const scene = {
    id:
      'ob:s:' +
      Buffer.from(JSON.stringify(['fixture', '123', 'group', '456'])).toString('base64url'),
    adapterId: 'fixture',
    accountId: '123',
    kind: 'group' as const,
    platformSceneId: '456',
  }
  store.saveScene({
    ...scene,
    name: 'synthetic',
    participationEnabled: false,
    learningEnabled: false,
    proactiveEnabled: false,
  })
  const messages: RuntimeMessage[] = Array.from({ length: 500 }, (_, i) => ({
    id: 'm' + i,
    sceneId: scene.id,
    senderId: 'user',
    sentAt: new Date().toISOString(),
    receivedAt: new Date().toISOString(),
    origin: 'live',
    status: 'available',
    parts: [{ type: 'text', text: 'Synthetic ' + i }],
  }))
  const target = messages[499]!
  target.parts = imageParts(scene.id, 'mface', { url: 'https://fixture.invalid/sticker' })
  const media = target.parts.find((part) => part.type === 'media')!
  assert.equal(media.type, 'media')
  for (const message of messages)
    runtime.ingest(message as Parameters<Runtime['ingest']>[0], scene, false)
  engine.memes.recognizeBytes = async () => undefined
  engine.memes.observeBytes = async () => undefined
  let binds = 0
  engine.memes.bindMediaReference = () => {
    binds++
  }
  const observe = (signal = new AbortController().signal) =>
    (
      engine as unknown as {
        observeImageMeme(
          bytes: Buffer,
          message: RuntimeMessage,
          assetId: string,
          signal: AbortSignal,
        ): Promise<unknown>
      }
    ).observeImageMeme(Buffer.from('synthetic fixture bytes'), target, media.assetId, signal)
  return {
    engine,
    store,
    runtime,
    scene,
    target,
    observe,
    binds: () => binds,
    async close() {
      await engine.dispose()
      store.close()
    },
  }
}

test('image observation projects one current source and eight context rows, not the full 500-row cache', async () => {
  const f = fixture()
  try {
    const expected = f.runtime
      .preview(f.scene.id)
      .slice(-8)
      .map((item) => ({
        isBot: item.senderId === 'bot',
        text: item.parts
          .filter((part) => part.type === 'text')
          .map((part) => part.text)
          .join(' ')
          .slice(0, 2000),
      }))
    const present = f.store.presentMessage.bind(f.store)
    const projected: string[] = []
    f.store.presentMessage = (message) => {
      projected.push(message.id)
      return present(message)
    }
    let selection: MemeSelectionContext | undefined
    f.engine.memes.observeBytes = async (_context, _bytes, _source, _router, options) => {
      selection = options?.selectionContext
      return undefined
    }
    await f.observe()
    assert.deepEqual(selection?.sourceContext, expected)
    assert.equal(projected.length, 11, 'three fresh single-source guards plus eight context rows')
    assert.deepEqual(projected, [
      'm499',
      ...Array.from({ length: 8 }, (_, i) => 'm' + (492 + i)),
      'm499',
      'm499',
    ])
    assert.equal(f.binds(), 0)
  } finally {
    await f.close()
  }
})

test('image observation rechecks a recall after its analysis wait before retaining a reference', async () => {
  const f = fixture()
  let release!: () => void, entered!: () => void
  const ready = new Promise<void>((resolve) => {
    entered = resolve
  })
  f.engine.memes.observeBytes = async () => {
    entered()
    await new Promise<void>((resolve) => {
      release = resolve
    })
    return undefined
  }
  try {
    const pending = f.observe()
    await ready
    f.runtime.recall(f.scene.id, f.target.id)
    release()
    await assert.rejects(pending, /image_source_unavailable/)
    assert.equal(f.binds(), 0)
  } finally {
    release?.()
    await f.close()
  }
})

test('image recognition never reuses a retained-row snapshot from before its wait', async () => {
  const f = fixture()
  let release!: () => void, entered!: () => void
  const ready = new Promise<void>((resolve) => {
    entered = resolve
  })
  f.engine.memes.recognizeBytes = async () => {
    entered()
    await new Promise<void>((resolve) => {
      release = resolve
    })
    return undefined
  }
  let observations = 0
  f.engine.memes.observeBytes = async () => {
    observations++
    return undefined
  }
  try {
    const pending = f.observe()
    await ready
    f.store.set('settings', { ...f.store.settings, cacheEnabled: false })
    release()
    await assert.rejects(pending, /image_source_unavailable/)
    assert.equal(observations, 0)
    assert.equal(f.binds(), 0)
  } finally {
    release?.()
    await f.close()
  }
})

test('bounded preview preserves tail ordering, recall markers, identity freshness and detached results', async () => {
  const f = fixture()
  try {
    assert.deepEqual(f.runtime.previewTail(f.scene.id, 8), f.runtime.preview(f.scene.id).slice(-8))
    f.store.recall(f.scene.id, 'm498')
    f.store.set(`identity:${f.scene.id}:user`, { senderDisplayName: 'Fresh identity' })
    const tail = f.runtime.previewTail(f.scene.id, 8)
    assert.equal(tail[0]!.senderDisplayName, 'Fresh identity')
    assert.equal(tail[6]!.status, 'recalled')
    assert.deepEqual(tail[6]!.parts, [])
    tail[0]!.parts.length = 0
    assert.equal(f.runtime.previewTail(f.scene.id, 8)[0]!.parts.length, 1)
    assert.deepEqual(f.runtime.previewTail('missing', 8), [])
    for (const limit of [0, -1, 501, 1.5, Infinity])
      assert.throws(() => f.runtime.previewTail(f.scene.id, limit), /invalid_preview_limit/)
  } finally {
    await f.close()
  }
})

test('full media intake bounds the post-fetch guard as well as observation projections', async () => {
  const f = fixture()
  const bytes = await sharp({ create: { width: 2, height: 2, channels: 3, background: '#f80' } })
    .png()
    .toBuffer()
  let reads = 0
  f.runtime.addAdapter('fixture', {
    send: async () => ({ status: 'cancelled' }),
    history: async () => ({ items: [], coverage: 'unavailable' }),
    getMessage: async () => ({ items: [], coverage: 'unavailable' }),
    readImage: async () => {
      reads++
      return bytes
    },
    dispose() {},
  })
  try {
    const present = f.store.presentMessage.bind(f.store)
    const projected: string[] = []
    f.store.presentMessage = (message) => {
      projected.push(message.id)
      return present(message)
    }
    await (
      f.engine as unknown as {
        ingestMedia(message: RuntimeMessage): Promise<void>
      }
    ).ingestMedia(f.target)
    assert.equal(reads, 1)
    assert.equal(projected.length, 12, 'one post-fetch row plus eleven observation rows')
    assert.deepEqual(projected, [
      'm499',
      'm499',
      ...Array.from({ length: 8 }, (_, i) => 'm' + (492 + i)),
      'm499',
      'm499',
    ])
    assert.equal(f.binds(), 0)
  } finally {
    await f.close()
  }
})

test('full media intake rejects a source recalled during the byte fetch', async () => {
  const f = fixture()
  const bytes = await sharp({ create: { width: 2, height: 2, channels: 3, background: '#f80' } })
    .png()
    .toBuffer()
  let release!: () => void, entered!: () => void
  const ready = new Promise<void>((resolve) => {
    entered = resolve
  })
  f.runtime.addAdapter('fixture', {
    send: async () => ({ status: 'cancelled' }),
    history: async () => ({ items: [], coverage: 'unavailable' }),
    getMessage: async () => ({ items: [], coverage: 'unavailable' }),
    readImage: async () => {
      entered()
      await new Promise<void>((resolve) => {
        release = resolve
      })
      return bytes
    },
    dispose() {},
  })
  let observations = 0
  f.engine.memes.observeBytes = async () => {
    observations++
    return undefined
  }
  try {
    const pending = (
      f.engine as unknown as {
        ingestMedia(message: RuntimeMessage): Promise<void>
      }
    ).ingestMedia(f.target)
    await ready
    f.runtime.recall(f.scene.id, f.target.id)
    release()
    await pending
    assert.equal(observations, 0)
    assert.equal(f.binds(), 0)
  } finally {
    release?.()
    await f.close()
  }
})
