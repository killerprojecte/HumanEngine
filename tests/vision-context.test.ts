import { Engine } from '../src/engine.ts'
import assert from 'node:assert/strict'
import test from 'node:test'
import sharp from 'sharp'
import { Store } from '../src/store.ts'
import { describeCurrentImages, ImageDescriptionCache } from '../src/vision-context.ts'
import type { Runtime, RuntimeMessage, TurnContext } from '../src/runtime.ts'
import type { Router, ChatMessage, Completion } from '../src/models.ts'

function imageMessage(index: number): RuntimeMessage {
  return {
    id: `image-${index}`,
    sceneId: 'scene',
    senderId: 'person',
    origin: 'live',
    status: 'available',
    sentAt: new Date().toISOString(),
    receivedAt: new Date().toISOString(),
    parts: [{ type: 'media', mediaType: 'image', assetId: `opaque-private-image-${index}` }],
  }
}
function fixture() {
  const store = new Store(':memory:')
  store.saveScene({
    id: 'scene',
    name: 'Fixture',
    kind: 'group',
    adapterId: 'fixture',
    accountId: 'bot',
    platformSceneId: 'scene',
    participationEnabled: true,
    learningEnabled: false,
    proactiveEnabled: true,
  })
  store.set('models', {
    providers: [
      {
        id: 'provider',
        name: 'Fixture',
        baseUrl: 'https://fixture.invalid',
        protocol: 'openai-chat',
        credentialEnv: '',
      },
    ],
    models: [
      {
        id: 'vision-model',
        displayName: 'Vision',
        providerId: 'provider',
        apiModelName: 'fixture-vision',
        capabilities: ['vision'],
      },
    ],
    routes: [{ role: 'vision', orderedModelIds: ['vision-model'], timeoutMs: 1000 }],
  })
  const controller = new AbortController()
  const context: TurnContext = {
    scene: store.scene('scene')!,
    mode: 'real',
    turnId: 'turn',
    signal: controller.signal,
    dependencies: new Set(),
    validations: [],
  }
  return { store, controller, context }
}
function completed(text = 'private-description-canary blue square'): Completion {
  return {
    text,
    toolCalls: [],
    usage: { inputTokens: 5, outputTokens: 3, totalTokens: 8 },
    modelId: 'vision-model',
    assistant: { role: 'assistant', content: text },
  }
}

test('current images become actual image model inputs even when learning is disabled; bounded cache prevents repeated billing', async () => {
  const { store, context } = fixture()
  const messages = Array.from({ length: 6 }, (_, i) => imageMessage(i))
  const png = await sharp({ create: { width: 2, height: 2, channels: 3, background: '#00f' } })
    .png()
    .toBuffer()
  let reads = 0,
    calls = 0
  const runtime = {
    preview: () => messages,
    imageSourceAvailable: (_context: unknown, id: string, assetId: string) =>
      messages.some(
        (m) => m.id === id && m.parts.some((p) => p.type === 'media' && p.assetId === assetId),
      ),
    readImage: async () => {
      reads++
      return png
    },
  } as unknown as Runtime
  const router = () =>
    ({
      complete: async (role: string, input: ChatMessage[]) => {
        assert.equal(role, 'vision')
        calls++
        assert.match(input[1]!.images![0]!, /^data:image\/png;base64,/)
        assert.match(input[0]!.content, /untrusted data/)
        assert.match(input[0]!.content, /possible common meme or sticker uses/)
        assert.match(input[0]!.content, /does not prove the sender feels/)
        assert.match(input[0]!.content, /Only the first frame/)
        return completed()
      },
    }) as unknown as Router
  const cache = new ImageDescriptionCache()
  try {
    const result = await describeCurrentImages(store, runtime, router, context, messages, cache)
    assert.equal(store.scene('scene')!.learningEnabled, false)
    assert.equal(result.images.length, 4)
    assert.equal(result.omitted.length, 2)
    assert.deepEqual(
      result.images.map((image) => image.messageId),
      ['image-2', 'image-3', 'image-4', 'image-5'],
    )
    assert.equal(result.trust, 'untrusted_image_descriptions')
    assert.equal(calls, 4)
    assert.equal(reads, 4)
    await describeCurrentImages(
      store,
      runtime,
      router,
      { ...context, dependencies: new Set(), validations: [] },
      messages,
      cache,
    )
    assert.equal(calls, 4)
    assert.equal(reads, 4)
    assert.ok(store.logs.some((row) => row.reason === 'image_description_cached'))
    await describeCurrentImages(store, runtime, router, context, [], cache)
    assert.equal(calls, 4, 'unrelated text turn does not refetch historical images')
    store.set('models', {
      ...store.models,
      models: store.models.models.map((model) => ({
        ...model,
        apiModelName: 'fixture-vision-changed',
      })),
    })
    await describeCurrentImages(store, runtime, router, context, [messages[5]!], cache)
    assert.equal(calls, 5, 'model configuration change invalidates the old description')
    store.recall('scene', 'image-5')
    await assert.rejects(
      describeCurrentImages(store, runtime, router, context, [messages[5]!], cache),
      /request_cancelled/,
    )
    assert.equal(calls, 5)
    assert.doesNotMatch(
      JSON.stringify(store.logs),
      /opaque-private-image|private-description-canary|data:image/,
    )
  } finally {
    store.close()
  }
})

