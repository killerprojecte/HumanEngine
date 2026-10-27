import assert from 'node:assert/strict'
import { mkdtemp, readFile, rm } from 'node:fs/promises'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import test from 'node:test'
import sharp from 'sharp'
import { MemeService, type MemeContext, type MemeTags } from '../src/memes.ts'
import { Store } from '../src/store.ts'
import { ModelError, type Router, type ChatMessage, type Completion } from '../src/models.ts'

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

test('finite bilingual retrieval uses original English tags, works in reverse, preserves filters', async () => {
  const { memes, store, close } = await setup()
  try {
    const c = ctx()
    const bytes = await sharp({ create: { width: 3, height: 3, channels: 3, background: 'red' } })
      .png()
      .toBuffer()
    const asset = await memes.import(c, bytes, source(c))
    const cases = [
      {
        emotion: ['happy', 'cheerful'],
        intent: ['celebrate'],
        queries: ['开心', '開心', 'celebrate', '庆祝'],
        misses: ['sad', '难过', 'confused', '困惑'],
      },
      {
        emotion: ['难过'],
        intent: [],
        queries: ['sad', 'unhappy', 'not happy'],
        misses: ['happy', '开心'],
      },
      { emotion: ['困惑'], intent: [], queries: ['confused', 'puzzled'], misses: ['开心', 'sad'] },
    ]
    for (const item of cases) {
      memes.update(asset.id, {
        tags: { ...tags, emotion: item.emotion, intent: item.intent },
        available: true,
      })
      for (const query of item.queries) {
        assert.equal(memes.search(c, query)[0]?.id, asset.id, query)
        assert.equal(memes.adminPage({ query, state: 'available' }).total, 1, query)
      }
      for (const query of item.misses) {
        assert.equal(memes.search(c, query).length, 0, query)
        assert.equal(memes.adminPage({ query, state: 'available' }).total, 0, query)
      }
    }
    assert.equal(memes.search(ctx('b'), 'confused').length, 0)
    memes.update(asset.id, { disabled: true })
    assert.equal(memes.search(c, 'confused').length, 0)
    memes.update(asset.id, { disabled: false, available: true })
    memes.recall('a', 'm1')
    assert.equal(memes.search(c, 'confused').length, 0)
    assert.equal(memes.adminPage({ query: 'confused', state: 'available' }).total, 0)
    assert.ok(store.recalled('a', 'm1'))
  } finally {
    await close()
  }
})

test('explicit transient vision failures retry boundedly and expose successful attempt count', async () => {
  const { memes, close } = await setup()
  try {
    const c = ctx(),
      asset = await memes.import(
        c,
        await sharp({ create: { width: 2, height: 2, channels: 3, background: 'blue' } })
          .png()
          .toBuffer(),
        source(c),
      )
    let calls = 0
    await memes.analyze(
      c,
      asset.id,
      router(() => {
        calls++
        if (calls < 3) throw new ModelError('provider_http_503', 503)
        return JSON.stringify(tags)
      }),
    )
    assert.equal(calls, 3)
    assert.equal(memes.adminGet(asset.id)?.analysisState, 'succeeded')
    assert.equal(memes.adminGet(asset.id)?.analysisAttempts, 3)
    assert.equal(memes.adminGet(asset.id)?.analysisError, undefined)
  } finally {
    await close()
  }
})

test('invalid JSON and uncertain timeouts never retry; explicit transient failures stop at three', async () => {
  const { memes, close } = await setup()
  try {
    const c = ctx(),
      asset = await memes.import(
        c,
        await sharp({ create: { width: 2, height: 2, channels: 3, background: 'green' } })
          .png()
          .toBuffer(),
        source(c),
      )
    for (const kind of ['json', 'timeout', '503']) {
      let calls = 0
      await assert.rejects(
        memes.analyze(
          c,
          asset.id,
          router(() => {
            calls++
            if (kind === 'json') return 'invalid'
            throw new ModelError(
              kind === '503' ? 'provider_http_503' : 'provider_timeout',
              kind === '503' ? 503 : undefined,
            )
          }),
        ),
        /analysis_failed/,
      )
      assert.equal(calls, kind === '503' ? 3 : 1)
      assert.equal(memes.adminGet(asset.id)?.analysisState, 'failed')
    }
  } finally {
    await close()
  }
})

