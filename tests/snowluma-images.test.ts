import assert from 'node:assert/strict'
import test from 'node:test'
import { once } from 'node:events'
import sharp from 'sharp'
import { readOneBotImage } from '../src/plugins/builtin/snowluma/media.ts'
import { normalizeMessage } from '../src/plugins/builtin/snowluma/normalization.ts'
import { OneBotAdapter, encode } from '../src/onebot.ts'
import { imageDataUrl } from '../src/media.ts'
import { fetchQqMedia } from '../src/media-fetch.ts'
const adapter = new OneBotAdapter({ id: 'snow', accountId: '42', url: 'ws://127.0.0.1:1' })
const scene = adapter.scene('group', '9')
const asset = (file: string, fileId?: string) =>
  `ob:media:${encode({ sceneId: scene.id, type: 'image', file, fileId })}`
const png = () =>
  sharp({ create: { width: 2, height: 2, channels: 3, background: '#ff0000' } })
    .png()
    .toBuffer()

test('SnowLuma file tokens resolve via get_image; URL refresh is bounded and URLs do not receive credentials', async () => {
  const bytes = await png(),
    actions: unknown[] = [],
    urls: string[] = []
  const action = async (name: string, params: unknown) => {
    actions.push({ name, params })
    return { url: 'https://gchat.qpic.cn/fresh' }
  }
  const fetcher = async (url: string) => {
    urls.push(url)
    if (url.endsWith('/expired')) throw new Error('media_fetch_failed')
    return bytes
  }
  assert.deepEqual(
    await readOneBotImage(scene, asset('cache-token'), action, undefined, fetcher),
    bytes,
  )
  assert.deepEqual(actions, [{ name: 'get_image', params: { file: 'cache-token' } }])
  actions.length = 0
  await readOneBotImage(
    scene,
    asset('https://gchat.qpic.cn/expired', 'cache-token'),
    action,
    undefined,
    fetcher,
  )
  assert.equal(actions.length, 1)
  assert.deepEqual(urls, [
    'https://gchat.qpic.cn/fresh',
    'https://gchat.qpic.cn/expired',
    'https://gchat.qpic.cn/fresh',
  ])
  await assert.rejects(
    readOneBotImage(scene, asset('token'), async () => ({ file: '/private/image.png' })),
    /image_download_url_unavailable/,
  )
  await assert.rejects(
    readOneBotImage({ ...scene, id: 'other' }, asset('token'), action),
    /media_scope_mismatch/,
  )
  const cancelled = AbortSignal.abort()
  await assert.rejects(readOneBotImage(scene, asset('token'), action, cancelled), /media_cancelled/)
})

test('SnowLuma array and CQ representations preserve image source and token without double unescaping', async () => {
  const context = {
    accountId: '42',
    scene: () => scene,
    messageId: (s: typeof scene, id: string) => adapter.messageId(s, id),
    userId: (id: string) => adapter.userId(id),
    isRecalled: () => false,
  }
  for (const message of [
    [{ type: 'image', data: { file: 'token', url: 'https://gchat.qpic.cn/a?a=1&b=2' } }],
    '[CQ:image,file=token,url=https://gchat.qpic.cn/a?a=1&amp;b=2]',
  ]) {
    const result = normalizeMessage(
      context,
      { message_type: 'group', group_id: 9, user_id: 7, message_id: 1, time: 1000, message },
      'live',
    ).message
    const part = result.parts[0]!
    assert.equal(part.type, 'media')
    if (part.type !== 'media') throw new Error('wrong type')
    assert.deepEqual(JSON.parse(Buffer.from(part.assetId.slice(9), 'base64url').toString()), {
      sceneId: scene.id,
      type: 'image',
      file: 'https://gchat.qpic.cn/a?a=1&b=2',
      fileId: 'token',
    })
  }
})

test('image bytes override claimed MIME; PNG JPEG GIF WebP become verified PNG model data', async () => {
  for (const format of ['png', 'jpeg', 'gif', 'webp'] as const) {
    const bytes = await sharp(await png())
      .toFormat(format)
      .toBuffer()
    for (const file of [
      `base64://${bytes.toString('base64')}`,
      `data:image/jpeg;base64,${bytes.toString('base64')}`,
    ]) {
      const result = await readOneBotImage(scene, asset(file), async () => {
        throw new Error('unexpected action')
      })
      const image = await imageDataUrl(result)
      assert.match(image, /^data:image\/png;base64,/)
      assert.equal(
        (await sharp(Buffer.from(image.split(',')[1]!, 'base64')).metadata()).format,
        'png',
      )
    }
  }
  await assert.rejects(
    imageDataUrl(Buffer.from('<html>expired token</html>')),
    /invalid_or_unsupported_image/,
  )
  await assert.rejects(
    readOneBotImage(scene, asset('base64://invalid!'), async () => ({})),
    /invalid_image_data/,
  )
  await assert.rejects(imageDataUrl(Buffer.alloc(10 * 1024 * 1024 + 1)), /media_size_limit/)
})