test('unavailable vision route is explicit and no media is fetched; cancellation drops late descriptions', async () => {
  const { store, context, controller } = fixture()
  const messages = [imageMessage(0)]
  let reads = 0
  const png = await sharp({ create: { width: 1, height: 1, channels: 3, background: '#fff' } })
    .png()
    .toBuffer()
  const runtime = {
    preview: () => messages,
    imageSourceAvailable: (_context: unknown, id: string, assetId: string) =>
      messages.some(
        (m) => m.id === id && m.parts.some((p) => p.type === 'media' && p.assetId === assetId),
      ),
    readImage: async () => {
      reads++
      return png
    },
  } as unknown as Runtime
  const original = store.models
  store.set('models', { ...original, routes: [] })
  try {
    const noRoute = await describeCurrentImages(
      store,
      runtime,
      () => {
        throw new Error('must_not_call')
      },
      context,
      messages,
    )
    assert.equal(noRoute.images[0]!.status, 'unavailable')
    assert.equal(noRoute.images[0]!.reason, 'route_not_configured')
    assert.equal(reads, 0)
    store.set('models', original)
    const router = () =>
      ({
        complete: async () => {
          controller.abort()
          return completed()
        },
      }) as unknown as Router
    await assert.rejects(
      describeCurrentImages(store, runtime, router, context, messages),
      /request_cancelled/,
    )
    const rows = store.logs.filter((row) => row.stage === 'vision.image')
    assert.ok(rows.some((row) => row.status === 'cancelled'))
    assert.equal(
      rows.some((row) => row.status === 'completed'),
      false,
    )
    assert.doesNotMatch(JSON.stringify(store.logs), /private-description-canary/)
  } finally {
    store.close()
  }
})

test('background legacy image fallback never fetches an unavailable or recalled source', async () => {
  const { store } = fixture()
  let fetched = 0
  const engine = new Engine(store, {
    fetchMedia: async () => {
      fetched++
      throw new Error('must_not_fetch')
    },
  })
  engine.runtime.addAdapter('fixture', {
    send: async () => ({ status: 'cancelled' }),
    history: async () => ({ items: [], coverage: 'unavailable' }),
    getMessage: async () => ({ items: [], coverage: 'unavailable' }),
    dispose() {},
  })
  const assetId = `ob:media:${Buffer.from(JSON.stringify({ sceneId: 'scene', file: 'https://fixture.invalid/private-image', type: 'image' })).toString('base64url')}`
  const message: RuntimeMessage = {
    ...imageMessage(0),
    parts: [{ type: 'media', mediaType: 'image', assetId }],
  }
  const intake = engine as unknown as { ingestMedia(message: RuntimeMessage): Promise<void> }
  try {
    await intake.ingestMedia(message)
    assert.equal(fetched, 0)
    engine.runtime.ingest(message, store.scene('scene')!, false)
    store.recall('scene', message.id)
    await intake.ingestMedia(message)
    assert.equal(fetched, 0)
    assert.doesNotMatch(JSON.stringify(store.logs), /private-image|ob:media:/)
  } finally {
    await engine.dispose()
    store.close()
  }
})

