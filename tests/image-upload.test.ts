import { ImageByteCache } from '../src/runtime/image-byte-cache.ts'
import assert from 'node:assert/strict'
import test from 'node:test'
import { randomBytes } from 'node:crypto'
import sharp from 'sharp'
import { compressImage, prepareImageRequest } from '../src/image-upload.ts'
import { imageDataUrl } from '../src/media.ts'
import { readOneBotImage } from '../src/plugins/builtin/snowluma/media.ts'
import { encode } from '../src/plugins/builtin/snowluma/protocol.ts'
const bytesOf = (url: string) => Buffer.from(url.split(',')[1]!, 'base64')
const urlOf = (bytes: Buffer) => `data:image/png;base64,${bytes.toString('base64')}`

test('oversized real source crosses the former 10 MiB boundary and becomes bounded vision payload without mutating source', async () => {
  const source = await sharp(randomBytes(2000 * 2000 * 3), {
    raw: { width: 2000, height: 2000, channels: 3 },
  })
    .png({ compressionLevel: 0 })
    .toBuffer()
  assert.ok(source.length > 10 * 1024 * 1024)
  const original = Buffer.from(source)
  const scene = {
    id: 'scene',
    adapterId: 'onebot',
    accountId: 'bot',
    kind: 'group' as const,
    platformSceneId: 'group',
  }
  const loaded = await readOneBotImage(
    scene,
    `ob:media:${encode({ sceneId: scene.id, type: 'image', file: `base64://${source.toString('base64')}` })}`,
    async () => {
      throw new Error('unexpected request')
    },
  )
  const cached = await new ImageByteCache().read(
    {},
    scene.id,
    'oversized',
    new AbortController().signal,
    () => true,
    async () => loaded,
  )
  const compressed = bytesOf(await imageDataUrl(cached))
  const info = await sharp(compressed).metadata()
  assert.ok(compressed.length <= 10 * 1024 * 1024)
  assert.ok(info.width! <= 1536 && info.height! <= 1536)
  assert.deepEqual(source, original)
})

test('acceptable originals are identical; alpha survives adaptation and animation reduction is explicit', async () => {
  const png = await sharp({
    create: { width: 128, height: 100, channels: 4, background: '#ff000080' },
  })
    .png()
    .toBuffer()
  assert.deepEqual(bytesOf(await compressImage(png, { maxImageBytes: 10000 })), png)
  const smaller = bytesOf(
    await compressImage(png, { maxWidth: 32, maxHeight: 32, maxImageBytes: 1000 }),
  )
  const info = await sharp(smaller).metadata()
  assert.equal(info.hasAlpha, true)
  assert.ok(info.width! <= 32 && info.height! <= 32)
  const animation = await sharp(
    Buffer.concat([Buffer.alloc(2 * 2 * 3, 200), Buffer.alloc(2 * 2 * 3, 50)]),
    {
      raw: { width: 2, height: 4, channels: 3, pageHeight: 2 },
    },
  )
    .gif({ delay: [100, 100], loop: 0 })
    .toBuffer()
  await assert.rejects(
    compressImage(animation, { maxWidth: 1 }),
    /animated_image_compression_requires_first_frame/,
  )
  const frame = bytesOf(await compressImage(animation, { maxWidth: 1 }, true))
  assert.equal((await sharp(frame).metadata()).pages ?? 1, 1)
  await assert.rejects(compressImage(Buffer.from('broken'), {}), /invalid_or_unsupported_image/)
  await assert.rejects(compressImage(png, { maxWidth: 1 }, false, AbortSignal.abort()), {
    name: 'AbortError',
  })
  await assert.rejects(compressImage(png, { maxImageBytes: 1 }), /image_upload_limit_unachievable/)
})

test('both native wire envelopes fit exact aggregate UTF-8 JSON/base64 limits and preserve caller payload', async () => {
  const png = await sharp(randomBytes(256 * 256 * 3), {
    raw: { width: 256, height: 256, channels: 3 },
  })
    .png()
    .toBuffer()
  for (const protocol of ['chat', 'responses']) {
    const parts = Array.from({ length: 2 }, () =>
      protocol === 'chat'
        ? { type: 'image_url', image_url: { url: urlOf(png) } }
        : { type: 'input_image', image_url: urlOf(png) },
    )
    const body = {
      [protocol === 'chat' ? 'messages' : 'input']: [
        { role: 'user', content: [{ type: 'text', text: '你好'.repeat(100) }, ...parts] },
      ],
      tools: [{ description: 'reserved envelope overhead' }],
    }
    const original = JSON.stringify(body)
    const wire = await prepareImageRequest(body, {
      maxImageBytes: 200000,
      maxRequestBytes: 9000,
      maxWidth: 128,
      maxHeight: 128,
    })
    assert.ok(Buffer.byteLength(wire) <= 9000)
    assert.equal(JSON.stringify(body), original)
    const payload = JSON.parse(wire)
    for (const part of payload[protocol === 'chat' ? 'messages' : 'input'][0].content.slice(1)) {
      const bytes = bytesOf(protocol === 'chat' ? part.image_url.url : part.image_url)
      const info = await sharp(bytes).metadata()
      assert.ok(info.width! <= 128 && info.height! <= 128)
    }
    await assert.rejects(
      prepareImageRequest(body, { maxRequestBytes: 10 }),
      /image_request_size_limit/,
    )
  }
})
