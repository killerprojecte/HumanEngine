import assert from 'node:assert/strict'
import test from 'node:test'
import sharp from 'sharp'
import { createChatMediaFixture } from './fixtures/chat-media.ts'
import { ChatMediaService, presentChatMessage } from '../src/chat-media.ts'

test('actual inbound/history projection and session gateway yield original image bytes, not source URLs or metadata captions', async () => {
  const f = await createChatMediaFixture()
  try {
    const base = 'http://fixture.local'
    const login = await f.handle(
      new Request(base + '/api/auth/session', {
        method: 'POST',
        headers: { 'X-HumanEngine-Session': '1', 'Content-Type': 'application/json' },
        body: JSON.stringify({ token: f.token }),
      }),
    )
    const cookie = login.headers.get('set-cookie')!.split(';')[0]!
    const headers = { Cookie: cookie, 'X-HumanEngine-Session': '1' }
    const list = await f.handle(
      new Request(`${base}/api/scenes/${encodeURIComponent(f.scene.id)}/history`, { headers }),
    )
    assert.equal(list.status, 200)
    const history = await list.json()
    assert.equal(history.items.length, 4)
    const serialized = JSON.stringify(history)
    assert.doesNotMatch(
      serialized,
      /synthetic-expired-key|ob:media:|internal fixture hint|fixture-qq-token/,
    )
    assert.equal(
      history.items[0].parts[0].text,
      f.literal,
      'literal user text resembling placeholders is never stripped',
    )
    const image = history.items[0].parts.find((part: { mediaUrl?: string }) => part.mediaUrl)
    const animated = history.items[1].parts.find((part: { mediaUrl?: string }) => part.mediaUrl)
    assert.ok(image.mediaUrl.endsWith('/media/1'), 'URL uses original source part index')
    assert.equal(animated.mediaKind, 'sticker')
    assert.equal(
      (await f.handle(new Request(base + image.mediaUrl, { headers: { Cookie: cookie } }))).status,
      401,
    )
    assert.equal(
      (
        await f.handle(
          new Request(base + image.mediaUrl, {
            headers: { ...headers, Origin: 'https://evil.example' },
          }),
        )
      ).status,
      401,
    )
    const response = await f.handle(new Request(base + image.mediaUrl, { headers }))
    assert.equal(response.status, 200)
    assert.equal(response.headers.get('content-type'), 'image/png')
    assert.equal(response.headers.get('cache-control'), 'no-store')
    assert.deepEqual(Buffer.from(await response.arrayBuffer()), f.png)
    const animation = await f.handle(new Request(base + animated.mediaUrl, { headers }))
    assert.equal(animation.headers.get('content-type'), 'image/gif')
    const gif = Buffer.from(await animation.arrayBuffer())
    assert.deepEqual(gif, f.gif)
    assert.equal((await sharp(gif, { animated: true }).metadata()).pages, 2)
    const expired = history.items[2].parts.find((part: { mediaUrl?: string }) => part.mediaUrl)
    assert.equal((await f.handle(new Request(base + expired.mediaUrl, { headers }))).status, 200)
    assert.ok(
      f.requests.some((call) => call.action === 'get_image' && call.params.file === 'expired'),
    )
    const bad = history.items[3].parts.find((part: { mediaUrl?: string }) => part.mediaUrl)
    assert.equal((await f.handle(new Request(base + bad.mediaUrl, { headers }))).status, 415)
    const before = f.requests.filter((call) => call.action === 'get_image').length
    await f.handle(new Request(base + image.mediaUrl, { headers }))
    assert.equal(f.requests.filter((call) => call.action === 'get_image').length, before)
    f.engine.runtime.recall(f.scene.id, f.messages[0]!.id)
    assert.equal((await f.handle(new Request(base + image.mediaUrl, { headers }))).status, 404)
    const other = image.mediaUrl.replace(
      encodeURIComponent(f.scene.id),
      encodeURIComponent('wrong-scene'),
    )
    assert.equal((await f.handle(new Request(base + other, { headers }))).status, 404)
    assert.equal(
      (
        await f.handle(
          new Request(base + image.mediaUrl.replace('/media/1', '/media/0'), { headers }),
        )
      ).status,
      404,
    )
    const logout = await f.handle(
      new Request(base + '/api/auth/session', { method: 'DELETE', headers }),
    )
    assert.equal(logout.status, 200)
    assert.equal((await f.handle(new Request(base + animated.mediaUrl, { headers }))).status, 401)
    assert.doesNotMatch(
      JSON.stringify(f.store.logs),
      /synthetic-expired-key|fixture-qq-token|fixture-admin-token/,
    )
  } finally {
    await f.close()
  }
})

