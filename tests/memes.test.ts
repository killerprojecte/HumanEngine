import assert from 'node:assert/strict'
import { mkdtemp, readFile, rm } from 'node:fs/promises'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import test from 'node:test'
import sharp from 'sharp'
import { MemeService, type MemeContext, type MemeTags } from '../src/memes.ts'
import { Store } from '../src/store.ts'
import type { Router, ChatMessage, Completion } from '../src/models.ts'

const tags: MemeTags = {
  appearance: ['cute'],
  style: ['cartoon'],
  subject: ['square'],
  emotion: ['happy'],
  intent: ['greeting'],
  tone: ['gentle'],
  intensity: ['low'],
  memeMeaning: ['welcome'],
  ocr: '',
}
const ctx = (sceneId = 'a', messageId = 'm1', senderId = 'person'): MemeContext => ({
  sceneId,
  simulation: false,
  sourceEvidence: [{ sceneId, messageId, senderId, available: true, isBot: false }],
})
const source = (context: MemeContext) => {
  const { available: _available, isBot: _isBot, ...result } = context.sourceEvidence![0]!
  return result
}
const completion = (text: string): Completion => ({
  text,
  toolCalls: [],
  usage: { inputTokens: 1, outputTokens: 1, totalTokens: 2 },
  modelId: 'fixture',
  assistant: { role: 'assistant', content: text },
})
function router(
  fn: (messages: ChatMessage[]) => Promise<string> | string,
): Pick<Router, 'complete'> {
  return { complete: async (_role, messages) => completion(await fn(messages)) }
}
async function setup() {
  const root = await mkdtemp(join(tmpdir(), 'humanengine-memes-'))
  const store = new Store(':memory:')
  for (const id of ['a', 'b'])
    store.saveScene({
      id,
      kind: 'group',
      adapterId: 'snow',
      accountId: '42',
      platformSceneId: id,
      name: id,
      participationEnabled: true,
      learningEnabled: true,
      proactiveEnabled: false,
    })
  return {
    store,
    memes: new MemeService(store, root),
    close: async () => {
      store.close()
      await rm(root, { recursive: true, force: true })
    },
  }
}
async function animated(format: 'gif' | 'webp') {
  const pixels = Buffer.concat([
    Buffer.from(Array(4).fill([255, 0, 0]).flat()),
    Buffer.from(Array(4).fill([0, 0, 255]).flat()),
  ])
  return sharp(pixels, { raw: { width: 2, height: 4, channels: 3, pageHeight: 2 } })
    .toFormat(format, { loop: 0, delay: [100, 200] })
    .toBuffer()
}
const snapshot = (store: Store) =>
  JSON.stringify(
    ['meme_assets', 'meme_sources', 'meme_feedback'].map((table) =>
      store.db.prepare(`SELECT * FROM ${table}`).all(),
    ),
  )

test('GIF and animated WebP keep original bytes and analyze distinct ordered frames once', async () => {
  const { memes, close } = await setup()
  try {
    for (const format of ['gif', 'webp'] as const) {
      const bytes = await animated(format)
      const context = ctx('a', format)
      const asset = await memes.import(context, bytes, source(context))
      assert.equal(asset.frames, 2)
      let calls = 0
      const model = router((messages) => {
        calls++
        assert.equal(messages[1]?.images?.length, 2)
        assert.notEqual(messages[1]?.images?.[0], messages[1]?.images?.[1])
        assert.match(messages[1]!.content, /0, 1 out of 2 frames/)
        return JSON.stringify(tags)
      })
      await memes.analyze(context, asset.id, model)
      await memes.analyze(context, asset.id, model)
      assert.equal(calls, 1)
      memes.update(asset.id, { available: true })
      assert.deepEqual(await readFile(memes.original(context, asset.id).path), bytes)
      assert.equal(memes.search(context, 'greeting').length >= 1, true)
      assert.equal(calls, 1)
    }
  } finally {
    await close()
  }
})

