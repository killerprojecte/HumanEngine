import assert from 'node:assert/strict'
import test from 'node:test'
import { mkdtemp, readFile, rm } from 'node:fs/promises'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import sharp from 'sharp'
import { MemeService } from '../src/memes.ts'
import type { SendSegment } from '../src/onebot.ts'
import { Engine } from '../src/engine.ts'
import { Store } from '../src/store.ts'
import type { ChatMessage, Completion, Router, ToolDefinition } from '../src/models.ts'

const completed = (name: string, args: Record<string, unknown>): Completion => {
  const toolCalls = [{ id: name, name, arguments: args }]
  return {
    text: '',
    toolCalls,
    modelId: 'fixture',
    usage: { inputTokens: 0, outputTokens: 0, totalTokens: 0 },
    assistant: { role: 'assistant', content: '', toolCalls },
  }
}

test('ordinary conversation can choose meaningful text after an empty meme search without sending a placeholder', async () => {
  const store = new Store(':memory:')
  const scene = {
    id: 's',
    name: 's',
    kind: 'group' as const,
    adapterId: 'fixture',
    accountId: 'bot',
    platformSceneId: 's',
    participationEnabled: true,
    learningEnabled: false,
    proactiveEnabled: true,
  }
  store.saveScene(scene)
  const engine = new Engine(store)
  let searched = false,
    sent = 0
  engine.runtime.addAdapter('fixture', {
    send: async () => {
      sent++
      return { status: 'sent', messageId: 'delivered' }
    },
    history: async () => ({ items: [], coverage: 'partial' }),
    getMessage: async () => ({ items: [], coverage: 'partial' }),
    dispose() {},
  })
  const complete = async (role: string, messages: ChatMessage[], tools: ToolDefinition[]) => {
    const input = JSON.parse(messages.find((m) => m.role === 'user')!.content)
    if (tools.some((t) => t.name === 'participation.decide'))
      return completed('participation.decide', {
        action: 'reply',
        reasonCode: 'fixture',
        targetMessageIds: ['hello'],
      })
    if (role === 'planner') {
      const system = messages
        .filter((m) => m.role === 'system')
        .map((m) => m.content)
        .join('\n')
      assert.match(
        system,
        /proactively use memes.search even if the user did not explicitly request an image/,
      )
      assert.match(system, /Do not force a meme into every reply/)
      assert.ok(tools.some((t) => t.name === 'memes.search'))
      if (!searched) {
        searched = true
        return completed('memes.search', { query: '开心' })
      }
      const receipt = JSON.parse(messages.find((m) => m.role === 'tool')!.content)
      assert.deepEqual(receipt.value.items, [])
      return completed('reply.plan', {
        action: 'reply',
        deliveryTool: 'messages.send',
        ...input.planContract,
        strategy: 'Respond warmly without inventing an unavailable image.',
        constraints: [],
        facts: [],
        assets: [],
        emoji: [],
        expressionGoals: ['Celebrate naturally.'],
        memorySelections: [],
        actions: [],
      })
    }
    assert.equal(role, 'replyer')
    assert.deepEqual(input.plan.assets, [])
    assert.equal(
      tools.some((t) => t.name === 'memes.search'),
      false,
    )
    return completed('messages.send', {
      operationId: 'celebrate',
      segments: [{ replyToId: 'hello', parts: [{ type: 'text', text: '哈哈，太好了！' }] }],
    })
  }
  engine.router = () => ({ complete }) as Router
  try {
    const at = new Date().toISOString()
    engine.runtime.ingest(
      {
        id: 'hello',
        sceneId: scene.id,
        senderId: 'person',
        origin: 'live',
        status: 'available',
        sentAt: at,
        receivedAt: at,
        parts: [{ type: 'text', text: '终于搞定了，哈哈！' }],
      },
      scene,
      false,
    )
    await engine.runtime.proactive(scene.id)
    assert.equal(searched, true)
    assert.equal(sent, 1)
  } finally {
    await engine.dispose()
    store.close()
  }
})

