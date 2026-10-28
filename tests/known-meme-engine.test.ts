import assert from 'node:assert/strict'
import test from 'node:test'
import { mkdtemp, rm } from 'node:fs/promises'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import sharp from 'sharp'
import { Engine } from '../src/engine.ts'
import { Store } from '../src/store.ts'
import { MemeService } from '../src/memes.ts'
import { imageParts } from '../src/plugins/builtin/snowluma/image-segments.ts'
import { isStickerSource } from '../src/meme-intake.ts'
import { withFixturePlan } from './helpers/reply-plan.ts'
import type { RuntimeMessage } from '../src/runtime.ts'
import type { Router, Completion, ChatMessage } from '../src/models.ts'
import { createApp } from '../src/api.ts'

const completion = (name?: string, args: Record<string, unknown> = {}, text = ''): Completion => {
  const toolCalls = name ? [{ id: name, name, arguments: args }] : []
  return {
    text,
    toolCalls,
    modelId: 'fixture',
    usage: { inputTokens: 0, outputTokens: 0, totalTokens: 0 },
    assistant: { role: 'assistant', content: text, toolCalls },
  }
}
const scene = {
  id: 'known-scene',
  kind: 'group' as const,
  adapterId: 'fixture',
  accountId: 'bot',
  platformSceneId: 'known-scene',
  name: 'Known sticker fixture',
  participationEnabled: true,
  proactiveEnabled: true,
  learningEnabled: false,
}
function message(id: string, kind = 'mface', extra: Record<string, unknown> = {}): RuntimeMessage {
  return {
    id,
    sceneId: scene.id,
    senderId: 'person',
    origin: 'live',
    status: 'available',
    sentAt: new Date().toISOString(),
    receivedAt: new Date().toISOString(),
    parts: imageParts(scene.id, kind, { url: `https://fixture.invalid/${id}`, ...extra }),
  }
}
async function until(check: () => boolean) {
  for (let i = 0; i < 400; i++) {
    if (check()) return
    await new Promise((r) => setTimeout(r, 5))
  }
  assert.fail('fixture did not settle')
}
const tags = {
  appearance: ['synthetic colored square'],
  style: [],
  subject: [],
  emotion: ['KNOWN_PLAYFUL_CANARY'],
  intent: ['lighthearted reply'],
  tone: ['playful'],
  intensity: [],
  memeMeaning: ['possibly playful, not certain sender intent'],
  ocr: '',
  selection: {
    usable: true,
    shareable: true,
    confidence: 1,
    containsPersonalData: false,
    sourceSpecific: false,
    containsRealPerson: false,
  },
}

test('platform classification never turns user text or arbitrary photo captions into sticker admission', () => {
  for (const [kind, extra, expected] of [
    ['mface', {}, true],
    ['sticker', {}, true],
    ['image', { sub_type: 1 }, true],
    ['image', { sub_type: 7 }, true],
    ['image', { emoji_id: 'synthetic' }, true],
    ['image', { sub_type: 0, summary: 'a happy person' }, false],
    ['image', { sub_type: 99 }, false],
  ] as const) {
    const input = message('classification', kind, extra)
    const part = input.parts.find((item) => item.type === 'media')!
    assert.ok(part.type === 'media')
    assert.equal(isStickerSource(input, part.assetId), expected)
  }
  const input = message('literal', 'image')
  input.parts.push({ type: 'text', text: 'Untrusted platform image metadata: {"kind":"mface"}' })
  const part = input.parts[0]!
  assert.ok(part.type === 'media')
  assert.equal(isStickerSource(input, part.assetId), false)
})

