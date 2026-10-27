import assert from 'node:assert/strict'
import test from 'node:test'
import { mkdtemp, rm } from 'node:fs/promises'
import { join } from 'node:path'
import { tmpdir } from 'node:os'
import sharp from 'sharp'
import { Engine } from '../src/engine.ts'
import { Store } from '../src/store.ts'
import { MemeService } from '../src/memes.ts'
import type { RuntimeExtension, RuntimeMessage, TurnContext } from '../src/runtime.ts'
import type { Completion } from '../src/models.ts'

test('actual Engine feedback handler emits stored receipts and rejects conflicting replay or invalid sources', async () => {
  const root = await mkdtemp(join(tmpdir(), 'humanengine-feedback-receipt-'))
  const store = new Store(':memory:')
  store.set('settings', { ...store.settings, learningBatchMs: 300000 })
  const scene = {
    id: 'scene',
    kind: 'group' as const,
    adapterId: 'local',
    accountId: 'bot',
    platformSceneId: 'group',
    name: 'Fixture',
    participationEnabled: false,
    learningEnabled: true,
    proactiveEnabled: false,
  }
  store.saveScene(scene)
  const engine = new Engine(store)
  await engine.memes.dispose()
  ;(engine as unknown as { memes: MemeService }).memes = new MemeService(store, root)
  const context: TurnContext = {
    scene,
    mode: 'real',
    turnId: 'fixture',
    signal: new AbortController().signal,
    dependencies: new Set(),
    validations: [],
  }
  const source = { sceneId: scene.id, messageId: 'asset-source', senderId: 'alice' }
  const assetContext = {
    sceneId: scene.id,
    simulation: false,
    sourceEvidence: [{ ...source, available: true, isBot: false }],
  }
  const tags = {
    appearance: [],
    style: ['cartoon'],
    subject: ['square'],
    emotion: ['happy'],
    intent: ['celebrate'],
    tone: [],
    intensity: [],
    memeMeaning: [],
    ocr: '',
  }
  const receipt = (id: string): RuntimeMessage => ({
    id,
    sceneId: scene.id,
    senderId: 'alice',
    origin: 'live',
    status: 'available',
    sentAt: new Date().toISOString(),
    receivedAt: new Date().toISOString(),
    parts: [{ type: 'text', text: id === 'negative' ? '这张我不喜欢。' : '这张还不错。' }],
  })
  try {
    const bytes = await sharp({
      create: { width: 2, height: 2, channels: 3, background: '#55aaff' },
    })
      .png()
      .toBuffer()
    const asset = await engine.memes.import(assetContext, bytes, source)
    await engine.memes.analyze(assetContext, asset.id, {
      complete: async (): Promise<Completion> => ({
        text: JSON.stringify(tags),
        toolCalls: [],
        modelId: 'local-fixture',
        assistant: { role: 'assistant', content: JSON.stringify(tags) },
        usage: { inputTokens: 0, outputTokens: 0, totalTokens: 0 },
      }),
    })
    engine.memes.update(asset.id, { available: true })
    engine.runtime.ingest(receipt('negative'), scene, false)
    const extension = (
      engine.runtime as unknown as { extensions: RuntimeExtension[] }
    ).extensions.find((item) => item.tools.some((tool) => tool.name === 'memes.feedback'))!
    const invoke = (sourceMessageId: string, positive: boolean) =>
      extension.execute('memes.feedback', { assetId: asset.id, sourceMessageId, positive }, context)
    const negative = await invoke('negative', false)
    assert.deepEqual(negative, {
      ok: true,
      actionTaken: 'ranking_adjusted',
      assetId: asset.id,
      scope: 'asset',
      sourceMessageId: 'negative',
      positive: false,
      preference: -0.2,
      disabled: false,
    })
    assert.deepEqual(await invoke('negative', false), negative)
    assert.deepEqual(await invoke('negative', true), {
      status: 'rejected',
      reason: 'feedback_direction_conflict',
    })
    assert.equal(engine.memes.adminGet(asset.id)!.preference, -0.2)
    engine.runtime.ingest(receipt('positive'), scene, false)
    assert.deepEqual(await invoke('positive', true), {
      ok: true,
      actionTaken: 'ranking_adjusted',
      assetId: asset.id,
      scope: 'asset',
      sourceMessageId: 'positive',
      positive: true,
      preference: 0,
      disabled: false,
    })
    await assert.rejects(invoke('missing', false), /invalid_source_evidence/)
    engine.memes.recall(scene.id, 'negative')
    await assert.rejects(invoke('negative', false), /invalid_source_evidence/)
    assert.equal(engine.habits.adminList().length, 0)
  } finally {
    await engine.dispose()
    store.close()
    await rm(root, { recursive: true, force: true })
  }
})
