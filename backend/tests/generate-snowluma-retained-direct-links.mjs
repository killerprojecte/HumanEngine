import assert from 'node:assert/strict'
import { readFileSync, writeFileSync } from 'node:fs'
import { createApp } from '../../src/api.ts'
import { Engine } from '../../src/engine.ts'
import { normalizeMessage } from '../../src/plugins/builtin/snowluma/normalization.ts'
import { Store } from '../../src/store.ts'

const store = new Store(':memory:')
const engine = new Engine(store)
const api = createApp(store, { engine, token: 'synthetic-token' })
const scene = {
  id: 'fixture/group', name: 'Fixture', kind: 'group', adapterId: 'snowluma', accountId: '42',
  platformSceneId: '104', participationEnabled: false, learningEnabled: false, proactiveEnabled: false,
}
const normalizer = {
  accountId: '42', scene: () => ({ ...scene }),
  messageId: (_scene, id) => 'fixture/message:' + id, userId: id => 'user:' + id,
  isRecalled: () => false,
}
const locators = [
  [{ type: 'text', data: { text: 'prefix' } }, { type: 'image', data: { url: 'https://public.fixture.invalid/direct?a=signed-synthetic' } }],
  [{ type: 'mface', data: { url: 'https://public.fixture.invalid/sticker', file_id: 'sticker-token' } }],
  [{ type: 'image', data: { url: 'http://127.0.0.1/blocked' } }],
  [{ type: 'image', data: { file: 'base64://YQ==' } }],
  [{ type: 'image', data: { file: 'opaque-fixture-token' } }],
]
try {
  // Ingress may schedule source-image capture. The synthetic adapter has no
  // network or byte source; the listing assertions use the real runtime.
  engine.runtime.addAdapter(scene.adapterId, {
    dispose() {},
    async readImage() { throw new Error('synthetic fixture has no image transport') },
  })
  store.saveScene(scene)
  for (let i = 0; i < locators.length; i++) {
    const { message } = normalizeMessage(normalizer, {
      self_id: 42, message_type: 'group', group_id: 104, user_id: 7,
      message_id: i + 1, time: 4070908800 + i, message: locators[i],
    }, 'live')
    message.receivedAt = '2099-01-01T00:00:00.000Z'
    engine.runtime.ingest(message, scene, false)
  }
  const rows = []
  for (const surface of ['messages', 'history?source=local']) {
    const response = await api.request('/api/scenes/' + encodeURIComponent(scene.id) + '/' + surface,
      { headers: { Authorization: 'Bearer synthetic-token' } })
    assert.equal(response.status, 200)
    const body = await response.json()
    const items = Array.isArray(body) ? body : body.items
    assert.equal(items.length, locators.length)
    const compact = items.map(({ id, origin, parts }) => ({ id, origin, parts }))
    assert.doesNotMatch(JSON.stringify(compact), /assetId|ob:media|public\.fixture|127\.0\.0\.1|signed-synthetic|opaque-fixture-token|Untrusted platform/)
    for (const {id, parts} of compact) {
      const index = id.endsWith(':1') ? 1 : 0
      assert.equal(parts[index].mediaUrl, '/api/scenes/' + encodeURIComponent(scene.id) + '/messages/' + encodeURIComponent(id) + '/media/' + index)
    }
    rows.push({ surface, items: compact })
  }
  const file = new URL('../internal/app/testdata/snowluma-retained-direct-links-source.json', import.meta.url)
  const output = JSON.stringify({ node: process.version, scene, locators, rows }, null, 2) + '\n'
  if (process.argv.includes('--check')) assert.equal(readFileSync(file, 'utf8'), output)
  else writeFileSync(file, output)
  console.log('PASS actual-source retained direct preview/local history')
} finally {
  engine.dispose()
  store.close()
}