test('display cache expires, cancellation and recall reject late bytes, and historical reads do not enter live runtime', async () => {
  const f = await createChatMediaFixture()
  try {
    let now = 0
    const service = new ChatMediaService(f.engine.runtime, () => now)
    const message = f.messages[1]!
    await service.read(f.scene.id, message.id, 1, new AbortController().signal)
    const targetLookups = () =>
      f.requests.filter((call) => call.action === 'get_image' && call.params.file === 'animation')
        .length
    const calls = targetLookups()
    assert.equal(calls, 1, 'the target GIF was resolved exactly once')
    await service.read(f.scene.id, message.id, 1, new AbortController().signal)
    assert.equal(targetLookups(), calls)
    now = 61_000
    await service.read(f.scene.id, message.id, 1, new AbortController().signal)
    assert.equal(
      targetLookups(),
      calls,
      'expired display cache reuses still-valid shared source bytes without a second platform lookup',
    )
    await assert.rejects(
      service.read(f.scene.id, message.id, 1, AbortSignal.abort()),
      /request_cancelled|image_unavailable/,
    )
    const simulation = presentChatMessage({ ...message, origin: 'simulation' })
    assert.equal(
      simulation.parts.some((part) => 'mediaUrl' in part),
      false,
    )
  } finally {
    await f.close()
  }
})

test('read-only history sources, late recall/cancel, failed-image recovery and exact-message URL refresh', async () => {
  const f = await createChatMediaFixture()
  try {
    const headers = { Authorization: `Bearer ${f.token}` }
    const historyEvent = structuredClone(f.events.get(1)!)
    historyEvent.message_id = 50
    f.events.set(50, historyEvent)
    const messageId = f.adapter.messageId(f.scene, '50')
    const path = `/api/scenes/${encodeURIComponent(f.scene.id)}/messages/${encodeURIComponent(messageId)}/media/1`
    const before = f.engine.runtime.preview(f.scene.id).length
    assert.equal((await f.app.request(path, { headers })).status, 200)
    assert.equal(f.engine.runtime.preview(f.scene.id).length, before)
    assert.ok(
      f.requests.some(
        (call) => call.action === 'get_msg' && String(call.params.message_id) === '50',
      ),
    )
    const badPath = presentChatMessage(f.messages[3]!).parts.find((part) => 'mediaUrl' in part)!
    assert.ok('mediaUrl' in badPath)
    assert.equal((await f.app.request(badPath.mediaUrl!, { headers })).status, 415)
    f.recoverCorrupt()
    assert.equal((await f.app.request(badPath.mediaUrl!, { headers })).status, 200)

    // Historical URL-only media expires between history listing and display fetch.
    const staleEvent = structuredClone(historyEvent)
    staleEvent.message_id = 51
    staleEvent.message = [
      { type: 'text', data: { text: '' } },
      { type: 'image', data: { url: 'https://public.example/image?signature=expired-fixture' } },
    ]
    f.events.set(51, staleEvent)
    const refreshedEvent = structuredClone(staleEvent)
    refreshedEvent.message = [
      { type: 'text', data: { text: '' } },
      { type: 'image', data: { file: `base64://${f.png.toString('base64')}` } },
    ]
    const originalRead = f.adapter.readImage
    let attempts = 0
    f.adapter.readImage = async (...args) => {
      attempts++
      if (attempts === 1) {
        f.events.set(51, refreshedEvent)
        throw new Error('public_http_403')
      }
      return originalRead(...args)
    }
    const refreshed = await new ChatMediaService(f.engine.runtime).read(
      f.scene.id,
      f.adapter.messageId(f.scene, '51'),
      1,
      new AbortController().signal,
    )
    assert.deepEqual(refreshed.bytes, f.png)
    assert.equal(attempts, 2)
    assert.equal(f.engine.runtime.preview(f.scene.id).length, before)
    f.adapter.readImage = originalRead

    // Use fresh exact historical sources: persisted aliases may legitimately bypass adapter I/O.
    for (const id of [52, 53])
      f.events.set(id, {
        ...staleEvent,
        message_id: id,
        message: [
          { type: 'text', data: { text: '' } },
          { type: 'image', data: { file: `fixture-delayed-${id}` } },
        ],
      })
    const delayedId = f.adapter.messageId(f.scene, '52')
    const cancelledId = f.adapter.messageId(f.scene, '53')
    const service = new ChatMediaService(f.engine.runtime)
    let release!: (bytes: Buffer) => void
    const oldRead = f.adapter.readImage
    f.adapter.readImage = async () =>
      new Promise<Buffer>((resolve) => {
        release = resolve
      })
    const pending = service.read(f.scene.id, delayedId, 1, new AbortController().signal)
    const recallDeadline = Date.now() + 2000
    while (!release) {
      if (Date.now() > recallDeadline) assert.fail('delayed image read did not start')
      await new Promise((resolve) => setImmediate(resolve))
    }
    f.engine.runtime.recall(f.scene.id, delayedId)
    release(f.png)
    await assert.rejects(pending, /image_unavailable/)
    let releaseCancelled!: (bytes: Buffer) => void
    f.adapter.readImage = async () =>
      new Promise<Buffer>((resolve) => {
        releaseCancelled = resolve
      })
    const controller = new AbortController()
    const cancelled = service.read(f.scene.id, cancelledId, 1, controller.signal)
    const cancelDeadline = Date.now() + 2000
    while (!releaseCancelled) {
      if (Date.now() > cancelDeadline) assert.fail('cancelled image read did not start')
      await new Promise((resolve) => setImmediate(resolve))
    }
    controller.abort()
    releaseCancelled(f.gif)
    await assert.rejects(cancelled, /image_unavailable/)
    f.adapter.readImage = oldRead
  } finally {
    await f.close()
  }
})

