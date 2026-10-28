import { mkdtemp, rm } from 'node:fs/promises'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { MemeService } from '../../src/memes.ts'
import { once } from 'node:events'
import sharp from 'sharp'
import { WebSocketServer } from 'ws'
import { Engine } from '../../src/engine.ts'
import { Store } from '../../src/store.ts'
import { OneBotAdapter, type Message } from '../../src/onebot.ts'
import { createApp } from '../../src/api.ts'
import { createSessionGateway } from '../../src/auth/session.ts'

/** Synthetic image-only fixture. No remote QQ, private URL, or model traffic is possible. */
export async function createChatMediaFixture() {
  const png = await sharp(
    Buffer.from(
      '<svg width="480" height="280" xmlns="http://www.w3.org/2000/svg"><rect width="480" height="280" fill="#d6edff"/><circle cx="365" cy="65" r="32" fill="#ffca59"/><path d="M0 240L140 65L300 240Z" fill="#6098ad"/><path d="M170 240L320 100L480 240Z" fill="#41836f"/><path d="M112 100L140 65L173 103L147 94Z" fill="#fff"/><path d="M0 230Q240 190 480 230V280H0Z" fill="#90bb76"/></svg>',
    ),
  )
    .png()
    .toBuffer()
  const pixels = Buffer.concat([
    Buffer.from(
      Array(64 * 64)
        .fill([255, 80, 40])
        .flat(),
    ),
    Buffer.from(
      Array(64 * 64)
        .fill([40, 200, 120])
        .flat(),
    ),
  ])
  const gif = await sharp(pixels, { raw: { width: 64, height: 128, channels: 3, pageHeight: 64 } })
    .gif({ loop: 0, delay: [500, 500] })
    .toBuffer()
  const store = new Store(':memory:')
  const engine = new Engine(store)
  const managedRoot = await mkdtemp(join(tmpdir(), 'chat-managed-'))
  Object.defineProperty(engine, 'memes', {
    value: new MemeService(store, managedRoot),
    configurable: true,
  })
  const requests: Array<{ action: string; params: Record<string, unknown> }> = []
  const messages: Message[] = []
  const events = new Map<number, Record<string, unknown>>()
  const bytes = new Map([
    ['picture', png],
    ['animation', gif],
    ['expired', png],
    ['corrupt', Buffer.from('<html>bad-image</html>')],
  ])
  const server = new WebSocketServer({ host: '127.0.0.1', port: 0 })
  server.on('connection', (socket) =>
    socket.on('message', (raw) => {
      const call = JSON.parse(raw.toString())
      requests.push(call)
      const data =
        call.action === 'get_login_info'
          ? { user_id: 42 }
          : call.action === 'get_image'
            ? {
                file: `base64://${(bytes.get(String(call.params.file)) ?? png).toString('base64')}`,
              }
            : call.action === 'get_msg'
              ? events.get(Number(call.params.message_id))
              : call.action === 'get_group_msg_history'
                ? { messages: [...events.values()] }
                : {}
      socket.send(
        JSON.stringify({
          status: data ? 'ok' : 'failed',
          retcode: data ? 0 : 1404,
          echo: call.echo,
          data,
        }),
      )
    }),
  )
  await once(server, 'listening')
  const address = server.address()
  if (!address || typeof address === 'string') throw new Error('fixture_not_listening')
  const adapter = new OneBotAdapter(
    {
      id: 'snowluma',
      accountId: '42',
      url: `ws://127.0.0.1:${address.port}`,
      token: 'fixture-qq-token',
    },
    {
      onMessage: (message, scene) => {
        messages.push(message)
        engine.runtime.ingest(message, scene, false)
      },
    },
  )
  const originalRead = adapter.readImage.bind(adapter)
  adapter.readImage = (scene, assetId, signal) =>
    originalRead(scene, assetId, signal, async () => {
      throw new Error('media_fetch_failed')
    })
  const scene = adapter.scene('group', '9')
  store.saveScene({
    ...scene,
    name: 'Synthetic image room',
    participationEnabled: false,
    learningEnabled: false,
    proactiveEnabled: false,
  })
  engine.runtime.addAdapter('snowluma', adapter)
  await adapter.connect()
  const literal = '[image/unknown][Untrusted platform image metadata: {"kind":"image"}]'
  for (const [id, file, type] of [
    [1, 'picture', 'image'],
    [2, 'animation', 'mface'],
    [3, 'expired', 'image'],
    [4, 'corrupt', 'image'],
  ] as const) {
    const event = {
      post_type: 'message',
      self_id: 42,
      message_type: 'group',
      group_id: 9,
      user_id: 7,
      message_id: id,
      time: Math.floor(Date.now() / 1000),
      sender: { nickname: 'Fixture participant' },
      message: [
        { type: 'text', data: { text: id === 1 ? literal : '' } },
        {
          type,
          data: {
            file,
            summary: 'internal fixture hint',
            sub_type: 7,
            ...(file === 'expired'
              ? { url: 'https://multimedia.nt.qq.com.cn/download?fixture=synthetic-expired-key' }
              : {}),
          },
        },
      ],
    }
    events.set(id, event)
    ;[...server.clients][0]!.send(JSON.stringify(event))
  }
  const deadline = Date.now() + 2000
  while (messages.length < 4) {
    if (Date.now() > deadline) throw new Error('fixture_ingress_failed')
    await new Promise((resolve) => setTimeout(resolve, 5))
  }
  const token = 'fixture-admin-token'
  const app = createApp(store, { engine, token })
  const gateway = createSessionGateway()
  const handle = (request: Request) =>
    gateway(request, async (authorized) => app.fetch(authorized), { token })
  return {
    png,
    gif,
    scene,
    messages,
    requests,
    engine,
    store,
    app,
    handle,
    token,
    literal,
    events,
    adapter,
    recoverCorrupt: () => bytes.set('corrupt', png),
    prepareManaged: async () => {
      store.saveScene({ ...store.scene(scene.id)!, learningEnabled: true })
      const source = {
        sceneId: scene.id,
        messageId: messages[0]!.id,
        senderId: messages[0]!.senderId,
      }
      const asset = await engine.memes.import(
        {
          sceneId: scene.id,
          simulation: false,
          sourceEvidence: [{ ...source, available: true, isBot: false }],
        },
        png,
        source,
      )
      store.saveScene({ ...store.scene(scene.id)!, learningEnabled: false })
      engine.memes.update(asset.id, { tags: asset.tags })
      // Deterministic unselected state whether background intake or explicit import won admission.
      engine.memes.adminSetIncluded(asset.id, false)
      const outgoing: Message = {
        ...messages[0]!,
        id: adapter.messageId(scene, '80'),
        senderId: 'bot',
        parts: [{ type: 'media', assetId: asset.id, mediaType: 'image/png' }],
      }
      engine.runtime.ingest(outgoing, scene, false)
      return { asset: engine.memes.adminGet(asset.id)!, outgoing }
    },
    close: async () => {
      await engine.dispose()
      store.close()
      if (managedRoot) await rm(managedRoot, { recursive: true, force: true })
      for (const socket of server.clients) socket.terminate()
      await new Promise<void>((resolve) => server.close(() => resolve()))
    },
  }
}
