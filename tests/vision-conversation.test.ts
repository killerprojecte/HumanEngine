import assert from 'node:assert/strict'
import test from 'node:test'
import sharp from 'sharp'
import { Runtime, type RuntimeMessage, type RuntimeAdapter } from '../src/runtime.ts'
import { Store } from '../src/store.ts'
import { describeCurrentImages, ImageDescriptionCache } from '../src/vision-context.ts'
import { fixtureReplyPlan, fixturePlanCompletion } from './helpers/reply-plan.ts'
import type { ChatMessage, Completion, Router } from '../src/models.ts'

function complete(name?: string, args: Record<string, unknown> = {}, text = ''): Completion {
  const toolCalls = name ? [{ id: name, name, arguments: args }] : []
  return {
    text,
    toolCalls,
    modelId: 'fixture',
    usage: { inputTokens: 0, outputTokens: 0, totalTokens: 0 },
    assistant: { role: 'assistant', content: text, toolCalls },
  }
}
async function until(check: () => boolean) {
  for (let i = 0; i < 300; i++) {
    if (check()) return
    await new Promise((r) => setTimeout(r, 5))
  }
  assert.fail('runtime did not settle')
}
for (const source of ['recent', 'history', 'quoted-history'] as const) {
  test(`${source} pixels reach planner and facts-free composer without live history replay`, async () => {
    const store = new Store(':memory:')
    const scene = {
      id: 'scene',
      kind: 'group' as const,
      adapterId: 'fixture',
      accountId: 'bot',
      platformSceneId: 'scene',
      name: 'Fixture',
      participationEnabled: true,
      proactiveEnabled: true,
      learningEnabled: false,
    }
    store.saveScene(scene)
    store.set('settings', { ...store.settings, debounceMs: 5, maxWaitMs: 100, cacheEnabled: true })
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
          id: 'fixture',
          displayName: 'Fixture',
          providerId: 'provider',
          apiModelName: 'fixture',
          capabilities: ['vision'],
        },
      ],
      routes: [{ role: 'vision', orderedModelIds: ['fixture'], timeoutMs: 1000 }],
    })
    const message = (id: string): RuntimeMessage => ({
      id,
      sceneId: scene.id,
      senderId: 'person',
      origin: 'live',
      status: 'available',
      sentAt: new Date().toISOString(),
      receivedAt: new Date().toISOString(),
      parts: [{ type: 'text', text: 'What does that expression mean?' }],
    })
    const old: RuntimeMessage = {
      ...message('old-picture'),
      origin: source === 'recent' ? 'live' : 'history',
      parts: [{ type: 'media', mediaType: 'image/png', assetId: 'private-pixel-reference' }],
    }
    const png = await sharp({ create: { width: 2, height: 2, channels: 3, background: '#36a' } })
      .png()
      .toBuffer()
    let visionCalls = 0,
      sends = 0,
      planningCalls = 0
    const participation: unknown[] = [],
      composition: unknown[] = [],
      plans: unknown[] = []
    const router = {
      complete: async (role: string, messages: ChatMessage[]) => {
        const data = JSON.parse(messages.find((item) => item.role === 'user')!.content)
        if (role === 'vision') {
          visionCalls++
          assert.match(messages[1]!.images![0]!, /^data:image\/png;base64,/)
          assert.match(messages[0]!.content, /possible common meme or sticker uses/)
          assert.match(messages[0]!.content, /does not prove the sender feels/)
          return complete(
            undefined,
            {},
            'VISIBLE: upturned mouth. POSSIBLE USE: playful amusement, not proof of sender intent.',
          )
        }
        if (role === 'planner') {
          if (fixtureReplyPlan(messages)) {
            plans.push(data)
            if (source === 'history' && ++planningCalls === 1)
              return complete('messages.around', {
                messageId: data.planContract.contextMessageIds.at(-1),
                limit: 10,
              })
            if (source === 'history') {
              const receipt = JSON.parse(messages.find((item) => item.role === 'tool')!.content)
              assert.match(JSON.stringify(receipt), /VISIBLE: upturned mouth/)
              assert.doesNotMatch(JSON.stringify(receipt), /private-pixel-reference/)
              return fixturePlanCompletion(role, messages, {
                contextMessageIds: [
                  ...data.planContract.contextMessageIds,
                  ...receipt.value.items.map((item: { id: string }) => item.id),
                ],
              })!
            }
            return fixturePlanCompletion(role, messages)!
          }
          participation.push(data)
          return complete('participation.decide', {
            action: 'reply',
            reasonCode: 'fixture_expression_question',
          })
        }
        composition.push(data)
        assert.deepEqual(data.plan.facts, [])
        assert.match(JSON.stringify(data.imageContext), /VISIBLE: upturned mouth/)
        assert.doesNotMatch(JSON.stringify(data), /private-pixel-reference/)
        return complete('messages.send', {
          operationId: `expression-${sends}`,
          segments: [
            {
              parts: [
                {
                  type: 'text',
                  text: 'It may convey playful amusement; the intended meaning depends on context.',
                },
              ],
            },
          ],
        })
      },
    } as unknown as Router
    const runtime = new Runtime(store, () => router)
    const adapter: RuntimeAdapter = {
      send: async () => {
        sends++
        return { status: 'sent', messageId: `out-${sends}` }
      },
      history: async () => ({
        items: [{ ...old, origin: 'history' as const }],
        coverage: 'partial',
      }),
      getMessage: async (_scene, id) => ({
        items: id === old.id ? [{ ...old, origin: 'history' as const }] : [],
        coverage: 'complete',
      }),
      readImage: async () => png,
      dispose() {},
    }
    runtime.addAdapter('fixture', adapter)
    const cache = new ImageDescriptionCache()
    runtime.addExtension({
      tools: [],
      execute: async () => ({}),
      describeImages: (ctx, items) =>
        describeCurrentImages(store, runtime, () => router, ctx, items, cache),
    })
    try {
      if (source === 'recent') {
        runtime.ingest(old, scene, false)
        const states = (
          runtime as unknown as { states: Map<string, { topics: Map<string, { lastAt: number }> }> }
        ).states
        for (const topic of states.get(scene.id)!.topics.values()) topic.lastAt -= 120001
      }
      const ask = message('question')
      if (source === 'quoted-history') ask.replyToId = old.id
      runtime.ingest(ask, scene)
      await until(() =>
        store.logs.some(
          (row) => row.stage === 'turn' && ['completed', 'failed'].includes(row.status ?? ''),
        ),
      )
      assert.equal(sends, 1)
      assert.equal(visionCalls, 1)
      assert.equal(composition.length, 1)
      if (source !== 'history')
        assert.match(JSON.stringify(participation), /VISIBLE: upturned mouth/)
      assert.equal(
        runtime.preview(scene.id).some((item) => item.id === old.id),
        source === 'recent',
      )
      if (source === 'recent') {
        runtime.ingest(message('followup'), scene)
        await until(() => sends === 2)
        assert.equal(
          visionCalls,
          1,
          'second text question reuses visual evidence, not question-specific intent',
        )
        assert.match(JSON.stringify(participation.at(-1)), /VISIBLE: upturned mouth/)
      }
    } finally {
      runtime.dispose()
      store.close()
    }
  })
}