test('managed outgoing image uses scene-bound original and revalidates proposed, disabled, missing and recalled assets on cache hits', async () => {
  const f = await createChatMediaFixture()
  try {
    const { asset, outgoing } = await f.prepareManaged()
    const headers = { Authorization: `Bearer ${f.token}` }
    const projected = presentChatMessage(outgoing).parts[0]!
    assert.ok('mediaUrl' in projected)
    const url = projected.mediaUrl!
    assert.equal(
      (await f.app.request(url, { headers })).status,
      404,
      'proposed/unavailable asset is not displayed',
    )
    assert.equal(asset.knownOnly, true)
    f.engine.memes.adminSetIncluded(asset.id, true)
    const displayed = await f.app.request(url, { headers })
    assert.equal(displayed.status, 200)
    assert.deepEqual(Buffer.from(await displayed.arrayBuffer()), f.png)
    assert.equal(
      (await f.app.request(url.replace(encodeURIComponent(f.scene.id), 'wrong-scene'), { headers }))
        .status,
      404,
    )
    f.engine.memes.update(asset.id, { disabled: true })
    assert.equal(
      (await f.app.request(url, { headers })).status,
      404,
      'cached bytes cannot bypass disabled state',
    )
    f.engine.memes.update(asset.id, { disabled: false })
    f.engine.memes.adminSetIncluded(asset.id, true)
    assert.equal((await f.app.request(url, { headers })).status, 200)
    f.engine.runtime.recall(f.scene.id, outgoing.id)
    assert.equal((await f.app.request(url, { headers })).status, 404)
    const missing = {
      ...outgoing,
      id: f.adapter.messageId(f.scene, '81'),
      parts: [{ type: 'media' as const, assetId: 'f'.repeat(64), mediaType: 'image/png' }],
    }
    f.engine.runtime.ingest(missing, f.scene, false)
    const missingPart = presentChatMessage(missing).parts[0]!
    assert.ok('mediaUrl' in missingPart)
    assert.equal((await f.app.request(missingPart.mediaUrl!, { headers })).status, 404)
    // A different extant scene containing the same hash does not gain access to the private asset.
    const other = f.adapter.scene('group', '10')
    f.store.saveScene({ ...f.store.scene(f.scene.id)!, ...other })
    const foreign = { ...outgoing, id: f.adapter.messageId(other, '82'), sceneId: other.id }
    f.engine.runtime.ingest(foreign, other, false)
    const foreignPart = presentChatMessage(foreign).parts[0]!
    assert.ok('mediaUrl' in foreignPart)
    assert.equal((await f.app.request(foreignPart.mediaUrl!, { headers })).status, 404)
    const deleted = { ...outgoing, id: f.adapter.messageId(f.scene, '83') }
    f.engine.runtime.ingest(deleted, f.scene, false)
    const deletedPart = presentChatMessage(deleted).parts[0]!
    assert.ok('mediaUrl' in deletedPart)
    assert.equal((await f.app.request(deletedPart.mediaUrl!, { headers })).status, 200)
    f.store.db.prepare('DELETE FROM meme_assets WHERE id=?').run(asset.id)
    assert.equal(
      (await f.app.request(deletedPart.mediaUrl!, { headers })).status,
      404,
      'cached bytes cannot bypass deleted asset',
    )
  } finally {
    await f.close()
  }
})
