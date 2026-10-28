import assert from 'node:assert/strict'
import { createServer } from 'node:http'
import { once } from 'node:events'
import test from 'node:test'
import sharp from 'sharp'
import { WebSocketServer } from 'ws'
import { Engine } from '../src/engine.ts'
import { Store } from '../src/store.ts'
import { OneBotAdapter } from '../src/onebot.ts'

for (const visionEnabled of [true, false]) {
  test(
    `real SnowLuma incoming image reaches current turn with learning disabled (vision route ${visionEnabled ? 'configured' : 'missing'})`,
    { timeout: 15000 },
    async () => {
      const bytes = await sharp({
        create: { width: 2, height: 2, channels: 3, background: '#ff0000' },
      })
        .png()
        .toBuffer()
      const requests: Array<{
        model: string
        messages: Array<{ role: string; content: unknown }>
      }> = []
      const actions: Array<{ action: string; params: Record<string, unknown> }> = []
      let thinkerCalls = 0
      const tool = (name: string, args: unknown) => ({
        id: `call-${name}`,
        type: 'function',
        function: { name: name.replaceAll('.', '__'), arguments: JSON.stringify(args) },
      })
      const modelServer = createServer(async (req, res) => {
        const chunks: Buffer[] = []
        for await (const chunk of req) chunks.push(Buffer.from(chunk))
        const body = JSON.parse(Buffer.concat(chunks).toString())
        requests.push(body)
        const calls =
          body.model === 'planner'
            ? [
                tool('participation.decide', {
                  action: 'reply',
                  reasonCode: 'fixture_image_question',
                }),
              ]
            : body.model === 'thinker' && ++thinkerCalls === 1
              ? [
                  tool('messages.send', {
                    operationId: 'image-reply',
                    segments: [
                      {
                        parts: [
                          {
                            type: 'text',
                            text: visionEnabled
                              ? 'The image shows a red square.'
                              : 'Image viewing is unavailable because no vision model is configured.',
                          },
                        ],
                      },
                    ],
                  }),
                ]
              : []
        res.setHeader('content-type', 'application/json')
        res.end(
          JSON.stringify({
            choices: [
              {
                finish_reason: calls.length ? 'tool_calls' : 'stop',
                message: {
                  role: 'assistant',
                  content:
                    body.model === 'vision' ? 'VISIBLE_RED_SQUARE_CANARY: a red square.' : '',
                  ...(calls.length ? { tool_calls: calls } : {}),
                },
              },
            ],
            usage: { prompt_tokens: 10, completion_tokens: 10, total_tokens: 20 },
          }),
        )
      })
      modelServer.listen(0, '127.0.0.1')
      await once(modelServer, 'listening')
      const modelAddress = modelServer.address()
      assert.ok(modelAddress && typeof modelAddress !== 'string')
      const wsServer = new WebSocketServer({ host: '127.0.0.1', port: 0 })
      let authorization: string | undefined
      wsServer.on('connection', (socket, req) => {
        authorization = req.headers.authorization
        socket.on('message', (raw) => {
          const call = JSON.parse(raw.toString())
          actions.push(call)
          const data =
            call.action === 'get_login_info'
              ? { user_id: 42 }
              : call.action === 'get_image'
                ? { file: `base64://${bytes.toString('base64')}` }
                : call.action === 'send_group_msg'
                  ? { message_id: 99 }
                  : {}
          socket.send(JSON.stringify({ status: 'ok', retcode: 0, echo: call.echo, data }))
        })
      })
      await once(wsServer, 'listening')
      const wsAddress = wsServer.address()
      assert.ok(wsAddress && typeof wsAddress !== 'string')
      const store = new Store(':memory:')
      const engine = new Engine(store)
      const adapter = new OneBotAdapter(
        {
          id: 'snowluma',
          accountId: '42',
          url: `ws://127.0.0.1:${wsAddress.port}`,
          token: 'fixture-adapter-canary',
        },
        { onMessage: (message, scene) => engine.runtime.ingest(message, scene) },
      )
      const scene = adapter.scene('group', '9')
      const sourceId = adapter.messageId(scene, '1')
      store.saveScene({
        ...scene,
        name: 'Fixture group',
        participationEnabled: true,
        learningEnabled: false,
        proactiveEnabled: false,
      })
      store.set('settings', { ...store.settings, debounceMs: 0, maxWaitMs: 100 })
      const roles = visionEnabled
        ? (['planner', 'thinker', 'vision'] as const)
        : (['planner', 'thinker'] as const)
      store.saveModels({
        providers: [
          {
            id: 'fixture',
            name: 'Fixture',
            protocol: 'openai-chat',
            credentialEnv: '',
            baseUrl: `http://127.0.0.1:${modelAddress.port}/v1`,
          },
        ],
        models: roles.map((role) => ({
          id: role,
          displayName: role,
          providerId: 'fixture',
          apiModelName: role,
          embeddingBatchSize: 4,
          capabilities: role === 'vision' ? ['text', 'vision'] : ['text', 'tools'],
        })),
        routes: roles.map((role) => ({ role, orderedModelIds: [role], timeoutMs: 5000 })),
      })
      engine.runtime.addAdapter('snowluma', adapter, adapter.userId('42'))
      try {
        await adapter.connect()
        assert.equal(authorization, 'Bearer fixture-adapter-canary')
        const peer = [...wsServer.clients][0]!
        peer.send(
          JSON.stringify({
            post_type: 'message',
            self_id: 42,
            message_type: 'group',
            group_id: 9,
            user_id: 7,
            message_id: 1,
            time: Math.floor(Date.now() / 1000),
            message: [
              { type: 'text', data: { text: 'What is in this image?' } },
              { type: 'image', data: { file: 'opaque-image-token-canary' } },
            ],
          }),
        )
        const deadline = Date.now() + 10000
        while (
          !store.logs.some(
            (log) =>
              log.stage === 'turn' && log.status !== 'started' && log.status !== 'cancel_requested',
          )
        ) {
          if (Date.now() > deadline) assert.fail('whole image turn did not finish')
          await new Promise((resolve) => setTimeout(resolve, 10))
        }
        const thinker = requests.find((request) => request.model === 'thinker')
        assert.ok(thinker, 'current thinker must run')
        const thinkerText = JSON.stringify(thinker.messages)
        const thinkerContext = JSON.parse(
          thinker.messages.find((message) => message.role === 'user')!.content as string,
        )
        const attributed = thinkerContext.learnedData.find(
          (item: { trust?: string }) => item.trust === 'untrusted_image_descriptions',
        )
        assert.equal(attributed.images.length, 1)
        assert.equal(attributed.images[0].messageId, sourceId)
        assert.equal(attributed.images[0].status, visionEnabled ? 'described' : 'unavailable')

        assert.match(thinkerText, /untrusted_image_descriptions/)
        assert.ok(thinkerText.includes(sourceId), 'visual result must carry its source message ID')
        const vision = requests.filter((request) => request.model === 'vision')
        if (visionEnabled) {
          assert.equal(vision.length, 1)
          const user = vision[0]!.messages.find((message) => message.role === 'user')!
          assert.ok(Array.isArray(user.content))
          const imagePart = user.content.find((part: { type: string }) => part.type === 'image_url')
          assert.match(imagePart.image_url.url, /^data:image\/png;base64,/)
          assert.equal(
            (await sharp(Buffer.from(imagePart.image_url.url.split(',')[1], 'base64')).metadata())
              .format,
            'png',
          )
          assert.match(thinkerText, /VISIBLE_RED_SQUARE_CANARY/)
          assert.equal(actions.filter((action) => action.action === 'get_image').length, 1)
          assert.deepEqual(actions.find((action) => action.action === 'get_image')!.params, {
            file: 'opaque-image-token-canary',
          })
          assert.ok(
            requests.findIndex((request) => request.model === 'vision') <
              requests.findIndex((request) => request.model === 'thinker'),
          )
        } else {
          assert.equal(vision.length, 0)
          assert.match(thinkerText, /route_not_configured/)
          assert.match(thinkerText, /unavailable/)
          assert.doesNotMatch(thinkerText, /VISIBLE_RED_SQUARE_CANARY/)
          assert.equal(actions.filter((action) => action.action === 'get_image').length, 0)
        }
        assert.equal(actions.filter((action) => action.action === 'send_group_msg').length, 1)
        assert.ok(
          store.logs.some((log) => log.stage === 'send_segment' && log.status === 'completed'),
        )
        for (const table of ['memories', 'meme_assets'])
          assert.equal(
            (store.db.prepare(`SELECT count(*) AS count FROM ${table}`).get() as { count: number })
              .count,
            0,
          )
        assert.equal(
          requests.some((request) => request.model === 'learning'),
          false,
        )
        assert.doesNotMatch(
          JSON.stringify(store.logs),
          /fixture-adapter-canary|opaque-image-token-canary|VISIBLE_RED_SQUARE_CANARY|data:image\/png;base64/,
        )
      } finally {
        await engine.dispose()
        store.close()
        for (const socket of wsServer.clients) socket.terminate()
        await new Promise<void>((resolve) => wsServer.close(() => resolve()))
        modelServer.closeAllConnections()
        await new Promise<void>((resolve) => modelServer.close(() => resolve()))
      }
    },
  )
}
