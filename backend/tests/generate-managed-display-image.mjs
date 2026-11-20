import fs from 'node:fs'
import { createHash } from 'node:crypto'
import { createChatMediaFixture } from '../../tests/fixtures/chat-media.ts'
import { presentChatMessage } from '../../src/chat-media.ts'

const f = await createChatMediaFixture()
try {
  const { asset, outgoing } = await f.prepareManaged()
  const cases = {}
  const route = (message) => presentChatMessage(message).parts[0].mediaUrl
  const url = route(outgoing)
  const check = async (name, path = url, method = 'GET', authorized = true) => {
    const headers = { Authorization: authorized ? `Bearer ${f.token}` : 'Bearer synthetic-invalid-bearer' }
    const response = await f.app.request(path, { method, headers })
    const entry = { status: response.status }
    if (response.ok) {
      entry.mime = response.headers.get('content-type')
      entry.length = response.headers.get('content-length')
      entry.disposition = response.headers.get('content-disposition')
      entry.resourcePolicy = response.headers.get('cross-origin-resource-policy')
      entry.cache = response.headers.get('cache-control')
      entry.nosniff = response.headers.get('x-content-type-options')
      if (method !== 'HEAD') {
        entry.hash = createHash('sha256').update(Buffer.from(await response.arrayBuffer())).digest('hex')
      }
    } else if (method !== 'HEAD') {
      entry.error = (await response.json()).error
    }
    cases[name] = entry
  }
  await check('proposed')
  f.engine.memes.adminSetIncluded(asset.id, true)
  await check('visible')
  await check('head', url, 'HEAD')
  await check('unauthorized', url, 'GET', false)
  await check('invalidIndex', url.replace(/\/0$/, '/00'))
  await check('outOfRange', url.replace(/\/0$/, '/256'))
  await check('wrongScene', url.replace(encodeURIComponent(f.scene.id), 'wrong-scene'))
  for (const [limit, prefix, route] of [
    [4096, 'scenePoints', (id) => `/api/scenes/${encodeURIComponent(id)}/messages/m/media/0`],
    [8192, 'messagePoints', (id) => `/api/scenes/${encodeURIComponent(f.scene.id)}/messages/${encodeURIComponent(id)}/media/0`],
  ]) {
    await check(prefix + 'At', route('😀'.repeat(limit)))
    await check(prefix + 'Beyond', route('😀'.repeat(limit + 1)))
  }
  f.engine.memes.update(asset.id, { disabled: true })
  await check('disabled')
  f.engine.memes.update(asset.id, { disabled: false })
  f.engine.memes.adminSetIncluded(asset.id, true)
  await check('visibleAgain')
  f.engine.runtime.recall(f.scene.id, outgoing.id)
  await check('recalled')
  const missing = { ...outgoing, id: f.adapter.messageId(f.scene, '81'),
    parts: [{ type: 'media', assetId: 'f'.repeat(64), mediaType: 'image/png' }],
  }
  f.engine.runtime.ingest(missing, f.scene, false)
  await check('missingAsset', route(missing))
  const other = f.adapter.scene('group', '10')
  f.store.saveScene({ ...f.store.scene(f.scene.id), ...other })
  const foreign = { ...outgoing, id: f.adapter.messageId(other, '82'), sceneId: other.id }
  f.engine.runtime.ingest(foreign, other, false)
  await check('foreignPrivateAsset', route(foreign))
  const deleted = { ...outgoing, id: f.adapter.messageId(f.scene, '83') }
  f.engine.runtime.ingest(deleted, f.scene, false)
  await check('beforeDelete', route(deleted))
  f.store.db.prepare('DELETE FROM meme_assets WHERE id=?').run(asset.id)
  await check('deleted', route(deleted))
  const output = JSON.stringify({ base64: f.png.toString('base64'), cases }, null, 2) + '\n'
  const target = new URL('../internal/app/testdata/managed-display-image-source.json', import.meta.url)
  if (process.argv.includes('--check')) {
    if (fs.readFileSync(target, 'utf8') !== output) process.exitCode = 1
  } else fs.writeFileSync(target, output)
} finally {
  await f.close()
}