test('Hash dedup retains scoped sources, sharing requires review, and simulation never mutates', async () => {
  const { store, memes, close } = await setup()
  try {
    const bytes = await animated('gif'),
      a = ctx('a'),
      b = ctx('b')
    const first = await memes.import(a, bytes, source(a))
    await memes.analyze(
      a,
      first.id,
      router(() => JSON.stringify(tags)),
    )
    memes.update(first.id, { available: true })
    assert.equal(memes.search(b, '').length, 0)
    assert.throws(() => memes.update(first.id, { shared: true }), /public_media_review_required/)
    const second = await memes.import(b, bytes, source(b))
    assert.equal(first.id, second.id)
    assert.equal(memes.adminList().length, 1)
    assert.equal(memes.adminList()[0]?.sources.length, 2)
    memes.update(first.id, { shared: true, privacyReviewed: true })
    const simulation = { sceneId: 'simulation', simulation: true }
    const before = snapshot(store)
    assert.deepEqual(memes.search(simulation, '')[0]?.sources, [])
    memes.original(simulation, first.id)
    await assert.rejects(memes.import(simulation, bytes, source(a)), /simulation_does_not_learn/)
    await assert.rejects(
      memes.analyze(
        simulation,
        first.id,
        router(() => '{}'),
      ),
      /simulation_does_not_learn/,
    )
    assert.throws(() => memes.recordUsage(simulation, first.id), /simulation_does_not_learn/)
    assert.throws(
      () => memes.recordFeedback(simulation, first.id, { source: source(a), positive: true }),
      /simulation_does_not_learn/,
    )
    assert.equal(snapshot(store), before)
    memes.recall('a', 'm1')
    assert.equal(memes.search(simulation, '').length, 1)
    memes.recall('b', 'm1')
    assert.equal(memes.search(simulation, '').length, 0)
  } finally {
    await close()
  }
})

test('Analysis failure and recall race cannot publish a candidate or revive recalled media', async () => {
  const { memes, close } = await setup()
  try {
    const context = ctx(),
      bytes = await animated('webp')
    const asset = await memes.import(context, bytes, source(context))
    await assert.rejects(
      memes.analyze(
        context,
        asset.id,
        router(() => 'not JSON'),
      ),
      /analysis_failed/,
    )
    assert.equal(memes.adminList()[0]?.analysisError, 'analysis_failed')
    assert.throws(() => memes.update(asset.id, { available: true }), /meme_not_available/)
    let release: (text: string) => void = () => {}
    let started: () => void = () => {}
    const start = new Promise<void>((resolve) => {
      started = resolve
    })
    const pending = memes.analyze(
      context,
      asset.id,
      router(() => {
        started()
        return new Promise((resolve) => {
          release = resolve
        })
      }),
    )
    await start
    memes.recall('a', 'm1')
    release(JSON.stringify(tags))
    await assert.rejects(pending, /analysis_cancelled/)
    assert.equal(memes.adminList()[0]?.analyzed, false)
    assert.equal(memes.adminList()[0]?.disabled, true)
    await assert.rejects(memes.import(context, bytes, source(context)), /invalid_source_evidence/)
    await assert.rejects(
      memes.import(ctx('a', 'other'), Buffer.from('<svg></svg>'), source(ctx('a', 'other'))),
      /unsupported_image_format/,
    )
  } finally {
    await close()
  }
})

test('Explicit feedback changes shared taste once; own usage and bot evidence do not', async () => {
  const { memes, close } = await setup()
  try {
    const context = ctx(),
      asset = await memes.import(context, await animated('gif'), source(context))
    await memes.analyze(
      context,
      asset.id,
      router(() => JSON.stringify(tags)),
    )
    memes.update(asset.id, { available: true })
    memes.recordUsage(context, asset.id)
    assert.equal(memes.adminList()[0]?.preference, 0)
    const feedback = ctx('a', 'feedback')
    memes.recordFeedback(feedback, asset.id, { source: source(feedback), positive: true })
    memes.recordFeedback(feedback, asset.id, { source: source(feedback), positive: true })
    assert.equal(memes.adminList()[0]?.preference, 0.2)
    const bot = ctx('a', 'bot', 'bot')
    bot.sourceEvidence![0]!.isBot = true
    assert.throws(
      () => memes.recordFeedback(bot, asset.id, { source: source(bot), positive: true }),
      /invalid_source_evidence/,
    )
    memes.recall('a', 'feedback')
    assert.equal(memes.adminList()[0]?.preference, 0)
    assert.equal(memes.adminList()[0]?.available, true)
  } finally {
    await close()
  }
})