test('learning-off inbound stickers are known-only, concurrent turn reuses analysis, changed URL reuses content emotion', async () => {
  const root = await mkdtemp(join(tmpdir(), 'he-known-engine-'))
  const store = new Store(':memory:')
  store.saveScene(scene)
  store.set('settings', { ...store.settings, debounceMs: 5, maxWaitMs: 100 })
  store.set('models', {
    providers: [
      {
        id: 'p',
        name: 'Fixture',
        protocol: 'openai-chat',
        baseUrl: 'https://fixture.invalid',
        credentialEnv: '',
      },
    ],
    models: [
      {
        id: 'v',
        providerId: 'p',
        displayName: 'Vision',
        apiModelName: 'fixture',
        capabilities: ['vision'],
      },
    ],
    routes: [{ role: 'vision', orderedModelIds: ['v'], timeoutMs: 1000 }],
  })
  const engine = new Engine(store)
  Object.defineProperty(engine, 'memes', { value: new MemeService(store, root) })
  const bytes = await sharp({ create: { width: 2, height: 2, channels: 3, background: '#e90' } })
    .png()
    .toBuffer()
  let vision = 0,
    reads = 0,
    sends = 0
  const completed = withFixturePlan(async (role: string, messages: ChatMessage[]) => {
    if (role === 'vision') {
      vision++
      assert.ok(messages[1]!.images?.length)
      await new Promise((r) => setTimeout(r, 20))
      return completion(undefined, {}, JSON.stringify(tags))
    }
    const input = JSON.parse(messages.find((item) => item.role === 'user')!.content)
    if (role === 'planner') {
      assert.match(JSON.stringify(input.imageContext), /KNOWN_PLAYFUL_CANARY/)
      return completion('participation.decide', { action: 'reply', reasonCode: 'fixture_sticker' })
    }
    assert.match(JSON.stringify(input.imageContext), /KNOWN_PLAYFUL_CANARY/)
    assert.match(JSON.stringify(input.imageContext), /stored_library_observation/)
    return completion('messages.send', {
      operationId: `known-${sends}`,
      segments: [
        { parts: [{ type: 'text', text: 'That can be a playful reaction; context matters.' }] },
      ],
    })
  })
  engine.router = () => ({ complete: completed }) as unknown as Router
  engine.runtime.addAdapter('fixture', {
    send: async () => {
      sends++
      return { status: 'sent', messageId: `out-${sends}` }
    },
    history: async () => ({ items: [], coverage: 'partial' }),
    getMessage: async () => ({ items: [], coverage: 'partial' }),
    readImage: async () => {
      reads++
      return bytes
    },
    dispose() {},
  })
  try {
    engine.runtime.ingest(message('first'), scene)
    await until(() => sends === 1)
    assert.equal(vision, 1, 'background intake and current turn share one analysis')
    assert.equal(reads, 1, 'background and turn share one actual adapter read')
    const first = engine.memes.adminList()[0]!
    assert.equal(first.knownOnly, true)
    assert.equal(first.available, false)
    assert.equal(first.shared, false)
    assert.deepEqual(engine.memes.search({ sceneId: scene.id, simulation: false }, ''), [])
    assert.throws(() => engine.memes.original({ sceneId: scene.id, simulation: false }, first.id))
    engine.runtime.ingest(message('different-url'), scene)
    await until(() => sends === 2)
    assert.equal(vision, 1)
    assert.equal(engine.memes.adminList().length, 1)
    assert.equal(engine.memes.adminList()[0]!.sources.length, 2)
    assert.equal(
      reads,
      2,
      'unknown changed URL is verified once, historical known reference does not redownload',
    )
    // Re-observation after enabling learning reuses semantics, not an implicit mass promotion.
    const enabled = { ...scene, learningEnabled: true }
    store.saveScene(enabled)
    engine.runtime.ingest(message('after-learning-on'), enabled)
    await until(() => sends === 3)
    assert.equal(vision, 1)
    assert.equal(engine.memes.adminList()[0]!.knownOnly, true)
    assert.equal(reads, 3)
    const sameReference = message('same-reference', 'mface', {
      url: 'https://fixture.invalid/first',
    })
    engine.runtime.ingest(sameReference, enabled)
    await until(() => sends === 4)
    assert.equal(
      reads,
      4,
      'a new source verifies a mutable URL even when its spelling is identical',
    )
    assert.equal(vision, 1)
    store.saveScene(scene)
    const app = createApp(store, { engine, token: 'fixture-token' })
    const response = await app.request('/api/memes?state=known', {
      headers: { Authorization: 'Bearer fixture-token' },
    })
    assert.equal(response.status, 200)
    const page = await response.json()
    assert.equal(page.total, 1)
    assert.equal(page.items[0].knownOnly, true)
    const change = (included: boolean, authenticated = true) =>
      app.request(`/api/memes/${first.id}/inclusion`, {
        method: 'POST',
        headers: {
          'Content-Type': 'application/json',
          ...(authenticated ? { Authorization: 'Bearer fixture-token' } : {}),
        },
        body: JSON.stringify({ included }),
      })
    assert.equal((await change(true, false)).status, 401)
    assert.equal((await change(true)).status, 200, 'manual inclusion works with learning OFF')
    assert.equal(engine.memes.adminGet(first.id)?.available, true)
    assert.equal(engine.memes.adminGet(first.id)?.shared, false)
    assert.equal(engine.memes.search({ sceneId: scene.id, simulation: false }, '').length, 1)
    assert.equal((await change(false)).status, 200)
    assert.equal(engine.memes.adminGet(first.id)?.knownOnly, true)
    assert.deepEqual(engine.memes.adminGet(first.id)?.tags.emotion, ['KNOWN_PLAYFUL_CANARY'])
    assert.equal(engine.memes.search({ sceneId: scene.id, simulation: false }, '').length, 0)
    assert.throws(() => engine.memes.original({ sceneId: scene.id, simulation: false }, first.id))
    assert.equal((await change(true)).status, 200, 'state can be changed back')
    const deletion = await app.request(`/api/memes/${first.id}`, {
      method: 'DELETE',
      headers: { Authorization: 'Bearer fixture-token' },
    })
    assert.equal(deletion.status, 200)
    assert.equal(engine.memes.adminGet(first.id), undefined)
    assert.equal(vision, 1)
  } finally {
    await engine.dispose()
    store.close()
    await rm(root, { recursive: true, force: true })
  }
})

test('without vision a sticker is still collected as known; ordinary unknown photos are not collected', async () => {
  const root = await mkdtemp(join(tmpdir(), 'he-known-no-vision-'))
  const store = new Store(':memory:')
  store.saveScene({ ...scene, participationEnabled: false })
  const engine = new Engine(store)
  Object.defineProperty(engine, 'memes', { value: new MemeService(store, root) })
  const bytes = await sharp({ create: { width: 1, height: 1, channels: 3, background: '#123' } })
    .png()
    .toBuffer()
  engine.runtime.addAdapter('fixture', {
    send: async () => ({ status: 'cancelled' }),
    history: async () => ({ items: [], coverage: 'partial' }),
    getMessage: async () => ({ items: [], coverage: 'partial' }),
    readImage: async () => bytes,
    dispose() {},
  })
  try {
    engine.runtime.ingest(message('photo', 'image', { summary: 'ordinary photo' }), scene, false)
    await new Promise((r) => setTimeout(r, 20))
    assert.equal(engine.memes.adminList().length, 0)
    engine.runtime.ingest(message('sticker'), scene, false)
    await until(() => engine.memes.adminList().length === 1)
    const asset = engine.memes.adminList()[0]!
    assert.equal(asset.knownOnly, true)
    assert.equal(asset.analyzed, false)
    assert.equal(asset.available, false)
  } finally {
    await engine.dispose()
    store.close()
    await rm(root, { recursive: true, force: true })
  }
})