// All model choices below are deterministic fixtures, not a claim about a live model.
for (const choice of [
  'media',
  'text',
  'noReply',
  'placeholderRepair',
  'revokedSelected',
  'hookReplaced',
] as const)
  test(`meme planning loop can query twice then choose ${choice} without inventing media`, async () => {
    const root = await mkdtemp(join(tmpdir(), 'he-meme-loop-'))
    const store = new Store(':memory:')
    const scene = {
      id: 'loop',
      name: 'loop',
      kind: 'group' as const,
      adapterId: 'fixture',
      accountId: 'bot',
      platformSceneId: 'loop',
      participationEnabled: true,
      learningEnabled: true,
      proactiveEnabled: true,
    }
    store.saveScene(scene)
    store.saveScene({ ...scene, id: 'foreign', platformSceneId: 'foreign' })
    const engine = new Engine(store)
    const memes = new MemeService(store, root)
    Object.defineProperty(engine, 'memes', { value: memes })
    const mediaChoice =
      choice === 'media' || choice === 'revokedSelected' || choice === 'hookReplaced'
    const sent: SendSegment[] = []
    let queries = 0,
      replyerCalls = 0
    try {
      const bytes = await sharp({
        create: { width: 2, height: 2, channels: 3, background: 'orange' },
      })
        .png()
        .toBuffer()
      const context = {
        sceneId: scene.id,
        simulation: false,
        sourceEvidence: [
          { sceneId: scene.id, messageId: 'source', senderId: 'u', available: true, isBot: false },
        ],
      }
      const asset = await memes.import(context, bytes, {
        sceneId: scene.id,
        messageId: 'source',
        senderId: 'u',
      })
      memes.update(asset.id, {
        available: true,
        tags: {
          appearance: ['colored square'],
          style: ['cartoon'],
          subject: [],
          emotion: ['happy'],
          intent: ['celebrate'],
          tone: ['playful'],
          intensity: ['low'],
          memeMeaning: ['congratulations'],
          ocr: '',
        },
      })
      let finalAsset = asset,
        finalBytes = bytes
      if (choice === 'hookReplaced') {
        finalBytes = await sharp({
          create: { width: 2, height: 2, channels: 3, background: 'blue' },
        })
          .png()
          .toBuffer()
        finalAsset = await memes.import(context, finalBytes, {
          sceneId: scene.id,
          messageId: 'source',
          senderId: 'u',
        })
        memes.update(finalAsset.id, {
          available: true,
          tags: {
            appearance: ['blue square'],
            style: ['cartoon'],
            subject: [],
            emotion: ['happy'],
            intent: ['celebrate'],
            tone: ['playful'],
            intensity: ['low'],
            memeMeaning: ['congratulations'],
            ocr: '',
          },
        })
      }
      assert.equal(
        memes.search({ sceneId: 'foreign', simulation: false }, 'celebrate').length,
        0,
        'seed is scene-owned, not globally shared',
      )
      const active = { ...scene, learningEnabled: false }
      store.saveScene(active)
      engine.runtime.addAdapter('fixture', {
        send: async (_scene, segment) => {
          for (const part of segment.parts)
            if (part.type === 'media') {
              assert.equal(part.assetId, finalAsset.id)
              const resolved = memes.original(
                { sceneId: scene.id, simulation: false },
                part.assetId,
              )
              assert.deepEqual(await readFile(resolved.path), finalBytes)
            }
          sent.push(structuredClone(segment))
          return { status: 'sent', messageId: `delivered-${sent.length}` }
        },
        history: async () => ({ items: [], coverage: 'partial' }),
        getMessage: async () => ({ items: [], coverage: 'partial' }),
        dispose() {},
      })
      const complete = async (role: string, messages: ChatMessage[], tools: ToolDefinition[]) => {
        const input = JSON.parse(messages.find((message) => message.role === 'user')!.content)
        const receipts = messages
          .filter((message) => message.role === 'tool')
          .map((message) => JSON.parse(message.content))
        if (tools.some((tool) => tool.name === 'participation.decide'))
          return completed('participation.decide', {
            action: 'reply',
            reasonCode: 'fixture',
            targetMessageIds: ['hello'],
          })
        if (role === 'planner') {
          const system = messages
            .filter((message) => message.role === 'system')
            .map((message) => message.content)
            .join('\n')
          assert.match(system, /may call memes.search multiple times/)
          assert.match(system, /independent meaningful text, or noReply/)
          assert.ok(tools.some((tool) => tool.name === 'memes.search'))
          if (!receipts.length) {
            queries++
            return completed('memes.search', { query: 'unmatched-first-query' })
          }
          assert.deepEqual(receipts[0].value.items, [])
          if (receipts.length === 1) {
            queries++
            return completed('memes.search', {
              query: mediaChoice || choice === 'text' ? 'celebrate' : 'unmatched-second-query',
            })
          }
          assert.equal(receipts.length, 2)
          const candidates = receipts[1].value.items
          if (mediaChoice || choice === 'text') {
            assert.equal(candidates.length, choice === 'hookReplaced' ? 2 : 1)
            assert.equal(typeof candidates[0].id, 'string')
            assert.deepEqual(candidates[0].tags.emotion, ['happy'])
            assert.deepEqual(candidates[0].tags.intent, ['celebrate'])
            assert.deepEqual(candidates[0].tags.memeMeaning, ['congratulations'])
          } else assert.deepEqual(candidates, [])
          if (choice === 'text') memes.update(asset.id, { disabled: true })
          return completed('reply.plan', {
            action: choice === 'noReply' ? 'noReply' : 'reply',
            deliveryTool: 'messages.send',
            ...input.planContract,
            strategy: mediaChoice
              ? 'Let the selected celebration meme express congratulations on its own.'
              : choice === 'noReply'
                ? 'No fitting image and no independent response needed.'
                : 'Express congratulations directly in words, not as an image description.',
            constraints: [],
            facts: [],
            assets: mediaChoice
              ? (choice === 'hookReplaced' ? candidates : candidates.slice(0, 1)).map(
                  (candidate: { id: string }) => ({
                    assetId: candidate.id,
                    evidenceId: receipts[1].id,
                  }),
                )
              : [],
            emoji: [],
            memorySelections: [],
            expressionGoals: [],
            actions: [],
          })
        }
        assert.equal(role, 'replyer')
        replyerCalls++
        const system = messages
          .filter((message) => message.role === 'system')
          .map((message) => message.content)
          .join('\n')
        assert.match(system, /a media-only reply is valid/)
        assert.equal(
          tools.some((tool) => tool.name === 'memes.search'),
          false,
        )
        if (choice === 'placeholderRepair' && replyerCalls === 1)
          return completed('messages.send', {
            operationId: 'placeholder',
            segments: [{ replyToId: 'hello', parts: [{ type: 'text', text: '[表情包：庆祝]' }] }],
          })
        if (choice === 'placeholderRepair') {
          assert.equal(sent.length, 0)
          assert.match(JSON.stringify(receipts), /meme_placeholder_not_media/)
        }
        return completed('messages.send', {
          operationId: 'expression',
          segments: [
            {
              replyToId: 'hello',
              parts: mediaChoice
                ? [{ type: 'media', assetId: input.plan.assets[0].assetId, mediaType: 'image' }]
                : [{ type: 'text', text: '不用表情包也想说一句：太好了！' }],
            },
          ],
        })
      }
      if (choice === 'hookReplaced')
        engine.runtime.beforeSend({
          id: 'fixture-replace-planned-media',
          priority: 0,
          handler: async (segments) => {
            memes.update(asset.id, { disabled: true })
            return {
              action: 'continue',
              segments: segments.map((segment) => ({
                ...segment,
                parts: segment.parts.map((part) =>
                  part.type === 'media' ? { ...part, assetId: finalAsset.id } : part,
                ),
              })),
            }
          },
        })
      if (choice === 'revokedSelected')
        engine.runtime.beforeSend({
          id: 'fixture-revoke-selected',
          priority: 0,
          handler: async (segments) => {
            memes.update(asset.id, { disabled: true })
            return { action: 'continue', segments }
          },
        })
      engine.router = () => ({ complete }) as Router
      const at = new Date().toISOString()
      engine.runtime.ingest(
        {
          id: 'hello',
          sceneId: scene.id,
          senderId: 'person',
          origin: 'live',
          status: 'available',
          sentAt: at,
          receivedAt: at,
          parts: [{ type: 'text', text: '终于搞定了，哈哈！' }],
        },
        active,
        false,
      )
      await engine.runtime.proactive(scene.id)
      assert.equal(queries, 2)
      assert.equal(sent.length, choice === 'noReply' || choice === 'revokedSelected' ? 0 : 1)
      assert.equal(replyerCalls, choice === 'noReply' ? 0 : choice === 'placeholderRepair' ? 2 : 1)
      if (choice === 'media' || choice === 'hookReplaced')
        assert.deepEqual(sent[0]!.parts, [
          { type: 'media', assetId: finalAsset.id, mediaType: 'image' },
        ])
      if (choice === 'text' || choice === 'placeholderRepair')
        assert.deepEqual(sent[0]!.parts, [{ type: 'text', text: '不用表情包也想说一句：太好了！' }])
    } finally {
      await engine.dispose()
      store.close()
      await rm(root, { recursive: true, force: true })
    }
  })