test('quoted image outranks newer images; four unique assets cover duplicate provenance and bound later history', async () => {
  const { store, context } = fixture()
  const messages = Array.from({ length: 6 }, (_, i) => imageMessage(i))
  const duplicate = { ...messages[0]!, id: 'duplicate', origin: 'history' as const }
  messages.push(duplicate)
  context.imagePriorityIds = ['image-0', ...messages.toReversed().map((m) => m.id)]
  const bytes = await sharp({ create: { width: 1, height: 1, channels: 3, background: '#abc' } })
    .png()
    .toBuffer()
  let calls = 0
  const runtime = {
    imageSourceAvailable: () => true,
    readImage: async () => bytes,
  } as unknown as Runtime
  const router = () =>
    ({
      complete: async () => {
        calls++
        return completed('Visible smile; possibly playful, not certain sender intent.')
      },
    }) as unknown as Router
  const cache = new ImageDescriptionCache()
  try {
    const result = await describeCurrentImages(store, runtime, router, context, messages, cache)
    assert.equal(calls, 4)
    assert.deepEqual(
      result.images.map((m) => m.messageId),
      ['image-0', 'image-3', 'image-4', 'image-5', 'duplicate'],
    )
    assert.deepEqual(result.omitted.map((m) => m.messageId).sort(), ['image-1', 'image-2'])
    const later = await describeCurrentImages(
      store,
      runtime,
      router,
      { ...context },
      [imageMessage(8)],
      cache,
    )
    assert.equal(
      calls,
      4,
      'history tools share the same per-turn budget, even through a context clone',
    )
    assert.equal(later.omitted[0]?.reason, 'image_budget_exhausted')
    assert.equal(later.images.length, 0)
  } finally {
    store.close()
  }
})

for (const failure of ['empty', 'download'] as const) {
  test(`duplicate ${failure} failures attempt once per turn, retain attribution and retry only in a new turn`, async () => {
    const { store, context } = fixture()
    const first = imageMessage(0),
      second = { ...first, id: 'other-message' }
    const bytes = await sharp({ create: { width: 1, height: 1, channels: 3, background: '#abc' } })
      .png()
      .toBuffer()
    let reads = 0,
      calls = 0
    const runtime = {
      imageSourceAvailable: () => true,
      readImage: async () => {
        reads++
        if (failure === 'download') throw new Error('public_http_403')
        return bytes
      },
    } as unknown as Runtime
    const router = () =>
      ({
        complete: async () => {
          calls++
          return completed('')
        },
      }) as unknown as Router
    try {
      const result = await describeCurrentImages(store, runtime, router, context, [first, second])
      assert.equal(reads, 1)
      assert.equal(calls, failure === 'empty' ? 1 : 0)
      assert.deepEqual(
        result.images.map((image) => image.messageId),
        [first.id, second.id],
      )
      assert.ok(
        result.images.every(
          (image) =>
            image.status === 'unavailable' &&
            image.reason === (failure === 'empty' ? 'empty_image_description' : 'public_http_403'),
        ),
      )
      await describeCurrentImages(store, runtime, router, { ...context }, [second])
      assert.equal(reads, 1)
      await describeCurrentImages(
        store,
        runtime,
        router,
        { ...context, dependencies: new Set(), validations: [] },
        [first],
      )
      assert.equal(reads, 2)
    } finally {
      store.close()
    }
  })
}