test('media schemes, credentials and private hosts are rejected without remote requests', async () => {
  for (const url of [
    'file:///etc/passwd',
    'ftp://public.example/a',
    'https://secret@public.example/a',
  ])
    await assert.rejects(fetchQqMedia(url), /media_url_not_supported/)
  await assert.rejects(fetchQqMedia('https://127.0.0.1/a'), /media_address_not_public/)
})

test('mocked outgoing vision request contains verified image content, not the unknown placeholder', async () => {
  const { createServer } = await import('node:http')
  const { Router } = await import('../src/models.ts')
  const requests: any[] = []
  const server = createServer(async (req, res) => {
    const chunks: Buffer[] = []
    for await (const chunk of req) chunks.push(Buffer.from(chunk))
    requests.push(JSON.parse(Buffer.concat(chunks).toString()))
    res.setHeader('content-type', 'application/json')
    res.end(
      JSON.stringify({
        choices: [{ finish_reason: 'stop', message: { content: 'A red square' } }],
        usage: { prompt_tokens: 3, completion_tokens: 4, total_tokens: 7 },
      }),
    )
  })
  server.listen(0, '127.0.0.1')
  await once(server, 'listening')
  try {
    const address = server.address()
    if (!address || typeof address === 'string') throw new Error('fixture failed')
    const config = {
      providers: [
        {
          id: 'p',
          name: 'Fixture',
          protocol: 'openai-chat',
          baseUrl: `http://127.0.0.1:${address.port}/v1`,
          credentialEnv: '',
        },
      ],
      models: [
        {
          id: 'v',
          displayName: 'Vision',
          providerId: 'p',
          apiModelName: 'mock',
          capabilities: ['text', 'vision'] as Array<'text' | 'vision'>,
        },
      ],
      routes: [{ role: 'vision' as const, orderedModelIds: ['v'], timeoutMs: 10000 }],
    }
    const bytes = await readOneBotImage(
      scene,
      asset('opaque'),
      async () => ({ url: 'https://gchat.qpic.cn/image' }),
      undefined,
      png,
    )
    const image = await imageDataUrl(bytes)
    const result = await new Router(config).complete('vision', [
      { role: 'user', content: 'Describe image', images: [image] },
    ])
    assert.equal(result.text, 'A red square')
    assert.deepEqual(requests[0].messages[0].content[1], {
      type: 'image_url',
      image_url: { url: image },
    })
    assert.equal(JSON.stringify(requests).includes('[image/unknown]'), false)
    config.models[0]!.capabilities = ['text']
    await assert.rejects(
      new Router(config).complete('vision', [
        { role: 'user', content: 'Describe', images: [image] },
      ]),
      /no_compatible_model/,
    )
    assert.equal(requests.length, 1)
  } finally {
    server.closeAllConnections()
    await new Promise<void>((resolve) => server.close(() => resolve()))
  }
})

test('file-only image lookup uses the authenticated adapter action channel and rejects cross-scene IDs', async () => {
  const { WebSocketServer } = await import('ws')
  const server = new WebSocketServer({ host: '127.0.0.1', port: 0 })
  const calls: any[] = []
  let authorization: string | undefined
  server.on('connection', (socket, request) => {
    authorization = request.headers.authorization
    socket.on('message', (raw) => {
      const call = JSON.parse(raw.toString())
      calls.push(call)
      socket.send(
        JSON.stringify({
          status: 'ok',
          retcode: 0,
          echo: call.echo,
          data:
            call.action === 'get_login_info'
              ? { user_id: '42' }
              : { url: 'https://gchat.qpic.cn/fixture' },
        }),
      )
    })
  })
  await once(server, 'listening')
  const address = server.address()
  if (!address || typeof address === 'string') throw new Error('fixture failed')
  const connected = new OneBotAdapter({
    id: 'snow',
    accountId: '42',
    url: `ws://127.0.0.1:${address.port}`,
    token: 'fixture-only-not-a-real-key',
  })
  try {
    await connected.connect()
    const bytes = await connected.readImage(scene, asset('opaque'), undefined, png)
    assert.equal(authorization, 'Bearer fixture-only-not-a-real-key')
    assert.equal((await sharp(bytes).metadata()).format, 'png')
    assert.deepEqual(
      calls.map(({ action, params }) => ({ action, params })),
      [
        { action: 'get_login_info', params: {} },
        { action: 'get_image', params: { file: 'opaque' } },
      ],
    )
    await assert.rejects(
      connected.readImage(connected.scene('group', '10'), asset('opaque'), undefined, png),
      /media_scope_mismatch/,
    )
    assert.equal(calls.length, 2)
  } finally {
    connected.dispose()
    for (const socket of server.clients) socket.terminate()
    await new Promise<void>((resolve) => server.close(() => resolve()))
  }
})