test('recall cancels pending retry without resurrection; startup indexes existing tags without model replay', async () => {
  const { memes, store, close } = await setup()
  try {
    const c = ctx(),
      asset = await memes.import(
        c,
        await sharp({ create: { width: 2, height: 2, channels: 3, background: 'purple' } })
          .png()
          .toBuffer(),
        source(c),
      )
    let calls = 0
    const pending = memes.analyze(
      c,
      asset.id,
      router(() => {
        calls++
        throw new ModelError('provider_http_503', 503)
      }),
    )
    const rejected = assert.rejects(pending, /analysis_cancelled/)
    while (memes.adminGet(asset.id)?.analysisState !== 'retrying')
      await new Promise((resolve) => setTimeout(resolve, 5))
    memes.recall('a', 'm1')
    await rejected
    assert.equal(calls, 1)
    assert.equal(memes.adminGet(asset.id)?.disabled, true)
    assert.equal(memes.search(c, 'happy').length, 0)
    const original = memes.adminGet(asset.id)!
    store.db
      .prepare('UPDATE meme_assets SET value=? WHERE id=?')
      .run(JSON.stringify({ ...original, tags, analysisState: 'running' }), asset.id)
    store.db.exec('DELETE FROM meme_search_concepts')
    const restarted = new MemeService(store, memes.mediaRoot)
    assert.equal(restarted.adminGet(asset.id)?.analysisState, 'interrupted')
    assert.equal(restarted.adminPage({ query: '开心' }).total, 1)
    assert.equal(restarted.adminPage({ query: '开心', state: 'available' }).total, 0)
    assert.equal(calls, 1)
  } finally {
    await close()
  }
})

test('exact reported Capoo07 tags recover both original queries without editing stored tags', async () => {
  const { memes, store, close } = await setup()
  try {
    const fixture = JSON.parse(
      await readFile(new URL('./fixtures/capoo07-reported-tags.json', import.meta.url), 'utf8'),
    ) as { assetId: string; tags: MemeTags }
    const asset = {
      id: fixture.assetId,
      format: 'png',
      bytes: 9746,
      width: 259,
      height: 224,
      frames: 1,
      sources: [source(ctx())],
      tags: fixture.tags,
      analyzed: true,
      available: true,
      shared: true,
      privacyReviewed: true,
      favorite: false,
      disabled: false,
      preference: 0,
      usageCount: 0,
      createdAt: '2026-10-26T00:00:00.000Z',
    }
    // Seed reduced historical metadata, not the copyrighted image or original evidence DB.
    store.db.prepare('INSERT INTO meme_assets VALUES(?,?)').run(asset.id, JSON.stringify(asset))
    store.db.prepare('INSERT INTO meme_sources VALUES(?,?,?,?)').run(asset.id, 'a', 'm1', 'person')
    const restored = new MemeService(store, memes.mediaRoot)
    for (const query of [
      '开心',
      '开心庆祝达成小目标 祝贺 撒花',
      'happy celebration success congrats',
    ]) {
      assert.deepEqual(
        restored.search(ctx(), query).map((item) => item.id),
        [fixture.assetId],
        query,
      )
      assert.deepEqual(
        restored.adminPage({ query, state: 'available' }).items.map((item) => item.id),
        [fixture.assetId],
        query,
      )
    }
    for (const query of ['sad', '难过', 'confused', '困惑']) {
      assert.equal(restored.search(ctx(), query).length, 0, query)
      assert.equal(restored.adminPage({ query, state: 'available' }).total, 0, query)
    }
    assert.deepEqual(restored.adminGet(asset.id)?.tags, fixture.tags)
    restored.recall('a', 'm1')
    assert.equal(restored.search(ctx(), '开心').length, 0)
    assert.equal(restored.adminPage({ query: '开心', state: 'available' }).total, 0)
    assert.deepEqual(restored.adminGet(asset.id)?.tags, fixture.tags)
  } finally {
    await close()
  }
})
