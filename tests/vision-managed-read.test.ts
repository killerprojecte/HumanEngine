import { describeCurrentImages } from '../src/vision-context.ts'
import type { Router, ChatMessage } from '../src/models.ts'
import assert from 'node:assert/strict'
import test from 'node:test'
import { createChatMediaFixture } from './fixtures/chat-media.ts'
import type { TurnContext } from '../src/runtime.ts'

test('current model reads managed bot image original; cache eligibility rejects disabled, missing, recalled and foreign assets', async () => {
  const f = await createChatMediaFixture()
  try {
    const { asset, outgoing } = await f.prepareManaged()
    f.store.saveScene({ ...f.store.scene(f.scene.id)!, participationEnabled: true })
    const context = (): TurnContext => ({
      scene: f.store.scene(f.scene.id)!,
      mode: 'real',
      turnId: 'managed',
      signal: new AbortController().signal,
      dependencies: new Set(),
      validations: [],
    })
    const runtime = f.engine.runtime
    assert.equal(
      runtime.imageSourceAvailable(context(), outgoing.id, asset.id),
      false,
      'proposed asset is not readable',
    )
    assert.equal(asset.knownOnly, true)
    f.engine.memes.adminSetIncluded(asset.id, true)
    const ctx = context()
    assert.equal(runtime.imageSourceAvailable(ctx, outgoing.id, asset.id), true)
    assert.deepEqual(await runtime.readImage(ctx, asset.id, outgoing), f.png)
    assert.equal(
      f.requests.some((call) => call.action === 'get_image' && call.params.file === asset.id),
      false,
      'internal hash is not sent to platform',
    )
    f.engine.memes.update(asset.id, { disabled: true })
    assert.equal(
      runtime.imageSourceAvailable(ctx, outgoing.id, asset.id),
      false,
      'cached visual evidence must be rejected after disable',
    )
    await assert.rejects(
      runtime.readImage(ctx, asset.id, outgoing),
      /image_context_unavailable|image_source_unavailable/,
    )
    f.engine.memes.update(asset.id, { disabled: false })
    f.engine.memes.adminSetIncluded(asset.id, true)
    const other = f.adapter.scene('group', '10')
    f.store.saveScene({ ...f.store.scene(f.scene.id)!, ...other })
    const foreign = { ...outgoing, id: f.adapter.messageId(other, '90'), sceneId: other.id }
    runtime.ingest(foreign, other, false)
    const foreignContext = { ...context(), scene: f.store.scene(other.id)! }
    assert.equal(runtime.imageSourceAvailable(foreignContext, foreign.id, asset.id), false)
    await assert.rejects(
      runtime.readImage(foreignContext, asset.id, foreign),
      /image_source_unavailable/,
    )
    runtime.recall(f.scene.id, outgoing.id)
    assert.equal(runtime.imageSourceAvailable(context(), outgoing.id, asset.id), false)
    const missing = {
      ...outgoing,
      id: f.adapter.messageId(f.scene, '91'),
      parts: [{ type: 'media' as const, mediaType: 'image/png', assetId: 'e'.repeat(64) }],
    }
    runtime.ingest(missing, f.scene, false)
    assert.equal(runtime.imageSourceAvailable(context(), missing.id, 'e'.repeat(64)), false)
  } finally {
    await f.close()
  }
})

test('unreadable managed images do not starve valid current user pixels or invalidate the turn', async () => {
  const f = await createChatMediaFixture()
  try {
    const { asset, outgoing } = await f.prepareManaged()
    f.store.saveScene({ ...f.store.scene(f.scene.id)!, participationEnabled: true })
    f.store.set('models', {
      providers: [
        {
          id: 'fixture-provider',
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
          providerId: 'fixture-provider',
          apiModelName: 'fixture',
          capabilities: ['vision'],
        },
      ],
      routes: [{ role: 'vision', orderedModelIds: ['fixture'], timeoutMs: 1000 }],
    })
    const missing = [1, 2, 3].map((index) => ({
      ...outgoing,
      id: f.adapter.messageId(f.scene, String(100 + index)),
      parts: [
        { type: 'media' as const, mediaType: 'image/png', assetId: String(index).repeat(64) },
      ],
    }))
    for (const message of missing) f.engine.runtime.ingest(message, f.scene, false)
    // This GIF has different bytes from the managed PNG; disabling that asset must
    // revoke its own original source, not this unrelated current image.
    for (const disabled of [false, true]) {
      if (disabled) f.engine.memes.update(asset.id, { disabled: true })
      const ctx: TurnContext = {
        scene: f.store.scene(f.scene.id)!,
        mode: 'real',
        turnId: `mixed-${disabled}`,
        signal: new AbortController().signal,
        dependencies: new Set(),
        validations: [],
        imagePriorityIds: [outgoing.id, ...missing.map((m) => m.id), f.messages[1]!.id],
      }
      let calls = 0
      const router = () =>
        ({
          complete: async (_role: string, input: ChatMessage[]) => {
            calls++
            assert.match(input[1]!.images![0]!, /^data:image\/png;base64,/)
            return {
              text: 'Visible synthetic colored frame',
              toolCalls: [],
              usage: { inputTokens: 1, outputTokens: 1, totalTokens: 2 },
              modelId: 'fixture',
              assistant: { role: 'assistant', content: 'Visible synthetic colored frame' },
            }
          },
        }) as unknown as Router
      const result = await describeCurrentImages(f.store, f.engine.runtime, router, ctx, [
        outgoing,
        ...missing,
        f.messages[1]!,
      ])
      assert.equal(
        result.images.find((image) => image.messageId === outgoing.id)?.status,
        'unavailable',
      )
      assert.equal(
        result.images.find((image) => image.messageId === f.messages[1]!.id)?.status,
        'described',
      )
      assert.equal(result.images.filter((image) => image.status === 'unavailable').length, 4)
      assert.equal(
        result.omitted.length,
        0,
        'unreadable managed references do not consume four-image budget',
      )
      assert.equal(calls, 1)
      assert.equal(
        ctx.validations.every((validate) => validate()),
        true,
      )
      assert.equal(
        ctx.dependencies.has(outgoing.id),
        false,
        'unreadable attachment must not poison unrelated turn',
      )
    }
  } finally {
    await f.close()
  }
})