test('native image, mface, sticker and CQ sources retain real bytes and untrusted metadata', async () => {
  const bytes = await png()
  const context = {
    accountId: '42',
    scene: () => scene,
    messageId: (s: typeof scene, id: string) => adapter.messageId(s, id),
    userId: (id: string) => adapter.userId(id),
    isRecalled: () => false,
  }
  const normalize = (message: unknown) =>
    normalizeMessage(
      context,
      { message_type: 'group', group_id: 9, user_id: 7, message_id: 1, time: 1000, message },
      'live',
    ).message
  for (const type of ['image', 'mface', 'sticker', 'emoji']) {
    const message = normalize([
      {
        type,
        data: {
          file: 'unusable-token',
          url: 'https://expired.qpic.cn/never-fetch',
          base64: bytes.toString('base64'),
          summary: '[wave]',
          sub_type: 1,
        },
      },
    ])
    const part = message.parts[0]!
    assert.equal(part.type, 'media')
    if (part.type !== 'media') throw new Error('missing image')
    const result = await readOneBotImage(
      scene,
      part.assetId,
      async () => {
        throw new Error('must not lookup with valid inline bytes')
      },
      undefined,
      async () => {
        throw new Error('must not download with valid inline bytes')
      },
    )
    assert.deepEqual(result, bytes)
    assert.match(JSON.stringify(message.parts), /Untrusted platform image metadata/)
  }
  const fileOnly = normalize('[CQ:mface,file_id=opaque-id,summary=wave]')
  const part = fileOnly.parts[0]!
  if (part.type !== 'media') throw new Error('missing CQ media')
  let called = false
  await readOneBotImage(scene, part.assetId, async (action, params) => {
    called = true
    assert.equal(action, 'get_image')
    assert.equal(params.file, 'opaque-id')
    return { file: `base64://${bytes.toString('base64')}` }
  })
  assert.ok(called)
  const missing = normalize([
    { type: 'image', data: {} },
    { type: 'mface', data: { emoji_id: 'metadata-only', summary: 'unavailable sticker' } },
    { type: 'text', data: { text: 'still retained' } },
  ])
  assert.match(JSON.stringify(missing.parts), /Image unavailable/)
  assert.ok(missing.parts.some((part) => part.type === 'text' && part.text === 'still retained'))
  const face = normalize([
    { type: 'face', data: { id: '14', text: 'smile' } },
    { type: 'face', data: { id: '999999' } },
  ])
  assert.match(JSON.stringify(face.parts), /smile/)
  assert.match(JSON.stringify(face.parts), /meaning.*unavailable/)
  assert.equal(
    face.parts.some((part) => part.type === 'media'),
    false,
  )
})

test('animated GIF and WebP conversion intentionally supplies only the first frame', async () => {
  const pixels = Buffer.concat([
    Buffer.from(Array(4).fill([255, 0, 0]).flat()),
    Buffer.from(Array(4).fill([0, 0, 255]).flat()),
  ])
  for (const format of ['gif', 'webp'] as const) {
    const animated = await sharp(pixels, {
      raw: { width: 2, height: 4, channels: 3, pageHeight: 2 },
    })
      .toFormat(format, { loop: 0, delay: [100, 100] })
      .toBuffer()
    assert.equal((await sharp(animated, { animated: true }).metadata()).pages, 2)
    const data = await imageDataUrl(animated)
    const output = Buffer.from(data.split(',')[1]!, 'base64')
    const metadata = await sharp(output).metadata()
    assert.equal(metadata.height, 2)
    const { data: decoded } = await sharp(output)
      .removeAlpha()
      .raw()
      .toBuffer({ resolveWithObject: true })
    assert.ok(decoded[0]! > 200 && decoded[2]! < 30, 'only the red first frame should be sent')
  }
})
